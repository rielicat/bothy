package channel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
)

// pictureChannel is a chat app whose bot shows a picture or none ("").
type pictureChannel struct {
	*channeltest.Channel
	mu       sync.Mutex
	botID    string
	shown    string // PictureID: "pic<n>" after Bothy's nth change
	idErr    error
	blindSet bool // PictureID fails once a change went through
	unnamed  bool // a change shows as "?", a picture the platform can't name
	setErr   error
	checks   int
	set      []core.Picture
	state    string // replaces the status state when set
}

func (c *pictureChannel) BotID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.botID
}

func (c *pictureChannel) PictureID(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks++
	if c.blindSet && len(c.set) > 0 {
		return "", errors.New("telegram getUserProfilePhotos: 502 Bad Gateway")
	}
	return c.shown, c.idErr
}

func (c *pictureChannel) SetPicture(_ context.Context, p core.Picture) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return c.setErr
	}
	c.set = append(c.set, p)
	c.shown = fmt.Sprintf("pic%d", len(c.set))
	if c.unnamed {
		c.shown = "?"
	}
	return nil
}

// show changes the bot's picture outside Bothy, as BotFather would.
func (c *pictureChannel) show(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shown = id
}

func (c *pictureChannel) Status() core.ChannelStatus {
	st := c.Channel.Status()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "" {
		st.State = c.state
	}
	return st
}

func (c *pictureChannel) counts() (checks int, set []core.Picture) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checks, append([]core.Picture(nil), c.set...)
}

// heldPicture is the picture Bothy holds; set stands for AgentPicture.Set.
type heldPicture struct {
	core.AgentPicture
	mu sync.Mutex
	p  core.Picture
}

func (f *heldPicture) Current(context.Context) (core.Picture, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.p, nil
}

func (f *heldPicture) set(p core.Picture) core.Picture {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.p = p
	return p
}

var (
	defaultPic = core.Picture{PNG: []byte("default"), JPEG: []byte("default.jpg"), Version: "d1"}
	newerPic   = core.Picture{PNG: []byte("default2"), JPEG: []byte("default2.jpg"), Version: "d2"}
	customPic  = core.Picture{PNG: []byte("custom"), JPEG: []byte("custom.jpg"), Custom: true, Version: "c1"}
)

// withPicture starts the bot showing shown ("" for no picture).
func withPicture(pc **pictureChannel, shown string) func(*harness) {
	return func(h *harness) {
		h.picture = &heldPicture{p: defaultPic}
		h.wrap = func(c *channeltest.Channel) core.Channel {
			*pc = &pictureChannel{Channel: c, botID: "1001", shown: shown}
			return *pc
		}
	}
}

func (h *harness) pictureMeta(t *testing.T) string {
	t.Helper()
	v, err := h.store.Meta().Get(h.ctx, pictureKey("telegram"))
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	return v
}

// reconnect turns the chat app off, runs between, and turns it on again.
func (h *harness) reconnect(between func()) {
	h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = false })
	settle()
	between()
	h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = true })
	settle()
}

// settle lets the hub poll statuses and finish what a connection starts.
func settle() {
	time.Sleep(5 * time.Second)
	synctest.Wait()
}

func TestFirstConnectionGivesTheBotBothysPicture(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "")).start()
		settle()
		checks, set := pc.counts()
		if checks != 2 || len(set) != 1 || set[0].Version != defaultPic.Version {
			t.Fatalf("checks=%d set=%+v", checks, set)
		}
		if v := h.pictureMeta(t); v != "1001 d1 pic1" {
			t.Fatalf("meta = %q", v)
		}

		// Reconnecting, or the next boot, doesn't ask again.
		h.reconnect(func() {})
		if checks, set := pc.counts(); checks != 2 || len(set) != 1 {
			t.Fatalf("after reconnecting: checks=%d sets=%d", checks, len(set))
		}
	})
}

func TestFirstConnectionKeepsAPictureSomeoneSet(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "own")).start()
		settle()
		if checks, set := pc.counts(); checks != 1 || len(set) != 0 {
			t.Fatalf("checks=%d set=%+v", checks, set)
		}
		if v := h.pictureMeta(t); v != "1001" {
			t.Fatalf("meta = %q", v)
		}
		// A new built-in default leaves it alone too.
		h.reconnect(func() { h.picture.(*heldPicture).set(newerPic) })
		if checks, set := pc.counts(); checks != 1 || len(set) != 0 {
			t.Fatalf("after a new default: checks=%d set=%+v", checks, set)
		}
	})
}

// A bot still showing the picture Bothy gave it follows Bothy's: a new
// built-in default after an upgrade, or an admin's change made while the
// chat app was off.
func TestABotKeepsUpWithBothysPicture(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "")).start()
		settle()
		held := h.picture.(*heldPicture)
		h.reconnect(func() { held.set(newerPic) })
		if _, set := pc.counts(); len(set) != 2 || set[1].Version != newerPic.Version {
			t.Fatalf("after a new default: %+v", set)
		}
		if v := h.pictureMeta(t); v != "1001 d2 pic2" {
			t.Fatalf("meta = %q", v)
		}
		h.reconnect(func() { held.set(customPic) })
		if _, set := pc.counts(); len(set) != 3 || set[2].Version != customPic.Version {
			t.Fatalf("after a change while off: %+v", set)
		}
		if v := h.pictureMeta(t); v != "1001 c1 pic3" {
			t.Fatalf("meta = %q", v)
		}
	})
}

// Someone changing the picture Bothy gave makes it theirs: Bothy's next
// picture doesn't replace it.
func TestAPictureSomeoneSetOverBothysStays(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "")).start()
		settle()
		h.reconnect(func() {
			pc.show("own")
			h.picture.(*heldPicture).set(newerPic)
		})
		if _, set := pc.counts(); len(set) != 1 {
			t.Fatalf("their picture was replaced: %+v", set)
		}
		if v := h.pictureMeta(t); v != "1001" {
			t.Fatalf("meta = %q", v)
		}
	})
}

// A bot looked after before Bothy kept track of what it gave keeps its
// picture: Bothy can't tell whose it is.
func TestABotFromBeforeTrackingIsLeftAlone(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "pic-old"))
		if err := h.store.Meta().Set(h.ctx, pictureKey("telegram"), "1001"); err != nil {
			t.Fatal(err)
		}
		h.picture.(*heldPicture).set(newerPic)
		h.start()
		settle()
		if checks, set := pc.counts(); checks != 0 || len(set) != 0 {
			t.Fatalf("checks=%d set=%+v", checks, set)
		}
	})
}

// When the chat app won't say what it shows after a change, or can't name
// it, the picture counts as the bot's own: Bothy never replaces what it
// can't recognise.
func TestAPictureBothyCantRecogniseCountsAsTheBots(t *testing.T) {
	for _, unnamed := range []bool{false, true} {
		bubble(t, func(t *testing.T) {
			var pc *pictureChannel
			h := newHarness(t, withPicture(&pc, ""))
			pc.blindSet, pc.unnamed = !unnamed, unnamed
			h.start()
			settle()
			if _, set := pc.counts(); len(set) != 1 {
				t.Fatalf("unnamed=%v: set = %+v", unnamed, set)
			}
			if v := h.pictureMeta(t); v != "1001" {
				t.Fatalf("unnamed=%v: meta = %q", unnamed, v)
			}
		})
	}
}

// Another bot behind the same chat app (a new token) is checked afresh; a
// check that failed is tried again on the next connection.
func TestFirstConnectionChecksANewBotAndRetries(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, ""))
		if err := h.store.Meta().Set(h.ctx, pictureKey("telegram"), "1000"); err != nil {
			t.Fatal(err)
		}
		pc.idErr = errors.New("telegram getUserProfilePhotos: 502 Bad Gateway")
		h.start()
		settle()
		if checks, set := pc.counts(); checks != 1 || len(set) != 0 {
			t.Fatalf("checks=%d sets=%d", checks, len(set))
		}
		if v := h.pictureMeta(t); v != "1000" {
			t.Fatalf("a failed check was remembered: %q", v)
		}
		h.reconnect(func() {
			pc.mu.Lock()
			pc.idErr = nil
			pc.mu.Unlock()
		})
		if checks, set := pc.counts(); checks != 3 || len(set) != 1 {
			t.Fatalf("retry: checks=%d sets=%d", checks, len(set))
		}
	})
}

// Discord bot names repeat, so a new bot under the same name is checked too.
func TestFirstConnectionTellsBotsApartByID(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "own")).start()
		settle()
		name := pc.Status().Identity
		h.reconnect(func() {
			pc.mu.Lock()
			pc.botID, pc.shown = "1002", ""
			pc.mu.Unlock()
		})
		if checks, set := pc.counts(); checks != 3 || len(set) != 1 || pc.Status().Identity != name {
			t.Fatalf("checks=%d sets=%d identity %q, was %q", checks, len(set), pc.Status().Identity, name)
		}
		if v := h.pictureMeta(t); v != "1002 d1 pic1" {
			t.Fatalf("meta = %q", v)
		}
	})
}

func TestApplyPictureReplacesWhateverTheBotHas(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "own"))
		slack := channeltest.New("slack")
		h.hub.Add(slack)
		h.hub.Add(channeltest.New("discord")) // turned off: not listed
		h.cfg.Set(func(c *config.Config) { c.Channels.Slack.Enabled = true })
		h.start()
		settle()

		held := h.picture.(*heldPicture)
		got := h.hub.ApplyPicture(h.ctx, held.set(customPic))
		if len(got) != 2 || got[0].App != "slack" || got[0].Outcome != core.PictureManual ||
			got[1].App != "telegram" || got[1].Outcome != core.PictureUpdated || got[1].Err != nil {
			t.Fatalf("results = %+v", got)
		}
		if _, set := pc.counts(); len(set) != 1 || set[0].Version != customPic.Version {
			t.Fatalf("set = %+v", set)
		}
		if v := h.pictureMeta(t); v != "1001 c1 pic1" {
			t.Fatalf("meta = %q", v)
		}

		pc.mu.Lock()
		pc.setErr = rateLimited{}
		pc.mu.Unlock()
		got = h.hub.ApplyPicture(h.ctx, held.set(defaultPic))
		if got[1].Outcome != core.PictureFailed || core.CodeOf(got[1].Err) != core.FailRateLimited {
			t.Fatalf("rate limited: %+v", got[1])
		}

		pc.mu.Lock()
		pc.setErr, pc.state = nil, "backoff"
		pc.mu.Unlock()
		got = h.hub.ApplyPicture(h.ctx, defaultPic)
		if got[1].Outcome != core.PictureFailed || !errors.Is(got[1].Err, core.ErrNotConnected) {
			t.Fatalf("not connected: %+v", got[1])
		}
		if _, set := pc.counts(); len(set) != 1 {
			t.Fatalf("a disconnected bot was changed: %d sets", len(set))
		}
	})
}

// Of two changes at once, the one whose turn comes last may carry the older
// picture; the bot still ends up with the one Bothy holds.
func TestApplyPictureSendsTheLatestPicture(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, "own")).start()
		settle()
		h.picture.(*heldPicture).set(defaultPic) // a reset came after this upload
		got := h.hub.ApplyPicture(h.ctx, customPic)
		if got[0].Outcome != core.PictureUpdated {
			t.Fatalf("results = %+v", got)
		}
		if _, set := pc.counts(); len(set) != 1 || set[0].Version != defaultPic.Version {
			t.Fatalf("set = %+v", set)
		}
	})
}

type rateLimited struct{}

func (rateLimited) Error() string                 { return "429 Too Many Requests" }
func (rateLimited) FailureCode() core.FailureCode { return core.FailRateLimited }
