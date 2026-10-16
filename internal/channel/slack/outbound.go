package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/slack-go/slack"

	"github.com/rielicat/bothy/internal/core"
)

const (
	maxBlocks   = 50
	maxElements = 25
	maxLabel    = 75
	maxValue    = 2000
)

// filesHost serves url_private downloads, which need the bot token;
// hooksHost serves response_url callbacks, which carry their own secret.
const (
	filesHost = "files.slack.com"
	hooksHost = "hooks.slack.com"
)

func (a *adapter) Send(ctx context.Context, m core.OutMessage) (core.MsgRef, error) {
	if err := checkChat(m.Chat); err != nil {
		return core.MsgRef{}, err
	}
	opts, err := messageOptions(m, false)
	if err != nil {
		return core.MsgRef{}, err
	}
	api, err := a.client(ctx)
	if err != nil {
		return core.MsgRef{}, err
	}
	_, ts, err := api.PostMessageContext(ctx, m.Chat.ChatID, opts...)
	if err != nil {
		return core.MsgRef{}, a.apiErr("chat.postMessage", botKey, err)
	}
	return core.MsgRef{Chat: m.Chat, ID: ts}, nil
}

func (a *adapter) Edit(ctx context.Context, ref core.MsgRef, m core.OutMessage) error {
	if err := checkChat(ref.Chat); err != nil {
		return err
	}
	if ref.ID == "" {
		return fmt.Errorf("slack: empty message ts: %w", core.ErrInvalid)
	}
	opts, err := messageOptions(m, true)
	if err != nil {
		return err
	}
	api, err := a.client(ctx)
	if err != nil {
		return err
	}
	if _, _, _, err := api.UpdateMessageContext(ctx, ref.Chat.ChatID, ref.ID, opts...); err != nil {
		return a.apiErr("chat.update", botKey, err)
	}
	return nil
}

func (a *adapter) Delete(ctx context.Context, ref core.MsgRef) error {
	if err := checkChat(ref.Chat); err != nil {
		return err
	}
	api, err := a.client(ctx)
	if err != nil {
		return err
	}
	if _, _, err := api.DeleteMessageContext(ctx, ref.Chat.ChatID, ref.ID); err != nil && slackCode(err) != "message_not_found" {
		return a.apiErr("chat.delete", botKey, err)
	}
	return nil
}

// Typing is a no-op: Socket Mode apps have no typing indicator.
func (a *adapter) Typing(context.Context, core.ChatRef) error { return nil }

// Ack posts toast as an ephemeral reply through the press's response_url;
// the envelope itself was acknowledged on receipt.
func (a *adapter) Ack(ctx context.Context, ev core.ActionEvent, toast string) error {
	if toast == "" {
		return nil
	}
	u, err := url.Parse(ev.AckRef)
	if err != nil || !a.hostAllowed(u, hooksHost) {
		return fmt.Errorf("slack: ack reference is not a Slack response URL: %w", core.ErrInvalid)
	}
	body, err := json.Marshal(map[string]any{"response_type": "ephemeral", "replace_original": false, "text": escape(toast)})
	if err != nil {
		return fmt.Errorf("slack: encode ack: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: ack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.noRedirects(u).Do(req)
	if err != nil {
		return fmt.Errorf("slack: acknowledge press: %w", hideURL(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: acknowledge press: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *adapter) OpenDM(ctx context.Context, externalUserID string) (core.ChatRef, error) {
	if externalUserID == "" {
		return core.ChatRef{}, fmt.Errorf("slack: empty user id: %w", core.ErrInvalid)
	}
	api, err := a.client(ctx)
	if err != nil {
		return core.ChatRef{}, err
	}
	ch, _, _, err := api.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{externalUserID}, ReturnIM: true})
	if err != nil {
		return core.ChatRef{}, a.apiErr("conversations.open", botKey, err)
	}
	return core.ChatRef{Channel: name, ChatID: ch.ID, Kind: core.ChatDM}, nil
}

// SelfTest checks both tokens: auth.test for the bot token and
// apps.connections.open for the app-level one. The connection URL it gets
// is never dialed and expires unused.
func (a *adapter) SelfTest(ctx context.Context) (string, error) {
	api, err := a.client(ctx)
	if err != nil {
		return "", err
	}
	bot, err := a.secret(ctx, &a.bot, a.conf().cfg.BotToken, botKey, "xoxb-")
	if err != nil {
		return "", err
	}
	app, err := a.secret(ctx, &a.app, a.conf().cfg.AppToken, appKey, "xapp-")
	if err != nil {
		return "", err
	}
	auth, err := api.AuthTestContext(ctx)
	if err != nil {
		return "", a.apiErr("auth.test", botKey, err)
	}
	if _, _, err := a.newClient(bot, app).StartSocketModeContext(ctx); err != nil {
		return "", a.apiErr("apps.connections.open", appKey, err)
	}
	a.setSelf(auth)
	return "@" + auth.User, nil
}

// Fetch downloads a url_private file with the bot token, which is only ever
// sent to Slack's file host (or the configured API host).
func (a *adapter) Fetch(ctx context.Context, att core.Attachment, limit int64) (io.ReadCloser, error) {
	if att.Size > limit {
		return nil, fmt.Errorf("slack: file %q is %d bytes, over the %d byte limit: %w", att.Name, att.Size, limit, core.ErrInvalid)
	}
	u, err := url.Parse(att.FetchRef)
	if err != nil || !a.hostAllowed(u, filesHost) {
		return nil, fmt.Errorf("slack: file %q is not on %s: %w", att.Name, filesHost, core.ErrForbidden)
	}
	bot, err := a.secret(ctx, &a.bot, a.conf().cfg.BotToken, botKey, "xoxb-")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("slack: file request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bot)
	resp, err := a.noRedirects(u).Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack: fetch file: %w", err)
	}
	// Without access Slack redirects to a sign-in page, which must not be
	// taken for the file.
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("slack: fetch file: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("slack: file %q is %d bytes, over the %d byte limit: %w", att.Name, resp.ContentLength, limit, core.ErrInvalid)
	}
	return &cappedBody{ReadCloser: resp.Body, left: limit}, nil
}

// hostAllowed admits https URLs on host and URLs on the configured API host.
func (a *adapter) hostAllowed(u *url.URL, host string) bool {
	if base := a.conf().apiBase; u.Scheme == base.Scheme && strings.EqualFold(u.Host, base.Host) {
		return true
	}
	return u.Scheme == "https" && strings.EqualFold(u.Hostname(), host) && (u.Port() == "" || u.Port() == "443")
}

// noRedirects returns the egress client for u's host with redirects off.
func (a *adapter) noRedirects(u *url.URL) *http.Client {
	c := *a.egressClient(hostPort(u))
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// hideURL keeps a request URL out of err: a response_url is a credential
// for posting into its conversation, and errors end up in logs.
func hideURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s response_url: %w", ue.Op, ue.Err)
	}
	return err
}

func checkChat(chat core.ChatRef) error {
	if chat.Channel != name || chat.ChatID == "" {
		return fmt.Errorf("slack: chat %q of channel %q: %w", chat.ChatID, chat.Channel, core.ErrInvalid)
	}
	return nil
}

// messageOptions builds chat.postMessage / chat.update options. Text-only
// messages go as mrkdwn text; with buttons the text moves into section
// blocks and text stays as the notification fallback. An edit always sends
// blocks, an empty list when there are no buttons, so old buttons go away.
func messageOptions(m core.OutMessage, edit bool) ([]slack.MsgOption, error) {
	text := render(m.Text)
	actions, err := actionBlocks(m.Buttons)
	if err != nil {
		return nil, err
	}
	if text == "" && len(actions) == 0 {
		return nil, fmt.Errorf("slack: empty message: %w", core.ErrInvalid)
	}
	opts := []slack.MsgOption{slack.MsgOptionDisableLinkUnfurl(), slack.MsgOptionDisableMediaUnfurl()}
	if len(actions) == 0 {
		opts = append(opts, slack.MsgOptionText(text, false))
		if edit {
			opts = append(opts, slack.MsgOptionBlocks())
		}
	} else {
		blocks := sections(text)
		if len(blocks)+len(actions) > maxBlocks {
			return nil, fmt.Errorf("slack: message needs %d blocks, at most %d: %w", len(blocks)+len(actions), maxBlocks, core.ErrInvalid)
		}
		opts = append(opts, slack.MsgOptionText(truncate(text, maxLen), false), slack.MsgOptionBlocks(append(blocks, actions...)...))
	}
	if !edit && m.Chat.ThreadID != "" {
		opts = append(opts, slack.MsgOptionTS(m.Chat.ThreadID))
	}
	return opts, nil
}

// sections splits mrkdwn into section blocks of at most maxLen runes,
// breaking at line ends where possible.
func sections(text string) []slack.Block {
	var out []slack.Block
	for text != "" {
		chunk := text
		if utf8.RuneCountInString(chunk) > maxLen {
			chunk = string([]rune(chunk)[:maxLen])
			if i := strings.LastIndexByte(chunk, '\n'); i > 0 {
				chunk = chunk[:i+1]
			}
		}
		text = text[len(chunk):]
		if body := strings.TrimRight(chunk, "\n"); body != "" {
			out = append(out, slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, body, false, false), nil, nil))
		}
	}
	return out
}

func actionBlocks(rows [][]core.Button) ([]slack.Block, error) {
	var out []slack.Block
	for i, row := range rows {
		if len(row) == 0 {
			continue
		}
		if len(row) > maxElements {
			return nil, fmt.Errorf("slack: %d buttons in a row, at most %d: %w", len(row), maxElements, core.ErrInvalid)
		}
		elems := make([]slack.BlockElement, 0, len(row))
		for j, b := range row {
			if b.Label == "" {
				return nil, fmt.Errorf("slack: button without a label: %w", core.ErrInvalid)
			}
			label := slack.NewTextBlockObject(slack.PlainTextType, truncate(b.Label, maxLabel), false, false)
			var el *slack.ButtonBlockElement
			switch {
			case b.URL != "":
				el = slack.NewButtonBlockElement(fmt.Sprintf("u%d.%d", i, j), "", label)
				el.URL = b.URL
			case b.Data != "" && len(b.Data) <= maxValue:
				el = slack.NewButtonBlockElement(fmt.Sprintf("b%d.%d", i, j), b.Data, label)
				switch b.Style {
				case core.BtnPrimary:
					el.Style = slack.StylePrimary
				case core.BtnDanger:
					el.Style = slack.StyleDanger
				}
			default:
				return nil, fmt.Errorf("slack: button %q needs data of at most %d bytes or a URL: %w", b.Label, maxValue, core.ErrInvalid)
			}
			elems = append(elems, el)
		}
		out = append(out, slack.NewActionBlock(fmt.Sprintf("r%d", i), elems...))
	}
	return out, nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
