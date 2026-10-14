package mockllm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func postResponses(t *testing.T, s *Server, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(s.URL()+"/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

type respEvent struct {
	name string
	data map[string]any
}

// respEvents reads the typed SSE events; done reports a terminal event.
func respEvents(t *testing.T, r io.Reader) (events []respEvent, done bool) {
	t.Helper()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var name string
	for sc.Scan() {
		line := sc.Text()
		if n, ok := strings.CutPrefix(line, "event: "); ok {
			name = n
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("event %q: %v", data, err)
		}
		if m["type"] != name {
			t.Fatalf("event: %q but type %v", name, m["type"])
		}
		events = append(events, respEvent{name: name, data: m})
		done = done || name == "response.completed"
	}
	return events, done
}

func TestResponsesStream(t *testing.T) {
	s := newServer(t, []Rule{{
		Reasoning: "Need both cities.",
		Text:      []string{"Checking ", "both."},
		ToolCalls: []core.ToolCall{{ID: "call_a", Name: "weather", Args: json.RawMessage(`{"city":"Oslo"}`)}, {Name: "weather", Args: json.RawMessage(`{"city":"Bergen"}`)}},
		Usage:     &core.Usage{Input: 100, Output: 40, CacheRead: 300, CacheWrite: 50, Reasoning: 12},
	}})
	resp := postResponses(t, s, `{"model":"mock","instructions":"You are Bothy.","input":[{"role":"user","content":[{"type":"input_text","text":"weather?"}]}],"tools":[{"type":"function","name":"weather","parameters":{"type":"object"},"strict":false}],"stream":true,"store":false}`)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events, done := respEvents(t, resp.Body)
	if !done || events[0].name != "response.created" {
		t.Fatalf("events %v", events)
	}
	var text, args, summary strings.Builder
	var items []map[string]any
	for i, ev := range events {
		if int(ev.data["sequence_number"].(float64)) != i {
			t.Fatalf("sequence %v at %d", ev.data["sequence_number"], i)
		}
		switch ev.name {
		case "response.output_text.delta":
			text.WriteString(ev.data["delta"].(string))
		case "response.function_call_arguments.delta":
			args.WriteString(ev.data["delta"].(string))
		case "response.reasoning_summary_text.delta":
			summary.WriteString(ev.data["delta"].(string))
		case "response.output_item.done":
			items = append(items, ev.data["item"].(map[string]any))
		}
	}
	if text.String() != "Checking both." || args.String() != `{"city":"Oslo"}{"city":"Bergen"}` || summary.String() != "Need both cities." {
		t.Fatalf("text %q args %q summary %q", text.String(), args.String(), summary.String())
	}
	if len(items) != 4 || items[0]["type"] != "reasoning" || !strings.HasPrefix(items[0]["encrypted_content"].(string), "mockenc_") ||
		items[1]["type"] != "message" || items[1]["phase"] != "commentary" ||
		items[2]["call_id"] != "call_a" || items[3]["call_id"] != "call_mock1" || items[3]["arguments"] != `{"city":"Bergen"}` {
		t.Fatalf("items %v", items)
	}
	final := events[len(events)-1].data["response"].(map[string]any)
	u := final["usage"].(map[string]any)
	in := u["input_tokens_details"].(map[string]any)
	out := u["output_tokens_details"].(map[string]any)
	if final["status"] != "completed" || len(final["output"].([]any)) != 4 || u["input_tokens"] != 450.0 || in["cached_tokens"] != 300.0 ||
		in["cache_write_tokens"] != 50.0 || u["output_tokens"] != 40.0 || out["reasoning_tokens"] != 12.0 || u["total_tokens"] != 490.0 {
		t.Fatalf("completed %v", final)
	}
	if got := s.Requests(); len(got) != 1 || !bytes.Contains(got[0], []byte(`"store":false`)) {
		t.Fatalf("requests %s", got)
	}
}

func TestResponsesNonStreamAndMatching(t *testing.T) {
	s := newServer(t, []Rule{
		{MatchToolResult: `^4°C$`, Text: []string{"It is 4°C."}},
		{MatchSystem: `(?s)^You are Bothy\.\nBe brief\.$`, MatchUser: `weather`, ToolCalls: []core.ToolCall{{Name: "weather", Args: json.RawMessage(`{}`)}}},
	})
	tool := `{"model":"mock","instructions":"You are Bothy.","input":[{"role":"developer","content":"Be brief."},{"role":"user","content":"weather?"},` +
		`{"type":"function_call","call_id":"c1","name":"weather","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"4°C"}]}`
	var r response
	if err := json.NewDecoder(postResponses(t, s, tool).Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "completed" || len(r.Output) != 1 || r.Output[0]["phase"] != "final_answer" {
		t.Fatalf("response %+v", r)
	}
	first := `{"model":"mock","instructions":"You are Bothy.","input":[{"role":"developer","content":"Be brief."},{"role":"user","content":"weather?"}]}`
	if err := json.NewDecoder(postResponses(t, s, first).Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if len(r.Output) != 1 || r.Output[0]["type"] != "function_call" || r.Usage == nil {
		t.Fatalf("response %+v", r)
	}
	// A plain string input is one user message.
	wantError(t, postResponses(t, s, `{"model":"mock","input":"hello"}`), http.StatusBadRequest, "no_rule")
}

func TestResponsesFaults(t *testing.T) {
	s := newServer(t, []Rule{
		{MatchUser: "rate", Fault: FaultRateLimit},
		{MatchUser: "cut", Text: []string{"a", "b", "c", "d"}, Fault: FaultMidstream},
		// The Chat Completions tool faults do not apply to Responses.
		{MatchUser: "astra", ToolCalls: []core.ToolCall{{Name: "x"}}, Fault: FaultToolsResponsesOnly},
	})
	if resp := postResponses(t, s, `{"model":"mock","input":"rate"}`); resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("rate: %d", resp.StatusCode)
	}
	resp := postResponses(t, s, `{"model":"mock","input":"cut","stream":true}`)
	if events, done := respEvents(t, resp.Body); done || len(events) == 0 {
		t.Fatalf("midstream: done %v after %d events", done, len(events))
	}
	resp = postResponses(t, s, `{"model":"mock","input":"astra","tools":[{"type":"function","name":"x"}],"stream":true}`)
	if _, done := respEvents(t, resp.Body); resp.StatusCode != http.StatusOK || !done {
		t.Fatalf("astra over responses: %d", resp.StatusCode)
	}
}

func TestChatToolFaults(t *testing.T) {
	s := newServer(t, []Rule{
		{MatchUser: "sol", ToolCalls: []core.ToolCall{{Name: "x"}}, Fault: FaultToolsReasoning},
		{MatchUser: "astra", ToolCalls: []core.ToolCall{{Name: "x"}}, Fault: FaultToolsResponsesOnly},
	})
	chat := func(text, effort string, tools bool) *http.Response {
		body := map[string]any{"model": "mock", "messages": []msg{{Role: "user", Content: text}}}
		if tools {
			body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "x"}}}
		}
		if effort != "" {
			body["reasoning_effort"] = effort
		}
		b, _ := json.Marshal(body)
		resp, err := http.Post(s.URL()+"/chat/completions", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	for _, effort := range []string{"", "medium"} {
		e := wantError(t, chat("sol", effort, true), http.StatusBadRequest, "")
		if !strings.Contains(e.Error.Message, "Function tools with reasoning_effort are not supported for mock in /v1/chat/completions") {
			t.Fatalf("message %q", e.Error.Message)
		}
		wantError(t, chat("astra", effort, true), http.StatusBadRequest, "")
	}
	if resp := chat("sol", "none", true); resp.StatusCode != http.StatusOK {
		t.Fatalf("sol with none: %d", resp.StatusCode)
	}
	wantError(t, chat("astra", "none", true), http.StatusBadRequest, "unsupported_value")
	// Without tools the rules answer normally.
	for _, text := range []string{"sol", "astra"} {
		if resp := chat(text, "", false); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s without tools: %d", text, resp.StatusCode)
		}
	}
}
