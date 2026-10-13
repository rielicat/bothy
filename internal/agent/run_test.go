package agent

import (
	"strings"
	"testing"
	"testing/synctest"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
)

func TestRunWithToolCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		echo := &testTool{name: "echo"}
		h := newHarness(t, opts{tools: []core.Tool{echo}, steps: []fakes.LLMStep{
			{Text: []string{"Let me ", "check."}, ToolCalls: []core.ToolCall{call("echo", `{"x":1}`)}, Usage: core.Usage{Input: 900, Output: 20}},
			{Text: []string{"Done."}, Usage: core.Usage{Input: 1000, Output: 5}},
		}})
		defer h.close()
		res := h.submit("hello")
		synctest.Wait()
		run := h.run(res.Run)
		if run.Status != core.RunDone {
			t.Fatalf("status = %s (%s); notices %v", run.Status, run.Error, h.notices())
		}
		if run.Steps != 2 || echo.calls.Load() != 1 {
			t.Fatalf("steps=%d calls=%d", run.Steps, echo.calls.Load())
		}
		reqs := h.requests()
		if len(reqs) != 2 {
			t.Fatalf("requests = %d", len(reqs))
		}
		checkPairs(t, reqs[1])
		if !strings.Contains(reqs[0].System[0].Text, "Bothy constitution") {
			t.Fatal("constitution missing from S1")
		}
		if !strings.Contains(lastUserText(reqs[0]), "hello") || !strings.Contains(reqs[0].Messages[0].Parts[0].Text, "<turn-context>") {
			t.Fatalf("user turn = %+v", reqs[0].Messages)
		}
		if got := h.sink.Text(); got != "Let me check.Done." {
			t.Fatalf("sink text = %q", got)
		}
		types := h.sink.Types()
		if types[0] != core.EvRunStarted || types[len(types)-1] != core.EvRunFinished {
			t.Fatalf("sink events = %v", types)
		}
		calls, err := h.store.Runs().LLMCalls(h.ctx, core.SystemViewer(), res.Run)
		if err != nil || len(calls) != 2 {
			t.Fatalf("llm calls = %d %v", len(calls), err)
		}
		for _, c := range calls {
			bd := c.Breakdown
			sum := bd.Constitution + bd.Persona + bd.Memory + bd.Skills + bd.Pins + bd.Tools + bd.Summary + bd.History + bd.ToolResults + bd.TurnContext
			if sum != bd.Total || bd.Total != c.Usage.TotalInput() || c.PrefixHash == "" {
				t.Fatalf("breakdown %+v (sum %d) for usage %+v", bd, sum, c.Usage)
			}
		}
		recs := h.toolCalls(res.Run)
		if len(recs) != 1 || recs[0].Status != core.TCOK || recs[0].Verdict != core.VerdictAuto {
			t.Fatalf("tool calls = %+v", recs)
		}
		if got := h.audit.Actions(); !slicesContains(got, "tool.exec") {
			t.Fatalf("audit = %v", got)
		}
	})
}

func slicesContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
