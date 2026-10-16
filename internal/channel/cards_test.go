package channel

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// request creates a pending approval in the fake and publishes
// approval.requested like policy.Approvals does.
func (h *harness) request(p core.PrincipalID, approver core.ApproverReq, chat *core.ChatRef, mod ...func(*core.Approval)) *core.Approval {
	h.t.Helper()
	return h.requestFrom("", p, approver, chat, mod...)
}

// requestFrom is request for a run started by the chat sender
// "<app>:<external id>".
func (h *harness) requestFrom(sender string, p core.PrincipalID, approver core.ApproverReq, chat *core.ChatRef, mod ...func(*core.Approval)) *core.Approval {
	h.t.Helper()
	ap, err := h.approvals.Request(h.ctx, core.ApprovalRequest{
		Run: core.RunID(core.NewID(core.PrefixRun)), Session: core.SessionID(core.NewID(core.PrefixSession)), Principal: p,
		Action: &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: core.SELocalWrite, Argv: []string{"bash", "-lc", "rm -rf build"}},
		Spec:   core.ToolSpec{Def: core.ToolDef{Name: "shell"}, Manifest: core.Manifest{Title: "Run shell command"}},
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: approver, Timeout: 15 * time.Minute, OnTimeout: core.TimeoutDeny,
			Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny}},
		Chat: chat, Context: "Telegram · DM", Sender: sender,
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

// teammateAsks is a team request from a teammate's own DM.
func (h *harness) teammateAsks(ext, name string, mod ...func(*core.Approval)) *core.Approval {
	h.t.Helper()
	chat := h.dmChat(ext)
	named := func(a *core.Approval) {
		a.Requester = &core.ApprovalPerson{Name: name, App: h.app, Identity: h.app + ":" + ext}
	}
	return h.requestFrom(h.app+":"+ext, h.pid(ext), core.ApproverReq{Team: true}, &chat, append([]func(*core.Approval){named}, mod...)...)
}

func (h *harness) publishRequested(ap *core.Approval) {
	h.bus.Publish(core.Event{Type: core.EvApprovalRequested, Principal: ap.Principal, Data: core.ApprovalData{Approval: ap}})
	if ap.Approver.AdminOnly || ap.Approver.Team {
		h.bus.Publish(core.Event{Type: core.EvApprovalRequested, Data: core.ApprovalData{Approval: ap}})
	}
	synctest.Wait()
}

func (h *harness) resolved(ap *core.Approval, d core.ApprovalResolvedData) {
	d.Approval = ap.ID
	h.bus.Publish(core.Event{Type: core.EvApprovalResolved, Principal: ap.Principal, Data: d})
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

// ref returns the recorded message of id in chat.
func (h *harness) ref(id core.ApprovalID, chat string) core.CardMessage {
	h.t.Helper()
	for _, r := range h.refs(id) {
		if r.Ref.Chat.ChatID == chat {
			return r
		}
	}
	h.t.Fatalf("approval %s has no message in chat %s: %+v", id, chat, h.refs(id))
	return core.CardMessage{}
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

func (h *harness) press(ref core.MsgRef, sender string, ap *core.Approval, c core.Choice) string {
	h.t.Helper()
	h.ch.Press(ref, sender, h.approvals.ButtonData(ap.ID, c))
	synctest.Wait()
	toasts := h.ch.AckToasts()
	if len(toasts) == 0 {
		h.t.Fatal("no toast")
	}
	return toasts[len(toasts)-1]
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

// TestTeamCardGoesToChatApproversOnly: a teammate's request goes as a card
// to every chat approver's DM, never to someone who can't approve; the
// teammate's own DM gets a waiting line with Cancel request.
func TestTeamCardGoesToChatApproversOnly(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		sam := h.letIn("3003", "Sam", true)
		_ = sam
		ap := h.teammateAsks(memberChat, "Mark")

		for _, chat := range []string{ownerChat, "3003"} {
			cards := h.sent(chat)
			if len(cards) != 1 || len(cards[0].Buttons) == 0 {
				t.Fatalf("approver %s got %+v", chat, cards)
			}
			if !strings.HasPrefix(cards[0].Text, "**Run this command?**\n[Mark](tg://user?id=2002) asked on Telegram.\n") {
				t.Fatalf("card text = %q", cards[0].Text)
			}
			if r := h.ref(ap.ID, chat); r.Kind != core.CardMsgCard || r.Truncated {
				t.Fatalf("card ref = %+v", r)
			}
		}
		line := h.sent(memberChat)
		if len(line) != 1 || line[0].Text != "Waiting for approval: Run this command." || strings.Join(buttonLabels(line[0]), ",") != "Cancel request" {
			t.Fatalf("requester's chat got %+v", line)
		}
		if r := h.ref(ap.ID, memberChat); r.Kind != core.CardMsgNotice || !r.Truncated {
			t.Fatalf("waiting line ref = %+v", r)
		}

		if got := h.press(h.ref(ap.ID, ownerChat).Ref, ownerChat, ap, core.AllowOnce); got != "Approved." {
			t.Fatalf("approver's toast = %q", got)
		}
		res := h.approvals.Resolutions
		if len(res) != 1 || res[0].By != "" || res[0].ByIdentity != "telegram:"+ownerChat || res[0].Via != "telegram" || res[0].ViaMsg == nil {
			t.Fatalf("resolutions = %+v", res)
		}
	})
}

// TestStaleApproverGetsLinkCard: an approver whose membership verdict is
// older than two hours gets a card that only links to Bothy.
func TestStaleApproverGetsLinkCard(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.cfg.Set(func(c *config.Config) { c.Server.PublicURL = "https://bothy.example.com" })
		// Slack doesn't answer, so the sweep can't renew the approver.
		h.mc.SetMemberErr(errors.New("slack is down"))
		time.Sleep(3 * time.Hour)
		ap := h.teammateAsks(memberChat, "Mark", func(a *core.Approval) { a.Card.WebURL = "https://bothy.example.com/approvals/" + string(a.ID) })
		card := h.sent(ownerChat)
		if len(card) != 1 || card[0].Text != "A request from chat apps needs an admin. Admins can approve it in Bothy." ||
			len(card[0].Buttons) != 1 || card[0].Buttons[0][0].URL != "https://bothy.example.com/approvals/"+string(ap.ID) {
			t.Fatalf("stale approver got %+v", card)
		}
		if r := h.ref(ap.ID, ownerChat); r.Kind != core.CardMsgLink || !r.Truncated {
			t.Fatalf("link card ref = %+v", r)
		}
		if line := h.sent(memberChat); len(line) != 1 || line[0].Text != needsAdmin {
			t.Fatalf("requester's chat got %+v", line)
		}
	})
}

// TestRequesterApproverGetsCardInOwnDM: an approver's own request shows the
// card once in their DM, with no waiting line.
func TestRequesterApproverGetsCardInOwnDM(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		ap := h.teammateAsks(ownerChat, "Olivia")
		if msgs := h.sent(ownerChat); len(msgs) != 1 || len(msgs[0].Buttons) == 0 || h.ref(ap.ID, ownerChat).Kind != core.CardMsgCard {
			t.Fatalf("own DM got %+v", msgs)
		}
		if got := h.press(h.ref(ap.ID, ownerChat).Ref, ownerChat, ap, core.AllowOnce); got != "Approved." {
			t.Fatalf("toast = %q", got)
		}
	})
}

func TestNoChatApproverSaysAdminNeeded(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		no := false
		if _, err := h.auth.SetTeammate(h.ctx, h.admin(), "telegram", ownerChat, authPatch(nil, &no)); err != nil {
			t.Fatal(err)
		}
		ap := h.teammateAsks(memberChat, "Mark")
		if line := h.sent(memberChat); len(line) != 1 || line[0].Text != needsAdmin || strings.Join(buttonLabels(line[0]), ",") != "Cancel request" {
			t.Fatalf("requester's chat got %+v", line)
		}
		if len(h.sent(ownerChat)) != 0 || len(h.refs(ap.ID)) != 1 {
			t.Fatalf("a card went out with no approver: %+v", h.refs(ap.ID))
		}
	})
}

// TestAdminOnlyTeamCardIsLinkOnlyFixedText: chat approvers can't decide an
// admin-class request; their card says so without saying what it is.
func TestAdminOnlyTeamCardIsLinkOnlyFixedText(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		chat := h.dmChat(memberChat)
		ap := h.requestFrom("telegram:"+memberChat, h.pid(memberChat), core.ApproverReq{AdminOnly: true}, &chat)
		cards := h.sent(ownerChat)
		if len(cards) != 1 || cards[0].Text != "A request from chat apps needs an admin. Admins can approve it in Bothy under Approvals." ||
			len(cards[0].Buttons) != 0 {
			t.Fatalf("approver got %+v (published twice?)", cards)
		}
		if line := h.sent(memberChat); len(line) != 1 || line[0].Text != needsAdmin {
			t.Fatalf("requester's chat got %+v", line)
		}
		// Even a forged press of the link card's message can't approve it.
		if got := h.press(h.ref(ap.ID, ownerChat).Ref, ownerChat, ap, core.AllowOnce); got != "Open in Bothy to approve." {
			t.Fatalf("toast = %q", got)
		}
	})
}

// TestUserPrincipalApprovalsAreWebOnly: a web account's own requests never
// reach a chat app.
func TestUserPrincipalApprovalsAreWebOnly(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.request(core.UserPrincipal(h.owner.ID), core.ApproverReq{User: h.owner.ID}, nil)
		h.request(core.UserPrincipal(h.member.ID), core.ApproverReq{AdminOnly: true}, nil)
		if n := len(h.ch.SentMessages()); n != 0 {
			t.Fatalf("a web account's request reached chat: %+v", h.ch.SentMessages())
		}
	})
}

// TestReleaseFromTeammateHasNoChatCards: a deploy a teammate asked for is
// voted on the web; their chat hears whom it waits for.
func TestReleaseFromTeammateHasNoChatCards(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		chat := h.dmChat(memberChat)
		ap := h.requestFrom("telegram:"+memberChat, h.pid(memberChat), core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2}, &chat)
		if len(h.sent(ownerChat)) != 0 {
			t.Fatalf("an approver got a vote card: %+v", h.sent(ownerChat))
		}
		if line := h.sent(memberChat); len(line) != 1 || line[0].Text != "Waiting for 2 other people to approve running a command." {
			t.Fatalf("requester's chat got %+v", line)
		}
		if got := h.press(h.ref(ap.ID, memberChat).Ref, memberChat, ap, core.Deny); got != "Request cancelled." {
			t.Fatalf("withdraw toast = %q", got)
		}
	})
}

// TestWaitingLineRecordedAndEditedToOutcome: the waiting line becomes the
// outcome; a DM keeps the approver's reason, a group never shows it; a
// resync doesn't post it again.
func TestWaitingLineRecordedAndEditedToOutcome(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		ap := h.teammateAsks(memberChat, "Mark")
		h.hub.resyncCards()
		synctest.Wait()
		if n := len(h.sent(memberChat)); n != 1 {
			t.Fatalf("resync posted the waiting line again (%d lines)", n)
		}
		h.resolved(ap, core.ApprovalResolvedData{Status: core.ApprovalDenied, Choice: core.Deny, ByIdentity: "telegram:" + ownerChat,
			Via: "telegram", Comment: "not on main"})
		got := map[string]string{}
		for _, e := range h.ch.EditedMessages() {
			got[e.Chat.ChatID] = e.Text
		}
		if got[memberChat] != "✗ Olivia denied running a command in Telegram. “not on main”" || got[ownerChat] != "✗ You denied running a command. “not on main”" {
			t.Fatalf("edits = %q", got)
		}

		sp := h.space("-500", true, false)
		group := h.groupChat("-500")
		ap = h.requestFrom("telegram:"+memberChat, sp.Principal, core.ApproverReq{Team: true}, &group)
		if line := h.sent("-500"); len(line) != 1 || line[0].Text != "Waiting for approval: Run this command." {
			t.Fatalf("group got %+v", line)
		}
		h.resolved(ap, core.ApprovalResolvedData{Status: core.ApprovalDenied, Choice: core.Deny, By: h.owner.ID, Via: "web", Comment: "secret reason"})
		var groupEdit string
		for _, e := range h.ch.EditedMessages() {
			if e.Chat.ChatID == "-500" {
				groupEdit = e.Text
			}
		}
		if groupEdit != "✗ Olivia denied running a command on the web." {
			t.Fatalf("group outcome = %q, want no reason", groupEdit)
		}

		ap = h.teammateAsks(memberChat, "Mark")
		h.resolved(ap, core.ApprovalResolvedData{Status: core.ApprovalCancelled, Via: "stop"})
		if e := h.ch.EditedMessages(); e[len(e)-1].Text != "✗ Stopped." && e[len(e)-2].Text != "✗ Stopped." {
			t.Fatalf("a stopped request's line = %+v", e[len(e)-2:])
		}
	})
}

// TestCancelRequestByRequesterOnly: in a channel, only the person who asked
// can cancel from the waiting line.
func TestCancelRequestByRequesterOnly(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		sp := h.space("-500", true, false)
		group := h.groupChat("-500")
		ap := h.requestFrom("telegram:"+memberChat, sp.Principal, core.ApproverReq{Team: true}, &group)
		line := h.ref(ap.ID, "-500").Ref
		if got := h.press(line, "7007", ap, core.Deny); got != "Only the person who asked can cancel this." {
			t.Fatalf("another member's toast = %q", got)
		}
		if got := h.press(line, ownerChat, ap, core.Deny); got != "Only the person who asked can cancel this." {
			t.Fatalf("an approver's press on the line = %q", got)
		}
		if got := h.press(line, memberChat, ap, core.Deny); got != "Request cancelled." {
			t.Fatalf("requester's toast = %q", got)
		}
		if r := h.approvals.Resolutions; len(r) != 1 || r[0].ByIdentity != "telegram:"+memberChat || r[0].Choice != core.Deny {
			t.Fatalf("resolutions = %+v", r)
		}
		h.resolved(ap, core.ApprovalResolvedData{Status: core.ApprovalDenied, Choice: core.Deny, ByIdentity: "telegram:" + memberChat, Via: "telegram"})
		if e := h.ch.EditedMessages(); e[len(e)-1].Text != "✗ Cancelled by Mark." {
			t.Fatalf("withdrawn line = %q", e[len(e)-1].Text)
		}
	})
}

// The store keeps no chat kind with a card's message ref; the decided line
// still names its reader "You", and anyone else by name and place.
func TestDecidedCardNamesIdentityReader(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.letIn("3003", "Sam Patel", true)
		ap := h.teammateAsks(memberChat, "Mark")
		h.resolved(ap, core.ApprovalResolvedData{Status: core.ApprovalAllowed, Choice: core.AllowOnce, ByIdentity: "telegram:3003", Via: "telegram"})
		got := map[string]string{}
		for _, e := range h.ch.EditedMessages() {
			got[e.Chat.ChatID] = e.Text
		}
		if got["3003"] != "✓ You approved running a command." || got[ownerChat] != "✓ Sam Patel approved running a command in Telegram." ||
			got[memberChat] != "✓ Sam Patel approved running a command in Telegram." {
			t.Fatalf("decided cards = %q", got)
		}
	})
}

func TestPressByNonApproverRefused(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		ap := h.teammateAsks(memberChat, "Mark")
		card := h.ref(ap.ID, ownerChat).Ref
		for _, who := range []string{memberChat, "9009"} {
			if got := h.press(card, who, ap, core.AllowOnce); got != notApprover {
				t.Fatalf("%s's toast = %q", who, got)
			}
		}
		if got := h.press(card, ownerChat, ap, core.AllowOnce); got != "Approved." {
			t.Fatalf("approver's toast = %q", got)
		}
		if len(h.approvals.Resolutions) != 1 {
			t.Fatalf("resolutions = %+v", h.approvals.Resolutions)
		}
	})
}

func TestPressByLeftApproverRefused(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		ap := h.teammateAsks(memberChat, "Mark")
		card := h.ref(ap.ID, ownerChat).Ref
		h.mc.SetMember(ownerChat, core.Membership{Status: core.MemberOutsider})
		time.Sleep(core.MemberTTL + time.Minute)
		if got := h.press(card, ownerChat, ap, core.AllowOnce); got != notApprover {
			t.Fatalf("a departed approver's toast = %q", got)
		}
		if id := h.ident(ownerChat); id.CanApprove || id.Member != core.MemberOutsider {
			t.Fatalf("identity after the recheck = %+v", id)
		}
		if len(h.approvals.Resolutions) != 0 {
			t.Fatal("a departed approver resolved")
		}
	})
}

// TestPressRechecksStaleMembershipAndFallsBackOnlyFifteenMinutes: a press
// with a verdict over an hour old asks the platform; when it can't answer,
// only a verdict under fifteen minutes would do.
func TestPressRechecksStaleMembershipAndFallsBackOnlyFifteenMinutes(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		ap := h.teammateAsks(memberChat, "Mark")
		card := h.ref(ap.ID, ownerChat).Ref
		// The sweep can't renew the verdict while Slack is down.
		h.mc.SetMemberErr(errors.New("slack is down"))
		time.Sleep(core.MemberTTL + time.Minute)
		h.mc.SetMemberErr(nil)
		calls := len(h.mc.MemberCalls())
		if got := h.press(card, ownerChat, ap, core.AllowOnce); got != "Approved." || len(h.mc.MemberCalls()) != calls+1 {
			t.Fatalf("toast = %q after %d lookups", got, len(h.mc.MemberCalls())-calls)
		}

		ap = h.teammateAsks(memberChat, "Mark")
		card = h.ref(ap.ID, ownerChat).Ref
		h.mc.SetMemberErr(errors.New("slack is down"))
		time.Sleep(core.MemberTTL + time.Minute)
		if got := h.press(card, ownerChat, ap, core.AllowOnce); got != "Bothy couldn't check that you can approve. Try again, or open it in Bothy." {
			t.Fatalf("toast = %q", got)
		}
	})
}

// TestCardMentionsOnlyTheRequester: the card's "asked" line is the one
// place a Slack mention may form, and the card says whose; the same link
// in what the model writes (the message tool here) carries no mention.
func TestCardMentionsOnlyTheRequester(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.letIn("U0MARK", "Mark", false)
		h.teammateAsks("U0MARK", "Mark")
		card := h.sent(ownerChat)
		if len(card) != 1 || !strings.Contains(card[0].Text, "\n[Mark](slack://user?id=U0MARK) asked on Slack.\n") ||
			!slices.Equal(card[0].Mentions, []string{"U0MARK"}) {
			t.Fatalf("card = %+v", card)
		}
		if line := h.sent("U0MARK"); len(line) != 1 || len(line[0].Mentions) != 0 {
			t.Fatalf("waiting line = %+v", line)
		}
		if _, err := h.hub.Send(h.ctx, h.dmChat(memberChat), "Done. [ok](slack://user?id=U0CEO)"); err != nil {
			t.Fatal(err)
		}
		if got := h.sent(memberChat); len(got) != 1 || len(got[0].Mentions) != 0 {
			t.Fatalf("model text went out as %+v", got)
		}
	})
}

func TestTeamCardButtonsNameThePerson(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.teammateAsks(memberChat, "Mark Thompson-Lee the Third of Everywhere")
		card := h.sent(ownerChat)[0]
		if got := strings.Join(buttonLabels(card), ","); got != "Run command,Allow in Mark Thompson-Lee the T…'s chat,Always allow for Mark Thompson-Lee the T…,Deny" {
			t.Fatalf("buttons = %q", got)
		}
	})
}

func TestChannelCardHasNoGrantButtons(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		sp := h.space("-500", true, false)
		group := h.groupChat("-500")
		h.requestFrom("telegram:"+memberChat, sp.Principal, core.ApproverReq{Team: true}, &group, func(a *core.Approval) {
			a.Requester = &core.ApprovalPerson{Name: "Mark", App: "telegram", Chat: "Team", Identity: "telegram:" + memberChat}
		})
		card := h.sent(ownerChat)[0]
		if got := strings.Join(buttonLabels(card), ","); got != "Run command,Deny" {
			t.Fatalf("buttons = %q", got)
		}
		if !strings.Contains(card.Text, "\n[Mark](tg://user?id=2002) asked in Team.\n") {
			t.Fatalf("card text = %q", card.Text)
		}
	})
}

func TestTruncatedCardOffersOnlyDenyAndWeb(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.cfg.Set(func(c *config.Config) { c.Server.PublicURL = "https://bothy.example.com" })
		ap := h.teammateAsks(memberChat, "Mark", func(a *core.Approval) {
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
		r := h.ref(ap.ID, ownerChat)
		if !r.Truncated {
			t.Fatalf("ref = %+v, want truncated", r)
		}
		if got := h.press(r.Ref, ownerChat, ap, core.AllowOnce); got != "Open in web to approve." {
			t.Fatalf("toast = %q", got)
		}
		h.press(r.Ref, ownerChat, ap, core.Deny)
		if a, _ := h.approvals.Get(h.ctx, core.SystemViewer(), ap.ID); a.Status != core.ApprovalDenied {
			t.Fatalf("deny from a truncated card left status %s", a.Status)
		}

		long := h.teammateAsks(memberChat, "Mark", func(a *core.Approval) { a.Card.Diff = strings.Repeat("+line\n", 700) })
		if r := h.ref(long.ID, ownerChat); !r.Truncated {
			t.Fatalf("a card with a diff over 3 KB was not truncated: %+v", r)
		}
		h.cfg.Set(func(c *config.Config) { c.Server.PublicURL = "" })
		h.teammateAsks(memberChat, "Mark", func(a *core.Approval) { a.Card.Code = strings.Repeat("y", 5000) })
		cards := h.sent(ownerChat)
		last := cards[len(cards)-1]
		if got := buttonLabels(last); len(got) != 1 || got[0] != "Deny" || !strings.Contains(last.Text, "web UI → Approvals") {
			t.Fatalf("truncated card without public_url = %+v", last)
		}
	})
}

// A team request pending from before the hub started is delivered once.
func TestTeamCardResync(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		early, err := h.approvals.Request(h.ctx, core.ApprovalRequest{Principal: h.pid(memberChat),
			Decision: core.Decision{Approver: core.ApproverReq{Team: true}, Choices: []core.Choice{core.AllowOnce, core.Deny}}})
		if err != nil {
			t.Fatal(err)
		}
		h.start()
		if refs := h.refs(early.ID); len(refs) != 1 || refs[0].Ref.Chat.ChatID != ownerChat {
			t.Fatalf("pending approval from before start = %+v", refs)
		}
	})
}

// TestCardSpeaksPlainCopy: a chat card carries the web card's words: the
// question first, the consequence, warnings, the verb on the primary button
// (red only when the action can't be undone, Deny never), and its decided
// line names who decided, where, and the reason they gave.
func TestCardSpeaksPlainCopy(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		chat := h.dmChat(memberChat)
		ap, err := h.approvals.Request(h.ctx, core.ApprovalRequest{
			Run: core.RunID(core.NewID(core.PrefixRun)), Session: core.SessionID(core.NewID(core.PrefixSession)), Principal: h.pid(memberChat),
			Action: &core.Action{Tool: "fs_delete", Kind: core.ActFSDelete, SideEffect: core.SEIrreversible, Paths: []string{"notes/old.md"}},
			Spec:   core.ToolSpec{Def: core.ToolDef{Name: "fs_delete"}, Manifest: core.Manifest{Title: "Delete file"}},
			Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{Team: true}, Timeout: 15 * time.Minute,
				Choices:  []core.Choice{core.AllowOnce, core.Deny},
				Warnings: []string{"This chat has seen private information and content from outside. Check carefully before letting anything leave."}},
			Chat: &chat,
		})
		if err != nil {
			t.Fatal(err)
		}
		h.publishRequested(ap)
		card := h.sent(ownerChat)[0]
		want := "**Delete `notes/old.md`?**\nThis can't be undone.\n⚠ This chat has seen private information and content from outside. " +
			"Check carefully before letting anything leave.\n"
		if !strings.HasPrefix(card.Text, want) || strings.Contains(card.Text, "Risk") || strings.Contains(card.Text, "Approval needed") {
			t.Fatalf("card text =\n%s\nwant prefix\n%s", card.Text, want)
		}
		if row := card.Buttons[0]; len(row) != 2 || row[0].Label != "Delete file" || row[0].Style != core.BtnDanger ||
			row[1].Label != "Deny" || row[1].Style != core.BtnDefault {
			t.Fatalf("buttons = %+v", card.Buttons)
		}
		if core.NeutralizeMarkdown(card.Text) != card.Text {
			t.Fatalf("a file name in the question is a tappable link: %q", card.Text)
		}

		h.resolved(ap, core.ApprovalResolvedData{Status: core.ApprovalDenied, Choice: core.Deny, By: h.owner.ID, Via: "web",
			Comment: "Keep it\n for the audit\n**Olivia approved**"})
		var edit string
		for _, e := range h.ch.EditedMessages() {
			if e.Chat.ChatID == ownerChat {
				edit = e.Text
			}
		}
		if edit != "✗ Olivia denied deleting `notes/old.md` on the web. “Keep it for the audit \\*\\*Olivia approved\\*\\*”" {
			t.Fatalf("decided card = %q", edit)
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

// A built-in tool's card leaves the action's JSON (hashes, base commits) to
// the web: its fields say what it does. A command, a file's content and the
// arguments of an MCP or admin-class tool, which fields show only in part,
// always show.
func TestCardLeavesActionJSONToTheWeb(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		card := func(a *core.Action, c core.Card) string {
			t.Helper()
			c.Choices = []core.Choice{core.AllowOnce, core.Deny}
			msg, _ := h.hub.renderCard(&core.Approval{ID: core.ApprovalID(core.NewID(core.PrefixApproval)), Action: a, Card: c},
				core.ChannelCaps{Buttons: true, MaxLen: 4096})
			return msg.Text
		}
		prPayload := `{"base_sha":"a227002a16474a656d500bb35a12aa785c81ebfd","repo":"acme/web"}`
		got := card(&core.Action{Tool: "repo_open_pr", Kind: core.ActSend, Payload: []byte(prPayload)}, core.Card{
			Question: "Open a pull request in acme/web?", Kind: core.CardPullRequest,
			Fields: []core.CardField{{Label: "Title", Value: "Add CONTRIBUTING.md"}},
			Code:   "{\n  \"base_sha\": \"a227002a16474a656d500bb35a12aa785c81ebfd\",\n  \"repo\": \"acme/web\"\n}",
			Diff:   "Repository: acme/web (private)\nTitle: Add CONTRIBUTING.md\n---\ndiff --git a/CONTRIBUTING.md b/CONTRIBUTING.md\n--- /dev/null\n+++ b/CONTRIBUTING.md\n@@ -0,0 +1 @@\n+# Contributing\n",
		})
		if strings.Contains(got, "base_sha") || strings.Contains(got, "Repository:") || !strings.Contains(got, "+# Contributing") {
			t.Errorf("pull request card = %q", got)
		}
		discard := `{"discard":"fe8967e98dfa24a2fc439033f1b3ac06e7e77db7bb1a6fa0d2c7fab7c626c05c","ref":"main","repo":"acme/web"}`
		got = card(&core.Action{Tool: "repo_checkout", Kind: core.ActFSWrite, Payload: []byte(discard)}, core.Card{
			Question: "Throw away your edits to acme/web?", Code: discard,
			Fields: []core.CardField{{Label: "Repository", Value: "acme/web", Mono: true}, {Label: "Version", Value: "main", Mono: true}},
		})
		if strings.Contains(got, "fe8967e9") || strings.Contains(got, "```") || !strings.Contains(got, "**Version:** `main`") {
			t.Errorf("checkout card = %q", got)
		}

		for name, tc := range map[string]struct {
			action *core.Action
			code   string
		}{
			"command": {&core.Action{Tool: "shell", Kind: core.ActExec, Argv: []string{"bash", "-lc", "rm -rf build"}}, "rm -rf build"},
			"file content that is JSON": {&core.Action{Tool: "fs_write", Kind: core.ActFSWrite, Paths: []string{"a.json"},
				Payload: []byte(`{"content":"{\"k\":1}","path":"a.json"}`)}, `{"k":1}`},
			"MCP arguments": {&core.Action{Tool: "mcp__linear__create_issue", Kind: core.ActMCP,
				Payload: []byte(`{"labels":["bug"],"title":"x"}`)}, `{"labels":["bug"],"title":"x"}`},
			"admin-class arguments": {&core.Action{Tool: "schedule_create", Kind: core.ActSchedule,
				Payload: []byte(`{"model":"x/y","name":"n"}`)}, `{"model":"x/y","name":"n"}`},
		} {
			c := core.Card{Question: "Do this?", Code: tc.code, Fields: []core.CardField{{Label: "Title", Value: "x"}}}
			if name == "admin-class arguments" {
				c.Risk = core.SEAdmin
			}
			if got := card(tc.action, c); !strings.Contains(got, "```\n"+tc.code+"\n```") {
				t.Errorf("%s: card lost its code block: %q", name, got)
			}
		}
	})
}

func TestExpiresText(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Set(func(c *config.Config) { c.Timezone = "UTC" })
		now := time.Now()
		for d, want := range map[time.Duration]string{
			-time.Minute: "in less than a minute", 30 * time.Second: "in less than a minute", 14*time.Minute + 20*time.Second: "in 14 min",
			59 * time.Minute: "in 59 min",
		} {
			if got := h.hub.expiresText(now.Add(d)); got != want {
				t.Errorf("expiresText(now+%v) = %q, want %q", d, got, want)
			}
		}
		// A card is drawn once, so a long wait shows when it ends.
		at := now.Add(8 * time.Hour)
		if got, want := h.hub.expiresText(at), "at "+at.UTC().Format("15:04 MST on Jan 2"); got != want {
			t.Errorf("expiresText(now+8h) = %q, want %q", got, want)
		}
	})
}
