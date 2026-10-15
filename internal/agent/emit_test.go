package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// A secret the model streams in two deltas that land in different flushes
// must not reach the bus or the sink: each flush is redacted on its own.
func TestSecretSplitAcrossFlushesStaysRedacted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const secret = "CANARY-0123456789abcdef"
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"The key is CANARY-01234", "56789abcdef, keep it."}, Block: block}}})
		defer h.close()
		h.red.Add("vault:global/canary", []byte(secret))
		res := h.submit("what is the key?")
		time.Sleep(time.Second) // the first delta's flush timer fires while the model blocks
		close(block)
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("run = %s", st)
		}
		var bus strings.Builder
		for _, ev := range h.busEvents(res.Run) {
			if d, ok := ev.Data.(core.DeltaData); ok && ev.Type == core.EvRunDelta {
				bus.WriteString(d.Text)
			}
		}
		for where, text := range map[string]string{"bus": bus.String(), "sink": h.sink.Text()} {
			if strings.Contains(text, "0123456789abcdef") || !strings.Contains(text, "The key is") || !strings.Contains(text, "keep it.") {
				t.Errorf("%s text = %q, want the reply with the secret masked", where, text)
			}
		}
	})
}

// The same holds for a tool's progress output: a secret printed in two
// writes that land in different run.tool_output events stays masked.
func TestSecretSplitAcrossToolOutputStaysRedacted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const secret = "CANARY-0123456789abcdef"
		printer := &testTool{name: "printer", exec: true, run: func(_ context.Context, env *core.ToolEnv, _ *core.Action) (*core.ToolResult, error) {
			env.Emit(core.ToolOutputData{Stream: 1, Chunk: "key=CANARY-01234"})
			time.Sleep(time.Second)
			env.Emit(core.ToolOutputData{Stream: 1, Chunk: "56789abcdef done\n"})
			time.Sleep(time.Second)
			return &core.ToolResult{Content: "key=" + secret + " done"}, nil
		}}
		h := newHarness(t, opts{tools: []core.Tool{printer}, steps: []fakes.LLMStep{{ToolCalls: []core.ToolCall{call("printer", `{}`)}}, {Text: []string{"ok"}}}})
		defer h.close()
		h.red.Add("vault:global/canary", []byte(secret))
		res := h.submit("print the key")
		time.Sleep(5 * time.Second) // the tool's own sleeps
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("run = %s", st)
		}
		var out strings.Builder
		for _, ev := range h.busEvents(res.Run) {
			if d, ok := ev.Data.(core.ToolOutputData); ok {
				out.WriteString(d.Chunk)
			}
		}
		if text := out.String(); strings.Contains(text, "0123456789abcdef") || !strings.Contains(text, "key=") || !strings.Contains(text, "done") {
			t.Fatalf("tool output = %q, want it with the secret masked", text)
		}
	})
}

// A key pasted into a chat is masked before the message is stored, so the
// history, search and the API never hold it, even after the key is deleted
// and the redactor forgets it; the live event carries the same text.
func TestPastedSecretIsStoredMasked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const secret = "CANARY-0123456789abcdef"
		echo := &testTool{name: "echo", run: func(context.Context, *core.ToolEnv, *core.Action) (*core.ToolResult, error) {
			return &core.ToolResult{Content: "echoed " + secret}, nil
		}}
		h := newHarness(t, opts{tools: []core.Tool{echo}, steps: []fakes.LLMStep{
			{Text: []string{"Checking " + secret}, ToolCalls: []core.ToolCall{call("echo", `{"text":"`+secret+`"}`)}},
			{Text: []string{"The key " + secret + " is saved."}},
		}})
		defer h.close()
		h.red.Add("vault:global/canary", []byte(secret))
		res := h.submit("I pasted my key by mistake: " + secret)
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("run = %s", st)
		}
		live := map[int64]string{}
		for _, ev := range h.busEvents(res.Run) {
			if d, ok := ev.Data.(core.MessageData); ok && ev.Type == core.EvMessageCreated {
				b, _ := json.Marshal(d.Message.Parts)
				live[d.Message.Seq] = string(b)
			}
		}
		h.red.Remove("vault:global/canary")
		var user string
		for _, m := range h.messages() {
			b, _ := json.Marshal(m.Parts)
			if strings.Contains(string(b), "0123456789abcdef") {
				t.Fatalf("message %d stored with the key: %s", m.Seq, b)
			}
			if live[m.Seq] != string(b) {
				t.Errorf("message %d: stored %s, live event %s", m.Seq, b, live[m.Seq])
			}
			if m.Role == core.MsgUser {
				user = m.PlainText()
			}
		}
		if user != "I pasted my key by mistake: [redacted]" {
			t.Fatalf("stored user text = %q", user)
		}
		hits, err := h.store.Messages().Search(h.ctx, core.SystemViewer(), "pasted", 10)
		if err != nil || len(hits) != 1 || strings.Contains(hits[0].Snippet, "0123456789abcdef") {
			t.Fatalf("search = %+v, %v", hits, err)
		}
	})
}
