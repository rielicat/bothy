package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	apiDenied      = "Requires approval, unavailable via API."
	userDenied     = "The user denied this action."
	approvalExpiry = "Approval expired (denied)."
)

var errNotParked = errors.New("run is not parked on this approval")

// ask handles an Ask decision: request the approval, release the run's slots
// while waiting (D22), then execute, deny or park.
func (r *Runner) ask(rs *runState, pc *pcall, rest []core.ToolCall) *runEnd {
	ctx := rs.ctx
	api := rs.req.Trigger == core.TrigAPI
	if api && rs.req.OnApproval != "wait" {
		return r.finishCall(rs, pc, textResult(core.TCDenied, apiDenied), false)
	}
	appr, err := r.d.Approvals.Request(ctx, core.ApprovalRequest{
		Run: rs.id, Session: rs.req.Session, Principal: rs.req.Principal, ToolCallID: pc.rec.ID,
		Action: pc.action, Spec: pc.spec, Decision: pc.dec, Chat: rs.req.Chat, Context: r.cardContext(rs),
	})
	if err != nil {
		if ctx.Err() != nil {
			return r.ctxEnd(rs)
		}
		return r.finishCall(rs, pc, textResult(core.TCError, "Could not request approval: "+r.redact(err.Error())), false)
	}
	pc.rec.Status, pc.rec.Approval = core.TCWaiting, appr.ID
	r.updateRec(ctx, pc.rec)
	rs.setStatus(ctx, core.RunWaitingApproval)
	rs.publish(core.EvRunStatus, core.RunStatusData{Status: "waiting_approval", Detail: appr.Card.Title})
	rs.pause()
	rs.releaseSems()

	waitCtx := ctx
	if api {
		wait := rs.req.ApprovalWait
		if wait <= 0 {
			wait = r.cfg().Policy.Approval.APIWait.D()
		}
		if wait <= 0 {
			wait = 120 * time.Second
		}
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	choice, err := r.d.Approvals.Await(waitCtx, appr.ID)
	if errors.Is(err, core.ErrParked) && !api {
		return r.park(rs, pc, appr, rest)
	}
	if errors.Is(err, core.ErrStopped) { // the approval was cancelled: the run is over
		rs.cancel(core.ErrStopped)
	}
	// An API run gave up on the card; cancel it so a later click cannot look
	// like it approved anything.
	if api && ctx.Err() == nil && (waitCtx.Err() != nil || errors.Is(err, core.ErrParked)) {
		if cerr := r.d.Approvals.CancelRun(context.WithoutCancel(ctx), rs.id); cerr != nil {
			r.log.Warn("cancel expired api approval", "run", rs.id, "err", cerr)
		}
	}
	if ctx.Err() != nil {
		return r.ctxEnd(rs)
	}
	if aerr := rs.acquire(ctx); aerr != nil {
		return r.ctxEnd(rs)
	}
	rs.resume()
	rs.setStatus(ctx, core.RunRunning)
	switch {
	case err == nil && !allows(choice): // deny, or anything unexpected: fail closed
		pc.rec.Status = core.TCDenied
		return r.finishCall(rs, pc, textResult(core.TCDenied, userDenied), false)
	case err == nil:
		r.grantFor(ctx, rs, pc, choice)
		pc.rec.Status = core.TCApproved
		r.updateRec(ctx, pc.rec)
		return r.finishCall(rs, pc, r.execute(rs, pc), false)
	case errors.Is(err, core.ErrApprovalExpired), errors.Is(err, core.ErrParked), waitCtx.Err() != nil:
		return r.finishCall(rs, pc, textResult(core.TCDenied, approvalExpiry), false)
	}
	return r.finishCall(rs, pc, textResult(core.TCError, "Approval failed: "+r.redact(err.Error())), false)
}

func allows(c core.Choice) bool {
	return c == core.AllowOnce || c == core.AllowSession || c == core.AllowAlways
}

// grantFor creates the narrow grant an allow_session or allow_always choice
// stands for. A choice the decision did not offer counts as allow_once, so a
// decision that lists no choices never yields a grant.
func (r *Runner) grantFor(ctx context.Context, rs *runState, pc *pcall, choice core.Choice) {
	if choice != core.AllowSession && choice != core.AllowAlways {
		return
	}
	if !slices.Contains(pc.dec.Choices, choice) {
		r.log.Warn("approval choice was not offered; treating it as allow_once", "choice", choice, "run", rs.id)
		return
	}
	scope, scopeID := core.GrantSession, string(rs.req.Session)
	if choice == core.AllowAlways {
		scope, scopeID = core.GrantPrincipal, ""
	}
	g, ok := r.d.Policy.NarrowGrant(pc.action, rs.req.Principal, scope, scopeID, rs.req.User)
	if !ok {
		return
	}
	id, err := r.d.Policy.Grant(ctx, g)
	if err != nil {
		r.log.Error("create grant", "run", rs.id, "err", err)
		return
	}
	r.auditf(ctx, "user:"+string(rs.req.User), "grant.create", string(id), "ok", rs.req.Session, rs.id,
		map[string]any{"via": "card", "grant": id, "scope": scope, "kind": g.Kind, "tool": g.Tool})
}

// park leaves the run parked on its approval (scheduled runs, D23): a
// placeholder result keeps the history valid while the session is used.
func (r *Runner) park(rs *runState, pc *pcall, appr *core.Approval, rest []core.ToolCall) *runEnd {
	ctx := context.WithoutCancel(rs.ctx)
	title := appr.Card.Title
	if title == "" {
		title = cmpOr(pc.spec.Manifest.Title, pc.name)
	}
	placeholder := fmt.Sprintf("Parked: waiting for approval of %s. It has not run.", title)
	res := core.ToolResult{CallID: pc.call.ID, Name: pc.name, Content: placeholder}
	m := &core.Message{Session: rs.req.Session, Run: rs.id, Role: core.MsgTool, Provenance: core.ProvSystem,
		Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &res}}}
	if err := r.appendMessage(ctx, rs.req.Principal, m, nil); err != nil {
		r.log.Error("persist parked placeholder", "run", rs.id, "err", err)
	}
	r.persistNotRun(ctx, rs, rest, parkedSibling)
	ttl := r.cfg().Policy.Approval.ParkTTL.D()
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &runEnd{status: core.RunParked, parkedOn: appr.ID,
		notice: fmt.Sprintf("Parked. Approve any time in the next %s.", humanDuration(ttl))}
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d h", int(d/time.Hour))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return d.String()
}

// cardContext is the card's context line: "Telegram · DM · session “Q3”".
func (r *Runner) cardContext(rs *runState) string {
	var parts []string
	if c := rs.req.Chat; c != nil {
		parts = append(parts, titleCase(c.Channel))
		switch c.Kind {
		case core.ChatDM:
			parts = append(parts, "DM")
		case core.ChatGroup:
			parts = append(parts, "group")
		}
	} else if rs.sess != nil {
		parts = append(parts, titleCase(rs.sess.Origin))
	}
	if rs.req.Trigger.Scheduled() {
		parts = append(parts, string(rs.req.Trigger))
	}
	if rs.sess != nil {
		name := rs.sess.Title
		if name == "" {
			name = string(rs.sess.ID)
		}
		parts = append(parts, "session “"+truncateRunes(name, 60)+"”")
	}
	return strings.Join(parts, " · ")
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Resume continues a parked run after its approval resolved: allowed =>
// execute the stored Action exactly once and continue the loop; denied =>
// record the denial and continue the loop; expired/cancelled => run stopped
// with a notice. It is a no-op when the approval's run is not parked.
func (r *Runner) Resume(ctx context.Context, a *core.Approval) error {
	if a == nil || a.ID == "" || !resolved(a.Status) {
		return nil
	}
	if err := r.ensureInit(ctx); err != nil {
		return err
	}
	// Most resolutions are for runs that never parked; a read keeps them off
	// the writer transaction.
	run, err := r.d.Store.Runs().Get(ctx, core.SystemViewer(), a.Run)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("agent: resume: read run %s: %w", a.Run, err)
	case run.Status != core.RunParked || run.ParkedOn != a.ID:
		return nil
	}
	// The caller's copy is only a signal: status, choice and Action come from
	// the approvals store, so nothing but a recorded resolution runs an Action.
	stored, err := r.d.Approvals.Get(ctx, core.SystemViewer(), a.ID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: resume: read approval %s: %w", a.ID, err)
	}
	a = stored
	if !resolved(a.Status) || a.Run == "" {
		return nil
	}
	stopOnly := a.Status == core.ApprovalExpired || a.Status == core.ApprovalCancelled || r.paused.Load() || r.closing.Load()
	// The status flip is a compare-and-swap inside one transaction, so two
	// resolutions or two runners can never execute the stored Action twice.
	err = r.d.Store.Tx(ctx, func(tx core.Store) error {
		cur, err := tx.Runs().Get(ctx, core.SystemViewer(), a.Run)
		if err != nil {
			return err
		}
		if cur.Status != core.RunParked || cur.ParkedOn != a.ID {
			return errNotParked
		}
		cur.Status = core.RunQueued
		if stopOnly {
			cur.Status, cur.FinishedAt = core.RunStopped, r.now()
		}
		if err := tx.Runs().Update(ctx, cur); err != nil {
			return err
		}
		run = cur
		return nil
	})
	if errors.Is(err, errNotParked) || errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: resume %s: %w", a.Run, err)
	}
	if stopOnly {
		r.endParked(ctx, run, a)
		return nil
	}
	for {
		w, err := r.worker(run.Session)
		if err != nil { // the row stays queued and becomes interrupted at boot
			return err
		}
		w.mu.Lock()
		if w.dead {
			w.mu.Unlock()
			continue
		}
		w.queue = append(w.queue, &resumeJob{run: run, approval: a})
		w.signal()
		w.mu.Unlock()
		return nil
	}
}

func resolved(st core.ApprovalStatus) bool {
	switch st {
	case core.ApprovalAllowed, core.ApprovalDenied, core.ApprovalExpired, core.ApprovalCancelled:
		return true
	}
	return false
}

const pausedParkedNotice = "Bothy is paused by an admin, so the parked run stopped."

// endParked finishes a parked run whose approval expired or was cancelled
// (or that cannot resume because Bothy is paused).
func (r *Runner) endParked(ctx context.Context, run *core.Run, a *core.Approval) {
	text := "The parked action was not approved in time, so the run stopped."
	st := core.TCTimeout
	switch {
	case a.Status == core.ApprovalCancelled:
		text, st = "The parked action was cancelled, so the run stopped.", core.TCDenied
	case a.Status != core.ApprovalExpired && r.paused.Load():
		text, st = pausedParkedNotice, core.TCStopped
	}
	r.closeParkedCall(ctx, run.ID, a.ToolCallID, st)
	sink := r.resumeSink(ctx, run)
	r.notice(ctx, run.Session, run.Principal, run.ID, text, sink)
	ev := r.d.Bus.Publish(core.Event{Type: core.EvRunFinished, Principal: run.Principal, Session: run.Session, Run: run.ID,
		Data: core.RunFinishedData{Status: core.RunStopped, Steps: run.Steps, Usage: run.Usage, Cost: run.Cost}})
	if sink != nil {
		sink.OnEvent(ev)
	}
}

// closeParkedCall finalizes the tool call record of a parked call that will
// not run.
func (r *Runner) closeParkedCall(ctx context.Context, run core.RunID, callID string, st core.ToolCallStatus) {
	recs, err := r.d.Store.Runs().ToolCalls(ctx, core.SystemViewer(), run)
	if err != nil {
		r.log.Warn("parked tool call", "run", run, "err", err)
		return
	}
	for _, rec := range recs {
		if rec.ID == callID {
			rec.Status, rec.FinishedAt = st, r.now()
			r.updateRec(ctx, rec)
		}
	}
}

// finishDetached stops a run that has no live goroutine (queued resume or
// parked); false when it was not open.
func (r *Runner) finishDetached(ctx context.Context, run *core.Run, st core.RunStatus, errText string, by core.UserID) bool {
	ok := false
	err := r.d.Store.Tx(ctx, func(tx core.Store) error {
		cur, err := tx.Runs().Get(ctx, core.SystemViewer(), run.ID)
		if err != nil {
			return err
		}
		if cur.Status.Terminal() {
			return nil
		}
		cur.Status, cur.Error, cur.StoppedBy, cur.FinishedAt = st, errText, by, r.now()
		if err := tx.Runs().Update(ctx, cur); err != nil {
			return err
		}
		*run, ok = *cur, true
		return nil
	})
	if err != nil {
		r.log.Error("stop run", "run", run.ID, "err", err)
		return false
	}
	if ok {
		r.d.Bus.Publish(core.Event{Type: core.EvRunFinished, Principal: run.Principal, Session: run.Session, Run: run.ID,
			Data: core.RunFinishedData{Status: st, Error: errText, Steps: run.Steps, Usage: run.Usage, Cost: run.Cost}})
	}
	return ok
}

// resumeSink picks where a resumed run streams: the schedule's delivery chat
// (or the user's preferred chat) for scheduled runs, the session's chat for
// channel sessions, the bus only for web sessions.
func (r *Runner) resumeSink(ctx context.Context, run *core.Run) core.RunSink {
	h := r.hubOrNil()
	if h == nil {
		return nil
	}
	if run.Trigger.Scheduled() {
		if run.Schedule != "" {
			if sc, err := r.d.Store.Schedules().Get(ctx, core.SystemViewer(), run.Schedule); err == nil && sc.Deliver != nil {
				if sc.Deliver.Channel == "web" {
					return nil
				}
				return h.StreamTo(*sc.Deliver, "final")
			}
		}
		if chat, ok := h.PreferredChat(ctx, run.User); ok {
			return h.StreamTo(chat, "final")
		}
		return nil
	}
	sess, err := r.d.Store.Sessions().Get(ctx, core.SystemViewer(), run.Session)
	if err == nil && sess.Chat != nil {
		return h.StreamTo(*sess.Chat, "stream")
	}
	return nil
}

// runResume continues a parked run in its session worker.
func (r *Runner) runResume(rs *runState, j *resumeJob) {
	defer close(rs.done)
	end := r.startResume(rs, j)
	if end == nil {
		end = r.executeApproved(rs, j.approval)
	}
	if end == nil {
		end = r.loop(rs)
	}
	r.finish(rs, end)
}

func (r *Runner) startResume(rs *runState, j *resumeJob) *runEnd {
	ctx := rs.ctx
	// A Stop that landed between Resume's flip to queued and this job
	// starting has already finished the row.
	if cur, err := r.d.Store.Runs().Get(ctx, core.SystemViewer(), rs.id); err != nil || cur.Status != core.RunQueued {
		return &runEnd{noRow: true}
	}
	if err := r.loadRunContext(ctx, rs); err != nil {
		return failEnd("could not load the session: " + err.Error())
	}
	if r.paused.Load() { // engaged while the resume waited in the queue
		r.closeParkedCall(ctx, rs.id, j.approval.ToolCallID, core.TCStopped)
		rs.emitMu.Lock()
		rs.sink = r.resumeSink(ctx, j.run)
		rs.emitMu.Unlock()
		return &runEnd{status: core.RunStopped, notice: pausedParkedNotice}
	}
	rs.req.Provenance = r.runProvenance(ctx, rs)
	rs.req.Chat = rs.sess.Chat
	rs.emitMu.Lock()
	rs.sink = r.resumeSink(ctx, j.run)
	rs.emitMu.Unlock()
	if end := r.resolveModel(ctx, rs); end != nil {
		return end
	}
	if err := rs.acquire(ctx); err != nil {
		return r.ctxEnd(rs)
	}
	run := j.run
	l := run.Limits
	if l.MaxSteps > 0 {
		l.MaxSteps = max(l.MaxSteps-run.Steps, 1)
	}
	if l.MaxTokens > 0 {
		l.MaxTokens = max(l.MaxTokens-run.Usage.TotalInput()-run.Usage.Output, 1)
	}
	if l.MaxCost > 0 {
		l.MaxCost = max(l.MaxCost-run.Cost, 1)
	}
	meter := r.d.Budget.StartRun(rs.id, run.User, l)
	rs.mu.Lock()
	rs.meter = meter
	rs.run.Status = core.RunRunning
	stopped := rs.stopped
	rs.saveLocked(ctx)
	rs.mu.Unlock()
	if stopped {
		return r.ctxEnd(rs)
	}
	rs.startDeadline()
	rs.setExposed(rs.taint)
	rs.publish(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigResume, Model: rs.chain[0].Ref.String()})
	if rs.taint != 0 {
		rs.publish(core.EvSessionUpdated, core.SessionData{Title: rs.sess.Title, Taint: rs.taint, Epoch: rs.sess.Epoch})
	}
	return nil
}

// runProvenance is the provenance of the message that started the run; the
// floors keep applying to it after a resume. Unknown means untrusted.
func (r *Runner) runProvenance(ctx context.Context, rs *runState) core.Provenance {
	msgs, err := r.d.Store.Messages().Range(ctx, core.SystemViewer(), rs.req.Session, 0, 0)
	if err != nil {
		return core.ProvUntrusted
	}
	for _, m := range msgs {
		if m.Run == rs.id && m.Role == core.MsgUser && m.Kind == core.KindNormal && m.Provenance != core.ProvSystem {
			return m.Provenance
		}
	}
	return core.ProvUntrusted
}

// executeApproved runs the stored Action of the parked call exactly once (or
// records the denial) and appends the outcome as an <approved-action> message.
func (r *Runner) executeApproved(rs *runState, a *core.Approval) *runEnd {
	ctx := rs.ctx
	recs, err := r.d.Store.Runs().ToolCalls(ctx, core.SystemViewer(), rs.id)
	if err != nil {
		return failEnd("could not load the parked call: " + err.Error())
	}
	var rec *core.ToolCallRecord
	for _, x := range recs {
		if x.ID == a.ToolCallID {
			rec = x
		}
	}
	// The card showed the approval's Action; it must also be the call this
	// run planned.
	action := a.Action
	if rec == nil || action == nil || a.Run != rs.id {
		rs.notice(ctx, "The approved action could not be found, so it did not run.")
		return nil
	}
	pc := &pcall{call: core.ToolCall{ID: rec.CallID, Name: action.Tool}, name: action.Tool, action: action, rec: rec,
		dec: a.Decision, decided: true, argsHash: fnv64(action.Payload)}
	pc.out = rs.newOutputLimiter(rec.ID)
	pc.env = r.toolEnv(rs, rec.ID, pc.out)
	pc.spec = core.ToolSpec{Def: core.ToolDef{Name: action.Tool}}
	if a.Status != core.ApprovalAllowed || !allows(a.Choice) {
		pc.rec.Status = core.TCDenied
		return r.finishCall(rs, pc, textResult(core.TCDenied, userDenied), true)
	}
	if rec.Action == nil || rec.Action.Tool != action.Tool || rec.Action.Fingerprint != action.Fingerprint {
		return r.finishCall(rs, pc, textResult(core.TCError,
			"The approved action does not match the planned call, so it did not run."), true)
	}
	t, ok := r.d.Tools.Get(action.Tool)
	if !ok {
		return r.finishCall(rs, pc, textResult(core.TCError,
			fmt.Sprintf("The approved action did not run: the tool %s is no longer available.", action.Tool)), true)
	}
	pc.tool, pc.spec = t, t.Spec()
	r.workspaceOnce(rs)
	r.grantFor(ctx, rs, pc, a.Choice)
	pc.rec.Status = core.TCApproved
	r.updateRec(ctx, pc.rec)
	r.publishToolCall(rs, pc)
	return r.finishCall(rs, pc, r.execute(rs, pc), true)
}
