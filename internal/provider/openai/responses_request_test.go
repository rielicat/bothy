package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

func marshalReq(t *testing.T, r respRequest) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestResponsesRequestShape(t *testing.T) {
	temp := 0.2
	r := &core.Request{
		Model:       "gpt-6-astra",
		System:      []core.SystemBlock{{Text: "Constitution."}, {Text: "Persona.", CacheBreak: true}},
		Tools:       []core.ToolDef{{Name: "shell", Description: "Run a command.", Schema: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)}, {Name: "noop"}},
		ToolChoice:  "required",
		MaxTokens:   1,
		Temperature: &temp,
		Stop:        []string{"END"},
		Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{
			{Type: core.PartContext, Text: "<turn-context/>"},
			{Type: core.PartText, Text: "What is in this image?"},
			{Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Ref: "a.png", Data: []byte{0x89, 'P'}}},
			{Type: core.PartFile, Blob: &core.Blob{MIME: "application/pdf", Name: "a.pdf", Ref: ".bothy/in/1/a.pdf", Size: 3}},
		}}},
	}
	got := string(marshalReq(t, NewResponses(respOpts("openai"), nil).buildRequest(r)))
	want := `{"model":"gpt-6-astra","instructions":"Constitution.\n\nPersona.","input":[{"role":"user","content":[` +
		`{"type":"input_text","text":"<turn-context/>"},{"type":"input_text","text":"What is in this image?"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,iVA=","detail":"auto"},` +
		`{"type":"input_text","text":"[attached file a.pdf (application/pdf, 3 bytes) at .bothy/in/1/a.pdf]"}]}],` +
		`"tools":[{"type":"function","name":"shell","description":"Run a command.","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}},"strict":false},` +
		`{"type":"function","name":"noop","parameters":{"type":"object"},"strict":false}],` +
		`"tool_choice":"required","max_output_tokens":16,"temperature":0.2,"store":false,"include":["reasoning.encrypted_content"],"stream":true}` + "\n"
	if got != want {
		t.Fatalf("request\n got %s\nwant %s", got, want)
	}
	for _, absent := range []string{`"stop"`, `"messages"`, `"max_tokens"`, `"max_completion_tokens"`, `"stream_options"`, `"reasoning_effort"`, `"previous_response_id"`} {
		if strings.Contains(got, absent) {
			t.Fatalf("%s sent: %s", absent, got)
		}
	}

	// A model known not to reason is not asked for encrypted reasoning, and
	// without tools there is no tool_choice.
	catalog := func(m string) (bool, bool) {
		switch m {
		case "gpt-4.1":
			return false, true
		case "gpt-6-astra":
			return true, true
		}
		return false, false
	}
	c := NewResponses(respOpts("openai"), catalog)
	got = string(marshalReq(t, c.buildRequest(&core.Request{Model: "gpt-4.1", Temperature: &temp, Messages: []core.Message{userText("hi")}})))
	if strings.Contains(got, "include") || strings.Contains(got, "tool_choice") || strings.Contains(got, "max_output_tokens") ||
		!strings.Contains(got, `"temperature":0.2`) {
		t.Fatalf("request %s", got)
	}
	// A model the catalog knows reasons gets no temperature; an unknown one
	// keeps it until it refuses.
	got = string(marshalReq(t, c.buildRequest(&core.Request{Model: "gpt-6-astra", Temperature: &temp, Messages: []core.Message{userText("hi")}})))
	if strings.Contains(got, "temperature") || strings.Contains(got, "top_p") || !strings.Contains(got, "include") {
		t.Fatalf("reasoning model request %s", got)
	}
	if got := c.buildRequest(&core.Request{Model: "ft:custom", Temperature: &temp}); got.Temperature == nil || len(got.Include) != 1 {
		t.Fatalf("unknown model request %+v", got)
	}
	for _, choice := range []string{"", "auto", "none"} {
		b := c.buildRequest(&core.Request{Tools: []core.ToolDef{{Name: "x"}}, ToolChoice: choice})
		if want := map[string]string{"": "auto", "auto": "auto", "none": "none"}[choice]; b.ToolChoice != want {
			t.Fatalf("tool choice %q -> %q", choice, b.ToolChoice)
		}
	}
}

// assistantTurn is what the Responses client stores for a reply: encrypted
// reasoning, a commentary message and two parallel calls.
func assistantTurn(provider string) core.Message {
	sig := func(v string) string { return provider + ":" + v }
	return core.Message{Role: core.MsgAssistant, Parts: []core.Part{
		{Type: core.PartThinking, Sig: sig(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA1"}`), SigKind: SigResponses},
		{Type: core.PartText, Text: "Checking both files.", Sig: sig(`{"id":"msg_1","phase":"commentary"}`), SigKind: SigResponses},
		{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "call_a", Name: "fs_read", Args: json.RawMessage(`{"path":"a"}`)}, Sig: sig("fc_a"), SigKind: SigResponses},
		{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "call_b", Name: "fs_read", Args: json.RawMessage(`{"path":"b"}`)}, Sig: sig("fc_b"), SigKind: SigResponses},
	}}
}

func toolResults(ids ...string) core.Message {
	m := core.Message{Role: core.MsgTool}
	for _, id := range ids {
		m.Parts = append(m.Parts, core.Part{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: id, Name: "fs_read", Content: "content of " + id}})
	}
	return m
}

func TestResponsesToolLoopItems(t *testing.T) {
	c := NewResponses(respOpts("openai"), nil)
	history := []core.Message{userText("Compare a and b."), assistantTurn("openai"), toolResults("call_a", "call_b")}
	b, _ := json.Marshal(c.buildRequest(&core.Request{Messages: history}).Input)
	want := `[{"role":"user","content":[{"type":"input_text","text":"Compare a and b."}]},` +
		`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA1"},` +
		`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Checking both files.","annotations":[],"logprobs":[]}],"phase":"commentary"},` +
		`{"type":"function_call","id":"fc_a","call_id":"call_a","name":"fs_read","arguments":"{\"path\":\"a\"}"},` +
		`{"type":"function_call","id":"fc_b","call_id":"call_b","name":"fs_read","arguments":"{\"path\":\"b\"}"},` +
		`{"type":"function_call_output","call_id":"call_a","output":"content of call_a"},` +
		`{"type":"function_call_output","call_id":"call_b","output":"content of call_b"}]`
	if string(b) != want {
		t.Fatalf("input\n got %s\nwant %s", b, want)
	}
}

// Everything before the newest turn renders to the same bytes on every
// call, so the provider's prompt cache keeps matching the prefix.
func TestResponsesPrefixByteStable(t *testing.T) {
	c := NewResponses(respOpts("openai"), nil)
	sys := []core.SystemBlock{{Text: "S1"}, {Text: "S2", CacheBreak: true}}
	tools := []core.ToolDef{{Name: "fs_read", Schema: json.RawMessage(`{"type":"object"}`)}}
	turn1 := []core.Message{userText("Compare a and b.")}
	turn2 := append(append([]core.Message(nil), turn1...), assistantTurn("openai"), toolResults("call_a", "call_b"))
	turn3 := append(append([]core.Message(nil), turn2...),
		core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "They match.", Sig: `openai:{"id":"msg_2","phase":"final_answer"}`, SigKind: SigResponses}}},
		userText("Thanks."))
	var prev respRequest
	for i, h := range [][]core.Message{turn1, turn2, turn3} {
		r := c.buildRequest(&core.Request{Model: "gpt-6-astra", System: sys, Tools: tools, Messages: h, MaxTokens: 4096})
		if i > 0 {
			if r.Instructions != prev.Instructions || string(marshalTools(t, r.Tools)) != string(marshalTools(t, prev.Tools)) {
				t.Fatal("instructions or tools changed between turns")
			}
			for j := range prev.Input {
				a, _ := json.Marshal(prev.Input[j])
				b, _ := json.Marshal(r.Input[j])
				if !bytes.Equal(a, b) {
					t.Fatalf("turn %d item %d changed:\n%s\n%s", i+1, j, a, b)
				}
			}
		}
		prev = r
	}
}

// The session's cache key goes out as prompt_cache_key, and with the newest
// items removed every later body serialises to exactly the earlier one: key,
// instructions, tools and history all keep their bytes.
func TestResponsesCacheKeyPrefixStable(t *testing.T) {
	c := NewResponses(respOpts("openai"), nil)
	temp := 0.3
	base := core.Request{Model: "gpt-6-astra", System: []core.SystemBlock{{Text: "S1"}, {Text: "S2", CacheBreak: true}},
		Tools: []core.ToolDef{{Name: "fs_read", Schema: json.RawMessage(`{"type":"object"}`)}}, ToolChoice: "auto",
		MaxTokens: 4096, Temperature: &temp, CacheKey: "bothy-0123456789abcdef01234567"}
	turn1 := []core.Message{userText("Compare a and b.")}
	turn2 := append(append([]core.Message(nil), turn1...), assistantTurn("openai"), toolResults("call_a", "call_b"))
	turn3 := append(append([]core.Message(nil), turn2...),
		core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "They match.", Sig: `openai:{"id":"msg_2","phase":"final_answer"}`, SigKind: SigResponses}}},
		userText("Thanks."))
	var prev []byte
	prevItems := 0
	for i, h := range [][]core.Message{turn1, turn2, turn3} {
		r := base
		r.Messages = h
		body := c.buildRequest(&r)
		if body.PromptCacheKey != base.CacheKey {
			t.Fatalf("turn %d: prompt_cache_key %q", i+1, body.PromptCacheKey)
		}
		if i > 0 {
			cut := body
			cut.Input = body.Input[:prevItems]
			if got := marshalReq(t, cut); !bytes.Equal(got, prev) {
				t.Fatalf("turn %d changed the prefix:\n%s\n%s", i+1, got, prev)
			}
		}
		prev, prevItems = marshalReq(t, body), len(body.Input)
	}
	if !strings.Contains(string(prev), `"prompt_cache_key":"bothy-0123456789abcdef01234567"`) {
		t.Fatalf("body %s", prev)
	}
	if b := marshalReq(t, c.buildRequest(&core.Request{Model: "gpt-6-astra"})); strings.Contains(string(b), "prompt_cache_key") {
		t.Fatalf("empty key sent: %s", b)
	}
}

func marshalTools(t *testing.T, ts []respTool) []byte {
	t.Helper()
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Reasoning, message ids and call ids go back only to the client kind and
// provider that wrote them; the facade strips other kinds before the call,
// and the builder checks again.
func TestResponsesReplayOnlySameSigKind(t *testing.T) {
	c := NewResponses(respOpts("openai"), nil)
	foreign := core.Message{Role: core.MsgAssistant, Parts: []core.Part{
		{Type: core.PartThinking, Text: "plan", Sig: "c2ln", SigKind: core.KindAnthropic},
		{Type: core.PartThinking, Text: "chat reasoning", SigKind: core.KindOpenAI},
		{Type: core.PartThinking, Text: "compat", Sig: "reasoning_content:deepseek", SigKind: core.KindOpenAICompat},
		{Type: core.PartText, Text: "Done.", Sig: `{"id":"msg_x"}`, SigKind: core.KindOpenAI},
		{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "call_g", Name: "x", Args: json.RawMessage(`{}`)}, Sig: "sig-gemini", SigKind: core.KindGemini},
	}}
	b, _ := json.Marshal(c.buildRequest(&core.Request{Messages: []core.Message{foreign, assistantTurn("azure")}}).Input)
	want := `[{"role":"assistant","content":"Done."},{"type":"function_call","call_id":"call_g","name":"x","arguments":"{}"},` +
		`{"role":"assistant","content":"Checking both files."},` +
		`{"type":"function_call","call_id":"call_a","name":"fs_read","arguments":"{\"path\":\"a\"}"},` +
		`{"type":"function_call","call_id":"call_b","name":"fs_read","arguments":"{\"path\":\"b\"}"}]`
	if string(b) != want {
		t.Fatalf("input\n got %s\nwant %s", b, want)
	}
	// A reasoning item without encrypted content (a compatible server that
	// sent none) is not replayed, and the items after it lose their ids.
	unpaired := assistantTurn("openai")
	unpaired.Parts[0].Sig = ""
	b, _ = json.Marshal(c.buildRequest(&core.Request{Messages: []core.Message{unpaired}}).Input)
	if strings.Contains(string(b), `"id"`) || strings.Contains(string(b), "reasoning") {
		t.Fatalf("unpaired ids sent: %s", b)
	}
}

func TestResponsesCallIDLimit(t *testing.T) {
	long := "toolu_" + strings.Repeat("x", 80)
	c := NewResponses(wire.Options{ID: "openai"}, nil)
	msgs := []core.Message{
		{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: long, Name: "x", Args: json.RawMessage(`{}`)}}}},
		toolResults(long),
	}
	in := c.buildRequest(&core.Request{Messages: msgs}).Input
	call, out := in[0].(functionCallItem), in[1].(functionOutputItem)
	if call.CallID != out.CallID || len(call.CallID) > maxCallID || call.CallID != callID(long) || callID("call_short") != "call_short" {
		t.Fatalf("call %q output %q", call.CallID, out.CallID)
	}
}
