package channel

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
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
		h.cfg.Set(func(c *config.Config) { c.Models.Main = []string{"openrouter/qwen/qwen3-coder"} })
		h.dm(ownerChat, "/model not-a-ref")
		h.wantVisible(ownerChat, "Usage: /model <model name>, for example `/model openrouter/qwen/qwen3-coder`.")
		// Checking the model must not change it; only an explicit word clears it.
		h.dm(ownerChat, "/model")
		h.wantVisible(ownerChat, "This chat uses `anthropic/claude-sonnet-4-6`. /model default goes back to the main model.")
		if s := h.session(p, chat); s.ModelOverride != "anthropic/claude-sonnet-4-6" {
			t.Fatalf("bare /model changed the override to %q", s.ModelOverride)
		}
		h.dm(ownerChat, "/model default")
		h.wantVisible(ownerChat, "This chat uses the main model again.")
		if s := h.session(p, chat); s.ModelOverride != "" {
			t.Fatalf("override not cleared: %q", s.ModelOverride)
		}
		h.dm(ownerChat, "/model")
		if got := h.lastText(ownerChat); got != "This chat uses the main model, `openrouter/qwen/qwen3-coder`. /model <model name> changes it." {
			t.Fatalf("/model without an override = %q", got)
		}

		old := h.session(p, chat).ID
		h.dm(ownerChat, "/new")
		h.wantVisible(ownerChat, "Started a fresh conversation. The previous one is archived; Activity in the web UI still opens it.")
		if _, err := h.store.Sessions().ByChannelKey(h.ctx, p, chat.Key()); err == nil {
			t.Fatal("/new left the session open")
		}
		h.dm(ownerChat, "hello again")
		if s := h.runner.Submitted(); s[len(s)-1].Session == old {
			t.Fatal("the next message went to the archived session")
		}
		h.dm(ownerChat, "/compact")
		h.wantVisible(ownerChat, "Older messages were summarised.")
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
		h.wantVisible(ownerChat, "Today: $0.00, no daily limit. This conversation: $0.01.")
		h.dm(ownerChat, "/status")
		if got := h.lastText(ownerChat); got != "No task running.\nToday: $0.00, no daily limit." {
			t.Fatalf("/status = %q", got)
		}
		h.sandbox.SetStatus(core.SandboxStatus{Backend: "none", Reason: "the sandbox self-test has not finished yet"})
		if err := h.store.Meta().Set(h.ctx, core.RefineMetaKey(sess.ID), core.RefineOn); err != nil {
			t.Fatal(err)
		}
		h.dm(ownerChat, "/status")
		want := "No task running.\nToday: $0.00, no daily limit.\n" +
			"Refine is on for this conversation, so answers are slower and cost more. /refine off turns it off.\n" +
			"Running code is off on this server."
		if got := h.lastText(ownerChat); got != want {
			t.Fatalf("/status = %q, want %q", got, want)
		}
		h.dm(ownerChat, "/help")
		h.wantVisible(ownerChat, "/stop · stop the current task")
		if got := h.lastText(ownerChat); !strings.Contains(got, "/stopall") {
			t.Fatalf("an admin's /help lacks /stopall: %q", got)
		}
		h.dm(memberChat, "/help")
		if got := h.lastText(memberChat); strings.Contains(got, "/stopall") || strings.Contains(got, "/resume") || !strings.Contains(got, "/new") {
			t.Fatalf("a member's /help = %q; want no admin-only commands", got)
		}
	})
}

func TestStatusNamesTheTask(t *testing.T) {
	bubble(t, func(t *testing.T) {
		ar := &activeRunner{}
		h := newHarness(t, func(h *harness) {
			ar.Runner = h.runner
			h.runner2 = ar
		}).start()
		h.dm(ownerChat, "hi")
		sess := h.session(core.UserPrincipal(h.owner.ID), h.dmChat(ownerChat)).ID
		ar.info = core.RunInfo{Session: sess, Status: core.RunWaitingApproval, Started: time.Now().Add(-130 * time.Second), Queued: 2}
		h.dm(ownerChat, "/status")
		h.wantVisible(ownerChat, "Waiting for approval for 2 min 10 s. 2 more messages are waiting.")
	})
}

func TestMoneyAndDurationText(t *testing.T) {
	for m, want := range map[core.Micros]string{
		0: "$0.00", core.MicrosFromUSD(0.0042): "<$0.01", core.MicrosFromUSD(0.8745): "$0.87", core.MicrosFromUSD(2): "$2.00",
		core.MicrosFromUSD(999.996): "$1,000", core.MicrosFromUSD(1240.4): "$1,240", core.MicrosFromUSD(1234567): "$1,234,567",
	} {
		if got := moneyText(m); got != want {
			t.Errorf("moneyText(%d) = %q, want %q", m, got, want)
		}
	}
	if got := spendText(core.MicrosFromUSD(0.8745), core.MicrosFromUSD(2)); got != "$0.87 of the $2.00 daily limit" {
		t.Errorf("spendText = %q", got)
	}
	for d, want := range map[time.Duration]string{
		0: "1 s", 3 * time.Second: "3 s", 2 * time.Minute: "2 min", 130 * time.Second: "2 min 10 s", 65 * time.Minute: "1 h 5 min", 2 * time.Hour: "2 h",
	} {
		if got := durationText(d); got != want {
			t.Errorf("durationText(%v) = %q, want %q", d, got, want)
		}
	}
}

// A lone mistyped command gets a short reply instead of a model run; a path
// or a forwarded message still reaches the agent.
func TestUnknownCommandIsNotAModelRun(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.dm(ownerChat, "/foo")
		h.wantVisible(ownerChat, "There's no /foo command. /help lists the commands.")
		h.dm(ownerChat, "/stat_us@bothy_bot")
		h.wantVisible(ownerChat, `There's no /stat\_us command.`)
		if n := len(h.runner.Submitted()); n != 0 {
			t.Fatalf("a mistyped command started %d runs", n)
		}
		h.dm(ownerChat, "/etc/hosts")
		h.dm(ownerChat, "/foo bar")
		h.inject(core.Inbound{Chat: h.dmChat(ownerChat), SenderID: ownerChat, Text: "/foo", Mentioned: true, Forwarded: true})
		if n := len(h.runner.Submitted()); n != 3 {
			t.Fatalf("submits = %d; want the path, the command with words and the forwarded message", n)
		}
	})
}

func TestCommandAuthorization(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.space("-500", true, false)
		h.group("-500", memberChat, "/stop", true)
		h.wantVisible("-500", "Only the person this chat belongs to or an admin can use /stop.")
		h.group("-500", "9009", "/new", true)
		if n := len(h.runner.Stopped()); n != 0 {
			t.Fatalf("a non-owner stopped a run")
		}
		h.dm(memberChat, "/stopall")
		h.wantVisible(memberChat, "Only an admin can use /stopall.")
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
		if v := h.visible("-500"); strings.Count(strings.Join(v, "\n"), "Only the person this chat belongs to") != 2 {
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
		ref, _ := h.ch.Send(h.ctx, core.OutMessage{Chat: h.dmChat(ownerChat), Text: "Working…"})
		h.ch.Press(ref, memberChat, "s:"+string(run))
		synctest.Wait()
		h.ch.Press(ref, ownerChat, "s:"+string(run))
		synctest.Wait()
		if got := h.ch.AckToasts(); !slices.Equal(got, []string{"Only the person this chat belongs to or an admin can stop it.", "Stopped."}) {
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
		h.wantVisible(ownerChat, "Already summarising older messages in this chat.")
		if n := cr.calls.Load(); n != 1 {
			t.Fatalf("compactions started = %d, want 1", n)
		}
		close(cr.release)
		synctest.Wait()
		h.wantVisible(ownerChat, "Older messages were summarised.")
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

// /model names models in plain words and finds them by plain or short name;
// a provider/model ref still works.
func TestModelCommandUsesPlainNames(t *testing.T) {
	bubble(t, func(t *testing.T) {
		sonnet := core.ModelInfo{Ref: core.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}, DisplayName: "Claude Sonnet 4.6", ShortName: "Sonnet 4.6"}
		haiku := core.ModelInfo{Ref: core.ModelRef{Provider: "anthropic", Model: "claude-haiku-4-5"}, DisplayName: "Claude Haiku 4.5", ShortName: "Haiku 4.5"}
		h := newHarness(t, func(h *harness) { h.llm = namedModels{models: []core.ModelInfo{sonnet, haiku}} }).start()
		h.cfg.Set(func(c *config.Config) { c.Models.Main = []string{"anthropic/claude-sonnet-4-6"} })
		p, chat := core.UserPrincipal(h.owner.ID), h.dmChat(ownerChat)
		override := func() string { return h.session(p, chat).ModelOverride }

		h.dm(ownerChat, "/model")
		if got := h.lastText(ownerChat); got != "This chat uses the main model, Claude Sonnet 4.6. /model <model name> changes it." {
			t.Fatalf("/model = %q", got)
		}
		h.dm(ownerChat, "/model haiku  4.5")
		if got := h.lastText(ownerChat); got != "This chat now uses Claude Haiku 4.5." || override() != "anthropic/claude-haiku-4-5" {
			t.Fatalf("/model by short name = %q, override %q", got, override())
		}
		h.dm(ownerChat, "/model claude sonnet 4.6")
		h.dm(ownerChat, "/model")
		if got := h.lastText(ownerChat); got != "This chat uses Claude Sonnet 4.6. /model default goes back to the main model." || override() != "anthropic/claude-sonnet-4-6" {
			t.Fatalf("/model after choosing by name = %q, override %q", got, override())
		}
		h.dm(ownerChat, "/model anthropic/claude-haiku-4-5")
		if got := h.lastText(ownerChat); got != "This chat now uses Claude Haiku 4.5." {
			t.Fatalf("/model by ref = %q", got)
		}
		h.dm(ownerChat, "/model gpt nine")
		want := "Usage: /model <model name>, for example `/model Claude Sonnet 4.6`. /model alone shows the model; /model default goes back to the main model."
		if got := h.lastText(ownerChat); got != want || override() != "anthropic/claude-haiku-4-5" {
			t.Fatalf("/model with an unknown name = %q, override %q", got, override())
		}
	})
}

// Names several models share never choose one silently.
func TestModelCommandSharedNames(t *testing.T) {
	bubble(t, func(t *testing.T) {
		us := core.ModelInfo{Ref: core.ModelRef{Provider: "bedrock", Model: "us.anthropic.claude-sonnet-4-6"}, DisplayName: "Claude Sonnet 4.6 (US)", ShortName: "Sonnet 4.6"}
		global := core.ModelInfo{Ref: core.ModelRef{Provider: "bedrock", Model: "global.anthropic.claude-sonnet-4-6"}, DisplayName: "Claude Sonnet 4.6 (Global)", ShortName: "Sonnet 4.6"}
		direct := core.ModelInfo{Ref: core.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}, DisplayName: "Claude Sonnet 4.6", ShortName: "Sonnet 4.6"}
		h := newHarness(t, func(h *harness) { h.llm = namedModels{models: []core.ModelInfo{direct, global, us}} }).start()
		h.cfg.Set(func(c *config.Config) { c.Models.Main = []string{"bedrock/us.anthropic.claude-sonnet-4-6"} })
		p, chat := core.UserPrincipal(h.owner.ID), h.dmChat(ownerChat)
		override := func() string { return h.session(p, chat).ModelOverride }

		// The main model is named with the region that sets it apart, and
		// that name chooses it.
		h.dm(ownerChat, "/model")
		if got := h.lastText(ownerChat); got != "This chat uses the main model, Claude Sonnet 4.6 (US). /model <model name> changes it." {
			t.Fatalf("/model = %q", got)
		}
		// A short name several share goes to the one already in use.
		h.dm(ownerChat, "/model sonnet 4.6")
		if got := h.lastText(ownerChat); got != "This chat now uses Claude Sonnet 4.6 (US)." || override() != "bedrock/us.anthropic.claude-sonnet-4-6" {
			t.Fatalf("/model sonnet 4.6 = %q, override %q", got, override())
		}
		// With none of them in use, Bothy asks which one.
		h.cfg.Set(func(c *config.Config) { c.Models.Main = []string{"openai/gpt-6"} })
		h.dm(ownerChat, "/model sonnet 4.6")
		got := h.lastText(ownerChat)
		if !strings.HasPrefix(got, "That name matches several models: ") || !strings.Contains(got, "Claude Sonnet 4.6 (Global) (`bedrock/global.anthropic.claude-sonnet-4-6`)") ||
			!strings.Contains(got, "Claude Sonnet 4.6 (`anthropic/claude-sonnet-4-6`)") || override() != "bedrock/us.anthropic.claude-sonnet-4-6" {
			t.Fatalf("/model with a shared name = %q, override %q", got, override())
		}
		h.dm(ownerChat, "/model Claude Sonnet 4.6 (Global)")
		if got := h.lastText(ownerChat); got != "This chat now uses Claude Sonnet 4.6 (Global)." {
			t.Fatalf("/model by full name = %q", got)
		}
	})
}

// namedModels is an LLM that only names models, all /model asks of it.
type namedModels struct {
	core.LLM
	models []core.ModelInfo
}

func (n namedModels) Models(context.Context) []core.ModelInfo { return n.models }

func (n namedModels) Model(ref core.ModelRef) (core.ModelInfo, bool) {
	for _, m := range n.models {
		if m.Ref == ref {
			return m, true
		}
	}
	return core.ModelInfo{}, false
}

func TestStopAllCountsTasks(t *testing.T) {
	for n, want := range map[int]string{
		0: "Paused. No tasks were running. /resume lifts the pause.",
		1: "Paused. Stopped 1 task. /resume lifts the pause.",
		3: "Paused. Stopped 3 tasks. /resume lifts the pause.",
	} {
		bubble(t, func(t *testing.T) {
			h := newHarness(t, func(h *harness) { h.runner2 = &stopAllRunner{Runner: h.runner, n: n} }).start()
			h.dm(ownerChat, "/stopall")
			if got := h.lastText(ownerChat); got != want {
				t.Fatalf("/stopall with %d tasks = %q, want %q", n, got, want)
			}
		})
	}
}

type stopAllRunner struct {
	*fakes.Runner
	n int
}

func (s *stopAllRunner) StopAll(ctx context.Context, by core.UserID) (int, error) {
	_, err := s.Runner.StopAll(ctx, by)
	return s.n, err
}
