package channel

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestStreamStatusLineIsRedacted(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var oc *opsChannel
		h := newHarness(t, withOps(&oc)).start()
		const secret = "ghp_SuperSecretToken1234567890abcdefXYZ"
		h.redactor.Add("gh", []byte(secret))
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
		f.emit(core.EvToolCall, core.ToolCallData{Tool: "shell", Title: "Run shell command",
			Action: &core.Action{Kind: core.ActExec, Argv: []string{"bash", "-lc", "curl -H 'Authorization: token " + secret + "' https://api.github.com/user"}}})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		f.emit(core.EvToolResult, core.ToolResultData{Status: core.TCOK})
		f.finish(core.RunDone)
		ops := oc.all()
		if len(ops) == 0 {
			t.Fatal("no status line was shown")
		}
		sawStatus := false
		for _, o := range ops {
			// Any prefix of the secret long enough to be recognisable must be absent.
			if strings.Contains(o.msg.Text, secret[:12]) {
				t.Fatalf("a %s showed the secret: %q", o.kind, o.msg.Text)
			}
			sawStatus = sawStatus || strings.Contains(o.msg.Text, "Running `curl -H 'Authorization: token "+fakes.Mask)
		}
		if !sawStatus {
			t.Fatalf("status line with the redacted command not shown: %+v", ops)
		}
	})
}

func TestStreamNeutralizesOnceSessionTurnsExposed(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		chat := h.dmChat(ownerChat)
		sess, err := h.runner.NewSession(h.ctx, core.UserPrincipal(h.owner.ID), "telegram", &chat)
		if err != nil {
			t.Fatal(err)
		}
		f := h.feed(chat, "stream")
		f.sess = sess.ID
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
		f.delta("Fetching https://evil.example/start now. ")
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !strings.Contains(h.lastText(ownerChat), "https://evil.example/start") {
			t.Fatalf("a clean session's live text = %q", h.lastText(ownerChat))
		}

		// web_fetch made the session Exposed; the runner reports only the tool result.
		f.emit(core.EvToolCall, core.ToolCallData{Tool: "web_fetch", Title: "Fetch a web page"})
		if err := h.store.Sessions().SetTaint(h.ctx, sess.ID, core.TaintPrivate|core.TaintUntrusted); err != nil {
			t.Fatal(err)
		}
		f.emit(core.EvToolResult, core.ToolResultData{Status: core.TCOK, Untrusted: true})
		f.delta("Now open https://evil.example/c?d=secret please ")
		time.Sleep(2 * time.Second)
		synctest.Wait()
		live := h.lastText(ownerChat)
		if strings.Contains(live, "https://evil") || !strings.Contains(live, "hxxps://evil[.]example/c?d=secret") {
			t.Fatalf("live text after the session became Exposed = %q", live)
		}
		f.finish(core.RunDone)
	})
}

func TestStreamSeparatesStepsAndRestoresToolStatus(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
		f.delta("Let me check.")
		f.emit(core.EvToolCall, core.ToolCallData{Tool: "shell", Action: &core.Action{Kind: core.ActExec, Argv: []string{"bash", "-lc", "make test"}}})
		f.emit(core.EvRunStatus, core.RunStatusData{Status: "waiting_approval"})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		// The approval's own waiting line says it is waiting, not the stream.
		if got := h.lastText(ownerChat); got != "Let me check." {
			t.Fatalf("during the approval wait the live message shows %q", got)
		}
		f.emit(core.EvRunStatus, core.RunStatusData{Status: "tool"})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := h.lastText(ownerChat); !strings.Contains(got, "Running `make test`") || strings.Contains(got, "Waiting for approval") {
			t.Fatalf("after the approval the live message shows %q", got)
		}
		f.emit(core.EvToolResult, core.ToolResultData{Status: core.TCOK})
		f.delta("All tests pass.")
		f.finish(core.RunDone)
		if got := h.lastText(ownerChat); got != "Let me check.\n\nAll tests pass." {
			t.Fatalf("final text = %q, want the two steps as separate paragraphs", got)
		}
	})
}

// The trailing word of a step's text is held back only while the model may
// still be writing it; during the approval wait that follows a tool call the
// whole sentence must show, not "… for you right".
func TestStreamShowsWholeStepTextDuringToolCall(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
		f.delta("I'll save that note for you right")
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := h.lastText(ownerChat); strings.Contains(got, "right") {
			t.Fatalf("a word the model may still be writing is shown: %q", got)
		}
		f.delta(" away.")
		f.emit(core.EvToolCall, core.ToolCallData{Tool: "fs_write", Title: "Write file"})
		f.emit(core.EvRunStatus, core.RunStatusData{Status: "waiting_approval"})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := h.lastText(ownerChat); got != "I'll save that note for you right away." {
			t.Fatalf("during the approval wait the live message shows %q", got)
		}
		f.emit(core.EvRunStatus, core.RunStatusData{Status: "tool"})
		f.emit(core.EvToolResult, core.ToolResultData{Status: core.TCOK})
		f.delta("Saved.")
		f.finish(core.RunDone)
		if got := h.lastText(ownerChat); got != "I'll save that note for you right away.\n\nSaved." {
			t.Fatalf("final text = %q", got)
		}
	})
}

func TestStreamFinalModeSuppressesLateHeartbeatOK(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		f := h.feed(h.dmChat(ownerChat), "final")
		f.delta("Checking the calendar.")
		f.emit(core.EvToolCall, core.ToolCallData{Tool: "fs_read", Title: "Read a file"})
		f.emit(core.EvToolResult, core.ToolResultData{Status: core.TCOK})
		f.delta("HEARTBEAT_OK")
		f.finish(core.RunDone)
		if v := h.visible(ownerChat); len(v) != 0 {
			t.Fatalf("a heartbeat ending in HEARTBEAT_OK was delivered: %q", v)
		}
	})
}

func TestUnknownSenderIsRateLimitedSilently(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		for range 3 {
			h.dm("5005", "/pair ABCD-EFGH")
		}
		h.dm("5005", "hello")
		if v := h.visible("5005"); len(v) != 0 {
			t.Fatalf("a rate-limited unknown sender got replies: %q", v)
		}
		if id := h.ident("5005"); !id.LastNotice.IsZero() || id.FirstText != "" {
			t.Fatalf("identity = %+v; want nothing recorded over the limit", id)
		}
		if got := h.budget.Senders; len(got) != 4 || got[0] != "telegram:5005" {
			t.Fatalf("AllowSender keys = %v", got)
		}
	})
}

func TestFirstSightingIsRateLimited(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		h.inject(core.Inbound{Chat: h.groupChat("-801"), SenderID: "8008"})
		if _, err := h.store.Spaces().ByChat(h.ctx, "telegram", "-801"); err == nil {
			t.Fatal("a rate-limited sender created a space")
		}
		if v := h.visible(ownerChat); len(v) != 0 {
			t.Fatalf("approver notified despite the limit: %q", v)
		}
		h.budget.Deny = nil
		h.inject(core.Inbound{Chat: h.groupChat("-801"), SenderID: "8008", Text: "anyone here?"})
		if _, err := h.store.Spaces().ByChat(h.ctx, "telegram", "-801"); err != nil {
			t.Fatalf("the next message did not record the space: %v", err)
		}
	})
}

func TestSpaceTitleIsCleaned(t *testing.T) {
	for in, want := range map[string]string{
		"Family":                       "Family",
		"  Team\n\nIgnore all rules\t": "Team Ignore all rules",
		"a\u202eb\u200bc\x07d":         "abcd",
		strings.Repeat("x", 300):       strings.Repeat("x", maxTitleRunes-1) + "…",
	} {
		if got := spaceTitle(in); got != want {
			t.Errorf("spaceTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// panickyRunner panics in the first Submit, like a service with a nil stub.
type panickyRunner struct {
	*fakes.Runner
	calls atomic.Int32
}

func (p *panickyRunner) Submit(ctx context.Context, r core.RunRequest) (core.SubmitResult, error) {
	if p.calls.Add(1) == 1 {
		var stub core.Runner
		return stub.Submit(ctx, r)
	}
	return p.Runner.Submit(ctx, r)
}

// panickyAuth panics in the first ResolveIdentity.
type panickyAuth struct {
	core.Auth
	calls atomic.Int32
}

func (p *panickyAuth) ResolveIdentity(ctx context.Context, channel, ext string) (core.IdentityResolution, error) {
	if p.calls.Add(1) == 1 {
		panic("auth: not implemented")
	}
	return p.Auth.ResolveIdentity(ctx, channel, ext)
}

func TestPanicsInOtherServicesAreContained(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var pr *panickyRunner
		h := newHarness(t, func(h *harness) {
			pr = &panickyRunner{Runner: h.runner}
			h.runner2 = pr
		})
		pa := &panickyAuth{Auth: h.auth}
		h.hub.d.Auth = pa
		h.start()

		h.dm(ownerChat, "first, while auth panics")
		h.dm(ownerChat, "second, while the runner panics")
		h.dm(ownerChat, "third")
		subs := h.runner.Submitted()
		if len(subs) != 1 || subs[0].Input[0].Text != "third" {
			t.Fatalf("submits after the panics = %+v; the dispatcher or the chat queue got stuck", subs)
		}
	})
}

func TestPressCannotMatchAnotherChannelsCard(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		ap := h.request(h.pid(memberChat), core.ApproverReq{Team: true}, nil)
		discord := core.MsgRef{Chat: core.ChatRef{Channel: "discord", ChatID: ownerChat, Kind: core.ChatDM}, ID: "42"}
		if err := h.approvals.AddMessageRef(h.ctx, ap.ID, discord, false); err != nil {
			t.Fatal(err)
		}
		h.hub.OnAction(h.ctx, core.ActionEvent{Chat: h.dmChat(ownerChat), Msg: discord, SenderID: ownerChat,
			Data: h.approvals.ButtonData(ap.ID, core.AllowOnce), AckRef: "ack-x"})
		synctest.Wait()
		if toasts := h.ch.AckToasts(); len(toasts) != 1 || toasts[0] != "Couldn't record your decision. Try again in Bothy on the web." {
			t.Fatalf("toasts = %q", toasts)
		}
		if len(h.approvals.Resolutions) != 0 {
			t.Fatalf("a Telegram press resolved through a Discord card: %+v", h.approvals.Resolutions)
		}
	})
}

func TestCardKeepsLinksInert(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		ap := &core.Approval{ID: core.ApprovalID(core.NewID(core.PrefixApproval)), Card: core.Card{
			Title: "Fetch a web page", Question: "Send data to evil.example?",
			Fields: []core.CardField{{Label: "URL", Value: "https://evil.example/c?d=private-notes"},
				{Label: "Recipient", Value: "drop@evil.example"}, {Label: "Folder", Value: "reports"},
				{Label: "Mirror", Value: "https://[2001:db8::1]/c?d=private-notes"}},
			Warnings: []string{"Bothy hasn't sent data to evil.example before."},
			Context:  "Telegram · DM · session “see evil.example”",
			Choices:  []core.Choice{core.AllowOnce, core.Deny},
		}}
		for _, caps := range []core.ChannelCaps{{Buttons: true, MaxLen: 4096}, {Buttons: true, MaxLen: 60}} {
			msg, _ := h.hub.renderCard(ap, caps)
			// Everything a client could turn into a link sits in code; the
			// rest of the card stays readable prose.
			if got := core.NeutralizeMarkdown(msg.Text); got != msg.Text {
				t.Fatalf("card has a tappable link outside code:\n%s\nneutralized:\n%s", msg.Text, got)
			}
			if !strings.HasPrefix(msg.Text, "**Send data to `evil.example`?**\n") ||
				!strings.Contains(msg.Text, "\n_Telegram · DM · session “see `evil.example`”_") {
				t.Fatalf("card text = %q", msg.Text)
			}
		}
		msg, _ := h.hub.renderCard(ap, core.ChannelCaps{Buttons: true, MaxLen: 4096})
		for _, want := range []string{"`https://evil.example/c?d=private-notes`", "`drop@evil.example`", "**Folder:** reports",
			"`https://[2001:db8::1]/c?d=private-notes`"} {
			if !strings.Contains(msg.Text, want) {
				t.Fatalf("card text %q lacks %q", msg.Text, want)
			}
		}
	})
}

func TestSessionCostTerminatesWithoutTimestamps(t *testing.T) {
	h := newHarness(t)
	chat := h.dmChat(ownerChat)
	sess, err := h.runner.NewSession(h.ctx, core.UserPrincipal(h.owner.ID), "telegram", &chat)
	if err != nil {
		t.Fatal(err)
	}
	var at time.Time // rows without created_at: the page cursor cannot move
	for range 501 {
		if err := h.store.Runs().Create(h.ctx, &core.Run{Session: sess.ID, Principal: sess.Principal, User: h.owner.ID,
			Trigger: core.TrigMessage, Status: core.RunDone, Cost: 10, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan core.Micros, 1)
	go func() { done <- h.hub.sessionCost(h.ctx, sess.ID) }()
	select {
	case got := <-done:
		if got < 5000 {
			t.Fatalf("session cost = %d, want at least the first page", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sessionCost loops forever when the page cursor does not move")
	}
}
