package bedrock

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func splitNova(chunks []string) (thinking, text string) {
	var th, tx strings.Builder
	n := &novaThinking{}
	on := func(b *strings.Builder) func(string) error {
		return func(s string) error { b.WriteString(s); return nil }
	}
	for _, c := range chunks {
		_ = n.feed(c, on(&th), on(&tx))
	}
	_ = n.flush(on(&th), on(&tx))
	return th.String(), tx.String()
}

func TestNovaThinkingSplit(t *testing.T) {
	for _, tc := range []struct{ in, thinking, text string }{
		{"<thinking>I need the fs_list tool.</thinking>\nThere is 1 entry.", "I need the fs_list tool.", "There is 1 entry."},
		{"<thinking>only a plan</thinking>\n", "only a plan", ""},
		{"\n <thinking>a</thinking>b", "a", "b"},
		{"Plain answer with <thinking> later.", "", "Plain answer with <thinking> later."},
		{"<think", "", "<think"},
		{"<thinking>cut off", "cut off", ""},
		{"<thinking>a < b and </thinkin g></thinking>done", "a < b and </thinkin g>", "done"},
		{"", "", ""},
		{"  ", "", "  "},
	} {
		// Every way of cutting the text in two, and one byte at a time.
		var cuts [][]string
		for i := 0; i <= len(tc.in); i++ {
			cuts = append(cuts, []string{tc.in[:i], tc.in[i:]})
		}
		cuts = append(cuts, strings.Split(tc.in, ""))
		for _, chunks := range cuts {
			if th, tx := splitNova(chunks); th != tc.thinking || tx != tc.text {
				t.Fatalf("%q in %q: thinking %q text %q, want %q / %q", tc.in, chunks, th, tx, tc.thinking, tc.text)
			}
		}
	}
}

// Nova Lite writes its plan in <thinking> tags before a tool call (live
// gateway run, 2026-10-14): it becomes a thinking part, the reply keeps
// only the answer, and Claude's text is never touched.
func TestStreamNovaThinkingPreamble(t *testing.T) {
	text := func(i int, s string) frame {
		b, _ := json.Marshal(map[string]any{"contentBlockIndex": i, "delta": map[string]string{"text": s}})
		return event("contentBlockDelta", string(b))
	}
	frames := []frame{
		event("messageStart", `{"role":"assistant"}`),
		text(0, "<think"), text(0, "ing>I need fs_list on \".\".</thin"), text(0, "king>\n"),
		event("contentBlockStop", `{"contentBlockIndex":0}`),
		event("contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"tooluse_1","name":"fs_list"}}}`),
		event("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"path\":\".\"}"}}}`),
		event("contentBlockStop", `{"contentBlockIndex":1}`),
		event("messageStop", `{"stopReason":"tool_use"}`),
		event("metadata", `{"usage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}`),
	}
	resp, ev, _, err := streamFrames(t, frames, &core.Request{Model: "us.amazon.nova-lite-v1:0", Messages: []core.Message{userText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	p := resp.Message.Parts
	if len(p) != 2 || p[0].Type != core.PartThinking || p[0].Text != `I need fs_list on ".".` || p[0].Sig != "" || p[1].Type != core.PartToolCall {
		t.Fatalf("parts %+v", p)
	}
	if ev.Text() != "" || ev.Thinking() != p[0].Text {
		t.Fatalf("events text %q thinking %q", ev.Text(), ev.Thinking())
	}
	// Unsigned, so it is not sent back.
	if s := toJSON(t, buildRequest(&core.Request{Model: "us.amazon.nova-lite-v1:0", Tools: []core.ToolDef{{Name: "fs_list"}},
		Messages: []core.Message{userText("x"), resp.Message}})); strings.Contains(s, "thinking") || strings.Contains(s, "reasoningContent") {
		t.Fatalf("request %s", s)
	}

	// The answer after the preamble, with the block left open at the end.
	frames = []frame{
		text(0, "<thinking>one entry</thinking>\nThere is 1 entry."),
		event("messageStop", `{"stopReason":"end_turn"}`),
	}
	resp, _, _, err = streamFrames(t, frames, &core.Request{Model: "global.amazon.nova-2-lite-v1:0", Messages: []core.Message{userText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if p := resp.Message.Parts; len(p) != 2 || p[0].Text != "one entry" || p[1].Type != core.PartText || p[1].Text != "There is 1 entry." {
		t.Fatalf("parts %+v", p)
	}

	// Claude's text stays text, tags and all.
	resp, _, _, err = streamFrames(t, frames, &core.Request{Model: "us.anthropic.claude-haiku-4-5-20251001-v1:0", Messages: []core.Message{userText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.PlainText() != "<thinking>one entry</thinking>\nThere is 1 entry." {
		t.Fatalf("claude text %q", resp.Message.PlainText())
	}
}

// The live recordings split the tags across deltas ("<thinking", ">" …
// ".</", "thinking", ">\n").
func TestStreamNovaRecordedToolLoop(t *testing.T) {
	nova := &core.Request{Model: "us.amazon.nova-lite-v1:0", Messages: []core.Message{userText("x")}}
	first, ev, _, err := streamFrames(t, readFrames(t, "live_nova_thinking.jsonl"), nova)
	if err != nil {
		t.Fatal(err)
	}
	p := first.Message.Parts
	if len(p) != 2 || p[0].Type != core.PartThinking || !strings.HasPrefix(p[0].Text, "I need to use the 'get_weather' tool") ||
		!strings.HasSuffix(p[0].Text, "set to Lisbon.") || p[1].ToolCall == nil || string(p[1].ToolCall.Args) != `{"city":"Lisbon"}` ||
		first.Stop != core.StopTool || ev.Text() != "" {
		t.Fatalf("first step %+v", p)
	}
	second, ev, _, err := streamFrames(t, readFrames(t, "live_nova_tool_loop.jsonl"), nova)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Message.PlainText(); got != "The weather in Lisbon is sunny with a temperature of 24°C." || ev.Text() != got ||
		second.Usage.CacheRead != 428 || second.Stop != core.StopEnd {
		t.Fatalf("second step %q %+v", got, second.Usage)
	}
}
