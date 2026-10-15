package channel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// Send delivers a system-originated message (message tool, schedule
// delivery, restart notices) to an explicit chat, split to the channel's
// MaxLen, with links neutralized when the chat's session is Exposed.
func (h *Hub) Send(ctx context.Context, chat core.ChatRef, markdown string) (core.MsgRef, error) {
	if strings.TrimSpace(markdown) == "" {
		return core.MsgRef{}, fmt.Errorf("channel send: empty message: %w", core.ErrInvalid)
	}
	text := h.redact(markdown)
	if h.chatExposed(ctx, chat) {
		text = core.NeutralizeMarkdown(text)
	}
	return h.sendText(ctx, chat, text, nil)
}

// chatExposed reports whether an open session of chat is Exposed (private
// and untrusted data in its context). A failed lookup counts as exposed.
func (h *Hub) chatExposed(ctx context.Context, chat core.ChatRef) bool {
	var principals []core.PrincipalID
	if chat.Kind == core.ChatGroup {
		sp, err := h.d.Store.Spaces().ByChat(ctx, chat.Channel, chat.ChatID)
		if errors.Is(err, core.ErrNotFound) {
			return false
		}
		if err != nil {
			return true
		}
		principals = append(principals, sp.Principal)
	} else {
		ids, err := h.d.Store.Identities().List(ctx, "")
		if err != nil {
			return true
		}
		for _, id := range ids {
			if id.Channel != chat.Channel || (id.DMChat != chat.ChatID && id.ExternalID != chat.ChatID) {
				continue
			}
			switch id.Status {
			case core.IdentityLinked:
				principals = append(principals, core.UserPrincipal(id.User))
			case core.IdentityGuest:
				principals = append(principals, id.Principal)
			}
		}
	}
	for _, p := range principals {
		s, err := h.d.Store.Sessions().ByChannelKey(ctx, p, chat.Key())
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil || s.Taint.Exposed() {
			return true
		}
	}
	return false
}

// PreferredChat returns the user's DM on the linked, enabled channel they
// used most recently.
func (h *Hub) PreferredChat(ctx context.Context, u core.UserID) (core.ChatRef, bool) {
	ids, err := h.d.Store.Identities().ForUser(ctx, u)
	if err != nil {
		h.log.Error("channel: listing identities failed", "user", u, "err", err)
		return core.ChatRef{}, false
	}
	// The chat app the person chose (Settings → Account) wins while it is
	// connected; otherwise the linked one they used last.
	pref := ""
	if usr, err := h.d.Store.Users().Get(ctx, u); err == nil {
		pref = usr.PreferredChat
	}
	var best *core.ChannelIdentity
	for _, id := range ids {
		if id.Status != core.IdentityLinked || id.User != u || id.DMChat == "" || h.live(id.Channel) == nil {
			continue
		}
		if id.Channel == pref {
			best = id
			break
		}
		if best == nil || id.LastSeen.After(best.LastSeen) {
			best = id
		}
	}
	if best == nil {
		return core.ChatRef{}, false
	}
	return core.ChatRef{Channel: best.Channel, ChatID: best.DMChat, Kind: core.ChatDM}, true
}

// dmOn returns the user's DM on one particular channel.
func (h *Hub) dmOn(ctx context.Context, u core.UserID, channel string) (core.ChatRef, bool) {
	if h.live(channel) == nil {
		return core.ChatRef{}, false
	}
	ids, err := h.d.Store.Identities().ForUser(ctx, u)
	if err != nil {
		return core.ChatRef{}, false
	}
	for _, id := range ids {
		if id.Status == core.IdentityLinked && id.User == u && id.Channel == channel && id.DMChat != "" {
			return core.ChatRef{Channel: channel, ChatID: id.DMChat, Kind: core.ChatDM}, true
		}
	}
	return core.ChatRef{}, false
}

// isDMOf reports whether chat is u's own DM with the bot.
func (h *Hub) isDMOf(ctx context.Context, u core.UserID, chat core.ChatRef) bool {
	if chat.Kind != core.ChatDM {
		return false
	}
	ids, err := h.d.Store.Identities().ForUser(ctx, u)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id.Status != core.IdentityLinked || id.User != u || id.Channel != chat.Channel {
			continue
		}
		if id.DMChat == chat.ChatID || (id.DMChat == "" && id.ExternalID == chat.ChatID) {
			return true
		}
	}
	return false
}

func (h *Hub) redact(s string) string {
	if h.d.Redactor == nil {
		return s
	}
	return h.d.Redactor.Redact(s)
}

// redactFor masks text kept for good on p's behalf: shared keys and the
// user's own personal keys, never another person's (vault.Redactor.ForScope).
func (h *Hub) redactFor(p *core.Principal, s string) string {
	sc, ok := h.d.Redactor.(interface {
		ForScope(core.Scope) core.Redactor
	})
	if !ok {
		return h.redact(s)
	}
	var own core.Scope
	if p != nil && p.Kind == core.PrincipalUser {
		own = core.UserScope(p.User)
	}
	return sc.ForScope(own).Redact(s)
}

// sendText redacts and splits markdown and sends the pieces in order; buttons
// go on the last piece. It returns the first piece's ref.
func (h *Hub) sendText(ctx context.Context, chat core.ChatRef, markdown string, buttons [][]core.Button) (core.MsgRef, error) {
	a := h.live(chat.Channel)
	if a == nil {
		return core.MsgRef{}, fmt.Errorf("channel %q is not available: %w", chat.Channel, core.ErrNotFound)
	}
	caps := a.ch.Caps()
	text := h.redact(markdown)
	if !caps.Buttons {
		text, buttons = inlineLinkButtons(text, buttons), nil
	}
	var first core.MsgRef
	pieces := splitAll(text, caps.MaxLen)
	for i, piece := range pieces {
		m := core.OutMessage{Chat: chat, Text: piece}
		if i == len(pieces)-1 {
			m.Buttons = buttons
		}
		ref, err := a.ch.Send(ctx, m)
		if err != nil {
			return first, fmt.Errorf("channel %s send: %w", chat.Channel, err)
		}
		if i == 0 {
			first = ref
		}
	}
	return first, nil
}

// inlineLinkButtons appends link buttons as text for channels that cannot
// show buttons; callback buttons have no text equivalent and are dropped.
func inlineLinkButtons(text string, buttons [][]core.Button) string {
	for _, row := range buttons {
		for _, b := range row {
			if b.URL != "" {
				text += "\n" + escapeMD(b.Label) + ": " + b.URL
			}
		}
	}
	return text
}

func (h *Hub) sendWithButtons(ctx context.Context, chat core.ChatRef, markdown string, buttons [][]core.Button) {
	if _, err := h.sendText(ctx, chat, markdown, buttons); err != nil {
		h.log.Warn("channel reply failed", "chat", chat.Key(), "err", err)
	}
}

func (h *Hub) reply(ctx context.Context, chat core.ChatRef, markdown string) {
	h.sendWithButtons(ctx, chat, markdown, nil)
}

// replyAsync sends without making the caller wait on the platform.
func (h *Hub) replyAsync(chat core.ChatRef, markdown string) {
	h.goAsync(func(ctx context.Context) { h.reply(ctx, chat, markdown) })
}

// replyOnce sends at most one message per key (one "Got it" per run).
func (h *Hub) replyOnce(ctx context.Context, chat core.ChatRef, key, markdown string) {
	h.mu.Lock()
	if _, done := h.replied[key]; done {
		h.mu.Unlock()
		return
	}
	h.replied[key] = time.Now()
	h.mu.Unlock()
	h.reply(ctx, chat, markdown)
}

// webURL returns server.public_url + route, or "" when no absolute public
// URL is configured (platforms reject loopback and relative button URLs).
func (h *Hub) webURL(route string) string {
	base := strings.TrimRight(h.cfg().Server.PublicURL, "/")
	if !isHTTPURL(base) {
		return ""
	}
	return base + route
}

func (h *Hub) webButton(label, route string) [][]core.Button {
	u := h.webURL(route)
	if u == "" {
		return nil
	}
	return [][]core.Button{{{Label: label, URL: u}}}
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}
