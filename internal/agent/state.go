package agent

import (
	"context"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// runState is the in-memory side of one run. The run goroutine (its session
// worker) owns the unexported working fields; Stop and the abandon timer use
// only the mutex-guarded ones.
type runState struct {
	r      *Runner
	w      *worker
	id     core.RunID
	turn   *turnJob // nil for a resumed run
	req    core.RunRequest
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	// Guarded by w.mu.
	turnPersisted bool
	finishing     bool

	mu         sync.Mutex
	run        core.Run // database row mirror; saveRun writes it
	rowCreated bool
	stopped    bool
	finished   bool            // run.finished published
	budget     core.BudgetKind // limit that ended the run, for run.finished
	userHeld   bool
	globalHeld bool
	meter      core.RunMeter
	postCancel context.CancelFunc // cancels the compaction after the run finished

	emitMu   sync.Mutex
	sink     core.RunSink
	detached bool
	exposed  bool // the sink gets neutralized links (SPEC §5.3)

	// Owned by the run goroutine.
	sess       *core.Session
	principal  *core.Principal
	user       *core.User // nil for a team-owned principal
	role       core.Role  // the account's role; member for a team-owned principal
	slot       core.Slot
	override   *core.ModelRef
	chain      []core.ModelInfo
	profile    profile
	sel        core.Selection
	seenSeq    int64           // newest message seq the last assembly read
	answered   int64           // newest message seq a completed model call has seen
	callIDs    map[string]bool // tool call ids in the history the last assembly read
	taint      core.Taint
	wsReady    bool
	toolErrors int // consecutive failed tool calls
	deadline   *time.Timer
	ctxRetried bool
	lastEst    int // estimated input of the last step, for post-run compaction

	refine *refineMode // nil: final answers are not refined
}

func (r *Runner) newRunState(w *worker, j *turnJob) *runState {
	ctx, cancel := context.WithCancelCause(context.Background())
	req := j.reqs[0]
	return &runState{r: r, w: w, id: j.id, turn: j, req: req, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), sink: req.Sink}
}

func (r *Runner) newResumeState(w *worker, j *resumeJob) *runState {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &runState{r: r, w: w, id: j.run.ID, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		run: *j.run, rowCreated: true,
		req: core.RunRequest{Session: j.run.Session, Principal: j.run.Principal, User: j.run.User,
			Trigger: j.run.Trigger, Schedule: j.run.Schedule}}
}

func (rs *runState) info() core.RunInfo {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	st := rs.run.Status
	if st == "" {
		st = core.RunQueued
	}
	return core.RunInfo{Run: rs.id, Session: rs.req.Session, Principal: rs.req.Principal, Status: st, Started: rs.run.StartedAt}
}

func (rs *runState) totals() core.RunTotals {
	rs.mu.Lock()
	m := rs.meter
	run := rs.run
	rs.mu.Unlock()
	if m == nil {
		return core.RunTotals{Steps: run.Steps, ToolCalls: run.ToolCalls, Usage: run.Usage, Cost: run.Cost}
	}
	t := m.Totals()
	if rs.turn == nil { // resumed: the meter counts only this segment
		t.Steps += run.Steps
		t.ToolCalls += run.ToolCalls
		t.Usage = t.Usage.Add(run.Usage)
		t.Cost += run.Cost
	}
	return t
}

// setStatus updates the in-memory status and persists it. A stopped run keeps
// "stopped" whatever the goroutine tries to write afterwards.
func (rs *runState) setStatus(ctx context.Context, st core.RunStatus) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.stopped {
		return
	}
	rs.run.Status = st
	rs.saveLocked(ctx)
}

func (rs *runState) saveLocked(ctx context.Context) {
	if !rs.rowCreated {
		return
	}
	row := rs.run
	if rs.stopped {
		row.Status = core.RunStopped
	}
	if err := rs.r.d.Store.Runs().Update(context.WithoutCancel(ctx), &row); err != nil {
		rs.r.log.Error("save run", "run", rs.id, "err", err)
	}
}

func (rs *runState) isStopped() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.stopped
}

// stop marks the run stopped and cancels it; false when it already ended.
func (rs *runState) stop(ctx context.Context, by core.UserID) bool {
	rs.mu.Lock()
	if rs.stopped || (rs.rowCreated && rs.run.Status.Terminal()) {
		rs.mu.Unlock()
		return false
	}
	rs.stopped = true
	rs.run.Status, rs.run.StoppedBy, rs.run.FinishedAt = core.RunStopped, by, rs.r.now()
	rs.saveLocked(ctx)
	rs.mu.Unlock()
	rs.cancel(core.ErrStopped)
	return true
}

// stopPostRun cancels the compaction a finished run does on its way out;
// false when none is running.
func (rs *runState) stopPostRun() bool {
	rs.mu.Lock()
	cancel := rs.postCancel
	rs.postCancel = nil
	rs.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (rs *runState) detach() {
	rs.emitMu.Lock()
	rs.detached = true
	rs.emitMu.Unlock()
}

// semKey keys agent.max_runs_per_user: an account's runs share its slots,
// and each team-owned principal (a teammate, a channel) has slots of its
// own, so a whole team never shares one person's two.
func semKey(req core.RunRequest) string {
	if req.User != "" {
		return string(req.User)
	}
	return "p:" + string(req.Principal)
}

// acquire takes the per-user slot, then the global one, so a user at their
// limit never holds a global slot (SPEC §5.4).
func (rs *runState) acquire(ctx context.Context) error {
	r := rs.r
	onWait := func(pos int) {
		rs.publish(core.EvRunQueued, core.RunQueuedData{Position: pos})
		rs.publish(core.EvRunStatus, core.RunStatusData{Status: "queued", Detail: plural(pos, "run") + " ahead"})
	}
	us := r.userSem(semKey(rs.req))
	rs.mu.Lock()
	needUser, needGlobal := !rs.userHeld, !rs.globalHeld
	rs.mu.Unlock()
	if needUser {
		if err := us.acquire(ctx, onWait); err != nil {
			return err
		}
		rs.mu.Lock()
		rs.userHeld = true
		rs.mu.Unlock()
	}
	if needGlobal {
		if err := r.global.acquire(ctx, onWait); err != nil {
			return err
		}
		rs.mu.Lock()
		rs.globalHeld = true
		rs.mu.Unlock()
	}
	return nil
}

func (rs *runState) releaseSems() {
	rs.mu.Lock()
	u, g := rs.userHeld, rs.globalHeld
	rs.userHeld, rs.globalHeld = false, false
	rs.mu.Unlock()
	if g {
		rs.r.global.release()
	}
	if u {
		rs.r.userSem(semKey(rs.req)).release()
	}
}

// startDeadline arms the wall-clock limit; approval waits pause it.
func (rs *runState) startDeadline() {
	rs.stopDeadline()
	if rs.meter == nil {
		return
	}
	dl := rs.meter.Deadline()
	if dl.IsZero() {
		return
	}
	limit := rs.run.Limits.MaxTime
	rs.deadline = time.AfterFunc(dl.Sub(rs.r.now()), func() {
		rs.cancel(&core.BudgetError{Kind: core.BudgetTime, Limit: int64(limit), Used: int64(limit)})
	})
}

func (rs *runState) stopDeadline() {
	if rs.deadline != nil {
		rs.deadline.Stop()
		rs.deadline = nil
	}
}

func (rs *runState) pause() {
	rs.stopDeadline()
	if rs.meter != nil {
		rs.meter.Pause()
	}
}

func (rs *runState) resume() {
	if rs.meter != nil {
		rs.meter.Resume()
	}
	rs.startDeadline()
}
