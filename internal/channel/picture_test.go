package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
)

// pictureChannel is a chat app whose bot has a picture or not.
type pictureChannel struct {
	*channeltest.Channel
	mu     sync.Mutex
	botID  string
	has    bool
	hasErr error
	setErr error
	checks int
	set    []core.Picture
	state  string // replaces the status state when set
}

func (c *pictureChannel) BotID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.botID
}

func (c *pictureChannel) HasPicture(context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks++
	return c.has, c.hasErr
}

func (c *pictureChannel) SetPicture(_ context.Context, p core.Picture) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return c.setErr
	}
	c.set, c.has = append(c.set, p), true
	return nil
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
	customPic  = core.Picture{PNG: []byte("custom"), JPEG: []byte("custom.jpg"), Custom: true, Version: "c1"}
)

func withPicture(pc **pictureChannel, has bool) func(*harness) {
	return func(h *harness) {
		h.picture = &heldPicture{p: defaultPic}
		h.wrap = func(c *channeltest.Channel) core.Channel {
			*pc = &pictureChannel{Channel: c, botID: "1001", has: has}
			return *pc
		}
	}
}

// settle lets the hub poll statuses and finish what a connection starts.
func settle() {
	time.Sleep(5 * time.Second)
	synctest.Wait()
}

func TestFirstConnectionGivesTheBotBothysPicture(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, false)).start()
		settle()
		checks, set := pc.counts()
		if checks != 1 || len(set) != 1 || set[0].Version != defaultPic.Version {
			t.Fatalf("checks=%d set=%+v", checks, set)
		}
		if v, err := h.store.Meta().Get(h.ctx, pictureKey("telegram")); err != nil || v != "1001" {
			t.Fatalf("meta = %q, %v", v, err)
		}

		// Reconnecting, or the next boot, doesn't ask again.
		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = false })
		settle()
		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = true })
		settle()
		if checks, set := pc.counts(); checks != 1 || len(set) != 1 {
			t.Fatalf("after reconnecting: checks=%d sets=%d", checks, len(set))
		}
	})
}

func TestFirstConnectionKeepsAPictureSomeoneSet(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, true)).start()
		settle()
		if checks, set := pc.counts(); checks != 1 || len(set) != 0 {
			t.Fatalf("checks=%d set=%+v", checks, set)
		}
		if v, _ := h.store.Meta().Get(h.ctx, pictureKey("telegram")); v != "1001" {
			t.Fatalf("meta = %q", v)
		}
	})
}

// Another bot behind the same chat app (a new token) is checked afresh; a
// check that failed is tried again on the next connection.
func TestFirstConnectionChecksANewBotAndRetries(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, false))
		if err := h.store.Meta().Set(h.ctx, pictureKey("telegram"), "1000"); err != nil {
			t.Fatal(err)
		}
		pc.hasErr = errors.New("telegram getUserProfilePhotos: 502 Bad Gateway")
		h.start()
		settle()
		if checks, set := pc.counts(); checks != 1 || len(set) != 0 {
			t.Fatalf("checks=%d sets=%d", checks, len(set))
		}
		if v, _ := h.store.Meta().Get(h.ctx, pictureKey("telegram")); v != "1000" {
			t.Fatalf("a failed check was remembered: %q", v)
		}
		pc.mu.Lock()
		pc.hasErr = nil
		pc.mu.Unlock()
		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = false })
		settle()
		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = true })
		settle()
		if checks, set := pc.counts(); checks != 2 || len(set) != 1 {
			t.Fatalf("retry: checks=%d sets=%d", checks, len(set))
		}
	})
}

// Discord bot names repeat, so a new bot under the same name is checked too.
func TestFirstConnectionTellsBotsApartByID(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, true)).start()
		settle()
		name := pc.Status().Identity
		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = false })
		settle()
		pc.mu.Lock()
		pc.botID, pc.has = "1002", false
		pc.mu.Unlock()
		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = true })
		settle()
		if checks, set := pc.counts(); checks != 2 || len(set) != 1 || pc.Status().Identity != name {
			t.Fatalf("checks=%d sets=%d identity %q, was %q", checks, len(set), pc.Status().Identity, name)
		}
		if v, _ := h.store.Meta().Get(h.ctx, pictureKey("telegram")); v != "1002" {
			t.Fatalf("meta = %q", v)
		}
	})
}

func TestApplyPictureReplacesWhateverTheBotHas(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pc *pictureChannel
		h := newHarness(t, withPicture(&pc, true))
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
		h := newHarness(t, withPicture(&pc, true)).start()
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
