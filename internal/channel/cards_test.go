package channel

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// request creates a pending approval in the fake and publishes
// approval.requested like policy.Approvals does.
func (h *harness) request(p core.PrincipalID, approver core.ApproverReq, chat *core.ChatRef, mod ...func(*core.Approval)) *core.Approval {
	h.t.Helper()
	ap, err := h.approvals.Request(h.ctx, core.ApprovalRequest{
		Run: core.RunID(core.NewID(core.PrefixRun)), Session: core.SessionID(core.NewID(core.PrefixSession)), Principal: p,
		Action: &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: core.SELocalWrite, Argv: []string{"bash", "-lc", "rm -rf build"}},
		Spec:   core.ToolSpec{Def: core.ToolDef{Name: "shell"}, Manifest: core.Manifest{Title: "Run shell command"}},
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: approver, Timeout: 15 * time.Minute, OnTimeout: core.TimeoutDeny,
			Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}},
		Chat: chat, Context: "Telegram · DM",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, m := range mod {
		m(ap)
	}
	h.publishRequested(ap)
	return ap
}

func (h *harness) publishRequested(ap *core.Approval) {
	h.bus.Publish(core.Event{Type: core.EvApprovalRequested, Principal: ap.Principal, Data: core.ApprovalData{Approval: ap}})
	if ap.Approver.AdminOnly {
		h.bus.Publish(core.Event{Type: core.EvApprovalRequested, Data: core.ApprovalData{Approval: ap}})
	}
	synctest.Wait()
}

func (h *harness) refs(id core.ApprovalID) []core.CardMessage {
	h.t.Helper()
	refs, err := h.approvals.MessageRefs(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return refs
}

func (h *harness) sent(chat string) []core.OutMessage {
	var out []core.OutMessage
	for _, m := range h.ch.SentMessages() {
		if m.Chat.ChatID == chat {
			out = append(out, m)
		}
	}
	return out
}

func buttonLabels(m core.OutMessage) []string {
	var out []string
	for _, row := range m.Buttons {
		for _, b := range row {
			out = append(out, b.Label)
		}
	}
	return out
}

func TestCardInApproverDMAndNonApproverRejected(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		owner := core.UserPrincipal(h.owner.ID)
		chat := h.dmChat(ownerChat)
		ap := h.request(owner, core.ApproverReq{User: h.owner.ID}, &chat)

		cards := h.sent(ownerChat)
		if len(cards) != 1 {
			t.Fatalf("cards = %d", len(cards))
		}
		card := cards[0]
		if got := buttonLabels(card); strings.Join(got, ",") != "Allow once,Allow for session,Deny" {
			t.Fatalf("buttons = %v", got)
		}
		if !strings.Contains(card.Text, "Run shell command") || !strings.Contains(card.Text, "rm -rf build") {
			t.Fatalf("card text = %q", card.Text)
		}
		refs := h.refs(ap.ID)
		if len(refs) != 1 || refs[0].Truncated || refs[0].Ref.Chat.ChatID != ownerChat {
			t.Fatalf("refs = %+v", refs)
		}

		allow := h.approvals.ButtonData(ap.ID, core.AllowOnce)
		h.ch.Press(refs[0].Ref, memberChat, allow)
		synctest.Wait()
		h.ch.Press(refs[0].Ref, "9009", allow)
		synctest.Wait()
		if got := h.ch.AckToasts(); len(got) != 2 || got[0] != "Not allowed." || got[1] != "Not allowed." {
			t.Fatalf("toasts = %q", got)
		}
		if a, _ := h.approvals.Get(h.ctx, core.SystemViewer(), ap.ID); a.Status != core.ApprovalPending {
			t.Fatalf("a non-approver changed the approval to %s", a.Status)
		}

		h.ch.Press(refs[0].Ref, ownerChat, "a:"+string(ap.ID)+":o:forged00")
		synctest.Wait()
		h.ch.Press(refs[0].Ref, ownerChat, allow)
		synctest.Wait()
		toasts := h.ch.AckToasts()
		if toasts[2] != "This button is not valid." || toasts[3] != "Approved" {
			t.Fatalf("toasts = %q", toasts)
		}
		res := h.approvals.Resolutions
		if len(res) != 1 || res[0].By != h.owner.ID || res[0].Via != "telegram" || res[0].ViaMsg == nil || *res[0].ViaMsg != refs[0].Ref {
			t.Fatalf("resolutions = %+v", res)
		}

		h.bus.Publish(core.Event{Type: core.EvApprovalResolved, Principal: owner, Data: core.ApprovalResolvedData{
			Approval: ap.ID, Status: core.ApprovalAllowed, Choice: core.AllowOnce, By: h.owner.ID, Via: "telegram"}})
		synctest.Wait()
		edited := h.ch.EditedMessages()
		if len(edited) != 1 || len(edited[0].Buttons) != 0 || !strings.Contains(edited[0].Text, "Allowed once by Olivia (telegram)") {
			t.Fatalf("card edits = %+v", edited)
		}
		h.ch.Press(refs[0].Ref, ownerChat, h.approvals.ButtonData(ap.ID, core.Deny))
		synctest.Wait()
		if got := h.ch.AckToasts(); got[len(got)-1] != "Already decided." {
			t.Fatalf("second press toast = %q", got[len(got)-1])
		}
	})
}

func TestGroupRunCardGoesToOwnerDM(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		sp := h.space("-500", true, true)
		group := h.groupChat("-500")
		ap := h.request(sp.Principal, core.ApproverReq{User: h.owner.ID}, &group)
		if cards := h.sent(ownerChat); len(cards) != 1 || len(cards[0].Buttons) == 0 {
			t.Fatalf("owner DM got %+v", cards)
		}
		inGroup := h.sent("-500")
		if len(inGroup) != 1 || len(inGroup[0].Buttons) != 0 || !strings.Contains(inGroup[0].Text, "Waiting for the owner to approve") {
			t.Fatalf("group got %+v; want only a waiting line without buttons", inGroup)
		}
		for _, r := range h.refs(ap.ID) {
			if r.Ref.Chat.ChatID != ownerChat {
				t.Fatalf("a card was recorded outside the owner's DM: %+v", r)
			}
		}
	})
}

func TestTruncatedCardOffersOnlyDenyAndWeb(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.cfg.Set(func(c *config.Config) { c.Server.PublicURL = "https://bothy.example.com" })
		owner := core.UserPrincipal(h.owner.ID)
		chat := h.dmChat(ownerChat)
		ap := h.request(owner, core.ApproverReq{User: h.owner.ID}, &chat, func(a *core.Approval) {
			a.Card.Code = strings.Repeat("echo x; ", 700)
			a.Card.WebURL = "https://bothy.example.com/approvals/" + string(a.ID)
		})
		card := h.sent(ownerChat)[0]
		if len(card.Buttons) != 2 || len(card.Buttons[0]) != 1 || card.Buttons[0][0].Label != "Deny" ||
			card.Buttons[1][0].URL != "https://bothy.example.com/approvals/"+string(ap.ID) || card.Buttons[1][0].Label != "Open in web to approve" {
			t.Fatalf("truncated card buttons = %+v", card.Buttons)
		}
		if strings.Contains(card.Text, "echo x") || ulen(card.Text) > 4096 {
			t.Fatalf("truncated card text = %q", card.Text)
		}
		refs := h.refs(ap.ID)
		if len(refs) != 1 || !refs[0].Truncated {
			t.Fatalf("refs = %+v, want truncated", refs)
		}
		h.ch.Press(refs[0].Ref, ownerChat, h.approvals.ButtonData(ap.ID, core.AllowOnce))
		synctest.Wait()
		if got := h.ch.AckToasts(); got[0] != "Open in web to approve." {
			t.Fatalf("toast = %q", got)
		}
		h.ch.Press(refs[0].Ref, ownerChat, h.approvals.ButtonData(ap.ID, core.Deny))
		synctest.Wait()
		if a, _ := h.approvals.Get(h.ctx, core.SystemViewer(), ap.ID); a.Status != core.ApprovalDenied {
			t.Fatalf("deny from a truncated card left status %s", a.Status)
		}

		long := h.request(owner, core.ApproverReq{User: h.owner.ID}, &chat, func(a *core.Approval) {
			a.Card.Diff = strings.Repeat("+line\n", 700)
		})
		h.cfg.Set(func(c *config.Config) { c.Server.PublicURL = "" })
		noWeb := h.request(owner, core.ApproverReq{User: h.owner.ID}, &chat, func(a *core.Approval) {
			a.Card.Code = strings.Repeat("y", 5000)
		})
		cards := h.sent(ownerChat)
		if refs := h.refs(long.ID); len(refs) != 1 || !refs[0].Truncated {
			t.Fatalf("a card with a diff over 3 KB was not truncated: %+v", refs)
		}
		last := cards[len(cards)-1]
		if got := buttonLabels(last); len(got) != 1 || got[0] != "Deny" || !strings.Contains(last.Text, "web UI → Approvals") {
			t.Fatalf("truncated card without public_url = %+v", last)
		}
		_ = noWeb
	})
}

func TestAdminOnlyCardGoesToOwnerOnce(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		member := h.dmChat(memberChat)
		ap := h.request(core.UserPrincipal(h.member.ID), core.ApproverReq{AdminOnly: true}, &member)
		if cards := h.sent(ownerChat); len(cards) != 1 {
			t.Fatalf("owner got %d cards for an admin-only approval (published twice)", len(cards))
		}
		for _, m := range h.sent(memberChat) {
			if len(m.Buttons) != 0 {
				t.Fatalf("the member's DM got approve buttons: %+v", m)
			}
		}
		h.wantVisible(memberChat, "Waiting for the owner")
		refs := h.refs(ap.ID)
		h.ch.Press(refs[0].Ref, ownerChat, h.approvals.ButtonData(ap.ID, core.AllowOnce))
		synctest.Wait()
		if got := h.ch.AckToasts(); got[0] != "Approved" {
			t.Fatalf("admin press toast = %q", got)
		}
	})
}

func TestWebOriginCardAndResync(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Set(func(c *config.Config) { c.Policy.Approval.Notify = []string{"telegram", "slack"} })
		early, err := h.approvals.Request(h.ctx, core.ApprovalRequest{Principal: core.UserPrincipal(h.owner.ID),
			Decision: core.Decision{Approver: core.ApproverReq{User: h.owner.ID}, Choices: []core.Choice{core.AllowOnce, core.Deny}}})
		if err != nil {
			t.Fatal(err)
		}
		h.start()
		if refs := h.refs(early.ID); len(refs) != 1 {
			t.Fatalf("pending approval from before start delivered %d times", len(refs))
		}
		ap := h.request(core.UserPrincipal(h.owner.ID), core.ApproverReq{User: h.owner.ID}, nil)
		if refs := h.refs(ap.ID); len(refs) != 1 || refs[0].Ref.Chat.ChatID != ownerChat {
			t.Fatalf("web-origin card refs = %+v", refs)
		}
	})
}

func TestCardShowsValuesVerbatim(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		ap := &core.Approval{ID: core.ApprovalID(core.NewID(core.PrefixApproval)), Card: core.Card{
			Title:  "Send **message**",
			Fields: []core.CardField{{Label: "Recipient", Value: "[click](https://evil.example)\u202egnp.exe"}, {Label: "Path", Value: "a`b", Mono: true}},
			Code:   "```\nbreak out\n```", Choices: []core.Choice{core.AllowOnce, core.Deny},
		}}
		msg, truncated := h.hub.renderCard(ap, core.ChannelCaps{Buttons: true, MaxLen: 4096})
		if truncated {
			t.Fatal("small card truncated")
		}
		for _, want := range []string{`Send \*\*message\*\*`, "`[click](https://evil.example)⟨U+202E⟩gnp.exe`", "``a`b``", "````\n```\nbreak out\n```\n````"} {
			if !strings.Contains(msg.Text, want) {
				t.Errorf("card text %q lacks %q", msg.Text, want)
			}
		}
		_, truncated = h.hub.renderCard(ap, core.ChannelCaps{MaxLen: 4096})
		if !truncated {
			t.Fatal("a card for a channel without buttons must be truncated")
		}
	})
}
