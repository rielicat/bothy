package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	defaultToolTimeout = 120 * time.Second
	execGrace          = 10 * time.Second // the box reports its own timeout first
	previewMax         = 2 << 10
	maxToolErrors      = 5
	loopWarnNote       = "You repeated an identical call 3 times; change approach or answer the user."
	parkedSibling      = "Not executed: an earlier call in this step is waiting for approval."
)

var errToolTimeout = errors.New("tool timed out")

// pcall is one tool call moving through the pipeline of SPEC §5.7.
type pcall struct {
	call     core.ToolCall // as the model wrote it (provider call id)
	name     string        // target tool after unwrapping use_tool
	tool     core.Tool
	spec     core.ToolSpec
	args     json.RawMessage
	action   *core.Action
	rec      *core.ToolCallRecord
	dec      core.Decision
	decided  bool
	fail     string // early error result (unknown tool, bad args, plan error)
	failSt   core.ToolCallStatus
	argsHash uint64
	env      *core.ToolEnv
	out      *outputLimiter
}

type execResult struct {
	res      *core.ToolResult
	status   core.ToolCallStatus
	dur      time.Duration
	egressed bool
}

func (r *Runner) toolEnv(rs *runState, callID string, out *outputLimiter) *core.ToolEnv {
	env := &core.ToolEnv{
		User:        rs.req.User,
		Principal:   *rs.principal,
		Session:     rs.req.Session,
		Run:         rs.id,
		CallID:      callID,
		Trigger:     rs.req.Trigger,
		Schedule:    rs.req.Schedule,
		Workspace:   r.workspaceDir(rs.principal),
		BoxKey:      boxKey(rs.req.Session),
		Sandbox:     r.d.Sandbox,
		Chat:        rs.req.Chat,
		Taint:       rs.taint,
		Interactive: rs.req.Trigger.Interactive(),
		Selection:   rs.sel,
		Emit:        func(core.EventData) {},
	}
	if rs.user != nil {
		env.Role = rs.user.Role
	}
	if r.d.Egress != nil {
		env.HTTP = r.d.Egress.PublicClient(rs.req.Session, rs.req.Principal)
	}
	if out != nil {
		env.Emit = out.emit
	}
	return env
}

// runTools runs one step's tool calls in model order. Consecutive calls that
// are reads, auto-allowed and not exec run in parallel; they are decided with
// the taint the batch could reach, so running them together never lets one
// slip past a floor that sequential execution would have applied.
func (r *Runner) runTools(rs *runState, step int, calls []core.ToolCall) *runEnd {
	ctx := rs.ctx
	limit := max(r.cfg().Agent.ParallelReadTools, 1)
	var batch []*pcall
	projected := rs.taint
	flush := func() *runEnd {
		if len(batch) == 0 {
			return nil
		}
		results := make([]execResult, len(batch))
		var wg sync.WaitGroup
		for i, pc := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = r.execute(rs, pc)
			}()
		}
		wg.Wait()
		var end *runEnd
		for i, pc := range batch {
			if e := r.finishCall(rs, pc, results[i], false); e != nil && end == nil {
				end = e
			}
		}
		batch, projected = nil, rs.taint
		return end
	}
	for i, call := range calls {
		if ctx.Err() != nil {
			if end := flush(); end != nil {
				return end
			}
			return r.ctxEnd(rs)
		}
		pc := r.prepareCall(rs, step, call)
		if pc.fail != "" {
			if end := flush(); end != nil {
				return end
			}
			res := &core.ToolResult{Content: pc.fail, IsError: true}
			if end := r.finishCall(rs, pc, execResult{res: res, status: pc.failSt}, false); end != nil {
				return end
			}
			continue
		}
		if rs.meter.ObserveTool(pc.name, pc.argsHash, 0) == core.LoopAbort {
			end := flush()
			r.markNotRun(ctx, rs, pc, core.TCStopped, "loop detected")
			if end != nil {
				return end
			}
			return loopEnd()
		}
		m := pc.spec.Manifest
		if pc.action.SideEffect == core.SERead && !m.Exec {
			pc.dec, pc.decided = r.decide(ctx, rs, pc, projected), true
			if pc.dec.Verdict == core.VerdictAuto {
				if len(batch) >= limit {
					if end := flush(); end != nil {
						return end
					}
				}
				r.publishToolCall(rs, pc)
				batch = append(batch, pc)
				projected |= m.Taint
				continue
			}
		}
		if end := flush(); end != nil {
			return end
		}
		if !pc.decided {
			pc.dec = r.decide(ctx, rs, pc, rs.taint)
		}
		r.publishToolCall(rs, pc)
		if end := r.handleDecision(rs, pc, calls[i+1:]); end != nil {
			return end
		}
	}
	return flush()
}

// prepareCall resolves, validates and plans a call and records it.
func (r *Runner) prepareCall(rs *runState, step int, call core.ToolCall) *pcall {
	ctx := rs.ctx
	pc := &pcall{call: call, name: call.Name, args: call.Args}
	id := core.NewID(core.PrefixToolCall)
	pc.out = rs.newOutputLimiter(id)
	pc.env = r.toolEnv(rs, id, pc.out)
	pc.rec = &core.ToolCallRecord{ID: id, Run: rs.id, Session: rs.req.Session, Principal: rs.req.Principal,
		Step: step, CallID: call.ID, Tool: call.Name, Source: "builtin", SideEffect: core.SERead,
		Status: core.TCPlanned, CreatedAt: r.now()}
	fail := func(st core.ToolCallStatus, msg string) *pcall {
		pc.fail, pc.failSt = msg, st
		pc.rec.Status = st
		pc.argsHash = fnv64(append([]byte(pc.name+"\x00"), pc.args...))
		if err := r.d.Store.Runs().AddToolCall(context.WithoutCancel(ctx), pc.rec); err != nil {
			r.log.Error("record tool call", "run", rs.id, "err", err)
		}
		return pc
	}
	if call.Name == "use_tool" {
		var u struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(call.Args, &u); err != nil || u.Name == "" || u.Name == "use_tool" {
			return fail(core.TCInvalid, `use_tool needs {"name": "<tool>", "args": {…}}. Use tool_search to find tools.`)
		}
		pc.name, pc.args = u.Name, u.Args
		pc.rec.Tool = u.Name
	}
	if trimmed := bytes.TrimSpace(pc.args); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		pc.args = json.RawMessage("{}")
	}
	if rs.profile == profileTextOnly {
		return fail(core.TCInvalid, "This model's window is too small for tools; answer in text.")
	}
	t, ok := r.d.Tools.Get(pc.name)
	if !ok || !r.d.Tools.Available(pc.name, pc.env, rs.sel) {
		return fail(core.TCInvalid, fmt.Sprintf("Unknown tool %s. Use tool_search.", pc.name))
	}
	pc.tool, pc.spec = t, t.Spec()
	pc.rec.Source, pc.rec.SideEffect = pc.spec.Source, pc.spec.Manifest.SideEffect
	if err := validateArgs(pc.spec.Def.Schema, pc.args); err != nil {
		return fail(core.TCInvalid, fmt.Sprintf("Invalid arguments for %s: %v", pc.name, err))
	}
	r.workspaceOnce(rs)
	a, err := t.Plan(ctx, pc.env, pc.args)
	if err == nil && a == nil {
		err = errors.New("no action")
	}
	if err != nil {
		return fail(core.TCError, fmt.Sprintf("%s could not plan this call: %s", pc.name, r.redact(err.Error())))
	}
	if err := normalizeAction(a, pc.name, pc.spec.Manifest.SideEffect, pc.args); err != nil {
		return fail(core.TCError, fmt.Sprintf("%s returned an invalid action: %v", pc.name, err))
	}
	pc.action = a
	pc.argsHash = fnv64(a.Payload)
	pc.rec.SideEffect, pc.rec.Action = a.SideEffect, a
	if err := r.d.Store.Runs().AddToolCall(context.WithoutCancel(ctx), pc.rec); err != nil {
		r.log.Error("record tool call", "run", rs.id, "err", err)
	}
	return pc
}

// normalizeAction enforces what the rest of the system relies on: the Action
// names its tool, carries canonical payload bytes and their fingerprint, and
// never declares a lower side effect than the manifest.
func normalizeAction(a *core.Action, name string, floor core.SideEffect, args json.RawMessage) error {
	a.Tool = name
	if !a.SideEffect.Valid() || a.SideEffect < floor {
		a.SideEffect = floor
	}
	payload := a.Payload
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = args
	}
	canon, err := core.CanonicalJSON(payload)
	if err != nil {
		return err
	}
	a.Payload = canon
	a.Fingerprint = core.ActionFingerprint(name, canon)
	return nil
}

func (r *Runner) decide(ctx context.Context, rs *runState, pc *pcall, taint core.Taint) core.Decision {
	in := core.PolicyInput{
		User: rs.req.User, Principal: *rs.principal, Session: rs.req.Session, Schedule: rs.req.Schedule,
		Trigger: rs.req.Trigger, Action: pc.action, Spec: pc.spec, Taint: taint, Provenance: rs.req.Provenance,
		Chat: rs.req.Chat,
	}
	if rs.user != nil {
		in.Role = rs.user.Role
	}
	d := r.d.Policy.Decide(ctx, in)
	switch d.Verdict {
	case core.VerdictAuto, core.VerdictAsk, core.VerdictDeny:
	default:
		d = core.Decision{Verdict: core.VerdictDeny, Rule: "invalid_decision", Reason: "the policy returned no verdict"}
	}
	pc.rec.Verdict, pc.rec.Rule = d.Verdict, d.Rule
	return d
}

func (r *Runner) publishToolCall(rs *runState, pc *pcall) {
	rs.publish(core.EvToolCall, core.ToolCallData{CallID: pc.rec.ID, Tool: pc.name, Title: pc.spec.Manifest.Title,
		Action: r.redactAction(pc.action), Verdict: pc.dec.Verdict, Rule: pc.dec.Rule})
}

func (r *Runner) handleDecision(rs *runState, pc *pcall, rest []core.ToolCall) *runEnd {
	switch pc.dec.Verdict {
	case core.VerdictDeny:
		r.auditf(rs.ctx, "agent:"+string(rs.id), "tool.denied", pc.name, "denied", rs.req.Session, rs.id,
			map[string]any{"tool": pc.name, "fingerprint": pc.action.Fingerprint, "rule": pc.dec.Rule})
		text := fmt.Sprintf("Denied by policy (%s): %s", pc.dec.Rule, pc.dec.Reason)
		return r.finishCall(rs, pc, textResult(core.TCDenied, text), false)
	case core.VerdictAsk:
		return r.ask(rs, pc, rest)
	}
	return r.finishCall(rs, pc, r.execute(rs, pc), false)
}

func textResult(st core.ToolCallStatus, text string) execResult {
	return execResult{res: &core.ToolResult{Content: text, IsError: st != core.TCOK}, status: st}
}

// execute runs the decided Action with the tool's timeout.
func (r *Runner) execute(rs *runState, pc *pcall) execResult {
	ctx := rs.ctx
	m := pc.spec.Manifest
	pc.rec.Status = core.TCRunning
	r.updateRec(ctx, pc.rec)
	rs.publish(core.EvRunStatus, core.RunStatusData{Status: "tool", Detail: r.redact(actionDetail(pc.spec, pc.action))})
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = defaultToolTimeout
	}
	if m.Exec {
		if t := r.cfg().Sandbox.ExecTimeout.D(); t > 0 {
			timeout = t
		}
		timeout += execGrace
		if err := r.ensureBox(ctx, rs); err != nil {
			pc.out.close()
			return textResult(core.TCError, "The sandbox is unavailable: "+r.redact(err.Error()))
		}
		if r.d.Egress != nil {
			r.d.Egress.Bind(boxKey(rs.req.Session), core.EgressBinding{
				Run: rs.id, ToolCallID: pc.rec.ID, User: rs.req.User, Role: pc.env.Role, Trigger: rs.req.Trigger,
				Schedule: rs.req.Schedule, Interactive: rs.req.Trigger.Interactive(), Chat: rs.req.Chat,
				Context: r.cardContext(rs),
			})
			r.d.Egress.TakeActivity(boxKey(rs.req.Session))
		}
	}
	tctx, cancel := context.WithTimeoutCause(ctx, timeout, errToolTimeout)
	defer cancel()
	start := r.now()
	res, err := pc.tool.Exec(tctx, pc.env, pc.action)
	pc.out.close()
	er := execResult{res: res, dur: r.now().Sub(start)}
	if m.Exec && r.d.Egress != nil {
		er.egressed = r.d.Egress.TakeActivity(boxKey(rs.req.Session))
	}
	switch {
	case ctx.Err() != nil:
		er.status = core.TCStopped
		if er.res == nil {
			er.res = &core.ToolResult{Content: "Stopped before finishing.", IsError: true}
		}
	case errors.Is(context.Cause(tctx), errToolTimeout):
		er.status = core.TCTimeout
		er.res = &core.ToolResult{Content: fmt.Sprintf("Timed out after %s.", timeout), IsError: true}
	case err != nil:
		er.status = core.TCError
		er.res = &core.ToolResult{Content: "Error: " + r.redact(err.Error()), IsError: true}
	case er.res == nil:
		er.status, er.res = core.TCOK, &core.ToolResult{}
	case er.res.IsError:
		er.status = core.TCError
	default:
		er.status = core.TCOK
	}
	return er
}

func (r *Runner) ensureBox(ctx context.Context, rs *runState) error {
	if rs.boxReady {
		return nil
	}
	if r.d.Sandbox == nil {
		return core.ErrNoSandbox
	}
	c := r.cfg().Sandbox
	profile := core.ProfileBase
	if c.Profile == string(core.ProfileNode) {
		profile = core.ProfileNode
	}
	err := r.d.Sandbox.Ensure(ctx, core.BoxSpec{
		Key:       boxKey(rs.req.Session),
		Profile:   profile,
		Workspace: rs.principal.Workspace,
		Limits:    core.BoxLimits{MemoryMB: c.Limits.MemoryMB, CPUs: c.Limits.CPUs, Pids: c.Limits.Pids},
		Egress:    core.BoxEgress{Session: rs.req.Session, Principal: rs.req.Principal},
	})
	if err != nil {
		return err
	}
	rs.boxReady = true
	return nil
}

func (r *Runner) updateRec(ctx context.Context, rec *core.ToolCallRecord) {
	if err := r.d.Store.Runs().UpdateToolCall(context.WithoutCancel(ctx), rec); err != nil {
		r.log.Error("update tool call", "id", rec.ID, "err", err)
	}
}

// finishCall post-processes a result (SPEC §5.7 i): redact, cap with a spill
// file, mark untrusted, loop-check, advance taint, persist, publish, audit.
// approved renders the result as the <approved-action> message of a resumed
// parked run instead of a tool-role message.
func (r *Runner) finishCall(rs *runState, pc *pcall, er execResult, approved bool) *runEnd {
	ctx := context.WithoutCancel(rs.ctx)
	res := *er.res
	res.CallID, res.Name = pc.call.ID, pc.name
	content := r.redact(res.Content)
	full := content
	capBytes := toolOutputCap(r.cfg(), profileFor(r.cfg(), rs.chain[0].Context), rs.chain[0].Context) * 36 / 10
	if len(content) > capBytes {
		elided := len(content) - capBytes
		ref, err := r.spill(rs.principal, pc.rec.ID, full)
		note := fmt.Sprintf("[… %d bytes elided]", elided)
		if err == nil {
			res.FullRef = ref
			note = fmt.Sprintf("[… %d bytes elided; full output at %s]", elided, ref)
		} else {
			r.log.Warn("spill tool output", "call", pc.rec.ID, "err", err)
		}
		content = headTail(content, capBytes, note)
		res.Truncated = true
	}
	res.Untrusted = res.Untrusted || er.egressed || pc.spec.Manifest.Taint.Has(core.TaintUntrusted)
	isErr := "0"
	if res.IsError {
		isErr = "1"
	}
	var end *runEnd
	if er.status != core.TCStopped && rs.meter != nil {
		switch rs.meter.ObserveTool(pc.name, pc.argsHash, nonZero(fnv64([]byte(isErr+content)))) {
		case core.LoopWarn:
			content += "\n\n" + loopWarnNote
		case core.LoopAbort:
			end = loopEnd()
		}
	}
	res.Content = content
	ran := er.status == core.TCOK || er.status == core.TCError || er.status == core.TCTimeout
	if pc.action != nil && pc.fail == "" && ran {
		next := r.d.Policy.NextTaint(rs.taint, pc.spec, pc.action, &res, er.egressed)
		if err := r.addTaint(ctx, rs, next); err != nil {
			r.log.Error("taint", "run", rs.id, "err", err)
		}
	}

	var msg *core.Message
	if approved {
		msg = approvedMessage(rs, pc, res, er.status)
	} else {
		prov := core.ProvTool
		if res.Untrusted {
			prov = core.ProvUntrusted
		}
		msg = &core.Message{Session: rs.req.Session, Run: rs.id, Role: core.MsgTool, Provenance: prov,
			Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &res}}}
	}
	msg.Tokens = r.d.LLM.Estimate(content, rs.chain[0].Ref)
	if err := r.appendMessage(ctx, rs.req.Principal, msg, nil); err != nil {
		r.log.Error("persist tool result", "run", rs.id, "err", err)
	}

	rec := pc.rec
	rec.Status, rec.ResultPreview, rec.ResultBytes = er.status, truncateBytes(content, previewMax), len(full)
	rec.FullRef, rec.Untrusted, rec.Duration, rec.FinishedAt = res.FullRef, res.Untrusted, er.dur, r.now()
	r.updateRec(ctx, rec)
	rs.publish(core.EvToolResult, core.ToolResultData{CallID: rec.ID, Status: er.status, Preview: rec.ResultPreview,
		Bytes: len(full), Ms: er.dur.Milliseconds(), Truncated: res.Truncated, Untrusted: res.Untrusted})
	if pc.action != nil && er.status != core.TCDenied && er.status != core.TCInvalid {
		r.auditf(ctx, "agent:"+string(rs.id), "tool.exec", pc.name, auditOutcome(er.status), rs.req.Session, rs.id,
			map[string]any{"tool": pc.name, "fingerprint": pc.action.Fingerprint, "verdict": pc.dec.Verdict, "status": er.status})
	}

	switch er.status {
	case core.TCError, core.TCTimeout, core.TCInvalid:
		rs.toolErrors++
	case core.TCOK:
		rs.toolErrors = 0
	}
	if end == nil && rs.toolErrors >= maxToolErrors {
		end = loopEnd()
	}
	if er.status == core.TCStopped && rs.ctx.Err() != nil {
		return r.ctxEnd(rs)
	}
	return end
}

func auditOutcome(st core.ToolCallStatus) string {
	if st == core.TCOK {
		return "ok"
	}
	return "error"
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// approvedMessage renders the outcome of a resumed parked call as a user-role
// message authored by Bothy (SPEC §5.7 g).
func approvedMessage(rs *runState, pc *pcall, res core.ToolResult, st core.ToolCallStatus) *core.Message {
	body := res.Content
	if res.Untrusted {
		body = wrapUntrusted(randomID(), body, "source", pc.name)
	}
	text := fmt.Sprintf("<approved-action tool=\"%s\" call=\"%s\" status=\"%s\">\n%s\n</approved-action>",
		attrValue(pc.name), attrValue(pc.call.ID), st, body)
	return &core.Message{Session: rs.req.Session, Run: rs.id, Role: core.MsgUser, Provenance: core.ProvSystem,
		Parts: []core.Part{{Type: core.PartText, Text: text}}}
}

// markNotRun finalizes a planned call that will not run.
func (r *Runner) markNotRun(ctx context.Context, rs *runState, pc *pcall, st core.ToolCallStatus, why string) {
	pc.rec.Status, pc.rec.ResultPreview, pc.rec.FinishedAt = st, "Not executed: "+why+".", r.now()
	r.updateRec(ctx, pc.rec)
}

// persistNotRun stores explicit results for calls a parked step will not run,
// so the history stays valid for providers while the run waits.
func (r *Runner) persistNotRun(ctx context.Context, rs *runState, calls []core.ToolCall, text string) {
	for i, c := range calls {
		id := c.ID
		if id == "" {
			id = "call_skipped_" + strconv.Itoa(i)
		}
		m := &core.Message{Session: rs.req.Session, Run: rs.id, Role: core.MsgTool, Provenance: core.ProvSystem,
			Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: id, Name: c.Name, Content: text, IsError: true}}}}
		if err := r.appendMessage(ctx, rs.req.Principal, m, nil); err != nil {
			r.log.Error("persist skipped call", "run", rs.id, "err", err)
		}
	}
}
