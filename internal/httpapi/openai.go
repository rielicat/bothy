package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	modelMain       = "bothy"
	modelFast       = "bothy-fast"
	waitingEvery    = 10 * time.Second
	maxQueuedEvents = 100000
)

// ---- wire types (OpenAI Chat Completions) ----

type oaRequest struct {
	Model               string          `json:"model"`
	Messages            []oaMessage     `json:"messages"`
	Stream              bool            `json:"stream"`
	StreamOptions       *oaStreamOpts   `json:"stream_options"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	Stop                json.RawMessage `json:"stop"`
	User                string          `json:"user"`
	Tools               []oaTool        `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	N                   *int            `json:"n"`
}

type oaStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []oaToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type oaPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type oaFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type oaToolCall struct {
	Index    *int       `json:"index,omitempty"`
	ID       string     `json:"id,omitempty"`
	Type     string     `json:"type,omitempty"`
	Function oaFunction `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type oaOutMessage struct {
	Role      string       `json:"role"`
	Content   *string      `json:"content"`
	ToolCalls []oaToolCall `json:"tool_calls,omitempty"`
}

type oaChoice struct {
	Index        int          `json:"index"`
	Message      oaOutMessage `json:"message"`
	FinishReason string       `json:"finish_reason"`
}

type oaResponse struct {
	ID      string     `json:"id"`
	Object  string     `json:"object"`
	Created int64      `json:"created"`
	Model   string     `json:"model"`
	Choices []oaChoice `json:"choices"`
	Usage   oaUsage    `json:"usage"`
}

type oaDelta struct {
	Role      string       `json:"role,omitempty"`
	Content   *string      `json:"content,omitempty"`
	ToolCalls []oaToolCall `json:"tool_calls,omitempty"`
}

type oaChunkChoice struct {
	Index        int     `json:"index"`
	Delta        oaDelta `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type oaChunk struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []oaChunkChoice `json:"choices"`
	Usage   *oaUsage        `json:"usage,omitempty"`
	Bothy   *bothyExtra     `json:"bothy,omitempty"`
}

// bothyExtra rides along in chunks when X-Bothy-Events: 1; standard clients
// ignore unknown fields.
type bothyExtra struct {
	Event  string `json:"event"`
	Tool   string `json:"tool,omitempty"`
	Status string `json:"status,omitempty"`
}

type oaErrorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

type oaError struct {
	Error oaErrorBody `json:"error"`
}

func openAIError(status int, code, msg, param string) oaError {
	typ := "invalid_request_error"
	switch {
	case code == "budget_exceeded":
		typ = "insufficient_quota"
	case status == http.StatusUnauthorized:
		typ, code = "invalid_request_error", "invalid_api_key"
	case status == http.StatusForbidden:
		typ = "permission_error"
	case status == http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case status == http.StatusBadGateway:
		typ = "upstream_error"
	case status >= 500:
		typ = "server_error"
	}
	e := oaError{Error: oaErrorBody{Message: msg, Type: typ, Code: code}}
	if param != "" {
		e.Error.Param = &param
	}
	return e
}

// v1Err is an OpenAI-shaped error with a parameter name.
type v1Err struct {
	status int
	code   string
	msg    string
	param  string
}

func (e *v1Err) Error() string { return e.msg }

func (s *Server) writeV1(w http.ResponseWriter, r *http.Request, err error) error {
	var ve *v1Err
	if errors.As(err, &ve) {
		writeJSON(w, ve.status, openAIError(ve.status, ve.code, s.redact(ve.msg), ve.param))
		return nil
	}
	var pe *core.ProviderError
	if errors.As(err, &pe) {
		status, code := http.StatusBadGateway, "upstream_error"
		switch pe.Kind {
		case core.ErrKindContextLength:
			status, code = http.StatusBadRequest, "context_length_exceeded"
		case core.ErrKindInvalidRequest:
			status, code = http.StatusBadRequest, "invalid_request"
		case core.ErrKindContentFilter:
			status, code = http.StatusBadRequest, "content_filter"
		}
		writeJSON(w, status, openAIError(status, code, s.redact(pe.Error()), ""))
		return nil
	}
	return err
}

func unsupported(param string) error {
	return &v1Err{status: http.StatusBadRequest, code: "unsupported_in_agent_mode", param: param,
		msg: param + " is not supported by the agent models; use a <provider>/<model> passthrough model for client tools"}
}

func invalidParam(param, format string, args ...any) error {
	return &v1Err{status: http.StatusBadRequest, code: "invalid_request", param: param, msg: fmt.Sprintf(format, args...)}
}

// ---- GET /v1/models ----

type oaModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) v1Models(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	created := s.boot.Unix()
	data := []oaModel{}
	if a.HasScope("chat") {
		data = append(data, oaModel{modelMain, "model", created, "bothy"}, oaModel{modelFast, "model", created, "bothy"})
	}
	if a.HasScope("llm") && s.cfg().API.Passthrough && s.d.LLM != nil {
		var ids []string
		for _, m := range s.d.LLM.Models(r.Context()) {
			ids = append(ids, m.Ref.String())
		}
		sort.Strings(ids)
		for _, id := range slices.Compact(ids) {
			owner, _, _ := strings.Cut(id, "/")
			data = append(data, oaModel{id, "model", created, owner})
		}
	}
	return ok(w, map[string]any{"object": "list", "data": data})
}

// ---- POST /v1/chat/completions ----

var (
	// metadata is accepted and ignored without being reported.
	agentHonoured       = []string{"model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "user", "metadata"}
	agentRejected       = []string{"tools", "tool_choice", "functions", "function_call", "audio", "prediction", "response_format"}
	passthroughHonoured = []string{"model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens", "temperature",
		"stop", "user", "metadata", "tools", "tool_choice", "n"}
)

func (s *Server) v1ChatCompletions(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return s.writeV1(w, r, invalidParam("", "invalid JSON: %v", err))
	}
	var req oaRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return s.writeV1(w, r, invalidParam("", "invalid request: %v", err))
	}
	if len(req.Messages) == 0 {
		return s.writeV1(w, r, invalidParam("messages", "messages must not be empty"))
	}
	switch {
	case req.Model == modelMain || req.Model == modelFast:
		if !a.HasScope("chat") {
			return s.writeV1(w, r, &v1Err{status: http.StatusForbidden, code: "insufficient_scope", msg: "the agent models need a token with the chat scope"})
		}
		return s.writeV1(w, r, s.agentCompletion(w, r, a, req, raw))
	case strings.Contains(req.Model, "/"):
		if !a.HasScope("llm") {
			return s.writeV1(w, r, &v1Err{status: http.StatusForbidden, code: "insufficient_scope", msg: "passthrough models need a token with the llm scope"})
		}
		if !s.cfg().API.Passthrough {
			return s.writeV1(w, r, &v1Err{status: http.StatusNotFound, code: "model_not_found", param: "model", msg: "passthrough is disabled (api.passthrough)"})
		}
		return s.writeV1(w, r, s.passthrough(w, r, a, req, raw))
	}
	return s.writeV1(w, r, &v1Err{status: http.StatusNotFound, code: "model_not_found", param: "model",
		msg: fmt.Sprintf("unknown model %q: use bothy, bothy-fast or <provider>/<model>", req.Model)})
}

func present(raw map[string]json.RawMessage, k string) bool {
	v, ok := raw[k]
	return ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

func ignoredParams(raw map[string]json.RawMessage, honoured []string) []string {
	var out []string
	for k := range raw {
		if !slices.Contains(honoured, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func maxOutput(req oaRequest) (int, error) {
	n := 0
	for _, v := range []*int{req.MaxCompletionTokens, req.MaxTokens} {
		if v != nil {
			if *v < 1 {
				return 0, invalidParam("max_tokens", "max_tokens must be positive")
			}
			if n == 0 {
				n = *v
			}
		}
	}
	return n, nil
}

// content extracts text and image parts of a message. Only data: images are
// accepted: fetching a remote URL would be agent-controlled egress.
type msgContent struct {
	text   []string
	images []dataImage
}

type dataImage struct {
	mime string
	data []byte
}

func parseContent(raw json.RawMessage) (msgContent, error) {
	var mc msgContent
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return mc, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return mc, invalidParam("messages", "invalid content")
		}
		mc.text = append(mc.text, s)
		return mc, nil
	}
	var parts []oaPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return mc, invalidParam("messages", "content must be a string or an array of parts")
	}
	for _, p := range parts {
		switch p.Type {
		case "text":
			mc.text = append(mc.text, p.Text)
		case "image_url":
			if p.ImageURL == nil {
				return mc, invalidParam("messages", "image_url part without a url")
			}
			img, err := decodeDataURI(p.ImageURL.URL)
			if err != nil {
				return mc, err
			}
			mc.images = append(mc.images, img)
		default:
			return mc, invalidParam("messages", "unsupported content part %q", p.Type)
		}
	}
	return mc, nil
}

func decodeDataURI(u string) (dataImage, error) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return dataImage{}, invalidParam("messages", "only data: image URLs are accepted; remote images are never fetched")
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok || !strings.HasSuffix(meta, ";base64") {
		return dataImage{}, invalidParam("messages", "image data URLs must be base64")
	}
	mt, _, err := mime.ParseMediaType(strings.TrimSuffix(meta, ";base64"))
	if err != nil || !strings.HasPrefix(mt, "image/") || mt == "image/svg+xml" {
		return dataImage{}, invalidParam("messages", "unsupported image type")
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > imageMax+3 {
		return dataImage{}, invalidParam("messages", "images are limited to 5 MB")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return dataImage{}, invalidParam("messages", "invalid base64 image data")
	}
	if len(data) > imageMax {
		return dataImage{}, invalidParam("messages", "images are limited to 5 MB")
	}
	return dataImage{mime: mt, data: data}, nil
}

func usageOf(u core.Usage) oaUsage {
	var o oaUsage
	o.PromptTokens = u.TotalInput()
	o.CompletionTokens = u.Output
	o.TotalTokens = o.PromptTokens + o.CompletionTokens
	o.PromptTokensDetails.CachedTokens = u.CacheRead
	o.CompletionTokensDetails.ReasoningTokens = u.Reasoning
	return o
}

// chunkStream writes chat.completion.chunk frames. Headers go out with the
// first frame, so an error before any output can still be a JSON response.
type chunkStream struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	id      string
	model   string
	created int64
	started bool
	roleOut bool
	before  func() // sets headers just before the first write
}

func newChunkStream(w http.ResponseWriter, id, model string, created int64, before func()) *chunkStream {
	return &chunkStream{w: w, rc: http.NewResponseController(w), id: id, model: model, created: created, before: before}
}

func (c *chunkStream) start() {
	if c.started {
		return
	}
	c.started = true
	if c.before != nil {
		c.before()
	}
	h := c.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	c.w.WriteHeader(http.StatusOK)
}

func (c *chunkStream) raw(b []byte) error {
	c.start()
	_ = c.rc.SetWriteDeadline(time.Now().Add(sseDeadline))
	if _, err := c.w.Write(b); err != nil {
		return err
	}
	return c.rc.Flush()
}

func (c *chunkStream) send(ch oaChunk) error {
	ch.ID, ch.Object, ch.Created, ch.Model = c.id, "chat.completion.chunk", c.created, c.model
	if ch.Choices == nil {
		ch.Choices = []oaChunkChoice{}
	}
	b, err := json.Marshal(ch)
	if err != nil {
		return err
	}
	return c.raw(append(append([]byte("data: "), b...), '\n', '\n'))
}

func (c *chunkStream) delta(d oaDelta) error {
	if !c.roleOut {
		d.Role, c.roleOut = "assistant", true
	}
	return c.send(oaChunk{Choices: []oaChunkChoice{{Delta: d}}})
}

func (c *chunkStream) finish(reason string, usage *oaUsage) error {
	if !c.roleOut {
		if err := c.delta(oaDelta{}); err != nil {
			return err
		}
	}
	if err := c.send(oaChunk{Choices: []oaChunkChoice{{Delta: oaDelta{}, FinishReason: &reason}}}); err != nil {
		return err
	}
	if usage != nil {
		if err := c.send(oaChunk{Usage: usage}); err != nil {
			return err
		}
	}
	return c.raw([]byte("data: [DONE]\n\n"))
}

func (c *chunkStream) fail(msg string) error {
	b, _ := json.Marshal(openAIError(http.StatusBadGateway, "upstream_error", msg, ""))
	if err := c.raw(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
		return err
	}
	return c.raw([]byte("data: [DONE]\n\n"))
}

func (c *chunkStream) comment(text string) error { return c.raw([]byte(": " + text + "\n\n")) }

// runQueue is the RunSink of an API run: OnEvent never blocks, and the
// handler drains events at its own pace.
type runQueue struct {
	mu     sync.Mutex
	evs    []core.Event
	notify chan struct{}
}

func newRunQueue() *runQueue { return &runQueue{notify: make(chan struct{}, 1)} }

func (q *runQueue) OnEvent(e core.Event) {
	q.mu.Lock()
	if len(q.evs) < maxQueuedEvents || e.Type == core.EvRunFinished {
		q.evs = append(q.evs, e)
	}
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *runQueue) drain() []core.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	evs := q.evs
	q.evs = nil
	return evs
}

// agentCompletion maps an OpenAI request onto a Bothy run (SPEC §8.5).
func (s *Server) agentCompletion(w http.ResponseWriter, r *http.Request, a core.Actor, req oaRequest, raw map[string]json.RawMessage) error {
	for _, k := range agentRejected {
		if present(raw, k) {
			return unsupported(k)
		}
	}
	if req.N != nil && *req.N > 1 {
		return unsupported("n")
	}
	if v, ok := raw["logprobs"]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("true")) {
		return unsupported("logprobs")
	}
	maxOut, err := maxOutput(req)
	if err != nil {
		return err
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		return invalidParam("messages", "the last message must have role user")
	}
	var instructions []string
	var history []core.Message
	for i, m := range req.Messages {
		mc, err := parseContent(m.Content)
		if err != nil {
			return err
		}
		switch m.Role {
		case "system", "developer":
			instructions = append(instructions, strings.Join(mc.text, "\n"))
		case "user", "assistant":
			if i == len(req.Messages)-1 {
				continue
			}
			text := strings.Join(mc.text, "\n")
			if len(mc.images) > 0 {
				text += "\n[image omitted]"
			}
			role := core.MsgUser
			if m.Role == "assistant" {
				role = core.MsgAssistant
			}
			history = append(history, core.Message{Role: role, Kind: core.KindNormal, Provenance: core.ProvUser,
				Sender: "api:" + a.TokenID, Parts: []core.Part{{Type: core.PartText, Text: text}}})
		case "tool", "function":
			return invalidParam("messages", "tool messages are not accepted by the agent models; use a passthrough model")
		default:
			return invalidParam("messages", "unknown role %q", m.Role)
		}
	}
	ctx := r.Context()
	sess, mapped, err := s.apiSession(ctx, a, core.SessionID(r.Header.Get("X-Bothy-Session")))
	if err != nil {
		return err
	}
	lastContent, err := parseContent(last.Content)
	if err != nil {
		return err
	}
	var input []core.Part
	if t := strings.Join(lastContent.text, "\n"); t != "" {
		input = append(input, core.Part{Type: core.PartText, Text: t})
	}
	if len(lastContent.images) > 0 {
		pr, err := s.d.Store.Principals().Get(ctx, a.Viewer, sess.Principal)
		if err != nil {
			return err
		}
		for i, img := range lastContent.images {
			name := "image-" + strconv.Itoa(i+1) + extFor(img.mime)
			up, err := s.storeUpload(r, pr, sess.ID, namedReader{bytes.NewReader(img.data), name})
			if err != nil {
				return err
			}
			input = append(input, core.Part{Type: core.PartImage, Blob: &core.Blob{MIME: up.MIME, Name: up.Name, Ref: up.Ref, Size: up.Size}})
		}
	}
	if len(input) == 0 {
		return invalidParam("messages", "the last user message is empty")
	}
	slot := core.SlotMain
	if req.Model == modelFast {
		slot = core.SlotFast
	}
	if req.User != "" {
		s.log.Info("api request", "token", a.TokenID, "client_user", req.User, "request_id", infoOf(r).id)
	}
	q := newRunQueue()
	rr := core.RunRequest{Session: sess.ID, Principal: sess.Principal, User: a.User, Trigger: core.TrigAPI, Slot: slot, Input: input,
		Provenance: core.ProvUser, Sender: "api:" + a.TokenID, Chat: &core.ChatRef{Channel: "api", ChatID: string(sess.ID), Kind: core.ChatAPI},
		Sink: q, Instructions: strings.Join(instructions, "\n\n"), OnApproval: a.OnApproval,
		ApprovalWait: s.cfg().Policy.Approval.APIWait.D(), MaxOutput: maxOut}
	if !mapped {
		rr.Ephemeral = history
	}
	if rr.OnApproval == "" {
		rr.OnApproval = "deny"
	}
	res, err := s.d.Runner.Submit(ctx, rr)
	if err != nil {
		return s.v1RunError(err)
	}
	if res.Run == "" {
		return &v1Err{status: http.StatusTooManyRequests, code: "busy", msg: "the session is busy with another run; retry shortly"}
	}
	h := w.Header()
	h.Set("X-Bothy-Run-Id", string(res.Run))
	h.Set("X-Bothy-Session", string(sess.ID))
	if mapped {
		h.Set("X-Bothy-History", "server")
	}
	if ign := ignoredParams(raw, agentHonoured); len(ign) > 0 {
		h.Set("X-Bothy-Ignored", strings.Join(ign, ","))
	}
	ar := &agentRun{s: s, w: w, r: r, a: a, req: req, sess: sess, run: res.Run, q: q,
		events: r.Header.Get("X-Bothy-Events") == "1", detach: r.Header.Get("X-Bothy-Detach") == "1"}
	return ar.serve()
}

type namedReader struct {
	io.Reader
	name string
}

func (n namedReader) FileName() string { return n.name }

func extFor(mt string) string {
	switch mt {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

func (s *Server) v1RunError(err error) error {
	var be *core.BudgetError
	switch {
	case errors.As(err, &be):
		return &v1Err{status: http.StatusTooManyRequests, code: "budget_exceeded", msg: "daily budget exhausted: " + string(be.Kind)}
	case errors.Is(err, core.ErrPaused):
		return &v1Err{status: http.StatusServiceUnavailable, code: "paused", msg: "Bothy is paused by an admin"}
	case errors.Is(err, core.ErrBusy):
		return &v1Err{status: http.StatusTooManyRequests, code: "busy", msg: "the session queue is full"}
	}
	return err
}

// apiSession resolves X-Bothy-Session (which must be visible to the token's
// user) or creates a hidden api session for a stateless request.
func (s *Server) apiSession(ctx context.Context, a core.Actor, id core.SessionID) (*core.Session, bool, error) {
	if id != "" {
		sess, err := s.d.Store.Sessions().Get(ctx, a.Viewer, id)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return nil, false, &v1Err{status: http.StatusNotFound, code: "session_not_found", msg: "unknown X-Bothy-Session"}
			}
			return nil, false, err
		}
		return sess, true, nil
	}
	sess, err := s.d.Runner.NewSession(ctx, core.UserPrincipal(a.User), "api", nil)
	if err != nil {
		return nil, false, s.v1RunError(err)
	}
	if !sess.Hidden {
		sess.Hidden = true
		if err := s.d.Store.Sessions().Update(ctx, sess); err != nil {
			return nil, false, err
		}
	}
	return sess, false, nil
}

type agentRun struct {
	s      *Server
	w      http.ResponseWriter
	r      *http.Request
	a      core.Actor
	req    oaRequest
	sess   *core.Session
	run    core.RunID
	q      *runQueue
	events bool
	detach bool

	text      strings.Builder
	toolSince bool // a tool ran since the last text: the next text starts a new paragraph
	stop      core.StopReason
	waiting   bool
}

func finishReason(status core.RunStatus, stop core.StopReason) string {
	switch {
	case status == core.RunBudgetExceeded || status == core.RunLoopDetected || stop == core.StopMaxTokens:
		return "length"
	case stop == core.StopRefusal:
		return "content_filter"
	}
	return "stop"
}

func (ar *agentRun) serve() error {
	s, ctx := ar.s, ar.r.Context()
	id := "chatcmpl-" + strings.TrimPrefix(string(ar.run), core.PrefixRun+"_")
	created := s.now().Unix()
	var cs *chunkStream
	if ar.req.Stream {
		cs = newChunkStream(ar.w, id, ar.req.Model, created, func() { ar.w.Header().Set("Trailer", "X-Bothy-Cost-Micros") })
	}
	wait := time.NewTicker(waitingEvery)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			if !ar.detach {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, _ = s.d.Runner.Stop(stopCtx, ar.a.Viewer, ar.sess.ID, ar.a.User)
				cancel()
			}
			return nil
		case <-wait.C:
			if ar.waiting && cs != nil {
				if err := cs.comment("waiting-approval"); err != nil {
					return nil
				}
			}
		case <-ar.q.notify:
			for _, e := range ar.q.drain() {
				done, err := ar.handle(cs, e)
				if err != nil {
					return nil
				}
				if done != nil {
					return ar.finish(cs, id, created, done)
				}
			}
		}
	}
}

// handle applies one run event; it returns the finished payload at the end.
func (ar *agentRun) handle(cs *chunkStream, e core.Event) (*core.RunFinishedData, error) {
	switch d := e.Data.(type) {
	case core.DeltaData:
		if e.Type != core.EvRunDelta || d.Text == "" {
			return nil, nil
		}
		chunk := d.Text
		if ar.toolSince && ar.text.Len() > 0 {
			chunk = "\n\n" + chunk
		}
		ar.toolSince = false
		ar.text.WriteString(chunk)
		if cs != nil {
			return nil, cs.delta(oaDelta{Content: &chunk})
		}
	case core.ToolCallData:
		ar.toolSince = true
		if cs != nil && ar.events {
			return nil, cs.send(oaChunk{Choices: []oaChunkChoice{{Delta: oaDelta{}}}, Bothy: &bothyExtra{Event: string(e.Type), Tool: d.Tool}})
		}
	case core.ToolResultData:
		if cs != nil && ar.events {
			return nil, cs.send(oaChunk{Choices: []oaChunkChoice{{Delta: oaDelta{}}}, Bothy: &bothyExtra{Event: string(e.Type), Status: string(d.Status)}})
		}
	case core.RunStatusData:
		ar.waiting = d.Status == "waiting_approval"
		if ar.waiting && cs != nil {
			return nil, cs.comment("waiting-approval")
		}
	case core.UsageData:
		ar.stop = d.Call.Stop
	case core.RunFinishedData:
		return &d, nil
	}
	return nil, nil
}

func (ar *agentRun) finish(cs *chunkStream, id string, created int64, fin *core.RunFinishedData) error {
	reason := finishReason(fin.Status, ar.stop)
	usage := usageOf(fin.Usage)
	cost := strconv.FormatInt(int64(fin.Cost), 10)
	failed := fin.Status == core.RunFailed || fin.Status == core.RunInterrupted
	if cs == nil {
		if failed && ar.text.Len() == 0 {
			return &v1Err{status: http.StatusBadGateway, code: "upstream_error", msg: "the model provider failed: " + fin.Error}
		}
		ar.w.Header().Set("X-Bothy-Cost-Micros", cost)
		content := ar.text.String()
		return ok(ar.w, oaResponse{ID: id, Object: "chat.completion", Created: created, Model: ar.req.Model, Usage: usage,
			Choices: []oaChoice{{Message: oaOutMessage{Role: "assistant", Content: &content}, FinishReason: reason}}})
	}
	if failed && !cs.started {
		return &v1Err{status: http.StatusBadGateway, code: "upstream_error", msg: "the model provider failed: " + fin.Error}
	}
	if failed {
		_ = cs.fail("the model provider failed: " + ar.s.redact(fin.Error))
	} else {
		var u *oaUsage
		if ar.req.StreamOptions != nil && ar.req.StreamOptions.IncludeUsage {
			u = &usage
		}
		_ = cs.finish(reason, u)
	}
	ar.w.Header().Set("X-Bothy-Cost-Micros", cost)
	return nil
}
