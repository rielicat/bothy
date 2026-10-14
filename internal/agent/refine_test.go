package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/agent/refine"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func say(s string) fakes.LLMStep {
	return fakes.LLMStep{Text: []string{s}, Usage: core.Usage{Input: 1000, Output: 50}}
}

func refineCfg(drafts, depth int, early bool) func(c *config.Config) {
	return func(c *config.Config) {
		c.Agent.Refine.Drafts, c.Agent.Refine.Depth, c.Agent.Refine.EarlyStop = drafts, depth, early
	}
}

func (h *harness) submitRefine(msg string, on bool) core.RunID {
	h.t.Helper()
	req := h.req(msg)
	req.Refine = &on
	res, err := h.r.Submit(h.ctx, req)
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Run
}

// answer returns the last assistant message of the session and its trace.
func (h *harness) answer() (*core.Message, *core.RefineTrace) {
	h.t.Helper()
	msgs := h.messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != core.MsgAssistant || m.Kind != core.KindNormal {
			continue
		}
		for _, p := range m.Parts {
			if p.Type == core.PartRefine {
				return m, p.Refine
			}
		}
		return m, nil
	}
	h.t.Fatal("no assistant message")
	return nil, nil
}

func roleOf(req *core.Request) string {
	last := req.Messages[len(req.Messages)-1]
	t := last.PlainText()
	for _, role := range []string{"generator", "critic", "corrector"} {
		if last.Role == core.MsgUser && strings.HasPrefix(t, "[Bothy refine · "+role+"]") {
			return role
		}
	}
	return ""
}

func TestRefineVotesOverDraftsAndStopsEarly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{tools: []core.Tool{&testTool{name: "echo"}}, cfg: refineCfg(3, 2, true), steps: []fakes.LLMStep{
			say("Six times seven is 42."),                                            // the run's own answer: draft 0
			say("It is 41.\nFinal answer: 41"),                                       // draft 1
			say("Final answer: 42.0"),                                                // draft 2
			say("Six times seven is 42.\nFinal answer: 42"), say("Verdict: correct"), // draft 0, round 1
			say("It is 41.\nFinal answer: 41"), say("Verdict: needs changes\n6 × 7 is 42."), say("6 × 7 = 42.\nFinal answer: 42"),
			say("Final answer: 42.0"), say("Verdict: correct"), // draft 2: confirmed, no correction
			say("Noted."), // the next turn, not refined
		}})
		defer h.close()
		h.r.refineParallel = 1
		sub := h.bus.Subscribe(core.Filter{System: true}, 1024)
		defer sub.Close()
		id := h.submitRefine("What is six times seven?", true)
		synctest.Wait()

		run := h.run(id)
		if run.Status != core.RunDone || run.Steps != 1 {
			t.Fatalf("run %s (%s), %d steps; notices %v", run.Status, run.Error, run.Steps, h.notices())
		}
		reqs := h.requests()
		if len(reqs) != 10 || len(reqs) > core.RefineMaxCalls(3, 2) {
			t.Fatalf("%d model calls, want 10", len(reqs))
		}
		if reqs[0].ToolChoice != "auto" {
			t.Fatalf("the answer step offers tools: %q", reqs[0].ToolChoice)
		}
		var roles []string
		for _, r := range reqs[1:] {
			if r.ToolChoice != "none" || len(r.Tools) == 0 {
				t.Fatalf("refinement keeps the tools block but never uses it: choice %q, %d tools", r.ToolChoice, len(r.Tools))
			}
			roles = append(roles, roleOf(r))
		}
		want := []string{"", "", "generator", "critic", "generator", "critic", "corrector", "generator", "critic"}
		if !slices.Equal(roles, want) {
			t.Fatalf("call roles %q, want %q", roles, want)
		}
		if !slices.EqualFunc(reqs[1].Messages, reqs[0].Messages, func(a, b core.Message) bool { return a.PlainText() == b.PlainText() }) {
			t.Fatal("a draft must see exactly the answer step's context")
		}
		corr := lastUserText(reqs[7])
		if !strings.Contains(corr, "It is 41.\nFinal answer: 41") || !strings.Contains(corr, "6 × 7 is 42.") ||
			!strings.Contains(corr, `<draft id="`) || !strings.Contains(corr, `<critique id="`) {
			t.Fatalf("the corrector sees the draft and the critique:\n%s", corr)
		}

		msg, tr := h.answer()
		if msg.PlainText() != "Six times seven is 42.\nFinal answer: 42" {
			t.Fatalf("reply %q", msg.PlainText())
		}
		if tr == nil || !tr.EarlyStop || tr.Rounds != 1 || tr.Winner != 0 || tr.Answer != "42" || tr.Calls != 10 || tr.Fallback != "" {
			t.Fatalf("trace %+v", tr)
		}
		if len(tr.Votes) != 1 || tr.Votes[0].Count != 3 || len(tr.Entries) != 10 {
			t.Fatalf("votes %+v, %d entries", tr.Votes, len(tr.Entries))
		}
		if got := h.sink.Text(); got != msg.PlainText() {
			t.Fatalf("the sink streams only the reply that won, got %q", got)
		}

		calls, err := h.store.Runs().LLMCalls(h.ctx, core.SystemViewer(), id)
		if err != nil || len(calls) != 10 {
			t.Fatalf("every refinement call is recorded: %d %v", len(calls), err)
		}
		ids := map[string]bool{}
		for _, c := range calls {
			ids[c.ID] = true
			if c.Purpose != "agent" || c.Step != 1 {
				t.Fatalf("call %+v", c)
			}
		}
		for _, e := range tr.Entries {
			if !ids[e.CallID] {
				t.Fatalf("entry %+v names no recorded call", e)
			}
		}
		if run.Usage.Input != 10*1000 {
			t.Fatalf("run usage %+v counts every call", run.Usage)
		}

		var phases []string
		for done := false; !done; {
			select {
			case e := <-sub.C():
				if d, ok := e.Data.(core.RefineData); ok && e.Run == id {
					phases = append(phases, d.Phase)
				}
			default:
				done = true
			}
		}
		if len(phases) != 12 || phases[0] != "start" || phases[11] != "done" {
			t.Fatalf("run.refine phases %v", phases)
		}
		for _, e := range h.sink.All() {
			if e.Type == core.EvRunRefine {
				t.Fatal("run.refine is for the bus only")
			}
		}

		// The trace never reaches a model.
		h.submitRefine("Thanks", false)
		synctest.Wait()
		next := h.requests()[10]
		for _, m := range next.Messages {
			for _, p := range m.Parts {
				if p.Type == core.PartRefine || strings.Contains(p.Text, `"entries"`) {
					t.Fatalf("refine trace sent to the model: %+v", p)
				}
			}
		}
		if !strings.Contains(allText(next), "Six times seven is 42.\nFinal answer: 42") {
			t.Fatal("the refined reply is part of the history")
		}
	})
}

func TestRefineNeverMultipliesToolSteps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		echo := &testTool{name: "echo"}
		h := newHarness(t, opts{tools: []core.Tool{echo}, cfg: refineCfg(2, 1, true), steps: []fakes.LLMStep{
			{Text: []string{"Let me ", "check."}, ToolCalls: []core.ToolCall{call("echo", `{"x":1}`)}, Usage: core.Usage{Input: 900, Output: 20}},
			say("It is 1.\nFinal answer: 1"),
			say("Final answer: 1"), // draft 1 agrees: no round needed
		}})
		defer h.close()
		id := h.submitRefine("Check it", true)
		synctest.Wait()
		run := h.run(id)
		if run.Status != core.RunDone || run.Steps != 2 || echo.calls.Load() != 1 {
			t.Fatalf("run %s, %d steps, %d tool runs", run.Status, run.Steps, echo.calls.Load())
		}
		reqs := h.requests()
		if len(reqs) != 3 || reqs[0].ToolChoice != "auto" || reqs[1].ToolChoice != "auto" || reqs[2].ToolChoice != "none" {
			t.Fatalf("calls %d: the tool step and the answer step run once, only the answer gets a draft", len(reqs))
		}
		_, tr := h.answer()
		if tr == nil || !tr.EarlyStop || tr.Rounds != 0 || tr.Calls != 2 {
			t.Fatalf("trace %+v", tr)
		}
		if got := h.sink.Text(); !strings.HasPrefix(got, "Let me check.") || !strings.HasSuffix(got, "It is 1.\nFinal answer: 1") {
			t.Fatalf("a tool step's text still streams: %q", got)
		}
	})
}

func TestRefineBudgetFallsBackToTheBestAnswerSoFar(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		big := func(s string) fakes.LLMStep {
			return fakes.LLMStep{Text: []string{s}, Usage: core.Usage{Input: 20_000, Output: 100}}
		}
		info := core.ModelInfo{Ref: core.ModelRef{Provider: "p", Model: "m"}, Context: 128_000, MaxOutput: 500, Tools: true}
		h := newHarness(t, opts{realBudget: true, info: info, cfg: refineCfg(3, 2, true), steps: []fakes.LLMStep{
			big("Maybe 4."), big("It is 5.\nFinal answer: 5"), big("Final answer: 5"),
			big("Maybe 4.\nFinal answer: 4"), // draft 0's continue; its critique would pass the token limit
		}})
		defer h.close()
		h.r.refineParallel = 1
		req := h.req("Count them")
		on := true
		req.Refine, req.Limits = &on, &core.RunLimits{MaxTokens: 70_000}
		res, err := h.r.Submit(h.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		run := h.run(res.Run)
		if run.Status != core.RunDone || len(h.notices()) != 0 {
			t.Fatalf("a budget stops refinement, not the run: %s (%s) %v", run.Status, run.Error, h.notices())
		}
		if n := len(h.requests()); n != 4 {
			t.Fatalf("%d calls, want 4", n)
		}
		msg, tr := h.answer()
		if tr.Fallback != refine.FallbackBudget || tr.Rounds != 0 || tr.Calls != 4 || tr.Winner != 1 {
			t.Fatalf("trace %+v", tr)
		}
		if msg.PlainText() != "It is 5.\nFinal answer: 5" {
			t.Fatalf("reply %q, want the vote's winner so far", msg.PlainText())
		}
	})
}

func TestRefineCriticSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{cfg: func(c *config.Config) {
			refineCfg(1, 1, true)(c)
			c.Agent.Refine.CriticSlot = "fast"
		}, steps: []fakes.LLMStep{
			say("A."), say("A.\nFinal answer: a"), say("Verdict: correct"),
		}})
		defer h.close()
		id := h.submitRefine("Letter?", true)
		synctest.Wait()
		_, tr := h.answer()
		calls, err := h.store.Runs().LLMCalls(h.ctx, core.SystemViewer(), id)
		if err != nil || len(calls) != 3 || tr == nil || tr.Calls != 3 {
			t.Fatalf("calls %d %v, trace %+v", len(calls), err, tr)
		}
		slot := map[string]core.Slot{}
		for _, c := range calls {
			slot[c.ID] = c.Slot
		}
		for _, e := range tr.Entries {
			want := core.SlotMain
			if e.Role == core.RefineCritique {
				want = core.SlotFast
			}
			if slot[e.CallID] != want {
				t.Fatalf("%s call ran on the %q slot, want %q", e.Role, slot[e.CallID], want)
			}
		}
	})
}

func TestRefineModeChoice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{})
		defer h.close()
		yes, no := true, false
		for _, tc := range []struct {
			name    string
			config  bool
			session string
			trigger core.Trigger
			req     *bool
			want    bool
		}{
			{"off by default", false, "", core.TrigMessage, nil, false},
			{"config default", true, "", core.TrigMessage, nil, true},
			{"session on", false, core.RefineOn, core.TrigMessage, nil, true},
			{"session off beats the default", true, core.RefineOff, core.TrigAPI, nil, false},
			{"the message beats the session", false, core.RefineOn, core.TrigMessage, &no, false},
			{"a message can ask", false, core.RefineOff, core.TrigMessage, &yes, true},
			{"schedules do not follow the default", true, core.RefineOn, core.TrigSchedule, nil, false},
			{"a schedule can ask", false, "", core.TrigSchedule, &yes, true},
		} {
			h.cfg.Agent.Refine.Enabled = tc.config
			key := core.RefineMetaKey(h.sess.ID)
			if tc.session == "" {
				_ = h.store.Meta().Delete(h.ctx, key)
			} else if err := h.store.Meta().Set(h.ctx, key, tc.session); err != nil {
				t.Fatal(err)
			}
			rs := &runState{req: core.RunRequest{Session: h.sess.ID, Trigger: tc.trigger, Refine: tc.req}, slot: core.SlotMain}
			if got := h.r.refineModeFor(h.ctx, rs) != nil; got != tc.want {
				t.Errorf("%s: refine = %v, want %v", tc.name, got, tc.want)
			}
		}
	})
}

func TestRefineSmallWindowAbridgesTheDraft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		info := core.ModelInfo{Ref: core.ModelRef{Provider: "p", Model: "small"}, Context: 16_384, MaxOutput: 2048, Tools: true}
		long := strings.Repeat("A long and careful line of reasoning. ", 1600) + "\nFinal answer: 9"
		h := newHarness(t, opts{info: info, cfg: refineCfg(1, 1, true), steps: []fakes.LLMStep{
			say(long), say("Shorter.\nFinal answer: 9"), say("Verdict: correct"),
		}})
		defer h.close()
		h.submitRefine("Think it through", true)
		synctest.Wait()
		reqs := h.requests()
		if len(reqs) != 3 {
			t.Fatalf("%d calls", len(reqs))
		}
		cfg := h.r.cfg()
		limit := info.Context - reserveFor(cfg, profileFor(cfg, info.Context), info.Context)
		for i, r := range reqs[1:] {
			if est := h.llm.Estimate(allText(r), info.Ref); est > limit {
				t.Fatalf("request %d estimates %d tokens, over the window's %d", i+1, est, limit)
			}
		}
		gen := lastUserText(reqs[1])
		if !strings.Contains(gen, abridgedNote) || !strings.Contains(gen, "Final answer: 9") {
			t.Fatal("a draft too long for the window keeps its head and its final answer line")
		}
		msg, tr := h.answer()
		if !tr.Entries[1].Abridged || msg.PlainText() != "Shorter.\nFinal answer: 9" {
			t.Fatalf("trace %+v reply %q", tr.Entries[1], msg.PlainText())
		}
	})
}

func TestRefineStopsForANewMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		blocked := say("Final answer: A")
		blocked.Block = gate
		h := newHarness(t, opts{cfg: refineCfg(2, 1, true), steps: []fakes.LLMStep{
			say("A."), blocked, // first answer and its draft
			say("B.\nFinal answer: B"), say("Final answer: B"), // the answer that reads the new message
		}})
		defer h.close()
		id := h.submitRefine("First", true)
		synctest.Wait()
		if res := h.submit("Also this"); !res.Steered {
			t.Fatalf("submit during refinement = %+v, want a steer", res)
		}
		close(gate)
		synctest.Wait()
		if run := h.run(id); run.Status != core.RunDone || len(h.requests()) != 4 {
			t.Fatalf("run %s after %d calls", run.Status, len(h.requests()))
		}
		var traces []*core.RefineTrace
		for _, m := range h.messages() {
			for _, p := range m.Parts {
				if p.Type == core.PartRefine {
					traces = append(traces, p.Refine)
				}
			}
		}
		if len(traces) != 2 || traces[0].Fallback != refine.FallbackNewMessage || !traces[1].EarlyStop {
			t.Fatalf("traces %+v", traces)
		}
		if msg, _ := h.answer(); msg.PlainText() != "B.\nFinal answer: B" {
			t.Fatalf("reply %q", msg.PlainText())
		}
	})
}

func TestRefineStopsBeforeTheTimeLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		maxTime time.Duration
		calls   int
		traced  int // a draft cut off before it was metered has no row to show
	}{
		{"no time left to start", 4 * time.Second, 1, 1},
		{"a draft outlasts the margin", time.Minute, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				slow := say("Final answer: 2")
				slow.Block = make(chan struct{}) // never closed: only the context ends it
				h := newHarness(t, opts{cfg: refineCfg(2, 1, true), steps: []fakes.LLMStep{say("It is 1.\nFinal answer: 1"), slow}})
				defer h.close()
				req := h.req("Quick")
				on := true
				req.Refine, req.Limits = &on, &core.RunLimits{MaxTime: tc.maxTime}
				res, err := h.r.Submit(h.ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				time.Sleep(2 * tc.maxTime)
				synctest.Wait()
				run := h.run(res.Run)
				if run.Status != core.RunDone || len(h.requests()) != tc.calls {
					t.Fatalf("run %s (%s) after %d calls", run.Status, run.Error, len(h.requests()))
				}
				msg, tr := h.answer()
				if tr.Fallback != refine.FallbackTime || tr.Calls != tc.traced || msg.PlainText() != "It is 1.\nFinal answer: 1" {
					t.Fatalf("trace %+v reply %q", tr, msg.PlainText())
				}
			})
		})
	}
}

func TestRefineParallelCallsAreMeteredOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Every call answers alike, so the order parallel calls take steps
		// in does not matter: each continued text states a final answer and
		// each critique confirms it.
		same := func() fakes.LLMStep { return say("Verdict: correct\nFinal answer: 7") }
		steps := make([]fakes.LLMStep, 12)
		for i := range steps {
			steps[i] = same()
		}
		info := core.ModelInfo{Ref: core.ModelRef{Provider: "p", Model: "m"}, Context: 128_000, MaxOutput: 500, Tools: true}
		h := newHarness(t, opts{realBudget: true, info: info, cfg: refineCfg(4, 1, false), steps: steps})
		defer h.close()
		id := h.submitRefine("Count", true)
		synctest.Wait()

		run := h.run(id)
		if run.Status != core.RunDone || run.Steps != 1 {
			t.Fatalf("run %s (%s), %d steps", run.Status, run.Error, run.Steps)
		}
		// 1 answer + 3 drafts + 4 × (continue, critique): confirmed, so no corrections.
		if n := len(h.requests()); n != 12 {
			t.Fatalf("%d calls, want 12", n)
		}
		calls, err := h.store.Runs().LLMCalls(h.ctx, core.SystemViewer(), id)
		if err != nil || len(calls) != 12 {
			t.Fatalf("recorded calls %d %v", len(calls), err)
		}
		ids := map[string]bool{}
		for _, c := range calls {
			ids[c.ID] = true
		}
		_, tr := h.answer()
		if tr == nil || tr.Calls != 12 || tr.Rounds != 1 || tr.Answer != "7" || tr.Votes[0].Count != 4 {
			t.Fatalf("trace %+v", tr)
		}
		for _, e := range tr.Entries {
			if !ids[e.CallID] {
				t.Fatalf("entry %+v names no recorded call", e)
			}
		}
		if run.Usage.Input != 12*1000 || run.Usage.Output != 12*50 {
			t.Fatalf("run usage %+v, want every call counted once", run.Usage)
		}
	})
}

func TestRefineTraceNamesOnlyRecordedCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{cfg: refineCfg(2, 1, true), steps: []fakes.LLMStep{
			say("A."),
			{Err: errors.New("overloaded")}, // draft 1 fails before any usage
			say("A.\nFinal answer: a"), say("Verdict: correct"),
		}})
		defer h.close()
		h.r.refineParallel = 1
		id := h.submitRefine("Letter?", true)
		synctest.Wait()
		calls, err := h.store.Runs().LLMCalls(h.ctx, core.SystemViewer(), id)
		if err != nil || len(calls) != 3 {
			t.Fatalf("recorded calls %d %v", len(calls), err)
		}
		ids := map[string]bool{}
		for _, c := range calls {
			ids[c.ID] = true
		}
		msg, tr := h.answer()
		if tr == nil || tr.Calls != 4 || msg.PlainText() != "A.\nFinal answer: a" {
			t.Fatalf("trace %+v reply %q", tr, msg.PlainText())
		}
		for _, e := range tr.Entries {
			switch {
			case e.Error != "" && e.CallID != "":
				t.Fatalf("a call that was never recorded names a row: %+v", e)
			case e.Error == "" && !ids[e.CallID]:
				t.Fatalf("entry %+v names no recorded call", e)
			}
		}
	})
}

func TestRefineMessageToggleIsNotFoldedIntoARunTheOtherWay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		blocked := say("Final answer: A")
		blocked.Block = gate
		h := newHarness(t, opts{cfg: refineCfg(2, 1, true), steps: []fakes.LLMStep{
			say("A."), blocked, // the first answer and its draft
			say("B.\nFinal answer: B"), say("Final answer: B"), // the steer's answer, refined like its run
			say("Plain."), // the message that asked for no refinement, in a run of its own
		}})
		defer h.close()
		first := h.submitRefine("First", true)
		synctest.Wait()

		off, on := false, true
		req := h.req("Quick one, no refining")
		req.Refine = &off
		res, err := h.r.Submit(h.ctx, req)
		if err != nil || res.Steered || !res.Queued {
			t.Fatalf("refine off during a refining run = %+v %v, want its own queued turn", res, err)
		}
		req = h.req("Also this")
		req.Refine = &on
		if res, err := h.r.Submit(h.ctx, req); err != nil || !res.Steered {
			t.Fatalf("refine on during a refining run = %+v %v, want a steer", res, err)
		}
		close(gate)
		synctest.Wait()

		runs := h.runs()
		if len(runs) != 2 || h.run(first).Status != core.RunDone || len(h.requests()) != 5 {
			t.Fatalf("%d runs, first %s, %d calls", len(runs), h.run(first).Status, len(h.requests()))
		}
		msg, tr := h.answer()
		if tr != nil || msg.PlainText() != "Plain." {
			t.Fatalf("the unrefined turn's reply %q has trace %+v", msg.PlainText(), tr)
		}
	})
}
