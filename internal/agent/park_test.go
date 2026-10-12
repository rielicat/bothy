package agent

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
)

func (h *harness) scheduledReq(text string) core.RunRequest {
	req := h.req(text)
	req.Trigger = core.TrigSchedule
	return req
}

// parkRun starts a scheduled run whose only tool call asks and lets the
// approval time out into parking.
func parkRun(t *testing.T, h *harness) (core.RunID, *core.Approval) {
	t.Helper()
	res, err := h.r.Submit(h.ctx, h.scheduledReq("nightly report"))
	if err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	all := h.appr.All()
	if len(all) != 1 {
		t.Fatalf("approvals = %d", len(all))
	}
	if st := h.run(res.Run).Status; st != core.RunWaitingApproval {
		t.Fatalf("status while waiting = %s", st)
	}
	if err := h.appr.Expire(all[0].ID); err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	run := h.run(res.Run)
	if run.Status != core.RunParked || run.ParkedOn != all[0].ID {
		t.Fatalf("run = %s parked on %q", run.Status, run.ParkedOn)
	}
	a := h.appr.All()[0]
	return res.Run, &a
}

func parkHarness(t *testing.T, act *testTool, extra ...fakes.LLMStep) *harness {
	steps := append([]fakes.LLMStep{
		{Text: []string{"Sending."}, ToolCalls: []core.ToolCall{call("act", `{"to":"x"}`), call("echo", `{}`)}},
	}, extra...)
	return newHarness(t, opts{
		tools: []core.Tool{act, &testTool{name: "echo"}},
		steps: steps,
		pol:   &fakes.Policy{Verdicts: map[string]core.Verdict{"act": core.VerdictAsk}},
		appr:  &fakes.Approvals{Manual: true},
	})
}

func TestParkPlaceholderKeepsHistoryValid(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"Hi again."}})
		defer h.close()
		_, a := parkRun(t, h)
		var placeholder bool
		for _, m := range h.messages() {
			for _, p := range m.Parts {
				if p.ToolResult != nil && strings.HasPrefix(p.ToolResult.Content, "Parked: waiting for approval of Run act. It has not run.") {
					placeholder = true
				}
			}
		}
		if !placeholder {
			t.Fatal("no parked placeholder result")
		}
		if !slicesContains(h.notices(), "Parked. Approve any time in the next 24 h.") {
			t.Fatalf("notices = %v", h.notices())
		}
		if !hasType(h.sink.Types(), core.EvRunParked) || hasType(h.sink.Types(), core.EvRunFinished) {
			t.Fatalf("sink = %v", h.sink.Types())
		}
		if a.Status != core.ApprovalParked {
			t.Fatalf("approval = %s", a.Status)
		}
		// The session stays usable while the run is parked.
		res := h.submit("unrelated question")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("second run = %s", st)
		}
		reqs := h.requests()
		checkPairs(t, reqs[len(reqs)-1])
		if act.calls.Load() != 0 {
			t.Fatal("parked action ran")
		}
	})
}

func TestResumeExecutesStoredActionOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"Report sent."}})
		defer h.close()
		h.appr.OnResolved(func(a *core.Approval) { _ = h.r.Resume(context.Background(), a) })
		id, a := parkRun(t, h)
		h.hub.PreferredChats = map[core.UserID]core.ChatRef{h.user.ID: {Channel: "telegram", ChatID: "9", Kind: core.ChatDM}}
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		run := h.run(id)
		if run.Status != core.RunDone {
			t.Fatalf("status = %s (%s) notices %v", run.Status, run.Error, h.notices())
		}
		if act.calls.Load() != 1 {
			t.Fatalf("act ran %d times", act.calls.Load())
		}
		reqs := h.requests()
		last := reqs[len(reqs)-1]
		checkPairs(t, last)
		if !strings.Contains(allText(last), `<approved-action tool="act" call="call_1" status="ok">`) {
			t.Fatalf("approved-action message missing:\n%s", allText(last))
		}
		if _, ok := h.hub.Stream(core.ChatRef{Channel: "telegram", ChatID: "9", Kind: core.ChatDM}); !ok || h.hub.StreamModes["telegram:9:"] != "final" {
			t.Fatalf("scheduled resume should stream to the preferred chat in final mode: %v", h.hub.StreamModes)
		}
		// A second resolution signal is a no-op.
		final := h.appr.All()[0]
		if err := h.r.Resume(h.ctx, &final); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if act.calls.Load() != 1 {
			t.Fatalf("act ran again: %d", act.calls.Load())
		}
		var started []core.Trigger
		for _, ev := range h.busEvents(id) {
			if d, ok := ev.Data.(core.RunStartedData); ok {
				started = append(started, d.Trigger)
			}
		}
		if len(started) != 2 || started[1] != core.TrigResume || h.run(id).Trigger != core.TrigSchedule {
			t.Fatalf("run.started triggers = %v, run trigger %s", started, h.run(id).Trigger)
		}
	})
}

func TestParkNewRunnerResumesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"Done after restart."}})
		defer h.close()
		id, a := parkRun(t, h)

		// A second runner on the same database (a restarted gateway).
		r2 := h.newRunner()
		defer r2.shutdown()
		h.appr.OnResolved(func(a *core.Approval) { _ = r2.Resume(context.Background(), a) })
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		final := h.appr.All()[0]
		_ = h.r.Resume(h.ctx, &final) // the old runner hears about it too
		synctest.Wait()
		if act.calls.Load() != 1 {
			t.Fatalf("act ran %d times", act.calls.Load())
		}
		if st := h.run(id).Status; st != core.RunDone {
			t.Fatalf("status = %s", st)
		}
	})
}

func TestResumeDeniedContinuesTheLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"Understood, not sending."}})
		defer h.close()
		h.appr.OnResolved(func(a *core.Approval) { _ = h.r.Resume(context.Background(), a) })
		id, a := parkRun(t, h)
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.Deny, By: h.user.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(id).Status; st != core.RunDone {
			t.Fatalf("status = %s", st)
		}
		if act.calls.Load() != 0 {
			t.Fatal("denied action ran")
		}
		reqs := h.requests()
		if !strings.Contains(allText(reqs[len(reqs)-1]), "The user denied this action.") {
			t.Fatal("denial not recorded for the model")
		}
	})
}

func TestExpiredParkedApprovalStopsRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act)
		defer h.close()
		h.appr.OnResolved(func(a *core.Approval) { _ = h.r.Resume(context.Background(), a) })
		id, a := parkRun(t, h)
		if err := h.appr.Expire(a.ID); err != nil { // park TTL passes
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(id).Status; st != core.RunStopped {
			t.Fatalf("status = %s", st)
		}
		if !slicesContains(h.notices(), "The parked action was not approved in time, so the run stopped.") {
			t.Fatalf("notices = %v", h.notices())
		}
		if act.calls.Load() != 0 {
			t.Fatal("expired action ran")
		}
	})
}

func hasType(types []core.EventType, want core.EventType) bool {
	for _, x := range types {
		if x == want {
			return true
		}
	}
	return false
}

// busEvents returns the ring events of one run.
func (h *harness) busEvents(run core.RunID) []core.Event {
	evs, _ := h.bus.Since(0, core.Filter{System: true})
	var out []core.Event
	for _, ev := range evs {
		if ev.Run == run {
			out = append(out, ev)
		}
	}
	return out
}
