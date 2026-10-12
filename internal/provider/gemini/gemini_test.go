package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
	"github.com/bothyhq/bothy/internal/provider/wire/wiretest"
)

func userText(s string) core.Message {
	return core.Message{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: s}}}
}

func stream(t *testing.T, fixture string) (*core.Response, *wiretest.Events, *wiretest.Server, error) {
	t.Helper()
	srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/"+fixture)))
	var ev wiretest.Events
	resp, err := New(wiretest.Options(srv, "gemini")).Stream(context.Background(),
		&core.Request{Model: "gemini-2.5-flash", MaxTokens: 512, Messages: []core.Message{userText("hi")}}, ev.On)
	return resp, &ev, srv, err
}

func TestStreamText(t *testing.T) {
	resp, ev, srv, err := stream(t, "js_text_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ev.Text(), "low!\n\nThe sky is blue primarily due to") || !strings.HasSuffix(ev.Text(), " rather than violet.") {
		t.Fatalf("text %q", ev.Text())
	}
	if resp.Message.PlainText() != ev.Text() || len(resp.Message.Parts) != 1 {
		t.Fatalf("parts %+v", resp.Message.Parts)
	}
	// Cumulative usage: the last chunk counts; thoughts are billed as output.
	if resp.Usage != (core.Usage{Input: 20, Output: 253 + 260, Reasoning: 260}) || resp.UsageEstimated {
		t.Fatalf("usage %+v", resp.Usage)
	}
	if resp.Stop != core.StopEnd || resp.Model != "gemini-2.5-flash" {
		t.Fatalf("stop %q model %q", resp.Stop, resp.Model)
	}
	r := srv.Requests()[0]
	if r.URL != "/models/gemini-2.5-flash:streamGenerateContent?alt=sse" {
		t.Fatalf("url %s", r.URL)
	}
	if r.Header.Get("x-goog-api-key") != wiretest.Key || strings.Contains(r.URL, "key=") || strings.Contains(r.URL, wiretest.Key) {
		t.Fatal("API key must travel in x-goog-api-key only")
	}
}

func TestStreamFunctionCallSignature(t *testing.T) {
	resp, ev, _, err := stream(t, "js_function_call_signature.sse")
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 1 || done[0].Name != "customDivide" || string(done[0].Args) != `{"denominator":2,"numerator":10}` || !wire.IsSynthesized(done[0].ID) {
		t.Fatalf("calls %+v", done)
	}
	p := resp.Message.Parts
	if len(p) != 1 || p[0].Type != core.PartToolCall || !strings.HasPrefix(p[0].Sig, "CiQBVKhc7gVNlWrmzW9MEk") || p[0].SigKind != core.KindGemini {
		t.Fatalf("parts %+v", p)
	}
	if resp.Stop != core.StopTool || resp.Usage != (core.Usage{Input: 81, Output: 199, Reasoning: 179}) {
		t.Fatalf("stop %q usage %+v", resp.Stop, resp.Usage)
	}

	// The signature goes back on the same functionCall part; the synthesized
	// id is not echoed.
	result := core.Message{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: done[0].ID, Name: "customDivide", Content: "5"}}}}
	req := buildRequest(&core.Request{Messages: []core.Message{userText("10/2"), resp.Message, result}})
	b, _ := json.Marshal(req.Contents[1:])
	want := `[{"role":"model","parts":[{"functionCall":{"name":"customDivide","args":{"denominator":2,"numerator":10}},"thoughtSignature":"` + p[0].Sig + `"}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"customDivide","response":{"output":"5"}}}]}]`
	if string(b) != want {
		t.Fatalf("round trip\n got %s\nwant %s", b, want)
	}
}

func TestStreamTextSignature(t *testing.T) {
	resp, _, _, err := stream(t, "js_text_signature.sse")
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Message.Parts
	if len(p) != 1 || p[0].Text != "The result is 2." || !strings.HasPrefix(p[0].Sig, "CiQBVKhc7mVR0ppW") || p[0].SigKind != core.KindGemini {
		t.Fatalf("parts %+v", p)
	}
}

func TestStreamParallelCalls(t *testing.T) {
	resp, ev, _, err := stream(t, "js_parallel_calls.sse")
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 2 || done[0].Name != "print" || string(done[0].Args) != `{"color":"blue","text":"hello"}` || done[1].Name != "beep" || string(done[1].Args) != `{}` {
		t.Fatalf("calls %+v", done)
	}
	if done[0].ID == done[1].ID {
		t.Fatal("parallel calls need distinct ids")
	}
	p := resp.Message.Parts
	// Only the first parallel call carries a signature.
	if len(p) != 2 || p[0].Sig == "" || p[1].Sig != "" {
		t.Fatalf("parts %+v", p)
	}
	if resp.Stop != core.StopTool {
		t.Fatalf("stop %q", resp.Stop)
	}
}

func TestStreamUsageWithCache(t *testing.T) {
	resp, _, _, err := stream(t, "js_usage_cache.sse")
	if err != nil {
		t.Fatal(err)
	}
	// promptTokenCount includes the cached tokens.
	if resp.Usage != (core.Usage{Input: 2, CacheRead: 3, Output: 238 + 41, Reasoning: 41}) {
		t.Fatalf("usage %+v", resp.Usage)
	}
	resp, _, _, err = stream(t, "js_function_call_stream.sse")
	if err != nil || resp.Usage != (core.Usage{Input: 21, Output: 6}) || resp.Model != "gemini-2.0-flash" {
		t.Fatalf("%v %+v", err, resp)
	}
}

func TestStreamMidstreamError(t *testing.T) {
	// A documented error body sent as an SSE event after a recorded chunk.
	first := strings.SplitN(string(wiretest.File(t, "testdata/js_text_stream.sse")), "\r\n\r\n", 2)[0]
	var doc bytes.Buffer
	if err := json.Compact(&doc, wiretest.File(t, "testdata/docs_error_api_key.json")); err != nil {
		t.Fatal(err)
	}
	body := first + "\r\n\r\ndata: " + doc.String() + "\r\n\r\n"
	srv := wiretest.NewServer(t, wiretest.SSE([]byte(body)))
	var ev wiretest.Events
	_, err := New(wiretest.Options(srv, "gemini")).Stream(context.Background(), &core.Request{Model: "m"}, ev.On)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindAuth || ev.Text() == "" {
		t.Fatalf("%v %q", err, ev.Text())
	}
	// A stream cut before finishReason is a network failure.
	cut := strings.SplitN(string(wiretest.File(t, "testdata/js_text_stream.sse")), "\r\n\r\n", 3)
	srv = wiretest.NewServer(t, wiretest.SSE([]byte(cut[0]+"\r\n\r\n"+cut[1]+"\r\n\r\n")))
	_, err = New(wiretest.Options(srv, "gemini")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindNetwork {
		t.Fatalf("cut stream: %v", err)
	}
}

// A blocked prompt arrives as a 200 chunk carrying only promptFeedback
// (GenerateContentResponse.promptFeedback.blockReason).
func TestPromptBlocked(t *testing.T) {
	srv := wiretest.NewServer(t, wiretest.SSE([]byte("data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\"},\"modelVersion\":\"gemini-2.5-flash\"}\r\n\r\n")))
	_, err := New(wiretest.Options(srv, "gemini")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindContentFilter || pe.Retryable() {
		t.Fatalf("%v", err)
	}
}

func TestErrorKinds(t *testing.T) {
	docBody := string(wiretest.File(t, "testdata/docs_error_api_key.json"))
	generic := func(code int, status, msg string) string {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": msg, "status": status}})
		return string(b)
	}
	for _, tc := range []struct {
		status int
		body   string
		want   core.ErrorKind
	}{
		{400, docBody, core.ErrKindAuth},
		{404, string(wiretest.File(t, "testdata/js_error_404.json")), core.ErrKindInvalidRequest},
		// The statuses and descriptions of the documented error table.
		{400, generic(400, "INVALID_ARGUMENT", "The request body is malformed."), core.ErrKindInvalidRequest},
		{400, generic(400, "FAILED_PRECONDITION", "Gemini API free tier is not available in your country."), core.ErrKindInvalidRequest},
		{400, generic(400, "INVALID_ARGUMENT", "The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."), core.ErrKindContextLength},
		{402, generic(402, "RESOURCE_EXHAUSTED", "Your Prepay credit balance is depleted."), core.ErrKindAuth},
		{403, generic(403, "PERMISSION_DENIED", "Your API key doesn't have the required permissions."), core.ErrKindAuth},
		{429, generic(429, "RESOURCE_EXHAUSTED", "You've exceeded the rate limit."), core.ErrKindRateLimit},
		{500, generic(500, "INTERNAL", "An unexpected error occurred on Google's side."), core.ErrKindServer},
		{503, generic(503, "UNAVAILABLE", "The service may be temporarily overloaded or down."), core.ErrKindOverloaded},
		{504, generic(504, "DEADLINE_EXCEEDED", "The service is unable to finish processing within the deadline."), core.ErrKindServer},
		{503, "[" + generic(503, "UNAVAILABLE", "overloaded") + "]", core.ErrKindOverloaded},
	} {
		srv := wiretest.NewServer(t, wiretest.JSON(tc.status, []byte(tc.body)))
		_, err := New(wiretest.Options(srv, "gemini")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != tc.want || pe.Status != tc.status {
			t.Errorf("%d %.80s: got %v, want %s", tc.status, tc.body, err, tc.want)
		}
	}
}

func TestRequestShape(t *testing.T) {
	srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/js_function_call_stream.sse")))
	temp := 0.5
	req := &core.Request{
		Model: "gemini-2.5-flash", MaxTokens: 100, Temperature: &temp, Stop: []string{"X"},
		System:     []core.SystemBlock{{Text: "S1"}, {Text: "S2"}},
		Tools:      []core.ToolDef{{Name: "f", Description: "F", Schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)}},
		ToolChoice: "auto",
		Messages: []core.Message{
			userText("q"),
			{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartImage, Blob: &core.Blob{MIME: "image/jpeg", Data: []byte{0xff}}}}},
			{Role: core.MsgAssistant, Parts: []core.Part{
				{Type: core.PartThinking, Text: "anthropic thought", Sig: "EqQ", SigKind: core.KindAnthropic},
				{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "fc-123", Name: "f", Args: json.RawMessage(`{"a":"b"}`)}, Sig: "foreign", SigKind: core.KindAnthropic},
			}},
			{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "fc-123", Name: "f", Content: "bad", IsError: true}}}},
		},
	}
	if _, err := New(wiretest.Options(srv, "gemini")).Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	want := `{"contents":[{"role":"user","parts":[{"text":"q"},{"inlineData":{"mimeType":"image/jpeg","data":"/w=="}}]},` +
		`{"role":"model","parts":[{"functionCall":{"id":"fc-123","name":"f","args":{"a":"b"}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"id":"fc-123","name":"f","response":{"error":"bad"}}}]}],` +
		`"systemInstruction":{"parts":[{"text":"S1\n\nS2"}]},` +
		`"tools":[{"functionDeclarations":[{"name":"f","description":"F","parametersJsonSchema":{"type":"object","properties":{"a":{"type":"string"}}}}]}],` +
		`"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},` +
		`"generationConfig":{"maxOutputTokens":100,"temperature":0.5,"stopSequences":["X"]}}`
	if got := string(srv.Requests()[0].Body); got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

func TestDiscover(t *testing.T) {
	srv := wiretest.NewServer(t, wiretest.JSON(200, []byte(`{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent","countTokens"],"thinking":true},{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}]}`)))
	ms, err := New(wiretest.Options(srv, "gemini")).Discover(context.Background())
	if err != nil || len(ms) != 1 || ms[0].Ref.Model != "gemini-2.5-flash" || ms[0].Context != 1048576 || ms[0].MaxOutput != 65536 || !ms[0].Thinking {
		t.Fatalf("%v %+v", err, ms)
	}
	if r := srv.Requests()[0]; r.Header.Get("x-goog-api-key") != wiretest.Key || strings.Contains(r.URL, "key=") {
		t.Fatal("discovery key placement")
	}
}
