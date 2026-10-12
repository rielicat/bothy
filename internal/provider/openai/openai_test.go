package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
	"github.com/bothyhq/bothy/internal/provider/wire/wiretest"
	"github.com/bothyhq/bothy/internal/testkit/mockllm"
)

func userText(s string) core.Message {
	return core.Message{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: s}}}
}

func stream(t *testing.T, fixture string, kind core.ProviderKind, req *core.Request) (*core.Response, *wiretest.Events, *wiretest.Server, error) {
	t.Helper()
	srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/"+fixture)))
	o := wiretest.Options(srv, "openai")
	o.Kind = kind
	if req == nil {
		req = &core.Request{Model: "gpt-4o-2024-08-06", MaxTokens: 256, Messages: []core.Message{userText("hi")}}
	}
	var ev wiretest.Events
	resp, err := New(o).Stream(context.Background(), req, ev.On)
	return resp, &ev, srv, err
}

func TestStreamText(t *testing.T) {
	resp, ev, _, err := stream(t, "sdk_text.sse", core.KindOpenAI, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "I'm unable to provide real-time weather updates. To get the current weather in San Francisco, I recommend checking a reliable weather website or a weather app."
	if ev.Text() != want || resp.Message.PlainText() != want {
		t.Fatalf("text %q", ev.Text())
	}
	if resp.Usage != (core.Usage{Input: 14, Output: 30}) || resp.UsageEstimated || resp.Stop != core.StopEnd || resp.Model != "gpt-4o-2024-08-06" {
		t.Fatalf("resp %+v", resp)
	}

	// The OpenAPI example includes an SSE comment line and no usage.
	resp, ev, _, err = stream(t, "docs_stream.sse", core.KindOpenAI, nil)
	if err != nil || ev.Text() != "Hello" || !resp.UsageEstimated || resp.Model != "gpt-6-astra" {
		t.Fatalf("docs stream: %v %q %+v", err, ev.Text(), resp)
	}
}

func TestStreamParallelTools(t *testing.T) {
	resp, ev, _, err := stream(t, "sdk_parallel_tools.sse", core.KindOpenAI, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 2 {
		t.Fatalf("calls %+v", done)
	}
	if done[0].ID != "call_JMW1whyEaYG438VE1OIflxA2" || done[0].Name != "GetWeatherArgs" || string(done[0].Args) != `{"city":"Edinburgh","country":"GB","units":"c"}` {
		t.Fatalf("first %+v", done[0])
	}
	if done[1].ID != "call_DNYTawLBoN8fj3KN6qU9N1Ou" || done[1].Name != "get_stock_price" || string(done[1].Args) != `{"ticker":"AAPL","exchange":"NASDAQ"}` {
		t.Fatalf("second %+v", done[1])
	}
	if resp.Stop != core.StopTool || resp.Usage != (core.Usage{Input: 149, Output: 60}) {
		t.Fatalf("stop %q usage %+v", resp.Stop, resp.Usage)
	}
	starts := 0
	for _, e := range ev.All {
		if e.Type == core.StreamToolCallStart {
			starts++
		}
	}
	if starts != 2 {
		t.Fatalf("%d starts", starts)
	}
}

func TestStreamMaxTokens(t *testing.T) {
	resp, _, _, err := stream(t, "sdk_max_tokens.sse", core.KindOpenAI, nil)
	if err != nil || resp.Stop != core.StopMaxTokens || resp.Usage.Output != 1 {
		t.Fatalf("%v %+v", err, resp)
	}
}

// The xAI usage example (docs.x.ai, prompt caching: usage & pricing) counts
// cached tokens inside prompt_tokens; core.Usage.Input must exclude them.
func TestUsageNormalization(t *testing.T) {
	doc := `{"prompt_tokens":125,"completion_tokens":48,"total_tokens":173,"prompt_tokens_details":{"text_tokens":125,"audio_tokens":0,"image_tokens":0,"cached_tokens":98},"completion_tokens_details":{"reasoning_tokens":0,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}`
	var u usageJSON
	if err := json.Unmarshal([]byte(doc), &u); err != nil {
		t.Fatal(err)
	}
	if got := u.normalize(); got != (core.Usage{Input: 27, Output: 48, CacheRead: 98}) {
		t.Fatalf("%+v", got)
	}
	var w usageJSON
	_ = json.Unmarshal([]byte(`{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"completion_tokens_details":{"reasoning_tokens":4}}`), &w)
	if got := w.normalize(); got != (core.Usage{Input: 50, Output: 10, CacheRead: 20, CacheWrite: 30, Reasoning: 4}) {
		t.Fatalf("%+v", got)
	}
}

// The chunks follow the xAI streaming example (docs.x.ai, streaming), whose
// <completion_id>/<creation_time> placeholders are filled in, plus the vLLM
// spelling "reasoning" of the same field.
func TestStreamReasoning(t *testing.T) {
	body := `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4.7","choices":[{"index":0,"delta":{"reasoning_content":"The","role":"assistant"}}],"usage":{"prompt_tokens":41,"completion_tokens":1,"total_tokens":42,"prompt_tokens_details":{"text_tokens":41,"audio_tokens":0,"image_tokens":0,"cached_tokens":0}},"system_fingerprint":"fp_xxxxxxxxxx"}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4.7","choices":[{"index":0,"delta":{"reasoning":" plan","role":"assistant"}}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4.7","choices":[{"index":0,"delta":{"content":"Ah","role":"assistant"}}],"usage":{"prompt_tokens":41,"completion_tokens":1,"total_tokens":42,"prompt_tokens_details":{"text_tokens":41,"audio_tokens":0,"image_tokens":0,"cached_tokens":0}},"system_fingerprint":"fp_xxxxxxxxxx"}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"grok-4.7","choices":[{"index":0,"delta":{"content":",","role":"assistant"}}],"usage":{"prompt_tokens":41,"completion_tokens":2,"total_tokens":43,"prompt_tokens_details":{"text_tokens":41,"audio_tokens":0,"image_tokens":0,"cached_tokens":0}},"system_fingerprint":"fp_xxxxxxxxxx"}

data: [DONE]
`
	srv := wiretest.NewServer(t, wiretest.SSE([]byte(body)))
	o := wiretest.Options(srv, "xai")
	o.Kind = core.KindOpenAICompat
	var ev wiretest.Events
	resp, err := New(o).Stream(context.Background(), &core.Request{Model: "grok-4.7"}, ev.On)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Thinking() != "The plan" || ev.Text() != "Ah," {
		t.Fatalf("thinking %q text %q", ev.Thinking(), ev.Text())
	}
	if p := resp.Message.Parts[0]; p.Type != core.PartThinking || p.Text != "The plan" || p.Sig != "" {
		t.Fatalf("thinking part %+v", p)
	}
	// Cumulative usage on every chunk: the last one wins.
	if resp.Usage != (core.Usage{Input: 41, Output: 2}) {
		t.Fatalf("usage %+v", resp.Usage)
	}
	// Reasoning is never sent back.
	req := New(o).buildRequest(&core.Request{Model: "m", Messages: []core.Message{userText("q"), resp.Message}})
	b, _ := json.Marshal(req.Messages)
	if strings.Contains(string(b), "plan") {
		t.Fatalf("reasoning echoed: %s", b)
	}
}

func mock(t *testing.T, rules []mockllm.Rule, models ...core.ModelInfo) (*mockllm.Server, *Client) {
	t.Helper()
	srv := mockllm.New(rules, models)
	t.Cleanup(srv.Close)
	return srv, New(wire.Options{ID: "mock", Kind: core.KindOpenAICompat, BaseURL: srv.URL(), HTTP: &http.Client{}})
}

func TestMockQuirks(t *testing.T) {
	// No usage reported: the facade estimates.
	_, c := mock(t, []mockllm.Rule{{Text: []string{"hel", "lo"}, NoUsage: true}})
	resp, err := c.Stream(context.Background(), &core.Request{Model: "mock"}, nil)
	if err != nil || resp.Message.PlainText() != "hello" || !resp.UsageEstimated {
		t.Fatalf("no usage: %v %+v", err, resp)
	}
	// No key configured: no Authorization header at all.
	srv, c := mock(t, []mockllm.Rule{{ToolCalls: []core.ToolCall{{Name: "a", Args: json.RawMessage(`{"k":"v"}`)}}}})
	var ev wiretest.Events
	resp, err = c.Stream(context.Background(), &core.Request{Model: "mock", Tools: []core.ToolDef{{Name: "a"}}}, ev.On)
	if err != nil || resp.Stop != core.StopTool || len(ev.Done()) != 1 || string(ev.Done()[0].Args) != `{"k":"v"}` {
		t.Fatalf("tool: %v %+v", err, ev.Done())
	}
	if h := srv.History()[0].Header.Get("Authorization"); h != "" {
		t.Fatalf("authorization %q", h)
	}
}

func TestMockFaults(t *testing.T) {
	for _, tc := range []struct {
		fault string
		kind  core.ErrorKind
	}{
		{mockllm.FaultRateLimit, core.ErrKindRateLimit},
		{mockllm.FaultServer, core.ErrKindServer},
		{mockllm.FaultOverflow, core.ErrKindContextLength},
		{mockllm.FaultMidstream, core.ErrKindNetwork},
		{mockllm.FaultMalformed, core.ErrKindServer},
	} {
		_, c := mock(t, []mockllm.Rule{{Text: []string{"a", "b", "c", "d"}, Fault: tc.fault}})
		_, err := c.Stream(context.Background(), &core.Request{Model: "mock"}, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != tc.kind {
			t.Errorf("%s: %v", tc.fault, err)
			continue
		}
		if tc.fault == mockllm.FaultRateLimit && pe.RetryAfter.Seconds() != 1 {
			t.Errorf("retry-after %v", pe.RetryAfter)
		}
	}
}

// The text form follows the Qwen function-calling docs
// (<tool_call>\n{"name": …, "arguments": {…}}\n</tool_call>), split across
// deltas, including inside the tags.
func TestToolCallTextExtraction(t *testing.T) {
	_, c := mock(t, []mockllm.Rule{{Text: []string{
		"Let me check.<tool", "_call>\n{\"name\": \"get_current_temperature\", \"argu",
		"ments\": {\"location\": \"San Francisco, CA, USA\"}}\n</tool_", "call> done <b>bold</b>",
	}}})
	var ev wiretest.Events
	resp, err := c.Stream(context.Background(), &core.Request{Model: "mock", Tools: []core.ToolDef{{Name: "get_current_temperature"}}}, ev.On)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Text() != "Let me check. done <b>bold</b>" {
		t.Fatalf("text %q", ev.Text())
	}
	done := ev.Done()
	if len(done) != 1 || done[0].Name != "get_current_temperature" || string(done[0].Args) != `{"location":"San Francisco, CA, USA"}` || !wire.IsSynthesized(done[0].ID) {
		t.Fatalf("calls %+v", done)
	}
	if resp.Stop != core.StopTool {
		t.Fatalf("stop %q", resp.Stop)
	}
	// Without tools in the request the text passes through untouched.
	_, c = mock(t, []mockllm.Rule{{Text: []string{"<tool_call>{\"name\":\"x\",\"arguments\":{}}</tool_call>"}}})
	ev = wiretest.Events{}
	if _, err := c.Stream(context.Background(), &core.Request{Model: "mock"}, ev.On); err != nil || len(ev.Done()) != 0 || !strings.HasPrefix(ev.Text(), "<tool_call>") {
		t.Fatalf("no tools: %v %q", err, ev.Text())
	}
	// An unparsable tag stays text.
	_, c = mock(t, []mockllm.Rule{{Text: []string{"a <tool_call>not json</tool_call> b"}}})
	ev = wiretest.Events{}
	if _, err := c.Stream(context.Background(), &core.Request{Model: "mock", Tools: []core.ToolDef{{Name: "x"}}}, ev.On); err != nil || ev.Text() != "a <tool_call>not json</tool_call> b" {
		t.Fatalf("bad tag: %v %q", err, ev.Text())
	}
}

func TestToolCallQuirks(t *testing.T) {
	// Missing ids, every call on index 0 told apart by id, whole arguments in
	// one delta and arguments sent as an object are all seen from compatible
	// servers; the chunk shape is the documented one.
	body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"a","arguments":"{\"x\":1}"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"id-b","function":{"name":"b","arguments":{"y":2}}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"id-c","function":{"name":"c","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	srv := wiretest.NewServer(t, wiretest.SSE([]byte(body)))
	o := wiretest.Options(srv, "compat")
	o.Kind = core.KindOpenAICompat
	var ev wiretest.Events
	if _, err := New(o).Stream(context.Background(), &core.Request{Model: "m"}, ev.On); err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 3 || !wire.IsSynthesized(done[0].ID) || string(done[0].Args) != `{"x":1}` ||
		done[1].ID != "id-b" || string(done[1].Args) != `{"y":2}` || done[2].ID != "id-c" || string(done[2].Args) != `{}` {
		t.Fatalf("calls %+v", done)
	}
}

func TestStreamErrorChunk(t *testing.T) {
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"par\"}}]}\n\ndata: {\"error\":{\"message\":\"The server had an error while processing your request.\",\"type\":\"server_error\",\"param\":null,\"code\":null}}\n\n"
	srv := wiretest.NewServer(t, wiretest.SSE([]byte(body)))
	var ev wiretest.Events
	_, err := New(wiretest.Options(srv, "openai")).Stream(context.Background(), &core.Request{Model: "m"}, ev.On)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer || ev.Text() != "par" {
		t.Fatalf("%v %q", err, ev.Text())
	}
}

// TestErrorKinds uses the documented error object
// {"error":{"message","type","param","code"}} with the codes from the OpenAI
// error-code guide, plus the looser bodies of compatible servers.
func TestErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   core.ErrorKind
	}{
		{401, `{"error":{"message":"Incorrect API key provided: sk-abc***xyz.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, core.ErrKindAuth},
		{403, `{"error":{"message":"Country, region, or territory not supported","type":"request_forbidden","param":null,"code":"unsupported_country_region_territory"}}`, core.ErrKindAuth},
		{429, `{"error":{"message":"Rate limit reached for requests","type":"requests","param":null,"code":"rate_limit_exceeded"}}`, core.ErrKindRateLimit},
		{429, `{"error":{"message":"Your request rate increased faster than the service can safely handle.","type":"rate_limit_error","param":null,"code":"slow_down"}}`, core.ErrKindRateLimit},
		{429, `{"error":{"message":"Rate limit reached for gpt-x on tokens per min (TPM): Limit 30000. Request too large: too many tokens.","type":"tokens","param":null,"code":"rate_limit_exceeded"}}`, core.ErrKindRateLimit},
		{400, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens. Please reduce the length of the messages.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`, core.ErrKindContextLength},
		{400, `{"error":{"message":"Invalid value for 'tool_choice'.","type":"invalid_request_error","param":"tool_choice","code":null}}`, core.ErrKindInvalidRequest},
		{400, `{"error":{"message":"Your request was rejected as a result of our safety system.","type":"invalid_request_error","param":null,"code":"content_policy_violation"}}`, core.ErrKindContentFilter},
		{500, `{"error":{"message":"The server had an error while processing your request.","type":"server_error","param":null,"code":null}}`, core.ErrKindServer},
		{503, `{"error":{"message":"The engine is currently overloaded, please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded"}}`, core.ErrKindOverloaded},
		{400, `{"object":"error","message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens.","type":"BadRequestError","param":null,"code":400}`, core.ErrKindContextLength},
		{404, `{"error":"model 'qwen3' not found"}`, core.ErrKindInvalidRequest},
		{422, `{"detail":"Input should be a valid list"}`, core.ErrKindInvalidRequest},
		{502, `<html>Bad gateway</html>`, core.ErrKindServer},
	} {
		srv := wiretest.NewServer(t, wiretest.JSON(tc.status, []byte(tc.body)))
		_, err := New(wiretest.Options(srv, "openai")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != tc.want || pe.Status != tc.status {
			t.Errorf("%d %s: got %v, want %s", tc.status, tc.body, err, tc.want)
		}
	}
}

func TestRequestShape(t *testing.T) {
	for _, kind := range []core.ProviderKind{core.KindOpenAI, core.KindOpenAICompat} {
		srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/sdk_text.sse")))
		o := wiretest.Options(srv, "p")
		o.Kind = kind
		req := &core.Request{
			Model: "gpt-x", MaxTokens: 100, Stop: []string{"END"},
			System: []core.SystemBlock{{Text: "S1"}, {Text: "S2", CacheBreak: true}},
			Tools:  []core.ToolDef{{Name: "a", Description: "A", Schema: json.RawMessage(`{"type":"object"}`)}},
			Messages: []core.Message{
				userText("one"),
				{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartContext, Text: "<turn-context/>"}, {Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Ref: "a.png", Data: []byte{1, 2}}}}},
				{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartThinking, Text: "hmm", SigKind: kind}, {Type: core.PartText, Text: "ok"}, {Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "c1", Name: "a", Args: json.RawMessage(`{"q":1}`)}}}},
				{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "c1", Content: "r"}}}},
			},
		}
		if _, err := New(o).Stream(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
		r := srv.Requests()[0]
		if r.URL != "/chat/completions" || r.Header.Get("Authorization") != "Bearer "+wiretest.Key {
			t.Fatalf("%s %v", r.URL, r.Header)
		}
		maxField := `"max_completion_tokens":100`
		if kind == core.KindOpenAICompat {
			maxField = `"max_tokens":100`
		}
		want := `{"model":"gpt-x","messages":[{"role":"system","content":"S1\n\nS2"},` +
			`{"role":"user","content":[{"type":"text","text":"one"},{"type":"text","text":"<turn-context/>"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQI="}}]},` +
			`{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"a","arguments":"{\"q\":1}"}}]},` +
			`{"role":"tool","content":"r","tool_call_id":"c1"}],` +
			`"tools":[{"type":"function","function":{"name":"a","description":"A","parameters":{"type":"object"}}}],"tool_choice":"auto",` +
			maxField + `,"stop":["END"],"stream":true,"stream_options":{"include_usage":true}}`
		if string(r.Body) != want {
			t.Fatalf("%s body\n got %s\nwant %s", kind, r.Body, want)
		}
	}
}

func TestDiscover(t *testing.T) {
	srv := wiretest.NewServer(t, wiretest.JSON(200, []byte(`{"object":"list","data":[{"id":"qwen/qwen3-32b","object":"model","context_length":40960},{"id":"local","object":"model","max_model_len":8192},{"id":"plain","object":"model"}]}`)))
	o := wiretest.Options(srv, "or")
	o.Kind = core.KindOpenAICompat
	ms, err := New(o).Discover(context.Background())
	if err != nil || len(ms) != 3 || ms[0].Context != 40960 || ms[1].Context != 8192 || ms[2].Context != 0 || ms[0].Ref.Provider != "or" {
		t.Fatalf("%v %+v", err, ms)
	}
}
