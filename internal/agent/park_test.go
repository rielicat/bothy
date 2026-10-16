package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
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
	return parkRunAs(t, h, core.TrigSchedule)
}

func parkRunAs(t *testing.T, h *harness, trigger core.Trigger) (core.RunID, *core.Approval) {
	t.Helper()
	req := h.scheduledReq("nightly report")
	req.Trigger = trigger
	res, err := h.r.Submit(h.ctx, req)
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
		if !slicesContains(h.notices(), "Waiting for approval. It can be approved any time in the next 24 h.") {
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
		h.hub.PrincipalChats = map[core.PrincipalID]core.ChatRef{core.UserPrincipal(h.user.ID): {Channel: "telegram", ChatID: "9", Kind: core.ChatDM}}
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

// A resumed heartbeat run streams to a sink resumeSink builds, not to the
// scheduler's quiet sink, so the runner must hold back HEARTBEAT_OK itself.
func TestResumedHeartbeatSuppressesHeartbeatOK(t *testing.T) {
	for _, tc := range []struct {
		reply     string
		delivered bool
	}{
		{"HEARTBEAT_OK", false},
		{"**HEARTBEAT_OK**", false},
		{"The nightly backup failed.", true},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				act := &testTool{name: "act", se: core.SEExternalSend}
				h := parkHarness(t, act, fakes.LLMStep{Text: []string{tc.reply}})
				defer h.close()
				h.appr.OnResolved(func(a *core.Approval) { _ = h.r.Resume(context.Background(), a) })
				id, a := parkRunAs(t, h, core.TrigHeartbeat)
				chat := core.ChatRef{Channel: "telegram", ChatID: "9", Kind: core.ChatDM}
				h.hub.PrincipalChats = map[core.PrincipalID]core.ChatRef{core.UserPrincipal(h.user.ID): chat}
				if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if run := h.run(id); run.Status != core.RunDone {
					t.Fatalf("status = %s (%s)", run.Status, run.Error)
				}
				s, ok := h.hub.Stream(chat)
				if !ok {
					t.Fatal("the resumed run got no delivery sink")
				}
				got := hasType(s.Types(), core.EvRunFinished)
				if got != tc.delivered || tc.delivered && !strings.Contains(s.Text(), tc.reply) {
					t.Fatalf("reply %q delivered = %v (events %v, text %q), want %v", tc.reply, got, s.Types(), s.Text(), tc.delivered)
				}
			})
		})
	}
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
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.Deny, By: h.user.ID, Via: "web", Comment: "Not tonight"}); err != nil {
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
		if !strings.Contains(allText(reqs[len(reqs)-1]), "The user denied this action. Their reason: “Not tonight”") {
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
		if !slicesContains(h.notices(), "Nobody approved in time, so Bothy stopped this task.") {
			t.Fatalf("notices = %v", h.notices())
		}
		if act.calls.Load() != 0 {
			t.Fatal("expired action ran")
		}
	})
}

func TestCancelledParkedApprovalStopsRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act)
		defer h.close()
		id, a := parkRun(t, h)
		if err := h.appr.CancelRun(h.ctx, id); err != nil {
			t.Fatal(err)
		}
		cur, err := h.appr.Get(h.ctx, core.SystemViewer(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.r.Resume(context.Background(), cur); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(id).Status; st != core.RunStopped {
			t.Fatalf("status = %s", st)
		}
		if !slicesContains(h.notices(), "The request was cancelled, so Bothy stopped this task.") {
			t.Fatalf("notices = %v", h.notices())
		}
		if act.calls.Load() != 0 {
			t.Fatal("cancelled action ran")
		}
	})
}

func TestResumeReadsTheApprovalFromTheStore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var payloads []string
		act := &testTool{name: "act", se: core.SEExternalSend, run: func(_ context.Context, _ *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
			mu.Lock()
			defer mu.Unlock()
			payloads = append(payloads, string(a.Payload))
			return &core.ToolResult{Content: "sent"}, nil
		}}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"Report sent."}})
		defer h.close()
		id, a := parkRun(t, h)

		forged := *a // claims allowed while the store still says parked
		forged.Status, forged.Choice = core.ApprovalAllowed, core.AllowOnce
		if err := h.r.Resume(h.ctx, &forged); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if act.calls.Load() != 0 || h.run(id).Status != core.RunParked {
			t.Fatalf("forged resolution: act calls %d, run %s", act.calls.Load(), h.run(id).Status)
		}

		resolved, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"})
		if err != nil {
			t.Fatal(err)
		}
		swapped := *resolved.Action
		swapped.Payload = json.RawMessage(`{"to":"attacker"}`)
		signal := *resolved
		signal.Action = &swapped
		if err := h.r.Resume(h.ctx, &signal); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if act.calls.Load() != 1 || payloads[0] != `{"to":"x"}` {
			t.Fatalf("act calls %d with %v; want the stored payload once", act.calls.Load(), payloads)
		}
		if st := h.run(id).Status; st != core.RunDone {
			t.Fatalf("status = %s", st)
		}
	})
}

func TestResumeRefusesAnActionThatIsNotThePlannedCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"It did not run."}})
		defer h.close()
		h.appr.OnResolved(func(a *core.Approval) { _ = h.r.Resume(context.Background(), a) })
		id, a := parkRun(t, h)
		for _, rec := range h.toolCalls(id) {
			if rec.ID != a.ToolCallID {
				continue
			}
			other := *rec.Action
			other.Payload = json.RawMessage(`{"to":"y"}`)
			other.Fingerprint = core.ActionFingerprint(other.Tool, other.Payload)
			rec.Action = &other
			if err := h.store.Runs().UpdateToolCall(h.ctx, rec); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if act.calls.Load() != 0 {
			t.Fatal("an action that differs from the planned call ran")
		}
		reqs := h.requests()
		if !strings.Contains(allText(reqs[len(reqs)-1]), "The approved action does not match the planned call") {
			t.Fatal("the model was not told why the action did not run")
		}
	})
}

func TestKillSwitchStopsAQueuedResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"busy"}, Block: block})
		defer h.close()
		h.appr.OnResolved(func(a *core.Approval) { _ = h.r.Resume(context.Background(), a) })
		id, a := parkRun(t, h)
		h.submit("keep the session busy")
		synctest.Wait()
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// The kill switch engages while the resume waits behind the active
		// run (StopAll has not reached this session yet).
		h.r.paused.Store(true)
		close(block)
		synctest.Wait()
		if act.calls.Load() != 0 {
			t.Fatal("a resumed action ran while Bothy was paused")
		}
		if st := h.run(id).Status; st != core.RunStopped {
			t.Fatalf("status = %s", st)
		}
		if !slicesContains(h.notices(), pausedParkedNotice) {
			t.Fatalf("notices = %v", h.notices())
		}
	})
}

func TestStopFinishesAResumeNotYetHandedToTheWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := parkHarness(t, act, fakes.LLMStep{Text: []string{"should not happen"}})
		defer h.close()
		id, a := parkRun(t, h)
		resolved, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"})
		if err != nil {
			t.Fatal(err)
		}
		// Resume has flipped the row to queued but not queued the job yet.
		run := h.run(id)
		run.Status = core.RunQueued
		if err := h.store.Runs().Update(h.ctx, run); err != nil {
			t.Fatal(err)
		}
		if ok, err := h.r.Stop(h.ctx, ownerViewer(h), h.sess.ID, h.user.ID); err != nil || !ok {
			t.Fatalf("Stop = %v %v", ok, err)
		}
		w, err := h.r.worker(h.sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		w.mu.Lock()
		w.queue = append(w.queue, &resumeJob{run: run, approval: resolved})
		w.signal()
		w.mu.Unlock()
		synctest.Wait()
		if act.calls.Load() != 0 {
			t.Fatal("a stopped resume ran its action")
		}
		if st := h.run(id).Status; st != core.RunStopped {
			t.Fatalf("status = %s", st)
		}
	})
}

// appendHookStore calls onAppend after every message it stores outside a
// transaction.
type appendHookStore struct {
	core.Store
	onAppend func(m *core.Message)
}

func (s *appendHookStore) Messages() core.MessageRepo {
	return appendHookMessages{MessageRepo: s.Store.Messages(), on: s.onAppend}
}

type appendHookMessages struct {
	core.MessageRepo
	on func(m *core.Message)
}

func (m appendHookMessages) Append(ctx context.Context, msg *core.Message) error {
	err := m.MessageRepo.Append(ctx, msg)
	if err == nil && m.on != nil {
		m.on(msg)
	}
	return err
}

// TestResolutionWhileParkingResumes: the user approves in the window where
// the approval is already parked but the run row is not yet, so Resolve's
// Resume sees a run that is not parked and does nothing. The run must still
// resume now, not at some later restart.
func TestResolutionWhileParkingResumes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		var h *harness
		var once sync.Once
		store := &appendHookStore{Store: memstore.New()}
		store.onAppend = func(m *core.Message) {
			for _, p := range m.Parts {
				if p.ToolResult == nil || !strings.HasPrefix(p.ToolResult.Content, "Parked: waiting for approval") {
					continue
				}
				once.Do(func() {
					a := h.appr.All()[0]
					if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: a.ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
						t.Errorf("resolve while parking: %v", err)
					}
				})
			}
		}
		h = newHarness(t, opts{
			store: store,
			tools: []core.Tool{act, &testTool{name: "echo"}},
			steps: []fakes.LLMStep{
				{Text: []string{"Sending."}, ToolCalls: []core.ToolCall{call("act", `{"to":"x"}`), call("echo", `{}`)}},
				{Text: []string{"Report sent."}},
			},
			pol:  &fakes.Policy{Verdicts: map[string]core.Verdict{"act": core.VerdictAsk}},
			appr: &fakes.Approvals{Manual: true},
		})
		defer h.close()
		h.appr.OnResolved(func(a *core.Approval) { _ = h.r.Resume(context.Background(), a) })
		res, err := h.r.Submit(h.ctx, h.scheduledReq("nightly report"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := h.appr.Expire(h.appr.All()[0].ID); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		run := h.run(res.Run)
		if run.Status != core.RunDone || act.calls.Load() != 1 {
			t.Fatalf("run = %s, act ran %d times, approval %s", run.Status, act.calls.Load(), h.appr.All()[0].Status)
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
