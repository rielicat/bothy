package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

func stream(t *testing.T, fixture string, req *core.Request) (*core.Response, *wiretest.Events, *wiretest.Server, error) {
	t.Helper()
	srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/"+fixture)))
	c := New(wiretest.Options(srv, "anthropic"))
	if req == nil {
		req = &core.Request{Model: "claude-sonnet-4-6", MaxTokens: 1024, Messages: []core.Message{userText("Hello")}}
	}
	var ev wiretest.Events
	resp, err := c.Stream(context.Background(), req, ev.On)
	return resp, &ev, srv, err
}

func userText(s string) core.Message {
	return core.Message{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: s}}}
}

func TestStreamText(t *testing.T) {
	for _, tc := range []struct {
		fixture, text, model string
		usage                core.Usage
	}{
		{"sdk_basic.sse", "Hello there!", "claude-3-opus-latest", core.Usage{Input: 11, Output: 6}},
		{"docs_basic.sse", "Hello!", "claude-opus-5-5", core.Usage{Input: 25, Output: 15}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			resp, ev, _, err := stream(t, tc.fixture, nil)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Text() != tc.text || resp.Message.PlainText() != tc.text {
				t.Fatalf("text %q / %q, want %q", ev.Text(), resp.Message.PlainText(), tc.text)
			}
			if resp.Usage != tc.usage || resp.UsageEstimated {
				t.Fatalf("usage %+v estimated=%v, want %+v", resp.Usage, resp.UsageEstimated, tc.usage)
			}
			if resp.Stop != core.StopEnd || resp.Model != tc.model {
				t.Fatalf("stop %q model %q", resp.Stop, resp.Model)
			}
			last := ev.All[len(ev.All)-1]
			if last.Type != core.StreamUsage || *last.Usage != tc.usage {
				t.Fatalf("last event %+v, want usage", last)
			}
		})
	}
}

func TestStreamToolUse(t *testing.T) {
	for _, tc := range []struct {
		fixture, id, args, text string
	}{
		{"sdk_tool_use.sse", "toolu_01NRLabsLyVHZPKxbKvkfSMn", `{"location":"Paris"}`, "I'll check the current weather in Paris for you."},
		{"docs_tool_use.sse", "toolu_01T1x1fJ34qAmk2tNTrN7Up6", `{"location":"San Francisco, CA"}`, "Okay, let's check the weather for San Francisco, CA:"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			resp, ev, _, err := stream(t, tc.fixture, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := ev.Done()
			if len(done) != 1 || done[0].ID != tc.id || done[0].Name != "get_weather" || string(done[0].Args) != tc.args {
				t.Fatalf("calls %+v", done)
			}
			if resp.Stop != core.StopTool || resp.Message.PlainText() != tc.text {
				t.Fatalf("stop %q text %q", resp.Stop, resp.Message.PlainText())
			}
			parts := resp.Message.Parts
			if len(parts) != 2 || parts[0].Type != core.PartText || parts[1].Type != core.PartToolCall {
				t.Fatalf("parts %+v", parts)
			}
			var starts int
			for _, e := range ev.All {
				if e.Type == core.StreamToolCallStart {
					starts++
					if e.Call.ID != tc.id || e.Call.Name != "get_weather" || e.Call.Args != nil {
						t.Fatalf("start %+v", e.Call)
					}
				}
			}
			if starts != 1 {
				t.Fatalf("%d starts", starts)
			}
		})
	}
}

func TestStreamParallelToolUse(t *testing.T) {
	resp, ev, _, err := stream(t, "composed_parallel_tool_use.sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 2 || done[0].ID != "toolu_01T1x1fJ34qAmk2tNTrN7Up6" || done[1].ID != "toolu_01NRLabsLyVHZPKxbKvkfSMn" {
		t.Fatalf("calls %+v", done)
	}
	if string(done[1].Args) != `{"location":"Paris"}` {
		t.Fatalf("second args %s", done[1].Args)
	}
	if n := len(resp.Message.Parts); n != 3 || resp.Stop != core.StopTool {
		t.Fatalf("parts %d stop %q", n, resp.Stop)
	}
}

func TestStreamThinkingSignature(t *testing.T) {
	resp, ev, _, err := stream(t, "docs_thinking.sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantThinking := "I need to find the GCD of 1071 and 462 using the Euclidean algorithm.\n\n1071 = 2 × 462 + 147\n462 = 3 × 147 + 21\n147 = 7 × 21 + 0\nThe remainder is 0, so GCD(1071, 462) = 21."
	if ev.Thinking() != wantThinking {
		t.Fatalf("thinking %q", ev.Thinking())
	}
	p := resp.Message.Parts
	if len(p) != 2 || p[0].Type != core.PartThinking || p[0].Text != wantThinking {
		t.Fatalf("parts %+v", p)
	}
	if p[0].Sig != "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds..." || p[0].SigKind != core.KindAnthropic {
		t.Fatalf("sig %q kind %q", p[0].Sig, p[0].SigKind)
	}
	if p[1].Text != "The greatest common divisor of 1071 and 462 is **21**." {
		t.Fatalf("text %q", p[1].Text)
	}
	// The documented trace carries no usage at all: the facade must estimate.
	if !resp.UsageEstimated {
		t.Fatal("usage should be marked estimated")
	}
	for _, e := range ev.All {
		if e.Type == core.StreamUsage {
			t.Fatal("no usage event expected without reported usage")
		}
	}

	// Round trip: the signed block goes back verbatim on the next request.
	req := buildRequest(&core.Request{Model: "m", Messages: []core.Message{userText("q"), resp.Message, userText("next")}})
	b, _ := json.Marshal(req.Messages[1])
	want := `{"role":"assistant","content":[{"type":"thinking","thinking":"` + jsonEscape(wantThinking) + `","signature":"EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds..."},{"type":"text","text":"The greatest common divisor of 1071 and 462 is **21**."}]}`
	if string(b) != want {
		t.Fatalf("round trip\n got %s\nwant %s", b, want)
	}
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func TestStreamUsageWithCache(t *testing.T) {
	resp, _, _, err := stream(t, "sdk_usage_cache.sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	// message_delta's cumulative counts replace message_start's.
	want := core.Usage{Input: 40, Output: 8, CacheRead: 7, CacheWrite: 12, Reasoning: 3}
	if resp.Usage != want {
		t.Fatalf("usage %+v, want %+v", resp.Usage, want)
	}
}

func TestStreamRefusalAndTruncation(t *testing.T) {
	resp, _, _, err := stream(t, "sdk_refusal.sse", nil)
	if err != nil || resp.Stop != core.StopRefusal {
		t.Fatalf("refusal: %v %+v", err, resp)
	}
	resp, ev, _, err := stream(t, "sdk_incomplete_tool_json.sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != core.StopMaxTokens {
		t.Fatalf("stop %q", resp.Stop)
	}
	// The tool block never closed: the call must not reach the message.
	if len(ev.Done()) != 0 {
		t.Fatalf("truncated call finished: %+v", ev.Done())
	}
	for _, p := range resp.Message.Parts {
		if p.Type == core.PartToolCall {
			t.Fatalf("truncated call kept: %+v", p.ToolCall)
		}
	}
}

func TestStreamErrors(t *testing.T) {
	_, _, _, err := stream(t, "docs_error.sse", nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindOverloaded || pe.Msg != "Overloaded" || !pe.Retryable() {
		t.Fatalf("error event: %v", err)
	}
	_, ev, _, err := stream(t, "composed_midstream_error.sse", nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindOverloaded {
		t.Fatalf("midstream: %v", err)
	}
	if ev.Text() != "Hello" {
		t.Fatalf("delta before the error not forwarded: %q", ev.Text())
	}
	// A stream cut before message_stop is a network failure, not success.
	srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/docs_basic.sse")[:300]))
	_, err = New(wiretest.Options(srv, "anthropic")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindNetwork {
		t.Fatalf("truncated stream: %v", err)
	}
}

// TestErrorKinds uses the documented error shape
// (platform.claude.com/docs/en/api/errors) with each documented status/type.
func TestErrorKinds(t *testing.T) {
	documented := `{"type":"error","error":{"type":"not_found_error","message":"The requested resource could not be found."},"request_id":"req_011CSHoEeqs5C35K2UUqR7Fy"}`
	for _, tc := range []struct {
		status int
		typ    string
		msg    string
		want   core.ErrorKind
	}{
		{400, "invalid_request_error", "There was an issue with the format or content of your request.", core.ErrKindInvalidRequest},
		{400, "invalid_request_error", "prompt is too long: 208310 tokens > 200000 maximum", core.ErrKindContextLength},
		{401, "authentication_error", "There's an issue with your API key.", core.ErrKindAuth},
		{402, "billing_error", "There's an issue with your billing or payment information.", core.ErrKindAuth},
		{403, "permission_error", "Your API key does not have permission to use the specified resource.", core.ErrKindAuth},
		{404, "not_found_error", "The requested resource could not be found.", core.ErrKindInvalidRequest},
		{413, "request_too_large", "Request exceeds the maximum allowed number of bytes.", core.ErrKindContextLength},
		{429, "rate_limit_error", "Your account has hit a rate limit.", core.ErrKindRateLimit},
		{500, "api_error", "An unexpected error has occurred internal to Anthropic's systems.", core.ErrKindServer},
		{504, "timeout_error", "The request timed out while processing.", core.ErrKindServer},
		{529, "overloaded_error", "The API is temporarily overloaded.", core.ErrKindOverloaded},
	} {
		body := strings.Replace(strings.Replace(documented, "not_found_error", tc.typ, 1), "The requested resource could not be found.", tc.msg, 1)
		h := http.Header{}
		if tc.status == 429 {
			h.Set("Retry-After", "7")
		}
		srv := wiretest.NewServer(t, wiretest.Response{Status: tc.status, ContentType: "application/json", Header: h, Body: []byte(body)})
		_, err := New(wiretest.Options(srv, "anthropic")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) {
			t.Fatalf("%d %s: %v", tc.status, tc.typ, err)
		}
		if pe.Kind != tc.want || pe.Status != tc.status || pe.Msg != tc.msg || pe.Provider != "anthropic" {
			t.Errorf("%d %s: got %+v, want kind %s", tc.status, tc.typ, pe, tc.want)
		}
		if tc.status == 429 && pe.RetryAfter.Seconds() != 7 {
			t.Errorf("retry-after %v", pe.RetryAfter)
		}
	}
}

func TestRequestShape(t *testing.T) {
	srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/sdk_basic.sse")))
	c := New(wiretest.Options(srv, "anthropic"))
	temp := 0.2
	req := &core.Request{
		Model:       "claude-sonnet-4-6",
		MaxTokens:   2048,
		System:      []core.SystemBlock{{Text: "S1"}, {Text: "S2", CacheBreak: true}},
		Tools:       []core.ToolDef{{Name: "a", Description: "A", Schema: json.RawMessage(`{"type":"object"}`)}, {Name: "b", Schema: json.RawMessage(`{"type":"object"}`)}},
		ToolsCache:  true,
		CacheTail:   true,
		Stop:        []string{"END"},
		Temperature: &temp,
		Messages: []core.Message{
			userText("first"),
			{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartContext, Text: "<turn-context/>"}}},
			{Role: core.MsgAssistant, Parts: []core.Part{
				{Type: core.PartThinking, Text: "foreign", Sig: "gem-sig", SigKind: core.KindGemini},
				{Type: core.PartText, Text: "calling"},
				{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "toolu_1", Name: "a", Args: json.RawMessage(`{"x":1}`)}},
			}},
			{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "toolu_1", Name: "a", Content: "ok"}}}},
			{Role: core.MsgUser, Kind: core.KindSteer, Parts: []core.Part{{Type: core.PartText, Text: "also this"}}},
		},
	}
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	r := srv.Requests()[0]
	if r.URL != "/messages" || r.Header.Get("x-api-key") != wiretest.Key || r.Header.Get("anthropic-version") != apiVersion {
		t.Fatalf("request %s %v", r.URL, r.Header)
	}
	if strings.Contains(r.URL, wiretest.Key) || r.Header.Get("Authorization") != "" {
		t.Fatal("key outside x-api-key")
	}
	want := `{"model":"claude-sonnet-4-6","max_tokens":2048,` +
		`"system":[{"type":"text","text":"S1"},{"type":"text","text":"S2","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"<turn-context/>"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"calling"},{"type":"tool_use","id":"toolu_1","name":"a","input":{"x":1}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"},{"type":"text","text":"also this","cache_control":{"type":"ephemeral"}}]}],` +
		`"tools":[{"name":"a","description":"A","input_schema":{"type":"object"}},{"name":"b","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],` +
		`"stop_sequences":["END"],"temperature":0.2,"stream":true}`
	if string(r.Body) != want {
		t.Fatalf("body\n got %s\nwant %s", r.Body, want)
	}
}

func TestBreakpointLimit(t *testing.T) {
	var sys []core.SystemBlock
	for i := range 5 {
		sys = append(sys, core.SystemBlock{Text: fmt.Sprint(i), CacheBreak: true})
	}
	r := buildRequest(&core.Request{Model: "m", System: sys, Tools: []core.ToolDef{{Name: "t"}}, ToolsCache: true, CacheTail: true, Messages: []core.Message{userText("x")}})
	n := 0
	for _, s := range r.System {
		if s.CacheControl != nil {
			n++
		}
	}
	if n != 2 || r.System[4].CacheControl == nil || r.Tools[0].CacheControl == nil || r.Messages[0].Content[0].CacheControl == nil {
		t.Fatalf("breakpoints: %d system, tools %v", n, r.Tools[0].CacheControl)
	}
}

func TestDiscover(t *testing.T) {
	srv := wiretest.NewServer(t, wiretest.JSON(200, []byte(`{"data":[{"type":"model","id":"claude-sonnet-4-6","display_name":"Claude Sonnet 4.6","created_at":"2026-02-17T00:00:00Z"}],"has_more":false}`)))
	o := wiretest.Options(srv, "anthropic")
	o.Discover = true
	o.Models = []core.ModelInfo{{Ref: core.ModelRef{Provider: "anthropic", Model: "claude-haiku-4-5"}}}
	ms, err := New(o).Models(context.Background())
	if err != nil || len(ms) != 2 || ms[1].Ref.Model != "claude-sonnet-4-6" || ms[1].Display != "Claude Sonnet 4.6" {
		t.Fatalf("%v %+v", err, ms)
	}
	if r := srv.Requests()[0]; r.URL != "/models?limit=1000" || r.Header.Get("x-api-key") != wiretest.Key {
		t.Fatalf("request %+v", r)
	}
}
