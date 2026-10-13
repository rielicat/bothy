package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

func ndjson(b []byte) wiretest.Response {
	return wiretest.Response{Status: 200, ContentType: "application/x-ndjson", Body: b}
}

func userText(s string) core.Message {
	return core.Message{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: s}}}
}

func TestStreamText(t *testing.T) {
	srv := wiretest.NewServer(t, ndjson(wiretest.File(t, "testdata/docs_chat_stream.ndjson")))
	var ev wiretest.Events
	resp, err := New(wiretest.Options(srv, "ollama")).Stream(context.Background(), &core.Request{Model: "llama3.2", NumCtx: 16384, Messages: []core.Message{userText("why is the sky blue?")}}, ev.On)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Text() != "The" || resp.Usage != (core.Usage{Input: 26, Output: 282}) || resp.UsageEstimated || resp.Stop != core.StopEnd || resp.Model != "llama3.2" {
		t.Fatalf("%q %+v", ev.Text(), resp)
	}
	var sent chatRequest
	if err := json.Unmarshal(srv.Requests()[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if srv.Requests()[0].URL != "/api/chat" || sent.Options == nil || sent.Options.NumCtx != 16384 || !sent.Stream {
		t.Fatalf("num_ctx not sent: %s", srv.Requests()[0].Body)
	}
}

func TestStreamTools(t *testing.T) {
	srv := wiretest.NewServer(t, ndjson(wiretest.File(t, "testdata/docs_tools_stream.ndjson")))
	var ev wiretest.Events
	resp, err := New(wiretest.Options(srv, "ollama")).Stream(context.Background(), &core.Request{Model: "llama3.2", Tools: []core.ToolDef{{Name: "get_weather"}}}, ev.On)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 1 || done[0].Name != "get_weather" || string(done[0].Args) != `{"city":"Tokyo"}` || !wire.IsSynthesized(done[0].ID) {
		t.Fatalf("calls %+v", done)
	}
	if resp.Stop != core.StopTool || resp.Usage != (core.Usage{Input: 169, Output: 15}) {
		t.Fatalf("%+v", resp)
	}
}

func TestStreamParallelToolsAndThinking(t *testing.T) {
	// Both documented chunk shapes in one stream: the tools chunk (as in the
	// streaming-with-tools example, two calls) and a thinking chunk (the
	// "thinking" field of the documented message object).
	body := `{"model":"qwen3","created_at":"2025-07-07T20:22:19.1Z","message":{"role":"assistant","content":"","thinking":"Two cities."},"done":false}
{"model":"qwen3","created_at":"2025-07-07T20:22:19.2Z","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_weather","arguments":{"city":"Tokyo"}}},{"function":{"name":"get_weather","arguments":{"city":"Paris"}}}]},"done":false}
{"model":"qwen3","created_at":"2025-07-07T20:22:19.3Z","message":{"role":"assistant","content":""},"done_reason":"stop","done":true,"prompt_eval_count":169,"eval_count":15}
`
	srv := wiretest.NewServer(t, ndjson([]byte(body)))
	var ev wiretest.Events
	resp, err := New(wiretest.Options(srv, "ollama")).Stream(context.Background(), &core.Request{Model: "qwen3"}, ev.On)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.Done()
	if len(done) != 2 || done[0].ID == done[1].ID || string(done[1].Args) != `{"city":"Paris"}` {
		t.Fatalf("calls %+v", done)
	}
	if ev.Thinking() != "Two cities." || resp.Message.Parts[0].SigKind != core.KindOllama {
		t.Fatalf("thinking %q %+v", ev.Thinking(), resp.Message.Parts[0])
	}
	// The thinking goes back in the assistant message's thinking field.
	req := buildRequest(&core.Request{Model: "qwen3", Messages: []core.Message{userText("q"), resp.Message}})
	if req.Messages[1].Thinking != "Two cities." || len(req.Messages[1].ToolCalls) != 2 || req.Messages[1].ToolCalls[0].ID != "" {
		t.Fatalf("assistant echo %+v", req.Messages[1])
	}
}

func TestStreamErrors(t *testing.T) {
	srv := wiretest.NewServer(t, ndjson(wiretest.File(t, "testdata/docs_midstream_error.ndjson")))
	_, err := New(wiretest.Options(srv, "ollama")).Stream(context.Background(), &core.Request{Model: "gemma4"}, nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer || pe.Msg != "an error was encountered while running the model" {
		t.Fatalf("midstream: %v", err)
	}
	// Cut mid-line: undecodable, a server fault.
	full := wiretest.File(t, "testdata/docs_chat_stream.ndjson")
	srv = wiretest.NewServer(t, ndjson(full[:100]))
	_, err = New(wiretest.Options(srv, "ollama")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer {
		t.Fatalf("cut line: %v", err)
	}
	// Cut after a whole line but before done: the connection dropped.
	first := full[:bytes.IndexByte(full, '\n')+1]
	srv = wiretest.NewServer(t, ndjson(first))
	_, err = New(wiretest.Options(srv, "ollama")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindNetwork {
		t.Fatalf("no done: %v", err)
	}
	for _, tc := range []struct {
		status int
		want   core.ErrorKind
	}{
		{400, core.ErrKindInvalidRequest}, {404, core.ErrKindInvalidRequest}, {429, core.ErrKindRateLimit},
		{500, core.ErrKindServer}, {502, core.ErrKindServer}, {401, core.ErrKindAuth},
	} {
		srv := wiretest.NewServer(t, wiretest.JSON(tc.status, []byte(`{"error":"the model failed to generate a response"}`)))
		_, err := New(wiretest.Options(srv, "ollama")).Stream(context.Background(), &core.Request{Model: "m"}, nil)
		if !errors.As(err, &pe) || pe.Kind != tc.want || pe.Msg != "the model failed to generate a response" {
			t.Errorf("%d: %v", tc.status, err)
		}
	}
}

func TestRequestShape(t *testing.T) {
	srv := wiretest.NewServer(t, ndjson(wiretest.File(t, "testdata/docs_chat_stream.ndjson")))
	temp := 0.1
	req := &core.Request{
		Model: "qwen3:14b", NumCtx: 16384, MaxTokens: 2048, Temperature: &temp, Stop: []string{"</s>"},
		System: []core.SystemBlock{{Text: "S1"}},
		Tools:  []core.ToolDef{{Name: "get_weather", Description: "Get the weather", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []core.Message{
			{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "look"}, {Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Data: []byte("img")}}}},
			{Role: core.MsgAssistant, Parts: []core.Part{
				{Type: core.PartThinking, Text: "foreign", Sig: "x", SigKind: core.KindAnthropic},
				{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "toolu_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Tokyo"}`)}},
			}},
			{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "toolu_1", Name: "get_weather", Content: "11 degrees"}}}},
		},
	}
	if _, err := New(wiretest.Options(srv, "ollama")).Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"qwen3:14b","messages":[{"role":"system","content":"S1"},` +
		`{"role":"user","content":"look","images":["aW1n"]},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"toolu_1","function":{"name":"get_weather","arguments":{"city":"Tokyo"}}}]},` +
		`{"role":"tool","content":"11 degrees","tool_name":"get_weather"}],` +
		`"tools":[{"type":"function","function":{"name":"get_weather","description":"Get the weather","parameters":{"type":"object"}}}],` +
		`"stream":true,"options":{"num_ctx":16384,"num_predict":2048,"temperature":0.1,"stop":["</s>"]}}`
	r := srv.Requests()[0]
	if string(r.Body) != want {
		t.Fatalf("body\n got %s\nwant %s", r.Body, want)
	}
	if r.Header.Get("Authorization") != "Bearer "+wiretest.Key {
		t.Fatalf("auth header %q", r.Header.Get("Authorization"))
	}
}

func TestDiscovery(t *testing.T) {
	srv := wiretest.NewServer(t,
		wiretest.JSON(200, wiretest.File(t, "testdata/docs_tags.json")),
		wiretest.JSON(200, wiretest.File(t, "testdata/docs_show.json")),
	)
	o := wiretest.Options(srv, "ollama")
	o.Discover = true
	ms, err := New(o).Models(context.Background())
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %+v", err, ms)
	}
	// /api/ps is not served here, so the 131,072 trained maximum is capped.
	for _, m := range ms {
		if m.Context != DefaultWindow || !m.Vision || !m.Thinking || m.Tools || !m.Local {
			t.Fatalf("model %+v", m)
		}
	}
	srv = wiretest.NewServer(t, wiretest.JSON(200, wiretest.File(t, "testdata/docs_ps.json")))
	running, err := New(wiretest.Options(srv, "ollama")).Running(context.Background())
	if err != nil || running["gemma4"] != 4096 {
		t.Fatalf("%v %v", err, running)
	}
}

func TestDiscoveryListsPastTheShowLimit(t *testing.T) {
	var tags struct {
		Models []map[string]string `json:"models"`
	}
	for i := range maxShow + 10 {
		tags.Models = append(tags.Models, map[string]string{"name": fmt.Sprintf("m%d:latest", i)})
	}
	body, _ := json.Marshal(tags)
	srv := wiretest.NewServer(t, wiretest.JSON(200, body), wiretest.JSON(200, wiretest.File(t, "testdata/docs_show.json")))
	ms, err := New(wiretest.Options(srv, "ollama")).Discover(context.Background())
	if err != nil || len(ms) != maxShow+10 {
		t.Fatalf("%v: %d models", err, len(ms))
	}
	if last := ms[len(ms)-1]; last.Ref.Model != fmt.Sprintf("m%d:latest", maxShow+9) || last.Context != 0 || !last.Tools {
		t.Fatalf("last %+v", last)
	}
	// tags, ps, then one show per model up to the limit
	if n := len(srv.Requests()); n != 2+maxShow {
		t.Fatalf("%d requests", n)
	}
}

// Window never sends the trained maximum blindly: it caps at DefaultWindow
// unless Ollama already serves the model with more.
func TestWindow(t *testing.T) {
	for _, tc := range []struct{ trained, served, want int }{
		{131072, 0, DefaultWindow},
		{8192, 0, 8192},
		{131072, 4096, DefaultWindow},
		{131072, 32768, 32768},
		{32768, 65536, 32768},
		{0, 0, 0},
		{0, 4096, 4096},
	} {
		if got := Window(tc.trained, tc.served); got != tc.want {
			t.Errorf("Window(%d, %d) = %d, want %d", tc.trained, tc.served, got, tc.want)
		}
	}
}
