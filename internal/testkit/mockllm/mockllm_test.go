package mockllm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

type msg struct {
	Role       string `json:"role"`
	Content    any    `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type req struct {
	Model         string          `json:"model"`
	Messages      []msg           `json:"messages"`
	Stream        bool            `json:"stream,omitempty"`
	StreamOptions map[string]bool `json:"stream_options,omitempty"`
	Tools         []any           `json:"tools,omitempty"`
}

func user(text string) req {
	return req{Model: "mock", Messages: []msg{{Role: "system", Content: "You are Bothy."}, {Role: "user", Content: text}}}
}

func streaming(r req) req {
	r.Stream, r.StreamOptions = true, map[string]bool{"include_usage": true}
	return r
}

func newServer(t *testing.T, rules []Rule, models ...core.ModelInfo) *Server {
	t.Helper()
	s := New(rules, models)
	t.Cleanup(s.Close)
	return s
}

func post(t *testing.T, client *http.Client, s *Server, body req) (*http.Response, error) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	hr, err := http.NewRequest(http.MethodPost, s.URL()+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer sk-test")
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(hr)
}

func mustPost(t *testing.T, s *Server, body req) *http.Response {
	t.Helper()
	resp, err := post(t, nil, s, body)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

type completionResp struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageJSON `json:"usage"`
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

type errorResp struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    *string `json:"code"`
	} `json:"error"`
}

func wantError(t *testing.T, resp *http.Response, status int, code string) errorResp {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d", resp.StatusCode, status)
	}
	e := decode[errorResp](t, resp)
	got := ""
	if e.Error.Code != nil {
		got = *e.Error.Code
	}
	if got != code || e.Error.Message == "" {
		t.Fatalf("error = %+v, want code %q and a message", e.Error, code)
	}
	return e
}

// sse reads data frames until [DONE]; err reports a broken stream.
func sse(r io.Reader) (frames []string, done bool, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			return frames, true, nil
		}
		frames = append(frames, data)
	}
	return frames, false, sc.Err()
}

type chunkResp struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Choices []struct {
		Delta struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageJSON `json:"usage"`
}

type assembled struct {
	text    string
	calls   []core.ToolCall
	finish  string
	usage   *usageJSON
	nFrames int
}

func assemble(t *testing.T, frames []string) assembled {
	t.Helper()
	var a assembled
	a.nFrames = len(frames)
	for _, f := range frames {
		var c chunkResp
		if err := json.Unmarshal([]byte(f), &c); err != nil {
			t.Fatalf("frame %q: %v", f, err)
		}
		if c.Object != "chat.completion.chunk" || c.ID == "" {
			t.Fatalf("frame %q: bad envelope", f)
		}
		if c.Usage != nil {
			a.usage = c.Usage
		}
		for _, ch := range c.Choices {
			if ch.Delta.Content != nil {
				a.text += *ch.Delta.Content
			}
			for _, tc := range ch.Delta.ToolCalls {
				for len(a.calls) <= tc.Index {
					a.calls = append(a.calls, core.ToolCall{})
				}
				call := &a.calls[tc.Index]
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if tc.Function.Name != "" {
					call.Name = tc.Function.Name
				}
				call.Args = append(call.Args, tc.Function.Arguments...)
			}
			if ch.FinishReason != nil {
				a.finish = *ch.FinishReason
			}
		}
	}
	return a
}

func TestModels(t *testing.T) {
	s := newServer(t, nil, core.ModelInfo{Ref: core.ModelRef{Provider: "mock", Model: "mock-16k"}, Context: 16384})
	resp, err := http.Get(s.URL() + "/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	list := decode[struct {
		Object string      `json:"object"`
		Data   []modelJSON `json:"data"`
	}](t, resp)
	if list.Object != "list" || len(list.Data) != 1 || list.Data[0].ID != "mock-16k" || list.Data[0].ContextLength != 16384 || list.Data[0].Object != "model" {
		t.Fatalf("models = %+v", list)
	}

	one, err := http.Get(s.URL() + "/models/mock-16k")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Body.Close()
	if m := decode[modelJSON](t, one); m.ID != "mock-16k" {
		t.Fatalf("model = %+v", m)
	}
	missing, err := http.Get(s.URL() + "/models/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	wantError(t, missing, http.StatusNotFound, "model_not_found")

	if got := mustPost(t, s, req{Model: "nope", Messages: []msg{{Role: "user", Content: "hi"}}}); got.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown model status = %d", got.StatusCode)
	}
	if !strings.HasSuffix(s.URL(), "/v1") {
		t.Fatalf("URL %q does not end in /v1", s.URL())
	}
}

func TestDefaultModelAcceptsAnyID(t *testing.T) {
	s := newServer(t, []Rule{{Text: []string{"ok"}}})
	resp, err := http.Get(s.URL() + "/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.Contains(string(must(io.ReadAll(resp.Body))), `"id":"mock"`) {
		t.Fatal("default model not listed")
	}
	r := user("hi")
	r.Model = "anything/at-all"
	if got := mustPost(t, s, r); got.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", got.StatusCode)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestNonStreamText(t *testing.T) {
	s := newServer(t, []Rule{{MatchUser: "^hello", Text: []string{"Hi ", "there"}}})
	resp := mustPost(t, s, user("hello world"))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	c := decode[completionResp](t, resp)
	if c.Object != "chat.completion" || c.Model != "mock" || len(c.Choices) != 1 {
		t.Fatalf("completion = %+v", c)
	}
	ch := c.Choices[0]
	if ch.Message.Role != "assistant" || ch.Message.Content == nil || *ch.Message.Content != "Hi there" || ch.FinishReason != "stop" {
		t.Fatalf("choice = %+v", ch)
	}
	if u := c.Usage; u == nil || u.PromptTokens == 0 || u.CompletionTokens == 0 || u.TotalTokens != u.PromptTokens+u.CompletionTokens {
		t.Fatalf("estimated usage = %+v", c.Usage)
	}
}

func TestNonStreamToolCalls(t *testing.T) {
	s := newServer(t, []Rule{{ToolCalls: []core.ToolCall{{Name: "shell", Args: json.RawMessage(`{"command":"ls"}`)}}}})
	c := decode[completionResp](t, mustPost(t, s, user("list files")))
	m := c.Choices[0].Message
	if m.Content != nil || len(m.ToolCalls) != 1 || c.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("message = %+v finish %q", m, c.Choices[0].FinishReason)
	}
	tc := m.ToolCalls[0]
	if tc.ID == "" || tc.Type != "function" || tc.Function.Name != "shell" || tc.Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("tool call = %+v", tc)
	}
}

func TestStreamTextAndParallelToolCalls(t *testing.T) {
	usage := &core.Usage{Input: 100, Output: 7, CacheRead: 50, CacheWrite: 5, Reasoning: 3}
	s := newServer(t, []Rule{{
		Text: []string{"Let me ", "check. ✓"},
		ToolCalls: []core.ToolCall{
			{ID: "call_a", Name: "web_search", Args: json.RawMessage(`{"query":"weather in Edinburgh, Scotland"}`)},
			{Name: "fs_read", Args: json.RawMessage(`{"path":"notes/é.md"}`)},
		},
		Usage: usage,
	}})
	resp := mustPost(t, s, streaming(user("weather?")))
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	frames, done, err := sse(resp.Body)
	if err != nil || !done {
		t.Fatalf("stream done=%v err=%v", done, err)
	}
	a := assemble(t, frames)
	if a.text != "Let me check. ✓" || a.finish != "tool_calls" {
		t.Fatalf("text %q finish %q", a.text, a.finish)
	}
	if len(a.calls) != 2 || a.calls[0].ID != "call_a" || a.calls[1].ID == "" || a.calls[1].ID == "call_a" {
		t.Fatalf("calls = %+v", a.calls)
	}
	if string(a.calls[0].Args) != `{"query":"weather in Edinburgh, Scotland"}` || a.calls[1].Name != "fs_read" || string(a.calls[1].Args) != `{"path":"notes/é.md"}` {
		t.Fatalf("calls = %s %s", a.calls[0].Args, a.calls[1].Args)
	}
	// role + 2 text + per call (start + 3 argument pieces) + finish + usage
	if a.nFrames != 1+2+2*4+1+1 {
		t.Fatalf("frames = %d: %q", a.nFrames, frames)
	}
	want := usageJSON{PromptTokens: 155, CompletionTokens: 7, TotalTokens: 162,
		PromptTokensDetails: promptDetails{CachedTokens: 50}, CompletionTokensDetails: responseDetails{ReasoningTokens: 3}}
	if a.usage == nil || *a.usage != want {
		t.Fatalf("usage = %+v, want %+v", a.usage, want)
	}
	var last chunkResp
	if err := json.Unmarshal([]byte(frames[len(frames)-1]), &last); err != nil || last.Choices == nil || len(last.Choices) != 0 {
		t.Fatalf("usage frame must carry an empty choices array: %s", frames[len(frames)-1])
	}
}

func TestStreamUsageOnlyWhenRequested(t *testing.T) {
	s := newServer(t, []Rule{{MatchUser: "plain", Text: []string{"a"}}, {MatchUser: "quirk", Text: []string{"b"}, NoUsage: true}})
	plain := user("plain")
	plain.Stream = true
	frames, done, err := sse(mustPost(t, s, plain).Body)
	if err != nil || !done {
		t.Fatal(done, err)
	}
	if a := assemble(t, frames); a.usage != nil || a.text != "a" {
		t.Fatalf("usage sent without include_usage: %+v", a)
	}
	frames, _, _ = sse(mustPost(t, s, streaming(user("quirk"))).Body)
	if a := assemble(t, frames); a.usage != nil {
		t.Fatalf("no_usage rule sent usage: %+v", a.usage)
	}
	body := must(io.ReadAll(mustPost(t, s, user("quirk")).Body))
	if bytes.Contains(body, []byte(`"usage"`)) {
		t.Fatalf("no_usage rule sent usage: %s", body)
	}
}

func TestMatching(t *testing.T) {
	s := newServer(t, []Rule{
		{MatchSystem: "(?i)summarize", Text: []string{"## Goals"}},
		{MatchToolResult: "sunny", Text: []string{"It is sunny."}},
		{MatchUser: "weather", ToolCalls: []core.ToolCall{{Name: "web_search"}}},
		{MatchUser: "once", Text: []string{"first"}, Times: 1},
		{MatchUser: "once", Text: []string{"second"}},
		{MatchUser: "parts", Text: []string{"saw parts"}},
	})
	text := func(r req) string {
		t.Helper()
		resp := mustPost(t, s, r)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d for %+v", resp.StatusCode, r)
		}
		c := decode[completionResp](t, resp)
		if c.Choices[0].Message.Content == nil {
			return "<tool:" + c.Choices[0].Message.ToolCalls[0].Function.Name + ">"
		}
		return *c.Choices[0].Message.Content
	}

	if got := text(user("what's the weather")); got != "<tool:web_search>" {
		t.Fatalf("fresh turn = %q", got)
	}
	round := user("what's the weather")
	round.Messages = append(round.Messages, msg{Role: "assistant", Content: nil}, msg{Role: "tool", ToolCallID: "call_1", Content: "Edinburgh: sunny"})
	if got := text(round); got != "It is sunny." {
		t.Fatalf("after tool round = %q", got)
	}
	next := round
	next.Messages = append(append([]msg(nil), round.Messages...), msg{Role: "assistant", Content: "It is sunny."}, msg{Role: "user", Content: "and the weather tomorrow?"})
	if got := text(next); got != "<tool:web_search>" {
		t.Fatalf("an old tool result must not match a new turn, got %q", got)
	}
	compaction := user("conversation to compress")
	compaction.Messages[0].Content = "Summarize the conversation."
	if got := text(compaction); got != "## Goals" {
		t.Fatalf("system match = %q", got)
	}
	if a, b := text(user("once")), text(user("once")); a != "first" || b != "second" {
		t.Fatalf("times: %q then %q", a, b)
	}
	parts := req{Model: "mock", Messages: []msg{{Role: "user", Content: []map[string]string{{"type": "text", "text": "content parts"}}}}}
	if got := text(parts); got != "saw parts" {
		t.Fatalf("content parts = %q", got)
	}
	wantError(t, mustPost(t, s, user("unmatched")), http.StatusBadRequest, "no_rule")

	bodies := s.Requests()
	if len(bodies) != 8 {
		t.Fatalf("recorded %d bodies", len(bodies))
	}
	if !bytes.Contains(bodies[0], []byte("what's the weather")) || !bytes.Contains(bodies[7], []byte("unmatched")) {
		t.Fatal("bodies out of order")
	}
}

func TestHistory(t *testing.T) {
	s := newServer(t, []Rule{{Text: []string{"ok"}}})
	if _, err := http.Get(s.URL() + "/models?probe=1"); err != nil {
		t.Fatal(err)
	}
	mustPost(t, s, user("hi"))
	h := s.History()
	if len(h) != 2 || h[0].Method != http.MethodGet || h[0].Path != "/v1/models?probe=1" || h[1].Path != "/v1/chat/completions" {
		t.Fatalf("history = %+v", h)
	}
	if h[1].Header.Get("Authorization") != "Bearer sk-test" || !bytes.Contains(h[1].Body, []byte(`"hi"`)) {
		t.Fatalf("history entry = %+v", h[1])
	}
	if len(s.Requests()) != 1 {
		t.Fatal("Requests must hold chat completions only")
	}
}

func TestErrorFaults(t *testing.T) {
	small := core.ModelInfo{Ref: core.ModelRef{Provider: "mock", Model: "mock"}, Context: 64}
	s := newServer(t, []Rule{
		{MatchUser: "rate", Fault: FaultRateLimit},
		{MatchUser: "boom", Fault: FaultServer},
		{MatchUser: "overflow", Fault: FaultOverflow},
		{Text: []string{"ok"}},
	}, small)

	resp := mustPost(t, s, user("rate"))
	if resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}
	wantError(t, resp, http.StatusTooManyRequests, "rate_limit_exceeded")

	resp = mustPost(t, s, user("boom"))
	if resp.StatusCode != http.StatusInternalServerError || decode[errorResp](t, resp).Error.Type != "server_error" {
		t.Fatalf("500 fault: %d", resp.StatusCode)
	}

	e := wantError(t, mustPost(t, s, user("overflow")), http.StatusBadRequest, "context_length_exceeded")
	if e.Error.Type != "invalid_request_error" || !strings.Contains(e.Error.Message, "maximum context length is 64 tokens") {
		t.Fatalf("overflow = %+v", e.Error)
	}

	long := user(strings.Repeat("x", 400))
	wantError(t, mustPost(t, s, long), http.StatusBadRequest, "context_length_exceeded")
	if got := mustPost(t, s, user("short")); got.StatusCode != http.StatusOK {
		t.Fatalf("short prompt status %d", got.StatusCode)
	}

	bad, err := http.Post(s.URL()+"/chat/completions", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	wantError(t, bad, http.StatusBadRequest, "invalid_json")
}

func TestMidstreamDisconnect(t *testing.T) {
	s := newServer(t, []Rule{{Text: []string{"one ", "two ", "three ", "four"}, Fault: FaultMidstream}})
	frames, done, err := sse(mustPost(t, s, streaming(user("go"))).Body)
	if done || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want an unexpected EOF before [DONE], got done=%v err=%v", done, err)
	}
	if a := assemble(t, frames); a.text == "" || a.finish != "" {
		t.Fatalf("want partial text and no finish, got %+v", a)
	}

	resp := mustPost(t, s, user("go"))
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("non-stream body read completed despite the disconnect")
	}
}

func TestMalformed(t *testing.T) {
	s := newServer(t, []Rule{{Text: []string{"a", "b", "c"}, Fault: FaultMalformed}})
	frames, done, err := sse(mustPost(t, s, streaming(user("go"))).Body)
	if err != nil || !done {
		t.Fatalf("malformed stream should still end with [DONE]: done=%v err=%v", done, err)
	}
	broken := 0
	for _, f := range frames {
		if !json.Valid([]byte(f)) {
			broken++
		}
	}
	if broken != 1 {
		t.Fatalf("%d broken frames, want 1: %q", broken, frames)
	}
	body := must(io.ReadAll(mustPost(t, s, user("go")).Body))
	if json.Valid(body) {
		t.Fatalf("non-stream body is valid JSON: %s", body)
	}
}

func TestSlowTTFT(t *testing.T) {
	s := newServer(t, []Rule{
		{MatchUser: "hang", Fault: FaultSlowTTFT},
		{MatchUser: "slow", Fault: FaultSlowTTFT, Delay: 80 * time.Millisecond, Text: []string{"late"}},
	})
	client := &http.Client{Timeout: 100 * time.Millisecond}
	start := time.Now()
	if _, err := post(t, client, s, streaming(user("hang"))); err == nil {
		t.Fatal("hung response did not time out")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("client timeout not honoured")
	}

	start = time.Now()
	resp := mustPost(t, s, streaming(user("slow")))
	frames, done, err := sse(resp.Body)
	if err != nil || !done || assemble(t, frames).text != "late" {
		t.Fatalf("slow response: done=%v err=%v", done, err)
	}
	if took := time.Since(start); took < 80*time.Millisecond {
		t.Fatalf("first byte after %v, want >= 80ms", took)
	}
}

func TestCloseInterruptsSlowResponses(t *testing.T) {
	s := New([]Rule{{Fault: FaultSlowTTFT, Delay: time.Hour}}, nil)
	errc := make(chan error, 1)
	go func() {
		resp, err := post(t, nil, s, user("hang"))
		if err == nil {
			resp.Body.Close()
		}
		errc <- err
	}()
	if !waitFor(func() bool { return len(s.Requests()) == 1 }) {
		t.Fatal("request never arrived")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a slow response")
	}
	<-errc
	s.Close()
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestDelayBetweenChunks(t *testing.T) {
	s := newServer(t, []Rule{{Text: []string{"a", "b", "c"}, Delay: 20 * time.Millisecond}})
	start := time.Now()
	frames, done, err := sse(mustPost(t, s, streaming(user("go"))).Body)
	if err != nil || !done || assemble(t, frames).text != "abc" {
		t.Fatal(done, err)
	}
	if took := time.Since(start); took < 80*time.Millisecond {
		t.Fatalf("stream took %v, want >= 4 delays", took)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	slow := New([]Rule{{Text: []string{"a", "b"}, Delay: time.Hour}}, nil)
	defer slow.Close()
	b, _ := json.Marshal(streaming(user("go")))
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, slow.URL()+"/chat/completions", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, done, _ := sse(resp.Body); done {
		t.Fatal("stream completed despite the client cancelling")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	script := `
models:
  - id: mock-16k
    context: 16384
    max_output: 2048
    tools: true
rules:
  - match_user: weather
    tool_calls:
      - name: web_search
        args: {query: "weather today", limit: 3}
      - id: call_fixed
        name: fs_read
        args: '{"path":"a.txt"}'
      - name: tool_search
    times: 1
  - match_user: limit
    fault: 429
  - text: ["It is ", "sunny."]
    usage: {input: 120, output: 4, cache_read: 100}
    delay: 1ms
`
	path := filepath.Join(dir, "script.yaml")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	r := user("weather?")
	r.Model = "mock-16k"
	c := decode[completionResp](t, mustPost(t, s, r))
	calls := c.Choices[0].Message.ToolCalls
	if len(calls) != 3 || calls[0].Function.Arguments != `{"limit":3,"query":"weather today"}` ||
		calls[1].ID != "call_fixed" || calls[1].Function.Arguments != `{"path":"a.txt"}` || calls[2].Function.Arguments != "{}" {
		t.Fatalf("tool calls = %+v", calls)
	}
	r.Messages[1].Content = "limit"
	wantError(t, mustPost(t, s, r), http.StatusTooManyRequests, "rate_limit_exceeded")
	r.Messages[1].Content = "weather again"
	c = decode[completionResp](t, mustPost(t, s, r))
	if *c.Choices[0].Message.Content != "It is sunny." || c.Usage.PromptTokens != 220 || c.Usage.PromptTokensDetails.CachedTokens != 100 {
		t.Fatalf("fallthrough rule = %+v usage %+v", c.Choices[0], c.Usage)
	}
	r.Model = "mock"
	if got := mustPost(t, s, r); got.StatusCode != http.StatusNotFound {
		t.Fatalf("unlisted model status %d", got.StatusCode)
	}
}

func TestLoadBareList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte("- text: [hi]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if c := decode[completionResp](t, mustPost(t, s, user("x"))); *c.Choices[0].Message.Content != "hi" {
		t.Fatalf("got %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, script := range map[string]string{
		"bad regexp":   "rules:\n  - match_user: '('\n",
		"bad fault":    "rules:\n  - fault: teapot\n",
		"bad args":     "rules:\n  - tool_calls: [{name: x, args: 'not json'}]\n",
		"bad duration": "rules:\n  - delay: soon\n",
		"model id":     "models:\n  - context: 5\n",
		"not yaml":     "rules: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.yaml")
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			if s, err := Load(path); err == nil {
				s.Close()
				t.Fatal("Load accepted an invalid script")
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("Load accepted a missing file")
	}
}

func TestNewPanicsOnInvalidRule(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New accepted an invalid regexp")
		}
	}()
	New([]Rule{{MatchUser: "("}}, nil).Close()
}
