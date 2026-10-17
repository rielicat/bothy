package agent

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// TestExecSeesItsApproval: a tool that must check its own approval (team
// memory) gets the id of the approval the call ran under, and none when
// policy allowed it outright.
func TestExecSeesItsApproval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		seen := map[string]core.ApprovalID{}
		record := func(_ context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
			mu.Lock()
			defer mu.Unlock()
			seen[a.Tool] = env.Approval
			return &core.ToolResult{Content: "ok"}, nil
		}
		asked := &testTool{name: "asked", se: core.SELocalWrite, run: record}
		auto := &testTool{name: "auto", se: core.SELocalWrite, run: record}
		h := newHarness(t, opts{tools: []core.Tool{asked, auto},
			pol:  &fakes.Policy{Verdicts: map[string]core.Verdict{"asked": core.VerdictAsk, "auto": core.VerdictAuto}},
			appr: &fakes.Approvals{Choice: core.AllowOnce},
			steps: []fakes.LLMStep{{ToolCalls: []core.ToolCall{call("asked", `{}`)}}, {ToolCalls: []core.ToolCall{call("auto", `{}`)}},
				{Text: []string{"done"}}}})
		defer h.close()
		res := h.submit("go")
		synctest.Wait()
		var want core.ApprovalID
		for _, rec := range h.toolCalls(res.Run) {
			if rec.Tool == "asked" {
				want = rec.Approval
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if want == "" || seen["asked"] != want || seen["auto"] != "" {
			t.Fatalf("Exec saw approvals %v, want %q for the asked call and none for the other", seen, want)
		}
	})
}
