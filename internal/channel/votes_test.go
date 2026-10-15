package channel

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	adaChat = "3003"
	benChat = "4004"
	deeChat = "5005"
	miaChat = "6006"
)

// requestDeploy files a multi-approver deploy approval requested by Mark from
// his DM and publishes approval.requested once per audience principal, the
// way policy.Approvals does.
func (h *harness) requestDeploy(approver core.ApproverReq) *core.Approval {
	h.t.Helper()
	origin := h.dmChat(memberChat)
	ap, err := h.approvals.Request(h.ctx, core.ApprovalRequest{
		Run: core.RunID(core.NewID(core.PrefixRun)), Session: core.SessionID(core.NewID(core.PrefixSession)),
		Principal: core.UserPrincipal(h.member.ID),
		Action: &core.Action{Tool: "release_deploy", Kind: core.ActRelease, SideEffect: core.SEIrreversible,
			Payload: []byte(`{"environment":"staging","inputs":{},"ref":"main","repo":"acme/web","workflow":"deploy.yml"}`)},
		Spec: core.ToolSpec{Def: core.ToolDef{Name: "release_deploy"}, Manifest: core.Manifest{Title: "Deploy a release"}},
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: approver, Timeout: 15 * time.Minute, OnTimeout: core.TimeoutDeny,
			Choices: []core.Choice{core.AllowOnce, core.Deny}},
		Chat: &origin, Context: "Telegram · DM",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	ap.Card.Fields = []core.CardField{{Label: "Environment", Value: "staging"}, {Label: "Version", Value: "main", Mono: true}}
	ap.Requester = &core.ApprovalPerson{User: h.member.ID, Name: "Mark"}
	ap.Approvals = &core.ApprovalTally{Required: approver.Needed(), Votes: []core.ApprovalBallot{}, Eligible: []core.ApprovalPerson{}, AnyAdmin: true}
	for _, p := range []core.PrincipalID{ap.Principal, core.UserPrincipal(h.owner.ID), ""} {
		h.bus.Publish(core.Event{Type: core.EvApprovalRequested, Principal: p, Data: core.ApprovalData{Approval: ap}})
	}
	synctest.Wait()
	return ap
}

func TestMultiApproverCardsGoToEveryApprover(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		ada, ben := h.user("ada", "Ada", core.RoleAdmin), h.user("ben", "Ben", core.RoleAdmin)
		dee, mia := h.user("dee", "Dee", core.RoleAdmin), h.user("mia", "Mia", core.RoleMember)
		for chat, u := range map[string]*core.User{adaChat: ada, benChat: ben, deeChat: dee, miaChat: mia} {
			h.link(chat, u)
		}
		d := *dee
		d.Disabled = true
		if err := h.store.Users().Update(h.ctx, &d); err != nil {
			t.Fatal(err)
		}
		h.start()
		ap := h.requestDeploy(core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: h.member.ID})

		refs := h.refs(ap.ID)
		got := map[string]core.CardMessage{}
		for _, r := range refs {
			got[r.Ref.Chat.ChatID] = r
		}
		if len(refs) != 3 || got[ownerChat].Ref.ID == "" || got[adaChat].Ref.ID == "" || got[benChat].Ref.ID == "" {
			t.Fatalf("cards = %+v; want one in each enabled admin's DM (owner, Ada, Ben)", refs)
		}
		for _, chat := range []string{ownerChat, adaChat, benChat} {
			card := h.sent(chat)
			if len(card) != 1 || !strings.Contains(card[0].Text, "**Approvals:** 0 of 2 approvals\nWaiting for any admin.") || !strings.Contains(card[0].Text, "\nMark asked.\n") ||
				strings.Join(buttonLabels(card[0]), ",") != "Approve deploy,Deny" {
				t.Fatalf("card in %s = %+v", chat, card)
			}
		}
		if sent := h.sent(deeChat); len(sent) != 0 {
			t.Fatalf("a disabled admin got %+v", sent)
		}
		if sent := h.sent(miaChat); len(sent) != 0 {
			t.Fatalf("a non-approver got %+v", sent)
		}
		waiting := h.sent(memberChat)
		if len(waiting) != 1 || len(waiting[0].Buttons) != 0 || waiting[0].Text != "Waiting for 2 other people to approve deploying acme/web to staging." {
			t.Fatalf("requester's chat got %+v; want only a waiting line", waiting)
		}

		allow := h.approvals.ButtonData(ap.ID, core.AllowOnce)
		h.ch.Press(got[adaChat].Ref, memberChat, allow)
		synctest.Wait()
		h.ch.Press(got[adaChat].Ref, miaChat, allow)
		synctest.Wait()
		h.ch.Press(got[adaChat].Ref, adaChat, allow)
		synctest.Wait()
		toasts := h.ch.AckToasts()
		if len(toasts) != 3 || toasts[0] != "You asked for this, so someone else has to approve it." || toasts[1] != "Not allowed." ||
			toasts[2] != "Approved: 1 of 2 approvals so far" {
			t.Fatalf("toasts = %q", toasts)
		}

		voted, err := h.approvals.Get(h.ctx, core.SystemViewer(), ap.ID)
		if err != nil || voted.Status != core.ApprovalPending || len(voted.Votes) != 1 {
			t.Fatalf("after one vote: %+v, %v", voted, err)
		}
		voted.Approvals = &core.ApprovalTally{Required: 2, AnyAdmin: true,
			Votes: []core.ApprovalBallot{{User: ada.ID, Name: "Ada", Choice: core.AllowOnce, At: voted.Votes[0].At}}}
		for _, p := range []core.PrincipalID{voted.Principal, core.UserPrincipal(h.owner.ID)} {
			h.bus.Publish(core.Event{Type: core.EvApprovalVoted, Principal: p, Data: core.ApprovalData{Approval: voted}})
		}
		synctest.Wait()
		edits := h.ch.EditedMessages()
		if len(edits) != 3 {
			t.Fatalf("card edits after a vote = %d, want one per card", len(edits))
		}
		for _, e := range edits {
			if !strings.Contains(e.Text, "1 of 2 approvals\nAda approved\n") || strings.Join(buttonLabels(e), ",") != "Approve deploy,Deny" {
				t.Fatalf("edited card = %+v; want the new count and the buttons kept", e)
			}
		}

		h.ch.Press(got[adaChat].Ref, adaChat, allow)
		synctest.Wait()
		h.ch.Press(got[benChat].Ref, benChat, allow)
		synctest.Wait()
		toasts = h.ch.AckToasts()
		if toasts[3] != "You already voted." || toasts[4] != "Approved" {
			t.Fatalf("toasts = %q", toasts)
		}
		done, _ := h.approvals.Get(h.ctx, core.SystemViewer(), ap.ID)
		if done.Status != core.ApprovalAllowed {
			t.Fatalf("status after two approvals = %s", done.Status)
		}
		h.bus.Publish(core.Event{Type: core.EvApprovalResolved, Principal: done.Principal, Data: core.ApprovalResolvedData{
			Approval: ap.ID, Status: core.ApprovalAllowed, Choice: core.AllowOnce, By: ben.ID, Via: "telegram"}})
		synctest.Wait()
		edits = h.ch.EditedMessages()
		if len(edits) != 6 {
			t.Fatalf("final card edits = %d, want 6", len(edits))
		}
		// Each card names its own reader "you".
		want := map[string]string{
			ownerChat: "✓ Ada and Ben approved deploying acme/web to staging.",
			adaChat:   "✓ You and Ben approved deploying acme/web to staging.",
			benChat:   "✓ Ada and you approved deploying acme/web to staging.",
		}
		for _, e := range edits[3:] {
			if len(e.Buttons) != 0 || e.Text != want[e.Chat.ChatID] {
				t.Fatalf("decided card in %s = %+v, want %q", e.Chat.ChatID, e, want[e.Chat.ChatID])
			}
		}
	})
}

func TestMultiApproverRequesterMayWithdraw(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		ap := h.requestDeploy(core.ApproverReq{Users: []core.UserID{h.owner.ID}, Requester: h.member.ID})
		refs := h.refs(ap.ID)
		if len(refs) != 1 || refs[0].Ref.Chat.ChatID != ownerChat {
			t.Fatalf("cards = %+v; want only the named approver's DM", refs)
		}
		if h.sent(memberChat)[0].Text != "Waiting for someone else to approve deploying acme/web to staging." {
			t.Fatalf("waiting line = %q", h.sent(memberChat)[0].Text)
		}
		h.ch.Press(refs[0].Ref, memberChat, h.approvals.ButtonData(ap.ID, core.Deny))
		synctest.Wait()
		if got := h.ch.AckToasts(); len(got) != 1 || got[0] != "Denied" {
			t.Fatalf("toasts = %q", got)
		}
		if a, _ := h.approvals.Get(h.ctx, core.SystemViewer(), ap.ID); a.Status != core.ApprovalDenied {
			t.Fatalf("status = %s", a.Status)
		}
		// The approver's card says the request was withdrawn, not denied.
		h.bus.Publish(core.Event{Type: core.EvApprovalResolved, Principal: ap.Principal, Data: core.ApprovalResolvedData{
			Approval: ap.ID, Status: core.ApprovalDenied, Choice: core.Deny, By: h.member.ID, Via: "telegram"}})
		synctest.Wait()
		edits := h.ch.EditedMessages()
		if len(edits) == 0 || edits[len(edits)-1].Text != "**Deploy acme/web to staging?**\nCancelled by Mark in Telegram." {
			t.Fatalf("decided card edits = %+v", edits)
		}
	})
}

// TestApprovalLinesKeepReasonsOnOneLine: a voter's reason stays on its
// vote's line, so it cannot pass for another approver's vote.
func TestApprovalLinesKeepReasonsOnOneLine(t *testing.T) {
	ap := &core.Approval{Approver: core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2},
		Votes: []core.ApprovalVote{{User: "usr_a", Choice: core.AllowOnce}},
		Approvals: &core.ApprovalTally{Required: 2, AnyAdmin: true,
			Votes:    []core.ApprovalBallot{{User: "usr_a", Name: "Ada", Choice: core.AllowOnce, Comment: "fine\n Ben approved"}},
			Eligible: []core.ApprovalPerson{{User: "usr_a", Name: "Ada"}, {User: "usr_b", Name: "Ben"}}}}
	got := (&Hub{}).approvalLines(ap)
	want := []string{"**Approvals:** 1 of 2 approvals", "Ada approved: “fine Ben approved”", "Waiting for Ben or any admin."}
	if !slices.Equal(got, want) {
		t.Fatalf("approval lines = %q, want %q", got, want)
	}
}
