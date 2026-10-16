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
		ids, err := h.d.Store.Identities().List(ctx, core.IdentityTeammate)
		if err != nil {
			return true
		}
		for _, id := range ids {
			if id.Channel == chat.Channel && id.Principal != "" && (id.DMChat == chat.ChatID || id.ExternalID == chat.ChatID) {
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

// PrincipalChat is where Bothy's own messages to p go (core.ChannelHub):
// a teammate's DM while they may use Bothy, a channel's chat while it is
// on. Web accounts hear on the web only.
func (h *Hub) PrincipalChat(ctx context.Context, p core.PrincipalID) (core.ChatRef, bool) {
	pr, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), p)
	if err != nil {
		return core.ChatRef{}, false
	}
	switch pr.Kind {
	case core.PrincipalPerson:
		id, a, res, err := h.teammate(ctx, p)
		if err != nil || !h.usable(a, res, core.MemberStale) {
			return core.ChatRef{}, false
		}
		chat, err := h.dmOf(ctx, a, *id)
		return chat, err == nil
	case core.PrincipalSpace:
		sp, err := h.d.Store.Spaces().Get(ctx, core.SystemViewer(), p)
		if err != nil || !sp.Enabled || h.live(sp.Chat.Channel) == nil {
			return core.ChatRef{}, false
		}
		chat := sp.Chat
		chat.Kind = core.ChatGroup
		return chat, true
	}
	return core.ChatRef{}, false
}

// TeamActive reports why a team-owned principal can't run a schedule now
// (core.ChannelHub). A teammate's verdict older than an hour is renewed
// first; when the platform doesn't answer, one under four hours old counts.
func (h *Hub) TeamActive(ctx context.Context, p core.PrincipalID) error {
	pr, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), p)
	if err != nil {
		return err
	}
	switch pr.Kind {
	case core.PrincipalPerson:
		id, a, res, err := h.teammate(ctx, p)
		if err != nil {
			return err
		}
		if _, ok := checkerOf(a); ok && res.Admitted() && !h.fresh(res, core.MemberTTL) {
			if r, err := h.recheck(ctx, a, *id, recheckWait); err == nil {
				res = r
			}
		}
		switch {
		case res.Member == core.MemberOutsider:
			return errors.New("they're no longer in the team")
		case !res.Admitted():
			return errors.New("they can't use Bothy now")
		case !h.usable(a, res, core.MemberStale):
			return errors.New("their membership couldn't be checked")
		}
		return nil
	case core.PrincipalSpace:
		sp, err := h.d.Store.Spaces().Get(ctx, core.SystemViewer(), p)
		if err != nil {
			return err
		}
		if !sp.Enabled {
			return errors.New("the channel is off")
		}
		if h.live(sp.Chat.Channel) == nil {
			return errors.New(channelTitle(sp.Chat.Channel) + " is off")
		}
	}
	return nil
}

// teammate returns a person principal's identity, its live adapter and a
// resolution read after the membership scope is synced.
func (h *Hub) teammate(ctx context.Context, p core.PrincipalID) (*core.ChannelIdentity, *adapter, core.IdentityResolution, error) {
	id, err := h.d.Store.Identities().ByPrincipal(ctx, p)
	if err != nil {
		return nil, nil, core.IdentityResolution{}, err
	}
	if id.Status == core.IdentityBlocked {
		return nil, nil, core.IdentityResolution{}, errors.New("they're blocked")
	}
	a := h.live(id.Channel)
	if a == nil {
		return nil, nil, core.IdentityResolution{}, errors.New(channelTitle(id.Channel) + " is off")
	}
	if mc, ok := checkerOf(a); ok {
		h.syncScope(ctx, a, mc)
	}
	res, err := h.d.Auth.ResolveIdentity(ctx, id.Channel, id.ExternalID)
	if err != nil {
		return nil, nil, core.IdentityResolution{}, err
	}
	return id, a, res, nil
}

// dmOf returns a person's DM with the bot, opening (and remembering) it
// when Bothy hasn't seen it yet.
func (h *Hub) dmOf(ctx context.Context, a *adapter, id core.ChannelIdentity) (core.ChatRef, error) {
	if id.DMChat != "" {
		return core.ChatRef{Channel: a.name, ChatID: id.DMChat, Kind: core.ChatDM}, nil
	}
	dm, err := a.ch.OpenDM(ctx, id.ExternalID)
	if err != nil {
		return core.ChatRef{}, err
	}
	if _, err := h.rememberDM(ctx, id, dm.ChatID); err != nil {
		return core.ChatRef{}, err
	}
	return core.ChatRef{Channel: a.name, ChatID: dm.ChatID, Kind: core.ChatDM}, nil
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
