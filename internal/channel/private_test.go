package channel

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
)

// slash injects what a slash command typed in a group becomes: a Private,
// mentioned message from a channel Slack names "#<chat>", whose ReplyRef
// answers its sender alone there.
func (h *harness) slash(chat, sender, text string) {
	h.inject(core.Inbound{Chat: h.groupChat(chat), ChatTitle: "#" + chat, SenderID: sender, SenderName: "User " + sender, Text: text,
		Mentioned: true, Private: true, ReplyRef: hookRef(chat, sender)})
}

func hookRef(chat, sender string) string {
	return "https://hooks.slack.com/commands/" + chat + "/" + sender
}

// private returns what sender alone saw where they typed slash commands.
func (h *harness) private(sender string) []string {
	var out []string
	for _, r := range h.ch.PrivateReplies() {
		if r.Sender == sender {
			out = append(out, r.Text)
		}
	}
	return out
}

func (h *harness) wantNoSpace(chat string) {
	h.t.Helper()
	if sp, err := h.store.Spaces().ByChat(h.ctx, h.app, chat); !errors.Is(err, core.ErrNotFound) {
		h.t.Fatalf("chat %s has space %+v (%v); a private message must never create one", chat, sp, err)
	}
}

// TestPrivateSlashInOffChannel: a slash command in a channel where Bothy is
// off (or isn't) tells a teammate so, there and to them alone, once an hour
// per channel; someone outside the team gets the outsider line and a failed
// lookup says so, in their DM, as a DM would. It never records a channel
// and never reaches the runner.
func TestPrivateSlashInOffChannel(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.slash("C200", memberChat, "/status")
		if v := h.private(memberChat); len(v) != 1 || v[0] != "Bothy is off in #C200. Message Bothy directly, or ask an admin to turn #C200 on." {
			t.Fatalf("private replies = %q, want the off notice naming the channel", v)
		}
		if r := h.ch.PrivateReplies()[0]; r.Chat.ChatID != "C200" || r.Ref != hookRef("C200", memberChat) {
			t.Fatalf("the notice went to %+v, want C200 through the command's reference", r)
		}
		h.wantNoSpace("C200")
		h.slash("C200", memberChat, "/status")
		if n := len(h.private(memberChat)); n != 1 {
			t.Fatalf("%d notices within the hour, want 1", n)
		}
		h.space("C201", false, false)
		h.slash("C201", memberChat, "summarize")
		if v := h.private(memberChat); len(v) != 2 || v[1] != offHere("Team") {
			t.Fatalf("an off channel got no notice of its own, by the name admins gave it: %q", v)
		}
		time.Sleep(offEvery + time.Minute)
		h.slash("C200", memberChat, "/status")
		if n := len(h.private(memberChat)); n != 3 {
			t.Fatalf("no notice after the hour: %q", h.private(memberChat))
		}
		if v := h.visible(memberChat); len(v) != 0 {
			t.Fatalf("the teammate's DM got %q too", v)
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
		if n := len(h.private(guest)) + len(h.private("U0000902")); n != 0 {
			t.Fatalf("%d private replies to people Bothy doesn't answer in channels", n)
		}
		if len(h.visible("C200"))+len(h.visible("C201")) != 0 || len(h.runner.Submitted()) != 0 {
			t.Fatal("a channel Bothy is off in was answered or reached the runner")
		}
		h.wantNoSpace("C200")

		h.mc.SetMemberErr(nil)
		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		h.slash("C203", ownerChat, "/status")
		if v, p := h.visible(ownerChat), h.private(ownerChat); len(v)+len(p) != 0 {
			t.Fatalf("a rate-limited sender got %q and %q", v, p)
		}
	})
}

// Wherever Bothy is off, the help list is the answer to /bothy help, with
// no notice beside it; anything else still gets the notice.
func TestPrivateSlashHelpInOffChannel(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.slash("C210", memberChat, "/help")
		if v := h.private(memberChat); len(v) != 1 || v[0] != helpText(h.ch.Caps()) || !strings.Contains(v[0], "\n`/bothy stop` – stop the current task\n") {
			t.Fatalf("/help in an off channel = %q", v)
		}
		h.slash("C210", memberChat, "/status")
		if v := h.private(memberChat); len(v) != 2 || v[1] != offHere("#C210") {
			t.Fatalf("/status after the help = %q, want the off notice", v)
		}
		if len(h.visible(memberChat))+len(h.visible("C210")) != 0 || len(h.runner.Submitted()) != 0 {
			t.Fatal("an off channel's slash commands reached a DM, the channel or the runner")
		}
	})
}

// Without a way to answer where the command was typed (no reference, a post
// that fails, a chat app that can't), the answer goes to the sender's DM,
// worded for it, and only there.
func TestPrivateSlashFallsBackToDM(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.inject(core.Inbound{Chat: h.groupChat("C220"), ChatTitle: "#eng", SenderID: memberChat, Text: "/status", Mentioned: true, Private: true})
		if v := h.visible(memberChat); len(v) != 1 || v[0] != "Bothy is off in #eng. You can message Bothy here, or ask an admin to turn #eng on." {
			t.Fatalf("without a reference, DM = %q", v)
		}
		h.ch.SetPrivateErr(errors.New("expired_url"))
		h.slash("C221", memberChat, "/help")
		if v := h.visible(memberChat); len(v) != 2 || v[1] != helpText(h.ch.Caps()) {
			t.Fatalf("after a failed private reply, DM = %q", v)
		}
		// An answer about a channel Bothy is on in names it in the DM, where
		// "this chat" or "here" would be the DM.
		h.space("C222", true, true)
		for _, tc := range []struct{ sender, text, want string }{
			{ownerChat, "/usage", "In Team: Today: $0.00, no daily limit. This conversation: $0.00."},
			{memberChat, "/status", "In Team:\nNo task running.\nToday: $0.00, no daily limit."},
			{memberChat, "/new", "In Team: Only someone who can approve can use /bothy new."},
			{ownerChat, "/pin keep it short", "In Team: Pinned. /bothy pins lists them."},
			{ownerChat, "/compact", "In Team: Older messages were summarised."},
		} {
			n := len(h.visible(tc.sender))
			h.slash("C222", tc.sender, tc.text)
			if v := h.visible(tc.sender); len(v) != n+1 || v[n] != tc.want {
				t.Fatalf("%s in an on channel after a failed private reply: DM = %q, want one more: %q", tc.text, v, tc.want)
			}
		}
		if n := len(h.ch.PrivateReplies()); n != 0 || len(h.visible("C222")) != 0 {
			t.Fatalf("%d private replies, channel shows %q", n, h.visible("C222"))
		}
	})
	bubble(t, func(t *testing.T) {
		h := newHarness(t, func(h *harness) { h.wrap = func(c *channeltest.Channel) core.Channel { return plainChannel{c} } }).start()
		h.slash("-700", ownerChat, "/status")
		if v := h.visible(ownerChat); len(v) != 1 || v[0] != offIn("#-700") || len(h.ch.PrivateReplies()) != 0 {
			t.Fatalf("on a chat app without private replies, DM = %q", v)
		}
	})
}

// plainChannel is a chat app with nothing beyond core.Channel.
type plainChannel struct{ core.Channel }

// In a channel Bothy is on in, a teammate's slash command takes the normal
// path, as the channel's: free text starts a task there, and a command's
// answer is shown to the sender alone, never in the channel or a DM. A
// slash command in the bot's own DM is answered there as any message.
func TestPrivateSlashInOnChannel(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		sp := h.space("C202", true, true)
		h.slash("C202", memberChat, "summarize the thread")
		subs := h.runner.Submitted()
		if len(subs) != 1 || subs[0].Principal != sp.Principal || subs[0].Chat.ChatID != "C202" || subs[0].Provenance != core.ProvUntrusted {
			t.Fatalf("on channel: submits = %+v", subs)
		}
		if run := fmt.Sprintf("%+v %+v", subs[0].Input, *subs[0].Chat); strings.Contains(run, "hooks.slack.com") {
			t.Fatalf("the reply reference reached the run: %s", run)
		}
		const outsider = "U0000903"
		h.slash("C202", outsider, "summarize again")
		h.slash("C202", outsider, "/status")
		if n := len(h.runner.Submitted()); n != 1 || len(h.private(outsider))+len(h.visible(outsider)) != 0 {
			t.Fatalf("someone outside the team was answered in a channel (%d runs)", n)
		}

		for _, tc := range []struct{ sender, text, want string }{
			{memberChat, "/status", "No task running.\nToday: $0.00, no daily limit."},
			{memberChat, "/new", "Only someone who can approve can use /bothy new here."},
			{memberChat, "/foo", "There's no /foo command. /bothy help lists the commands."},
			{ownerChat, "/pin keep it short", "Pinned. /bothy pins lists them."},
			{ownerChat, "/compact", "Older messages were summarised."},
		} {
			n := len(h.private(tc.sender))
			h.slash("C202", tc.sender, tc.text)
			if v := h.private(tc.sender); len(v) != n+1 || v[n] != tc.want {
				t.Fatalf("%s in an on channel: private replies %q, want one more: %q", tc.text, v, tc.want)
			}
		}
		if v := h.visible("C202"); len(v) != 0 {
			t.Fatalf("the channel shows %q", v)
		}
		if n := len(h.visible(memberChat)) + len(h.visible(ownerChat)); n != 0 {
			t.Fatalf("%d command answers went to a DM as well", n)
		}

		h.inject(core.Inbound{Chat: h.dmChat(memberChat), SenderID: memberChat, Text: "/usage", Mentioned: true, Private: true})
		if v := h.visible(memberChat); len(v) != 1 || !strings.HasPrefix(v[0], "Today: ") {
			t.Fatalf("a slash command in the bot's DM: DM = %q", v)
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
