package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
)

func (h *harness) appendRaw(m *core.Message) {
	h.t.Helper()
	m.Session = h.sess.ID
	if m.Kind == "" {
		m.Kind = core.KindNormal
	}
	if err := h.store.Messages().Append(h.ctx, m); err != nil {
		h.t.Fatal(err)
	}
}

func toolCallPart(id, name string) core.Part {
	return core.Part{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: id, Name: name, Args: json.RawMessage(`{}`)}}
}

func toolResultMsg(id, name, content string) *core.Message {
	return &core.Message{Role: core.MsgTool, Provenance: core.ProvTool,
		Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: id, Name: name, Content: content}}}}
}

func TestHistoryParkedAndDanglingCallsStayProviderValid(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"ok"}}}})
		defer h.close()
		user := func(text string, kind core.MsgKind) *core.Message {
			return &core.Message{Role: core.MsgUser, Kind: kind, Provenance: core.ProvUser,
				Parts: []core.Part{{Type: core.PartText, Text: text}}}
		}
		h.appendRaw(user("first", core.KindNormal))
		// A parked call (placeholder result) and a dangling call from an
		// interrupted run, with a steer message persisted in between.
		h.appendRaw(&core.Message{Role: core.MsgAssistant, Provenance: core.ProvSystem,
			Parts: []core.Part{{Type: core.PartText, Text: "working"}, toolCallPart("c1", "act"), toolCallPart("c2", "echo")}})
		h.appendRaw(user("also this", core.KindSteer))
		h.appendRaw(toolResultMsg("c1", "act", "Parked: waiting for approval of Run act. It has not run."))
		// An orphan result whose call is not in history.
		h.appendRaw(toolResultMsg("c9", "echo", "orphan"))
		h.submit("next")
		synctest.Wait()
		req := h.requests()[0]
		checkPairs(t, req)
		text := allText(req)
		if !strings.Contains(text, notExecuted) || strings.Contains(text, "orphan") {
			t.Fatalf("history:\n%s", text)
		}
		// The synthesized result is not persisted, so the bytes repeat.
		for _, m := range h.messages() {
			for _, p := range m.Parts {
				if p.ToolResult != nil && p.ToolResult.Content == notExecuted {
					t.Fatal("synthesized result was persisted")
				}
			}
		}
	})
}

func TestOrderToolPairs(t *testing.T) {
	asst := &core.Message{Seq: 2, Role: core.MsgAssistant, Parts: []core.Part{toolCallPart("a", "x"), toolCallPart("b", "y")}}
	steer := &core.Message{Seq: 3, Role: core.MsgUser, Kind: core.KindSteer, Parts: []core.Part{{Type: core.PartText, Text: "s"}}}
	rb := toolResultMsg("b", "y", "B")
	rb.Seq = 4
	ra := toolResultMsg("a", "x", "A")
	ra.Seq = 5
	dup := toolResultMsg("a", "x", "A2")
	dup.Seq = 6
	out := orderToolPairs([]*core.Message{asst, steer, rb, ra, dup})
	var got []string
	for _, m := range out {
		switch {
		case m.Role == core.MsgTool:
			got = append(got, m.Parts[0].ToolResult.Content)
		default:
			got = append(got, string(m.Role))
		}
	}
	want := []string{"assistant", "A", "B", "user"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}
