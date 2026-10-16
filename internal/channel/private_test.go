package channel

import (
	"errors"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// slash injects what a slash command typed in a group becomes: a Private,
// mentioned message, from a channel Slack names "#<chat>".
func (h *harness) slash(chat, sender, text string) {
	h.inject(core.Inbound{Chat: h.groupChat(chat), ChatTitle: "#" + chat, SenderID: sender, SenderName: "User " + sender, Text: text,
		Mentioned: true, Private: true})
}

func (h *harness) wantNoSpace(chat string) {
	h.t.Helper()
	if sp, err := h.store.Spaces().ByChat(h.ctx, h.app, chat); !errors.Is(err, core.ErrNotFound) {
		h.t.Fatalf("chat %s has space %+v (%v); a private message must never create one", chat, sp, err)
	}
}

// TestPrivateSlashInOffChannel: a slash command in a channel where Bothy is
// off (or isn't) is answered in the sender's DM, as a DM would be: a
// teammate hears the channel is off (once an hour per channel), someone
// outside the team gets the outsider line, and a failed lookup says so.
// It never records a channel and never reaches the runner.
func TestPrivateSlashInOffChannel(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.slash("C200", memberChat, "/status")
		if v := h.visible(memberChat); len(v) != 1 || v[0] != "Bothy is off in #C200. You can message Bothy here, or ask an admin to turn #C200 on." {
			t.Fatalf("DM = %q, want the off notice naming the channel", v)
		}
		h.wantNoSpace("C200")
		h.slash("C200", memberChat, "/status")
		if n := len(h.visible(memberChat)); n != 1 {
			t.Fatalf("%d notices within the hour, want 1", n)
		}
		h.space("C201", false, false)
		h.slash("C201", memberChat, "summarize")
		if v := h.visible(memberChat); len(v) != 2 || v[1] != offIn("Team") {
			t.Fatalf("an off channel got no notice of its own, by the name admins gave it: %q", v)
		}
		time.Sleep(offEvery + time.Minute)
		h.slash("C200", memberChat, "/status")
		if n := len(h.visible(memberChat)); n != 3 {
			t.Fatalf("no notice after the hour: %q", h.visible(memberChat))
		}

		const guest = "U0000901"
		h.mc.SetMember(guest, core.Membership{Status: core.MemberOutsider, Team: "Acme"})
		h.slash("C200", guest, "hello?")
		if v := h.visible(guest); len(v) != 1 || v[0] != "Bothy works with members of this workspace." {
			t.Fatalf("outsider's DM = %q", v)
		}

		h.mc.SetMemberErr(errors.New("slack is down"))
		h.slash("C200", "U0000902", "hi")
		if v := h.visible("U0000902"); len(v) != 1 || v[0] != cantCheck {
			t.Fatalf("lookup failure DM = %q", v)
		}
		if len(h.visible("C200"))+len(h.visible("C201")) != 0 || len(h.runner.Submitted()) != 0 {
			t.Fatal("a channel Bothy is off in was answered or reached the runner")
		}
		h.wantNoSpace("C200")

		h.mc.SetMemberErr(nil)
		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		h.slash("C203", ownerChat, "/status")
		if v := h.visible(ownerChat); len(v) != 0 {
			t.Fatalf("a rate-limited sender got %q", v)
		}
	})
}

// In a channel Bothy is on in, a teammate's slash command takes the normal
// path, as the channel's.
func TestPrivateSlashInOnChannel(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		sp := h.space("C202", true, true)
		h.slash("C202", memberChat, "summarize the thread")
		if subs := h.runner.Submitted(); len(subs) != 1 || subs[0].Principal != sp.Principal || subs[0].Chat.ChatID != "C202" ||
			subs[0].Provenance != core.ProvUntrusted {
			t.Fatalf("on channel: submits = %+v", subs)
		}
		h.slash("C202", "U0000903", "summarize again")
		if n := len(h.runner.Submitted()); n != 1 {
			t.Fatalf("someone outside the team reached the runner in a channel (%d runs)", n)
		}
	})
}

// A space created before its platform gave a title takes one from a later
// message, the principal too; a title anyone set stays.
func TestSpaceRenamedFromFallbackTitle(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.group("-950", ownerChat, "hello", true)
		sp, err := h.store.Spaces().ByChat(h.ctx, "telegram", "-950")
		if err != nil || sp.Title != "Telegram group -950" {
			t.Fatalf("space = %+v, %v", sp, err)
		}
		h.inject(core.Inbound{Chat: h.groupChat("-950"), SenderID: ownerChat, Text: "again", Mentioned: true, ChatTitle: "#eng​\n"})
		sp, _ = h.store.Spaces().ByChat(h.ctx, "telegram", "-950")
		p, _ := h.store.Principals().Get(h.ctx, core.SystemViewer(), sp.Principal)
		if sp.Title != "#eng" || p.DisplayName != "#eng" || !sp.ReviewedAt.IsZero() {
			t.Fatalf("after a titled message: space %+v, principal %q; want #eng, still unreviewed", sp, p.DisplayName)
		}

		sp.Title = "Engineering"
		if err := h.store.Spaces().Update(h.ctx, sp); err != nil {
			t.Fatal(err)
		}
		h.inject(core.Inbound{Chat: h.groupChat("-950"), SenderID: ownerChat, Text: "more", Mentioned: true, ChatTitle: "#eng-old"})
		if sp, _ = h.store.Spaces().ByChat(h.ctx, "telegram", "-950"); sp.Title != "Engineering" {
			t.Fatalf("an admin's title became %q", sp.Title)
		}

		// A slash command to an off space names it too.
		h.group("-951", ownerChat, "hi", true)
		h.inject(core.Inbound{Chat: h.groupChat("-951"), SenderID: ownerChat, Text: "/status", Mentioned: true, Private: true, ChatTitle: "#ops"})
		if sp, _ = h.store.Spaces().ByChat(h.ctx, "telegram", "-951"); sp.Title != "#ops" {
			t.Fatalf("space = %q after a titled slash command", sp.Title)
		}
	})
}
