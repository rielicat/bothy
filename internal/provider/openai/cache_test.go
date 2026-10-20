package openai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

func TestExplicitCacheDetection(t *testing.T) {
	const or = "https://openrouter.ai/api/v1"
	cases := []struct {
		kind        core.ProviderKind
		base, model string
		want        bool
	}{
		{core.KindOpenAICompat, or, "anthropic/claude-sonnet-5.5", true},
		{core.KindOpenAICompat, "https://OpenRouter.ai/api/v1/", "Anthropic/claude-haiku-4.5", true},
		{core.KindOpenAICompat, or, "~anthropic/claude-sonnet-latest", true},
		{core.KindOpenAICompat, or, "openai/gpt-5", false},
		{core.KindOpenAICompat, "https://api.groq.com/openai/v1", "anthropic/claude-sonnet-5.5", false},
		{core.KindOpenAICompat, "https://openrouter.ai.example.com/v1", "anthropic/claude-sonnet-5.5", false},
		{core.KindOpenAI, or, "anthropic/claude-sonnet-5.5", false},
	}
	for _, tc := range cases {
		if got := ExplicitCache(tc.kind, tc.base, tc.model); got != tc.want {
			t.Errorf("ExplicitCache(%s, %s, %s) = %v", tc.kind, tc.base, tc.model, got)
		}
	}
}

// openRouterBody sends req through a client that takes the test server for
// OpenRouter and returns the request body it sent.
func openRouterBody(t *testing.T, req *core.Request) string {
	t.Helper()
	srv := wiretest.NewServer(t, wiretest.SSE(wiretest.File(t, "testdata/sdk_text.sse")))
	o := wiretest.Options(srv, "router")
	o.Kind = core.KindOpenAICompat
	c := New(o)
	c.openRouter = true
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	return string(srv.Requests()[0].Body)
}

func agentRequest(model string, msgs ...core.Message) *core.Request {
	return &core.Request{
		Model: model, MaxTokens: 100, CacheKey: "bothy-0123",
		System:     []core.SystemBlock{{Text: "S1"}, {Text: "S2", CacheBreak: true}},
		Tools:      []core.ToolDef{{Name: "a", Description: "A", Schema: json.RawMessage(`{"type":"object"}`)}},
		ToolsCache: true, CacheTail: true, ToolChoice: "auto",
		Messages: msgs,
	}
}

// TestOpenRouterClaudeBreakpoints: OpenRouter caches Claude only at
// cache_control markers, so the system block marked CacheBreak and the last
// message (here a tool result, which takes the marker inside a content part)
// carry one. Tools carry none: the system marker already covers them.
func TestOpenRouterClaudeBreakpoints(t *testing.T) {
	got := openRouterBody(t, agentRequest("anthropic/claude-sonnet-5.5",
		userText("one"),
		core.Message{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartContext, Text: "<turn-context/>"}}},
		core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "ok"}, {Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "c1", Name: "a", Args: json.RawMessage(`{}`)}}}},
		core.Message{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "c1", Content: "r"}}}},
	))
	want := `{"model":"anthropic/claude-sonnet-5.5","messages":[` +
		`{"role":"system","content":[{"type":"text","text":"S1"},{"type":"text","text":"S2","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":[{"type":"text","text":"one"},{"type":"text","text":"<turn-context/>"}]},` +
		`{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"a","arguments":"{}"}}]},` +
		`{"role":"tool","content":[{"type":"text","text":"r","cache_control":{"type":"ephemeral"}}],"tool_call_id":"c1"}],` +
		`"tools":[{"type":"function","function":{"name":"a","description":"A","parameters":{"type":"object"}}}],"tool_choice":"auto",` +
		`"max_tokens":100,"stream":true,"stream_options":{"include_usage":true},"session_id":"bothy-0123"}`
	if got != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
}

// TestOpenRouterOtherModelsUnmarked: other models on OpenRouter cache on
// their own and keep the plain shape; the session id still routes a
// session's calls to the provider that holds its cache.
func TestOpenRouterOtherModelsUnmarked(t *testing.T) {
	got := openRouterBody(t, agentRequest("openai/gpt-5", userText("one")))
	if strings.Contains(got, "cache_control") || !strings.Contains(got, `{"role":"system","content":"S1\n\nS2"},{"role":"user","content":"one"}`) ||
		!strings.Contains(got, `"session_id":"bothy-0123"`) {
		t.Fatalf("body %s", got)
	}
}

// TestOpenRouterClaudeAppendedTurnKeepsPrefix: a refine call appends one
// user message to the step's request. The step's blocks must reach the
// server unchanged (only the rolling marker moves to the new last block),
// or the call cannot read what the step wrote to the cache.
func TestOpenRouterClaudeAppendedTurnKeepsPrefix(t *testing.T) {
	step := agentRequest("anthropic/claude-sonnet-5.5", userText("question"))
	refine := agentRequest("anthropic/claude-sonnet-5.5", userText("question"), userText("[Bothy refine · critic]"))
	refine.ToolChoice = "none"
	type body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	var a, b body
	if err := json.Unmarshal([]byte(openRouterBody(t, step)), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(openRouterBody(t, refine)), &b); err != nil {
		t.Fatal(err)
	}
	stepUser, refineUser := a.Messages[1].Content, b.Messages[1].Content
	if len(stepUser) != 1 || stepUser[0]["text"] != "question" || stepUser[0]["cache_control"] == nil {
		t.Fatalf("step user %v", stepUser)
	}
	if len(refineUser) != 2 || refineUser[0]["text"] != "question" || refineUser[0]["cache_control"] != nil ||
		refineUser[1]["text"] != "[Bothy refine · critic]" || refineUser[1]["cache_control"] == nil {
		t.Fatalf("refine user %v", refineUser)
	}
}

// TestOpenRouterClaudeToolResultKeepsShape: the step after a tool result
// moves the rolling marker on; the result itself must reach the server in
// the shape it had when it carried the marker, a one-part list, or the
// next call's prefix would differ from what the previous one cached.
func TestOpenRouterClaudeToolResultKeepsShape(t *testing.T) {
	call := core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "c1", Name: "a", Args: json.RawMessage(`{}`)}}}}
	result := core.Message{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "c1", Content: "r"}}}}
	got := openRouterBody(t, agentRequest("anthropic/claude-sonnet-5.5", userText("one"), call, result,
		core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "done"}}}, userText("two")))
	for _, want := range []string{
		`{"role":"tool","content":[{"type":"text","text":"r"}],"tool_call_id":"c1"}`,
		`{"role":"user","content":[{"type":"text","text":"two","cache_control":{"type":"ephemeral"}}]}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in\n%s", want, got)
		}
	}
}

// TestOpenRouterClaudeBreakpointLimit: Anthropic takes at most four
// markers; the earliest system ones give way to the rolling tail, which
// moves past an empty tool result (a marker on empty text is refused).
func TestOpenRouterClaudeBreakpointLimit(t *testing.T) {
	req := agentRequest("anthropic/claude-sonnet-5.5",
		userText("one"),
		core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "c1", Name: "a"}}}},
		core.Message{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "c1"}}}},
	)
	req.System = nil
	for _, s := range []string{"A", "B", "C", "D", "E"} {
		req.System = append(req.System, core.SystemBlock{Text: s, CacheBreak: true})
	}
	got := openRouterBody(t, req)
	if n := strings.Count(got, "cache_control"); n != 4 {
		t.Fatalf("%d markers: %s", n, got)
	}
	for _, want := range []string{
		`{"type":"text","text":"A"},{"type":"text","text":"B"},{"type":"text","text":"C","cache_control":{"type":"ephemeral"}}`,
		`{"role":"user","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral"}}]}`,
		`{"role":"tool","content":"","tool_call_id":"c1"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in\n%s", want, got)
		}
	}
}

// TestOpenRouterCachedUsage: OpenRouter's last chunk reports cache reads
// and writes inside prompt_tokens; Input keeps only the uncached rest, so
// spend prices each part at its own rate.
func TestOpenRouterCachedUsage(t *testing.T) {
	body := `data: {"id":"gen-1","provider":"Anthropic","model":"anthropic/claude-sonnet-5.5","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null,"native_finish_reason":null,"logprobs":null}]}

data: {"id":"gen-1","provider":"Anthropic","model":"anthropic/claude-sonnet-5.5","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"stop","native_finish_reason":"end_turn","logprobs":null}]}

data: {"id":"gen-1","provider":"Anthropic","model":"anthropic/claude-sonnet-5.5","object":"chat.completion.chunk","created":1,"choices":[],"usage":{"prompt_tokens":12000,"completion_tokens":50,"total_tokens":12050,"cost":0.0061,"is_byok":false,"prompt_tokens_details":{"cached_tokens":10318,"cache_write_tokens":1500,"audio_tokens":0},"cost_details":{"upstream_inference_cost":null},"completion_tokens_details":{"reasoning_tokens":0,"image_tokens":0}}}

data: [DONE]

`
	srv := wiretest.NewServer(t, wiretest.SSE([]byte(body)))
	o := wiretest.Options(srv, "router")
	o.Kind = core.KindOpenAICompat
	var ev wiretest.Events
	resp, err := New(o).Stream(context.Background(), agentRequest("anthropic/claude-sonnet-5.5", userText("hi")), ev.On)
	if err != nil {
		t.Fatal(err)
	}
	if want := (core.Usage{Input: 182, CacheRead: 10318, CacheWrite: 1500, Output: 50}); resp.Usage != want || resp.UsageEstimated {
		t.Fatalf("usage %+v, want %+v", resp.Usage, want)
	}
}
