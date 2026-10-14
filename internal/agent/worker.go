package agent

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// worker serializes everything that happens in one session: turns, resumed
// parked runs and compactions run one at a time on its goroutine, which exits
// after workerIdle without work.
type worker struct {
	r    *Runner
	id   core.SessionID
	wake chan struct{} // capacity 1

	// Guarded by mu. Steer messages are persisted while holding mu, so their
	// sequence numbers order correctly against the active turn's.
	mu      sync.Mutex
	dead    bool
	active  *runState
	busy    bool     // a non-run job (compaction) is executing
	pending *turnJob // DM turn inside its coalescing window
	queue   []job    // waiting jobs, oldest first
	steers  int      // steer messages persisted but not yet seen by an assembly
	merged  int      // inputs merged into a turn that has not been persisted yet
}

type job interface{ isJob() }

// turnJob is one user turn: the requests merged into it share one run.
type turnJob struct {
	id    core.RunID
	reqs  []core.RunRequest
	until time.Time // end of the DM coalescing window; zero = start at once
	// announced: its sink got run.queued, so a channel shows a live
	// "Queued (N ahead)…" message that some later event must finish.
	announced bool
}

type resumeJob struct {
	run      *core.Run
	approval *core.Approval
}

type compactJob struct {
	user core.UserID
	done chan error
}

func (*turnJob) isJob()    {}
func (*resumeJob) isJob()  {}
func (*compactJob) isJob() {}

// worker returns the live worker of a session, starting one when needed. The
// caller must lock it and re-check dead, since an idle worker may be retiring.
// Once shutdown has begun no worker starts, so r.loops never grows while
// shutdown waits on it.
func (r *Runner) worker(id core.SessionID) (*worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing.Load() {
		return nil, errShuttingDown
	}
	if w, ok := r.workers[id]; ok {
		return w, nil
	}
	w := &worker{r: r, id: id, wake: make(chan struct{}, 1)}
	r.workers[id] = w
	r.loops.Add(1)
	go w.loop()
	return w, nil
}

func (r *Runner) existingWorker(id core.SessionID) *worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.workers[id]
}

func (w *worker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// load is how many accepted inputs wait for this session, against
// agent.queue_depth.
func (w *worker) load() int { return len(w.queue) + w.steers + w.merged }

func (w *worker) loop() {
	defer w.r.loops.Done()
	for {
		j := w.take()
		if j == nil {
			return
		}
		w.exec(j)
	}
}

// take blocks until there is a job, the coalescing window of a pending DM turn
// ends, or the worker retires (nil).
func (w *worker) take() job {
	idle := time.NewTimer(workerIdle)
	defer idle.Stop()
	for {
		w.mu.Lock()
		if w.dead { // replaced after its stopped run was abandoned
			w.mu.Unlock()
			return nil
		}
		if w.r.closing.Load() {
			w.mu.Unlock()
			w.retireForShutdown()
			return nil
		}
		if p := w.pending; p != nil {
			if wait := p.until.Sub(w.r.now()); wait > 0 {
				w.mu.Unlock()
				t := time.NewTimer(wait)
				select {
				case <-t.C:
				case <-w.wake:
					t.Stop()
				}
				continue
			}
			w.pending = nil
			w.active = w.r.newRunState(w, p)
			w.mu.Unlock()
			return p
		}
		if len(w.queue) > 0 {
			j := w.queue[0]
			w.queue = w.queue[1:]
			var merged []*turnJob
			switch j := j.(type) {
			case *turnJob:
				for len(w.queue) > 0 {
					next, ok := w.queue[0].(*turnJob)
					if !ok || !mergeable(j.reqs[0], next.reqs[0]) {
						break
					}
					j.reqs = append(j.reqs, next.reqs...)
					merged = append(merged, next)
					w.queue = w.queue[1:]
				}
				w.active = w.r.newRunState(w, j)
			case *resumeJob:
				w.active = w.r.newResumeState(w, j)
			case *compactJob:
				w.busy = true
			}
			w.mu.Unlock()
			if t, ok := j.(*turnJob); ok {
				for _, m := range merged {
					w.r.finishQueued(m, t.reqs[0].Sink, core.RunDone, mergedNotice)
				}
			}
			return j
		}
		w.mu.Unlock()
		select {
		case <-w.wake:
		case <-idle.C:
			if w.tryRetire() {
				return nil
			}
			idle.Reset(workerIdle)
		}
	}
}

func (w *worker) exec(j job) {
	var compacted chan error
	var err error
	switch j := j.(type) {
	case *turnJob:
		w.r.runTurn(w.activeRun())
	case *resumeJob:
		w.r.runResume(w.activeRun(), j)
	case *compactJob:
		compacted = j.done
		err = w.r.compactSession(context.Background(), w.id, j.user)
	}
	if w.r.beforeIdle != nil {
		w.r.beforeIdle()
	}
	w.mu.Lock()
	// Steers the run never read are in the history the next turn reads, so
	// they no longer count against queue_depth.
	w.active, w.busy, w.steers = nil, false, 0
	w.mu.Unlock()
	// Compact returns only now: a message sent right after it must start a
	// run, not queue behind a compaction that has already finished.
	if compacted != nil {
		compacted <- err
	}
}

func (w *worker) activeRun() *runState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active
}

// tryRetire removes an idle worker from the runner. Lock order: r.mu, w.mu.
func (w *worker) tryRetire() bool {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != nil || w.busy || w.pending != nil || len(w.queue) > 0 {
		return false
	}
	w.dead = true
	w.r.forgetLocked(w)
	return true
}

// retireForShutdown retires the worker and drops its queued work.
func (w *worker) retireForShutdown() {
	w.r.mu.Lock()
	w.mu.Lock()
	jobs := w.queue
	if w.pending != nil {
		jobs = append([]job{w.pending}, jobs...)
	}
	w.queue, w.pending, w.dead = nil, nil, true
	w.r.forgetLocked(w)
	w.mu.Unlock()
	w.r.mu.Unlock()
	w.r.dropForShutdown(jobs)
}

const droppedAtShutdown = "Bothy restarted before it got to this message. Send 'retry' to continue."

// dropForShutdown disposes of jobs no worker will run. A waiting /compact
// gets an error instead of blocking its caller; queued resumes keep their
// row "queued" and become interrupted at the next boot; accepted messages
// are kept (see keepDroppedTurn).
func (r *Runner) dropForShutdown(jobs []job) {
	for _, j := range jobs {
		switch j := j.(type) {
		case *compactJob:
			j.done <- errShutdown
		case *turnJob:
			r.keepDroppedTurn(context.Background(), j)
		}
	}
}

// keepDroppedTurn saves a turn that was accepted (queued, or waiting in its
// coalescing window) but never ran: its input has no run row and is stored
// only when a turn starts, and the hub has already deduplicated the platform
// message, so nothing would bring it back. The input goes into the history
// with its taint, a notice says it was not answered, its chat is told
// directly (its sink may already be stopped), and its sink is finished.
func (r *Runner) keepDroppedTurn(ctx context.Context, j *turnJob) {
	req := j.reqs[0]
	if parts := inputsOf(j.reqs); len(parts) > 0 {
		var legs core.Taint
		for _, q := range j.reqs {
			if q.Provenance == core.ProvUntrusted {
				legs |= core.TaintUntrusted
			}
		}
		if _, err := r.recordTaint(ctx, req.Session, 0, legs); err != nil {
			r.log.Error("keep a message dropped at shutdown", "session", req.Session, "err", err)
			return
		}
		m := &core.Message{Session: req.Session, Role: core.MsgUser, Parts: parts, Provenance: req.Provenance, Sender: req.Sender}
		m.Tokens = r.d.LLM.Estimate(plainText(parts), core.ModelRef{})
		if err := r.appendMessage(ctx, req.Principal, m, nil); err != nil {
			r.log.Error("keep a message dropped at shutdown", "session", req.Session, "err", err)
		}
		r.recordURLs(ctx, req.Session, req.Provenance, parts)
		if untrustedFiles(j.reqs) {
			if p, err := r.d.Store.Principals().Get(ctx, core.SystemViewer(), req.Principal); err == nil {
				r.markWorkspaceUntrusted(ctx, p, "attachments from someone other than the user")
			}
		}
	}
	r.notice(ctx, req.Session, req.Principal, "", droppedAtShutdown, nil)
	told := map[string]bool{}
	for _, q := range j.reqs {
		if q.Chat != nil && !told[q.Chat.Key()] {
			told[q.Chat.Key()] = true
			r.sendChat(ctx, q.Chat, droppedAtShutdown)
		}
	}
	r.finishQueued(j, nil, core.RunInterrupted, "")
}

// forgetLocked drops a retiring worker and its session's per-process state,
// which would otherwise grow with every session ever run (each stateless /v1
// request has its own); r.mu is held.
func (r *Runner) forgetLocked(w *worker) {
	if r.workers[w.id] == w {
		delete(r.workers, w.id)
		delete(r.prefix, w.id)
	}
}

const mergedNotice = "Merged into the reply above."

// finishQueued ends the sinks of an announced turn that will not run as its
// own (merged into an earlier turn's run, or dropped), so their live
// "Queued…" messages are finished instead of lingering until the sink idles
// out. keep is the sink the run streams to.
func (r *Runner) finishQueued(j *turnJob, keep core.RunSink, st core.RunStatus, notice string) {
	if !j.announced {
		return
	}
	for _, q := range j.reqs {
		if q.Sink == nil || q.Sink == keep {
			continue
		}
		base := core.Event{Principal: q.Principal, Session: q.Session, Run: j.id, At: r.now()}
		if notice != "" {
			ev := base
			ev.Type = core.EvMessageCreated
			ev.Data = core.MessageData{Message: &core.Message{Session: q.Session, Run: j.id, Role: core.MsgAssistant,
				Kind: core.KindNotice, Provenance: core.ProvSystem, Parts: []core.Part{{Type: core.PartText, Text: notice}}}}
			q.Sink.OnEvent(ev)
		}
		ev := base
		ev.Type, ev.Data = core.EvRunFinished, core.RunFinishedData{Status: st}
		q.Sink.OnEvent(ev)
	}
}

// mergeable reports whether two queued requests can share one user turn.
func mergeable(a, b core.RunRequest) bool {
	chatKey := func(c *core.ChatRef) string {
		if c == nil {
			return ""
		}
		return c.Key()
	}
	return a.Trigger == core.TrigMessage && b.Trigger == core.TrigMessage &&
		a.User == b.User && a.Provenance == b.Provenance && a.Sender == b.Sender &&
		a.Slot == b.Slot && chatKey(a.Chat) == chatKey(b.Chat) &&
		a.Limits == nil && b.Limits == nil &&
		a.Instructions == "" && b.Instructions == "" &&
		len(a.Ephemeral) == 0 && len(b.Ephemeral) == 0 &&
		a.OnApproval == b.OnApproval && a.MaxOutput == b.MaxOutput &&
		a.Temperature == nil && b.Temperature == nil && len(a.Stop) == 0 && len(b.Stop) == 0 &&
		sameRefine(a.Refine, b.Refine)
}

func sameRefine(a, b *bool) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// inputsOf flattens the inputs of merged requests in arrival order.
func inputsOf(reqs []core.RunRequest) []core.Part {
	var parts []core.Part
	for _, q := range reqs {
		parts = append(parts, slices.Clone(q.Input)...)
	}
	return parts
}
