package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

func toJSON(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(buf.String())
}

func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}

// The ConverseStream API reference's sample request, built from the same
// conversation.
func TestBuildRequestMatchesDocs(t *testing.T) {
	temp := 0.5
	req := &core.Request{Model: "anthropic.claude-3-sonnet-20240229-v1:0", MaxTokens: 1000, Temperature: &temp,
		System:   []core.SystemBlock{{Text: "You are an economist with access to lots of data"}},
		Messages: []core.Message{userText("Write an article about impact of high inflation to GDP of a country")}}
	srv := wiretest.NewServer(t, eventStream(encodeFrames(t, readFrames(t, "live_text.jsonl"))))
	c, _ := testClient(srv)
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	r := srv.Requests()[0]
	if r.URL != "/model/anthropic.claude-3-sonnet-20240229-v1%3A0/converse-stream" {
		t.Fatalf("path %s", r.URL)
	}
	if !sameJSON(t, r.Body, wiretest.File(t, "testdata/docs_converse_stream_request.json")) {
		t.Fatalf("body %s differs from the documented request", r.Body)
	}
}

func TestBuildRequestCachePoints(t *testing.T) {
	tools := []core.ToolDef{{Name: "a", Schema: json.RawMessage(`{"type":"object"}`)}, {Name: "b"}}
	base := core.Request{
		System:     []core.SystemBlock{{Text: "constitution", CacheBreak: true}, {Text: "persona", CacheBreak: true}, {Text: "  "}, {Text: "turn"}},
		Tools:      tools,
		ToolsCache: true,
		CacheTail:  true,
		Messages:   []core.Message{userText("hi")},
	}
	claude := base
	claude.Model = "global.anthropic.claude-opus-4-5-20251101-v1:0"
	got := toJSON(t, buildRequest(&claude))
	want := `{"messages":[{"role":"user","content":[{"text":"hi"},{"cachePoint":{"type":"default"}}]}],` +
		`"system":[{"text":"constitution"},{"cachePoint":{"type":"default"}},{"text":"persona"},{"cachePoint":{"type":"default"}},{"text":"turn"}],` +
		`"toolConfig":{"tools":[{"toolSpec":{"name":"a","inputSchema":{"json":{"type":"object"}}}},{"toolSpec":{"name":"b","inputSchema":{"json":{"type":"object"}}}},{"cachePoint":{"type":"default"}}]}}`
	if got != want {
		t.Fatalf("claude\n got %s\nwant %s", got, want)
	}

	// Nova caches system and messages but not tools.
	nova := base
	nova.Model = "us.amazon.nova-pro-v1:0"
	r := buildRequest(&nova)
	if n := len(r.ToolConfig.Tools); n != 2 || r.ToolConfig.Tools[1].CachePoint != nil {
		t.Fatalf("nova tools %+v", r.ToolConfig.Tools)
	}
	if r.System[1].CachePoint == nil || r.Messages[0].Content[1].CachePoint == nil {
		t.Fatalf("nova system/messages cache points missing: %s", toJSON(t, r))
	}

	// In a tool loop Nova's tail point follows the latest text block: Nova
	// refuses one after a toolResult or toolUse ("extraneous key
	// [cachePoint] is not permitted"), Claude takes it at the very end.
	loop := base
	loop.Messages = []core.Message{userText("weather?"),
		{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "t1", Name: "a", Args: json.RawMessage(`{}`)}}}},
		{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "t1", Name: "a", Content: "rain"}}}}}
	loop.Model = "us.amazon.nova-lite-v1:0"
	got = toJSON(t, buildRequest(&loop).Messages)
	want = `[{"role":"user","content":[{"text":"weather?"},{"cachePoint":{"type":"default"}}]},` +
		`{"role":"assistant","content":[{"toolUse":{"toolUseId":"t1","name":"a","input":{}}}]},` +
		`{"role":"user","content":[{"toolResult":{"toolUseId":"t1","content":[{"text":"rain"}]}}]}]`
	if got != want {
		t.Fatalf("nova tool loop\n got %s\nwant %s", got, want)
	}
	loop.Model = claude.Model
	msgs := buildRequest(&loop).Messages
	if last := msgs[2].Content; len(last) != 2 || last[1].CachePoint == nil || len(msgs[0].Content) != 1 {
		t.Fatalf("claude tool loop: %s", toJSON(t, msgs))
	}
	// Nothing ends in text: no tail point rather than an invalid one.
	loop.Model = "us.amazon.nova-lite-v1:0"
	loop.Messages = loop.Messages[1:]
	if s := toJSON(t, buildRequest(&loop).Messages); strings.Contains(s, "cachePoint") {
		t.Fatalf("nova without a text block: %s", s)
	}

	// Models without prompt caching get no cache points at all.
	for _, m := range []string{"meta.llama3-3-70b-instruct-v1:0", "us.anthropic.claude-3-haiku-20240307-v1:0", "mistral.mistral-large-2407-v1:0"} {
		other := base
		other.Model = m
		if s := toJSON(t, buildRequest(&other)); strings.Contains(s, "cachePoint") {
			t.Errorf("%s: %s", m, s)
		}
	}

	// At most four: the earliest system ones go first.
	many := base
	many.Model = claude.Model
	many.System = []core.SystemBlock{{Text: "1", CacheBreak: true}, {Text: "2", CacheBreak: true}, {Text: "3", CacheBreak: true}, {Text: "4", CacheBreak: true}}
	r = buildRequest(&many)
	if s := toJSON(t, r.System); s != `[{"text":"1"},{"text":"2"},{"text":"3"},{"cachePoint":{"type":"default"}},{"text":"4"},{"cachePoint":{"type":"default"}}]` {
		t.Fatalf("system after limiting: %s", s)
	}
}

func TestBuildRequestToolChoice(t *testing.T) {
	tools := []core.ToolDef{{Name: "probe_echo", Schema: json.RawMessage(`{"type":"object"}`)}}
	call := core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "t1", Name: "probe_echo", Args: json.RawMessage(`{"value":"ok"}`)}}}}
	result := core.Message{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "t1", Name: "probe_echo", Content: "ok"}}}}
	for _, tc := range []struct {
		name, model, choice string
		history             []core.Message
		want                string // toolConfig JSON, "" for none
	}{
		{"required", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "required", nil,
			`{"tools":[{"toolSpec":{"name":"probe_echo","inputSchema":{"json":{"type":"object"}}}}],"toolChoice":{"any":{}}}`},
		{"required on a model without forced tool use", "global.anthropic.claude-opus-5-5", "required", nil,
			`{"tools":[{"toolSpec":{"name":"probe_echo","inputSchema":{"json":{"type":"object"}}}}]}`},
		{"auto", "us.amazon.nova-lite-v1:0", "auto", nil,
			`{"tools":[{"toolSpec":{"name":"probe_echo","inputSchema":{"json":{"type":"object"}}}}]}`},
		{"none without tool history", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "none", nil, ""},
		{"none with tool history drops the definitions", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "none", []core.Message{call, result}, ""},
		{"none on nova with tool history", "us.amazon.nova-lite-v1:0", "none", []core.Message{call, result}, ""},
	} {
		msgs := append([]core.Message{userText("go")}, tc.history...)
		r := buildRequest(&core.Request{Model: tc.model, Tools: tools, ToolChoice: tc.choice, Messages: msgs})
		got := ""
		if r.ToolConfig != nil {
			got = toJSON(t, r.ToolConfig)
		}
		if got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// A history with tool blocks but no tool definitions (compaction) turns
	// them into text: Converse refuses toolUse without toolConfig.
	r := buildRequest(&core.Request{Model: "us.anthropic.claude-haiku-4-5-20251001-v1:0", Messages: []core.Message{userText("go"), call, result}})
	got := toJSON(t, r.Messages)
	want := `[{"role":"user","content":[{"text":"go"}]},{"role":"assistant","content":[{"text":"[called tool probe_echo with {\"value\":\"ok\"}]"}]},` +
		`{"role":"user","content":[{"text":"[result of tool probe_echo]\nok"}]}]`
	if r.ToolConfig != nil || got != want {
		t.Fatalf("tool history as text\n got %s\nwant %s", got, want)
	}
	// ToolChoice none renders the same history the same way.
	r = buildRequest(&core.Request{Model: "us.anthropic.claude-haiku-4-5-20251001-v1:0", Tools: tools, ToolChoice: "none", Messages: []core.Message{userText("go"), call, result}})
	if got := toJSON(t, r.Messages); r.ToolConfig != nil || got != want {
		t.Fatalf("tool choice none\n got %s\nwant %s", got, want)
	}
}

func TestBuildMessages(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	msgs := []core.Message{
		{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "look"}, {Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Data: png, Size: int64(len(png)), Ref: "in/a.png"}}}},
		{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartContext, Text: "<turn-context/>"}, {Type: core.PartText, Text: "  "},
			{Type: core.PartImage, Blob: &core.Blob{MIME: "image/bmp", Data: png, Size: 4, Ref: "in/b.bmp", Name: "b.bmp"}},
			{Type: core.PartFile, Blob: &core.Blob{MIME: "application/pdf", Size: 9, Ref: "in/c.pdf", Name: "c.pdf"}}}},
		{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "calling"},
			{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "t1", Name: "shell"}},
			{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "t2", Name: "fs_read", Args: json.RawMessage(`{"path":"a"}`)}}}},
		{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "t1", Name: "shell", Content: "", IsError: true}}}},
		{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "t2", Name: "fs_read", Content: "data"}}}},
		{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "and?"}}},
	}
	got := toJSON(t, buildMessages(msgs, true, true))
	want := `[{"role":"user","content":[{"text":"look"},{"image":{"format":"png","source":{"bytes":"iVBORw0KGgpmYWtl"}}},` +
		`{"text":"<turn-context/>"},{"text":"[attached file b.bmp (image/bmp, 4 bytes) at in/b.bmp]"},{"text":"[attached file c.pdf (application/pdf, 9 bytes) at in/c.pdf]"}]},` +
		`{"role":"assistant","content":[{"text":"calling"},{"toolUse":{"toolUseId":"t1","name":"shell","input":{}}},{"toolUse":{"toolUseId":"t2","name":"fs_read","input":{"path":"a"}}}]},` +
		`{"role":"user","content":[{"toolResult":{"toolUseId":"t1","content":[{"text":"(no output)"}],"status":"error"}},{"toolResult":{"toolUseId":"t2","content":[{"text":"data"}]}},{"text":"and?"}]}]`
	if got != want {
		t.Fatalf("messages\n got %s\nwant %s", got, want)
	}
	// Images go only in user messages.
	asst := core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Data: png, Ref: "x.png", Size: 1}}}}
	if s := toJSON(t, buildMessages([]core.Message{asst}, true, true)); strings.Contains(s, `"image"`) {
		t.Fatalf("assistant image %s", s)
	}
}

func TestBuildRequestInference(t *testing.T) {
	r := buildRequest(&core.Request{Model: "m", Messages: []core.Message{userText("x")}})
	if r.InferenceConfig != nil {
		t.Fatalf("empty inference config sent: %+v", r.InferenceConfig)
	}
	r = buildRequest(&core.Request{Model: "m", MaxTokens: 77, Stop: []string{"END"}, Messages: []core.Message{userText("x")}})
	if s := toJSON(t, r.InferenceConfig); s != `{"maxTokens":77,"stopSequences":["END"]}` {
		t.Fatal(s)
	}
}

func TestCapabilities(t *testing.T) {
	for _, tc := range []struct {
		id, geo, base string
		cache, tools  bool
	}{
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us", "anthropic.claude-sonnet-4-5-20250929-v1:0", true, true},
		{"global.anthropic.claude-opus-5-5", "global", "anthropic.claude-opus-5-5", true, true},
		{"us-gov.anthropic.claude-sonnet-4-5-20250929-v1:0", "us-gov", "anthropic.claude-sonnet-4-5-20250929-v1:0", true, true},
		{"anthropic.claude-3-haiku-20240307-v1:0", "", "anthropic.claude-3-haiku-20240307-v1:0", false, false},
		{"apac.anthropic.claude-3-5-sonnet-20241022-v2:0", "apac", "anthropic.claude-3-5-sonnet-20241022-v2:0", false, false},
		{"us.amazon.nova-pro-v1:0", "us", "amazon.nova-pro-v1:0", true, false},
		{"global.amazon.nova-2-lite-v1:0", "global", "amazon.nova-2-lite-v1:0", true, false},
		{"amazon.titan-text-express-v1", "", "amazon.titan-text-express-v1", false, false},
		{"xx.meta.llama3-3-70b-instruct-v1:0", "xx", "meta.llama3-3-70b-instruct-v1:0", false, false},
		{"arn:aws:bedrock:us-west-2:111122223333:inference-profile/eu.anthropic.claude-haiku-4-5-20251001-v1:0", "eu", "anthropic.claude-haiku-4-5-20251001-v1:0", true, true},
		{"arn:aws:bedrock:us-west-2::foundation-model/amazon.nova-micro-v1:0", "", "amazon.nova-micro-v1:0", true, false},
		{"plain-name", "", "plain-name", false, false},
	} {
		geo, base := SplitModel(tc.id)
		if geo != tc.geo || base != tc.base || SupportsCache(tc.id) != tc.cache || supportsToolCache(tc.id) != tc.tools {
			t.Errorf("%s: geo %q base %q cache %v tools %v", tc.id, geo, base, SupportsCache(tc.id), supportsToolCache(tc.id))
		}
	}
	if !rejectsForcedTool("us.anthropic.claude-sonnet-5-5") || !rejectsForcedTool("anthropic.claude-fable-5-1") || rejectsForcedTool("global.anthropic.claude-opus-5") {
		t.Fatal("rejectsForcedTool")
	}
	if SupportsVision("us.amazon.nova-micro-v1:0") || !SupportsVision("us.amazon.nova-lite-v1:0") || !SupportsVision("anthropic.claude-haiku-5-5") {
		t.Fatal("SupportsVision")
	}
}
