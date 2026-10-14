package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

const toolStream = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"gpt-6-sol","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"probe_echo","arguments":"{\"value\":\"ok\"}"}}]},"finish_reason":null}]}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"gpt-6-sol","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func toolReq() *core.Request {
	return &core.Request{Model: "gpt-6-sol", Tools: []core.ToolDef{{Name: "probe_echo"}}, Messages: []core.Message{userText("call it")}}
}

func effortOf(t *testing.T, body []byte) string {
	t.Helper()
	var r struct {
		ReasoningEffort *string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.ReasoningEffort == nil {
		return "<unset>"
	}
	return *r.ReasoningEffort
}

func refusal(t *testing.T) wiretest.Response {
	return wiretest.JSON(http.StatusBadRequest, wiretest.File(t, "testdata/cc_tools_reasoning_error.json"))
}

func TestChatRetriesToolsWithReasoningOffOnce(t *testing.T) {
	srv := wiretest.NewServer(t, refusal(t), wiretest.SSE([]byte(toolStream)))
	var learned []EffortEntry
	mem := NewEffortMemory(8, func(e EffortEntry) { learned = append(learned, e) })
	c := NewChat(wiretest.Options(srv, "openai"), mem)
	var ev wiretest.Events
	resp, err := c.Stream(context.Background(), toolReq(), ev.On)
	if err != nil || resp.Stop != core.StopTool || len(ev.Done()) != 1 {
		t.Fatalf("%v %+v", err, resp)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || effortOf(t, reqs[0].Body) != "<unset>" || effortOf(t, reqs[1].Body) != "none" {
		t.Fatalf("requests %d: %s / %s", len(reqs), reqs[0].Body, reqs[len(reqs)-1].Body)
	}
	if mem.Mode("openai", "gpt-6-sol") != EffortOff || len(learned) != 1 || learned[0] != (EffortEntry{"openai", "gpt-6-sol", EffortOff}) {
		t.Fatalf("memory %+v", mem.Entries())
	}

	// Later calls with tools send "none" at once; calls without tools keep
	// the model's reasoning.
	if _, err := c.Stream(context.Background(), toolReq(), nil); err != nil {
		t.Fatal(err)
	}
	plain := &core.Request{Model: "gpt-6-sol", Messages: []core.Message{userText("hi")}}
	if _, err := c.Stream(context.Background(), plain, nil); err != nil {
		t.Fatal(err)
	}
	reqs = srv.Requests()
	if len(reqs) != 4 || effortOf(t, reqs[2].Body) != "none" || effortOf(t, reqs[3].Body) != "<unset>" {
		t.Fatalf("requests %d: %s / %s", len(reqs), reqs[2].Body, reqs[3].Body)
	}
	if len(learned) != 1 {
		t.Fatalf("notified again: %+v", learned)
	}
}

// GPT-6 Astra rejects "none" too: one retry, then an error that says both.
func TestChatToolsRefusedEvenWithReasoningOff(t *testing.T) {
	none := wiretest.JSON(http.StatusBadRequest, []byte(`{"error":{"message":"Unsupported value: 'none' is not supported with the gpt-6-astra model.","type":"invalid_request_error","param":"reasoning_effort","code":"unsupported_value"}}`))
	srv := wiretest.NewServer(t, refusal(t), none, refusal(t))
	mem := NewEffortMemory(8, nil)
	c := NewChat(wiretest.Options(srv, "openai"), mem)
	_, err := c.Stream(context.Background(), toolReq(), nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest || pe.Status != http.StatusBadRequest ||
		!strings.HasPrefix(pe.Msg, "Function tools with reasoning_effort") ||
		!strings.Contains(pe.Msg, `Tools cannot be used over Chat Completions for this model: retrying with reasoning_effort "none" failed too: Unsupported value`) ||
		!errors.Is(err, core.ErrModelUnsupported) {
		t.Fatalf("%v", err)
	}
	if len(srv.Requests()) != 2 || mem.Mode("openai", "gpt-6-sol") != EffortNoTools {
		t.Fatalf("requests %d mode %d", len(srv.Requests()), mem.Mode("openai", "gpt-6-sol"))
	}
	// Later calls do not ask again.
	_, err = c.Stream(context.Background(), toolReq(), nil)
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, `an earlier retry with reasoning_effort "none" was refused too`) || len(srv.Requests()) != 3 ||
		!errors.Is(err, core.ErrModelUnsupported) {
		t.Fatalf("%v after %d requests", err, len(srv.Requests()))
	}
	// A refusal for reasoning alone (tools work with it off) is not the
	// model's inability.
	if _, err := NewChat(wiretest.Options(wiretest.NewServer(t, refusal(t), wiretest.JSON(http.StatusInternalServerError, []byte(`{"error":{"message":"boom","type":"server_error"}}`))), "openai"), nil).
		Stream(context.Background(), toolReq(), nil); err == nil || errors.Is(err, core.ErrModelUnsupported) {
		t.Fatalf("%v", err)
	}
}

func TestChatNoRetryForOtherFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		reply wiretest.Response
		req   *core.Request
	}{
		"unrelated 400":  {wiretest.JSON(http.StatusBadRequest, []byte(`{"error":{"message":"Invalid schema for function 'probe_echo'.","type":"invalid_request_error","param":"tools[0]","code":"invalid_function_parameters"}}`)), toolReq()},
		"no tools":       {refusal(t), &core.Request{Model: "gpt-6-sol", Messages: []core.Message{userText("hi")}}},
		"rate limit":     {wiretest.JSON(http.StatusTooManyRequests, []byte(`{"error":{"message":"Rate limit reached. Please try again in 1s.","type":"requests","param":null,"code":"rate_limit_exceeded"}}`)), toolReq()},
		"context length": {wiretest.JSON(http.StatusBadRequest, []byte(`{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`)), toolReq()},
	} {
		srv := wiretest.NewServer(t, tc.reply, wiretest.SSE([]byte(toolStream)))
		mem := NewEffortMemory(8, nil)
		if _, err := NewChat(wiretest.Options(srv, "openai"), mem).Stream(context.Background(), tc.req, nil); err == nil {
			t.Fatalf("%s: no error", name)
		}
		if n := len(srv.Requests()); n != 1 || len(mem.Entries()) != 0 {
			t.Fatalf("%s: %d requests, memory %+v", name, n, mem.Entries())
		}
	}
}

// A refusal that arrives in the stream after an event reached the caller
// is not retried: the facade never switches after the first event.
func TestChatNoRetryAfterFirstEvent(t *testing.T) {
	body := `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Let me"},"finish_reason":null}]}

data: {"error":{"message":"Function tools with reasoning_effort are not supported for m in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","type":"invalid_request_error","param":null,"code":null}}

`
	srv := wiretest.NewServer(t, wiretest.SSE([]byte(body)), wiretest.SSE([]byte(toolStream)))
	mem := NewEffortMemory(8, nil)
	var ev wiretest.Events
	_, err := NewChat(wiretest.Options(srv, "openai"), mem).Stream(context.Background(), toolReq(), ev.On)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest || ev.Text() != "Let me" {
		t.Fatalf("%v %q", err, ev.Text())
	}
	if len(srv.Requests()) != 1 || len(mem.Entries()) != 0 {
		t.Fatalf("retried after the first event: %d requests", len(srv.Requests()))
	}
}

func TestToolsNeedNoReasoningMatching(t *testing.T) {
	pe := func(kind core.ErrorKind, msg string) error {
		return &core.ProviderError{Provider: "p", Kind: kind, Status: 400, Msg: msg}
	}
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"Function tools with reasoning_effort are not supported for gpt-6-astra in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.", true},
		// Azure's documented wording for gpt-5.6-sol is the same sentence.
		{"Function tools with reasoning_effort are not supported for gpt-5.6-sol in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.", true},
		{`tool calls are unsupported unless reasoning effort is set to "none"`, true},
		{"Unsupported value: 'none' is not supported with the gpt-6-astra model.", false},
		{"reasoning_effort must be one of low, medium, high", false},
		{"Invalid schema for function 'x'", false},
	} {
		if got := toolsNeedNoReasoning(pe(core.ErrKindInvalidRequest, tc.msg)); got != tc.want {
			t.Errorf("%q: %v", tc.msg, got)
		}
	}
	if toolsNeedNoReasoning(pe(core.ErrKindRateLimit, "Function tools with reasoning_effort are not supported")) || toolsNeedNoReasoning(errors.New("x")) || toolsNeedNoReasoning(nil) {
		t.Fatal("matched a non-invalid_request error")
	}
	// Compatible servers that forward the restriction may use a looser body.
	kind, msg := classify(400, []byte(`{"error":"Function tools with reasoning_effort are not supported for openai/gpt-6-sol in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'."}`))
	if !toolsNeedNoReasoning(pe(kind, msg)) {
		t.Fatalf("loose body: %s %q", kind, msg)
	}
}

func TestEffortMemoryBounded(t *testing.T) {
	var notes int
	m := NewEffortMemory(2, func(EffortEntry) { notes++ })
	m.record("a", "m1", EffortOff)
	m.record("a", "m2", EffortOff)
	m.record("a", "m2", EffortOff)
	m.record("b", "m1", EffortNoTools)
	if m.Mode("a", "m1") != 0 || m.Mode("a", "m2") != EffortOff || m.Mode("b", "m1") != EffortNoTools || notes != 3 {
		t.Fatalf("entries %+v notes %d", m.Entries(), notes)
	}
	m.record("a", "m2", EffortNoTools)
	if got := m.Entries(); len(got) != 2 || got[0] != (EffortEntry{"a", "m2", EffortNoTools}) || got[1].Provider != "b" || notes != 4 {
		t.Fatalf("entries %+v notes %d", got, notes)
	}
	m.Forget("a")
	if got := m.Entries(); len(got) != 1 || got[0].Provider != "b" {
		t.Fatalf("after forget %+v", got)
	}
	var nilMem *EffortMemory
	nilMem.record("a", "m", EffortOff)
	if nilMem.Mode("a", "m") != 0 || nilMem.Entries() != nil {
		t.Fatal("nil memory")
	}
}

func TestResolveAPI(t *testing.T) {
	for _, tc := range []struct {
		kind      core.ProviderKind
		api, want string
	}{
		{core.KindOpenAI, "", APIResponses},
		{core.KindOpenAI, APIAuto, APIResponses},
		{core.KindOpenAI, APIChat, APIChat},
		{core.KindOpenAICompat, "", APIChat},
		{core.KindOpenAICompat, APIAuto, APIChat},
		{core.KindOpenAICompat, APIResponses, APIResponses},
	} {
		if got := ResolveAPI(tc.kind, tc.api); got != tc.want {
			t.Errorf("%s %q: %s", tc.kind, tc.api, got)
		}
	}
}
