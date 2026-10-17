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
	has    bool
	hasErr error
	setErr error
	checks int
	set    []core.Picture
	state  string // replaces the status state when set
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

type fixedPicture struct {
	core.AgentPicture
	p core.Picture
}

func (f fixedPicture) Current(context.Context) (core.Picture, error) { return f.p, nil }

var (
	defaultPic = core.Picture{PNG: []byte("default"), JPEG: []byte("default.jpg"), Version: "d1"}
	customPic  = core.Picture{PNG: []byte("custom"), JPEG: []byte("custom.jpg"), Custom: true, Version: "c1"}
)

func withPicture(pc **pictureChannel, has bool) func(*harness) {
	return func(h *harness) {
		h.picture = fixedPicture{p: defaultPic}
		h.wrap = func(c *channeltest.Channel) core.Channel {
			*pc = &pictureChannel{Channel: c, has: has}
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
		if v, err := h.store.Meta().Get(h.ctx, pictureKey("telegram")); err != nil || v != "@telegram_bot" {
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
		if v, _ := h.store.Meta().Get(h.ctx, pictureKey("telegram")); v != "@telegram_bot" {
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
		if err := h.store.Meta().Set(h.ctx, pictureKey("telegram"), "@old_bot"); err != nil {
			t.Fatal(err)
		}
		pc.hasErr = errors.New("telegram getUserProfilePhotos: 502 Bad Gateway")
		h.start()
		settle()
		if checks, set := pc.counts(); checks != 1 || len(set) != 0 {
			t.Fatalf("checks=%d sets=%d", checks, len(set))
		}
		if v, _ := h.store.Meta().Get(h.ctx, pictureKey("telegram")); v != "@old_bot" {
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

		got := h.hub.ApplyPicture(h.ctx, customPic)
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
		got = h.hub.ApplyPicture(h.ctx, defaultPic)
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

type rateLimited struct{}

func (rateLimited) Error() string                 { return "429 Too Many Requests" }
func (rateLimited) FailureCode() core.FailureCode { return core.FailRateLimited }
