package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func (f *fx) schedule(p core.PrincipalID, next time.Time, enabled bool) *core.Schedule {
	f.t.Helper()
	n := f.next()
	s := &core.Schedule{
		Principal: p, Name: fmt.Sprintf("schedule %d", n), Kind: core.SchedCron, Spec: "0 9 * * *", TZ: "UTC",
		Prompt: "summarize", Slot: core.SlotMain, Limits: core.RunLimits{MaxSteps: 15}, MissedRun: "skip",
		Enabled: enabled, CreatedBy: "system", NextRunAt: next, CreatedAt: at(n), UpdatedAt: at(n),
	}
	f.must(f.s.Schedules().Upsert(f.ctx, s), "Upsert schedule")
	return s
}

func runIDs(rs []*core.Run) []core.RunID {
	out := make([]core.RunID, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func testRuns(f *fx) {
	r := f.s.Runs()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)
	s := f.session(pu)
	s2 := f.session(pu)
	sch := f.schedule(pu, at(500), true)

	run := &core.Run{
		Session: s.ID, Principal: pu, User: u.ID, Trigger: core.TrigSchedule, Schedule: sch.ID, Status: core.RunRunning,
		Steps: 3, ToolCalls: 2, Usage: core.Usage{Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5}, Cost: 1234,
		Models: []string{"anthropic/claude-sonnet"}, Limits: core.RunLimits{MaxSteps: 15, MaxTokens: 150_000, MaxTime: 15 * time.Minute, MaxCost: 250_000},
		CreatedAt: at(1), StartedAt: at(2),
	}
	f.must(r.Create(f.ctx, run), "Create")
	f.check(core.ValidID(core.PrefixRun, string(run.ID)), "Create assigned id %q, want a run_ id", run.ID)
	got, err := r.Get(f.ctx, v, run.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, run)
	_, err = r.Get(f.ctx, viewer(other), run.ID)
	f.want(err, core.ErrNotFound, "Get with a foreign viewer")
	_, err = r.Get(f.ctx, v, core.RunID(core.NewID(core.PrefixRun)))
	f.want(err, core.ErrNotFound, "Get missing")

	run.Status = core.RunBudgetExceeded
	run.Error = "run cost budget"
	run.Steps, run.ToolCalls, run.Cost = 7, 4, 250_001
	run.Usage = core.Usage{Input: 900, Output: 300, CacheRead: 40, CacheWrite: 20}
	run.Models = []string{"anthropic/claude-sonnet", "openai/gpt-5"}
	run.ParkedOn = "apr_01aaaaaaaaaaaaaaaaaaaaaaaa"
	run.StoppedBy = u.ID
	run.FinishedAt = at(9)
	run.Refined = true
	f.must(r.Update(f.ctx, run), "Update")
	got, err = r.Get(f.ctx, v, run.ID)
	f.must(err, "Get after Update")
	f.eq("Update round trip", got, run)
	f.want(r.Update(f.ctx, newRun(s, core.RunDone, at(3))), core.ErrNotFound, "Update missing")

	dup := newRun(s, core.RunDone, at(3))
	dup.ID = run.ID
	f.want(r.Create(f.ctx, dup), core.ErrConflict, "duplicate id")
	bad := func(mod func(*core.Run)) *core.Run {
		x := newRun(s2, core.RunDone, at(3))
		mod(x)
		return x
	}
	f.want(r.Create(f.ctx, bad(func(x *core.Run) { x.Trigger = "cron" })), core.ErrInvalid, "trigger CHECK")
	f.want(r.Create(f.ctx, bad(func(x *core.Run) { x.Status = "paused" })), core.ErrInvalid, "status CHECK")
	f.want(r.Create(f.ctx, bad(func(x *core.Run) { x.Session = core.SessionID(core.NewID(core.PrefixSession)) })), core.ErrInvalid, "session_id foreign key")
	f.want(r.Create(f.ctx, bad(func(x *core.Run) { x.Principal = core.PrincipalID(core.NewID(core.PrefixSpace)) })), core.ErrInvalid, "principal_id foreign key")
	f.want(r.Create(f.ctx, bad(func(x *core.Run) { x.Schedule = core.ScheduleID(core.NewID(core.PrefixSchedule)) })), core.ErrInvalid, "schedule_id foreign key")

	mk := func(s *core.Session, status core.RunStatus, sec int, mod func(*core.Run)) *core.Run {
		x := newRun(s, status, at(sec))
		if mod != nil {
			mod(x)
		}
		f.must(r.Create(f.ctx, x), "Create")
		return x
	}
	a := mk(s, core.RunDone, 10, nil)
	b := mk(s2, core.RunFailed, 11, nil)
	c := mk(s2, core.RunDone, 12, func(x *core.Run) { x.Schedule, x.Trigger = sch.ID, core.TrigSchedule })
	d := mk(s, core.RunQueued, 13, nil)
	f.must(r.Create(f.ctx, newRun(f.session(core.UserPrincipal(other.ID)), core.RunDone, at(14))), "Create foreign run")

	list := func(flt core.RunFilter) []core.RunID {
		f.t.Helper()
		rs, err := r.List(f.ctx, v, flt)
		f.must(err, "List")
		return runIDs(rs)
	}
	f.eq("List newest first", list(core.RunFilter{}), []core.RunID{d.ID, c.ID, b.ID, a.ID, run.ID})
	f.eq("List by session", list(core.RunFilter{Session: s2.ID}), []core.RunID{c.ID, b.ID})
	f.eq("List by schedule", list(core.RunFilter{Schedule: sch.ID}), []core.RunID{c.ID, run.ID})
	f.eq("List by status", list(core.RunFilter{Status: core.RunDone}), []core.RunID{c.ID, a.ID})
	f.eq("List first page", list(core.RunFilter{Page: core.Page{Limit: 2}}), []core.RunID{d.ID, c.ID})
	f.eq("List next page", list(core.RunFilter{Page: core.Page{Before: c.CreatedAt.UnixMilli(), Limit: 2}}), []core.RunID{b.ID, a.ID})
	rs, err := r.List(f.ctx, viewer(other), core.RunFilter{Session: s.ID})
	f.must(err, "List foreign")
	f.check(len(rs) == 0, "List with a foreign viewer returned %d runs", len(rs))
}

func testRunsOneActive(f *fx) {
	r := f.s.Runs()
	u := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	s := f.session(pu)

	a := f.run(s, core.RunRunning)
	f.want(r.Create(f.ctx, newRun(s, core.RunRunning, at(50))), core.ErrConflict, "a second running run")
	f.want(r.Create(f.ctx, newRun(s, core.RunWaitingApproval, at(50))), core.ErrConflict, "a waiting_approval run beside a running one")
	b := f.run(s, core.RunQueued)
	c := f.run(s, core.RunParked)

	b.Status = core.RunRunning
	f.want(r.Update(f.ctx, b), core.ErrConflict, "Update to running beside a running run")
	got, err := r.Get(f.ctx, viewer(u), b.ID)
	f.must(err, "Get")
	f.check(got.Status == core.RunQueued, "a rejected Update changed status to %s", got.Status)

	a.Status = core.RunDone
	f.must(r.Update(f.ctx, a), "finish the active run")
	f.must(r.Update(f.ctx, b), "the next run may become running")
	c.Status = core.RunWaitingApproval
	f.want(r.Update(f.ctx, c), core.ErrConflict, "a resumed run waiting beside a running one")
	f.must(r.Create(f.ctx, newRun(f.session(pu), core.RunRunning, at(60))), "running runs of different sessions")

	s3 := f.session(pu)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Go(func() { errs[i] = r.Create(f.ctx, newRun(s3, core.RunRunning, at(70+i))) })
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, core.ErrConflict):
			f.t.Errorf("concurrent Create: %v", err)
		}
	}
	f.check(won == 1, "%d concurrent running runs were created for one session, want 1", won)
}

func testMarkInterrupted(f *fx) {
	r := f.s.Runs()
	u := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)
	q := f.run(f.session(pu), core.RunQueued)
	ru := f.run(f.session(pu), core.RunRunning)
	w := f.run(f.session(pu), core.RunWaitingApproval)
	p := f.run(f.session(pu), core.RunParked)
	d := f.run(f.session(pu), core.RunDone)
	now := at(500)

	got, err := r.MarkInterrupted(f.ctx, now)
	f.must(err, "MarkInterrupted")
	for _, x := range []*core.Run{q, ru, w} {
		x.Status, x.FinishedAt = core.RunInterrupted, now
	}
	byID := func(x *core.Run) core.RunID { return x.ID }
	f.eq("MarkInterrupted returns the changed rows", sortBy(got, byID), sortBy([]*core.Run{q, ru, w}, byID))
	for _, x := range []*core.Run{q, ru, w, p, d} {
		stored, err := r.Get(f.ctx, v, x.ID)
		f.must(err, "Get")
		f.eq("stored run after MarkInterrupted", stored, x)
	}
	got, err = r.MarkInterrupted(f.ctx, now)
	f.must(err, "MarkInterrupted again")
	f.check(len(got) == 0, "a second MarkInterrupted changed %d runs", len(got))
	parked, err := r.Parked(f.ctx)
	f.must(err, "Parked")
	f.eq("Parked", parked, []*core.Run{p})
}

func testLLMCalls(f *fx) {
	r := f.s.Runs()
	u := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)
	run := f.run(f.session(pu), core.RunRunning)

	ref := core.ModelRef{Provider: "anthropic", Model: "claude-sonnet"}
	c2 := core.CallInfo{
		Ref: ref, Slot: core.SlotMain, Purpose: "agent", Step: 2,
		Attempts:       []core.Attempt{{Ref: core.ModelRef{Provider: "openai", Model: "gpt-5"}, Err: "429", Kind: core.ErrKindRateLimit, Latency: 250 * time.Millisecond}, {Ref: ref, Latency: 900 * time.Millisecond}},
		Usage:          core.Usage{Input: 100, Output: 20, CacheRead: 1000, CacheWrite: 50, Reasoning: 7},
		UsageEstimated: true, Cost: 4321, PriceKnown: true, TTFT: 120 * time.Millisecond, Latency: 900 * time.Millisecond,
		Breakdown:  core.Breakdown{Constitution: 900, Tools: 1500, History: 200, Total: 2600, Window: 200_000, Reserve: 8192, Cacheable: 2400},
		PrefixHash: "0123456789abcdef", Stop: core.StopTool,
	}
	c1 := core.CallInfo{ID: core.NewID(core.PrefixLLMCall), Ref: ref, Slot: core.SlotFast, Purpose: "compaction", Step: 1, Stop: core.StopEnd, Error: "overloaded"}
	f.must(r.AddLLMCall(f.ctx, run.ID, pu, u.ID, c2), "AddLLMCall")
	f.must(r.AddLLMCall(f.ctx, run.ID, pu, u.ID, c1), "AddLLMCall with an explicit id")
	got, err := r.LLMCalls(f.ctx, v, run.ID)
	f.must(err, "LLMCalls")
	if len(got) == 2 {
		f.check(core.ValidID(core.PrefixLLMCall, got[1].ID), "AddLLMCall assigned id %q, want an llm_ id", got[1].ID)
		c2.ID = got[1].ID
	}
	f.eq("LLMCalls in step order", got, []core.CallInfo{c1, c2})

	f.want(r.AddLLMCall(f.ctx, run.ID, pu, u.ID, c1), core.ErrConflict, "duplicate id")
	f.must(r.AddLLMCall(f.ctx, "", "", u.ID, core.CallInfo{Ref: ref, Purpose: "probe"}), "a call outside runs")
	f.want(r.AddLLMCall(f.ctx, core.RunID(core.NewID(core.PrefixRun)), pu, u.ID, core.CallInfo{Ref: ref, Purpose: "agent"}), core.ErrInvalid, "run_id foreign key")
	f.want(r.AddLLMCall(f.ctx, run.ID, pu, u.ID, core.CallInfo{Ref: ref, Purpose: "chat"}), core.ErrInvalid, "purpose CHECK")
	_, err = r.LLMCalls(f.ctx, viewer(f.user(core.RoleMember)), run.ID)
	f.want(err, core.ErrNotFound, "LLMCalls with a foreign viewer")
	_, err = r.LLMCalls(f.ctx, v, core.RunID(core.NewID(core.PrefixRun)))
	f.want(err, core.ErrNotFound, "LLMCalls of a missing run")
}

func testToolCalls(f *fx) {
	r := f.s.Runs()
	u := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)
	s := f.session(pu)
	run := f.run(s, core.RunRunning)

	payload := json.RawMessage(`{"argv":["bash","-lc","ls -la"],"cwd":"."}`)
	tc := &core.ToolCallRecord{
		Run: run.ID, Session: s.ID, Principal: pu, Step: 2, CallID: "call_1", Tool: "shell", Source: "builtin",
		SideEffect: core.SELocalWrite,
		Action: &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: core.SELocalWrite, Argv: []string{"bash", "-lc", "ls -la"}, Cwd: ".",
			Payload: payload, Fingerprint: core.ActionFingerprint("shell", payload)},
		Verdict: core.VerdictAuto, Rule: "default:local_write", Status: core.TCPlanned, CreatedAt: at(1),
	}
	f.must(r.AddToolCall(f.ctx, tc), "AddToolCall")
	f.check(core.ValidID(core.PrefixToolCall, tc.ID), "AddToolCall assigned id %q, want a tcl_ id", tc.ID)
	tc0 := &core.ToolCallRecord{Run: run.ID, Session: s.ID, Principal: pu, Step: 1, CallID: "call_0", Tool: "nope", Source: "builtin",
		SideEffect: core.SERead, Status: core.TCInvalid, ResultPreview: "Unknown tool nope. Use tool_search.", CreatedAt: at(1)}
	f.must(r.AddToolCall(f.ctx, tc0), "AddToolCall without action or verdict")
	got, err := r.ToolCalls(f.ctx, v, run.ID)
	f.must(err, "ToolCalls")
	f.eq("ToolCalls in step order", got, []*core.ToolCallRecord{tc0, tc})

	tc.Status, tc.Verdict, tc.Approval = core.TCOK, core.VerdictAsk, "apr_01aaaaaaaaaaaaaaaaaaaaaaaa"
	tc.ResultPreview, tc.ResultBytes, tc.FullRef, tc.Untrusted = "total 0", 9000, ".bothy/out/x.txt", true
	tc.Duration, tc.FinishedAt = 1500*time.Millisecond, at(3)
	f.must(r.UpdateToolCall(f.ctx, tc), "UpdateToolCall")
	got, err = r.ToolCalls(f.ctx, v, run.ID)
	f.must(err, "ToolCalls after Update")
	f.eq("UpdateToolCall round trip", got, []*core.ToolCallRecord{tc0, tc})
	missing := *tc
	missing.ID = core.NewID(core.PrefixToolCall)
	f.want(r.UpdateToolCall(f.ctx, &missing), core.ErrNotFound, "UpdateToolCall missing")

	bad := func(mod func(*core.ToolCallRecord)) *core.ToolCallRecord {
		x := *tc0
		x.ID = ""
		mod(&x)
		return &x
	}
	f.want(r.AddToolCall(f.ctx, bad(func(x *core.ToolCallRecord) { x.ID = tc.ID })), core.ErrConflict, "duplicate id")
	f.want(r.AddToolCall(f.ctx, bad(func(x *core.ToolCallRecord) { x.Verdict = "maybe" })), core.ErrInvalid, "verdict CHECK")
	f.want(r.AddToolCall(f.ctx, bad(func(x *core.ToolCallRecord) { x.Status = "done" })), core.ErrInvalid, "status CHECK")
	f.want(r.AddToolCall(f.ctx, bad(func(x *core.ToolCallRecord) { x.SideEffect = 0 })), core.ErrInvalid, "side_effect NOT NULL")
	f.want(r.AddToolCall(f.ctx, bad(func(x *core.ToolCallRecord) { x.Run = core.RunID(core.NewID(core.PrefixRun)) })), core.ErrInvalid, "run_id foreign key")
	_, err = r.ToolCalls(f.ctx, viewer(f.user(core.RoleMember)), run.ID)
	f.want(err, core.ErrNotFound, "ToolCalls with a foreign viewer")
}

func approvalIDs(as []*core.Approval) []core.ApprovalID {
	out := make([]core.ApprovalID, len(as))
	for i, a := range as {
		out[i] = a.ID
	}
	return out
}

func testApprovals(f *fx) {
	r := f.s.Approvals()
	u := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)
	run := f.run(f.session(pu), core.RunWaitingApproval)
	run2 := f.run(f.session(pu), core.RunWaitingApproval)

	a := f.approval(run, core.ApprovalPending, at(10))
	f.check(core.ValidID(core.PrefixApproval, string(a.ID)), "Create assigned id %q, want an apr_ id", a.ID)
	got, err := r.Get(f.ctx, v, a.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, a)
	_, err = r.Get(f.ctx, v, core.ApprovalID(core.NewID(core.PrefixApproval)))
	f.want(err, core.ErrNotFound, "Get missing")

	bad := func(mod func(*core.Approval)) *core.Approval {
		x := newApproval(run, core.ApprovalPending, at(11))
		mod(x)
		return x
	}
	f.want(r.Create(f.ctx, bad(func(x *core.Approval) { x.ID = a.ID })), core.ErrConflict, "duplicate id")
	f.want(r.Create(f.ctx, bad(func(x *core.Approval) { x.Status = "open" })), core.ErrInvalid, "status CHECK")
	f.want(r.Create(f.ctx, bad(func(x *core.Approval) { x.OnTimeout = "" })), core.ErrInvalid, "on_timeout CHECK")
	f.want(r.Create(f.ctx, bad(func(x *core.Approval) { x.Run = core.RunID(core.NewID(core.PrefixRun)) })), core.ErrInvalid, "run_id foreign key")

	pending, err := r.Pending(f.ctx, v)
	f.must(err, "Pending")
	f.eq("Pending", approvalIDs(pending), []core.ApprovalID{a.ID})
	f.must(r.Park(f.ctx, a.ID, at(100)), "Park")
	f.must(r.Park(f.ctx, a.ID, at(200)), "Park a parked approval moves park_until")
	got, err = r.Get(f.ctx, v, a.ID)
	f.must(err, "Get after Park")
	f.check(got.Status == core.ApprovalParked && got.ParkUntil.Equal(at(200)), "after Park: status %s, park_until %v", got.Status, got.ParkUntil)
	pending, err = r.Pending(f.ctx, v)
	f.must(err, "Pending")
	f.eq("Pending includes parked", approvalIDs(pending), []core.ApprovalID{a.ID})

	n, err := r.CountOpen(f.ctx, run.Principal)
	f.must(err, "CountOpen")
	f.check(n == 1, "CountOpen with one parked approval = %d", n)
	ok, err := r.Decide(f.ctx, a.ID, []core.ApprovalStatus{core.ApprovalPending, core.ApprovalParked}, core.ApprovalAllowed, core.AllowOnce, core.Decider{User: u.ID}, "web", at(150))
	f.must(err, "Decide")
	f.check(ok, "Decide from parked returned false")
	got, err = r.Get(f.ctx, v, a.ID)
	f.must(err, "Get after Decide")
	a.Status, a.Choice, a.DecidedBy, a.DecidedVia, a.DecidedAt, a.ParkUntil = core.ApprovalAllowed, core.AllowOnce, u.ID, "web", at(150), at(200)
	f.eq("Decide round trip", got, a)
	f.want(r.Park(f.ctx, a.ID, at(300)), core.ErrConflict, "Park a decided approval")
	got, err = r.Get(f.ctx, v, a.ID)
	f.must(err, "Get")
	f.check(got.Status == core.ApprovalAllowed, "a rejected Park changed status to %s", got.Status)
	f.want(r.Park(f.ctx, core.ApprovalID(core.NewID(core.PrefixApproval)), at(300)), core.ErrNotFound, "Park missing")
	n, err = r.CountOpen(f.ctx, run.Principal)
	f.must(err, "CountOpen")
	f.check(n == 0, "CountOpen after the decision = %d", n)

	b := f.approval(run, core.ApprovalDenied, at(20))
	c := f.approval(run2, core.ApprovalExpired, at(30))
	d := f.approval(run2, core.ApprovalCancelled, at(40))
	recent := func(p core.Page) []core.ApprovalID {
		f.t.Helper()
		as, err := r.Recent(f.ctx, v, p)
		f.must(err, "Recent")
		return approvalIDs(as)
	}
	f.eq("Recent newest first", recent(core.Page{}), []core.ApprovalID{d.ID, c.ID, b.ID, a.ID})
	f.eq("Recent first page", recent(core.Page{Limit: 2}), []core.ApprovalID{d.ID, c.ID})
	f.eq("Recent next page", recent(core.Page{Before: c.CreatedAt.UnixMilli()}), []core.ApprovalID{b.ID, a.ID})

	e := newApproval(run, core.ApprovalPending, at(45))
	e.ExpiresAt = at(50)
	f.must(r.Create(f.ctx, e), "Create")
	pk := newApproval(run2, core.ApprovalParked, at(46))
	pk.ParkUntil = at(60)
	f.must(r.Create(f.ctx, pk), "Create")
	later := newApproval(run2, core.ApprovalPending, at(47))
	later.ExpiresAt = at(1000)
	f.must(r.Create(f.ctx, later), "Create")
	due, err := r.Due(f.ctx, at(70))
	f.must(err, "Due")
	byID := func(x *core.Approval) core.ApprovalID { return x.ID }
	f.eq("Due: pending past expires_at and parked past park_until", approvalIDs(sortBy(due, byID)), approvalIDs(sortBy([]*core.Approval{e, pk}, byID)))
	byRun, err := r.ByRun(f.ctx, run.ID)
	f.must(err, "ByRun")
	f.eq("ByRun", approvalIDs(sortBy(byRun, byID)), approvalIDs(sortBy([]*core.Approval{a, b, e}, byID)))

	notice := core.CardMessage{Ref: core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "-100", Kind: core.ChatGroup}, ID: "m0"}, Truncated: true,
		Kind: core.CardMsgNotice}
	link := core.CardMessage{Ref: core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "43", Kind: core.ChatDM}, ID: "m3"}, Truncated: true,
		Kind: core.CardMsgLink}
	m1 := core.CardMessage{Ref: core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM}, ID: "m1"}}
	m2 := core.CardMessage{Ref: core.MsgRef{Chat: core.ChatRef{Channel: "slack", ChatID: "D1", ThreadID: "171.2", Kind: core.ChatDM}, ID: "m2"}, Truncated: true,
		Kind: core.CardMsgCard}
	f.must(r.AddMessage(f.ctx, a.ID, notice), "AddMessage notice")
	f.must(r.AddMessage(f.ctx, a.ID, link), "AddMessage link")
	f.must(r.AddMessage(f.ctx, a.ID, m1), "AddMessage")
	f.must(r.AddMessage(f.ctx, a.ID, m2), "AddMessage")
	f.want(r.AddMessage(f.ctx, a.ID, m1), core.ErrConflict, "duplicate card message")
	f.want(r.AddMessage(f.ctx, core.ApprovalID(core.NewID(core.PrefixApproval)), m1), core.ErrInvalid, "approval_id foreign key")
	f.want(r.AddMessage(f.ctx, a.ID, core.CardMessage{Ref: core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "44"}, ID: "m4"}, Kind: "banner"}),
		core.ErrInvalid, "approval_messages.kind CHECK")
	msgs, err := r.Messages(f.ctx, a.ID)
	f.must(err, "Messages")
	m1.Kind = core.CardMsgCard
	for _, m := range []*core.CardMessage{&notice, &link, &m1, &m2} {
		m.Ref.Chat.Kind = ""
	}
	for i := range msgs {
		msgs[i].Ref.Chat.Kind = ""
	}
	f.eq("Messages lists full cards, truncated cards, link-only cards, then waiting lines (chat kind has no column)",
		msgs, []core.CardMessage{m1, m2, link, notice})
	msgs, err = r.Messages(f.ctx, b.ID)
	f.must(err, "Messages of an approval without cards")
	f.check(len(msgs) == 0, "Messages returned %d cards for another approval", len(msgs))
}

func testApprovalVisibility(f *fx) {
	r := f.s.Approvals()
	f.user(core.RoleOwner)
	alice := f.user(core.RoleMember)
	bob := f.user(core.RoleAdmin)
	run := f.run(f.session(core.UserPrincipal(alice.ID)), core.RunWaitingApproval)

	plain := f.approval(run, core.ApprovalPending, at(1))
	adminOnly := newApproval(run, core.ApprovalPending, at(2))
	adminOnly.Approver = core.ApproverReq{AdminOnly: true}
	adminOnly.Decision.Approver = adminOnly.Approver
	f.must(r.Create(f.ctx, adminOnly), "Create admin-only approval")
	decided := newApproval(run, core.ApprovalAllowed, at(3))
	decided.Approver = core.ApproverReq{AdminOnly: true}
	f.must(r.Create(f.ctx, decided), "Create decided admin-only approval")

	va, vb := viewer(alice), viewer(bob)
	vbAdmin := viewer(bob)
	vbAdmin.Admin = true
	for _, c := range []struct {
		name string
		v    core.Viewer
		a    *core.Approval
		ok   bool
	}{
		{"owner principal, plain", va, plain, true},
		{"owner principal, admin-only", va, adminOnly, true},
		{"foreign non-admin, plain", vb, plain, false},
		{"foreign non-admin, admin-only", vb, adminOnly, false},
		{"foreign admin, admin-only", vbAdmin, adminOnly, true},
		{"foreign admin, plain", vbAdmin, plain, false},
	} {
		_, err := r.Get(f.ctx, c.v, c.a.ID)
		if c.ok {
			f.must(err, "Get: "+c.name)
		} else {
			f.want(err, core.ErrNotFound, "Get: "+c.name)
		}
	}
	pending := func(v core.Viewer) []core.ApprovalID {
		f.t.Helper()
		as, err := r.Pending(f.ctx, v)
		f.must(err, "Pending")
		return approvalIDs(sortBy(as, func(x *core.Approval) core.ApprovalID { return x.ID }))
	}
	f.eq("Pending for an admin", pending(vbAdmin), []core.ApprovalID{adminOnly.ID})
	f.eq("Pending for a non-admin", pending(vb), []core.ApprovalID{})
	f.eq("Pending for the principal", pending(va), approvalIDs(sortBy([]*core.Approval{plain, adminOnly}, func(x *core.Approval) core.ApprovalID { return x.ID })))
	rec, err := r.Recent(f.ctx, vbAdmin, core.Page{})
	f.must(err, "Recent")
	f.eq("Recent for an admin", approvalIDs(rec), []core.ApprovalID{decided.ID})
	rec, err = r.Recent(f.ctx, vb, core.Page{})
	f.must(err, "Recent")
	f.check(len(rec) == 0, "Recent for a non-admin returned %d approvals", len(rec))

	// A channel's or teammate's request goes to the team's approvers: every
	// admin sees it, members don't, whoever started the run.
	ch := f.space()
	teamRun := f.run(f.session(ch.Principal), core.RunWaitingApproval)
	team := newApproval(teamRun, core.ApprovalPending, at(4))
	team.Approver = core.ApproverReq{Team: true, RequesterIdentity: "telegram:42"}
	team.Decision.Approver = team.Approver
	f.must(r.Create(f.ctx, team), "Create team approval")
	got, err := r.Get(f.ctx, vbAdmin, team.ID)
	f.must(err, "an admin sees a team approval")
	f.check(got.Approver.Team && got.Approver.RequesterIdentity == "telegram:42", "team approver round trip = %+v", got.Approver)
	_, err = r.Get(f.ctx, vb, team.ID)
	f.want(err, core.ErrNotFound, "a member doesn't see a team approval")
	f.eq("Pending for an admin includes team approvals", pending(vbAdmin), approvalIDs(sortBy([]*core.Approval{adminOnly, team},
		func(x *core.Approval) core.ApprovalID { return x.ID })))
	f.eq("Pending for a member leaves team approvals out", pending(va), approvalIDs(sortBy([]*core.Approval{plain, adminOnly},
		func(x *core.Approval) core.ApprovalID { return x.ID })))

	ok, err := r.Decide(f.ctx, team.ID, []core.ApprovalStatus{core.ApprovalPending}, core.ApprovalAllowed, core.AllowOnce,
		core.Decider{Identity: "slack:U01SAM"}, "slack", at(5))
	f.must(err, "Decide by a chat teammate")
	f.check(ok, "Decide by a chat teammate returned false")
	got, err = r.Get(f.ctx, core.SystemViewer(), team.ID)
	f.must(err, "Get after a chat decision")
	f.check(got.DecidedBy == "" && got.DecidedByIdentity == "slack:U01SAM" && got.DecidedVia == "slack",
		"a chat decider reads back as DecidedByIdentity only: by %q identity %q via %q", got.DecidedBy, got.DecidedByIdentity, got.DecidedVia)
}

func testApprovalDecideCAS(f *fx) {
	r := f.s.Approvals()
	u := f.user(core.RoleMember)
	run := f.run(f.session(core.UserPrincipal(u.ID)), core.RunWaitingApproval)
	a := f.approval(run, core.ApprovalPending, at(1))
	from := []core.ApprovalStatus{core.ApprovalPending, core.ApprovalParked}

	const n = 50
	wins := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		to, c := core.ApprovalAllowed, core.AllowOnce
		if i%2 == 1 {
			to, c = core.ApprovalDenied, core.Deny
		}
		wg.Go(func() {
			wins[i], errs[i] = r.Decide(f.ctx, a.ID, from, to, c, core.Decider{User: u.ID}, fmt.Sprintf("web-%d", i), at(10+i))
		})
	}
	wg.Wait()
	winner := -1
	for i := range n {
		f.must(errs[i], "concurrent Decide")
		if wins[i] {
			f.check(winner < 0, "deciders %d and %d both won", winner, i)
			winner = i
		}
	}
	if winner < 0 {
		f.t.Fatal("no concurrent Decide won")
	}
	got, err := r.Get(f.ctx, viewer(u), a.ID)
	f.must(err, "Get")
	wantStatus := core.ApprovalAllowed
	if winner%2 == 1 {
		wantStatus = core.ApprovalDenied
	}
	f.check(got.Status == wantStatus && got.DecidedVia == fmt.Sprintf("web-%d", winner) && got.DecidedAt.Equal(at(10+winner)),
		"stored decision %s via %s at %v, want the winner's (%s via web-%d)", got.Status, got.DecidedVia, got.DecidedAt, wantStatus, winner)

	ok, err := r.Decide(f.ctx, a.ID, from, core.ApprovalExpired, "", core.Decider{}, "timeout", at(99))
	f.must(err, "Decide after resolution")
	f.check(!ok, "Decide on a resolved approval won")
	ok, err = r.Decide(f.ctx, core.ApprovalID(core.NewID(core.PrefixApproval)), from, core.ApprovalAllowed, core.AllowOnce, core.Decider{User: u.ID}, "web", at(99))
	f.want(err, core.ErrNotFound, "Decide missing")
	f.check(!ok, "Decide on a missing approval won")
	other := f.approval(run, core.ApprovalPending, at(2))
	_, err = r.Decide(f.ctx, other.ID, from, "approved", core.AllowOnce, core.Decider{User: u.ID}, "web", at(99))
	f.want(err, core.ErrInvalid, "Decide to an invalid status")
}

func testApprovalSeen(f *fx) {
	r := f.s.Approvals()
	alice := f.user(core.RoleMember)
	bob := f.user(core.RoleMember)
	pa := core.UserPrincipal(alice.ID)
	run := f.run(f.session(pa), core.RunWaitingApproval)
	bobRun := f.run(f.session(core.UserPrincipal(bob.ID)), core.RunWaitingApproval)

	mk := func(r *core.Run, status core.ApprovalStatus, action *core.Action) {
		a := newApproval(r, status, at(f.next()))
		a.Action = action
		f.must(f.s.Approvals().Create(f.ctx, a), "Create")
	}
	httpAction := &core.Action{Tool: "http_request", Kind: core.ActHTTP, SideEffect: core.SEExternalSend, Method: "POST",
		URL: "https://api.example.com/v1", Host: "api.example.com", Port: 443, Payload: json.RawMessage(`{}`)}
	mk(run, core.ApprovalAllowed, sendAction("telegram:7:", ""))
	mk(run, core.ApprovalAllowed, httpAction)
	mk(run, core.ApprovalDenied, sendAction("telegram:8:", ""))
	mk(run, core.ApprovalPending, sendAction("telegram:9:", ""))
	mk(run, core.ApprovalAllowed, nil)
	mk(bobRun, core.ApprovalAllowed, sendAction("telegram:10:", "bob.example"))

	for _, c := range []struct {
		p               core.PrincipalID
		recipient, host string
		want            bool
	}{
		{pa, "telegram:7:", "", true},
		{pa, "", "api.example.com", true},
		{pa, "telegram:7:", "other.example", true},
		{pa, "telegram:8:", "", false},
		{pa, "telegram:9:", "", false},
		{pa, "", "", false},
		{pa, "telegram:10:", "bob.example", false},
		{core.UserPrincipal(bob.ID), "telegram:7:", "", false},
		{core.UserPrincipal(bob.ID), "", "bob.example", true},
	} {
		got, err := r.Seen(f.ctx, c.p, c.recipient, c.host)
		f.must(err, "Seen")
		f.check(got == c.want, "Seen(%s, %q, %q) = %t, want %t", c.p, c.recipient, c.host, got, c.want)
	}
}

func grantIDs(gs []*core.Grant) []core.GrantID {
	out := make([]core.GrantID, len(gs))
	for i, g := range sortBy(gs, func(g *core.Grant) core.GrantID { return g.ID }) {
		out[i] = g.ID
	}
	return out
}

func testGrants(f *fx) {
	r := f.s.Grants()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)
	s := f.session(pu)
	sch := f.schedule(pu, at(100), true)

	g := &core.Grant{Principal: pu, Scope: core.GrantSession, ScopeID: string(s.ID), Kind: core.GrantTool, Tool: "shell",
		Pattern: map[string]string{"/argv/0": "ls", "/argv/1": "-la"}, MaxSE: core.SELocalWrite, CreatedBy: u.ID, CreatedAt: at(1), ExpiresAt: at(9000)}
	f.must(r.Add(f.ctx, g), "Add")
	f.check(core.ValidID(core.PrefixGrant, string(g.ID)), "Add assigned id %q, want a grt_ id", g.ID)
	got, err := r.List(f.ctx, v, core.GrantSession, string(s.ID))
	f.must(err, "List")
	f.eq("List round trip", got, []*core.Grant{g})

	bad := func(mod func(*core.Grant)) *core.Grant {
		x := *g
		x.ID = ""
		mod(&x)
		return &x
	}
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.ID = g.ID })), core.ErrConflict, "duplicate id")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Pattern = nil })), core.ErrInvalid, "whole-tool grant (nil pattern)")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Pattern = map[string]string{} })), core.ErrInvalid, "whole-tool grant (empty pattern)")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Tool = "" })), core.ErrInvalid, "tool grant without a tool")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Scope = core.GrantPrincipal })), core.ErrInvalid, "principal scope with a scope_id")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.ScopeID = "" })), core.ErrInvalid, "session scope without a scope_id")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Scope, x.ScopeID = core.GrantSchedule, "" })), core.ErrInvalid, "schedule scope without a scope_id")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Scope = "global" })), core.ErrInvalid, "scope CHECK")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Kind = "file" })), core.ErrInvalid, "kind CHECK")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Kind, x.Value, x.AllowWhenTainted = core.GrantDomain, "example.com:443", true })), core.ErrInvalid, "allow_when_tainted needs kind recipient")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.MaxSE = 0 })), core.ErrInvalid, "max_se NOT NULL")
	f.want(r.Add(f.ctx, bad(func(x *core.Grant) { x.Principal = core.PrincipalID(core.NewID(core.PrefixSpace)) })), core.ErrInvalid, "principal_id foreign key")

	rcpt := &core.Grant{Principal: pu, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "telegram:42:", MaxSE: core.SEExternalSend,
		AllowWhenTainted: true, CreatedBy: u.ID, CreatedAt: at(2)}
	f.must(r.Add(f.ctx, rcpt), "Add an allow_when_tainted recipient grant")
	dom := &core.Grant{Principal: pu, Scope: core.GrantSchedule, ScopeID: string(sch.ID), Kind: core.GrantDomain, Value: "api.example.com:443",
		MaxSE: core.SEExternalSend, MaxUses: 2, CreatedBy: u.ID, CreatedAt: at(3)}
	f.must(r.Add(f.ctx, dom), "Add a schedule domain grant")

	list := func(v core.Viewer, scope core.GrantScope, id string) []core.GrantID {
		f.t.Helper()
		gs, err := r.List(f.ctx, v, scope, id)
		f.must(err, "List")
		return grantIDs(gs)
	}
	f.eq("List all", list(v, "", ""), grantIDs([]*core.Grant{g, rcpt, dom}))
	f.eq("List principal scope", list(v, core.GrantPrincipal, ""), []core.GrantID{rcpt.ID})
	f.eq("List schedule scope", list(v, core.GrantSchedule, string(sch.ID)), []core.GrantID{dom.ID})
	f.eq("List with a foreign viewer", list(viewer(other), "", ""), []core.GrantID{})

	f.must(r.Use(f.ctx, dom.ID), "Use")
	f.must(r.Use(f.ctx, dom.ID), "Use up to max_uses")
	f.want(r.Use(f.ctx, dom.ID), core.ErrConflict, "Use past max_uses")
	gs, err := r.List(f.ctx, v, core.GrantSchedule, string(sch.ID))
	f.must(err, "List")
	if len(gs) == 1 {
		f.check(gs[0].Uses == 2, "Uses = %d after two Use calls", gs[0].Uses)
	}
	f.want(r.Use(f.ctx, core.GrantID(core.NewID(core.PrefixGrant))), core.ErrNotFound, "Use missing")

	f.want(r.Revoke(f.ctx, viewer(other), g.ID, at(5)), core.ErrNotFound, "Revoke with a foreign viewer")
	f.eq("a foreign Revoke changes nothing", list(v, "", ""), grantIDs([]*core.Grant{g, rcpt, dom}))
	f.must(r.Revoke(f.ctx, v, g.ID, at(5)), "Revoke")
	f.eq("List omits revoked grants", list(v, "", ""), grantIDs([]*core.Grant{rcpt, dom}))
	f.want(r.Revoke(f.ctx, v, g.ID, at(6)), core.ErrNotFound, "Revoke twice")
	f.want(r.Use(f.ctx, g.ID), core.ErrConflict, "Use a revoked grant")
	f.want(r.Revoke(f.ctx, v, core.GrantID(core.NewID(core.PrefixGrant)), at(6)), core.ErrNotFound, "Revoke missing")

	person, _ := f.teammate("slack", "U77")
	chat := &core.Grant{Principal: person.Principal, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "slack:C1:",
		MaxSE: core.SEExternalSend, CreatedByIdentity: "slack:U01SAM", CreatedAt: at(7)}
	f.must(r.Add(f.ctx, chat), "Add a grant a chat approver created")
	got, err = r.List(f.ctx, core.Viewer{Principals: []core.PrincipalID{person.Principal}}, "", "")
	f.must(err, "List")
	f.eq("a chat approver's grant reads back with CreatedByIdentity only", got, []*core.Grant{chat})
}

func testGrantsActive(f *fx) {
	r := f.s.Grants()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	s1, s2 := f.session(pu), f.session(pu)
	sch1, sch2 := f.schedule(pu, at(100), true), f.schedule(pu, at(100), true)
	now := at(1000)

	add := func(p core.PrincipalID, scope core.GrantScope, id string, mod func(*core.Grant)) *core.Grant {
		n := f.next()
		g := &core.Grant{Principal: p, Scope: scope, ScopeID: id, Kind: core.GrantDomain, Value: fmt.Sprintf("h%d.example:443", n),
			MaxSE: core.SEExternalSend, CreatedBy: u.ID, CreatedAt: at(n)}
		if mod != nil {
			mod(g)
		}
		f.must(r.Add(f.ctx, g), "Add")
		return g
	}
	gp := add(pu, core.GrantPrincipal, "", nil)
	gs1 := add(pu, core.GrantSession, string(s1.ID), nil)
	gs2 := add(pu, core.GrantSession, string(s2.ID), nil)
	gsc1 := add(pu, core.GrantSchedule, string(sch1.ID), nil)
	add(pu, core.GrantSchedule, string(sch2.ID), nil)
	add(pu, core.GrantSession, string(s1.ID), func(g *core.Grant) { g.ExpiresAt = at(999) })
	soon := add(pu, core.GrantSession, string(s1.ID), func(g *core.Grant) { g.ExpiresAt = at(1001) })
	revoked := add(pu, core.GrantSession, string(s1.ID), nil)
	f.must(r.Revoke(f.ctx, viewer(u), revoked.ID, at(500)), "Revoke")
	used := add(pu, core.GrantSession, string(s1.ID), func(g *core.Grant) { g.MaxUses = 1 })
	f.must(r.Use(f.ctx, used.ID), "Use")
	foreign := add(core.UserPrincipal(other.ID), core.GrantPrincipal, "", nil)

	active := func(p core.PrincipalID, s core.SessionID, sch core.ScheduleID, now time.Time) []core.GrantID {
		f.t.Helper()
		gs, err := r.Active(f.ctx, p, s, sch, now)
		f.must(err, "Active")
		return grantIDs(gs)
	}
	f.eq("Active for a session", active(pu, s1.ID, "", now), grantIDs([]*core.Grant{gp, gs1, soon}))
	f.eq("Active for another session", active(pu, s2.ID, "", now), grantIDs([]*core.Grant{gp, gs2}))
	f.eq("Active for a schedule", active(pu, "", sch1.ID, now), grantIDs([]*core.Grant{gp, gsc1}))
	f.eq("Active for a session and a schedule", active(pu, s1.ID, sch1.ID, now), grantIDs([]*core.Grant{gp, gs1, soon, gsc1}))
	f.eq("Active with neither", active(pu, "", "", now), grantIDs([]*core.Grant{gp}))
	f.eq("Active after expiry", active(pu, s1.ID, "", at(1002)), grantIDs([]*core.Grant{gp, gs1}))
	f.eq("Active of another principal", active(core.UserPrincipal(other.ID), s1.ID, "", now), grantIDs([]*core.Grant{foreign}))
}

func testSchedules(f *fx) {
	r := f.s.Schedules()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)
	s := f.session(pu)

	sc := &core.Schedule{
		Principal: pu, Name: "daily digest", Kind: core.SchedCron, Spec: "0 9 * * 1-5", TZ: "Europe/London", ActiveHours: "08:00-22:00",
		Prompt: "Summarize my inbox", Session: s.ID, Deliver: &core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM},
		Slot: core.SlotFast, Limits: core.RunLimits{MaxSteps: 15, MaxTokens: 150_000, MaxTime: 15 * time.Minute, MaxCost: 250_000},
		MissedRun: "once", Enabled: true, CreatedBy: "user:" + string(u.ID), Failures: 1, NextRunAt: at(100), LastRunAt: at(50),
		LastStatus: core.RunDone, CreatedAt: at(1), UpdatedAt: at(2),
	}
	f.must(r.Upsert(f.ctx, sc), "Upsert new")
	f.check(core.ValidID(core.PrefixSchedule, string(sc.ID)), "Upsert assigned id %q, want a sch_ id", sc.ID)
	got, err := r.Get(f.ctx, v, sc.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, sc)
	_, err = r.Get(f.ctx, viewer(other), sc.ID)
	f.want(err, core.ErrNotFound, "Get with a foreign viewer")

	sc.Name, sc.Deliver, sc.Session, sc.Kind, sc.Spec, sc.UpdatedAt = "renamed", nil, "", core.SchedHeartbeat, "30m", at(3)
	f.must(r.Upsert(f.ctx, sc), "Upsert existing")
	got, err = r.Get(f.ctx, v, sc.ID)
	f.must(err, "Get after update")
	f.eq("Upsert update round trip", got, sc)
	list, err := r.List(f.ctx, v)
	f.must(err, "List")
	f.eq("List", list, []*core.Schedule{sc})
	list, err = r.List(f.ctx, viewer(other))
	f.must(err, "List foreign")
	f.check(len(list) == 0, "List with a foreign viewer returned %d schedules", len(list))

	bad := func(mod func(*core.Schedule)) *core.Schedule {
		x := *sc
		x.ID = ""
		mod(&x)
		return &x
	}
	f.want(r.Upsert(f.ctx, bad(func(x *core.Schedule) { x.Kind = "weekly" })), core.ErrInvalid, "kind CHECK")
	f.want(r.Upsert(f.ctx, bad(func(x *core.Schedule) { x.MissedRun = "" })), core.ErrInvalid, "missed_run CHECK")
	f.want(r.Upsert(f.ctx, bad(func(x *core.Schedule) { x.Principal = core.PrincipalID(core.NewID(core.PrefixSpace)) })), core.ErrInvalid, "principal_id foreign key")
	f.want(r.Upsert(f.ctx, bad(func(x *core.Schedule) { x.Session = core.SessionID(core.NewID(core.PrefixSession)) })), core.ErrInvalid, "session_id foreign key")

	sc2 := f.schedule(pu, at(80), true)
	f.schedule(pu, at(10), false)
	next, err := r.NextDue(f.ctx)
	f.must(err, "NextDue")
	f.check(next.ID == sc2.ID, "NextDue = %s, want the earliest enabled %s", next.ID, sc2.ID)
	f.must(r.SetRun(f.ctx, sc2.ID, at(80), at(200), core.RunFailed, 2, true), "SetRun")
	got, err = r.Get(f.ctx, v, sc2.ID)
	f.must(err, "Get after SetRun")
	sc2.LastRunAt, sc2.NextRunAt, sc2.LastStatus, sc2.Failures = at(80), at(200), core.RunFailed, 2
	f.eq("SetRun round trip", got, sc2)
	next, err = r.NextDue(f.ctx)
	f.must(err, "NextDue")
	f.check(next.ID == sc.ID, "NextDue after SetRun = %s, want %s", next.ID, sc.ID)
	f.must(r.SetRun(f.ctx, sc.ID, at(100), at(300), core.RunFailed, 3, false), "SetRun disabling")
	f.must(r.SetRun(f.ctx, sc2.ID, at(80), at(200), core.RunFailed, 3, false), "SetRun disabling")
	_, err = r.NextDue(f.ctx)
	f.want(err, core.ErrNotFound, "NextDue without enabled schedules")
	f.want(r.SetRun(f.ctx, core.ScheduleID(core.NewID(core.PrefixSchedule)), at(1), at(2), core.RunDone, 0, true), core.ErrNotFound, "SetRun missing")

	run := newRun(s, core.RunDone, at(20))
	run.Trigger, run.Schedule = core.TrigSchedule, sc.ID
	f.must(f.s.Runs().Create(f.ctx, run), "Create schedule run")
	f.want(r.Delete(f.ctx, viewer(other), sc.ID), core.ErrNotFound, "Delete with a foreign viewer")
	_, err = r.Get(f.ctx, v, sc.ID)
	f.must(err, "a foreign Delete removed the schedule")
	f.must(r.Delete(f.ctx, v, sc.ID), "Delete")
	_, err = r.Get(f.ctx, v, sc.ID)
	f.want(err, core.ErrNotFound, "Get after Delete")
	f.want(r.Delete(f.ctx, v, sc.ID), core.ErrNotFound, "Delete twice")
	gotRun, err := f.s.Runs().Get(f.ctx, v, run.ID)
	f.must(err, "Get run")
	f.check(gotRun.Schedule == "", "runs.schedule_id is ON DELETE SET NULL, got %s", gotRun.Schedule)

	ch := f.space()
	f.schedule(ch.Principal, at(400), true)
	f.schedule(ch.Principal, at(500), true)
	off := f.schedule(core.UserPrincipal(other.ID), at(600), false)
	ps, err := r.PrincipalsWithEnabled(f.ctx)
	f.must(err, "PrincipalsWithEnabled")
	f.check(slices.Contains(ps, ch.Principal) && !slices.Contains(ps, off.Principal) && len(ps) == len(slices.Compact(slices.Clone(ps))),
		"PrincipalsWithEnabled = %v, want %s once and not %s", ps, ch.Principal, off.Principal)
}

func testUsage(f *fx) {
	r := f.s.Usage()
	const day = "2026-05-28"
	f.must(r.AddSpend(f.ctx, "usr_a", day, core.CallInfo{Cost: 100, Usage: core.Usage{Input: 10, Output: 5}}), "AddSpend")
	f.must(r.AddSpend(f.ctx, "usr_a", day, core.CallInfo{Cost: 50}), "AddSpend")
	f.must(r.AddSpend(f.ctx, "usr_b", day, core.CallInfo{Cost: 25}), "AddSpend")
	f.must(r.AddSpend(f.ctx, "usr_a", "2026-05-29", core.CallInfo{Cost: 7}), "AddSpend")
	f.must(r.AddSpend(f.ctx, "spc_eng", day, core.CallInfo{Cost: 40}), "AddSpend for a channel")
	f.must(r.AddSpend(f.ctx, "spc_maya", day, core.CallInfo{Cost: 2}), "AddSpend for a teammate")
	f.must(r.AddSpend(f.ctx, "spc_maya", "2026-05-29", core.CallInfo{Cost: 9}), "AddSpend for a teammate")
	for _, c := range []struct {
		p    core.PrincipalID
		day  string
		want core.Micros
	}{{"usr_a", day, 150}, {"usr_b", day, 25}, {"", day, 217}, {"usr_a", "2026-05-29", 7}, {"usr_c", day, 0}, {"spc_maya", day, 2}} {
		got, err := r.Spend(f.ctx, c.p, c.day)
		f.must(err, "Spend")
		f.check(got == c.want, "Spend(%q, %s) = %d, want %d", c.p, c.day, got, c.want)
	}
	team, err := r.TeamSpend(f.ctx, day)
	f.must(err, "TeamSpend")
	f.check(team == 42, "TeamSpend = %d, want 42 (team-owned principals only)", team)

	alice := f.user(core.RoleMember)
	bob := f.user(core.RoleMember)
	pa := core.UserPrincipal(alice.ID)
	s1, s2 := f.session(pa), f.session(pa)
	sch := f.schedule(pa, at(100), true)
	r1 := newRun(s1, core.RunDone, at(1))
	r1.Trigger, r1.Schedule = core.TrigSchedule, sch.ID
	f.must(f.s.Runs().Create(f.ctx, r1), "Create run")
	r2 := f.run(s2, core.RunDone)
	anthropic := core.ModelRef{Provider: "anthropic", Model: "a"}
	call := func(run core.RunID, p core.PrincipalID, u core.UserID, ref core.ModelRef, purpose string, cost core.Micros, known bool, usage core.Usage) {
		f.t.Helper()
		f.must(f.s.Runs().AddLLMCall(f.ctx, run, p, u, core.CallInfo{Ref: ref, Purpose: purpose, Cost: cost, PriceKnown: known, Usage: usage}), "AddLLMCall")
	}
	call(r1.ID, pa, alice.ID, anthropic, "agent", 100, true, core.Usage{Input: 10, Output: 5, CacheRead: 1, CacheWrite: 2})
	call(r1.ID, pa, alice.ID, core.ModelRef{Provider: "openai", Model: "b"}, "agent", 200, false, core.Usage{Input: 20, Output: 10})
	call(r2.ID, pa, alice.ID, anthropic, "title", 300, true, core.Usage{Input: 30, Output: 15, CacheRead: 3, CacheWrite: 4})
	call("", "", bob.ID, core.ModelRef{Provider: "ollama", Model: "c"}, "passthrough", 0, true, core.Usage{Input: 1, Output: 1})
	ch := f.space()
	call("", ch.Principal, "", anthropic, "notes", 5, true, core.Usage{Input: 2, Output: 2})

	report := func(q core.UsageQuery) []*core.UsageRow {
		f.t.Helper()
		rows, err := r.Report(f.ctx, q)
		f.must(err, "Report")
		return sortBy(ptrSlice(rows), func(x *core.UsageRow) string { return x.Key })
	}
	f.eq("Report by model", report(core.UsageQuery{GroupBy: "model"}), []*core.UsageRow{
		{Key: "anthropic/a", Cost: 405, Input: 42, Output: 22, CacheRead: 4, CacheWrite: 6, Calls: 3, PriceKnown: true},
		{Key: "ollama/c", Cost: 0, Input: 1, Output: 1, Calls: 1, PriceKnown: true},
		{Key: "openai/b", Cost: 200, Input: 20, Output: 10, Calls: 1, PriceKnown: false},
	})
	f.eq("Report by user falls back to the principal of calls no account paid for", report(core.UsageQuery{GroupBy: "user"}), sortBy([]*core.UsageRow{
		{Key: string(alice.ID), Cost: 600, Input: 60, Output: 30, CacheRead: 4, CacheWrite: 6, Calls: 3, PriceKnown: false},
		{Key: string(bob.ID), Input: 1, Output: 1, Calls: 1, PriceKnown: true},
		{Key: string(ch.Principal), Cost: 5, Input: 2, Output: 2, Calls: 1, PriceKnown: true},
	}, func(x *core.UsageRow) string { return x.Key }))
	f.eq("Report of one user", report(core.UsageQuery{GroupBy: "user", User: bob.ID}), []*core.UsageRow{
		{Key: string(bob.ID), Input: 1, Output: 1, Calls: 1, PriceKnown: true},
	})
	f.eq("Report by session", report(core.UsageQuery{GroupBy: "session"}), sortBy([]*core.UsageRow{
		{Key: "", Cost: 5, Input: 3, Output: 3, Calls: 2, PriceKnown: true},
		{Key: string(s1.ID), Cost: 300, Input: 30, Output: 15, CacheRead: 1, CacheWrite: 2, Calls: 2, PriceKnown: false},
		{Key: string(s2.ID), Cost: 300, Input: 30, Output: 15, CacheRead: 3, CacheWrite: 4, Calls: 1, PriceKnown: true},
	}, func(x *core.UsageRow) string { return x.Key }))
	f.eq("Report by schedule (joins runs)", report(core.UsageQuery{GroupBy: "schedule", User: alice.ID}), sortBy([]*core.UsageRow{
		{Key: "", Cost: 300, Input: 30, Output: 15, CacheRead: 3, CacheWrite: 4, Calls: 1, PriceKnown: true},
		{Key: string(sch.ID), Cost: 300, Input: 30, Output: 15, CacheRead: 1, CacheWrite: 2, Calls: 2, PriceKnown: false},
	}, func(x *core.UsageRow) string { return x.Key }))
	calls := 0
	for _, row := range report(core.UsageQuery{GroupBy: "day"}) {
		_, err := time.Parse(time.DateOnly, row.Key)
		f.check(err == nil, "day key %q is not YYYY-MM-DD", row.Key)
		calls += row.Calls
	}
	f.check(calls == 5, "Report by day counted %d calls, want 5", calls)
	y2k, y3k := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	f.check(len(report(core.UsageQuery{GroupBy: "user", From: y3k})) == 0, "Report From the future returned rows")
	f.check(len(report(core.UsageQuery{GroupBy: "user", To: y2k})) == 0, "Report To the past returned rows")
	f.check(len(report(core.UsageQuery{GroupBy: "user", From: y2k, To: y3k})) == 3, "Report over all time lost rows")
	_, err = r.Report(f.ctx, core.UsageQuery{GroupBy: "week"})
	f.want(err, core.ErrInvalid, "Report with an unknown GroupBy")
}

func ptrSlice[T any](rows []T) []*T {
	out := make([]*T, len(rows))
	for i := range rows {
		out[i] = &rows[i]
	}
	return out
}
