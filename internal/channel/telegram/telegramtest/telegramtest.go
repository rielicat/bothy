// Package telegramtest is a fake Telegram Bot API server for adapter tests,
// the E2E suite and the RSS budget job. It implements
// conformance.PlatformFake.
//
// It validates what Telegram validates and Bothy relies on: the bot token in
// the path, HTML parse mode (unknown tags, bad nesting and unescaped "<", ">"
// or "&" are rejected with "can't parse entities"), the 4096-character limit,
// callback data of at most 64 bytes, and button URLs (no loopback hosts).
// SentTexts reports what a chat shows now: an edit replaces its message's
// text and a delete removes it.
package telegramtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// Config sets the fake bot's identity; zero values get defaults.
type Config struct {
	Token       string // default DefaultToken
	BotID       int64  // default 7000001
	BotUsername string // default "bothy_test_bot"
}

// DefaultToken is the token the fake accepts unless Config.Token is set.
const DefaultToken = "7000001:TEST-fake-token_for-bothy"

const maxMessageLen = 4096

// Message is one message the bot sent, as the fake stores it.
type Message struct {
	ChatID           int64
	ID               int
	ThreadID         int
	Raw              string // text as submitted (HTML when ParseMode is HTML)
	Text             string // what the user sees
	ParseMode        string
	Buttons          [][]Button
	PreviewsDisabled bool
	Silent           bool
	ReplyTo          int
	Edits            int
	Deleted          bool
}

// Button is an inline keyboard button.
type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// CallbackAnswer records an answerCallbackQuery call.
type CallbackAnswer struct {
	ID   string
	Text string
	At   time.Time
}

// ChatAction records a sendChatAction call.
type ChatAction struct {
	ChatID   int64
	ThreadID int
	Action   string
}

// BotCommand is one entry of setMyCommands.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// File is a downloadable file served through getFile.
type File struct {
	ID   string
	Name string
	MIME string
	Data []byte
}

// Inbound describes a user message to inject.
type Inbound struct {
	ChatID    int64
	ChatType  string // private (default when ChatID == SenderID) | group | supergroup
	ChatTitle string
	SenderID  int64
	FirstName string // default "Test"
	LastName  string
	Username  string
	IsBot     bool
	Text      string
	Mention   bool // prefix "@<bot> " with a mention entity
	ReplyTo   int  // id of a bot message this replies to
	MessageID int  // 0 assigns the chat's next id
	ThreadID  int  // forum topic
	Photo     *File
	Document  *File
}

// Server is the fake. It is safe for concurrent use.
type Server struct {
	URL   string
	Token string
	BotID int64
	Bot   string // username

	srv     *httptest.Server
	closing chan struct{}
	once    sync.Once

	mu        sync.Mutex
	nextUpd   int64
	pending   []update
	history   map[int64]update
	wake      chan struct{}
	nextMsg   map[int64]int
	msgs      map[int64][]*Message
	callbacks map[string]bool
	answers   []CallbackAnswer
	actions   []ChatAction
	files     map[string]File
	limits    map[string][]int
	commands  []BotCommand
	calls     map[string]int
	sends     int
	leaked    bool
	nextCB    int
}

type update struct {
	ID   int64
	Body map[string]any
}

// New starts a fake on a loopback port; Close stops it.
func New(cfg Config) *Server {
	if cfg.Token == "" {
		cfg.Token = DefaultToken
	}
	if cfg.BotID == 0 {
		cfg.BotID = 7000001
	}
	if cfg.BotUsername == "" {
		cfg.BotUsername = "bothy_test_bot"
	}
	s := &Server{
		Token: cfg.Token, BotID: cfg.BotID, Bot: cfg.BotUsername, closing: make(chan struct{}),
		history: map[int64]update{}, wake: make(chan struct{}), nextMsg: map[int64]int{},
		msgs: map[int64][]*Message{}, callbacks: map[string]bool{}, files: map[string]File{},
		limits: map[string][]int{}, calls: map[string]int{}, nextUpd: 1,
	}
	s.srv = httptest.NewServer(s)
	s.URL = s.srv.URL
	return s
}

// Close releases waiting long polls and shuts the server down.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.closing)
		s.srv.Close()
	})
}

// Client returns an HTTP client for the fake.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// ---- conformance.PlatformFake ----

func (s *Server) InjectText(chatID, senderID, text string, mention bool) {
	cid, _ := strconv.ParseInt(chatID, 10, 64)
	sid, _ := strconv.ParseInt(senderID, 10, 64)
	s.Inject(Inbound{ChatID: cid, SenderID: sid, Text: text, Mention: mention && cid != sid})
}

func (s *Server) InjectButton(chatID, msgID, senderID, data string) {
	cid, _ := strconv.ParseInt(chatID, 10, 64)
	sid, _ := strconv.ParseInt(senderID, 10, 64)
	id, _ := strconv.Atoi(msgID)
	s.Press(cid, id, sid, data)
}

func (s *Server) SentTexts(chatID string) []string {
	cid, _ := strconv.ParseInt(chatID, 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs[cid] {
		if !m.Deleted {
			out = append(out, m.Text)
		}
	}
	return out
}

func (s *Server) PreviewsDisabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends > 0 && !s.leaked
}

// ---- injection ----

// Inject queues a user message update and returns its update and message ids.
func (s *Server) Inject(in Inbound) (updateID int64, msgID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	typ := in.ChatType
	if typ == "" {
		typ = "supergroup"
		if in.ChatID == in.SenderID {
			typ = "private"
		}
	}
	msgID = in.MessageID
	if msgID == 0 {
		s.nextMsg[in.ChatID]++
		msgID = s.nextMsg[in.ChatID]
	}
	first := in.FirstName
	if first == "" {
		first = "Test"
	}
	from := map[string]any{"id": in.SenderID, "is_bot": in.IsBot, "first_name": first}
	if in.LastName != "" {
		from["last_name"] = in.LastName
	}
	if in.Username != "" {
		from["username"] = in.Username
	}
	text := in.Text
	var entities []map[string]any
	if in.Mention {
		at := "@" + s.Bot
		text = at + " " + text
		entities = append(entities, map[string]any{"type": "mention", "offset": 0, "length": u16(at)})
	}
	if strings.HasPrefix(in.Text, "/") {
		start := len(text) - len(in.Text)
		word := in.Text
		if i := strings.IndexAny(word, " \n"); i >= 0 {
			word = word[:i]
		}
		entities = append(entities, map[string]any{"type": "bot_command", "offset": u16(text[:start]), "length": u16(word)})
	}
	msg := map[string]any{"message_id": msgID, "date": time.Now().Unix(), "chat": s.chatJSON(in.ChatID, typ, in.ChatTitle), "from": from}
	textKey, entKey := "text", "entities"
	if in.Photo != nil || in.Document != nil {
		textKey, entKey = "caption", "caption_entities"
	}
	if text != "" {
		msg[textKey] = text
	}
	if len(entities) > 0 {
		msg[entKey] = entities
	}
	if in.ThreadID != 0 {
		msg["message_thread_id"], msg["is_topic_message"] = in.ThreadID, true
	}
	if in.ReplyTo != 0 {
		msg["reply_to_message"] = map[string]any{"message_id": in.ReplyTo, "date": time.Now().Unix(),
			"chat": s.chatJSON(in.ChatID, typ, in.ChatTitle), "from": s.botJSON()}
	}
	if f := in.Photo; f != nil {
		s.files[f.ID] = *f
		msg["photo"] = []map[string]any{{"file_id": f.ID, "file_unique_id": "u" + f.ID, "file_size": len(f.Data), "width": 800, "height": 600}}
	}
	if f := in.Document; f != nil {
		s.files[f.ID] = *f
		msg["document"] = map[string]any{"file_id": f.ID, "file_unique_id": "u" + f.ID, "file_name": f.Name, "mime_type": f.MIME, "file_size": len(f.Data)}
	}
	return s.pushLocked(map[string]any{"message": msg}), msgID
}

// Press queues a callback query for the bot's message msgID in chatID.
func (s *Server) Press(chatID int64, msgID int, senderID int64, data string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextCB++
	id := "cb" + strconv.Itoa(s.nextCB)
	s.callbacks[id] = true
	typ := "supergroup"
	if chatID == senderID {
		typ = "private"
	}
	msg := map[string]any{"message_id": msgID, "date": time.Now().Unix(), "chat": s.chatJSON(chatID, typ, ""), "from": s.botJSON()}
	for _, m := range s.msgs[chatID] {
		if m.ID == msgID {
			msg["text"] = m.Text
			if m.ThreadID != 0 {
				msg["message_thread_id"], msg["is_topic_message"] = m.ThreadID, true
			}
		}
	}
	s.pushLocked(map[string]any{"callback_query": map[string]any{
		"id": id, "from": map[string]any{"id": senderID, "is_bot": false, "first_name": "Test"}, "message": msg,
		"chat_instance": "ci" + strconv.FormatInt(chatID, 10), "data": data,
	}})
	return id
}

// InjectBotAdded queues a my_chat_member update: user by added the bot to
// the group chatID.
func (s *Server) InjectBotAdded(chatID int64, title string, by int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushLocked(map[string]any{"my_chat_member": map[string]any{
		"chat": s.chatJSON(chatID, "supergroup", title), "date": time.Now().Unix(),
		"from":            map[string]any{"id": by, "is_bot": false, "first_name": "Adder"},
		"old_chat_member": map[string]any{"status": "left", "user": s.botJSON()},
		"new_chat_member": map[string]any{"status": "member", "user": s.botJSON()},
	}})
}

// Redeliver queues an earlier update again under a new update id, as
// Telegram does when a client crashed before confirming it.
func (s *Server) Redeliver(updateID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.history[updateID]
	if !ok {
		return fmt.Errorf("telegramtest: no update %d", updateID)
	}
	s.pushLocked(u.Body)
	return nil
}

func (s *Server) pushLocked(body map[string]any) int64 {
	id := s.nextUpd
	s.nextUpd++
	u := update{ID: id, Body: body}
	s.pending = append(s.pending, u)
	s.history[id] = u
	close(s.wake)
	s.wake = make(chan struct{})
	return id
}

func (s *Server) chatJSON(id int64, typ, title string) map[string]any {
	c := map[string]any{"id": id, "type": typ}
	if typ != "private" {
		if title == "" {
			title = "Group " + strconv.FormatInt(id, 10)
		}
		c["title"] = title
	} else {
		c["first_name"] = "Test"
	}
	return c
}

func (s *Server) botJSON() map[string]any {
	return map[string]any{"id": s.BotID, "is_bot": true, "first_name": "Bothy", "username": s.Bot}
}

// ---- inspection and fault injection ----

// Messages returns copies of the bot's messages in chatID, deleted ones
// included, oldest first.
func (s *Server) Messages(chatID int64) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, 0, len(s.msgs[chatID]))
	for _, m := range s.msgs[chatID] {
		c := *m
		c.Buttons = slices.Clone(m.Buttons)
		out = append(out, c)
	}
	return out
}

func (s *Server) Answers() []CallbackAnswer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.answers)
}

func (s *Server) ChatActions() []ChatAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.actions)
}

func (s *Server) Commands() []BotCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

// Calls reports how many times method was called with a valid token.
func (s *Server) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

// AddFile makes f available through getFile and the file endpoint.
func (s *Server) AddFile(f File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[f.ID] = f
}

// RateLimit makes the next call of method fail with 429 and retry_after.
func (s *Server) RateLimit(method string, retryAfter int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits[method] = append(s.limits[method], retryAfter)
}

// ---- HTTP ----

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if rest, ok := strings.CutPrefix(path, "/file/bot"); ok {
		s.serveFile(w, rest)
		return
	}
	rest, ok := strings.CutPrefix(path, "/bot")
	tok, method, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 {
		apiErr(w, http.StatusNotFound, "Not Found", 0)
		return
	}
	if tok != s.Token {
		apiErr(w, http.StatusUnauthorized, "Unauthorized", 0)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: unreadable body", 0)
		return
	}
	s.mu.Lock()
	s.calls[method]++
	if q := s.limits[method]; len(q) > 0 {
		s.limits[method] = q[1:]
		s.mu.Unlock()
		apiErr(w, http.StatusTooManyRequests, "Too Many Requests: retry after "+strconv.Itoa(q[0]), q[0])
		return
	}
	s.mu.Unlock()
	switch method {
	case "getMe":
		ok200(w, s.botJSON())
	case "getUpdates":
		s.getUpdates(w, r, body)
	case "sendMessage":
		s.sendMessage(w, body)
	case "editMessageText":
		s.editMessageText(w, body)
	case "deleteMessage":
		s.deleteMessage(w, body)
	case "sendChatAction":
		s.sendChatAction(w, body)
	case "answerCallbackQuery":
		s.answerCallback(w, body)
	case "getFile":
		s.getFile(w, body)
	case "setMyCommands":
		var p struct {
			Commands []BotCommand `json:"commands"`
		}
		if json.Unmarshal(body, &p) != nil {
			apiErr(w, http.StatusBadRequest, "Bad Request: can't parse commands", 0)
			return
		}
		s.mu.Lock()
		s.commands = p.Commands
		s.mu.Unlock()
		ok200(w, true)
	default:
		apiErr(w, http.StatusNotFound, "Not Found: method not found", 0)
	}
}

func ok200(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func apiErr(w http.ResponseWriter, code int, desc string, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := map[string]any{"ok": false, "error_code": code, "description": desc}
	if retryAfter > 0 {
		body["parameters"] = map[string]any{"retry_after": retryAfter}
	}
	_ = json.NewEncoder(w).Encode(body)
}

// chatID accepts Telegram's integer or string chat ids.
type chatID int64

func (c *chatID) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n = json.Number(s)
	}
	v, err := n.Int64()
	*c = chatID(v)
	return err
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request, body []byte) {
	var p struct {
		Offset  int64 `json:"offset"`
		Timeout int   `json:"timeout"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: can't parse parameters", 0)
		return
	}
	deadline := time.NewTimer(time.Duration(min(max(p.Timeout, 0), 50)) * time.Second)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		kept := s.pending[:0]
		var out []map[string]any
		for _, u := range s.pending {
			if u.ID < p.Offset {
				continue
			}
			kept = append(kept, u)
			if len(out) < 100 {
				b := map[string]any{"update_id": u.ID}
				for k, v := range u.Body {
					b[k] = v
				}
				out = append(out, b)
			}
		}
		s.pending = kept
		wake := s.wake
		s.mu.Unlock()
		if len(out) > 0 || p.Timeout <= 0 {
			ok200(w, nonNil(out))
			return
		}
		select {
		case <-wake:
		case <-deadline.C:
			ok200(w, []any{})
			return
		case <-r.Context().Done():
			return
		case <-s.closing:
			ok200(w, []any{})
			return
		}
	}
}

func nonNil(v []map[string]any) any {
	if v == nil {
		return []any{}
	}
	return v
}

type sendParams struct {
	ChatID             chatID `json:"chat_id"`
	MessageID          int    `json:"message_id"`
	MessageThreadID    int    `json:"message_thread_id"`
	Text               string `json:"text"`
	ParseMode          string `json:"parse_mode"`
	LinkPreviewOptions *struct {
		IsDisabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
	DisableNotification bool `json:"disable_notification"`
	ReplyParameters     *struct {
		MessageID int `json:"message_id"`
	} `json:"reply_parameters"`
	ReplyMarkup *struct {
		InlineKeyboard [][]Button `json:"inline_keyboard"`
	} `json:"reply_markup"`
}

// checkContent validates text, parse mode and keyboard like Telegram and
// returns the visible text.
func checkContent(p *sendParams) (string, [][]Button, error) {
	visible, err := Visible(p.Text, p.ParseMode)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(visible) == "" {
		return "", nil, badRequest("message text is empty")
	}
	if u16(visible) > maxMessageLen {
		return "", nil, badRequest("message is too long")
	}
	var kb [][]Button
	if p.ReplyMarkup != nil {
		for _, row := range p.ReplyMarkup.InlineKeyboard {
			for _, b := range row {
				switch {
				case b.CallbackData != "" && len(b.CallbackData) > 64:
					return "", nil, badRequest("BUTTON_DATA_INVALID")
				case b.URL != "" && !validButtonURL(b.URL):
					return "", nil, badRequest("inline keyboard button URL '" + b.URL + "' is invalid")
				case b.CallbackData == "" && b.URL == "":
					return "", nil, badRequest("inline keyboard button must have a callback_data or url")
				}
			}
			kb = append(kb, slices.Clone(row))
		}
	}
	return visible, kb, nil
}

func validButtonURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "tg") || u.Hostname() == "" {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}

func (s *Server) sendMessage(w http.ResponseWriter, body []byte) {
	var p sendParams
	if err := json.Unmarshal(body, &p); err != nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: can't parse parameters", 0)
		return
	}
	visible, kb, err := checkContent(&p)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error(), 0)
		return
	}
	cid := int64(p.ChatID)
	s.mu.Lock()
	s.nextMsg[cid]++
	m := &Message{ChatID: cid, ID: s.nextMsg[cid], ThreadID: p.MessageThreadID, Raw: p.Text, Text: visible,
		ParseMode: p.ParseMode, Buttons: kb, Silent: p.DisableNotification,
		PreviewsDisabled: p.LinkPreviewOptions != nil && p.LinkPreviewOptions.IsDisabled}
	if p.ReplyParameters != nil {
		m.ReplyTo = p.ReplyParameters.MessageID
	}
	s.msgs[cid] = append(s.msgs[cid], m)
	s.notePreviewLocked(m.PreviewsDisabled)
	s.mu.Unlock()
	ok200(w, s.messageJSON(m))
}

func (s *Server) notePreviewLocked(disabled bool) {
	s.sends++
	if !disabled {
		s.leaked = true
	}
}

func (s *Server) messageJSON(m *Message) map[string]any {
	typ := "supergroup"
	if m.ChatID > 0 {
		typ = "private"
	}
	out := map[string]any{"message_id": m.ID, "date": time.Now().Unix(), "chat": s.chatJSON(m.ChatID, typ, ""),
		"from": s.botJSON(), "text": m.Text}
	if m.ThreadID != 0 {
		out["message_thread_id"] = m.ThreadID
	}
	return out
}

func (s *Server) findLocked(cid int64, id int) *Message {
	for _, m := range s.msgs[cid] {
		if m.ID == id && !m.Deleted {
			return m
		}
	}
	return nil
}

func (s *Server) editMessageText(w http.ResponseWriter, body []byte) {
	var p sendParams
	if err := json.Unmarshal(body, &p); err != nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: can't parse parameters", 0)
		return
	}
	visible, kb, err := checkContent(&p)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error(), 0)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.findLocked(int64(p.ChatID), p.MessageID)
	if m == nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: message to edit not found", 0)
		return
	}
	if m.Raw == p.Text && m.ParseMode == p.ParseMode && slices.EqualFunc(m.Buttons, kb, slices.Equal) {
		apiErr(w, http.StatusBadRequest, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message", 0)
		return
	}
	m.Raw, m.Text, m.ParseMode, m.Buttons = p.Text, visible, p.ParseMode, kb
	m.PreviewsDisabled = p.LinkPreviewOptions != nil && p.LinkPreviewOptions.IsDisabled
	m.Edits++
	s.notePreviewLocked(m.PreviewsDisabled)
	ok200(w, s.messageJSON(m))
}

func (s *Server) deleteMessage(w http.ResponseWriter, body []byte) {
	var p sendParams
	if err := json.Unmarshal(body, &p); err != nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: can't parse parameters", 0)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.findLocked(int64(p.ChatID), p.MessageID)
	if m == nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: message to delete not found", 0)
		return
	}
	m.Deleted = true
	ok200(w, true)
}

func (s *Server) sendChatAction(w http.ResponseWriter, body []byte) {
	var p struct {
		ChatID          chatID `json:"chat_id"`
		Action          string `json:"action"`
		MessageThreadID int    `json:"message_thread_id"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Action == "" {
		apiErr(w, http.StatusBadRequest, "Bad Request: wrong parameter action in request", 0)
		return
	}
	s.mu.Lock()
	s.actions = append(s.actions, ChatAction{ChatID: int64(p.ChatID), ThreadID: p.MessageThreadID, Action: p.Action})
	s.mu.Unlock()
	ok200(w, true)
}

func (s *Server) answerCallback(w http.ResponseWriter, body []byte) {
	var p struct {
		ID   string `json:"callback_query_id"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		apiErr(w, http.StatusBadRequest, "Bad Request: can't parse parameters", 0)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.callbacks[p.ID] {
		apiErr(w, http.StatusBadRequest, "Bad Request: query is too old and response timeout expired or query ID is invalid", 0)
		return
	}
	delete(s.callbacks, p.ID)
	s.answers = append(s.answers, CallbackAnswer{ID: p.ID, Text: p.Text, At: time.Now()})
	ok200(w, true)
}

func (s *Server) getFile(w http.ResponseWriter, body []byte) {
	var p struct {
		FileID string `json:"file_id"`
	}
	_ = json.Unmarshal(body, &p)
	s.mu.Lock()
	f, ok := s.files[p.FileID]
	s.mu.Unlock()
	if !ok {
		apiErr(w, http.StatusBadRequest, "Bad Request: invalid file_id", 0)
		return
	}
	ok200(w, map[string]any{"file_id": f.ID, "file_unique_id": "u" + f.ID, "file_size": len(f.Data), "file_path": "files/" + url.PathEscape(f.ID)})
}

func (s *Server) serveFile(w http.ResponseWriter, rest string) {
	tok, p, ok := strings.Cut(rest, "/")
	if !ok || tok != s.Token {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(p, "files/"))
	s.mu.Lock()
	f, found := s.files[id]
	s.mu.Unlock()
	if err != nil || !found {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
	_, _ = w.Write(f.Data)
}

func u16(s string) int { return len(utf16.Encode([]rune(s))) }
