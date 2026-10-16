package channel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
)

// slashChannel stands in for Slack, where a DM has an id of its own ("D" +
// the user's) rather than Telegram's user id; it records each OpenDM.
type slashChannel struct {
	*channeltest.Channel
	mu     sync.Mutex
	opened []string
}

func (c *slashChannel) OpenDM(_ context.Context, user string) (core.ChatRef, error) {
	c.mu.Lock()
	c.opened = append(c.opened, user)
	c.mu.Unlock()
	return core.ChatRef{Channel: c.NameV, ChatID: "D" + user, Kind: core.ChatDM}, nil
}

func (c *slashChannel) openedDMs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.opened)
}

func newSlashHarness(t *testing.T) (*harness, *slashChannel) {
	var sc *slashChannel
	h := newHarness(t, func(h *harness) {
		h.wrap = func(ch *channeltest.Channel) core.Channel {
			sc = &slashChannel{Channel: ch}
			return sc
		}
	}).start()
	return h, sc
}

// slash injects what a slash command typed in a group becomes: a Private,
// mentioned message.
func (h *harness) slash(chat, sender, text string) {
	h.inject(core.Inbound{Chat: h.groupChat(chat), SenderID: sender, SenderName: "User " + sender, Text: text, Mentioned: true, Private: true})
}

func (h *harness) wantNoSpace(chat string) {
	h.t.Helper()
	if sp, err := h.store.Spaces().ByChat(h.ctx, "telegram", chat); !errors.Is(err, core.ErrNotFound) {
		h.t.Fatalf("chat %s has space %+v (%v); a private message must never create one", chat, sp, err)
	}
}

func (h *harness) dmChatOf(ext string) string {
	h.t.Helper()
	id, err := h.store.Identities().Get(h.ctx, "telegram", ext)
	if err != nil {
		h.t.Fatal(err)
	}
	return id.DMChat
}

// The incident: someone not linked yet sends "/bothy pair CODE" in a
// channel. It pairs them in their DM, like a DM would, and leaves no space
// behind for the channel.
func TestPrivatePairingInGroupFromUnknownSender(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h, sc := newSlashHarness(t)
		const sender = "3003"
		h.auth.Codes = map[string]core.UserID{"ABCDEFGH": h.member.ID}
		h.slash("C100", sender, "/pair ABCD-EFGH")
		if got := h.auth.Redeemed; len(got) != 1 || got[0] != "telegram:"+sender+"|ABCD-EFGH" {
			t.Fatalf("redeemed = %v", got)
		}
		if len(h.auth.Burned) != 0 {
			t.Fatalf("a private code was burned: %v", h.auth.Burned)
		}
		h.wantVisible("D"+sender, "Linked to Mark.")
		h.wantVisible("D"+sender, helloText)
		if got := sc.openedDMs(); !slices.Equal(got, []string{sender}) {
			t.Fatalf("OpenDM calls = %v", got)
		}
		if dm := h.dmChatOf(sender); dm != "D"+sender {
			t.Fatalf("DM chat = %q, want the one OpenDM gave", dm)
		}
		if v := h.visible("C100"); len(v) != 0 {
			t.Fatalf("the channel shows %q", v)
		}
		h.wantNoSpace("C100")

		h.slash("C100", "3004", "/pair WRONG-CODE")
		h.wantVisible("D3004", "That code didn't work.")
		h.wantNoSpace("C100")
	})
}

// Anything else from an unknown sender gets the daily notice in their DM,
// never a run, and never a space.
func TestPrivateMessageInGroupFromUnknownSender(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h, _ := newSlashHarness(t)
		const stranger = "5005"
		h.slash("C110", stranger, "what can you do?")
		h.slash("C111", stranger, "hello?")
		replies := h.visible("D" + stranger)
		if len(replies) != 1 || !strings.HasPrefix(replies[0], "Bothy only works with people on this team.") {
			t.Fatalf("replies = %q, want one notice in the DM", replies)
		}
		if n := len(h.auth.Notices); n != 2 || h.auth.Notices[0].DMChat != "D"+stranger {
			t.Fatalf("notices = %+v, want 2 carrying the DM chat", h.auth.Notices)
		}
		if n := len(h.runner.Submitted()); n != 0 {
			t.Fatalf("an unknown sender reached the runner %d times", n)
		}
		h.wantNoSpace("C110")
		h.wantNoSpace("C111")

		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		h.slash("C110", "5006", "hi")
		if len(h.visible("D5006")) != 0 || len(h.auth.Notices) != 2 {
			t.Fatal("a rate-limited unknown sender was answered or recorded")
		}
	})
}

// A known sender where Bothy is off hears so in their DM, at most once an
// hour per channel; in an enabled space nothing changes.
func TestPrivateMessageInGroupFromKnownSender(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h, _ := newSlashHarness(t)
		dm := "D" + memberChat
		h.slash("C200", memberChat, "/status")
		if v := h.visible(dm); len(v) != 1 || v[0] != offHere {
			t.Fatalf("DM = %q, want the off notice", v)
		}
		h.wantNoSpace("C200")
		h.slash("C200", memberChat, "/status")
		if n := len(h.visible(dm)); n != 1 {
			t.Fatalf("%d notices within the hour, want 1", n)
		}
		h.space("C201", false, false)
		h.slash("C201", memberChat, "summarize")
		if n := len(h.visible(dm)); n != 2 {
			t.Fatalf("a disabled space got no notice of its own: %q", h.visible(dm))
		}
		time.Sleep(offEvery + time.Minute)
		h.slash("C200", memberChat, "/status")
		if n := len(h.visible(dm)); n != 3 {
			t.Fatalf("no notice after the hour: %q", h.visible(dm))
		}
		if len(h.visible("C200"))+len(h.visible("C201")) != 0 || len(h.runner.Submitted()) != 0 {
			t.Fatal("a group Bothy is off in was answered or reached the runner")
		}

		sp := h.space("C202", true, true)
		h.auth.Codes = map[string]core.UserID{"ABCDEFGH": h.owner.ID}
		h.slash("C202", memberChat, "summarize the thread")
		if subs := h.runner.Submitted(); len(subs) != 1 || subs[0].Principal != sp.Principal || subs[0].Chat.ChatID != "C202" {
			t.Fatalf("enabled space: submits = %+v", subs)
		}
		h.slash("C202", memberChat, "/pair ABCD-EFGH")
		if got := h.lastText("C202"); got != "This account is already linked." {
			t.Fatalf("a private /pair in an enabled space got %q", got)
		}
		if len(h.auth.Burned) != 0 || len(h.auth.Codes) != 1 {
			t.Fatal("a code nobody else saw was burned")
		}

		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		h.slash("C203", ownerChat, "/status")
		if v := h.visible("D" + ownerChat); len(v) != 0 {
			t.Fatalf("a rate-limited sender got %q", v)
		}
	})
}

// A live code posted where the group can read it is cancelled, wherever it
// was posted and by whoever; anything else goes on as before.
func TestCodePostedInGroupIsBurned(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.auth.Codes = map[string]core.UserID{"ABCDEFGH": h.member.ID, "BCDEFGHJ": h.member.ID, "CDEFGHJK": h.owner.ID}

		h.group("-900", "3005", "/pair ABCD-EFGH", false)
		if got := h.auth.Burned; len(got) != 1 || got[0] != "telegram:3005|ABCD-EFGH" {
			t.Fatalf("burned = %v", got)
		}
		if v := h.visible("-900"); len(v) != 1 || v[0] != codeShared {
			t.Fatalf("group shows %q, want the notice", v)
		}
		h.wantNoSpace("-900")
		if len(h.auth.Redeemed) != 0 {
			t.Fatal("a code posted in a group was redeemed")
		}

		h.space("-901", false, false)
		h.group("-901", memberChat, "/pair@bothy_bot BCDEFGHJ", true)
		if len(h.auth.Burned) != 2 || h.lastText("-901") != codeShared {
			t.Fatalf("a disabled space: burned %v, shows %q", h.auth.Burned, h.visible("-901"))
		}

		h.space("-902", true, false)
		h.group("-902", ownerChat, "/start CDEF-GHJK", false)
		if len(h.auth.Burned) != 3 || h.lastText("-902") != codeShared || len(h.runner.Submitted()) != 0 {
			t.Fatalf("an enabled space: burned %v, shows %q", h.auth.Burned, h.visible("-902"))
		}

		h.auth.Codes = map[string]core.UserID{"DEFGHJKM": h.member.ID}
		h.group("-902", ownerChat, "/pair ZZZZ-ZZZZ", false)
		if got := h.lastText("-902"); len(h.auth.Burned) != 3 || !strings.HasPrefix(got, "Pairing codes only work in a direct message") {
			t.Fatalf("a dead code: burned %v, reply %q", h.auth.Burned, got)
		}
		h.inject(core.Inbound{Chat: h.groupChat("-902"), SenderID: ownerChat, Text: "/pair DEFG-HJKM", Mentioned: true, Forwarded: true})
		if len(h.auth.Burned) != 3 {
			t.Fatal("a forwarded code was burned")
		}
		h.group("-903", memberChat, "/start", true)
		if _, err := h.store.Spaces().ByChat(h.ctx, "telegram", "-903"); err != nil {
			t.Fatalf("a bare /start no longer records the group: %v", err)
		}

		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		h.group("-904", "3006", "/pair DEFG-HJKM", false)
		if len(h.auth.Burned) != 4 || len(h.visible("-904")) != 0 {
			t.Fatalf("over the limit: burned %v, group shows %q; want burned without a reply", h.auth.Burned, h.visible("-904"))
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
		if sp.Title != "#eng" || p.DisplayName != "#eng" {
			t.Fatalf("after a titled message: space %q, principal %q; want #eng", sp.Title, p.DisplayName)
		}

		sp.Title = "Engineering"
		if err := h.store.Spaces().Update(h.ctx, sp); err != nil {
			t.Fatal(err)
		}
		h.inject(core.Inbound{Chat: h.groupChat("-950"), SenderID: ownerChat, Text: "more", Mentioned: true, ChatTitle: "#eng-old"})
		if sp, _ = h.store.Spaces().ByChat(h.ctx, "telegram", "-950"); sp.Title != "Engineering" {
			t.Fatalf("an admin's title became %q", sp.Title)
		}

		// A slash command to a disabled space names it too.
		h.group("-951", ownerChat, "hi", true)
		h.inject(core.Inbound{Chat: h.groupChat("-951"), SenderID: ownerChat, Text: "/status", Mentioned: true, Private: true, ChatTitle: "#ops"})
		if sp, _ = h.store.Spaces().ByChat(h.ctx, "telegram", "-951"); sp.Title != "#ops" {
			t.Fatalf("space = %q after a titled slash command", sp.Title)
		}
	})
}
