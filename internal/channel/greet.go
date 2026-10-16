package channel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	approverText = "An admin made you an approver. Bothy will send you requests from people in chat apps here, to approve or deny."
	// budgetDMEvery: one note a day per teammate about their daily limit.
	budgetDMEvery = 20 * time.Hour
)

// helloText greets a teammate the first time they may use Bothy.
func helloText(name string) string {
	const rest = "I'm Bothy. Ask me anything. When something needs approval, I'll ask an admin and tell you here."
	if name = strings.TrimSpace(name); name == "" {
		return "Hi, " + rest
	}
	return "Hi " + inert(revealInvisible(name)) + ", " + rest
}

// movedText greets someone whose chat was linked to a Bothy account before.
func movedText(name string) string {
	const rest = "this chat is no longer tied to your Bothy account, so your earlier chats and notes stay in Bothy on the web. " +
		"Ask me anything; when something needs approval, I'll ask an admin and tell you here."
	if name = strings.TrimSpace(name); name == "" {
		return "Hi, " + rest
	}
	return "Hi " + inert(revealInvisible(name)) + ", " + rest
}

func greeting(id *core.ChannelIdentity) string {
	if id.Greet == core.GreetMoved {
		return movedText(id.DisplayName)
	}
	return helloText(id.DisplayName)
}

// Greet sends a teammate their pending greeting once (core.ChannelHub);
// httpapi calls it after an admin lets someone in.
func (h *Hub) Greet(ctx context.Context, channel, externalID string) error {
	a := h.live(channel)
	if a == nil {
		return fmt.Errorf("channel %q is not available: %w", channel, core.ErrNotFound)
	}
	id, err := h.d.Store.Identities().Get(ctx, channel, externalID)
	if err != nil {
		return err
	}
	if id.Greet == "" {
		return nil
	}
	chat, err := h.dmOf(ctx, a, *id)
	if err != nil {
		return err
	}
	return h.greetIn(ctx, chat, channel, externalID)
}

// greetIn sends the pending greeting to chat and then marks it sent; a
// greeting already sent is not repeated.
func (h *Hub) greetIn(ctx context.Context, chat core.ChatRef, channel, externalID string) error {
	id, err := h.d.Store.Identities().Get(ctx, channel, externalID)
	if err != nil || id.Greet == "" {
		return err
	}
	if _, err := h.sendText(ctx, chat, greeting(id), nil); err != nil {
		h.log.Warn("channel: greeting failed", "channel", channel, "err", err)
		return err
	}
	return h.d.Store.Tx(ctx, func(tx core.Store) error {
		cur, err := tx.Identities().Get(ctx, channel, externalID)
		if err != nil || cur.Greet == "" {
			return err
		}
		cur.Greet = ""
		return tx.Identities().Update(ctx, cur)
	})
}

// TellApprover tells a teammate an admin made them an approver
// (core.ChannelHub).
func (h *Hub) TellApprover(ctx context.Context, channel, externalID string) error {
	a := h.live(channel)
	if a == nil {
		return fmt.Errorf("channel %q is not available: %w", channel, core.ErrNotFound)
	}
	id, err := h.d.Store.Identities().Get(ctx, channel, externalID)
	if err != nil {
		return err
	}
	chat, err := h.dmOf(ctx, a, *id)
	if err != nil {
		return err
	}
	_, err = h.sendText(ctx, chat, approverText, nil)
	return err
}

// onBudgetWarning DMs a teammate whose own daily limit passed
// budgets.warn_ratio, once a day, with that limit's figures: Daily would
// name whichever cap is closest to running out.
func (h *Hub) onBudgetWarning(e core.Event, w core.BudgetWarningData) {
	if e.Principal == "" || w.Limit <= 0 {
		return
	}
	p := e.Principal
	h.goAsync(func(ctx context.Context) {
		pr, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), p)
		if err != nil || pr.Kind != core.PrincipalPerson || !h.noteOnce("budget80:"+string(p), budgetDMEvery) {
			return
		}
		chat, ok := h.PrincipalChat(ctx, p)
		if !ok {
			return
		}
		text := fmt.Sprintf("You've used %d%% of your %s daily limit in Bothy.", min(w.Spent*100/w.Limit, 100), moneyText(w.Limit))
		h.enqueueChat(chat.Key(), func(ctx context.Context) { h.reply(ctx, chat, text) })
	})
}
