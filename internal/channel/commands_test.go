package channel

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestParseCommand(t *testing.T) {
	for _, tc := range []struct {
		in, name, args string
		ok             bool
	}{
		{"/stop", "stop", "", true},
		{"  /STOP@bothy_bot  ", "stop", "", true},
		{"/pin don't touch prod\never", "pin", "don't touch prod\never", true},
		{"/start 7KQ2-MX4D", "start", "7KQ2-MX4D", true},
		{"/etc/hosts is broken", "", "", false},
		{"/unknown thing", "", "", false},
		{"stop", "", "", false},
		{"/", "", "", false},
	} {
		name, args, ok := parseCommand(tc.in)
		if name != tc.name || args != tc.args || ok != tc.ok {
			t.Errorf("parseCommand(%q) = %q, %q, %v", tc.in, name, args, ok)
		}
	}
}

func TestCommandsManageSession(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		p := core.UserPrincipal(h.owner.ID)
		chat := h.dmChat(ownerChat)

		h.dm(ownerChat, "/pin never deploy on Fridays")
		h.dm(ownerChat, "/pin answer in **English**")
		h.dm(ownerChat, "/pins")
		h.wantVisible(ownerChat, "1. never deploy on Fridays")
		h.wantVisible(ownerChat, `2. answer in \*\*English\*\*`)
		h.dm(ownerChat, "/unpin 1")
		pins, _ := h.store.Pins().List(h.ctx, core.SystemViewer(), h.session(p, chat).ID)
		if len(pins) != 1 || pins[0].Text != "answer in **English**" {
			t.Fatalf("pins after /unpin 1 = %+v", pins)
		}
		h.dm(ownerChat, "/unpin 9")
		h.wantVisible(ownerChat, "There is no pin 9.")

		h.dm(ownerChat, "/model anthropic/claude-sonnet-4-6")
		if s := h.session(p, chat); s.ModelOverride != "anthropic/claude-sonnet-4-6" {
			t.Fatalf("override = %q", s.ModelOverride)
		}
		h.dm(ownerChat, "/model not-a-ref")
		h.wantVisible(ownerChat, "Usage: /model")
		h.dm(ownerChat, "/model")
		if s := h.session(p, chat); s.ModelOverride != "" {
			t.Fatalf("override not cleared: %q", s.ModelOverride)
		}

		old := h.session(p, chat).ID
		h.dm(ownerChat, "/new")
		h.wantVisible(ownerChat, "Started a fresh conversation.")
		if _, err := h.store.Sessions().ByChannelKey(h.ctx, p, chat.Key()); err == nil {
			t.Fatal("/new left the session open")
		}
		h.dm(ownerChat, "hello again")
		if s := h.runner.Submitted(); s[len(s)-1].Session == old {
			t.Fatal("the next message went to the archived session")
		}
		h.dm(ownerChat, "/compact")
		h.wantVisible(ownerChat, "Compacted.")
		if n := len(h.runner.Submitted()); n != 1 {
			t.Fatalf("commands reached the runner as messages: %d submits", n)
		}
	})
}

func TestStatusUsageHelp(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.dm(ownerChat, "hi")
		sess := h.session(core.UserPrincipal(h.owner.ID), h.dmChat(ownerChat))
		run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: sess.Principal,
			User: h.owner.ID, Trigger: core.TrigMessage, Status: core.RunDone, Cost: core.MicrosFromUSD(0.0123), CreatedAt: time.Now()}
		if err := h.store.Runs().Create(h.ctx, run); err != nil {
			t.Fatal(err)
		}
		h.dm(ownerChat, "/usage")
		h.wantVisible(ownerChat, "This conversation: $0.0123.")
		h.dm(ownerChat, "/status")
		h.wantVisible(ownerChat, "No task running.")
		h.wantVisible(ownerChat, "Sandbox: fake (fake).")
		h.dm(ownerChat, "/help")
		h.wantVisible(ownerChat, "/stop · stop the current task")
	})
}

func TestCommandAuthorization(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.space("-500", true, false)
		h.group("-500", memberChat, "/stop", true)
		h.wantVisible("-500", "Not allowed")
		h.group("-500", "9009", "/new", true)
		if n := len(h.runner.Stopped()); n != 0 {
			t.Fatalf("a non-owner stopped a run")
		}
		h.dm(memberChat, "/stopall")
		h.wantVisible(memberChat, "Not allowed")
		if h.runner.Paused() {
			t.Fatal("a member engaged the kill switch")
		}

		h.dm(ownerChat, "/stopall")
		h.wantVisible(ownerChat, "Paused.")
		if !h.runner.Paused() || h.runner.StopAlls != 1 {
			t.Fatal("/stopall did not pause and stop everything")
		}
		h.dm(ownerChat, "/resume")
		h.wantVisible(ownerChat, "Resumed.")
		if h.runner.Paused() {
			t.Fatal("/resume left the kill switch on")
		}
		if got := h.audit.Actions(); !slices.Equal(got, []string{"admin.pause", "admin.resume"}) {
			t.Fatalf("audit = %v", got)
		}
		if e := h.audit.All()[0]; e.Actor != "channel:telegram:"+ownerChat {
			t.Fatalf("audit actor = %q", e.Actor)
		}

		h.group("-500", ownerChat, "/stop", true)
		h.wantVisible("-500", "Nothing to stop.")
		if v := h.visible("-500"); strings.Count(strings.Join(v, "\n"), "Not allowed") != 2 {
			t.Fatalf("group replies = %q", v)
		}
	})
}

func TestStopButton(t *testing.T) {
	bubble(t, func(t *testing.T) {
		run := core.RunID(core.NewID(core.PrefixRun))
		sess := core.SessionID(core.NewID(core.PrefixSession))
		h := newHarness(t, func(h *harness) {
			h.runner2 = &activeRunner{Runner: h.runner, info: core.RunInfo{Run: run, Session: sess,
				Principal: core.UserPrincipal(h.owner.ID), Status: core.RunRunning}}
		}).start()
		ref, _ := h.ch.Send(h.ctx, core.OutMessage{Chat: h.dmChat(ownerChat), Text: "Thinking…"})
		h.ch.Press(ref, memberChat, "s:"+string(run))
		synctest.Wait()
		h.ch.Press(ref, ownerChat, "s:"+string(run))
		synctest.Wait()
		if got := h.ch.AckToasts(); !slices.Equal(got, []string{"Not allowed.", "Stopped."}) {
			t.Fatalf("toasts = %q", got)
		}
		if got := h.runner.Stopped(); len(got) != 1 || got[0] != sess {
			t.Fatalf("stops = %v", got)
		}
		h.ch.Press(ref, ownerChat, "s:not-a-run")
		synctest.Wait()
		if got := h.ch.AckToasts(); got[len(got)-1] != "This button is not valid." {
			t.Fatalf("toasts = %q", got)
		}
	})
}

// /compact waits behind the session's active run; meanwhile the chat keeps
// flowing, so a steer sent after it reaches the runner at once.
func TestCompactDoesNotHoldTheChat(t *testing.T) {
	bubble(t, func(t *testing.T) {
		cr := &compactRunner{release: make(chan struct{})}
		h := newHarness(t, func(h *harness) {
			cr.Runner = h.runner
			h.runner2 = cr
		}).start()
		h.dm(ownerChat, "run the test suite")
		h.dm(ownerChat, "/compact")
		h.dm(ownerChat, "actually, skip the tests")
		if subs := h.runner.Submitted(); len(subs) != 2 || subs[1].Input[0].Text != "actually, skip the tests" {
			t.Fatalf("submits while compacting = %+v", subs)
		}
		h.dm(ownerChat, "/compact")
		h.wantVisible(ownerChat, "Already compacting this conversation.")
		if n := cr.calls.Load(); n != 1 {
			t.Fatalf("compactions started = %d, want 1", n)
		}
		close(cr.release)
		synctest.Wait()
		h.wantVisible(ownerChat, "Compacted.")
		h.dm(ownerChat, "/compact")
		if n := cr.calls.Load(); n != 2 {
			t.Fatalf("compactions after the first finished = %d, want 2", n)
		}
	})
}

// compactRunner's Compact blocks until release, like the runner's does while
// the session has an active run.
type compactRunner struct {
	*fakes.Runner
	release chan struct{}
	calls   atomic.Int32
}

func (c *compactRunner) Compact(ctx context.Context, _ core.Viewer, _ core.SessionID) error {
	c.calls.Add(1)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type activeRunner struct {
	*fakes.Runner
	info core.RunInfo
}

func (a *activeRunner) Active() []core.RunInfo { return []core.RunInfo{a.info} }
