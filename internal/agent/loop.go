package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

// runEnd says how a run ends (or parks).
type runEnd struct {
	status   core.RunStatus
	err      string
	notice   string
	budget   *core.BudgetError
	parkedOn core.ApprovalID
	noRow    bool // refused before a run row existed
}

func loopEnd() *runEnd {
	return &runEnd{status: core.RunLoopDetected, err: "loop detected",
		notice: "Stopped: I was repeating myself (loop detected). Rephrase the request or press Retry."}
}

func failEnd(msg string) *runEnd {
	return &runEnd{status: core.RunFailed, err: msg, notice: "The run failed: " + msg}
}

func budgetEnd(be *core.BudgetError) *runEnd {
	var what string
	switch be.Kind {
	case core.BudgetSteps:
		what = fmt.Sprintf("its step limit (%d steps). Press Continue to allow 10 more steps", be.Limit)
	case core.BudgetTokens:
		what = fmt.Sprintf("its token limit (%d tokens)", be.Limit)
	case core.BudgetTime:
		what = "its time limit"
	case core.BudgetRunCost:
		what = fmt.Sprintf("its cost limit (%s)", core.Micros(be.Limit))
	case core.BudgetDailyCost:
		what = fmt.Sprintf("today's spending cap (%s)", core.Micros(be.Limit))
	default:
		what = "a budget limit"
	}
	return &runEnd{status: core.RunBudgetExceeded, err: be.Error(), budget: be,
		notice: "Stopped: this run reached " + what + ". Limits are in Settings → Budgets."}
}

// ctxEnd classifies a cancelled run by its cause.
func (r *Runner) ctxEnd(rs *runState) *runEnd {
	cause := context.Cause(rs.ctx)
	var be *core.BudgetError
	switch {
	case errors.As(cause, &be):
		return budgetEnd(be)
	case errors.Is(cause, errShutdown):
		return &runEnd{status: core.RunInterrupted, err: "interrupted by shutdown"}
	}
	return &runEnd{status: core.RunStopped}
}

// loadRunContext loads the session, principal and user of a run.
func (r *Runner) loadRunContext(ctx context.Context, rs *runState) error {
	sess, err := r.d.Store.Sessions().Get(ctx, core.SystemViewer(), rs.req.Session)
	if err != nil {
		return err
	}
	p, err := r.d.Store.Principals().Get(ctx, core.SystemViewer(), sess.Principal)
	if err != nil {
		return err
	}
	u, err := r.d.Store.Users().Get(ctx, p.User)
	if err != nil {
		return err
	}
	rs.sess, rs.principal, rs.user, rs.taint = sess, p, u, sess.Taint
	return nil
}

// resolveModel picks the slot and override and resolves the chain. An
// unknown session override is cleared with a notice (SPEC §5.1 /model).
func (r *Runner) resolveModel(ctx context.Context, rs *runState) *runEnd {
	rs.slot = rs.req.Slot
	if rs.slot == "" {
		rs.slot = rs.sess.ModelSlot
	}
	if rs.slot != core.SlotFast {
		rs.slot = core.SlotMain
	}
	rs.override = nil
	if o := rs.sess.ModelOverride; o != "" {
		ref, err := core.ParseModelRef(o)
		if err == nil {
			if _, ok := r.d.LLM.Model(ref); !ok {
				err = errors.New("unknown model")
			}
		}
		if err != nil {
			rs.notice(ctx, fmt.Sprintf("The model override %q is not a known model, so I cleared it and used the default.", o))
			if s, merr := r.mutateSession(ctx, rs.sess.ID, func(s *core.Session) error { s.ModelOverride = ""; return nil }); merr == nil {
				rs.sess = s
			}
		} else {
			rs.override = &ref
		}
	}
	rs.chain = r.d.LLM.Chain(rs.slot, rs.override)
	if len(rs.chain) == 0 {
		return &runEnd{status: core.RunFailed, err: "no model configured",
			notice: fmt.Sprintf("No model is configured for the %s slot. Add one in Settings → Models.", rs.slot)}
	}
	return nil
}

// runTurn executes one user turn on the session worker.
func (r *Runner) runTurn(rs *runState) {
	defer close(rs.done)
	end := r.startTurn(rs)
	if end == nil {
		end = r.loop(rs)
	}
	r.finish(rs, end)
}

func (r *Runner) startTurn(rs *runState) *runEnd {
	ctx := rs.ctx
	req := rs.req
	if err := r.loadRunContext(ctx, rs); err != nil {
		r.log.Error("start run", "session", req.Session, "err", err)
		return &runEnd{noRow: true}
	}
	if rs.sess.Archived {
		return &runEnd{noRow: true}
	}
	if err := r.d.Budget.CheckDaily(ctx, req.User); err != nil {
		r.refuseDaily(ctx, rs.sess, req, err)
		return &runEnd{noRow: true}
	}
	limits := r.d.Budget.DefaultLimits(req.Trigger)
	if req.Limits != nil {
		limits = *req.Limits
	}
	rs.mu.Lock()
	rs.run = core.Run{ID: rs.id, Session: req.Session, Principal: req.Principal, User: req.User, Trigger: req.Trigger,
		Schedule: req.Schedule, Status: core.RunQueued, Limits: limits, CreatedAt: r.now()}
	if rs.stopped {
		rs.run.Status = core.RunStopped
	}
	err := r.d.Store.Runs().Create(context.WithoutCancel(ctx), &rs.run)
	rs.rowCreated = err == nil
	rs.mu.Unlock()
	if err != nil {
		r.log.Error("create run", "session", req.Session, "err", err)
		return &runEnd{noRow: true}
	}
	if ctx.Err() != nil {
		return r.ctxEnd(rs)
	}
	if end := r.resolveModel(ctx, rs); end != nil {
		r.persistTurnOrLog(ctx, rs)
		return end
	}
	if err := r.ensureSnapshot(ctx, rs); err != nil {
		r.persistTurnOrLog(ctx, rs)
		return failEnd(err.Error())
	}
	if err := r.persistTurn(ctx, rs); err != nil {
		return failEnd(err.Error())
	}
	if err := rs.acquire(ctx); err != nil {
		return r.ctxEnd(rs)
	}
	meter := r.d.Budget.StartRun(rs.id, req.User, limits)
	rs.mu.Lock()
	rs.meter = meter
	rs.run.StartedAt = r.now()
	if !rs.stopped {
		rs.run.Status = core.RunRunning
	}
	rs.saveLocked(ctx)
	rs.mu.Unlock()
	if ctx.Err() != nil {
		return r.ctxEnd(rs)
	}
	rs.startDeadline()
	rs.setExposed(rs.taint)
	rs.publish(core.EvRunStarted, core.RunStartedData{Trigger: req.Trigger, Model: rs.chain[0].Ref.String()})
	if rs.taint != 0 {
		rs.publish(core.EvSessionUpdated, core.SessionData{Title: rs.sess.Title, Taint: rs.taint, Epoch: rs.sess.Epoch})
	}
	return nil
}

// persistTurn stores the turn (ephemeral API history, then the user message
// with its turn-context) while holding the worker lock, so steer messages
// accepted meanwhile merge into it or are ordered after it.
func (r *Runner) persistTurn(ctx context.Context, rs *runState) error {
	w := rs.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if rs.turnPersisted {
		return nil
	}
	reqs := slices.Clone(rs.turn.reqs)
	req := reqs[0]
	text, memSeen, pinsSeen, err := r.turnContext(ctx, rs, rs.sess, req)
	if err != nil {
		return err
	}
	for _, e := range req.Ephemeral {
		m := e
		m.ID, m.Seq, m.Session, m.Run, m.Kind = 0, 0, req.Session, rs.id, core.KindNormal
		if m.Role != core.MsgAssistant {
			m.Role = core.MsgUser
		}
		if m.Provenance == "" {
			m.Provenance = req.Provenance
		}
		m.Tokens = r.d.LLM.Estimate(m.PlainText(), r.headRef(rs))
		if err := r.appendMessage(ctx, req.Principal, &m, nil); err != nil {
			return fmt.Errorf("agent: persist history: %w", err)
		}
	}
	parts := append([]core.Part{{Type: core.PartContext, Text: text}}, inputsOf(reqs)...)
	m := &core.Message{Session: req.Session, Run: rs.id, Role: core.MsgUser, Parts: parts, Provenance: req.Provenance,
		Sender: req.Sender}
	m.Tokens = r.d.LLM.Estimate(plainText(parts)+text, r.headRef(rs))
	if err := r.appendMessage(ctx, req.Principal, m, nil); err != nil {
		return fmt.Errorf("agent: persist turn: %w", err)
	}
	rs.turnPersisted = true
	w.merged = 0
	legs := core.Taint(0)
	if req.Provenance == core.ProvUntrusted {
		legs |= core.TaintUntrusted
	}
	s, err := r.mutateSession(ctx, req.Session, func(s *core.Session) error {
		s.MemorySeen, s.PinsSeen = max(s.MemorySeen, memSeen), max(s.PinsSeen, pinsSeen)
		s.LastMessageAt = r.now()
		s.Taint |= legs
		return nil
	})
	if err != nil {
		return fmt.Errorf("agent: update session: %w", err)
	}
	if s.Taint != rs.taint {
		rs.taint = s.Taint
		rs.setExposed(s.Taint)
	}
	rs.sess = s
	r.recordURLs(ctx, req.Session, req.Provenance, inputsOf(reqs))
	return nil
}

// persistTurnOrLog keeps the user's message when the run fails before it
// could start, so the transcript shows what was asked.
func (r *Runner) persistTurnOrLog(ctx context.Context, rs *runState) {
	if err := r.persistTurn(ctx, rs); err != nil {
		r.log.Error("persist turn", "run", rs.id, "err", err)
	}
}

// recordURLs feeds URL provenance (floor F8) from the user's own messages.
func (r *Runner) recordURLs(ctx context.Context, s core.SessionID, prov core.Provenance, parts []core.Part) {
	if prov != core.ProvUser {
		return
	}
	if urls := extractURLs(plainText(parts)); len(urls) > 0 {
		if err := r.d.Store.Sessions().AddURLs(context.WithoutCancel(ctx), s, urls, "user"); err != nil {
			r.log.Warn("record session urls", "session", s, "err", err)
		}
	}
}

func (r *Runner) headRef(rs *runState) core.ModelRef {
	if len(rs.chain) > 0 {
		return rs.chain[0].Ref
	}
	return core.ModelRef{}
}

// persistSteer stores a message for the active run; w.mu is held.
func (r *Runner) persistSteer(ctx context.Context, a *runState, sess *core.Session, req core.RunRequest) error {
	text, memSeen, pinsSeen, err := r.turnContext(ctx, a, sess, req)
	if err != nil {
		return err
	}
	parts := append([]core.Part{{Type: core.PartContext, Text: text}}, req.Input...)
	m := &core.Message{Session: req.Session, Run: a.id, Role: core.MsgUser, Kind: core.KindSteer, Parts: parts,
		Provenance: req.Provenance, Sender: req.Sender}
	if err := r.appendMessage(ctx, req.Principal, m, nil); err != nil {
		return fmt.Errorf("agent: persist steer: %w", err)
	}
	if _, err := r.mutateSession(ctx, req.Session, func(s *core.Session) error {
		s.MemorySeen, s.PinsSeen = max(s.MemorySeen, memSeen), max(s.PinsSeen, pinsSeen)
		s.LastMessageAt = r.now()
		return nil
	}); err != nil {
		r.log.Warn("update session after steer", "session", req.Session, "err", err)
	}
	r.recordURLs(ctx, req.Session, req.Provenance, req.Input)
	return nil
}

// loop is the step loop of SPEC §5.5 (steps 3–9).
func (r *Runner) loop(rs *runState) *runEnd {
	ctx := rs.ctx
	showThinking := r.cfg().Agent.ShowThinking
	for {
		if r.closing.Load() {
			return &runEnd{status: core.RunInterrupted, err: "interrupted by shutdown",
				notice: "I was restarted mid-task. Send 'retry' to continue."}
		}
		if ctx.Err() != nil {
			return r.ctxEnd(rs)
		}
		asm, head, err := r.prepareStep(ctx, rs)
		if err != nil {
			if ctx.Err() != nil {
				return r.ctxEnd(rs)
			}
			return failEnd(err.Error())
		}
		var be *core.BudgetError
		if err := rs.meter.Step(); err != nil {
			if errors.As(err, &be) {
				return budgetEnd(be)
			}
			return failEnd(err.Error())
		}
		info := rs.chain[0]
		info.MaxOutput = head.req.MaxTokens
		if err := rs.meter.BeforeCall(ctx, head.est, info); err != nil {
			if errors.As(err, &be) {
				return budgetEnd(be)
			}
			return failEnd(err.Error())
		}
		step := rs.totals().Steps
		rs.publish(core.EvRunStatus, core.RunStatusData{Status: "thinking"})
		text := rs.newStreamer(core.EvRunDelta, true)
		think := rs.newStreamer(core.EvRunThinking, false)
		streamed := false
		resp, ci, err := r.d.LLM.Complete(ctx, rs.slot, rs.override, asm.build, func(ev core.StreamEvent) error {
			switch ev.Type {
			case core.StreamTextDelta:
				streamed = true
				text.add(ev.Text)
			case core.StreamThinkingDelta:
				streamed = true
				if showThinking {
					think.add(ev.Text)
				}
			case core.StreamToolCallStart:
				streamed = true
			}
			return nil
		})
		text.close()
		think.close()
		if err != nil {
			if ci.Usage != (core.Usage{}) || ci.Cost > 0 {
				r.recordCall(ctx, rs, asm, &ci, step, "agent")
			}
			if ctx.Err() != nil {
				return r.ctxEnd(rs)
			}
			var pe *core.ProviderError
			if errors.As(err, &pe) && pe.Kind == core.ErrKindContextLength && !streamed && !rs.ctxRetried {
				rs.ctxRetried = true
				r.compactInRun(ctx, rs, true)
				continue
			}
			msg := r.redact(err.Error())
			if pe != nil {
				msg = r.redact(pe.Msg)
			}
			if streamed {
				if resp != nil {
					r.persistAssistant(ctx, rs, resp)
				}
				return &runEnd{status: core.RunFailed, err: "model error mid-reply: " + msg,
					notice: "Model error mid-reply. Press Retry to try again."}
			}
			return &runEnd{status: core.RunFailed, err: "model error: " + msg,
				notice: "The model could not answer (" + truncateRunes(msg, 200) + "). Press Retry to try again."}
		}
		rs.ctxRetried = false
		r.recordCall(ctx, rs, asm, &ci, step, "agent")
		rs.lastEst = head.est
		calls := r.persistAssistant(ctx, rs, resp)
		reply := strings.TrimSpace(resp.Message.PlainText())
		var h uint64
		if reply != "" {
			h = nonZero(fnv64([]byte(reply)))
		}
		if rs.meter.ObserveText(h) == core.LoopAbort {
			return loopEnd()
		}
		if len(calls) == 0 {
			rs.w.mu.Lock()
			more := rs.w.steers > 0
			if !more {
				rs.finishing = true
			}
			rs.w.mu.Unlock()
			if more {
				continue
			}
			return &runEnd{status: core.RunDone}
		}
		if end := r.runTools(rs, step, calls); end != nil {
			return end
		}
	}
}

// prepareStep refreshes the session, folds steer messages into history,
// assembles the request for the chain head and compacts first when the
// estimate crosses the profile trigger (SPEC §5.5 step 3).
func (r *Runner) prepareStep(ctx context.Context, rs *runState) (*assembler, assembled, error) {
	build := func() (*assembler, assembled, error) {
		sess, err := r.d.Store.Sessions().Get(ctx, core.SystemViewer(), rs.req.Session)
		if err != nil {
			return nil, assembled{}, err
		}
		rs.sess, rs.taint = sess, sess.Taint|rs.taint
		rs.setExposed(rs.taint)
		rs.w.mu.Lock()
		rs.w.steers = 0
		rs.w.mu.Unlock()
		hist, err := r.loadHistory(ctx, rs)
		if err != nil {
			return nil, assembled{}, err
		}
		asm := r.newAssembler(rs, hist)
		return asm, asm.assemble(rs.chain[0]), nil
	}
	asm, head, err := build()
	if err != nil {
		return nil, assembled{}, err
	}
	if head.est > r.compactThreshold(rs.chain[0]) {
		r.compactInRun(ctx, rs, false)
		return build()
	}
	return asm, head, nil
}

// compactThreshold is trigger × window − reserve, lowered to a price tier's
// threshold so a call is not repriced (SPEC §5.5 step 3).
func (r *Runner) compactThreshold(m core.ModelInfo) int {
	c := r.cfg()
	p := profileFor(c, m.Context)
	t := int(triggerFor(c, p)*float64(m.Context)) - reserveFor(c, p, m.Context)
	for _, tier := range m.Price.Tiers {
		if tier.AboveInput > 0 {
			t = min(t, tier.AboveInput)
		}
	}
	return t
}

// persistAssistant stores the assistant message and returns its tool calls,
// with ids filled in where the provider sent none.
func (r *Runner) persistAssistant(ctx context.Context, rs *runState, resp *core.Response) []core.ToolCall {
	msg := resp.Message
	msg.ID, msg.Seq, msg.Session, msg.Run = 0, 0, rs.req.Session, rs.id
	msg.Role, msg.Kind, msg.Provenance = core.MsgAssistant, core.KindNormal, core.ProvSystem
	msg.Parts = slices.Clone(msg.Parts)
	var calls []core.ToolCall
	step := rs.totals().Steps
	for i, p := range msg.Parts {
		if p.Type != core.PartToolCall || p.ToolCall == nil {
			continue
		}
		tc := *p.ToolCall
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d_%d", step, i)
		}
		msg.Parts[i].ToolCall = &tc
		calls = append(calls, tc)
	}
	if len(msg.Parts) == 0 {
		return nil
	}
	msg.Tokens = resp.Usage.Output
	msg.CreatedAt = r.now()
	if err := r.appendMessage(ctx, rs.req.Principal, &msg, nil); err != nil {
		r.log.Error("persist assistant message", "run", rs.id, "err", err)
	}
	return calls
}

// recordCall writes the llm_calls row, meters the call, calibrates the
// estimator and publishes run.usage.
func (r *Runner) recordCall(ctx context.Context, rs *runState, asm *assembler, ci *core.CallInfo, step int, purpose string) {
	ctx = context.WithoutCancel(ctx)
	ci.Purpose, ci.Step = purpose, step
	if ci.ID == "" {
		ci.ID = core.NewID(core.PrefixLLMCall)
	}
	var built assembled
	ok := false
	if asm != nil {
		if built, ok = asm.result(ci.Ref); !ok && len(rs.chain) > 0 {
			built, ok = asm.result(rs.chain[0].Ref)
		}
	}
	if ok {
		ci.Breakdown = scaleBreakdown(built.bd, ci.Usage.TotalInput())
		ci.PrefixHash = built.hash
	}
	if err := r.d.Store.Runs().AddLLMCall(ctx, rs.id, rs.req.Principal, rs.req.User, *ci); err != nil {
		r.log.Error("record llm call", "run", rs.id, "err", err)
	}
	if err := rs.meter.AfterCall(ctx, *ci); err != nil {
		r.log.Error("meter llm call", "run", rs.id, "err", err)
	}
	if ok && !ci.UsageEstimated && ci.Usage.TotalInput() > 0 && built.est > 0 {
		r.d.LLM.Calibrate(ci.Ref, built.est, ci.Usage.TotalInput())
	}
	if purpose == "agent" && ci.PrefixHash != "" {
		r.checkPrefix(rs, ci.PrefixHash)
	}
	tot := rs.totals()
	rs.mu.Lock()
	if m := ci.Ref.String(); ci.Ref.Model != "" && !slices.Contains(rs.run.Models, m) {
		rs.run.Models = append(rs.run.Models, m)
	}
	rs.run.Steps, rs.run.ToolCalls, rs.run.Usage, rs.run.Cost = tot.Steps, tot.ToolCalls, tot.Usage, tot.Cost
	rs.saveLocked(ctx)
	rs.mu.Unlock()
	spent, limit, err := r.d.Budget.Daily(ctx, rs.req.User)
	if err != nil {
		r.log.Warn("daily spend", "err", err)
	}
	rs.publish(core.EvRunUsage, core.UsageData{Call: *ci, RunCost: tot.Cost, DayCost: spent, DayLimit: limit})
}

// checkPrefix logs a cache-stability bug when the prefix changes within one
// session epoch.
func (r *Runner) checkPrefix(rs *runState, hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, ok := r.prefix[rs.req.Session]
	if ok && prev.epoch == rs.sess.Epoch && prev.hash != hash {
		r.log.Warn("prompt prefix changed within an epoch (cache-stability bug)", "session", rs.req.Session,
			"epoch", rs.sess.Epoch, "from", prev.hash, "to", hash)
	}
	r.prefix[rs.req.Session] = prefixMark{epoch: rs.sess.Epoch, hash: hash}
}

// finish records the final status (SPEC §5.5 step 10).
func (r *Runner) finish(rs *runState, end *runEnd) {
	ctx := context.WithoutCancel(rs.ctx)
	rs.stopDeadline()
	defer rs.releaseSems()
	if end == nil || end.noRow {
		return
	}
	if end.status == "" {
		end.status = core.RunDone
	}
	tot := rs.totals()
	rs.mu.Lock()
	stopped := rs.stopped
	if stopped {
		end = &runEnd{status: core.RunStopped}
	}
	rs.run.Status, rs.run.Error = end.status, end.err
	rs.run.Steps, rs.run.ToolCalls, rs.run.Usage, rs.run.Cost = tot.Steps, tot.ToolCalls, tot.Usage, tot.Cost
	if end.status == core.RunParked {
		rs.run.ParkedOn = end.parkedOn
	} else if rs.run.FinishedAt.IsZero() {
		rs.run.FinishedAt = r.now()
	}
	rs.saveLocked(ctx)
	rs.mu.Unlock()
	if end.status == core.RunParked {
		rs.publish(core.EvRunParked, core.RunParkedData{Approval: end.parkedOn})
	} else {
		rs.publishFinished()
	}
	if end.notice != "" {
		rs.notice(ctx, end.notice)
	}
	if end.budget != nil {
		r.auditf(ctx, "agent:"+string(rs.id), "budget.exceeded", string(rs.id), "denied", rs.req.Session, rs.id,
			map[string]any{"kind": end.budget.Kind, "limit": end.budget.Limit, "used": end.budget.Used})
	}
	if end.status != core.RunDone || rs.sess == nil {
		return
	}
	if rs.sess.Title == "" {
		r.titleAsync(rs)
	}
	if len(rs.chain) > 0 && rs.lastEst*10 > rs.chain[0].Context*6 {
		r.compactInRun(ctx, rs, false)
	}
}
