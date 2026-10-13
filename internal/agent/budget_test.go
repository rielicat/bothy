package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func repeatCalls(n int, name string, args func(i int) string) []fakes.LLMStep {
	steps := make([]fakes.LLMStep, n)
	for i := range steps {
		steps[i] = fakes.LLMStep{ToolCalls: []core.ToolCall{call(name, args(i))}, Usage: core.Usage{Input: 100, Output: 10}}
	}
	return steps
}

func (h *harness) submitLimits(text string, l core.RunLimits) core.RunID {
	h.t.Helper()
	req := h.req(text)
	req.Limits = &l
	res, err := h.r.Submit(h.ctx, req)
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Run
}

func TestBudgetPathsEndWithTheRightStatus(t *testing.T) {
	priced := core.ModelInfo{Ref: core.ModelRef{Provider: "p", Model: "m"}, Context: 128_000, MaxOutput: 4096, Tools: true,
		Price: core.Price{InPerM: 3_000_000, OutPerM: 15_000_000, Known: true}}
	cases := []struct {
		name   string
		limits core.RunLimits
		info   core.ModelInfo
		steps  []fakes.LLMStep
		kind   core.BudgetKind
		sleep  bool
		spent  float64 // already spent today against a $1 daily cap
	}{
		{name: "steps", limits: core.RunLimits{MaxSteps: 2}, steps: repeatCalls(3, "echo", func(i int) string { return fmt.Sprintf(`{"i":%d}`, i) }), kind: core.BudgetSteps},
		{name: "tokens", limits: core.RunLimits{MaxTokens: 500}, steps: repeatCalls(1, "echo", func(int) string { return `{}` }), kind: core.BudgetTokens},
		{name: "run cost", limits: core.RunLimits{MaxCost: 1000}, info: priced, steps: repeatCalls(1, "echo", func(int) string { return `{}` }), kind: core.BudgetRunCost},
		{name: "time", limits: core.RunLimits{MaxTime: time.Minute}, steps: repeatCalls(1, "nap", func(int) string { return `{}` }), kind: core.BudgetTime, sleep: true},
		{name: "daily projection", info: priced, steps: repeatCalls(1, "echo", func(int) string { return `{}` }), kind: core.BudgetDailyCost, spent: 0.99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				nap := &testTool{name: "nap", run: func(ctx context.Context, _ *core.ToolEnv, _ *core.Action) (*core.ToolResult, error) {
					select {
					case <-time.After(2 * time.Minute):
						return &core.ToolResult{Content: "rested"}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}}
				h := newHarness(t, opts{realBudget: true, info: tc.info, tools: []core.Tool{&testTool{name: "echo"}, nap}, steps: tc.steps,
					cfg: func(c *config.Config) { c.Budgets.DailyUSD = 1 }})
				defer h.close()
				if tc.spent > 0 {
					if err := h.budget.RecordSpend(h.ctx, h.user.ID, core.CallInfo{Purpose: "probe", Cost: core.MicrosFromUSD(tc.spent)}); err != nil {
						t.Fatal(err)
					}
				}
				id := h.submitLimits("go", tc.limits)
				synctest.Wait()
				if tc.sleep {
					time.Sleep(90 * time.Second) // the nap outlasts the one-minute limit
					synctest.Wait()
				}
				run := h.run(id)
				if run.Status != core.RunBudgetExceeded || run.Error != "budget exceeded: "+string(tc.kind) {
					t.Fatalf("run = %s %q", run.Status, run.Error)
				}
				notes := h.notices()
				if len(notes) != 1 || !strings.Contains(notes[0], "Settings → Budgets") {
					t.Fatalf("notices = %v", notes)
				}
				if tc.kind == core.BudgetSteps && !strings.Contains(notes[0], "Continue") {
					t.Fatalf("steps notice lacks Continue: %q", notes[0])
				}
				if !slicesContains(h.audit.Actions(), "budget.exceeded") {
					t.Fatal("budget.exceeded not audited")
				}
				evs, _ := h.bus.Since(0, core.Filter{System: true})
				var fin *core.RunFinishedData
				for _, ev := range evs {
					if d, ok := ev.Data.(core.RunFinishedData); ok && ev.Run == id {
						fin = &d
					}
				}
				if fin == nil || fin.Budget != tc.kind {
					t.Fatalf("run.finished = %+v, want budget %s", fin, tc.kind)
				}
			})
		})
	}
}

func TestContinueAfterStepsBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		steps := repeatCalls(1, "echo", func(int) string { return `{}` })
		steps = append(steps, fakes.LLMStep{Text: []string{"finished"}})
		h := newHarness(t, opts{realBudget: true, tools: []core.Tool{&testTool{name: "echo"}}, steps: steps})
		defer h.close()
		id := h.submitLimits("go", core.RunLimits{MaxSteps: 1})
		synctest.Wait()
		if st := h.run(id).Status; st != core.RunBudgetExceeded {
			t.Fatalf("status = %s", st)
		}
		res, err := h.r.Continue(h.ctx, ownerViewer(h), id, h.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		run := h.run(res.Run)
		if run.Status != core.RunDone || run.Limits.MaxSteps != 10 {
			t.Fatalf("continued run = %s limits %+v", run.Status, run.Limits)
		}
		if _, err := h.r.Continue(h.ctx, ownerViewer(h), res.Run, h.user.ID); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("continue of a finished run = %v", err)
		}
	})
}

// TestContinueKeepsTheRunsProvenance: a run an untrusted group member
// started is still theirs after the owner presses Continue, so floor F6
// keeps asking for every action above read.
func TestContinueKeepsTheRunsProvenance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		steps := repeatCalls(1, "echo", func(int) string { return `{}` })
		steps = append(steps, fakes.LLMStep{ToolCalls: []core.ToolCall{call("echo", `{"again":true}`)}}, fakes.LLMStep{Text: []string{"finished"}})
		h := newHarness(t, opts{realBudget: true, tools: []core.Tool{&testTool{name: "echo"}}, steps: steps})
		defer h.close()
		req := h.req("member: run the deploy script")
		req.Provenance, req.Sender = core.ProvUntrusted, "telegram:999"
		req.Limits = &core.RunLimits{MaxSteps: 1}
		first, err := h.r.Submit(h.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(first.Run).Status; st != core.RunBudgetExceeded {
			t.Fatalf("status = %s", st)
		}
		res, err := h.r.Continue(h.ctx, ownerViewer(h), first.Run, h.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		inputs := h.pol.Inputs
		if len(inputs) != 2 || inputs[1].Provenance != core.ProvUntrusted {
			t.Fatalf("policy inputs of the continued run = %+v", inputs)
		}
		for _, m := range h.messages() {
			if m.Run == res.Run && m.Role == core.MsgUser && m.Provenance != core.ProvUntrusted {
				t.Fatalf("continued turn persisted as %s", m.Provenance)
			}
		}
	})
}

func TestDailyCapRefusesRunWithoutRow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{realBudget: true, cfg: func(c *config.Config) { c.Budgets.DailyUSD = 1 }})
		defer h.close()
		if err := h.budget.RecordSpend(h.ctx, h.user.ID, core.CallInfo{Purpose: "probe", Cost: core.MicrosFromUSD(1)}); err != nil {
			t.Fatal(err)
		}
		_, err := h.r.Submit(h.ctx, h.req("hello"))
		var be *core.BudgetError
		if !errors.As(err, &be) || be.Kind != core.BudgetDailyCost {
			t.Fatalf("submit = %v", err)
		}
		if len(h.runs()) != 0 {
			t.Fatal("a run row was created")
		}
		if n := h.notices(); len(n) != 1 || !strings.Contains(n[0], "New runs start again at 00:00") {
			t.Fatalf("notices = %v", n)
		}
	})
}

func TestLoopWarnThenAbort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := 0
		poll := &testTool{name: "poll", run: func(context.Context, *core.ToolEnv, *core.Action) (*core.ToolResult, error) {
			n++
			return &core.ToolResult{Content: fmt.Sprintf("status %d", n)}, nil
		}}
		h := newHarness(t, opts{realBudget: true, tools: []core.Tool{poll}, steps: repeatCalls(6, "poll", func(int) string { return `{"job":1}` })})
		defer h.close()
		res := h.submit("wait for the job")
		synctest.Wait()
		run := h.run(res.Run)
		if run.Status != core.RunLoopDetected {
			t.Fatalf("status = %s", run.Status)
		}
		var warned int
		for _, m := range h.messages() {
			for _, p := range m.Parts {
				if p.ToolResult != nil && strings.Contains(p.ToolResult.Content, loopWarnNote) {
					warned++
				}
			}
		}
		if warned != 2 || n != 4 {
			t.Fatalf("warned %d times, executed %d times", warned, n)
		}
		if !strings.Contains(strings.Join(h.notices(), "\n"), "loop detected") {
			t.Fatalf("notices = %v", h.notices())
		}
	})
}

func TestLoopOnRepeatedTextAndErrors(t *testing.T) {
	t.Run("identical text", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var steps []fakes.LLMStep
			for i := range 4 {
				steps = append(steps, fakes.LLMStep{Text: []string{"Let me look again."}, ToolCalls: []core.ToolCall{call("echo", fmt.Sprintf(`{"i":%d}`, i))}})
			}
			h := newHarness(t, opts{realBudget: true, tools: []core.Tool{&testTool{name: "echo"}}, steps: steps})
			defer h.close()
			res := h.submit("go")
			synctest.Wait()
			if st := h.run(res.Run).Status; st != core.RunLoopDetected || h.run(res.Run).Steps != 3 {
				t.Fatalf("status = %s after %d steps", st, h.run(res.Run).Steps)
			}
		})
	})
	t.Run("five tool errors in a row", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fail := &testTool{name: "fail", run: func(_ context.Context, _ *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
				return nil, errors.New("boom " + string(a.Payload))
			}}
			h := newHarness(t, opts{realBudget: true, tools: []core.Tool{fail}, steps: repeatCalls(6, "fail", func(i int) string { return fmt.Sprintf(`{"i":%d}`, i) })})
			defer h.close()
			res := h.submit("go")
			synctest.Wait()
			if st := h.run(res.Run).Status; st != core.RunLoopDetected || fail.calls.Load() != 5 {
				t.Fatalf("status = %s after %d calls", st, fail.calls.Load())
			}
		})
	})
}

func TestApprovalWaitDoesNotCountAgainstTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &testTool{name: "send", se: core.SEExternalSend}
		h := newHarness(t, opts{realBudget: true, tools: []core.Tool{send},
			pol:   &fakes.Policy{Verdicts: map[string]core.Verdict{"send": core.VerdictAsk}},
			appr:  &fakes.Approvals{Manual: true},
			steps: []fakes.LLMStep{{ToolCalls: []core.ToolCall{call("send", `{}`)}}, {Text: []string{"sent"}}}})
		defer h.close()
		id := h.submitLimits("send it", core.RunLimits{MaxTime: time.Minute, MaxSteps: 5})
		synctest.Wait()
		time.Sleep(10 * time.Minute) // the user takes a while
		synctest.Wait()
		if st := h.run(id).Status; st != core.RunWaitingApproval {
			t.Fatalf("status while waiting = %s", st)
		}
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: h.appr.All()[0].ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(id).Status; st != core.RunDone || send.calls.Load() != 1 {
			t.Fatalf("status = %s, calls %d", st, send.calls.Load())
		}
	})
}

func TestBudgetWarningInTurnContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{realBudget: true, cfg: func(c *config.Config) { c.Budgets.DailyUSD = 1 },
			steps: []fakes.LLMStep{{Text: []string{"ok"}}}})
		defer h.close()
		if err := h.budget.RecordSpend(h.ctx, h.user.ID, core.CallInfo{Purpose: "probe", Cost: core.MicrosFromUSD(0.9)}); err != nil {
			t.Fatal(err)
		}
		h.submit("hi")
		synctest.Wait()
		if ctx := lastUserText(h.requests()[0]); !strings.Contains(ctx, "budget: $0.1000 of today's $1.0000 budget left") {
			t.Fatalf("turn context = %q", ctx)
		}
	})
}
