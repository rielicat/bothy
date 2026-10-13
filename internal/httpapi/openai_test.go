package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
)

// scriptRunner is fakes.Runner that plays a scripted event sequence into
// each request's sink, as the real runner does.
type scriptRunner struct {
	*fakes.Runner
	script func(req core.RunRequest, run core.RunID) []core.Event
	block  chan struct{} // when set, the run never finishes until closed
	mu     sync.Mutex
	stops  []core.SessionID
}

func (r *scriptRunner) Submit(ctx context.Context, req core.RunRequest) (core.SubmitResult, error) {
	res, err := r.Runner.Submit(ctx, req)
	if err != nil {
		return res, err
	}
	evs := r.script(req, res.Run)
	go func() {
		for _, e := range evs {
			e.Run, e.Session, e.Principal = res.Run, req.Session, req.Principal
			if e.Type == core.EvRunFinished && r.block != nil {
				<-r.block
			}
			req.Sink.OnEvent(e)
		}
	}()
	return res, nil
}

func (r *scriptRunner) Stop(_ context.Context, _ core.Viewer, s core.SessionID, _ core.UserID) (bool, error) {
	r.mu.Lock()
	r.stops = append(r.stops, s)
	r.mu.Unlock()
	return true, nil
}

func (r *scriptRunner) stopped() []core.SessionID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.stops)
}

func defaultScript(core.RunRequest, core.RunID) []core.Event {
	return []core.Event{
		{Type: core.EvRunStarted, Data: core.RunStartedData{Trigger: core.TrigAPI, Model: "fake/fake-model"}},
		{Type: core.EvRunDelta, Data: core.DeltaData{Text: "Let me"}},
		{Type: core.EvRunDelta, Data: core.DeltaData{Text: " check."}},
		{Type: core.EvToolCall, Data: core.ToolCallData{CallID: "tcl_1", Tool: "web_search", Verdict: core.VerdictAuto}},
		{Type: core.EvToolResult, Data: core.ToolResultData{CallID: "tcl_1", Status: core.TCOK}},
		{Type: core.EvRunDelta, Data: core.DeltaData{Text: "Sunny, 24°C."}},
		{Type: core.EvRunUsage, Data: core.UsageData{Call: core.CallInfo{Stop: core.StopEnd}}},
		{Type: core.EvRunFinished, Data: core.RunFinishedData{Status: core.RunDone, Steps: 2,
			Usage: core.Usage{Input: 100, CacheRead: 400, Output: 30}, Cost: 1234}},
	}
}

func (h *harness) useScriptRunner(script func(core.RunRequest, core.RunID) []core.Event) *scriptRunner {
	sr := &scriptRunner{Runner: h.runner, script: script}
	h.srv.d.Runner = sr
	return sr
}

// oaClientChunk mirrors the fields openai-python's ChatCompletionChunk
// requires; decodeChunk fails when any is missing.
type oaClientChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string       `json:"role"`
			Content   *string      `json:"content"`
			ToolCalls []oaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
	Bothy *struct {
		Event string `json:"event"`
		Tool  string `json:"tool"`
	} `json:"bothy"`
}

func decodeChunk(t *testing.T, data string) oaClientChunk {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		t.Fatalf("chunk is not JSON: %q", data)
	}
	for _, k := range []string{"id", "object", "created", "model", "choices"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("chunk lacks required %q: %s", k, data)
		}
	}
	var c oaClientChunk
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		t.Fatal(err)
	}
	if c.Object != "chat.completion.chunk" {
		t.Fatalf("object = %q", c.Object)
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(raw["choices"], &choices); err != nil {
		t.Fatalf("choices: %v", err)
	}
	for _, ch := range choices {
		for _, k := range []string{"index", "delta", "finish_reason"} {
			if _, ok := ch[k]; !ok {
				t.Fatalf("choice lacks required %q: %s", k, data)
			}
		}
	}
	return c
}

// readStream applies the client's SSE rules: "data:" lines carry JSON until
// "[DONE]", comment lines (":") are ignored.
func readStream(t *testing.T, body io.Reader) (chunks []oaClientChunk, comments []string) {
	t.Helper()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	done := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
		case strings.HasPrefix(line, ":"):
			comments = append(comments, strings.TrimSpace(line[1:]))
		case line == "data: [DONE]":
			done = true
		case strings.HasPrefix(line, "data: "):
			if done {
				t.Fatal("data after [DONE]")
			}
			chunks = append(chunks, decodeChunk(t, line[6:]))
		default:
			t.Fatalf("unexpected line %q", line)
		}
	}
	if !done {
		t.Fatal("stream ended without [DONE]")
	}
	return chunks, comments
}

func TestV1ModelsByScope(t *testing.T) {
	h := newHarness(t).setup()
	type list struct {
		Object string    `json:"object"`
		Data   []oaModel `json:"data"`
	}
	ids := func(l list) []string {
		var out []string
		for _, m := range l.Data {
			out = append(out, m.ID)
		}
		return out
	}
	l := decodeAs[list](t, h.do("GET", "/v1/models", nil, h.tokenFor(h.owner, "chat")))
	if l.Object != "list" || !slices.Equal(ids(l), []string{"bothy", "bothy-fast"}) || l.Data[0].Created != h.srv.boot.Unix() || l.Data[0].OwnedBy != "bothy" {
		t.Fatalf("chat models = %+v", l)
	}
	l = decodeAs[list](t, h.do("GET", "/v1/models", nil, h.tokenFor(h.owner, "llm")))
	if !slices.Equal(ids(l), []string{"fake/fake-model"}) {
		t.Fatalf("llm models = %v", ids(l))
	}
	h.cfg.update(func(c *configT) { c.API.Passthrough = false })
	l = decodeAs[list](t, h.do("GET", "/v1/models", nil, h.tokenFor(h.owner, "llm", "chat")))
	if !slices.Equal(ids(l), []string{"bothy", "bothy-fast"}) {
		t.Fatalf("passthrough off = %v", ids(l))
	}
	h.cfg.update(func(c *configT) { c.API.OpenAICompat = false })
	expect(t, h.do("GET", "/v1/models", nil, h.tokenFor(h.owner, "chat")), http.StatusNotFound)
}

func TestV1AgentStreamParsedLikeOfficialClient(t *testing.T) {
	h := newHarness(t).setup()
	sr := h.useScriptRunner(defaultScript)
	body, err := os.ReadFile("testdata/openai_client_request.json")
	if err != nil {
		t.Fatal(err)
	}
	tok := h.tokenFor(h.member, "chat")
	srv := h.server(t)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bothy-Events", "1")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	if resp.Header.Get("X-Bothy-Run-Id") == "" || resp.Header.Get("X-Bothy-Session") == "" || resp.Header.Get("X-Bothy-History") != "" {
		t.Fatalf("headers = %v", resp.Header)
	}
	if got := resp.Header.Get("X-Bothy-Ignored"); got != "temperature" {
		t.Fatalf("X-Bothy-Ignored = %q", got)
	}
	chunks, _ := readStream(t, resp.Body)
	if resp.Trailer.Get("X-Bothy-Cost-Micros") != "1234" {
		t.Fatalf("cost trailer = %v", resp.Trailer)
	}
	var content strings.Builder
	var finish string
	var usage *oaUsage
	var tools []string
	for i, c := range chunks {
		if c.ID != chunks[0].ID || c.Model != "bothy" {
			t.Fatalf("chunk %d id/model = %s/%s", i, c.ID, c.Model)
		}
		if i == 0 && (len(c.Choices) != 1 || c.Choices[0].Delta.Role != "assistant") {
			t.Fatalf("first chunk = %+v", c)
		}
		if c.Bothy != nil {
			tools = append(tools, c.Bothy.Event+":"+c.Bothy.Tool)
		}
		for _, ch := range c.Choices {
			if ch.Delta.Content != nil {
				content.WriteString(*ch.Delta.Content)
			}
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
		if c.Usage != nil {
			if len(c.Choices) != 0 {
				t.Fatal("usage chunk must have empty choices")
			}
			usage = c.Usage
		}
	}
	if content.String() != "Let me check.\n\nSunny, 24°C." || finish != "stop" {
		t.Fatalf("content %q finish %q", content.String(), finish)
	}
	if usage == nil || usage.PromptTokens != 500 || usage.CompletionTokens != 30 || usage.TotalTokens != 530 || usage.PromptTokensDetails.CachedTokens != 400 {
		t.Fatalf("usage = %+v", usage)
	}
	if !slices.Equal(tools, []string{"run.tool_call:web_search", "run.tool_result:"}) {
		t.Fatalf("bothy events = %v", tools)
	}
	subs := sr.Submitted()
	if len(subs) != 1 {
		t.Fatalf("submits = %d", len(subs))
	}
	rr := subs[0]
	if rr.Trigger != core.TrigAPI || rr.Slot != core.SlotMain || rr.Instructions != "Answer briefly." || rr.OnApproval != "deny" ||
		rr.Provenance != core.ProvUser || !strings.HasPrefix(rr.Sender, "api:tok_") || len(rr.Ephemeral) != 0 ||
		len(rr.Input) != 1 || rr.Input[0].Text != "What's the weather in Madrid?" {
		t.Fatalf("run request = %+v", rr)
	}
	sess, err := h.store.Sessions().Get(context.Background(), core.SystemViewer(), rr.Session)
	if err != nil || !sess.Hidden || sess.Origin != "api" {
		t.Fatalf("api session = %+v, %v", sess, err)
	}
}

func TestV1AgentNonStreamAndHistory(t *testing.T) {
	h := newHarness(t).setup()
	sr := h.useScriptRunner(defaultScript)
	tok := h.tokenFor(h.member, "chat")
	body := map[string]any{"model": "bothy-fast", "max_tokens": 64, "metadata": map[string]string{"a": "b"}, "top_p": 0.5,
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "hello"},
			{"role": "user", "content": []map[string]any{{"type": "text", "text": "and now?"}}},
		}}
	rec := h.do("POST", "/v1/chat/completions", body, tok)
	expect(t, rec, http.StatusOK)
	resp := decodeAs[oaResponse](t, rec)
	if resp.Object != "chat.completion" || len(resp.Choices) != 1 || *resp.Choices[0].Message.Content != "Let me check.\n\nSunny, 24°C." ||
		resp.Choices[0].FinishReason != "stop" || resp.Usage.TotalTokens != 530 {
		t.Fatalf("response = %+v", resp)
	}
	if rec.Header().Get("X-Bothy-Cost-Micros") != "1234" || rec.Header().Get("X-Bothy-Ignored") != "top_p" {
		t.Fatalf("headers = %v", rec.Header())
	}
	rr := sr.Submitted()[0]
	if rr.Slot != core.SlotFast || rr.MaxOutput != 64 || len(rr.Ephemeral) != 2 || rr.Ephemeral[1].Role != core.MsgAssistant ||
		rr.Ephemeral[0].Provenance != core.ProvUser || rr.Input[0].Text != "and now?" {
		t.Fatalf("run request = %+v", rr)
	}

	// X-Bothy-Session maps to server history: only the last message is used.
	own := decodeAs[core.Session](t, h.do("POST", "/api/sessions", map[string]any{}, h.member))
	rec = h.do("POST", "/v1/chat/completions", body, tok, withHeader("X-Bothy-Session", string(own.ID)))
	expect(t, rec, http.StatusOK)
	if rec.Header().Get("X-Bothy-History") != "server" || rec.Header().Get("X-Bothy-Session") != string(own.ID) {
		t.Fatalf("headers = %v", rec.Header())
	}
	if rr := sr.Submitted()[1]; rr.Session != own.ID || len(rr.Ephemeral) != 0 {
		t.Fatalf("mapped request = %+v", rr)
	}
	foreign := decodeAs[core.Session](t, h.do("POST", "/api/sessions", map[string]any{}, h.owner))
	rec = h.do("POST", "/v1/chat/completions", body, tok, withHeader("X-Bothy-Session", string(foreign.ID)))
	expect(t, rec, http.StatusNotFound)
}

func TestV1AgentRejections(t *testing.T) {
	h := newHarness(t).setup()
	h.useScriptRunner(defaultScript)
	tok := h.tokenFor(h.member, "chat", "llm")
	msgs := []map[string]any{{"role": "user", "content": "hi"}}
	for _, tc := range []struct {
		extra map[string]any
		param string
	}{
		{map[string]any{"tools": []map[string]any{{"type": "function", "function": map[string]any{"name": "f"}}}}, "tools"},
		{map[string]any{"tool_choice": "auto"}, "tool_choice"},
		{map[string]any{"functions": []any{}}, "functions"},
		{map[string]any{"function_call": "auto"}, "function_call"},
		{map[string]any{"n": 2}, "n"},
		{map[string]any{"logprobs": true}, "logprobs"},
		{map[string]any{"audio": map[string]any{}}, "audio"},
		{map[string]any{"prediction": map[string]any{}}, "prediction"},
		{map[string]any{"response_format": map[string]any{"type": "json_object"}}, "response_format"},
	} {
		body := map[string]any{"model": "bothy", "messages": msgs}
		for k, v := range tc.extra {
			body[k] = v
		}
		rec := h.do("POST", "/v1/chat/completions", body, tok)
		expect(t, rec, http.StatusBadRequest)
		e := decodeAs[oaError](t, rec)
		if e.Error.Code != "unsupported_in_agent_mode" || e.Error.Type != "invalid_request_error" || e.Error.Param == nil || *e.Error.Param != tc.param {
			t.Fatalf("%s: error = %+v", tc.param, e.Error)
		}
	}
	for name, body := range map[string]map[string]any{
		"tool message":   {"model": "bothy", "messages": []map[string]any{{"role": "tool", "content": "x", "tool_call_id": "c"}, {"role": "user", "content": "hi"}}},
		"remote image":   {"model": "bothy", "messages": []map[string]any{{"role": "user", "content": []map[string]any{{"type": "image_url", "image_url": map[string]string{"url": "https://evil.example/x.png"}}}}}},
		"last not user":  {"model": "bothy", "messages": []map[string]any{{"role": "user", "content": "hi"}, {"role": "assistant", "content": "yo"}}},
		"empty messages": {"model": "bothy", "messages": []any{}},
	} {
		if rec := h.do("POST", "/v1/chat/completions", body, tok); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := h.do("POST", "/v1/chat/completions", map[string]any{"model": "gpt-4o", "messages": msgs}, tok)
	expect(t, rec, http.StatusNotFound)
	if decodeAs[oaError](t, rec).Error.Code != "model_not_found" {
		t.Fatal(rec.Body.String())
	}
	llmOnly := h.tokenFor(h.member, "llm")
	expect(t, h.do("POST", "/v1/chat/completions", map[string]any{"model": "bothy", "messages": msgs}, llmOnly), http.StatusForbidden)
	chatOnly := h.tokenFor(h.member, "chat")
	expect(t, h.do("POST", "/v1/chat/completions", map[string]any{"model": "fake/fake-model", "messages": msgs}, chatOnly), http.StatusForbidden)
	expect(t, h.do("POST", "/v1/chat/completions", map[string]any{"model": "bothy", "messages": msgs}, h.member), http.StatusUnauthorized)
}

func TestV1AgentImageDataURI(t *testing.T) {
	h := newHarness(t).setup()
	sr := h.useScriptRunner(defaultScript)
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	body := map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": []map[string]any{
		{"type": "text", "text": "what is this?"},
		{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + png}},
	}}}}
	expect(t, h.do("POST", "/v1/chat/completions", body, h.tokenFor(h.member, "chat")), http.StatusOK)
	rr := sr.Submitted()[0]
	if len(rr.Input) != 2 || rr.Input[1].Type != core.PartImage || rr.Input[1].Blob.MIME != "image/png" ||
		!strings.HasPrefix(rr.Input[1].Blob.Ref, ".bothy/in/upl_") {
		t.Fatalf("input = %+v", rr.Input)
	}
}

func TestV1BudgetAndPausedErrors(t *testing.T) {
	h := newHarness(t).setup()
	sr := h.useScriptRunner(defaultScript)
	tok := h.tokenFor(h.member, "chat")
	body := map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	sr.Err = &core.BudgetError{Kind: core.BudgetDailyCost, Limit: 5, Used: 5}
	rec := h.do("POST", "/v1/chat/completions", body, tok)
	expect(t, rec, http.StatusTooManyRequests)
	e := decodeAs[oaError](t, rec)
	if e.Error.Type != "insufficient_quota" || e.Error.Code != "budget_exceeded" {
		t.Fatalf("budget error = %+v", e)
	}
	sr.Err = nil
	sr.PausedV = true
	rec = h.do("POST", "/v1/chat/completions", body, tok)
	expect(t, rec, http.StatusServiceUnavailable)
	sr.PausedV = false
	sr.Err = core.ErrBusy
	rec = h.do("POST", "/v1/chat/completions", body, tok)
	expect(t, rec, http.StatusTooManyRequests)
	if decodeAs[oaError](t, rec).Error.Type != "rate_limit_error" {
		t.Fatal(rec.Body.String())
	}
	sr.Err = nil
	// A provider failure before any output is a 502 upstream_error.
	sr.script = func(core.RunRequest, core.RunID) []core.Event {
		return []core.Event{{Type: core.EvRunFinished, Data: core.RunFinishedData{Status: core.RunFailed, Error: "all providers failed"}}}
	}
	for _, stream := range []bool{false, true} {
		b := map[string]any{"model": "bothy", "stream": stream, "messages": []map[string]any{{"role": "user", "content": "hi"}}}
		rec = h.do("POST", "/v1/chat/completions", b, tok)
		expect(t, rec, http.StatusBadGateway)
		if decodeAs[oaError](t, rec).Error.Code != "upstream_error" {
			t.Fatal(rec.Body.String())
		}
	}
}

func TestV1TokenRateLimitOpenAIShape(t *testing.T) {
	h := newHarness(t).setup()
	h.useScriptRunner(defaultScript)
	ctx := context.Background()
	a, _ := h.auth.FromCookie(ctx, h.member.cookie)
	plain, _, _ := h.auth.CreateToken(ctx, a, core.TokenSpec{Name: "x", Scopes: []string{"chat"}, TTL: time.Hour, RatePerMin: 1})
	tok := &client{name: "rl", token: plain, scopes: []string{"chat"}}
	expect(t, h.do("GET", "/v1/models", nil, tok), http.StatusOK)
	rec := h.do("GET", "/v1/models", nil, tok)
	expect(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" || decodeAs[oaError](t, rec).Error.Type != "rate_limit_error" {
		t.Fatalf("rate limit = %v %s", rec.Header(), rec.Body.String())
	}
}

func TestV1WaitingApprovalAndDisconnect(t *testing.T) {
	h := newHarness(t).setup()
	sr := h.useScriptRunner(func(core.RunRequest, core.RunID) []core.Event {
		return []core.Event{
			{Type: core.EvRunDelta, Data: core.DeltaData{Text: "Working"}},
			{Type: core.EvRunStatus, Data: core.RunStatusData{Status: "waiting_approval"}},
			{Type: core.EvRunFinished, Data: core.RunFinishedData{Status: core.RunDone}},
		}
	})
	sr.block = make(chan struct{})
	defer close(sr.block)
	tok := h.tokenFor(h.member, "chat")
	srv := h.server(t)
	ctx, cancel := context.WithCancel(context.Background())
	body := `{"model":"bothy","stream":true,"messages":[{"role":"user","content":"rm -rf?"}]}`
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok.token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	sawComment := false
	for i := 0; i < 20 && !sawComment; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		sawComment = strings.HasPrefix(line, ": waiting-approval")
	}
	if !sawComment {
		t.Fatal("no waiting-approval comment")
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for len(sr.stopped()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(sr.stopped()) != 1 {
		t.Fatal("disconnect did not stop the run")
	}
}

func TestV1PassthroughToolRoundTrip(t *testing.T) {
	h := newHarness(t).setup()
	h.llm.Steps = []fakes.LLMStep{
		{ToolCalls: []core.ToolCall{{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Paris"}`)}}, Usage: core.Usage{Input: 20, Output: 5}},
		{Text: []string{"Sunny ", "in Paris."}, Usage: core.Usage{Input: 40, Output: 4}},
		{ToolCalls: []core.ToolCall{{ID: "call_2", Name: "get_weather", Args: json.RawMessage(`{"city":"Oslo"}`)}}, Usage: core.Usage{Input: 20, Output: 5}},
	}
	tok := h.tokenFor(h.member, "llm")
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "get_weather", "description": "Weather",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]string{"type": "string"}}}}}}
	first := map[string]any{"model": "fake/fake-model", "tools": tools, "temperature": 0.1, "stop": "END",
		"messages": []map[string]any{{"role": "system", "content": "Be terse."}, {"role": "user", "content": "Weather in Paris?"}}}
	rec := h.do("POST", "/v1/chat/completions", first, tok)
	expect(t, rec, http.StatusOK)
	resp := decodeAs[oaResponse](t, rec)
	ch := resp.Choices[0]
	if ch.FinishReason != "tool_calls" || len(ch.Message.ToolCalls) != 1 || ch.Message.ToolCalls[0].Function.Name != "get_weather" ||
		ch.Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` || ch.Message.ToolCalls[0].Type != "function" || ch.Message.Content != nil {
		t.Fatalf("first response = %+v", resp)
	}
	creq := h.llm.Recorded()[0]
	if creq.Model != "fake-model" || len(creq.Tools) != 1 || creq.Tools[0].Name != "get_weather" || creq.System[0].Text != "Be terse." ||
		*creq.Temperature != 0.1 || !slices.Equal(creq.Stop, []string{"END"}) {
		t.Fatalf("translated request = %+v", creq)
	}
	second := map[string]any{"model": "fake/fake-model", "tools": tools, "messages": []map[string]any{
		{"role": "user", "content": "Weather in Paris?"},
		{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "call_1", "type": "function",
			"function": map[string]string{"name": "get_weather", "arguments": `{"city":"Paris"}`}}}},
		{"role": "tool", "tool_call_id": "call_1", "content": `{"temp":24}`},
	}}
	rec = h.do("POST", "/v1/chat/completions", second, tok)
	expect(t, rec, http.StatusOK)
	resp = decodeAs[oaResponse](t, rec)
	if *resp.Choices[0].Message.Content != "Sunny in Paris." || resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("second response = %+v", resp)
	}
	creq = h.llm.Recorded()[1]
	toolMsg := creq.Messages[2]
	if toolMsg.Role != core.MsgTool || toolMsg.Parts[0].ToolResult.CallID != "call_1" || toolMsg.Parts[0].ToolResult.Name != "get_weather" ||
		creq.Messages[1].Parts[0].ToolCall.Name != "get_weather" {
		t.Fatalf("tool round trip = %+v", creq.Messages)
	}
	// Streaming tool calls arrive as indexed deltas.
	stream := map[string]any{"model": "fake/fake-model", "tools": tools, "stream": true, "stream_options": map[string]bool{"include_usage": true},
		"messages": []map[string]any{{"role": "user", "content": "Oslo?"}}}
	rec = h.do("POST", "/v1/chat/completions", stream, tok)
	expect(t, rec, http.StatusOK)
	chunks, _ := readStream(t, rec.Body)
	var name, args, finish string
	for _, c := range chunks {
		for _, choice := range c.Choices {
			for _, tc := range choice.Delta.ToolCalls {
				if tc.Index == nil || *tc.Index != 0 {
					t.Fatalf("tool call index = %v", tc.Index)
				}
				name += tc.Function.Name
				args += tc.Function.Arguments
			}
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
		}
	}
	if name != "get_weather" || args != `{"city":"Oslo"}` || finish != "tool_calls" || chunks[len(chunks)-1].Usage == nil {
		t.Fatalf("stream tool call = %q %q %q", name, args, finish)
	}
	if len(h.budget.Calls) != 3 || h.budget.Calls[0].Purpose != "passthrough" {
		t.Fatalf("metering = %+v", h.budget.Calls)
	}
	n := 0
	for _, e := range h.audit.All() {
		if e.Action == "api.passthrough" {
			n++
			if strings.Contains(string(e.Detail), "Paris") {
				t.Fatalf("audit holds content: %s", e.Detail)
			}
		}
	}
	if n != 3 {
		t.Fatalf("api.passthrough audited %d times", n)
	}
}

func TestV1PassthroughGuards(t *testing.T) {
	h := newHarness(t).setup()
	tok := h.tokenFor(h.member, "llm")
	msgs := []map[string]any{{"role": "user", "content": "hi"}}
	h.budget.Deny = &core.BudgetError{Kind: core.BudgetDailyCost}
	rec := h.do("POST", "/v1/chat/completions", map[string]any{"model": "fake/fake-model", "messages": msgs}, tok)
	expect(t, rec, http.StatusTooManyRequests)
	if decodeAs[oaError](t, rec).Error.Type != "insufficient_quota" {
		t.Fatal(rec.Body.String())
	}
	h.budget.Deny = nil
	h.cfg.update(func(c *configT) { c.API.Passthrough = false })
	expect(t, h.do("POST", "/v1/chat/completions", map[string]any{"model": "fake/fake-model", "messages": msgs}, tok), http.StatusNotFound)
	h.cfg.update(func(c *configT) { c.API.Passthrough = true })
	rec = h.do("POST", "/v1/chat/completions", map[string]any{"model": "fake/fake-model", "messages": msgs, "tool_choice": "required"}, tok)
	expect(t, rec, http.StatusBadRequest)
	h.llm.Steps = []fakes.LLMStep{{Err: &core.ProviderError{Provider: "fake", Kind: core.ErrKindServer, Msg: "boom"}}}
	rec = h.do("POST", "/v1/chat/completions", map[string]any{"model": "fake/fake-model", "messages": msgs}, tok)
	expect(t, rec, http.StatusBadGateway)
	if decodeAs[oaError](t, rec).Error.Code != "upstream_error" {
		t.Fatal(rec.Body.String())
	}
}
