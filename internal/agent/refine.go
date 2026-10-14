package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/agent/refine"
	"github.com/rielicat/bothy/internal/core"
)

const (
	// refineTimeMargin is kept between the end of a refinement and the run's
	// time limit, so the best answer so far is still delivered.
	refineTimeMargin = 5 * time.Second
	// minQuoteTokens is the least a quoted draft keeps when it is abridged.
	minQuoteTokens = 256
	abridgedNote   = "[… part of this text was left out to fit the model's context window …]"
)

var errRefineTime = errors.New("agent: the run's time limit is near")

// refineMode is how a run refines its final answers.
type refineMode struct {
	drafts, depth int
	earlyStop     bool
	critic        core.Slot
}

// refineModeFor decides whether the run refines: the request's own choice,
// else, for message and api runs, the session's setting, else
// agent.refine.enabled. Scheduled and heartbeat runs refine only when their
// request asks.
func (r *Runner) refineModeFor(ctx context.Context, rs *runState) *refineMode {
	c := r.cfg().Agent.Refine
	on := false
	switch {
	case rs.req.Refine != nil:
		on = *rs.req.Refine
	case rs.req.Trigger == core.TrigMessage || rs.req.Trigger == core.TrigAPI:
		on = c.Enabled
		v, err := r.d.Store.Meta().Get(ctx, core.RefineMetaKey(rs.req.Session))
		switch {
		case err == nil:
			on = v == core.RefineOn || (on && v != core.RefineOff)
		case !errors.Is(err, core.ErrNotFound):
			r.log.Warn("read the session's refine setting", "session", rs.req.Session, "err", err)
		}
	}
	if !on {
		return nil
	}
	critic := core.SlotMain
	if c.CriticSlot == string(core.SlotFast) {
		critic = core.SlotFast
	}
	return &refineMode{drafts: orInt(c.Drafts, 3), depth: orInt(c.Depth, 2), earlyStop: c.EarlyStop, critic: critic}
}

// refineAnswer runs refine mode on a final answer (one with text and no tool
// calls): resp is draft 0, asm the step's assembler, so every other draft
// sees the same context. It publishes the reply that won as the run's text
// and returns the message to persist, which carries the trace.
func (r *Runner) refineAnswer(rs *runState, asm *assembler, step int, resp *core.Response, ci core.CallInfo) *core.Response {
	m := rs.refine
	set := refine.Settings{Drafts: m.drafts, Depth: m.depth, EarlyStop: m.earlyStop, Parallel: r.refineParallel, Redact: r.redact,
		Proceed: func() bool {
			rs.w.mu.Lock()
			defer rs.w.mu.Unlock()
			return rs.w.steers == 0
		}}
	model := ci.Ref.String()
	if ci.Ref.Model == "" {
		model = ""
	}
	first := refine.Reply{Text: resp.Message.PlainText(), CallID: ci.ID, Model: model, Cost: ci.Cost}
	rs.publish(core.EvRunStatus, core.RunStatusData{Status: "thinking", Detail: "Refining the answer"})
	rs.publishBus(core.EvRunRefine, core.RefineData{Phase: "start", Drafts: set.Drafts, Depth: set.Depth,
		MaxCalls: core.RefineMaxCalls(set.Drafts, set.Depth)})

	var out refine.Outcome
	if ctx, cancel, ok := r.refineContext(rs); !ok {
		out = refine.Skip(set, first, refine.FallbackTime)
	} else {
		c := &refineCaller{r: r, rs: rs, asm: asm, step: step, critic: m.critic, untrusted: rs.taint.Has(core.TaintUntrusted)}
		out = refine.Run(ctx, set, first, c, func(e core.RefineEntry) {
			rs.publishBus(core.EvRunRefine, core.RefineData{Phase: "entry", Entry: &e})
		})
		if out.Trace.Fallback == refine.FallbackStopped && rs.ctx.Err() == nil && errors.Is(context.Cause(ctx), errRefineTime) {
			out.Trace.Fallback = refine.FallbackTime
		}
		cancel()
	}
	summary := out.Trace
	summary.Entries = nil
	rs.publishBus(core.EvRunRefine, core.RefineData{Phase: "done", Trace: &summary})

	trace := out.Trace
	msg := core.Message{Role: core.MsgAssistant}
	text := out.Text
	if text == "" || (trace.Winner == 0 && text == strings.TrimSpace(first.Text)) {
		// No draft had text, or the run's own answer won unchanged: keep
		// its parts, thinking included.
		msg.Parts = slices.Clone(resp.Message.Parts)
		text = resp.Message.PlainText()
	} else {
		msg.Parts = []core.Part{{Type: core.PartText, Text: text}}
	}
	msg.Parts = append(msg.Parts, core.Part{Type: core.PartRefine, Refine: &trace})
	s := rs.newStreamer(core.EvRunDelta, true)
	s.add(text)
	s.close()
	return &core.Response{Message: msg, Usage: core.Usage{Output: r.d.LLM.Estimate(text, r.headRef(rs))}, Stop: core.StopEnd,
		Model: resp.Model}
}

// refineContext ends refinement refineTimeMargin before the run's time
// limit; ok is false when that leaves no time at all.
func (r *Runner) refineContext(rs *runState) (context.Context, context.CancelFunc, bool) {
	dl := rs.meter.Deadline()
	if dl.IsZero() {
		ctx, cancel := context.WithCancel(rs.ctx)
		return ctx, cancel, true
	}
	left := dl.Sub(r.now()) - refineTimeMargin
	if left <= 0 {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeoutCause(rs.ctx, left, errRefineTime)
	return ctx, cancel, true
}

// publishBus publishes a run event to the bus only.
func (rs *runState) publishBus(typ core.EventType, data core.EventData) {
	rs.emitMu.Lock()
	defer rs.emitMu.Unlock()
	rs.publishLocked(typ, data, nil)
}

// refineCaller makes refinement calls for one answer: metered like agent
// steps (but not counted as steps), refused once a budget would be exceeded,
// never with tool use.
type refineCaller struct {
	r         *Runner
	rs        *runState
	asm       *assembler
	step      int
	critic    core.Slot
	untrusted bool // the session holds untrusted content: quoted drafts are spotlighted

	mu          sync.Mutex
	inIn, inOut int // projected input and output tokens of the calls in flight
}

func (c *refineCaller) Call(ctx context.Context, call refine.Call) (refine.Reply, error) {
	r, rs := c.r, c.rs
	slot, override := rs.slot, rs.override
	if call.Role == core.RefineCritique && c.critic != rs.slot && len(r.d.LLM.Chain(c.critic, nil)) > 0 {
		slot, override = c.critic, nil
	}
	chain := r.d.LLM.Chain(slot, override)
	if len(chain) == 0 {
		return refine.Reply{}, errors.New("no model is configured")
	}
	head := chain[0]
	var (
		asm      *assembler
		build    core.BuildFunc
		est, out int
		abridged bool
	)
	if call.Role == core.RefineDraft {
		asm = c.asm
		a := asm.assemble(head, false)
		est, out = a.est, a.req.MaxTokens
		build = func(m core.ModelInfo) (*core.Request, error) {
			req, err := asm.build(m)
			if err != nil {
				return nil, err
			}
			cp := *req
			withoutToolUse(&cp)
			return &cp, nil
		}
	} else {
		req, e, _ := c.roleRequest(call, head, false)
		est, out = e, req.MaxTokens
		build = func(m core.ModelInfo) (*core.Request, error) {
			req, _, ab := c.roleRequest(call, m, true)
			abridged = ab
			return req, nil
		}
	}
	if err := c.admit(ctx, head, est, out); err != nil {
		return refine.Reply{}, err
	}
	defer c.release(est, out)
	resp, ci, err := r.d.LLM.Complete(ctx, slot, override, build, nil)
	if err == nil && resp == nil {
		err = errors.New("the provider returned no response")
	}
	rep := refine.Reply{Cost: ci.Cost, Abridged: abridged}
	if err == nil || ci.Usage != (core.Usage{}) || ci.Cost > 0 {
		// Role calls append to the step's prompt, so their breakdown and
		// prefix would not describe what was sent.
		r.recordCall(ctx, rs, asm, &ci, c.step, "agent")
		// The facade names every call, but only recorded ones have a row
		// for the trace to point at.
		rep.CallID = ci.ID
	}
	if ci.Ref.Model != "" {
		rep.Model = ci.Ref.String()
	}
	if err != nil {
		return rep, err
	}
	rep.Text = resp.Message.PlainText()
	return rep, nil
}

// admit checks the run's budgets for one more call on top of the calls in
// flight, which the meter has not seen yet.
func (c *refineCaller) admit(ctx context.Context, head core.ModelInfo, est, out int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := head
	info.MaxOutput = out + c.inOut
	if err := c.rs.meter.BeforeCall(ctx, est+c.inIn, info); err != nil {
		if _, ok := errors.AsType[*core.BudgetError](err); ok {
			return &refine.Halt{Reason: refine.FallbackBudget}
		}
		return err
	}
	c.inIn += est
	c.inOut += out
	return nil
}

func (c *refineCaller) release(est, out int) {
	c.mu.Lock()
	c.inIn -= est
	c.inOut -= out
	c.mu.Unlock()
}

// roleRequest is the step's request fitted to m with room for one appended
// user message: the quoted draft (and critique) and the role's instructions.
// Quotes longer than a third of the window are abridged to their head and
// tail, which holds the final answer line.
func (c *refineCaller) roleRequest(call refine.Call, m core.ModelInfo, load bool) (*core.Request, int, bool) {
	r := c.r
	cfg := r.cfg()
	est := func(s string) int { return r.d.LLM.Estimate(s, m.Ref) }
	quotes := refine.Quotes(call)
	for i := range quotes {
		quotes[i].Text = r.redact(quotes[i].Text)
	}
	abridged := false
	if m.Context > 0 {
		room := (m.Context - reserveFor(cfg, profileFor(cfg, m.Context), m.Context)) / 3
		avail := max(room-est(refine.Message(call, nil, c.wrap))-msgOverhead, minQuoteTokens)
		total := 0
		for _, q := range quotes {
			total += est(q.Text)
		}
		if total > avail {
			abridged = true
			for i, q := range quotes {
				share := max(avail*est(q.Text)/total, minPartTokens)
				quotes[i].Text = headTail(q.Text, max(len(q.Text)*share/max(est(q.Text), 1), 64), abridgedNote)
			}
		}
	}
	text := refine.Message(call, quotes, c.wrap)
	extra := est(text) + msgOverhead
	base := c.asm.withExtra(extra).assemble(m, load)
	req := *base.req
	req.Messages = append(slices.Clone(base.req.Messages), core.Message{Role: core.MsgUser, Kind: core.KindNormal,
		Provenance: core.ProvSystem, Parts: []core.Part{{Type: core.PartText, Text: text}}})
	withoutToolUse(&req)
	return &req, base.est + extra, abridged
}

// wrap quotes a text in a block whose random id its content cannot close;
// in a session holding untrusted content it is a spotlight block, since a
// draft may repeat what an untrusted source said.
func (c *refineCaller) wrap(kind, text string) string {
	id := randomID()
	if c.untrusted {
		return wrapUntrusted(id, text, "source", kind)
	}
	return "<" + kind + ` id="` + id + `">` + "\n" + strings.TrimRight(text, "\n") + "\n</" + kind + ` id="` + id + `">`
}

// withoutToolUse keeps the tools block, so the prompt prefix stays the one
// the step used, but forbids calling them: refinement never runs tools.
func withoutToolUse(req *core.Request) {
	if len(req.Tools) > 0 {
		req.ToolChoice = "none"
	}
}

// heldText holds a step's text back while the step may still turn out to be
// a final answer that refinement replaces; a tool call releases it.
type heldText struct {
	s    *streamer
	hold bool
	buf  strings.Builder
}

func (h *heldText) add(t string) {
	if h.hold {
		h.buf.WriteString(t)
		return
	}
	h.s.add(t)
}

// release publishes what was held and stops holding.
func (h *heldText) release() {
	if !h.hold {
		return
	}
	h.hold = false
	h.s.add(h.buf.String())
	h.buf.Reset()
}

func hasToolCalls(m core.Message) bool {
	return slices.ContainsFunc(m.Parts, func(p core.Part) bool { return p.Type == core.PartToolCall && p.ToolCall != nil })
}
