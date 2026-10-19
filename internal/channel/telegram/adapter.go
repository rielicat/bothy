package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// Name is the channel name ("telegram") used in ChatRefs and config.
const Name = "telegram"

const (
	defaultAPIBase = "https://api.telegram.org"
	defaultHost    = "api.telegram.org"
	offsetKey      = "channel.telegram.offset"
	pollTimeout    = 25 * time.Second
	maxBackoff     = time.Minute
	authRetry      = 5 * time.Minute
	maxToastRunes  = 200
)

var (
	caps = core.ChannelCaps{Edit: true, Buttons: true, Threads: true, Typing: true, MaxLen: 4096, EditMinInterval: time.Second}

	tokenRe = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{1,128}$`)

	commands = []botCommand{
		{"stop", "Stop the current task"},
		{"new", "Start a fresh conversation"},
		{"status", "What Bothy is doing and today's spending"},
		{"usage", "Today's spending and this conversation's cost"},
		{"pins", "List pinned instructions"},
		{"pin", "Pin an instruction"},
		{"unpin", "Remove a pin"},
		{"compact", "Summarise older messages now"},
		{"model", "Show or change the AI model of this conversation"},
		{"refine", "Check answers more carefully: on, off or default"},
		{"help", "List commands"},
	}
)

var (
	_ core.Channel       = (*Adapter)(nil)
	_ core.ChatInspector = (*Adapter)(nil)
	_ core.PictureSetter = (*Adapter)(nil)

	errNoToken = errors.New("telegram: no bot token")
)

// Adapter is the Telegram Bot API channel: long polling for updates, HTML
// messages with previews always disabled, inline keyboards for buttons.
type Adapter struct {
	deps core.AdapterDeps
	log  *slog.Logger

	mu     sync.Mutex
	cfg    config.Telegram
	cl     *client // nil until a token has been resolved
	me     tgUser
	status core.ChannelStatus
}

// authError marks failures that mean the credentials are wrong or missing.
type authError struct{ err error }

func (e *authError) Error() string { return e.err.Error() }
func (e *authError) Unwrap() error { return e.err }

func (a *Adapter) Name() string { return Name }

func (a *Adapter) Caps() core.ChannelCaps { return caps }

// Reconfigure takes a new config snapshot; the hub restarts Run afterwards.
func (a *Adapter) Reconfigure(c *config.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := c.Channels.Telegram
	if next.Token != a.cfg.Token || next.APIBase != a.cfg.APIBase {
		a.cl = nil
	}
	a.cfg = next
	return nil
}

func (a *Adapter) Status() core.ChannelStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.status
	st.Name, st.Enabled = Name, a.cfg.Enabled
	if !st.Enabled {
		st.State, st.Detail = "disabled", ""
	}
	return st
}

func (a *Adapter) setState(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status.State != state || a.status.Detail != detail {
		a.status.State, a.status.Detail, a.status.Since = state, detail, a.deps.Clock.Now()
	}
}

// newClient resolves the token and builds a client bound to exactly the
// api_base host. A non-default api_base (a self-hosted Bot API server, or a
// test fake) is an operator-set, locked value and may be on a private
// network, so only then are private addresses admitted.
func (a *Adapter) newClient(ctx context.Context, cfg config.Telegram) (*client, error) {
	base := strings.TrimRight(cfg.APIBase, "/")
	if base == "" {
		base = defaultAPIBase
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("telegram: invalid channels.telegram.api_base %q: %w", base, core.ErrInvalid)
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, &authError{errNoToken}
	}
	sec, err := a.deps.Secret(ctx, cfg.Token)
	if err != nil {
		return nil, &authError{fmt.Errorf("telegram: resolving the bot token: %w", err)}
	}
	tok := strings.TrimSpace(sec.Reveal())
	if !tokenRe.MatchString(tok) {
		return nil, &authError{errors.New("telegram: the stored bot token does not look like a Telegram bot token")}
	}
	hc := a.deps.HTTP(core.SystemDest{
		Name:         "channel:" + Name,
		Hosts:        []string{net.JoinHostPort(u.Hostname(), port)},
		AllowPrivate: !strings.EqualFold(u.Hostname(), defaultHost),
	})
	if hc == nil {
		return nil, errors.New("telegram: no HTTP client available")
	}
	return &client{base: base, token: tok, http: hc}, nil
}

// client returns the cached client, resolving the token on first use.
func (a *Adapter) client(ctx context.Context) (*client, error) {
	a.mu.Lock()
	cl, cfg := a.cl, a.cfg
	a.mu.Unlock()
	if cl != nil {
		return cl, nil
	}
	cl, err := a.newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.cl == nil && a.cfg == cfg {
		a.cl = cl
	}
	a.mu.Unlock()
	return cl, nil
}

func (a *Adapter) Run(ctx context.Context, sink core.InboundSink) error {
	defer a.setState("connecting", "not running")
	a.mu.Lock()
	enabled := a.cfg.Enabled
	a.mu.Unlock()
	if !enabled {
		<-ctx.Done()
		return nil
	}
	a.setState("connecting", "")
	backoff := time.Second
	for ctx.Err() == nil {
		cl, err := a.connect(ctx)
		if err == nil {
			backoff = time.Second
			err = a.poll(ctx, cl, sink)
		}
		if ctx.Err() != nil {
			return nil
		}
		if !sleep(ctx, a.failure(err, &backoff)) {
			return nil
		}
	}
	return nil
}

func (a *Adapter) connect(ctx context.Context) (*client, error) {
	cl, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	me, err := cl.getMe(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.me = me
	a.status.Identity = "@" + me.Username
	a.mu.Unlock()
	if err := cl.setMyCommands(ctx, commands); err != nil && ctx.Err() == nil {
		a.log.Debug("telegram setMyCommands failed", "err", err)
	}
	a.setState("connected", "")
	return cl, nil
}

// failure records why the connection dropped and returns how long to wait.
func (a *Adapter) failure(err error, backoff *time.Duration) time.Duration {
	var ae *apiError
	var auth *authError
	switch {
	case errors.As(err, &auth) || (errors.As(err, &ae) && ae.unauthorized()):
		a.mu.Lock()
		a.cl = nil // the vault value may be fixed before the retry
		a.mu.Unlock()
		a.setState("auth_failed", authDetail(err))
		return authRetry
	case errors.As(err, &ae) && ae.Code == 429 && ae.RetryAfter > 0:
		a.setState("backoff", "rate limited by Telegram")
		return min(ae.RetryAfter, 5*time.Minute)
	case errors.As(err, &ae) && ae.Code == 409:
		a.setState("backoff", "another client is polling this bot, or a webhook is set")
	default:
		a.setState("backoff", errText(err))
	}
	wait := *backoff
	*backoff = min(*backoff*2, maxBackoff)
	return wait
}

func authDetail(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return "Telegram rejected the bot token (" + strconv.Itoa(ae.Code) + " " + ae.Description + ")"
	}
	return errText(err)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *Adapter) poll(ctx context.Context, cl *client, sink core.InboundSink) error {
	offset := a.loadOffset(ctx)
	// Keep the long poll inside the HTTP client's own timeout, if it has one.
	timeout := pollTimeout
	if t := cl.http.Timeout; t > 0 && t < pollTimeout+10*time.Second {
		timeout = max(time.Second, t-10*time.Second)
	}
	for {
		ups, err := cl.getUpdates(ctx, offset, timeout)
		if err != nil {
			return err
		}
		a.setState("connected", "")
		if len(ups) == 0 {
			continue
		}
		a.mu.Lock()
		me := a.me
		a.mu.Unlock()
		for i := range ups {
			if ups[i].UpdateID >= offset {
				offset = ups[i].UpdateID + 1
			}
			a.dispatch(ctx, &ups[i], me, sink)
		}
		a.saveOffset(ctx, offset)
	}
}

func (a *Adapter) dispatch(ctx context.Context, u *tgUpdate, me tgUser, sink core.InboundSink) {
	switch {
	case u.Message != nil:
		if in, ok := inbound(u.Message, me); ok {
			sink.OnMessage(ctx, in)
		}
	case u.CallbackQuery != nil:
		if ev, ok := callback(u.CallbackQuery); ok {
			sink.OnAction(ctx, ev)
		}
	case u.MyChatMember != nil:
		if in, ok := joined(u.MyChatMember, u.UpdateID, me); ok {
			sink.OnMessage(ctx, in)
		}
	}
}

func (a *Adapter) loadOffset(ctx context.Context) int64 {
	if a.deps.Meta == nil {
		return 0
	}
	v, err := a.deps.Meta.Get(ctx, offsetKey)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			a.log.Warn("telegram: reading the update offset failed", "err", err)
		}
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (a *Adapter) saveOffset(ctx context.Context, offset int64) {
	if a.deps.Meta == nil {
		return
	}
	if err := a.deps.Meta.Set(ctx, offsetKey, strconv.FormatInt(offset, 10)); err != nil && ctx.Err() == nil {
		a.log.Warn("telegram: saving the update offset failed", "err", err)
	}
}

func parseChat(ref core.ChatRef) (chatID int64, thread int, err error) {
	chatID, err = strconv.ParseInt(ref.ChatID, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("telegram: chat id %q: %w", ref.ChatID, core.ErrInvalid)
	}
	if ref.ThreadID != "" {
		if thread, err = strconv.Atoi(ref.ThreadID); err != nil {
			return 0, 0, fmt.Errorf("telegram: thread id %q: %w", ref.ThreadID, core.ErrInvalid)
		}
	}
	return chatID, thread, nil
}

func keyboard(rows [][]core.Button) (*inlineKeyboard, error) {
	var kb inlineKeyboard
	for _, row := range rows {
		var out []inlineButton
		for _, b := range row {
			switch {
			case b.URL != "":
				if !buttonURLOK(b.URL) {
					continue
				}
				out = append(out, inlineButton{Text: b.Label, URL: b.URL})
			case b.Data != "":
				if len(b.Data) > 64 {
					return nil, fmt.Errorf("telegram: button data over 64 bytes: %w", core.ErrInvalid)
				}
				out = append(out, inlineButton{Text: b.Label, CallbackData: b.Data})
			}
		}
		if len(out) > 0 {
			kb.InlineKeyboard = append(kb.InlineKeyboard, out)
		}
	}
	if len(kb.InlineKeyboard) == 0 {
		return nil, nil
	}
	return &kb, nil
}

// buttonURLOK drops link buttons Telegram would reject, failing the whole
// message: non-http(s) schemes and loopback hosts.
func buttonURLOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return false
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip == nil || !ip.IsLoopback()
}

// isParseError reports Telegram refusing our HTML; the message is then sent
// again as plain text rather than lost.
func isParseError(err error) bool { return isAPIError(err, 400, "can't parse entities") }

func (a *Adapter) Send(ctx context.Context, m core.OutMessage) (core.MsgRef, error) {
	chatID, thread, err := parseChat(m.Chat)
	if err != nil {
		return core.MsgRef{}, err
	}
	html, plain := render(m.Text, m.Mentions...)
	if strings.TrimSpace(plain) == "" {
		return core.MsgRef{}, fmt.Errorf("telegram: empty message: %w", core.ErrInvalid)
	}
	kb, err := keyboard(m.Buttons)
	if err != nil {
		return core.MsgRef{}, err
	}
	cl, err := a.client(ctx)
	if err != nil {
		return core.MsgRef{}, err
	}
	p := sendMessageParams{ChatID: chatID, MessageThreadID: thread, Text: html, ParseMode: "HTML",
		LinkPreviewOptions: linkPreviewOptions{IsDisabled: true}, DisableNotification: m.Silent, ReplyMarkup: kb}
	if m.ReplyTo != "" {
		if id, err := strconv.Atoi(m.ReplyTo); err == nil {
			p.ReplyParameters = &replyParameters{MessageID: id, AllowSendingWithoutReply: true}
		}
	}
	msg, err := cl.sendMessage(ctx, p)
	if isParseError(err) {
		p.Text, p.ParseMode = plain, ""
		msg, err = cl.sendMessage(ctx, p)
	}
	if err != nil {
		return core.MsgRef{}, err
	}
	chat := m.Chat
	chat.Channel = Name
	return core.MsgRef{Chat: chat, ID: strconv.Itoa(msg.MessageID)}, nil
}

func (a *Adapter) Edit(ctx context.Context, ref core.MsgRef, m core.OutMessage) error {
	chatID, _, err := parseChat(ref.Chat)
	if err != nil {
		return err
	}
	msgID, err := strconv.Atoi(ref.ID)
	if err != nil {
		return fmt.Errorf("telegram: message id %q: %w", ref.ID, core.ErrInvalid)
	}
	html, plain := render(m.Text, m.Mentions...)
	if strings.TrimSpace(plain) == "" {
		return fmt.Errorf("telegram: empty message: %w", core.ErrInvalid)
	}
	kb, err := keyboard(m.Buttons)
	if err != nil {
		return err
	}
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	p := editMessageTextParams{ChatID: chatID, MessageID: msgID, Text: html, ParseMode: "HTML",
		LinkPreviewOptions: linkPreviewOptions{IsDisabled: true}, ReplyMarkup: kb}
	err = cl.editMessageText(ctx, p)
	if isParseError(err) {
		p.Text, p.ParseMode = plain, ""
		err = cl.editMessageText(ctx, p)
	}
	switch {
	case isAPIError(err, 400, "message is not modified"):
		return nil
	case isAPIError(err, 400, "message to edit not found"):
		return fmt.Errorf("telegram: message %s: %w", ref.ID, core.ErrNotFound)
	}
	return err
}

func (a *Adapter) Delete(ctx context.Context, ref core.MsgRef) error {
	chatID, _, err := parseChat(ref.Chat)
	if err != nil {
		return err
	}
	msgID, err := strconv.Atoi(ref.ID)
	if err != nil {
		return fmt.Errorf("telegram: message id %q: %w", ref.ID, core.ErrInvalid)
	}
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	err = cl.deleteMessage(ctx, chatID, msgID)
	if isAPIError(err, 400, "message to delete not found") {
		return nil
	}
	return err
}

func (a *Adapter) Typing(ctx context.Context, chat core.ChatRef) error {
	chatID, thread, err := parseChat(chat)
	if err != nil {
		return err
	}
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	return cl.sendChatAction(ctx, chatID, thread)
}

func (a *Adapter) Ack(ctx context.Context, ev core.ActionEvent, toast string) error {
	if ev.AckRef == "" {
		return fmt.Errorf("telegram: press without a callback id: %w", core.ErrInvalid)
	}
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	if r := []rune(toast); len(r) > maxToastRunes {
		toast = string(r[:maxToastRunes-1]) + "…"
	}
	return cl.answerCallbackQuery(ctx, ev.AckRef, toast)
}

// OpenDM needs no API call: a Telegram private chat's id is the user's id
// (the bot can write only to users who started it).
func (a *Adapter) OpenDM(_ context.Context, externalUserID string) (core.ChatRef, error) {
	if _, err := strconv.ParseInt(externalUserID, 10, 64); err != nil {
		return core.ChatRef{}, fmt.Errorf("telegram: user id %q: %w", externalUserID, core.ErrInvalid)
	}
	return core.ChatRef{Channel: Name, ChatID: externalUserID, Kind: core.ChatDM}, nil
}

func (a *Adapter) Fetch(ctx context.Context, att core.Attachment, limit int64) (io.ReadCloser, error) {
	if att.FetchRef == "" {
		return nil, fmt.Errorf("telegram: attachment without a file id: %w", core.ErrInvalid)
	}
	cl, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	f, err := cl.getFile(ctx, att.FetchRef)
	if err != nil {
		return nil, err
	}
	if f.FileSize > limit {
		return nil, fmt.Errorf("telegram: file is %d bytes, over the %d byte limit", f.FileSize, limit)
	}
	if f.FilePath == "" {
		return nil, fmt.Errorf("telegram: file %s has no download path", att.FetchRef)
	}
	return cl.download(ctx, f.FilePath, limit)
}

// SelfTest resolves the current token afresh and calls getMe, so it also
// checks a token stored after the adapter started.
func (a *Adapter) SelfTest(ctx context.Context) (string, error) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	cl, err := a.newClient(ctx, cfg)
	if err != nil {
		return "", err
	}
	me, err := cl.getMe(ctx)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.me = me
	a.status.Identity = "@" + me.Username
	a.mu.Unlock()
	return "@" + me.Username, nil
}

// InspectChat describes a group: getChat gives its title and
// getChatMemberCount how many people are in it.
func (a *Adapter) InspectChat(ctx context.Context, chat core.ChatRef) (core.ChatInfo, error) {
	cl, err := a.client(ctx)
	if err != nil {
		return core.ChatInfo{}, err
	}
	id, err := strconv.ParseInt(chat.ChatID, 10, 64)
	if err != nil {
		return core.ChatInfo{}, fmt.Errorf("telegram: chat id %q: %w", chat.ChatID, core.ErrInvalid)
	}
	c, err := cl.getChat(ctx, id)
	if err != nil {
		return core.ChatInfo{}, err
	}
	info := core.ChatInfo{Title: c.Title}
	if n, err := cl.getChatMemberCount(ctx, id); err == nil {
		info.Members = n
	} else if ctx.Err() != nil {
		return core.ChatInfo{}, ctx.Err()
	}
	return info, nil
}

// BotID is the bot's numeric user id from getMe.
func (a *Adapter) BotID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.me.ID == 0 {
		return ""
	}
	return strconv.FormatInt(a.me.ID, 10)
}

// PictureID is the file_unique_id of the bot's profile photo, however it got
// one (BotFather, or Bothy before), "" when it has none.
func (a *Adapter) PictureID(ctx context.Context) (string, error) {
	cl, err := a.client(ctx)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	id := a.me.ID
	a.mu.Unlock()
	if id == 0 {
		me, err := cl.getMe(ctx)
		if err != nil {
			return "", err
		}
		id = me.ID
	}
	return cl.getUserProfilePhotos(ctx, id)
}

// SetPicture makes p the bot's profile photo. Telegram takes a static photo
// only as a JPEG uploaded with the request; profile photos can't be reused.
func (a *Adapter) SetPicture(ctx context.Context, p core.Picture) error {
	if len(p.JPEG) == 0 {
		return fmt.Errorf("telegram: a picture without its JPEG: %w", core.ErrInvalid)
	}
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	return cl.setMyProfilePhoto(ctx, p.JPEG)
}
