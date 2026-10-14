package channel

import (
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
	ap.Card.Fields = []core.CardField{{Label: "Environment", Value: "staging"}, {Label: "Ref", Value: "main", Mono: true}}
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
			if len(card) != 1 || !strings.Contains(card[0].Text, "0 of 2 approvals") || !strings.Contains(card[0].Text, "staging") ||
				strings.Join(buttonLabels(card[0]), ",") != "Allow once,Deny" {
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
		if len(waiting) != 1 || len(waiting[0].Buttons) != 0 || !strings.Contains(waiting[0].Text, "Waiting for 2 other people to approve") {
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
		for _, p := range []core.PrincipalID{voted.Principal, core.UserPrincipal(h.owner.ID)} {
			h.bus.Publish(core.Event{Type: core.EvApprovalVoted, Principal: p, Data: core.ApprovalData{Approval: voted}})
		}
		synctest.Wait()
		edits := h.ch.EditedMessages()
		if len(edits) != 3 {
			t.Fatalf("card edits after a vote = %d, want one per card", len(edits))
		}
		for _, e := range edits {
			if !strings.Contains(e.Text, "1 of 2 approvals") || strings.Join(buttonLabels(e), ",") != "Allow once,Deny" {
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
		last := edits[len(edits)-1]
		if len(edits) != 6 || len(last.Buttons) != 0 || !strings.Contains(last.Text, "Approved by Ada and Ben") {
			t.Fatalf("final card edits = %d, last %+v", len(edits), last)
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
		if !strings.Contains(h.sent(memberChat)[0].Text, "Waiting for someone else to approve") {
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
	})
}
