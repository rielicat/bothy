package mockllm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	maxBody         = 32 << 20
	createdAt       = 1_700_000_000
	overflowContext = 8192 // reported by an overflow fault on a model without Context
)

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	Stream          bool            `json:"stream"`
	StreamOptions   *streamOptions  `json:"stream_options"`
	Tools           json.RawMessage `json:"tools"`
	ReasoningEffort string          `json:"reasoning_effort"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

// contentText flattens a string or an array of content parts to its text.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		// "text" in Chat Completions; input_text and output_text in Responses.
		if p.Type == "text" || p.Type == "input_text" || p.Type == "output_text" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func estimateTokens(req *chatRequest) int {
	n := len(req.Tools)
	for _, m := range req.Messages {
		n += len(contentText(m.Content)) + len(m.ToolCalls)
	}
	return (n + 3) / 4
}

type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

func writeError(w http.ResponseWriter, status int, typ, code, param, msg string) {
	body := apiErrorBody{Message: msg, Type: typ}
	if code != "" {
		body.Code = &code
	}
	if param != "" {
		body.Param = &param
	}
	writeJSON(w, status, apiError{Error: body})
}

func writeOverflow(w http.ResponseWriter, window, tokens int) {
	writeError(w, http.StatusBadRequest, "invalid_request_error", "context_length_exceeded", "messages",
		fmt.Sprintf("This model's maximum context length is %d tokens. However, your messages resulted in %d tokens. Please reduce the length of the messages.", window, tokens))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.listModels)
	mux.HandleFunc("GET /v1/models/{id...}", s.getModel)
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("POST /v1/responses", s.responses)
	return s.record(mux)
}

func (s *Server) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "", "", "mockllm: reading body: "+err.Error())
			return
		}
		s.mu.Lock()
		s.recorded = append(s.recorded, Recorded{Method: r.Method, Path: r.URL.RequestURI(), Header: r.Header.Clone(), Body: body})
		if r.Method == http.MethodPost && (r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/responses") {
			s.requests = append(s.requests, body)
		}
		s.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

type modelJSON struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Created       int64  `json:"created"`
	OwnedBy       string `json:"owned_by"`
	ContextLength int    `json:"context_length,omitempty"` // OpenRouter style
	MaxModelLen   int    `json:"max_model_len,omitempty"`  // vLLM style
}

func toModelJSON(m core.ModelInfo) modelJSON {
	return modelJSON{ID: m.Ref.Model, Object: "model", Created: createdAt, OwnedBy: "mockllm", ContextLength: m.Context, MaxModelLen: m.Context}
}

func (s *Server) listedModels() []core.ModelInfo {
	if len(s.models) == 0 {
		return []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: "mock"}}}
	}
	return s.models
}

func (s *Server) listModels(w http.ResponseWriter, _ *http.Request) {
	data := []modelJSON{}
	for _, m := range s.listedModels() {
		data = append(data, toModelJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, m := range s.listedModels() {
		if m.Ref.Model == id {
			writeJSON(w, http.StatusOK, toModelJSON(m))
			return
		}
	}
	writeError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", "model", fmt.Sprintf("The model `%s` does not exist", id))
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "", "mockllm: request body is not valid JSON: "+err.Error())
		return
	}
	info, ok := s.model(req.Model)
	if !ok {
		writeError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", "model", fmt.Sprintf("The model `%s` does not exist", req.Model))
		return
	}
	est := estimateTokens(&req)
	rule, ok := s.begin(w, r, info, est, analyze(req.Messages))
	if !ok {
		return
	}
	if hasTools(req.Tools) && refusesTools(w, rule.Fault, req.Model, req.ReasoningEffort) {
		return
	}
	p := s.plan(rule, req.Model, est)
	if req.Stream {
		s.stream(w, r, p, rule, req.StreamOptions != nil && req.StreamOptions.IncludeUsage)
	} else {
		s.complete(w, r, p, rule)
	}
}

// begin checks the window, matches a rule and serves the HTTP-level faults.
// ok is false when a response was written; on success the rule's slow_ttft
// wait is over.
func (s *Server) begin(w http.ResponseWriter, r *http.Request, info core.ModelInfo, est int, t turn) (Rule, bool) {
	if info.Context > 0 && est > info.Context {
		writeOverflow(w, info.Context, est)
		return Rule{}, false
	}
	rule, ok := s.match(t)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "no_rule", "", "mockllm: no rule matched the request")
		return Rule{}, false
	}
	switch rule.Fault {
	case FaultRateLimit:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "requests", "rate_limit_exceeded", "", "Rate limit reached for requests. Please try again in 1s.")
		return Rule{}, false
	case FaultServer:
		writeError(w, http.StatusInternalServerError, "server_error", "", "", "The server had an error while processing your request.")
		return Rule{}, false
	case FaultOverflow:
		window := info.Context
		if window == 0 {
			window = overflowContext
		}
		writeOverflow(w, window, max(est, window+1))
		return Rule{}, false
	case FaultSlowTTFT:
		wait := rule.Delay
		if wait <= 0 {
			wait = DefaultSlowTTFT
		}
		if !s.sleep(r, wait) {
			return Rule{}, false
		}
		rule.Delay = 0
	}
	return rule, true
}

func hasTools(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && string(t) != "null" && string(t) != "[]"
}

// refusesTools answers like OpenAI's Chat Completions for a model that takes
// function tools only with reasoning off (tools_reasoning), or never
// (tools_responses_only). The first message is OpenAI's, verbatim; the
// second is unverified wording.
func refusesTools(w http.ResponseWriter, fault, model, effort string) bool {
	switch {
	case fault != FaultToolsReasoning && fault != FaultToolsResponsesOnly:
		return false
	case effort != "none":
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", "", "Function tools with reasoning_effort are not supported for "+model+
			" in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.")
		return true
	case fault == FaultToolsResponsesOnly:
		writeError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_value", "reasoning_effort", "Unsupported value: 'none' is not supported with the "+model+" model.")
		return true
	}
	return false
}

// sleep reports false when the client went away or the server is closing.
func (s *Server) sleep(r *http.Request, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
	case <-s.closed:
	}
	return false
}

type usageJSON struct {
	PromptTokens            int             `json:"prompt_tokens"`
	CompletionTokens        int             `json:"completion_tokens"`
	TotalTokens             int             `json:"total_tokens"`
	PromptTokensDetails     promptDetails   `json:"prompt_tokens_details"`
	CompletionTokensDetails responseDetails `json:"completion_tokens_details"`
}

type promptDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responseDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type plannedCall struct{ id, name, args string }

type planned struct {
	id, model string
	seq       int
	reasoning string
	text      []string
	calls     []plannedCall
	usage     *usageJSON
	u         *core.Usage // what usage reports, in core terms
}

func (s *Server) plan(rule Rule, model string, est int) planned {
	s.mu.Lock()
	s.respSeq++
	p := planned{id: "chatcmpl-mock" + strconv.Itoa(s.respSeq), model: model, seq: s.respSeq, reasoning: rule.Reasoning, text: rule.Text}
	out := len(rule.Reasoning)
	for _, t := range rule.Text {
		out += len(t)
	}
	for _, tc := range rule.ToolCalls {
		id := tc.ID
		if id == "" {
			s.callSeq++
			id = "call_mock" + strconv.Itoa(s.callSeq)
		}
		args := string(tc.Args)
		if args == "" {
			args = "{}"
		}
		out += len(tc.Name) + len(args)
		p.calls = append(p.calls, plannedCall{id: id, name: tc.Name, args: args})
	}
	s.mu.Unlock()
	if rule.NoUsage {
		return p
	}
	u := core.Usage{Input: est, Output: max(1, (out+3)/4)}
	if rule.Usage != nil {
		u = *rule.Usage
	}
	p.u = &u
	prompt := u.Input + u.CacheRead + u.CacheWrite
	p.usage = &usageJSON{
		PromptTokens: prompt, CompletionTokens: u.Output, TotalTokens: prompt + u.Output,
		PromptTokensDetails: promptDetails{CachedTokens: u.CacheRead}, CompletionTokensDetails: responseDetails{ReasoningTokens: u.Reasoning},
	}
	return p
}

func (p planned) finishReason() string {
	if len(p.calls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usageJSON    `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int     `json:"index"`
	ID       string  `json:"id,omitempty"`
	Type     string  `json:"type,omitempty"`
	Function fnDelta `json:"function"`
}

type fnDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

func (p planned) frames(includeUsage bool) [][]byte {
	var out [][]byte
	add := func(choices []chunkChoice, u *usageJSON) {
		b, _ := json.Marshal(chunk{ID: p.id, Object: "chat.completion.chunk", Created: createdAt, Model: p.model, Choices: choices, Usage: u})
		out = append(out, b)
	}
	one := func(d delta) []chunkChoice { return []chunkChoice{{Delta: d}} }
	empty := ""
	add(one(delta{Role: "assistant", Content: &empty}), nil)
	for _, t := range p.text {
		add(one(delta{Content: &t}), nil)
	}
	for i, c := range p.calls {
		add(one(delta{ToolCalls: []toolCallDelta{{Index: i, ID: c.id, Type: "function", Function: fnDelta{Name: c.name}}}}), nil)
		for _, piece := range split3(c.args) {
			add(one(delta{ToolCalls: []toolCallDelta{{Index: i, Function: fnDelta{Arguments: piece}}}}), nil)
		}
	}
	finish := p.finishReason()
	add([]chunkChoice{{Delta: delta{}, FinishReason: &finish}}, nil)
	if includeUsage && p.usage != nil {
		add([]chunkChoice{}, p.usage)
	}
	return out
}

// split3 cuts s into up to three pieces on rune boundaries so clients must
// reassemble tool-call arguments.
func split3(s string) []string {
	size := (len(s) + 2) / 3
	var out []string
	for len(s) > 0 {
		n := min(size, len(s))
		for n < len(s) && !utf8.RuneStart(s[n]) {
			n++
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, p planned, rule Rule, includeUsage bool) {
	frames := p.frames(includeUsage)
	cut, garble := len(frames), -1
	switch rule.Fault {
	case FaultMidstream:
		cut = max(1, len(frames)/2)
	case FaultMalformed:
		garble = len(frames) / 2
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	for i, f := range frames[:cut] {
		if i > 0 && rule.Delay > 0 && !s.sleep(r, rule.Delay) {
			return
		}
		if i == garble {
			f = f[:len(f)/2]
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", f); err != nil {
			return
		}
		_ = rc.Flush()
	}
	if rule.Fault == FaultMidstream {
		panic(http.ErrAbortHandler) // drops the connection without the terminating chunk
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	_ = rc.Flush()
}

type completion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *usageJSON         `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int         `json:"index"`
	Message      respMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type respMessage struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	ToolCalls []respToolCall `json:"tool_calls,omitempty"`
}

type respToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request, p planned, rule Rule) {
	msg := respMessage{Role: "assistant"}
	if text := strings.Join(p.text, ""); text != "" || len(p.calls) == 0 {
		msg.Content = &text
	}
	for _, c := range p.calls {
		tc := respToolCall{ID: c.id, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = c.name, c.args
		msg.ToolCalls = append(msg.ToolCalls, tc)
	}
	body, _ := json.Marshal(completion{
		ID: p.id, Object: "chat.completion", Created: createdAt, Model: p.model,
		Choices: []completionChoice{{Message: msg, FinishReason: p.finishReason()}}, Usage: p.usage,
	})
	if rule.Delay > 0 && !s.sleep(r, rule.Delay) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch rule.Fault {
	case FaultMalformed:
		body = body[:len(body)/2]
	case FaultMidstream:
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:len(body)/2])
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler) // the client sees fewer bytes than Content-Length
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
