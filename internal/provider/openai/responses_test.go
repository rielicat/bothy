package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

// event frames one JSON value (pretty-printed in some fixtures) as an SSE
// data line.
func event(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return "data: " + b.String() + "\n\n"
}

// lines frames each line of a JSON-lines fixture as an SSE event.
func lines(t *testing.T, file string) string {
	t.Helper()
	var out strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(wiretest.File(t, "testdata/"+file)))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out.WriteString(event(t, []byte(l)))
		}
	}
	return out.String()
}

// completed wraps a response object fixture in a response.completed event.
func completed(t *testing.T, response []byte) string {
	t.Helper()
	return event(t, []byte(`{"type":"response.completed","response":`+string(response)+`}`))
}

func respStream(t *testing.T, body string, req *core.Request) (*core.Response, *wiretest.Events, *wiretest.Server, error) {
	t.Helper()
	srv := wiretest.NewServer(t, wiretest.SSE([]byte(body)))
	if req == nil {
		req = &core.Request{Model: "gpt-6-astra", MaxTokens: 256, Messages: []core.Message{userText("hi")}}
	}
	var ev wiretest.Events
	resp, err := NewResponses(wiretest.Options(srv, "openai"), nil).Stream(context.Background(), req, ev.On)
	return resp, &ev, srv, err
}

func TestResponsesTextStream(t *testing.T) {
	resp, ev, srv, err := respStream(t, string(wiretest.File(t, "testdata/spec_stream.sse")), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The spec example omits the middle deltas; output_item.done is
	// authoritative for the stored message.
	if ev.Text() != "Hi" || resp.Message.PlainText() != "Hi there! How can I assist you today?" {
		t.Fatalf("streamed %q stored %q", ev.Text(), resp.Message.PlainText())
	}
	if resp.Usage != (core.Usage{Input: 37, Output: 11}) || resp.UsageEstimated || resp.Stop != core.StopEnd || resp.Model != "gpt-6-astra" {
		t.Fatalf("resp %+v", resp)
	}
	p := resp.Message.Parts[0]
	if p.Sig != `openai:{"id":"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654"}` || p.SigKind != SigResponses {
		t.Fatalf("text part %+v", p)
	}
	r := srv.Requests()[0]
	if r.URL != "/responses" || r.Header.Get("Authorization") != "Bearer "+wiretest.Key || r.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("request %s %v", r.URL, r.Header)
	}
	if last := ev.All[len(ev.All)-1]; last.Type != core.StreamUsage {
		t.Fatalf("last event %+v", last)
	}
}

func TestResponsesFunctionCallStream(t *testing.T) {
	// The docs example has no terminal event; the spec's Functions example
	// closes it (its own call is at the same output index, already done).
	body := lines(t, "docs_function_call_events.jsonl") + completed(t, wiretest.File(t, "testdata/spec_functions.json"))
	resp, ev, _, err := respStream(t, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 1 || done[0].ID != "call_1234xyz" || done[0].Name != "get_weather" || string(done[0].Args) != `{"location":"Paris, France"}` {
		t.Fatalf("calls %+v", done)
	}
	if ev.Types()[0] != core.StreamToolCallStart {
		t.Fatalf("events %v", ev.Types())
	}
	if resp.Stop != core.StopTool || resp.Usage != (core.Usage{Input: 291, Output: 23}) {
		t.Fatalf("resp %+v", resp)
	}
	if p := resp.Message.Parts[0]; p.Type != core.PartToolCall || p.Sig != "openai:fc_1234xyz" || p.SigKind != SigResponses {
		t.Fatalf("call part %+v", p)
	}
}

func TestResponsesParallelCallsFromTerminalResponse(t *testing.T) {
	out := wiretest.File(t, "testdata/docs_parallel_calls.json")
	usage := wiretest.File(t, "testdata/codex_usage.json")
	body := completed(t, []byte(`{"id":"resp_parallel","object":"response","status":"completed","model":"gpt-6-astra","output":`+string(out)+`,"usage":`+string(usage)+`}`))
	resp, ev, _, err := respStream(t, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 3 || done[0].ID != "call_12345xyz" || done[1].ID != "call_67890abc" || string(done[1].Args) != `{"location":"Bogotá, Colombia"}` ||
		done[2].Name != "send_email" {
		t.Fatalf("calls %+v", done)
	}
	if resp.Stop != core.StopTool || len(resp.Message.Parts) != 3 {
		t.Fatalf("resp %+v", resp)
	}
	// input_tokens counts cached and cache-write tokens; output_tokens
	// counts reasoning.
	if resp.Usage != (core.Usage{Input: 0, Output: 10, CacheRead: 40, CacheWrite: 60, Reasoning: 5}) {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestResponsesUsageAndCost(t *testing.T) {
	var u respUsage
	if err := json.Unmarshal(wiretest.File(t, "testdata/codex_usage.json"), &u); err != nil {
		t.Fatal(err)
	}
	got := u.normalize()
	if got != (core.Usage{Input: 0, Output: 10, CacheRead: 40, CacheWrite: 60, Reasoning: 5}) {
		t.Fatalf("%+v", got)
	}
	var spec struct {
		Usage respUsage `json:"usage"`
	}
	if err := json.Unmarshal(wiretest.File(t, "testdata/spec_reasoning.json"), &spec); err != nil {
		t.Fatal(err)
	}
	reasoning := spec.Usage.normalize()
	if reasoning != (core.Usage{Input: 81, Output: 1035, Reasoning: 832}) {
		t.Fatalf("%+v", reasoning)
	}
	// GPT-6 Astra: $10 in, $50 out, $1 cache read, $12.50 cache write per 1M.
	astra := core.Price{InPerM: 10_000_000, OutPerM: 50_000_000, CacheReadPerM: 1_000_000, CacheWritePerM: 12_500_000, Known: true}
	if c := astra.Cost(got); c != 500+40+750 {
		t.Fatalf("cost %d", c)
	}
	if c := astra.Cost(reasoning); c != 810+51750 {
		t.Fatalf("reasoning cost %d", c)
	}
}

func TestResponsesReasoningRoundTrip(t *testing.T) {
	resp, ev, _, err := respStream(t, string(wiretest.File(t, "testdata/cookbook_reasoning_tool.sse")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.Parts) != 2 || ev.Thinking() != "" {
		t.Fatalf("parts %+v", resp.Message.Parts)
	}
	th, call := resp.Message.Parts[0], resp.Message.Parts[1]
	if th.Type != core.PartThinking || th.SigKind != SigResponses || !strings.HasPrefix(th.Sig, `openai:{"type":"reasoning","id":"rs_6821243503d481919e1b385c2a154d5103d2cbc5a14f3696","summary":[],"encrypted_content":"gAAAAABoISQ24`) {
		t.Fatalf("thinking %+v", th)
	}
	if call.ToolCall.ID != "call_9ylqPOZUyFEwhxvBwgpNDqPT" || string(call.ToolCall.Args) != `{"latitude":48.8566,"longitude":2.3522}` {
		t.Fatalf("call %+v", call)
	}
	if resp.Usage != (core.Usage{Input: 81, Output: 1035, Reasoning: 832}) || resp.Stop != core.StopTool || resp.Model != "o3" {
		t.Fatalf("resp %+v", resp)
	}

	history := []core.Message{
		userText("What's the weather like in Paris today?"), resp.Message,
		{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: call.ToolCall.ID, Name: "get_weather", Content: "20"}}}},
	}
	c := NewResponses(respOpts("openai"), nil)
	b, _ := json.Marshal(c.buildRequest(&core.Request{Model: "o3", Messages: history}).Input)
	enc := strings.TrimPrefix(th.Sig, "openai:")
	want := `[{"role":"user","content":[{"type":"input_text","text":"What's the weather like in Paris today?"}]},` + enc +
		`,{"type":"function_call","id":"fc_68210c78357c8191977197499d5de6ca00c77cc15fd2f785","call_id":"call_9ylqPOZUyFEwhxvBwgpNDqPT","name":"get_weather","arguments":"{\"latitude\":48.8566,\"longitude\":2.3522}"}` +
		`,{"type":"function_call_output","call_id":"call_9ylqPOZUyFEwhxvBwgpNDqPT","output":"20"}]`
	if string(b) != want {
		t.Fatalf("input\n got %s\nwant %s", b, want)
	}

	// Another endpoint of the same API gets neither the reasoning nor the
	// item ids.
	b, _ = json.Marshal(NewResponses(respOpts("azure"), nil).buildRequest(&core.Request{Model: "o3", Messages: history}).Input)
	if strings.Contains(string(b), "reasoning") || strings.Contains(string(b), "fc_6821") {
		t.Fatalf("replayed to another provider: %s", b)
	}
}

func respOpts(id string) wire.Options { return wire.Options{ID: id, Kind: core.KindOpenAI} }

func TestResponsesReasoningSummary(t *testing.T) {
	const rs = "rs_6806bfca0b2481918a5748308061a2600d3ce51bdffd5476"
	summary := lines(t, "spec_summary_events.jsonl")
	var done struct {
		Text string `json:"text"`
	}
	sc := bufio.NewScanner(bytes.NewReader(wiretest.File(t, "testdata/spec_summary_events.jsonl")))
	for sc.Scan() {
		if strings.Contains(sc.Text(), "reasoning_summary_text.done") {
			_ = json.Unmarshal(sc.Bytes(), &done)
		}
	}
	text, _ := json.Marshal(done.Text)
	body := event(t, []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"`+rs+`","type":"reasoning","summary":[]}}`)) +
		summary +
		event(t, []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"`+rs+`","type":"reasoning","summary":[{"type":"summary_text","text":`+string(text)+`}],"encrypted_content":"enc"}}`)) +
		event(t, []byte(`{"type":"response.completed","response":{"status":"completed","usage":null}}`))
	resp, ev, _, err := respStream(t, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(done.Text, "**Responding to a greeting**") || ev.Thinking() != done.Text {
		t.Fatalf("thinking %q", ev.Thinking())
	}
	p := resp.Message.Parts[0]
	if p.Text != done.Text || p.Sig != `openai:{"type":"reasoning","id":"`+rs+`","summary":[{"type":"summary_text","text":`+string(text)+`}],"encrypted_content":"enc"}` {
		t.Fatalf("part %+v", p)
	}
	if !resp.UsageEstimated {
		t.Fatal("usage null must be estimated")
	}
}

// The openai-node fixture has raw reasoning text, no output_item.done (the
// terminal response lists the items) and a trailing [DONE].
func TestResponsesSDKReasoningStream(t *testing.T) {
	resp, ev, _, err := respStream(t, string(wiretest.File(t, "testdata/sdk_reasoning_stream.sse")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Thinking() != "Chain: Step 1. Step 2." || ev.Text() != "The answer is 42" {
		t.Fatalf("thinking %q text %q", ev.Thinking(), ev.Text())
	}
	th := resp.Message.Parts[0]
	if th.Type != core.PartThinking || th.Text != "Chain: Step 1. Step 2." || th.Sig != "" || th.SigKind != SigResponses {
		t.Fatalf("thinking %+v", th)
	}
	if resp.Message.PlainText() != "The answer is 42" || resp.Usage != (core.Usage{Output: 6, Reasoning: 2}) || resp.Model != "o3" {
		t.Fatalf("resp %+v", resp)
	}
	// Without encrypted content the reasoning cannot be replayed, and the
	// message after it goes back without its item id.
	b, _ := json.Marshal(NewResponses(respOpts("openai"), nil).buildRequest(&core.Request{Messages: []core.Message{resp.Message}}).Input)
	if string(b) != `[{"role":"assistant","content":"The answer is 42"}]` {
		t.Fatalf("replay %s", b)
	}
}

func TestResponsesIncomplete(t *testing.T) {
	spec := wiretest.File(t, "testdata/spec_stream.sse")
	var head strings.Builder
	for _, ev := range strings.Split(string(spec), "\n\n") {
		if strings.Contains(ev, "response.created") || strings.Contains(ev, "response.output_item.added") || strings.Contains(ev, "output_text.delta") {
			head.WriteString(ev + "\n\n")
		}
	}
	cut := event(t, []byte(`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"","status":"in_progress"}}`)) +
		event(t, []byte(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"argv\":[\"rm\",\"-rf\",\"/ho"}`))
	body := head.String() + cut + event(t, wiretest.File(t, "testdata/spec_incomplete.json"))
	resp, ev, _, err := respStream(t, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != core.StopMaxTokens || resp.Message.PlainText() != "Hi" || !resp.UsageEstimated {
		t.Fatalf("resp %+v", resp)
	}
	for _, p := range resp.Message.Parts {
		if p.Type == core.PartToolCall {
			t.Fatalf("cut-off call kept: %+v", p)
		}
	}
	if len(ev.Done()) != 0 {
		t.Fatalf("done events for a cut-off call: %+v", ev.Done())
	}

	filtered := strings.Replace(string(wiretest.File(t, "testdata/spec_incomplete.json")), `"max_output_tokens"`, `"content_filter"`, 1)
	resp, _, _, err = respStream(t, head.String()+event(t, []byte(filtered)), nil)
	if err != nil || resp.Stop != core.StopRefusal || resp.Message.PlainText() != "Hi" {
		t.Fatalf("content filter: %v %+v", err, resp)
	}
}

func TestResponsesFailures(t *testing.T) {
	sc := bufio.NewScanner(bytes.NewReader(wiretest.File(t, "testdata/codex_failed_events.jsonl")))
	sc.Buffer(nil, 1<<20)
	var kinds []core.ErrorKind
	var rate *core.ProviderError
	for sc.Scan() {
		_, _, _, err := respStream(t, event(t, sc.Bytes()), nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Status != http.StatusOK {
			t.Fatalf("%s: %v", sc.Text(), err)
		}
		kinds = append(kinds, pe.Kind)
		if pe.Kind == core.ErrKindRateLimit {
			rate = pe
		}
	}
	want := []core.ErrorKind{core.ErrKindRateLimit, core.ErrKindContextLength, core.ErrKindAuth, core.ErrKindContentFilter, core.ErrKindContentFilter}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v, want %v", kinds, want)
		}
	}
	if rate.RetryAfter != 11054*time.Millisecond || !strings.Contains(rate.Msg, "Rate limit reached") {
		t.Fatalf("rate limit %+v", rate)
	}

	for file, kind := range map[string]core.ErrorKind{"spec_failed.json": core.ErrKindServer, "spec_error.json": core.ErrKindInvalidRequest} {
		_, _, _, err := respStream(t, event(t, wiretest.File(t, "testdata/"+file)), nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != kind {
			t.Fatalf("%s: %v", file, err)
		}
	}
	// Some servers nest the error event's fields.
	_, _, _, err := respStream(t, `data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`+"\n\n", nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindContextLength {
		t.Fatalf("nested error: %v", err)
	}
	// A failure after text reached the caller is still an error; the facade
	// sees the started stream and does not retry.
	spec := string(wiretest.File(t, "testdata/spec_stream.sse"))
	partial := spec[:strings.Index(spec, ": Intermediate")]
	_, ev, _, err := respStream(t, partial+event(t, wiretest.File(t, "testdata/spec_failed.json")), nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer || ev.Text() != "Hi" {
		t.Fatalf("mid-stream failure: %v %q", err, ev.Text())
	}
}

func TestResponsesStreamEnds(t *testing.T) {
	spec := string(wiretest.File(t, "testdata/spec_stream.sse"))
	for name, body := range map[string]string{
		"empty":          "",
		"no terminal":    spec[:strings.Index(spec, "event: response.completed")],
		"done sentinel":  spec[:strings.Index(spec, "event: response.completed")] + "data: [DONE]\n\n",
		"malformed json": "data: {\"type\":\"response.created\"\n\n",
	} {
		_, _, _, err := respStream(t, body, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || (name == "malformed json") != (pe.Kind == core.ErrKindServer) || name != "malformed json" && pe.Kind != core.ErrKindNetwork {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestResponsesHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		kind   core.ErrorKind
	}{
		// The spec's components.responses examples.
		{429, `{"error":{"message":"Your request rate increased too quickly. Please reduce the request rate and gradually increase it again.","type":"rate_limit_error","param":null,"code":"slow_down"}}`, core.ErrKindRateLimit},
		{503, `{"error":{"message":"The model is temporarily overloaded. Please retry your request after a brief delay.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded"}}`, core.ErrKindOverloaded},
		// Billing codes from the error-codes guide; the type may stay insufficient_quota.
		{429, `{"error":{"message":"m","type":"insufficient_quota","param":null,"code":"credit_balance_exhausted"}}`, core.ErrKindAuth},
		{429, `{"error":{"message":"m","type":"requests","param":null,"code":"organization_spend_limit_exceeded"}}`, core.ErrKindAuth},
		{429, `{"error":{"message":"m","type":"requests","param":null,"code":"project_spend_limit_exceeded"}}`, core.ErrKindAuth},
		{429, `{"error":{"message":"m","type":"requests","param":null,"code":"organization_usage_limit_exceeded"}}`, core.ErrKindAuth},
	} {
		srv := wiretest.NewServer(t, wiretest.Response{Status: tc.status, ContentType: "application/json", Header: http.Header{"Retry-After": {"2"}}, Body: []byte(tc.body)})
		_, err := NewResponses(wiretest.Options(srv, "openai"), nil).Stream(context.Background(), &core.Request{Model: "m"}, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != tc.kind || pe.RetryAfter != 2*time.Second {
			t.Fatalf("%s: %v", tc.body, err)
		}
	}
}
