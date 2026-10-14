package telegram

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
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	maxResponse  = 8 << 20
	maxRetryWait = 30 * time.Second
	maxRetries   = 2
)

// apiError is a Bot API {"ok":false} answer.
type apiError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

func (e *apiError) Unwrap() error {
	switch e.Code {
	case http.StatusBadRequest:
		return core.ErrInvalid
	case http.StatusForbidden:
		return core.ErrForbidden
	}
	return nil
}

// FailureCode says why the Bot API refused, for a failed connection test.
func (e *apiError) FailureCode() core.FailureCode {
	switch {
	case e.unauthorized():
		return core.FailKeyRejected
	case e.Code == http.StatusTooManyRequests:
		return core.FailRateLimited
	case e.Code >= 500:
		return core.FailProviderError
	}
	return core.FailUnknown
}

// unauthorized reports a rejected token: 401, or 404 for a token so
// malformed that no bot matches the path.
func (e *apiError) unauthorized() bool {
	return e.Code == http.StatusUnauthorized || e.Code == http.StatusNotFound
}

func (e *apiError) has(s string) bool { return strings.Contains(strings.ToLower(e.Description), s) }

func isAPIError(err error, code int, desc string) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == code && (desc == "" || ae.has(desc))
}

// transportError carries a network failure with the bot token scrubbed from
// its text (net/http errors quote the request URL, which contains it).
type transportError struct {
	method string
	msg    string
	err    error
}

func (e *transportError) Error() string { return "telegram " + e.method + ": " + e.msg }
func (e *transportError) Unwrap() error { return e.err }

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// client speaks the Bot API with one token over the egress system client.
type client struct {
	base  string // api_base without a trailing slash
	token string
	http  *http.Client
}

func (c *client) scrub(method string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = c.base + "/bot<token>/" + method
	}
	msg := err.Error()
	if c.token != "" {
		msg = strings.ReplaceAll(msg, c.token, "<token>")
	}
	return &transportError{method: method, msg: msg, err: err}
}

// call invokes method with params as JSON and decodes the result into out.
// A 429 with a short retry_after is waited out and retried.
func (c *client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	for attempt := 0; ; attempt++ {
		err := c.do(ctx, method, body, out)
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == http.StatusTooManyRequests && attempt < maxRetries &&
			ae.RetryAfter > 0 && ae.RetryAfter <= maxRetryWait {
			if !sleep(ctx, ae.RetryAfter) {
				return ctx.Err()
			}
			continue
		}
		return err
	}
}

func (c *client) do(ctx context.Context, method string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.scrub(method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.scrub(method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return c.scrub(method, err)
	}
	if len(data) > maxResponse {
		return fmt.Errorf("telegram %s: response larger than %d bytes", method, maxResponse)
	}
	var r apiResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d with an unreadable body", method, resp.StatusCode)
	}
	if !r.OK {
		ae := &apiError{Method: method, Code: r.ErrorCode, Description: r.Description}
		if ae.Code == 0 {
			ae.Code = resp.StatusCode
		}
		if r.Parameters != nil {
			ae.RetryAfter = time.Duration(r.Parameters.RetryAfter) * time.Second
		}
		return ae
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("telegram %s: decoding result: %w", method, err)
	}
	return nil
}

// download fetches a file from the Bot API file endpoint, refusing more than
// limit bytes.
func (c *client) download(ctx context.Context, filePath string, limit int64) (io.ReadCloser, error) {
	segs := strings.Split(filePath, "/")
	for i, s := range segs {
		// Dot segments would let a file path reach other Bot API endpoints
		// once a proxy or server normalizes the URL.
		if s == "" || s == "." || s == ".." {
			return nil, fmt.Errorf("telegram file download: unusable file path %q", filePath)
		}
		segs[i] = url.PathEscape(s)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/file/bot"+c.token+"/"+strings.Join(segs, "/"), nil)
	if err != nil {
		return nil, c.scrub("file", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.scrub("file", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("telegram file download: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, fmt.Errorf("telegram file is %d bytes, over the %d byte limit", resp.ContentLength, limit)
	}
	return &limitedBody{r: io.LimitReader(resp.Body, limit+1), c: resp.Body, limit: limit}, nil
}

type limitedBody struct {
	r     io.Reader
	c     io.Closer
	n     int64
	limit int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.n > b.limit {
		return n, fmt.Errorf("telegram file larger than %d bytes", b.limit)
	}
	return n, err
}

func (b *limitedBody) Close() error { return b.c.Close() }

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ---- Bot API types (only the fields Bothy reads) ----

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

type tgChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // private | group | supergroup | channel
	Title string `json:"title,omitempty"`
}

type tgEntity struct {
	Type   string  `json:"type"`
	Offset int     `json:"offset"` // UTF-16 code units
	Length int     `json:"length"`
	User   *tgUser `json:"user,omitempty"`
}

type tgPhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

type tgFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

type tgMessage struct {
	MessageID       int           `json:"message_id"`
	MessageThreadID int           `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool          `json:"is_topic_message,omitempty"`
	From            *tgUser       `json:"from,omitempty"`
	Chat            tgChat        `json:"chat"`
	Date            int64         `json:"date"`
	Text            string        `json:"text,omitempty"`
	Entities        []tgEntity    `json:"entities,omitempty"`
	Caption         string        `json:"caption,omitempty"`
	CaptionEntities []tgEntity    `json:"caption_entities,omitempty"`
	ReplyToMessage  *tgMessage    `json:"reply_to_message,omitempty"`
	Photo           []tgPhotoSize `json:"photo,omitempty"`
	Document        *tgFile       `json:"document,omitempty"`
	Audio           *tgFile       `json:"audio,omitempty"`
	Voice           *tgFile       `json:"voice,omitempty"`
	Video           *tgFile       `json:"video,omitempty"`
	// ForwardOrigin is set on forwards (Bot API 7.0+); older servers send
	// only forward_date. ViaBot names the inline bot the message came from.
	ForwardOrigin *tgForwardOrigin `json:"forward_origin,omitempty"`
	ForwardDate   int64            `json:"forward_date,omitempty"`
	ViaBot        *tgUser          `json:"via_bot,omitempty"`
}

type tgForwardOrigin struct {
	Type string `json:"type"` // user | hidden_user | chat | channel
}

type tgCallbackQuery struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message,omitempty"`
	Data    string     `json:"data,omitempty"`
}

type tgChatMember struct {
	Status string `json:"status"`
	User   tgUser `json:"user"`
}

type tgChatMemberUpdated struct {
	Chat          tgChat       `json:"chat"`
	From          tgUser       `json:"from"`
	Date          int64        `json:"date"`
	OldChatMember tgChatMember `json:"old_chat_member"`
	NewChatMember tgChatMember `json:"new_chat_member"`
}

type tgUpdate struct {
	UpdateID      int64                `json:"update_id"`
	Message       *tgMessage           `json:"message,omitempty"`
	CallbackQuery *tgCallbackQuery     `json:"callback_query,omitempty"`
	MyChatMember  *tgChatMemberUpdated `json:"my_chat_member,omitempty"`
}

type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type replyParameters struct {
	MessageID                int  `json:"message_id"`
	AllowSendingWithoutReply bool `json:"allow_sending_without_reply"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type sendMessageParams struct {
	ChatID              int64              `json:"chat_id"`
	MessageThreadID     int                `json:"message_thread_id,omitempty"`
	Text                string             `json:"text"`
	ParseMode           string             `json:"parse_mode,omitempty"`
	LinkPreviewOptions  linkPreviewOptions `json:"link_preview_options"`
	DisableNotification bool               `json:"disable_notification,omitempty"`
	ReplyParameters     *replyParameters   `json:"reply_parameters,omitempty"`
	ReplyMarkup         *inlineKeyboard    `json:"reply_markup,omitempty"`
}

type editMessageTextParams struct {
	ChatID             int64              `json:"chat_id"`
	MessageID          int                `json:"message_id"`
	Text               string             `json:"text"`
	ParseMode          string             `json:"parse_mode,omitempty"`
	LinkPreviewOptions linkPreviewOptions `json:"link_preview_options"`
	ReplyMarkup        *inlineKeyboard    `json:"reply_markup,omitempty"`
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

func (c *client) getMe(ctx context.Context) (tgUser, error) {
	var u tgUser
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// getUpdates long-polls once. It is not retried on 429: the poll loop owns
// backoff for it.
func (c *client) getUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]tgUpdate, error) {
	body, err := json.Marshal(getUpdatesParams{Offset: offset, Timeout: int(timeout / time.Second),
		AllowedUpdates: []string{"message", "callback_query", "my_chat_member"}})
	if err != nil {
		return nil, fmt.Errorf("telegram getUpdates: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	var ups []tgUpdate
	err = c.do(ctx, "getUpdates", body, &ups)
	return ups, err
}

type getUpdatesParams struct {
	Offset         int64    `json:"offset"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

func (c *client) sendMessage(ctx context.Context, p sendMessageParams) (tgMessage, error) {
	var m tgMessage
	err := c.call(ctx, "sendMessage", p, &m)
	return m, err
}

func (c *client) editMessageText(ctx context.Context, p editMessageTextParams) error {
	return c.call(ctx, "editMessageText", p, nil)
}

func (c *client) deleteMessage(ctx context.Context, chatID int64, msgID int) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": msgID}, nil)
}

func (c *client) sendChatAction(ctx context.Context, chatID int64, thread int) error {
	p := map[string]any{"chat_id": chatID, "action": "typing"}
	if thread != 0 {
		p["message_thread_id"] = thread
	}
	return c.call(ctx, "sendChatAction", p, nil)
}

func (c *client) answerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

func (c *client) getFile(ctx context.Context, fileID string) (tgFile, error) {
	var f tgFile
	err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f)
	return f, err
}

func (c *client) setMyCommands(ctx context.Context, cmds []botCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}
