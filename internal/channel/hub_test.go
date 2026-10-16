package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
)

// reconfChannel counts Run starts and Reconfigure calls.
type reconfChannel struct {
	*channeltest.Channel
	mu      sync.Mutex
	runs    int
	configs []config.Telegram
	state   string
}

func (c *reconfChannel) Run(ctx context.Context, s core.InboundSink) error {
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	return c.Channel.Run(ctx, s)
}

func (c *reconfChannel) Reconfigure(cfg *config.Config) error {
	c.mu.Lock()
	c.configs = append(c.configs, cfg.Channels.Telegram)
	c.mu.Unlock()
	return nil
}

func (c *reconfChannel) Status() core.ChannelStatus {
	st := c.Channel.Status()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "" {
		st.State, st.Detail = c.state, "401 Unauthorized"
	}
	return st
}

func (c *reconfChannel) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs, len(c.configs)
}

func withReconf(rc **reconfChannel) func(*harness) {
	return func(h *harness) {
		h.wrap = func(c *channeltest.Channel) core.Channel {
			*rc = &reconfChannel{Channel: c}
			return *rc
		}
	}
}

func TestHotReloadStartsStopsAndRestartsAdapters(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var rc *reconfChannel
		h := newHarness(t, withReconf(&rc)).start()
		if runs, recs := rc.counts(); runs != 1 || recs != 1 {
			t.Fatalf("after start: runs=%d reconfigures=%d", runs, recs)
		}

		h.cfg.Set(func(c *config.Config) { c.Channels.UsageFooter = true })
		synctest.Wait()
		if runs, _ := rc.counts(); runs != 1 {
			t.Fatal("an unrelated change restarted the adapter")
		}

		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = false })
		synctest.Wait()
		if st := h.ch.Status(); st.State == "connected" {
			t.Fatal("disabling the channel left the adapter running")
		}
		if st := h.hub.Status(); len(st) != 1 || st[0].State != "disabled" || st[0].Enabled {
			t.Fatalf("hub status = %+v", st)
		}
		h.hub.OnMessage(h.ctx, core.Inbound{Chat: h.dmChat(ownerChat), MsgID: "x", SenderID: ownerChat, Text: "hi"})
		synctest.Wait()

		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = true })
		synctest.Wait()
		if st := h.ch.Status(); st.State != "connected" {
			t.Fatal("re-enabling did not start the adapter")
		}

		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Token = "secret:new_bot_token" })
		synctest.Wait()
		runs, recs := rc.counts()
		if runs != 3 || rc.configs[recs-1].Token != "secret:new_bot_token" {
			t.Fatalf("token change: runs=%d configs=%+v; want a reconfigure and a restart", runs, rc.configs)
		}
	})
}

func TestStatusTestAndChecks(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var rc *reconfChannel
		h := newHarness(t, withReconf(&rc))
		sub := h.bus.Subscribe(core.Filter{Admin: true, Types: []core.EventType{core.EvChannelStatus}}, 16)
		defer sub.Close()
		h.start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if st, ok := lastStatus(sub); !ok || st.Name != "telegram" || st.State != "connected" {
			t.Fatalf("last channel.status = %+v (%v)", st, ok)
		}
		if id, err := h.hub.Test(h.ctx, "telegram"); err != nil || id != "@telegram_bot" {
			t.Fatalf("Test = %q, %v", id, err)
		}
		if _, err := h.hub.Test(h.ctx, "matrix"); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("Test(unknown) = %v", err)
		}
		checks := h.hub.Checks()
		if len(checks) != 1 || checks[0].ID() != "channels.auth_failed" || len(checks[0].Run(h.ctx)) != 0 {
			t.Fatal("healthy channel produced findings")
		}
		rc.mu.Lock()
		rc.state = "auth_failed"
		rc.mu.Unlock()
		f := checks[0].Run(h.ctx)
		if len(f) != 1 || f[0].ID != "channels.auth_failed" || f[0].Severity != core.SevMedium || !strings.Contains(f[0].Detail, "401") {
			t.Fatalf("findings = %+v", f)
		}
		if err := checks[0].Fix(h.ctx, f[0]); !errors.Is(err, core.ErrNotImplemented) {
			t.Fatalf("Fix = %v", err)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if st, ok := lastStatus(sub); !ok || st.State != "auth_failed" {
			t.Fatalf("state change event = %+v (%v)", st, ok)
		}
		h.cfg.Set(func(c *config.Config) { c.Channels.Telegram.Enabled = false })
		synctest.Wait()
		if f := checks[0].Run(h.ctx); len(f) != 0 {
			t.Fatalf("disabled channel reported %+v", f)
		}
	})
}

// lastStatus drains sub and returns the newest channel.status, which must be
// an admin broadcast.
func lastStatus(sub core.Subscription) (core.ChannelStatus, bool) {
	var st core.ChannelStatus
	got := false
	for {
		select {
		case e := <-sub.C():
			if e.Principal != "" {
				return core.ChannelStatus{}, false
			}
			st, got = e.Data.(core.ChannelStatusData).Status, true
		default:
			return st, got
		}
	}
}

func TestMessengerSend(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.redactor.Add("k", []byte("hunter2-secret-value"))
		long := strings.Repeat("word ", 2000) + "hunter2-secret-value"
		ref, err := h.hub.Send(h.ctx, h.dmChat(ownerChat), long)
		if err != nil || ref.ID != "1" {
			t.Fatalf("Send = %+v, %v", ref, err)
		}
		msgs := h.visible(ownerChat)
		if len(msgs) != 3 {
			t.Fatalf("10K message sent as %d pieces", len(msgs))
		}
		for _, m := range msgs {
			if ulen(m) > 4096 || strings.Contains(m, "hunter2") {
				t.Fatalf("piece too long or unredacted: %d units", ulen(m))
			}
		}
		if _, err := h.hub.Send(h.ctx, core.ChatRef{Channel: "web", ChatID: "s"}, "x"); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("Send to web = %v", err)
		}

		chat := h.dmChat(ownerChat)
		if _, err := h.hub.Send(h.ctx, chat, "see https://evil.example/x"); err != nil {
			t.Fatal(err)
		}
		if got := h.lastText(ownerChat); got != "see https://evil.example/x" {
			t.Fatalf("clean chat got %q", got)
		}
		sess, err := h.runner.NewSession(h.ctx, h.pid(ownerChat), "telegram", &chat)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.Sessions().SetTaint(h.ctx, sess.ID, core.TaintPrivate|core.TaintUntrusted); err != nil {
			t.Fatal(err)
		}
		if _, err := h.hub.Send(h.ctx, chat, "see https://evil.example/x"); err != nil {
			t.Fatal(err)
		}
		if got := h.lastText(ownerChat); got != "see hxxps://evil[.]example/x" {
			t.Fatalf("exposed chat got %q, want neutralized links", got)
		}
		if _, err := h.hub.Send(h.ctx, chat, "Details: https://[2001:db8:abcd::1]/r?d=SECRET"); err != nil {
			t.Fatal(err)
		}
		if got := h.lastText(ownerChat); got != "Details: hxxps://[2001:db8:abcd::1]/r?d=SECRET" {
			t.Fatalf("exposed chat got %q, want the IPv6 link neutralized", got)
		}
		if _, err := h.hub.Send(h.ctx, h.dmChat(ownerChat), "  "); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("empty Send = %v", err)
		}
	})
}

// TestPrincipalChatAndTeamActive: Bothy's own messages reach a teammate's
// DM (opened when Bothy hasn't seen it) while their verdict is fresh enough,
// a channel's chat while it is on, and never a web account.
func TestPrincipalChatAndTeamActive(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		maya := h.pid(ownerChat)
		if c, ok := h.hub.PrincipalChat(h.ctx, maya); !ok || c != h.dmChat(ownerChat) {
			t.Fatalf("PrincipalChat(teammate) = %+v, %v", c, ok)
		}
		if err := h.hub.TeamActive(h.ctx, maya); err != nil {
			t.Fatalf("TeamActive(teammate) = %v", err)
		}
		// A teammate whose DM Bothy never saw: the DM is opened.
		sam := h.letIn("3003", "Sam", false)
		id := h.ident("3003")
		id.DMChat = ""
		if err := h.store.Identities().Update(h.ctx, id); err != nil {
			t.Fatal(err)
		}
		if c, ok := h.hub.PrincipalChat(h.ctx, sam); !ok || c.ChatID != "3003" || h.ident("3003").DMChat != "3003" {
			t.Fatalf("PrincipalChat(no DM yet) = %+v, %v", c, ok)
		}

		// After five hours with Slack not answering, the verdict is too old.
		h.mc.SetMemberErr(errors.New("slack is down"))
		time.Sleep(5 * time.Hour)
		if _, ok := h.hub.PrincipalChat(h.ctx, maya); ok {
			t.Fatal("a stale verdict still reaches the teammate")
		}
		if err := h.hub.TeamActive(h.ctx, maya); err == nil {
			t.Fatal("a stale verdict that couldn't be renewed is active")
		}
		h.mc.SetMemberErr(nil)
		if err := h.hub.TeamActive(h.ctx, maya); err != nil {
			t.Fatalf("TeamActive renews an old verdict: %v", err)
		}
		h.mc.SetMember(ownerChat, core.Membership{Status: core.MemberOutsider})
		time.Sleep(2 * time.Hour)
		if err := h.hub.TeamActive(h.ctx, maya); err == nil || !strings.Contains(err.Error(), "no longer in the team") {
			t.Fatalf("TeamActive(outsider) = %v", err)
		}

		sp := h.space("C1", true, false)
		if c, ok := h.hub.PrincipalChat(h.ctx, sp.Principal); !ok || c != h.groupChat("C1") {
			t.Fatalf("PrincipalChat(channel) = %+v, %v", c, ok)
		}
		off := h.space("C2", false, false)
		if _, ok := h.hub.PrincipalChat(h.ctx, off.Principal); ok {
			t.Fatal("an off channel gets messages")
		}
		if err := h.hub.TeamActive(h.ctx, off.Principal); err == nil {
			t.Fatal("an off channel is active")
		}
		if _, ok := h.hub.PrincipalChat(h.ctx, core.UserPrincipal(h.owner.ID)); ok {
			t.Fatal("a web account has a chat")
		}
	})
}

func TestNewAndRunGuards(t *testing.T) {
	if _, err := New(Deps{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("New(Deps{}) = %v", err)
	}
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		if err := h.hub.Run(h.ctx); !errors.Is(err, errRunTwice) {
			t.Fatalf("second Run = %v", err)
		}
		extra := channeltest.New("matrix")
		h.hub.Add(extra)
		h.hub.Add(channeltest.New("matrix"))
		synctest.Wait()
		if extra.Status().State != "connected" {
			t.Fatal("an adapter added after Run did not start")
		}
		if got := h.hub.Status(); len(got) != 2 || got[0].Name != "matrix" || got[1].Name != "telegram" {
			t.Fatalf("status = %+v", got)
		}
	})
}

// failingChannel's Run returns at once, as an adapter with a fatal error does.
type failingChannel struct {
	*channeltest.Channel
	mu   sync.Mutex
	runs int
}

func (c *failingChannel) Run(context.Context, core.InboundSink) error {
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	return errors.New("boom")
}

func TestSupervisorRestartsWithBackoff(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var fc *failingChannel
		h := newHarness(t, func(h *harness) {
			h.wrap = func(c *channeltest.Channel) core.Channel {
				fc = &failingChannel{Channel: c}
				return fc
			}
		})
		ctx, cancel := context.WithCancel(h.ctx)
		done := make(chan error, 1)
		go func() { done <- h.hub.Run(ctx) }()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		fc.mu.Lock()
		runs := fc.runs
		fc.mu.Unlock()
		if runs < 3 || runs > 5 {
			t.Fatalf("runs in 10 s = %d, want exponential retries (1+1s+2s+4s)", runs)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
