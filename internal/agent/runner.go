package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	steerReply        = "Got it, adding that to the current task."
	steerReplyWaiting = "Waiting for your approval above; I'll read this after."
)

// Submit returns ErrBusy when the session mailbox is full and ErrPaused when
// the kill switch is engaged.
func (r *Runner) Submit(ctx context.Context, req core.RunRequest) (core.SubmitResult, error) {
	if err := r.ensureInit(ctx); err != nil {
		return core.SubmitResult{}, err
	}
	if r.closing.Load() {
		return core.SubmitResult{}, errShuttingDown
	}
	if r.paused.Load() {
		return core.SubmitResult{}, core.ErrPaused
	}
	sess, err := r.validate(ctx, &req)
	if err != nil {
		return core.SubmitResult{}, err
	}
	if err := r.d.Budget.CheckDaily(ctx, req.User); err != nil {
		r.refuseDaily(ctx, sess, req, err)
		return core.SubmitResult{}, err
	}
	for {
		w, err := r.worker(req.Session)
		if err != nil {
			return core.SubmitResult{}, err
		}
		w.mu.Lock()
		if w.dead {
			w.mu.Unlock()
			continue
		}
		res, err := r.place(ctx, w, req)
		w.mu.Unlock()
		return res, err
	}
}

// validate checks the request against its session: the principal must own
// the session and the acting user must be the principal's user.
func (r *Runner) validate(ctx context.Context, req *core.RunRequest) (*core.Session, error) {
	switch req.Trigger {
	case core.TrigMessage, core.TrigSchedule, core.TrigHeartbeat, core.TrigAPI:
	default:
		return nil, fmt.Errorf("agent: trigger %q: %w", req.Trigger, core.ErrInvalid)
	}
	switch req.Provenance {
	case "":
		req.Provenance = core.ProvUser
	case core.ProvUser, core.ProvUntrusted, core.ProvSystem:
	default:
		return nil, fmt.Errorf("agent: provenance %q: %w", req.Provenance, core.ErrInvalid)
	}
	if len(req.Input) == 0 && len(req.Ephemeral) == 0 {
		return nil, fmt.Errorf("agent: empty input: %w", core.ErrInvalid)
	}
	sess, err := r.d.Store.Sessions().Get(ctx, core.SystemViewer(), req.Session)
	if err != nil {
		return nil, err
	}
	if sess.Principal != req.Principal {
		return nil, fmt.Errorf("agent: session %s: %w", req.Session, core.ErrNotFound)
	}
	if sess.Archived {
		return nil, fmt.Errorf("agent: session %s is archived: %w", req.Session, core.ErrInvalid)
	}
	p, err := r.d.Store.Principals().Get(ctx, core.SystemViewer(), req.Principal)
	if err != nil {
		return nil, err
	}
	if p.User != req.User {
		return nil, fmt.Errorf("agent: user %s does not own principal %s: %w", req.User, p.ID, core.ErrForbidden)
	}
	return sess, nil
}

func (r *Runner) refuseDaily(ctx context.Context, sess *core.Session, req core.RunRequest, err error) {
	var be *core.BudgetError
	if !errors.As(err, &be) {
		return
	}
	reset := r.resetTime()
	text := fmt.Sprintf("Today's spending cap is reached (%s of %s). New runs start again at %s. Settings → Budgets has the limits.",
		core.Micros(be.Used), core.Micros(be.Limit), reset.Format("15:04 MST"))
	r.notice(ctx, sess.ID, sess.Principal, "", text, req.Sink)
	r.auditf(ctx, "user:"+string(req.User), "budget.exceeded", string(sess.ID), "denied", sess.ID, "",
		map[string]any{"kind": be.Kind, "limit": be.Limit, "used": be.Used})
}

// resetTime is the next local midnight in the configured timezone, when the
// daily caps reset.
func (r *Runner) resetTime() time.Time {
	loc := time.UTC
	if tz := r.cfg().Timezone; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	now := r.now().In(loc)
	y, m, d := now.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

// place decides what happens to a request; w.mu is held.
func (r *Runner) place(ctx context.Context, w *worker, req core.RunRequest) (core.SubmitResult, error) {
	c := r.cfg()
	depth := orInt(c.Agent.QueueDepth, 8)
	if a := w.active; a != nil && !a.finishing && r.steerable(a, req) {
		if w.load() >= depth {
			return core.SubmitResult{}, core.ErrBusy
		}
		switch {
		case !a.turnPersisted && mergeable(a.turn.reqs[0], req):
			a.turn.reqs = append(a.turn.reqs, req)
			w.merged++
			return core.SubmitResult{Run: a.id, Steered: true}, nil
		case a.turnPersisted:
			if err := r.persistSteer(ctx, a, req); err != nil {
				return core.SubmitResult{}, err
			}
			w.steers++
			if !a.steerReplied && req.Chat != nil {
				a.steerReplied = true
				text := steerReply
				if a.status() == core.RunWaitingApproval {
					text = steerReplyWaiting
				}
				r.sendAsync(*req.Chat, text)
			}
			return core.SubmitResult{Run: a.id, Steered: true}, nil
		}
	}
	if p := w.pending; p != nil && mergeable(p.reqs[0], req) {
		if w.load() >= depth {
			return core.SubmitResult{}, core.ErrBusy
		}
		p.reqs = append(p.reqs, req)
		w.merged++
		return core.SubmitResult{Run: p.id}, nil
	}
	j := &turnJob{id: core.RunID(core.NewID(core.PrefixRun)), reqs: []core.RunRequest{req}}
	if w.active == nil && !w.busy && w.pending == nil && len(w.queue) == 0 {
		if win := c.Agent.CoalesceWindow.D(); win > 0 && coalescible(req) {
			j.until = r.now().Add(win)
			w.pending = j
		} else {
			w.queue = append(w.queue, j)
		}
		w.signal()
		return core.SubmitResult{Run: j.id}, nil
	}
	if w.load() >= depth {
		return core.SubmitResult{}, core.ErrBusy
	}
	w.queue = append(w.queue, j)
	w.signal()
	ev := r.d.Bus.Publish(core.Event{Type: core.EvRunQueued, Principal: req.Principal, Session: req.Session,
		Data: core.RunQueuedData{Position: len(w.queue)}})
	if req.Sink != nil {
		req.Sink.OnEvent(ev)
	}
	return core.SubmitResult{Queued: true}, nil
}

// steerable: only interactive turns take steer messages, and only from the
// same user with the same provenance, so an untrusted participant can never
// ride on a run that started from the owner's message (floor F6).
func (r *Runner) steerable(a *runState, req core.RunRequest) bool {
	return r.cfg().Agent.BusyMode != "queue" && a.turn != nil &&
		req.Trigger == core.TrigMessage && a.req.Trigger == core.TrigMessage &&
		req.User == a.req.User && req.Provenance == a.req.Provenance && len(req.Ephemeral) == 0
}

func coalescible(req core.RunRequest) bool {
	return req.Trigger == core.TrigMessage && req.Chat != nil && req.Chat.Kind == core.ChatDM
}

func (r *Runner) sendAsync(chat core.ChatRef, text string) {
	h := r.hubOrNil()
	if h == nil {
		return
	}
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		ctx, cancel := context.WithTimeout(r.bgCtx, 30*time.Second)
		defer cancel()
		if _, err := h.Send(ctx, chat, text); err != nil {
			r.log.Warn("chat notice", "chat", chat.Key(), "err", err)
		}
	}()
}

// Stop cancels the active run, SIGKILLs the session box, cancels its pending
// approvals and clears its queue. It never waits on the run goroutine.
func (r *Runner) Stop(ctx context.Context, v core.Viewer, s core.SessionID, by core.UserID) (bool, error) {
	sess, err := r.d.Store.Sessions().Get(ctx, v, s)
	if err != nil {
		return false, err
	}
	return r.stopSession(ctx, sess, by), nil
}

func (r *Runner) stopSession(ctx context.Context, sess *core.Session, by core.UserID) bool {
	var rs *runState
	var dropped []job
	if w := r.existingWorker(sess.ID); w != nil {
		w.mu.Lock()
		rs = w.active
		dropped = w.queue
		w.queue, w.pending, w.steers, w.merged = nil, nil, 0, 0
		w.mu.Unlock()
	}
	runStopped := rs != nil && rs.stop(ctx, by)
	stopped := runStopped
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopKillWithin)
	defer cancel()
	if r.d.Sandbox != nil {
		if err := r.d.Sandbox.Kill(kctx, boxKey(sess.ID)); err != nil && !errors.Is(err, core.ErrNoSandbox) {
			r.log.Warn("kill session box", "session", sess.ID, "err", err)
		}
	}
	if rs != nil {
		if err := r.d.Approvals.CancelRun(kctx, rs.id); err != nil {
			r.log.Warn("cancel approvals", "run", rs.id, "err", err)
		}
	}
	for _, j := range dropped {
		switch j := j.(type) {
		case *resumeJob:
			if r.finishDetached(kctx, j.run, core.RunStopped, "", by) {
				stopped = true
			}
		case *compactJob:
			j.done <- core.ErrStopped
		}
	}
	// Parked runs, and resumes Resume has flipped to queued but not yet handed
	// to the worker, have no goroutine to cancel.
	for _, st := range []core.RunStatus{core.RunParked, core.RunQueued} {
		runs, err := r.d.Store.Runs().List(kctx, core.SystemViewer(), core.RunFilter{Session: sess.ID, Status: st})
		if err != nil {
			r.log.Warn("list runs to stop", "session", sess.ID, "status", st, "err", err)
			continue
		}
		for _, run := range runs {
			if rs != nil && run.ID == rs.id {
				continue
			}
			if st == core.RunParked {
				if err := r.d.Approvals.CancelRun(kctx, run.ID); err != nil {
					r.log.Warn("cancel approvals of parked run", "run", run.ID, "err", err)
				}
			}
			if r.finishDetached(kctx, run, core.RunStopped, "", by) {
				stopped = true
			}
		}
	}
	if !stopped {
		return false
	}
	var detail map[string]any
	if runStopped {
		tot := rs.totals()
		detail = map[string]any{"run": rs.id, "steps": tot.Steps, "cost_micros": tot.Cost}
		r.notice(kctx, sess.ID, sess.Principal, rs.id, fmt.Sprintf("Stopped. %s, %s", plural(tot.Steps, "step"), tot.Cost), nil)
		time.AfterFunc(abandonAfter, func() { r.abandon(rs) })
	}
	var runID core.RunID
	if runStopped {
		runID = rs.id
	}
	r.auditf(kctx, "user:"+string(by), "run.stop", string(sess.ID), "ok", sess.ID, runID, detail)
	return true
}

// abandon detaches a stopped run whose goroutine is still stuck (a tool that
// ignores cancellation): its sink gets nothing more, its semaphores are
// freed and the session gets a fresh worker.
func (r *Runner) abandon(rs *runState) {
	select {
	case <-rs.done:
		return
	default:
	}
	r.log.Warn("abandoning a stopped run that did not exit", "run", rs.id)
	rs.publishFinished()
	rs.detach()
	rs.releaseSems()
	r.mu.Lock()
	r.forgetLocked(rs.w)
	r.mu.Unlock()
	rs.w.mu.Lock()
	rs.w.dead = true
	rs.w.mu.Unlock()
}

// StopAll stops every session that has a run or queued work, so a resume
// waiting in a queue cannot start after the kill switch is engaged.
func (r *Runner) StopAll(ctx context.Context, by core.UserID) (int, error) {
	n := 0
	for _, id := range r.busySessions() {
		sess, err := r.d.Store.Sessions().Get(ctx, core.SystemViewer(), id)
		if err != nil {
			r.log.Warn("stop all: session", "session", id, "err", err)
			continue
		}
		if r.stopSession(ctx, sess, by) {
			n++
		}
	}
	return n, nil
}

func (r *Runner) busySessions() []core.SessionID {
	r.mu.Lock()
	ws := make([]*worker, 0, len(r.workers))
	for _, w := range r.workers {
		ws = append(ws, w)
	}
	r.mu.Unlock()
	var out []core.SessionID
	for _, w := range ws {
		w.mu.Lock()
		if w.active != nil || w.pending != nil || len(w.queue) > 0 {
			out = append(out, w.id)
		}
		w.mu.Unlock()
	}
	return out
}

func (r *Runner) activeRuns() []*runState {
	r.mu.Lock()
	ws := make([]*worker, 0, len(r.workers))
	for _, w := range r.workers {
		ws = append(ws, w)
	}
	r.mu.Unlock()
	var out []*runState
	for _, w := range ws {
		w.mu.Lock()
		if w.active != nil {
			out = append(out, w.active)
		}
		w.mu.Unlock()
	}
	return out
}

// SetPaused engages or releases the kill switch (persisted in meta.paused).
// Engaging it also stops every active run.
func (r *Runner) SetPaused(ctx context.Context, paused bool, by core.UserID) error {
	if err := r.ensureInit(ctx); err != nil {
		return err
	}
	v := "0"
	if paused {
		v = "1"
	}
	if err := r.d.Store.Meta().Set(ctx, metaPaused, v); err != nil {
		return fmt.Errorf("agent: persist kill switch: %w", err)
	}
	r.paused.Store(paused)
	action := "admin.resume"
	banner := core.BannerData{Code: "paused", Clear: true}
	if paused {
		action = "admin.pause"
		banner = core.BannerData{Level: "error", Code: "paused", Text: "Bothy is paused by an admin.", Action: "/settings"}
	}
	r.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: banner})
	r.auditf(ctx, "user:"+string(by), action, "runner", "ok", "", "", nil)
	if paused {
		if _, err := r.StopAll(ctx, by); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) Paused() bool {
	if err := r.ensureInit(context.Background()); err != nil {
		r.log.Warn("load kill switch", "err", err)
	}
	return r.paused.Load()
}

func (r *Runner) Active() []core.RunInfo {
	r.mu.Lock()
	ws := make([]*worker, 0, len(r.workers))
	for _, w := range r.workers {
		ws = append(ws, w)
	}
	r.mu.Unlock()
	var out []core.RunInfo
	for _, w := range ws {
		w.mu.Lock()
		if a := w.active; a != nil {
			info := a.info()
			info.Queued = len(w.queue)
			out = append(out, info)
		}
		w.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b core.RunInfo) int { return a.Started.Compare(b.Started) })
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
