// Package discordtest is a fake Discord: the REST endpoints and the gateway
// the adapter uses, served over plain HTTP and WebSocket from one
// httptest.Server. The adapter's tests, the E2E suite and the RSS budget job
// point channels.discord.api_base and gateway_url at it. It implements
// conformance.PlatformFake and conformance.PlatformIDs.
package discordtest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults New fills in.
const (
	DefaultToken  = "discord-test-token"
	DefaultBotID  = "1100000000000000001"
	DefaultAppID  = "1100000000000000002"
	DefaultGuild  = "1100000000000000003"
	DefaultUserID = "1100000000000000004"
	apiPrefix     = "/api/v10"
	logCap        = 1024
)

// SuppressEmbeds and Ephemeral are Discord message flag bits.
const (
	SuppressEmbeds = 1 << 2
	Ephemeral      = 1 << 6
)

// Message is a message the bot created, as the fake recorded it.
type Message struct {
	ID              string
	ChannelID       string
	Content         string
	Flags           int64
	Components      json.RawMessage
	AllowedMentions json.RawMessage
	ReplyTo         string
	Edits           int
	Deleted         bool
}

// Ack is an interaction callback; After is measured from the dispatch.
type Ack struct {
	Token string
	Type  int
	After time.Duration
}

// Followup is a message sent with an interaction token.
type Followup struct {
	Token   string
	Content string
	Flags   int64
}

// Incoming describes a user message to dispatch. ChatID is a DM channel from
// DMChannel, a thread from AddThread, or any other id, which the fake treats
// as a text channel of its guild.
type Incoming struct {
	ChatID      string
	SenderID    string
	SenderName  string
	Text        string
	Mention     bool // prefix the bot mention (ignored in DMs, which address the bot anyway)
	ReplyToBot  bool // reply to the bot's latest message in the chat
	Bot         bool
	Attachments []File
}

// Guild is a server the bot is in: GUILD_CREATE after IDENTIFY announces
// it, and GET /guilds/{id} describes it.
type Guild struct {
	ID      string
	Name    string
	Members int // approximate_member_count with with_counts=true
}

// Member is how GET /guilds/{guild}/members/{user} answers. Status 0 is 200
// with the member; 404 is Unknown Member and 403 Missing Access.
type Member struct {
	Status  int
	Bot     bool
	Pending bool
}

// File is an attachment the fake serves from its own CDN path.
type File struct {
	Name string
	MIME string
	Data []byte
}

// Server is safe for concurrent use. Set the exported fields before the
// adapter connects.
type Server struct {
	Token     string
	BotID     string
	AppID     string
	GuildID   string
	BotName   string
	Heartbeat time.Duration
	// DisallowedIntents makes IDENTIFY fail with close code 4014 when it
	// requests any of these intent bits.
	DisallowedIntents int64

	srv *httptest.Server

	mu         sync.Mutex
	conn       *wsConn
	live       bool
	session    string
	seq        int
	log        []logged
	subs       map[int]func([]byte)
	nextSub    int
	identifies int
	resumes    []int
	paths      []string
	queries    []url.Values
	intents    []int64
	nextID     uint64
	msgs       []*Message
	byID       map[string]*Message
	dms        map[string]string
	dmByUser   map[string]string
	threads    map[string]string
	leak       bool
	pending    map[string]time.Time
	acks       []Ack
	followups  []Followup
	limits     map[string]int
	files      map[string]File
	requests   []string
	guilds     map[string]Guild
	members    map[string]Member // guild + "/" + user
	chanGuild  map[string]string
}

type logged struct {
	seq   int
	frame []byte
}

// New starts a fake with the Default* identities.
func New() *Server {
	s := &Server{
		Token:     DefaultToken,
		BotID:     DefaultBotID,
		AppID:     DefaultAppID,
		GuildID:   DefaultGuild,
		BotName:   "bothy",
		Heartbeat: 41250 * time.Millisecond,
		subs:      map[int]func([]byte){},
		nextID:    1200000000000000000,
		byID:      map[string]*Message{},
		dms:       map[string]string{},
		dmByUser:  map[string]string{},
		threads:   map[string]string{},
		pending:   map[string]time.Time{},
		limits:    map[string]int{},
		files:     map[string]File{},
		members:   map[string]Member{},
		chanGuild: map[string]string{},
	}
	s.guilds = map[string]Guild{s.GuildID: {ID: s.GuildID, Name: "Bothy Test"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gateway", s.gateway)
	mux.HandleFunc("GET /resume", s.gateway)
	mux.HandleFunc("GET /attachments/{name}", s.attachment)
	bot := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.botAuth(h)) }
	bot("GET "+apiPrefix+"/users/@me", s.me)
	bot("GET "+apiPrefix+"/gateway/bot", s.gatewayBot)
	bot("GET "+apiPrefix+"/channels/{id}", s.channel)
	bot("POST "+apiPrefix+"/channels/{id}/messages", s.createMessage)
	bot("PATCH "+apiPrefix+"/channels/{id}/messages/{mid}", s.updateMessage)
	bot("DELETE "+apiPrefix+"/channels/{id}/messages/{mid}", s.deleteMessage)
	bot("POST "+apiPrefix+"/channels/{id}/typing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	bot("POST "+apiPrefix+"/users/@me/channels", s.openDM)
	bot("GET "+apiPrefix+"/guilds/{id}", s.guild)
	bot("GET "+apiPrefix+"/guilds/{id}/members/{user}", s.member)
	mux.HandleFunc("POST "+apiPrefix+"/interactions/{id}/{token}/callback", s.callback)
	mux.HandleFunc("POST "+apiPrefix+"/webhooks/{app}/{token}", s.followup)
	s.srv = httptest.NewServer(s.record(mux))
	return s
}

// Close stops the server and drops the gateway connection.
func (s *Server) Close() {
	s.mu.Lock()
	c := s.conn
	s.conn, s.live = nil, false
	s.mu.Unlock()
	if c != nil {
		c.close()
	}
	s.srv.CloseClientConnections()
	s.srv.Close()
}

// URL is the REST base (channels.discord.api_base).
func (s *Server) URL() string { return s.srv.URL + apiPrefix }

// GatewayURL is the gateway (channels.discord.gateway_url); GET /gateway/bot
// returns it too.
func (s *Server) GatewayURL() string { return s.wsBase() + "/gateway" }

func (s *Server) wsBase() string { return "ws" + strings.TrimPrefix(s.srv.URL, "http") }

// DMChannel returns the DM channel between the bot and userID, creating it
// as POST /users/@me/channels would.
func (s *Server) DMChannel(userID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dmLocked(userID)
}

func (s *Server) dmLocked(userID string) string {
	if id, ok := s.dmByUser[userID]; ok {
		return id
	}
	id := s.newIDLocked()
	s.dms[id], s.dmByUser[userID] = userID, id
	return id
}

// AddThread declares threadID a thread of parentID.
func (s *Server) AddThread(threadID, parentID string) {
	s.mu.Lock()
	s.threads[threadID] = parentID
	s.mu.Unlock()
}

// AddGuild adds or renames a server; a connected bot gets GUILD_CREATE.
func (s *Server) AddGuild(g Guild) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guilds[g.ID] = g
	s.dispatchLocked("GUILD_CREATE", s.guildJSON(g, false))
}

// RemoveGuild takes the bot out of a server with GUILD_DELETE.
func (s *Server) RemoveGuild(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.guilds, id)
	s.dispatchLocked("GUILD_DELETE", map[string]any{"id": id})
}

// SetMember sets how the member lookup of user in guild answers.
func (s *Server) SetMember(guild, user string, m Member) {
	s.mu.Lock()
	s.members[guild+"/"+user] = m
	s.mu.Unlock()
}

// SetChannelGuild puts a text channel in another server than GuildID, for
// GET /channels/{id} and its messages' guild_id.
func (s *Server) SetChannelGuild(channel, guild string) {
	s.mu.Lock()
	s.chanGuild[channel] = guild
	s.mu.Unlock()
}

func (s *Server) guildOfLocked(channel string) string {
	if g, ok := s.chanGuild[channel]; ok {
		return g
	}
	return s.GuildID
}

func (s *Server) guildJSON(g Guild, counts bool) map[string]any {
	out := map[string]any{"id": g.ID, "name": g.Name, "owner_id": DefaultUserID, "features": []any{}, "roles": []any{},
		"emojis": []any{}, "stickers": []any{}, "joined_at": time.Now().UTC().Format(time.RFC3339)}
	if counts {
		out["approximate_member_count"] = g.Members
	}
	return out
}

// ConformanceIDs implements conformance.PlatformIDs: a DM with DefaultUserID.
func (s *Server) ConformanceIDs() (string, string) {
	return s.DMChannel(DefaultUserID), DefaultUserID
}

// InjectText implements conformance.PlatformFake.
func (s *Server) InjectText(chatID, senderID, text string, mention bool) {
	s.Inject(Incoming{ChatID: chatID, SenderID: senderID, Text: text, Mention: mention})
}

// Inject dispatches MESSAGE_CREATE for in and returns the message id.
func (s *Server) Inject(in Incoming) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newIDLocked()
	_, dm := s.dms[in.ChatID]
	content, mentions := in.Text, []any{}
	if in.Mention && !dm {
		content = "<@" + s.BotID + "> " + content
		mentions = append(mentions, s.botUser())
	}
	name := in.SenderName
	if name == "" {
		name = "user" + in.SenderID
	}
	m := map[string]any{
		"id": id, "channel_id": in.ChatID, "content": content, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type": 0, "tts": false, "mention_everyone": false, "pinned": false, "flags": 0,
		"author":   map[string]any{"id": in.SenderID, "username": name, "discriminator": "0", "global_name": name, "avatar": nil, "bot": in.Bot},
		"mentions": mentions, "mention_roles": []any{}, "embeds": []any{}, "components": []any{},
	}
	if !dm {
		m["guild_id"] = s.guildOfLocked(in.ChatID)
	}
	if in.ReplyToBot {
		for _, bm := range slices.Backward(s.msgs) {
			if bm.ChannelID == in.ChatID && !bm.Deleted {
				m["type"] = 19
				m["message_reference"] = map[string]any{"message_id": bm.ID, "channel_id": in.ChatID}
				m["referenced_message"] = s.messageJSON(bm)
				break
			}
		}
	}
	atts := []any{}
	for _, f := range in.Attachments {
		key := s.newIDLocked() + "-" + url.PathEscape(f.Name)
		s.files[key] = f
		atts = append(atts, map[string]any{"id": s.newIDLocked(), "filename": f.Name, "content_type": f.MIME,
			"size": len(f.Data), "url": s.srv.URL + "/attachments/" + key, "proxy_url": s.srv.URL + "/attachments/" + key})
	}
	m["attachments"] = atts
	s.dispatchLocked("MESSAGE_CREATE", m)
	return id
}

// InjectButton implements conformance.PlatformFake: a press of the button
// carrying data on the bot's message msgID.
func (s *Server) InjectButton(chatID, msgID, senderID, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b [16]byte
	_, _ = rand.Read(b[:])
	token := "interaction." + hex.EncodeToString(b[:])
	msg := map[string]any{"id": msgID, "channel_id": chatID, "content": "", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type": 0, "author": s.botUser(), "mentions": []any{}, "mention_roles": []any{}, "attachments": []any{}, "embeds": []any{}, "components": []any{}}
	if bm, ok := s.byID[msgID]; ok {
		msg = s.messageJSON(bm)
	}
	user := map[string]any{"id": senderID, "username": "user" + senderID, "discriminator": "0", "global_name": nil, "avatar": nil}
	ev := map[string]any{
		"id": s.newIDLocked(), "application_id": s.AppID, "type": 3, "token": token, "version": 1,
		"channel": s.channelJSON(chatID), "channel_id": chatID, "message": msg,
		"data":   map[string]any{"custom_id": data, "component_type": 2},
		"locale": "en-US", "app_permissions": "0", "entitlements": []any{}, "authorizing_integration_owners": map[string]any{},
	}
	if _, dm := s.dms[chatID]; dm {
		ev["user"], ev["context"] = user, 1
	} else {
		ev["guild_id"], ev["context"] = s.GuildID, 0
		ev["member"] = map[string]any{"user": user, "roles": []any{}, "joined_at": time.Now().UTC().Format(time.RFC3339), "permissions": "0", "deaf": false, "mute": false, "flags": 0}
	}
	s.pending[token] = time.Now()
	s.dispatchLocked("INTERACTION_CREATE", ev)
}

// SentTexts implements conformance.PlatformFake.
func (s *Server) SentTexts(chatID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		if m.ChannelID == chatID && !m.Deleted {
			out = append(out, m.Content)
		}
	}
	return out
}

// PreviewsDisabled implements conformance.PlatformFake: every create, edit
// and followup set SUPPRESS_EMBEDS.
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
		if m.ChannelID == chatID {
			out = append(out, *m)
		}
	}
	return out
}

// Acks returns the interaction callbacks received.
func (s *Server) Acks() []Ack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.acks)
}

// Followups returns the interaction followups received.
func (s *Server) Followups() []Followup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.followups)
}

// Identifies counts IDENTIFY payloads; Resumes lists the seq of each RESUME.
func (s *Server) Identifies() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identifies
}

func (s *Server) Resumes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.resumes)
}

// GatewayPaths lists the path of every gateway connection ("/gateway" or the
// "/resume" path READY advertises as resume_gateway_url).
func (s *Server) GatewayPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

// GatewayQueries lists the query of every gateway connection.
func (s *Server) GatewayQueries() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queries)
}

// Intents lists the intents of every IDENTIFY.
func (s *Server) Intents() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.intents)
}

// Connected reports whether a gateway client has identified or resumed and
// is receiving events.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// ForceClose drops the gateway connection with close code, as Discord does
// when it moves a session; events dispatched until the client resumes are
// replayed then.
func (s *Server) ForceClose(code int) {
	s.mu.Lock()
	c := s.conn
	s.conn, s.live = nil, false
	s.mu.Unlock()
	if c != nil {
		c.closeWith(code, "forced close")
	}
}

// RateLimitNext answers the next request for method and path (for example
// "POST", "/api/v10/channels/1/messages") with a route-scoped 429 asking to
// retry after retryAfter seconds.
func (s *Server) RateLimitNext(method, path string, retryAfter int) {
	s.mu.Lock()
	s.limits[method+" "+path] = retryAfter
	s.mu.Unlock()
}

// Requests lists "METHOD /path" for every HTTP request, in order.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Subscribe attaches an in-process gateway client: fn receives a READY frame
// for a fresh session, then every dispatch frame, exactly as a WebSocket
// client would. It exists for tests built with the race detector, which
// reports data races inside disgo v0.19.6's gateway; do not mix it with a
// WebSocket client.
func (s *Server) Subscribe(fn func(frame []byte)) (stop func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.newSessionLocked()
	s.live = true
	s.identifies++
	s.dispatchLocked("READY", s.readyJSON())
	s.guildsLocked()
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.live = s.conn != nil
		s.mu.Unlock()
	}
}

func (s *Server) newIDLocked() string {
	s.nextID++
	return strconv.FormatUint(s.nextID, 10)
}

func (s *Server) newSessionLocked() {
	var b [8]byte
	_, _ = rand.Read(b[:])
	s.session = hex.EncodeToString(b[:])
	s.seq, s.log = 0, nil
}

func (s *Server) dispatchLocked(t string, d any) {
	s.seq++
	frame, err := json.Marshal(map[string]any{"op": 0, "t": t, "s": s.seq, "d": d})
	if err != nil {
		panic(err)
	}
	s.log = append(s.log, logged{s.seq, frame})
	if len(s.log) > logCap {
		s.log = s.log[len(s.log)-logCap:]
	}
	if s.live && s.conn != nil {
		_ = s.conn.writeText(frame)
	}
	for _, fn := range s.subs {
		fn(frame)
	}
}

func (s *Server) botUser() map[string]any {
	return map[string]any{"id": s.BotID, "username": s.BotName, "discriminator": "0", "global_name": nil, "avatar": nil, "bot": true}
}

func (s *Server) readyJSON() map[string]any {
	return map[string]any{
		"v": 10, "user": s.botUser(), "guilds": []any{}, "session_id": s.session,
		"resume_gateway_url": s.wsBase() + "/resume", "shard": []int{0, 1},
		"application": map[string]any{"id": s.AppID, "flags": 0},
	}
}

func (s *Server) messageJSON(m *Message) map[string]any {
	out := map[string]any{"id": m.ID, "channel_id": m.ChannelID, "content": m.Content, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type": 0, "flags": m.Flags, "author": s.botUser(), "tts": false, "mention_everyone": false, "pinned": false,
		"mentions": []any{}, "mention_roles": []any{}, "attachments": []any{}, "embeds": []any{}, "components": []any{}}
	if _, dm := s.dms[m.ChannelID]; !dm {
		out["guild_id"] = s.GuildID
	}
	return out
}

func (s *Server) channelJSON(id string) map[string]any {
	if user, ok := s.dms[id]; ok {
		return map[string]any{"id": id, "type": 1, "last_message_id": nil,
			"recipients": []any{map[string]any{"id": user, "username": "user" + user, "discriminator": "0", "global_name": nil, "avatar": nil}}}
	}
	if parent, ok := s.threads[id]; ok {
		return map[string]any{"id": id, "type": 11, "guild_id": s.GuildID, "parent_id": parent, "name": "thread", "owner_id": DefaultUserID,
			"message_count": 0, "member_count": 1, "rate_limit_per_user": 0,
			"thread_metadata": map[string]any{"archived": false, "auto_archive_duration": 1440, "archive_timestamp": time.Now().UTC().Format(time.RFC3339), "locked": false}}
	}
	return map[string]any{"id": id, "type": 0, "guild_id": s.guildOfLocked(id), "name": "general", "position": 0, "permission_overwrites": []any{}, "nsfw": false}
}

func (s *Server) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.requests = append(s.requests, key)
		retry, limited := s.limits[key]
		delete(s.limits, key)
		s.mu.Unlock()
		if limited {
			h := w.Header()
			h.Set("X-RateLimit-Bucket", "fake-"+strconv.Itoa(len(key)))
			h.Set("X-RateLimit-Limit", "5")
			h.Set("X-RateLimit-Remaining", "0")
			h.Set("X-RateLimit-Reset-After", strconv.Itoa(retry))
			h.Set("Retry-After", strconv.Itoa(retry))
			h.Set("Via", "1.1 fake") // without Via disgo assumes a Cloudflare ban and pauses every route
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "You are being rate limited.", "retry_after": retry, "global": false})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) botAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot "+s.Token {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "401: Unauthorized", "code": 0})
			return
		}
		next(w, r)
	})
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request) {
	u := s.botUser()
	u["verified"], u["mfa_enabled"], u["flags"] = true, false, 0
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) gatewayBot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"url": s.GatewayURL(), "shards": 1,
		"session_start_limit": map[string]any{"total": 1000, "remaining": 1000, "reset_after": 0, "max_concurrency": 1}})
}

func (s *Server) channel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ch := s.channelJSON(r.PathValue("id"))
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, ch)
}

func (s *Server) guild(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	g, ok := s.guilds[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown Guild", "code": 10004})
		return
	}
	writeJSON(w, http.StatusOK, s.guildJSON(g, r.URL.Query().Get("with_counts") == "true"))
}

func (s *Server) member(w http.ResponseWriter, r *http.Request) {
	guild, user := r.PathValue("id"), r.PathValue("user")
	s.mu.Lock()
	m, ok := s.members[guild+"/"+user]
	s.mu.Unlock()
	switch {
	case !ok || m.Status == http.StatusNotFound:
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown Member", "code": 10007})
	case m.Status == http.StatusForbidden:
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Missing Access", "code": 50001})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": user, "username": "user" + user, "discriminator": "0",
			"global_name": nil, "avatar": nil, "bot": m.Bot}, "roles": []any{}, "joined_at": time.Now().UTC().Format(time.RFC3339),
			"deaf": false, "mute": false, "flags": 0, "pending": m.Pending})
	}
}

type messageBody struct {
	Content          *string         `json:"content"`
	Flags            *int64          `json:"flags"`
	Components       json.RawMessage `json:"components"`
	AllowedMentions  json.RawMessage `json:"allowed_mentions"`
	MessageReference *struct {
		MessageID string `json:"message_id"`
	} `json:"message_reference"`
}

func (s *Server) createMessage(w http.ResponseWriter, r *http.Request) {
	var body messageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid Form Body", "code": 50035})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := &Message{ID: s.newIDLocked(), ChannelID: r.PathValue("id"), Components: body.Components, AllowedMentions: body.AllowedMentions}
	if body.Content != nil {
		m.Content = *body.Content
	}
	if body.Flags != nil {
		m.Flags = *body.Flags
	}
	if body.MessageReference != nil {
		m.ReplyTo = body.MessageReference.MessageID
	}
	s.leak = s.leak || m.Flags&SuppressEmbeds == 0
	s.msgs = append(s.msgs, m)
	s.byID[m.ID] = m
	writeJSON(w, http.StatusOK, s.messageJSON(m))
}

func (s *Server) updateMessage(w http.ResponseWriter, r *http.Request) {
	var body messageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid Form Body", "code": 50035})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[r.PathValue("mid")]
	if !ok || m.Deleted || m.ChannelID != r.PathValue("id") {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown Message", "code": 10008})
		return
	}
	if body.Content != nil {
		m.Content = *body.Content
	}
	if body.Components != nil {
		m.Components = body.Components
	}
	if body.AllowedMentions != nil {
		m.AllowedMentions = body.AllowedMentions
	}
	s.leak = s.leak || body.Flags == nil || *body.Flags&SuppressEmbeds == 0
	if body.Flags != nil {
		m.Flags = *body.Flags
	}
	m.Edits++
	writeJSON(w, http.StatusOK, s.messageJSON(m))
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[r.PathValue("mid")]
	if !ok || m.Deleted || m.ChannelID != r.PathValue("id") {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown Message", "code": 10008})
		return
	}
	m.Deleted = true
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) openDM(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RecipientID string `json:"recipient_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RecipientID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid Form Body", "code": 50035})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.channelJSON(s.dmLocked(body.RecipientID)))
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type int `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid Form Body", "code": 50035})
		return
	}
	token := r.PathValue("token")
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.pending[token]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown interaction", "code": 10062})
		return
	}
	delete(s.pending, token)
	s.acks = append(s.acks, Ack{Token: token, Type: body.Type, After: time.Since(at)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) followup(w http.ResponseWriter, r *http.Request) {
	var body messageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid Form Body", "code": 50035})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.PathValue("app") != s.AppID || !s.acked(r.PathValue("token")) {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown Webhook", "code": 10015})
		return
	}
	f := Followup{Token: r.PathValue("token"), Content: *body.Content}
	if body.Flags != nil {
		f.Flags = *body.Flags
	}
	s.leak = s.leak || f.Flags&SuppressEmbeds == 0
	s.followups = append(s.followups, f)
	writeJSON(w, http.StatusOK, map[string]any{"id": s.newIDLocked(), "channel_id": "0", "content": f.Content, "flags": f.Flags,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": 0, "author": s.botUser(), "mentions": []any{}, "attachments": []any{}, "embeds": []any{}})
}

// acked reports whether the interaction was answered; Discord rejects
// followups before the initial response.
func (s *Server) acked(token string) bool {
	return slices.ContainsFunc(s.acks, func(a Ack) bool { return a.Token == token })
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) gateway(w http.ResponseWriter, r *http.Request) {
	c, err := upgrade(w, r)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.queries = append(s.queries, maps.Clone(r.URL.Query()))
	hello, _ := json.Marshal(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": s.Heartbeat.Milliseconds()}})
	s.mu.Unlock()
	if err := c.writeText(hello); err != nil {
		c.close()
		return
	}
	defer s.drop(c)
	for {
		b, err := c.readMessage()
		if err != nil {
			return
		}
		var msg struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
		}
		if json.Unmarshal(b, &msg) != nil {
			c.closeWith(4002, "Decode error.")
			return
		}
		switch msg.Op {
		case 1:
			_ = c.writeText([]byte(`{"op":11}`))
		case 2:
			if !s.identify(c, msg.D) {
				return
			}
		case 6:
			if !s.resume(c, msg.D) {
				return
			}
		}
	}
}

func (s *Server) identify(c *wsConn, raw json.RawMessage) bool {
	var d struct {
		Token   string `json:"token"`
		Intents int64  `json:"intents"`
	}
	_ = json.Unmarshal(raw, &d)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intents = append(s.intents, d.Intents)
	if d.Token != s.Token {
		c.closeWith(4004, "Authentication failed.")
		return false
	}
	if d.Intents&s.DisallowedIntents != 0 {
		c.closeWith(4014, "Disallowed intent(s).")
		return false
	}
	s.identifies++
	s.newSessionLocked()
	s.conn, s.live = c, true
	s.dispatchLocked("READY", s.readyJSON())
	s.guildsLocked()
	return true
}

// guildsLocked announces every server after READY, as Discord does.
func (s *Server) guildsLocked() {
	for _, id := range slices.Sorted(maps.Keys(s.guilds)) {
		s.dispatchLocked("GUILD_CREATE", s.guildJSON(s.guilds[id], false))
	}
}

func (s *Server) resume(c *wsConn, raw json.RawMessage) bool {
	var d struct {
		Token     string `json:"token"`
		SessionID string `json:"session_id"`
		Seq       int    `json:"seq"`
	}
	_ = json.Unmarshal(raw, &d)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Token != s.Token {
		c.closeWith(4004, "Authentication failed.")
		return false
	}
	if d.SessionID == "" || d.SessionID != s.session {
		_ = c.writeText([]byte(`{"op":9,"d":false}`))
		return true
	}
	s.resumes = append(s.resumes, d.Seq)
	s.conn, s.live = c, true
	for _, l := range s.log {
		if l.seq > d.Seq {
			_ = c.writeText(l.frame)
		}
	}
	s.dispatchLocked("RESUMED", map[string]any{})
	return true
}

func (s *Server) drop(c *wsConn) {
	s.mu.Lock()
	if s.conn == c {
		s.conn, s.live = nil, len(s.subs) > 0
	}
	s.mu.Unlock()
	c.close()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
