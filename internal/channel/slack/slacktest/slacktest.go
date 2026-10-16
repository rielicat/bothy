// Package slacktest is a fake Slack: the Web API methods the adapter calls
// and a Socket Mode WebSocket, served from one httptest.Server. The adapter's
// tests, the E2E suite and the RSS budget job point channels.slack.api_base
// at it; apps.connections.open hands out its own WebSocket URL. It implements
// conformance.PlatformFake and conformance.PlatformIDs.
package slacktest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults New fills in.
const (
	DefaultBotToken = "xoxb-test-bot-token"
	DefaultAppToken = "xapp-test-app-token"
	DefaultBotUser  = "UBOT00001"
	DefaultBotID    = "BBOT00001"
	DefaultTeam     = "T0000001"
	DefaultUser     = "U0000001"
	DefaultAppID    = "A0000001"
)

// Message is a message the bot posted, as the fake recorded it.
type Message struct {
	Channel     string
	TS          string
	ThreadTS    string
	Text        string
	Blocks      json.RawMessage
	UnfurlLinks string
	UnfurlMedia string
	Edits       int
	Deleted     bool
	Replies     int // thread replies injected under this message
}

// Ack is a Socket Mode acknowledgement; After is measured from the envelope.
type Ack struct {
	EnvelopeID string
	After      time.Duration
}

// Call is one Web API call and the kind of token it carried: "bot", "app" or
// "invalid".
type Call struct {
	Method string
	Token  string
}

// Response is a POST to a response_url.
type Response struct {
	Text            string `json:"text"`
	ResponseType    string `json:"response_type"`
	ReplaceOriginal bool   `json:"replace_original"`
}

// File is a file the fake serves to the bot token.
type File struct {
	Name string
	MIME string
	Data []byte
}

// Incoming describes a user message. A ChatID starting with "D" is a DM,
// "G" a private channel, anything else a public channel.
type Incoming struct {
	ChatID   string
	SenderID string
	Text     string
	ThreadTS string
	Mention  bool // add "<@bot>"; in channels the fake also sends app_mention, as Slack does
	Bot      bool
	// AppBot posts as another app does: bot_profile with an app_id but no
	// bot_id at the top level.
	AppBot bool
	Files  []File
}

// User is what users.info says about a person beyond their names. The zero
// value is a full member of the fake's workspace.
type User struct {
	TeamID          string // "" = Server.TeamID
	EnterpriseID    string
	Restricted      bool // multi-channel guest
	UltraRestricted bool // single-channel guest
	Stranger        bool // Slack Connect: from another company
	Deleted         bool
	Bot             bool
	WorkflowBot     bool
	Error           string // users.info fails with this code, e.g. "user_not_found"
}

// ChannelInfo is what conversations.info reports beyond the name.
type ChannelInfo struct {
	Members        int
	ExtShared      bool
	Shared         bool
	ConnectedTeams []string
}

// Server is safe for concurrent use. Set the exported fields before the
// adapter connects.
type Server struct {
	BotToken  string
	AppToken  string
	BotUserID string
	BotID     string
	BotName   string
	TeamID    string
	TeamName  string
	// EnterpriseID makes auth.test report an Enterprise Grid org.
	EnterpriseID string
	// PingInterval is how often the server pings each connection; slack-go
	// drops a connection that goes 30 s without one.
	PingInterval time.Duration

	srv *httptest.Server

	mu        sync.Mutex
	conn      *wsConn
	opens     int
	conns     int
	pending   [][]byte
	envelopes map[string]time.Time
	acks      []Ack
	nextSeq   int
	msgs      []*Message
	leak      bool
	calls     []Call
	responses []Response
	users     map[string][2]string
	flags     map[string]User
	chanInfo  map[string]ChannelInfo
	files     map[string]File
	dms       map[string]string
	channels  map[string]string
	infoErr   string
	userCalls []string
}

// New starts a fake with the Default* identities.
func New() *Server {
	s := &Server{
		BotToken:     DefaultBotToken,
		AppToken:     DefaultAppToken,
		BotUserID:    DefaultBotUser,
		BotID:        DefaultBotID,
		BotName:      "bothy",
		TeamID:       DefaultTeam,
		TeamName:     "Fake",
		PingInterval: 5 * time.Second,
		envelopes:    map[string]time.Time{},
		users:        map[string][2]string{},
		flags:        map[string]User{},
		chanInfo:     map[string]ChannelInfo{},
		files:        map[string]File{},
		dms:          map[string]string{},
		channels:     map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/{method}", s.api)
	mux.HandleFunc("GET /link/", s.socket)
	mux.HandleFunc("GET /files/{name}", s.file)
	mux.HandleFunc("POST /response/{id}", s.response)
	s.srv = httptest.NewServer(mux)
	return s
}

// Close stops the server and drops the Socket Mode connection.
func (s *Server) Close() {
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		c.close()
	}
	s.srv.CloseClientConnections()
	s.srv.Close()
}

// URL is the Web API base (channels.slack.api_base), ending in a slash.
func (s *Server) URL() string { return s.srv.URL + "/api/" }

// SetChannel names a channel for conversations.info and for the
// channel_name of slash commands in a public one; conversations.info
// answers channel_not_found for a channel that was never named.
func (s *Server) SetChannel(id, name string) {
	s.mu.Lock()
	s.channels[id] = name
	s.mu.Unlock()
}

// SetChannelInfoError makes conversations.info fail with code, as
// "missing_scope" does for an app installed without channels:read and
// groups:read; "" restores it.
func (s *Server) SetChannelInfoError(code string) {
	s.mu.Lock()
	s.infoErr = code
	s.mu.Unlock()
}

// SetUser sets what users.info returns for id.
func (s *Server) SetUser(id, name, displayName string) {
	s.mu.Lock()
	s.users[id] = [2]string{name, displayName}
	s.mu.Unlock()
}

// SetUserFlags sets what kind of account users.info reports for id, or
// makes users.info fail for it (User.Error).
func (s *Server) SetUserFlags(id string, u User) {
	s.mu.Lock()
	s.flags[id] = u
	s.mu.Unlock()
}

// SetChannelInfo sets the member count and sharing conversations.info
// reports for a channel named with SetChannel.
func (s *Server) SetChannelInfo(id string, c ChannelInfo) {
	s.mu.Lock()
	s.chanInfo[id] = c
	s.mu.Unlock()
}

// UserInfoCalls counts users.info calls for id.
func (s *Server) UserInfoCalls(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.userCalls {
		if c == id {
			n++
		}
	}
	return n
}

// DMChannel returns the DM channel id between the bot and user.
func (s *Server) DMChannel(user string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dmLocked(user)
}

func (s *Server) dmLocked(user string) string {
	if id, ok := s.dms[user]; ok {
		return id
	}
	id := "D" + strings.TrimPrefix(user, "U")
	s.dms[user] = id
	return id
}

// ConformanceIDs implements conformance.PlatformIDs: a DM with DefaultUser.
func (s *Server) ConformanceIDs() (string, string) {
	return s.DMChannel(DefaultUser), DefaultUser
}

// InjectText implements conformance.PlatformFake.
func (s *Server) InjectText(chatID, senderID, text string, mention bool) {
	s.Inject(Incoming{ChatID: chatID, SenderID: senderID, Text: text, Mention: mention})
}

// Inject delivers a message event (and app_mention for mentions outside
// DMs) and returns its ts.
func (s *Server) Inject(in Incoming) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tsLocked()
	text := in.Text
	if in.Mention {
		text = "<@" + s.BotUserID + "> " + text
	}
	ev := map[string]any{"type": "message", "channel": in.ChatID, "channel_type": channelType(in.ChatID), "user": in.SenderID,
		"text": text, "ts": ts, "event_ts": ts, "client_msg_id": s.idLocked()}
	parent := ""
	if in.ThreadTS != "" {
		ev["thread_ts"] = in.ThreadTS
		if m := s.findLocked(in.ChatID, in.ThreadTS); m != nil {
			m.Replies++
			parent = s.BotUserID
			ev["parent_user_id"] = parent
		}
	}
	if in.Bot {
		ev["bot_id"] = "B" + in.SenderID
	}
	if in.AppBot {
		ev["bot_profile"] = map[string]any{"id": "B" + in.SenderID, "app_id": "A" + in.SenderID, "name": "Other app"}
	}
	if len(in.Files) > 0 {
		ev["subtype"] = "file_share"
		var files []any
		for _, f := range in.Files {
			key := s.idLocked() + "-" + f.Name
			s.files[key] = f
			files = append(files, map[string]any{"id": "F" + key, "name": f.Name, "title": f.Name, "mimetype": f.MIME, "size": len(f.Data),
				"url_private": s.srv.URL + "/files/" + key, "url_private_download": s.srv.URL + "/files/" + key})
		}
		ev["files"] = files
	}
	if in.Mention && channelType(in.ChatID) != "im" {
		mention := map[string]any{"type": "app_mention", "channel": in.ChatID, "user": in.SenderID, "text": text, "ts": ts, "event_ts": ts}
		if in.ThreadTS != "" {
			mention["thread_ts"] = in.ThreadTS
		}
		if parent != "" {
			mention["parent_user_id"] = parent
		}
		if in.Bot {
			mention["bot_id"] = "B" + in.SenderID
		}
		if files, ok := ev["files"]; ok {
			mention["files"] = files
		}
		s.eventLocked(mention)
	}
	s.eventLocked(ev)
	return ts
}

// InjectSlash delivers the /bothy slash command. Its channel_name is what
// Slack sends: "directmessage" in a DM, "privategroup" in a private
// channel, otherwise the name SetChannel gave (default "general").
func (s *Server) InjectSlash(chatID, senderID, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	channelName := "general"
	switch {
	case channelType(chatID) == "im":
		channelName = "directmessage"
	case channelType(chatID) == "group":
		channelName = "privategroup"
	case s.channels[chatID] != "":
		channelName = s.channels[chatID]
	}
	s.envelopeLocked("slash_commands", map[string]any{
		"token": "verification", "team_id": s.TeamID, "team_domain": "fake", "channel_id": chatID, "channel_name": channelName,
		"user_id": senderID, "user_name": "user-" + senderID, "command": "/bothy", "text": text, "api_app_id": DefaultAppID,
		"is_enterprise_install": "false", "response_url": s.srv.URL + "/response/" + s.idLocked(), "trigger_id": s.idLocked(),
	}, true)
}

// InjectButton implements conformance.PlatformFake: a block_actions press of
// the button carrying data on the bot's message ts msgID.
func (s *Server) InjectButton(chatID, msgID, senderID, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	actionID, blockID, threadTS := "b0", "r0", ""
	parentOfThread := false
	if m := s.findLocked(chatID, msgID); m != nil {
		threadTS = m.ThreadTS
		parentOfThread = m.ThreadTS == "" && m.Replies > 0
		var blocks []struct {
			BlockID  string `json:"block_id"`
			Elements []struct {
				ActionID string `json:"action_id"`
				Value    string `json:"value"`
			} `json:"elements"`
		}
		_ = json.Unmarshal(m.Blocks, &blocks)
		for _, b := range blocks {
			for _, e := range b.Elements {
				if e.Value == data {
					actionID, blockID = e.ActionID, b.BlockID
				}
			}
		}
	}
	msg := map[string]any{"type": "message", "user": s.BotUserID, "bot_id": s.BotID, "ts": msgID, "text": ""}
	container := map[string]any{"type": "message", "message_ts": msgID, "channel_id": chatID, "is_ephemeral": false}
	if threadTS != "" {
		msg["thread_ts"], container["thread_ts"] = threadTS, threadTS
	}
	if parentOfThread {
		// Slack reports a message with replies as the root of its thread.
		msg["thread_ts"] = msgID
	}
	s.envelopeLocked("interactive", map[string]any{
		"type": "block_actions", "token": "verification", "api_app_id": DefaultAppID, "trigger_id": s.idLocked(),
		"user":      map[string]any{"id": senderID, "username": "user-" + senderID, "name": "user-" + senderID, "team_id": s.TeamID},
		"team":      map[string]any{"id": s.TeamID, "domain": "fake"},
		"channel":   map[string]any{"id": chatID, "name": "general"},
		"container": container, "message": msg,
		"response_url": s.srv.URL + "/response/" + s.idLocked(),
		"actions": []any{map[string]any{"action_id": actionID, "block_id": blockID, "type": "button", "value": data,
			"action_ts": s.tsLocked(), "text": map[string]any{"type": "plain_text", "text": "button"}}},
	}, false)
}

// Disconnect asks the client to reconnect, as Slack does before it rotates
// a connection.
func (s *Server) Disconnect() {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c != nil {
		_ = c.writeText([]byte(`{"type":"disconnect","reason":"refresh_requested","debug_info":{"host":"fake"}}`))
	}
}

// Drop closes the Socket Mode connection without warning.
func (s *Server) Drop() {
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		c.closeWith(1011, "gone")
	}
}

// SentTexts implements conformance.PlatformFake.
func (s *Server) SentTexts(chatID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		if m.Channel == chatID && !m.Deleted {
			out = append(out, m.Text)
		}
	}
	return out
}

// PreviewsDisabled implements conformance.PlatformFake: every post and update
// set unfurl_links and unfurl_media to false.
func (s *Server) PreviewsDisabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.leak
}

// Messages returns copies of the bot's messages in chatID, deleted ones included.
func (s *Server) Messages(chatID string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.msgs {
		if m.Channel == chatID {
			out = append(out, *m)
		}
	}
	return out
}

// Acks returns the envelope acknowledgements received.
func (s *Server) Acks() []Ack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.acks)
}

// Calls returns every Web API call, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// Responses returns the response_url posts received.
func (s *Server) Responses() []Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.responses)
}

// Connections counts Socket Mode connections accepted.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

// Connected reports whether a Socket Mode client is connected.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

func channelType(id string) string {
	switch {
	case strings.HasPrefix(id, "D"):
		return "im"
	case strings.HasPrefix(id, "G"):
		return "group"
	default:
		return "channel"
	}
}

func (s *Server) idLocked() string {
	s.nextSeq++
	return strconv.Itoa(s.nextSeq)
}

func (s *Server) tsLocked() string {
	s.nextSeq++
	return fmt.Sprintf("1760000000.%06d", s.nextSeq)
}

func (s *Server) findLocked(channel, ts string) *Message {
	for _, m := range s.msgs {
		if m.Channel == channel && m.TS == ts {
			return m
		}
	}
	return nil
}

func (s *Server) eventLocked(ev map[string]any) {
	s.envelopeLocked("events_api", map[string]any{
		"token": "verification", "team_id": s.TeamID, "api_app_id": DefaultAppID, "type": "event_callback",
		"event_id": "Ev" + s.idLocked(), "event_time": time.Now().Unix(), "event": ev,
	}, false)
}

func (s *Server) envelopeLocked(typ string, payload map[string]any, acceptsResponse bool) {
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	frame, err := json.Marshal(map[string]any{"envelope_id": id, "type": typ, "payload": payload,
		"accepts_response_payload": acceptsResponse, "retry_attempt": 0, "retry_reason": ""})
	if err != nil {
		panic(err)
	}
	s.envelopes[id] = time.Now()
	if s.conn == nil || s.conn.writeText(frame) != nil {
		s.pending = append(s.pending, frame)
	}
}

func (s *Server) token(r *http.Request) string {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		tok = r.PostFormValue("token")
	}
	switch tok {
	case s.BotToken:
		return "bot"
	case s.AppToken:
		return "app"
	default:
		return "invalid"
	}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")
	if err := r.ParseForm(); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	tok := s.token(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: method, Token: tok})
	want := "bot"
	if method == "apps.connections.open" {
		want = "app"
	}
	if tok != want {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	switch method {
	case "apps.connections.open":
		s.opens++
		writeJSON(w, map[string]any{"ok": true, "url": "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/link/?ticket=" + strconv.Itoa(s.opens)})
	case "auth.test":
		res := map[string]any{"ok": true, "url": "https://fake.slack.com/", "team": s.TeamName, "user": s.BotName,
			"team_id": s.TeamID, "user_id": s.BotUserID, "bot_id": s.BotID}
		if s.EnterpriseID != "" {
			res["enterprise_id"] = s.EnterpriseID
		}
		writeJSON(w, res)
	case "chat.postMessage":
		m := &Message{Channel: r.PostFormValue("channel"), TS: s.tsLocked(), ThreadTS: r.PostFormValue("thread_ts"),
			Text: r.PostFormValue("text"), Blocks: json.RawMessage(r.PostFormValue("blocks")),
			UnfurlLinks: r.PostFormValue("unfurl_links"), UnfurlMedia: r.PostFormValue("unfurl_media")}
		if m.Channel == "" {
			writeJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
			return
		}
		s.leak = s.leak || m.UnfurlLinks != "false" || m.UnfurlMedia != "false"
		s.msgs = append(s.msgs, m)
		writeJSON(w, map[string]any{"ok": true, "channel": m.Channel, "ts": m.TS,
			"message": map[string]any{"type": "message", "text": m.Text, "ts": m.TS, "user": s.BotUserID, "bot_id": s.BotID}})
	case "chat.update":
		m := s.findLocked(r.PostFormValue("channel"), r.PostFormValue("ts"))
		if m == nil || m.Deleted {
			writeJSON(w, map[string]any{"ok": false, "error": "message_not_found"})
			return
		}
		m.Text = r.PostFormValue("text")
		if r.PostForm.Has("blocks") {
			m.Blocks = json.RawMessage(r.PostFormValue("blocks"))
		}
		s.leak = s.leak || r.PostFormValue("unfurl_links") != "false" || r.PostFormValue("unfurl_media") != "false"
		m.Edits++
		writeJSON(w, map[string]any{"ok": true, "channel": m.Channel, "ts": m.TS, "text": m.Text})
	case "chat.delete":
		m := s.findLocked(r.PostFormValue("channel"), r.PostFormValue("ts"))
		if m == nil || m.Deleted {
			writeJSON(w, map[string]any{"ok": false, "error": "message_not_found"})
			return
		}
		m.Deleted = true
		writeJSON(w, map[string]any{"ok": true, "channel": m.Channel, "ts": m.TS})
	case "conversations.open":
		users := r.PostFormValue("users")
		if users == "" || strings.Contains(users, ",") {
			writeJSON(w, map[string]any{"ok": false, "error": "invalid_users"})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "channel": map[string]any{"id": s.dmLocked(users), "is_im": true}})
	case "users.info":
		id := r.PostFormValue("user")
		s.userCalls = append(s.userCalls, id)
		name, display := "user-"+id, "User "+id
		if u, ok := s.users[id]; ok {
			name, display = u[0], u[1]
		}
		f := s.flags[id]
		if f.Error != "" {
			writeJSON(w, map[string]any{"ok": false, "error": f.Error})
			return
		}
		team := f.TeamID
		if team == "" {
			team = s.TeamID
		}
		u := map[string]any{"id": id, "team_id": team, "name": name, "real_name": display, "deleted": f.Deleted,
			"is_bot": f.Bot, "is_workflow_bot": f.WorkflowBot, "is_restricted": f.Restricted, "is_ultra_restricted": f.UltraRestricted,
			"is_stranger": f.Stranger, "profile": map[string]any{"display_name": display, "real_name": display}}
		if f.EnterpriseID != "" {
			u["enterprise_user"] = map[string]any{"id": id, "enterprise_id": f.EnterpriseID}
		}
		writeJSON(w, map[string]any{"ok": true, "user": u})
	case "conversations.info":
		id := r.PostFormValue("channel")
		name, named := s.channels[id]
		switch {
		case s.infoErr != "":
			writeJSON(w, map[string]any{"ok": false, "error": s.infoErr})
		case channelType(id) == "im":
			writeJSON(w, map[string]any{"ok": true, "channel": map[string]any{"id": id, "is_im": true}})
		case !named:
			writeJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
		default:
			ci := s.chanInfo[id]
			ch := map[string]any{"id": id, "name": name, "is_channel": channelType(id) == "channel",
				"is_group": channelType(id) == "group", "is_private": channelType(id) == "group", "is_mpim": strings.HasPrefix(name, "mpdm-"),
				"is_ext_shared": ci.ExtShared, "is_shared": ci.Shared}
			if r.PostFormValue("include_num_members") == "true" {
				ch["num_members"] = ci.Members
			}
			if len(ci.ConnectedTeams) > 0 {
				ch["connected_team_ids"] = ci.ConnectedTeams
			}
			writeJSON(w, map[string]any{"ok": true, "channel": ch})
		}
	default:
		writeJSON(w, map[string]any{"ok": false, "error": "unknown_method"})
	}
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	if s.token(r) != "bot" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	f, ok := s.files[r.PathValue("name")]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
	_, _ = w.Write(f.Data)
}

func (s *Server) response(w http.ResponseWriter, r *http.Request) {
	var resp Response
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.responses = append(s.responses, resp)
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	c, err := upgrade(w, r)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conns++
	s.conn = c
	hello := fmt.Sprintf(`{"type":"hello","num_connections":1,"debug_info":{"host":"fake","build_number":1,"approximate_connection_time":18060},"connection_info":{"app_id":%q}}`, DefaultAppID)
	err = c.writeText([]byte(hello))
	pending := s.pending
	s.pending = nil
	for i, f := range pending {
		if err == nil {
			err = c.writeText(f)
		}
		if err != nil {
			s.pending = append(s.pending, pending[i:]...)
			break
		}
	}
	interval := s.PingInterval
	s.mu.Unlock()
	done := make(chan struct{})
	defer close(done)
	defer s.drop(c)
	if err != nil {
		return
	}
	go s.pinger(c, interval, done)
	for {
		b, err := c.readMessage()
		if err != nil {
			return
		}
		var ack struct {
			EnvelopeID string `json:"envelope_id"`
		}
		if json.Unmarshal(b, &ack) != nil || ack.EnvelopeID == "" {
			continue
		}
		s.mu.Lock()
		if at, ok := s.envelopes[ack.EnvelopeID]; ok {
			delete(s.envelopes, ack.EnvelopeID)
			s.acks = append(s.acks, Ack{EnvelopeID: ack.EnvelopeID, After: time.Since(at)})
		}
		s.mu.Unlock()
	}
}

func (s *Server) pinger(c *wsConn, interval time.Duration, done <-chan struct{}) {
	if c.ping() != nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if c.ping() != nil {
				return
			}
		}
	}
}

func (s *Server) drop(c *wsConn) {
	s.mu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.mu.Unlock()
	c.close()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
