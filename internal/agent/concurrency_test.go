package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func ownerViewer(h *harness) core.Viewer {
	return core.Viewer{User: h.user.ID, Principals: []core.PrincipalID{h.pid}}
}

func TestStopWhileStreaming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"Partial", " answer"}, Block: block}}})
		defer h.close()
		res := h.submit("long question")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunRunning {
			t.Fatalf("status before stop = %s", st)
		}
		start := time.Now()
		ok, err := h.r.Stop(h.ctx, ownerViewer(h), h.sess.ID, h.user.ID)
		if elapsed := time.Since(start); err != nil || !ok || elapsed >= 100*time.Millisecond {
			t.Fatalf("Stop = %v, %v after %v", ok, err, elapsed)
		}
		if killed := h.sbx.KilledKeys(); len(killed) != 1 || killed[0] != boxKey(h.sess.ID) {
			t.Fatalf("killed = %v", killed)
		}
		synctest.Wait()
		run := h.run(res.Run)
		if run.Status != core.RunStopped || run.StoppedBy != h.user.ID {
			t.Fatalf("run = %s by %q", run.Status, run.StoppedBy)
		}
		if !slicesContains(h.notices(), "Stopped. 1 step, $0.0000") {
			t.Fatalf("notices = %v", h.notices())
		}
		ev, err := h.sink.Wait(h.ctx, core.EvRunFinished)
		if err != nil || ev.Data.(core.RunFinishedData).Status != core.RunStopped {
			t.Fatalf("run.finished = %+v %v", ev, err)
		}
		if !slicesContains(h.audit.Actions(), "run.stop") {
			t.Fatal("run.stop not audited")
		}
		close(block)
	})
}

func TestStopWhileToolSleeps(t *testing.T) {
	for _, tc := range []struct {
		name string
		call core.ToolCall
	}{{"gateway tool", call("sleep", `{}`)}, {"exec tool", call("shell", `{}`)}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { stopWhileToolSleeps(t, tc.call) })
		})
	}
}

func stopWhileToolSleeps(t *testing.T, tc core.ToolCall) {
	{
		sleeper := &testTool{name: "sleep", run: func(ctx context.Context, _ *core.ToolEnv, _ *core.Action) (*core.ToolResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Hour):
				return &core.ToolResult{Content: "woke"}, nil
			}
		}}
		shell := &testTool{name: "shell", se: core.SELocalWrite, exec: true, run: func(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
			res, err := env.Sandbox.Exec(ctx, env.BoxKey, core.ExecRequest{Argv: []string{"sleep", "600"}}, nil)
			if err != nil {
				return nil, err
			}
			return &core.ToolResult{Content: string(res.Stdout), IsError: res.Killed}, nil
		}}
		h := newHarness(t, opts{tools: []core.Tool{sleeper, shell}, steps: []fakes.LLMStep{{ToolCalls: []core.ToolCall{tc}}}})
		defer h.close()
		h.sbx.ExecFunc = func(ctx context.Context, _ string, _ core.ExecRequest) (*core.ExecResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		res := h.submit("sleep please")
		synctest.Wait()
		start := time.Now()
		if ok, err := h.r.Stop(h.ctx, ownerViewer(h), h.sess.ID, h.user.ID); !ok || err != nil || time.Since(start) >= 100*time.Millisecond {
			t.Fatalf("Stop = %v %v", ok, err)
		}
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunStopped {
			t.Fatalf("status = %s", st)
		}
		if len(h.sbx.KilledKeys()) != 1 {
			t.Fatalf("sandbox kill not called: %v", h.sbx.KilledKeys())
		}
		if recs := h.toolCalls(res.Run); len(recs) != 1 || recs[0].Status != core.TCStopped {
			t.Fatalf("tool calls = %+v", recs)
		}
	}
}

func TestConcurrentSubmitsOneActiveRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"first"}, Block: block}, {Text: []string{"second"}}, {Text: []string{"third"}}}})
		defer h.close()
		h.submit("start")
		synctest.Wait()
		var busy, ok int
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := h.r.Submit(h.ctx, h.req("more "+string(rune('A'+i%26))))
				mu.Lock()
				defer mu.Unlock()
				switch {
				case errors.Is(err, core.ErrBusy):
					busy++
				case err == nil:
					ok++
				default:
					t.Errorf("submit: %v", err)
				}
			}()
		}
		wg.Wait()
		if ok != 8 || busy != 42 {
			t.Fatalf("accepted %d, busy %d; want 8 and 42", ok, busy)
		}
		if active := h.r.Active(); len(active) != 1 {
			t.Fatalf("active runs = %d", len(active))
		}
		close(block)
		synctest.Wait()
		runs := h.runs()
		if len(runs) != 1 || runs[0].Status != core.RunDone {
			t.Fatalf("runs = %d (%v)", len(runs), runs)
		}
		reqs := h.requests()
		if len(reqs) != 2 {
			t.Fatalf("requests = %d", len(reqs))
		}
		var steers int
		for _, m := range h.messages() {
			if m.Kind == core.KindSteer {
				steers++
			}
		}
		if steers != 8 || strings.Count(allText(reqs[1]), "more ") != 8 {
			t.Fatalf("steers persisted %d; second request:\n%s", steers, allText(reqs[1]))
		}
	})
}

func TestSteerInjectedAtNextStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		echo := &testTool{name: "echo"}
		h := newHarness(t, opts{tools: []core.Tool{echo}, steps: []fakes.LLMStep{
			{Text: []string{"Looking."}, ToolCalls: []core.ToolCall{call("echo", `{}`)}, Block: block},
			{Text: []string{"Done with both."}},
		}})
		defer h.close()
		chat := &core.ChatRef{Channel: "telegram", ChatID: "5", Kind: core.ChatDM}
		h.sess = h.newSession("telegram", chat)
		req := h.req("find the file")
		req.Chat = chat
		res, err := h.r.Submit(h.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		steer := h.req("and check the date")
		steer.Chat = chat
		got, err := h.r.Submit(h.ctx, steer)
		if err != nil || !got.Steered || got.Run != res.Run {
			t.Fatalf("steer = %+v %v", got, err)
		}
		second := h.req("thanks")
		second.Chat = chat
		if got, _ := h.r.Submit(h.ctx, second); !got.Steered {
			t.Fatalf("second steer = %+v", got)
		}
		close(block)
		synctest.Wait()
		reqs := h.requests()
		if len(reqs) != 2 || !strings.Contains(allText(reqs[1]), "and check the date") || strings.Contains(allText(reqs[0]), "and check the date") {
			t.Fatalf("steer not injected at the next step")
		}
		checkPairs(t, reqs[1])
		if n := len(h.hub.Messages()); n != 1 || !strings.HasSuffix(h.hub.Messages()[0], "|"+steerReply) {
			t.Fatalf("hub sends = %v", h.hub.Messages())
		}
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("status = %s", st)
		}
	})
}

func TestQueueModeCoalescesIntoOneTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{cfg: func(c *config.Config) { c.Agent.BusyMode = "queue" },
			steps: []fakes.LLMStep{{Text: []string{"one"}, Block: block}, {Text: []string{"all three"}}}})
		defer h.close()
		h.submit("first")
		synctest.Wait()
		for _, text := range []string{"b", "c", "d"} {
			if res := h.submit(text); !res.Queued {
				t.Fatalf("%s not queued: %+v", text, res)
			}
		}
		close(block)
		synctest.Wait()
		runs := h.runs()
		if len(runs) != 2 {
			t.Fatalf("runs = %d", len(runs))
		}
		reqs := h.requests()
		last := reqs[1].Messages[len(reqs[1].Messages)-1]
		var texts []string
		for _, p := range last.Parts {
			texts = append(texts, p.Text)
		}
		if len(texts) != 4 || texts[1] != "b" || texts[2] != "c" || texts[3] != "d" {
			t.Fatalf("coalesced turn parts = %q", texts)
		}
	})
}

func TestDMCoalescingWindowMergesMessages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{cfg: func(c *config.Config) { c.Agent.CoalesceWindow = config.Duration(1200 * time.Millisecond) },
			steps: []fakes.LLMStep{{Text: []string{"got both"}}}})
		defer h.close()
		chat := &core.ChatRef{Channel: "telegram", ChatID: "7", Kind: core.ChatDM}
		h.sess = h.newSession("telegram", chat)
		a := h.req("hi")
		a.Chat = chat
		b := h.req("are you there?")
		b.Chat = chat
		ra, _ := h.r.Submit(h.ctx, a)
		time.Sleep(500 * time.Millisecond)
		rb, _ := h.r.Submit(h.ctx, b)
		if ra.Run != rb.Run {
			t.Fatalf("messages not merged: %+v %+v", ra, rb)
		}
		synctest.Wait()
		if len(h.requests()) != 0 {
			t.Fatal("run started inside the window")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		reqs := h.requests()
		if len(reqs) != 1 || !strings.Contains(lastUserText(reqs[0]), "hi\nare you there?") {
			t.Fatalf("requests = %d", len(reqs))
		}
	})
}

func TestApprovalWaitReleasesSemaphores(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := newHarness(t, opts{
			cfg:   func(c *config.Config) { c.Agent.MaxConcurrentRuns = 1 },
			tools: []core.Tool{act},
			pol:   &fakes.Policy{Verdicts: map[string]core.Verdict{"act": core.VerdictAsk}},
			appr:  &fakes.Approvals{Manual: true},
			steps: []fakes.LLMStep{
				{ToolCalls: []core.ToolCall{call("act", `{}`)}}, // run A asks
				{Text: []string{"B is done"}},                   // run B
				{Text: []string{"A is done"}},                   // run A after approval
			},
		})
		defer h.close()
		resA := h.submit("A")
		synctest.Wait()
		if st := h.run(resA.Run).Status; st != core.RunWaitingApproval {
			t.Fatalf("A = %s", st)
		}
		bob := &core.User{Username: "bob", Role: core.RoleMember, PWHash: "x", CreatedAt: time.Now()}
		if err := h.store.Users().Create(h.ctx, bob); err != nil {
			t.Fatal(err)
		}
		sb, err := h.r.NewSession(h.ctx, core.UserPrincipal(bob.ID), "web", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.r.mutateSession(h.ctx, sb.ID, func(s *core.Session) error { s.Title = "B"; return nil }); err != nil {
			t.Fatal(err)
		}
		resB, err := h.r.Submit(h.ctx, core.RunRequest{Session: sb.ID, Principal: core.UserPrincipal(bob.ID), User: bob.ID,
			Trigger: core.TrigMessage, Input: []core.Part{{Type: core.PartText, Text: "B"}}})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		runB, _ := h.store.Runs().Get(h.ctx, core.SystemViewer(), resB.Run)
		if runB.Status != core.RunDone {
			t.Fatalf("B blocked behind A's approval: %s", runB.Status)
		}
		if _, err := h.appr.Resolve(h.ctx, core.Resolution{ID: h.appr.All()[0].ID, Choice: core.AllowOnce, By: h.user.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(resA.Run).Status; st != core.RunDone || act.calls.Load() != 1 {
			t.Fatalf("A = %s, act calls %d", st, act.calls.Load())
		}
	})
}

func TestKillSwitch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"x"}, Block: block}}})
		defer h.close()
		res := h.submit("work")
		synctest.Wait()
		if err := h.r.SetPaused(h.ctx, true, h.user.ID); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunStopped {
			t.Fatalf("active run = %s", st)
		}
		if _, err := h.r.Submit(h.ctx, h.req("more")); !errors.Is(err, core.ErrPaused) {
			t.Fatalf("submit while paused = %v", err)
		}
		if v, _ := h.store.Meta().Get(h.ctx, metaPaused); v != "1" {
			t.Fatalf("meta.paused = %q", v)
		}
		banners := h.bus.Banners(core.Filter{Admin: true})
		if len(banners) != 1 || banners[0].Data.(core.BannerData).Code != "paused" {
			t.Fatalf("banners = %v", banners)
		}
		r2 := h.newRunner()
		defer r2.shutdown()
		if !r2.Paused() {
			t.Fatal("kill switch not restored from meta")
		}
		if err := h.r.SetPaused(h.ctx, false, h.user.ID); err != nil {
			t.Fatal(err)
		}
		if h.r.Paused() || len(h.bus.Banners(core.Filter{Admin: true})) != 0 {
			t.Fatal("kill switch did not release")
		}
		acts := h.audit.Actions()
		if !slicesContains(acts, "admin.pause") || !slicesContains(acts, "admin.resume") {
			t.Fatalf("audit = %v", acts)
		}
		close(block)
	})
}

func TestStopAllAndQueueClearing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{cfg: func(c *config.Config) { c.Agent.BusyMode = "queue" },
			steps: []fakes.LLMStep{{Text: []string{"x"}, Block: block}}})
		defer h.close()
		h.submit("one")
		synctest.Wait()
		if res := h.submit("queued"); !res.Queued {
			t.Fatal("not queued")
		}
		n, err := h.r.StopAll(h.ctx, h.user.ID)
		if err != nil || n != 1 {
			t.Fatalf("StopAll = %d %v", n, err)
		}
		synctest.Wait()
		if runs := h.runs(); len(runs) != 1 || runs[0].Status != core.RunStopped {
			t.Fatalf("runs = %v", runs)
		}
		if ok, _ := h.r.Stop(h.ctx, ownerViewer(h), h.sess.ID, h.user.ID); ok {
			t.Fatal("second stop reported a run")
		}
		if _, err := h.r.Stop(h.ctx, core.Viewer{User: "usr_other"}, h.sess.ID, "usr_other"); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("foreign stop = %v", err)
		}
		close(block)
	})
}

func TestShutdownInterruptsRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"x"}, Block: block}}})
		ctx, cancel := context.WithCancel(context.Background())
		if err := h.r.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		done := make(chan error)
		go func() { done <- h.r.Run(ctx) }()
		res := h.submit("work")
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if st := h.run(res.Run).Status; st != core.RunInterrupted {
			t.Fatalf("status after shutdown = %s", st)
		}
		if _, err := h.r.Submit(h.ctx, h.req("late")); !errors.Is(err, core.ErrPaused) {
			t.Fatalf("submit after shutdown = %v", err)
		}
		close(block)
	})
}

func TestShutdownCancelsTheApprovalOfAnInterruptedRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := newHarness(t, opts{tools: []core.Tool{act},
			pol:   &fakes.Policy{Verdicts: map[string]core.Verdict{"act": core.VerdictAsk}},
			appr:  &fakes.Approvals{Manual: true},
			steps: []fakes.LLMStep{{ToolCalls: []core.ToolCall{call("act", `{}`)}}}})
		ctx, cancel := context.WithCancel(context.Background())
		// Recovery precedes intake (as in cmd/bothy); otherwise Run's own
		// recovery could take the fresh run for a leftover.
		if err := h.r.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		done := make(chan error)
		go func() { done <- h.r.Run(ctx) }()
		res := h.submit("act")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunWaitingApproval {
			t.Fatalf("status = %s", st)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if st := h.run(res.Run).Status; st != core.RunInterrupted {
			t.Fatalf("status after shutdown = %s", st)
		}
		// Boot recovery only sees runs still open, so the card must be
		// cancelled now rather than stay approvable for a dead run.
		if st := h.appr.All()[0].Status; st != core.ApprovalCancelled {
			t.Fatalf("approval after shutdown = %s", st)
		}
		if !slicesContains(h.notices(), "I was restarted mid-task. Send 'retry' to continue.") {
			t.Fatalf("notices = %v", h.notices())
		}
	})
}

func TestRetiredWorkerForgetsSessionState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"hi"}}}})
		defer h.close()
		h.submit("hello")
		synctest.Wait()
		h.r.mu.Lock()
		before := len(h.r.prefix)
		h.r.mu.Unlock()
		if before != 1 {
			t.Fatalf("prefix marks after a run = %d", before)
		}
		synctestSleep(int((workerIdle + time.Second) / time.Second))
		h.r.mu.Lock()
		defer h.r.mu.Unlock()
		if len(h.r.workers) != 0 || len(h.r.prefix) != 0 {
			t.Fatalf("after idle retirement: %d workers, %d prefix marks", len(h.r.workers), len(h.r.prefix))
		}
	})
}

func TestCompactRefusedWhilePaused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{})
		defer h.close()
		if err := h.r.SetPaused(h.ctx, true, h.user.ID); err != nil {
			t.Fatal(err)
		}
		if err := h.r.Compact(h.ctx, ownerViewer(h), h.sess.ID); !errors.Is(err, core.ErrPaused) {
			t.Fatalf("Compact while paused = %v", err)
		}
	})
}
