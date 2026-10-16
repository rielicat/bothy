package discord

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/rielicat/bothy/internal/core"
)

const (
	maxRows     = 5
	maxPerRow   = 5
	maxLabel    = 80
	maxCustomID = 100
)

// cdnHosts serve message attachments; attachment URLs are signed, so no
// token is sent with them.
var cdnHosts = map[string]bool{"cdn.discordapp.com": true, "media.discordapp.net": true}

func (a *adapter) Send(ctx context.Context, m core.OutMessage) (core.MsgRef, error) {
	target, err := channelOf(m.Chat)
	if err != nil {
		return core.MsgRef{}, err
	}
	content := a.content(m.Text, m.Mentions...)
	rows, err := components(m.Buttons)
	if err != nil {
		return core.MsgRef{}, err
	}
	if content == "" && len(rows) == 0 {
		return core.MsgRef{}, fmt.Errorf("discord: empty message: %w", core.ErrInvalid)
	}
	mc := discord.MessageCreate{
		Content:         content,
		Components:      rows,
		Flags:           discord.MessageFlagSuppressEmbeds,
		AllowedMentions: allowedMentions(!m.Silent),
	}
	if m.Silent {
		mc.Flags = mc.Flags.Add(discord.MessageFlagSuppressNotifications)
	}
	if m.ReplyTo != "" {
		id, err := snowflake.Parse(m.ReplyTo)
		if err != nil {
			return core.MsgRef{}, fmt.Errorf("discord: reply-to id %q: %w", m.ReplyTo, core.ErrInvalid)
		}
		mc.MessageReference = &discord.MessageReference{MessageID: &id, FailIfNotExists: false}
	}
	id, err := a.client().CreateMessage(ctx, target, mc)
	if err != nil {
		return core.MsgRef{}, a.apiErr("send message", err)
	}
	return core.MsgRef{Chat: m.Chat, ID: id.String()}, nil
}

func (a *adapter) Edit(ctx context.Context, ref core.MsgRef, m core.OutMessage) error {
	target, msg, err := messageOf(ref)
	if err != nil {
		return err
	}
	content := a.content(m.Text, m.Mentions...)
	rows, err := components(m.Buttons)
	if err != nil {
		return err
	}
	if content == "" && len(rows) == 0 {
		return fmt.Errorf("discord: empty message: %w", core.ErrInvalid)
	}
	if rows == nil {
		rows = []discord.LayoutComponent{} // an empty list removes the old buttons
	}
	// Edits may only toggle SUPPRESS_EMBEDS; notification flags are create-only.
	flags := discord.MessageFlagSuppressEmbeds
	err = a.client().UpdateMessage(ctx, target, msg, discord.MessageUpdate{
		Content:         &content,
		Components:      &rows,
		Flags:           &flags,
		AllowedMentions: allowedMentions(false),
	})
	if err != nil {
		return a.apiErr("edit message", err)
	}
	return nil
}

func (a *adapter) Delete(ctx context.Context, ref core.MsgRef) error {
	target, msg, err := messageOf(ref)
	if err != nil {
		return err
	}
	if err := a.client().DeleteMessage(ctx, target, msg); err != nil && statusCode(err) != http.StatusNotFound {
		return a.apiErr("delete message", err)
	}
	return nil
}

func (a *adapter) Typing(ctx context.Context, chat core.ChatRef) error {
	target, err := channelOf(chat)
	if err != nil {
		return err
	}
	if err := a.client().Typing(ctx, target); err != nil {
		return a.apiErr("typing", err)
	}
	return nil
}

// Ack sends toast as an ephemeral followup to the press, which onPress has
// already acknowledged; an empty toast needs nothing more.
func (a *adapter) Ack(ctx context.Context, ev core.ActionEvent, toast string) error {
	if toast == "" {
		return nil
	}
	appStr, token, ok := strings.Cut(ev.AckRef, ":")
	app, err := snowflake.Parse(appStr)
	if !ok || err != nil || token == "" {
		return fmt.Errorf("discord: malformed ack reference: %w", core.ErrInvalid)
	}
	err = a.client().Followup(ctx, app, token, discord.MessageCreate{
		Content:         a.content(toast),
		Flags:           discord.MessageFlagEphemeral | discord.MessageFlagSuppressEmbeds,
		AllowedMentions: allowedMentions(false),
	})
	if err != nil {
		return fmt.Errorf("discord: acknowledge press: %w", err)
	}
	return nil
}

func (a *adapter) OpenDM(ctx context.Context, externalUserID string) (core.ChatRef, error) {
	user, err := snowflake.Parse(externalUserID)
	if err != nil {
		return core.ChatRef{}, fmt.Errorf("discord: user id %q: %w", externalUserID, core.ErrInvalid)
	}
	dm, err := a.client().OpenDM(ctx, user)
	if err != nil {
		return core.ChatRef{}, a.apiErr("open DM", err)
	}
	return core.ChatRef{Channel: name, ChatID: dm.String(), Kind: core.ChatDM}, nil
}

func (a *adapter) SelfTest(ctx context.Context) (string, error) {
	me, err := a.client().CurrentUser(ctx)
	if err != nil {
		return "", a.apiErr("get current user", err)
	}
	a.setSelf(me.ID, me.Username)
	return "@" + me.Username, nil
}

func (a *adapter) Fetch(ctx context.Context, att core.Attachment, limit int64) (io.ReadCloser, error) {
	if att.Size > limit {
		return nil, fmt.Errorf("discord: attachment %q is %d bytes, over the %d byte limit: %w", att.Name, att.Size, limit, core.ErrInvalid)
	}
	u, err := url.Parse(att.FetchRef)
	if err != nil || !a.fetchAllowed(u) {
		return nil, fmt.Errorf("discord: attachment %q is not on the Discord CDN: %w", att.Name, core.ErrForbidden)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("discord: attachment request: %w", err)
	}
	hc := *a.egressClient(hostPort(u))
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discord: fetch attachment: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("discord: fetch attachment: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("discord: attachment %q is %d bytes, over the %d byte limit: %w", att.Name, resp.ContentLength, limit, core.ErrInvalid)
	}
	return &cappedBody{ReadCloser: resp.Body, left: limit}, nil
}

func (a *adapter) fetchAllowed(u *url.URL) bool {
	if base := a.conf().apiBase; u.Scheme == base.Scheme && strings.EqualFold(u.Host, base.Host) {
		return true
	}
	return u.Scheme == "https" && cdnHosts[strings.ToLower(u.Hostname())] && (u.Port() == "" || u.Port() == "443")
}

// content renders text for Discord, cutting it to the hard limit as a last
// resort: the router splits at Caps.MaxLen, and only an escape-heavy chunk
// can still overflow.
func (a *adapter) content(text string, mentions ...string) string {
	out := render(text, mentions...)
	if utf8.RuneCountInString(out) <= contentLimit {
		return out
	}
	a.log.Warn("discord: rendered message over the content limit, truncating", "runes", utf8.RuneCountInString(out))
	return truncate(out, contentLimit)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func components(rows [][]core.Button) ([]discord.LayoutComponent, error) {
	var out []discord.LayoutComponent
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		if len(row) > maxPerRow {
			return nil, fmt.Errorf("discord: %d buttons in a row, at most %d: %w", len(row), maxPerRow, core.ErrInvalid)
		}
		btns := make([]discord.InteractiveComponent, 0, len(row))
		for _, b := range row {
			if b.Label == "" {
				return nil, fmt.Errorf("discord: button without a label: %w", core.ErrInvalid)
			}
			btn := discord.ButtonComponent{Label: truncate(b.Label, maxLabel)}
			switch {
			case b.URL != "":
				btn.Style, btn.URL = discord.ButtonStyleLink, b.URL
			case b.Data != "" && len(b.Data) <= maxCustomID:
				btn.Style, btn.CustomID = buttonStyle(b.Style), b.Data
			default:
				return nil, fmt.Errorf("discord: button %q needs data of at most %d bytes or a URL: %w", b.Label, maxCustomID, core.ErrInvalid)
			}
			btns = append(btns, btn)
		}
		out = append(out, discord.ActionRowComponent{Components: btns})
	}
	if len(out) > maxRows {
		return nil, fmt.Errorf("discord: %d button rows, at most %d: %w", len(out), maxRows, core.ErrInvalid)
	}
	return out, nil
}

func buttonStyle(s core.ButtonStyle) discord.ButtonStyle {
	switch s {
	case core.BtnPrimary:
		return discord.ButtonStylePrimary
	case core.BtnDanger:
		return discord.ButtonStyleDanger
	default:
		return discord.ButtonStyleSecondary
	}
}

// allowedMentions disables every mention ping: model output must never
// reach @everyone, roles or users. Only the author of a replied-to message
// is notified, and only for non-silent replies.
func allowedMentions(repliedUser bool) *discord.AllowedMentions {
	return &discord.AllowedMentions{
		Parse:       []discord.AllowedMentionType{},
		Roles:       []snowflake.ID{},
		Users:       []snowflake.ID{},
		RepliedUser: repliedUser,
	}
}

func channelOf(chat core.ChatRef) (snowflake.ID, error) {
	if chat.Channel != name {
		return 0, fmt.Errorf("discord: chat of channel %q: %w", chat.Channel, core.ErrInvalid)
	}
	id := chat.ChatID
	if chat.ThreadID != "" {
		id = chat.ThreadID
	}
	s, err := snowflake.Parse(id)
	if err != nil {
		return 0, fmt.Errorf("discord: channel id %q: %w", id, core.ErrInvalid)
	}
	return s, nil
}

func messageOf(ref core.MsgRef) (snowflake.ID, snowflake.ID, error) {
	target, err := channelOf(ref.Chat)
	if err != nil {
		return 0, 0, err
	}
	msg, err := snowflake.Parse(ref.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("discord: message id %q: %w", ref.ID, core.ErrInvalid)
	}
	return target, msg, nil
}
