// Package refine is refine mode (v0.2 F3), after arXiv:2608.05643 ("Refining
// Over Resampling"): N independent drafts of a final answer (breadth), D
// rounds of continue → critique → correct per draft (depth), then a plurality
// vote over the normalized final answers. It is verifier-free: the model
// reviews its own drafts. The engine knows nothing about providers, context
// or budgets; a Caller makes each model call and may refuse one (Halt).
package refine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// DefaultParallel bounds the calls in flight when Settings.Parallel is unset.
const DefaultParallel = 4

// Why refinement ended before its last round (core.RefineTrace.Fallback).
const (
	FallbackBudget     = "budget"      // a budget refused the next call
	FallbackTime       = "time"        // the run's time limit was near
	FallbackNewMessage = "new_message" // the user wrote again; the run answers that next
	FallbackStopped    = "stopped"     // the run was stopped
	FallbackFailed     = "failed"      // every draft still being refined failed
)

// traceBudget bounds the text one trace keeps across all of its entries;
// answerCap is the share of it an entry's or a vote's answer may use.
const (
	traceBudget = 128 << 10
	answerCap   = 256
)

type Settings struct {
	Drafts    int // breadth, >= 1
	Depth     int // rounds, >= 0
	EarlyStop bool
	Parallel  int                 // calls in flight; <= 0 = DefaultParallel
	Redact    func(string) string // applied to every text the trace keeps
	// Proceed is asked before each round; false ends refinement with
	// FallbackNewMessage.
	Proceed func() bool
}

// Call is one model call the engine needs.
type Call struct {
	Role     core.RefineRole
	Draft    int
	Round    int
	Trace    string // continue and critique: the text under work; correct: the continued text
	Critique string // correct only
}

// Reply is a finished call. CallID names the call's llm_calls row, so it is
// set only for recorded calls, failed ones included; a call cut off by the
// context with neither CallID nor Cost leaves no entry.
type Reply struct {
	Text     string
	CallID   string
	Model    string
	Cost     core.Micros
	Abridged bool
}

// Caller makes one model call. Returning a *Halt means the call was never
// made and no further call may start (a budget would be exceeded); any other
// error fails that call only, and its draft keeps its last text.
type Caller interface {
	Call(ctx context.Context, c Call) (Reply, error)
}

type Halt struct{ Reason string }

func (h *Halt) Error() string { return "refine: halted: " + h.Reason }

type Outcome struct {
	Text  string // the winning draft's full text; "" when no draft has any
	Trace core.RefineTrace
}

type draft struct {
	text   string
	answer string // normalized, "" when the text has no final answer line
	ok     bool   // the draft exists (has text)
	frozen bool   // a call for it failed: it keeps its text and takes no more rounds
}

func (d *draft) set(text string) {
	d.text, d.answer, d.ok = text, Normalize(Extract(text)), text != ""
}

type engine struct {
	s       Settings
	caller  Caller
	onEntry func(core.RefineEntry)
	textCap int

	halted atomic.Bool

	mu      sync.Mutex
	reason  string
	entries []core.RefineEntry
	cost    core.Micros
}

func newEngine(s Settings, c Caller, onEntry func(core.RefineEntry)) *engine {
	s.Drafts, s.Depth = max(s.Drafts, 1), max(s.Depth, 0)
	if s.Parallel <= 0 {
		s.Parallel = DefaultParallel
	}
	if s.Redact == nil {
		s.Redact = func(t string) string { return t }
	}
	if onEntry == nil {
		onEntry = func(core.RefineEntry) {}
	}
	textCap := min(max(traceBudget/core.RefineMaxCalls(s.Drafts, s.Depth)-answerCap, 512), 4096)
	return &engine{s: s, caller: c, onEntry: onEntry, textCap: textCap}
}

// Run refines first, the run's own final answer, which is draft 0. onEntry
// sees every finished call, from several goroutines but never concurrently.
func Run(ctx context.Context, s Settings, first Reply, c Caller, onEntry func(core.RefineEntry)) Outcome {
	e := newEngine(s, c, onEntry)
	return e.run(ctx, first)
}

// Skip is the outcome of a refinement that could not start: the first
// answer stands and the trace says why.
func Skip(s Settings, first Reply, reason string) Outcome {
	e := newEngine(s, nil, nil)
	drafts := make([]draft, e.s.Drafts)
	drafts[0].set(strings.TrimSpace(first.Text))
	e.recordFirst(first, &drafts[0])
	e.reason = reason
	return e.finish(drafts, 0, false)
}

func (e *engine) run(ctx context.Context, first Reply) Outcome {
	drafts := make([]draft, e.s.Drafts)
	drafts[0].set(strings.TrimSpace(first.Text))
	e.recordFirst(first, &drafts[0])

	more := make([]int, 0, e.s.Drafts-1)
	for k := 1; k < e.s.Drafts; k++ {
		more = append(more, k)
	}
	e.pool(more, func(k int) {
		if rep, ok := e.call(ctx, Call{Role: core.RefineDraft, Draft: k}); ok {
			drafts[k].set(rep.Text)
		}
	})
	rounds, early := 0, false
	if e.s.EarlyStop && agree(drafts) && !e.stopped(ctx) {
		early = true
	}
	for round := 1; round <= e.s.Depth && !early; round++ {
		if e.stopped(ctx) {
			break
		}
		if e.s.Proceed != nil && !e.s.Proceed() {
			e.halt(FallbackNewMessage)
			break
		}
		var live []int
		for k := range drafts {
			if drafts[k].ok && !drafts[k].frozen {
				live = append(live, k)
			}
		}
		var completed atomic.Int32
		e.pool(live, func(k int) {
			if e.deepen(ctx, round, k, &drafts[k]) {
				completed.Add(1)
			}
		})
		if e.stopped(ctx) {
			break // a round cut short does not count
		}
		if completed.Load() == 0 {
			e.halt(FallbackFailed)
			break
		}
		rounds = round
		if e.s.EarlyStop && agree(drafts) {
			early = true
		}
	}
	return e.finish(drafts, rounds, early)
}

// deepen takes one draft through a round: continue, critique, and correct
// unless the critic confirmed a continued text that states a final answer.
func (e *engine) deepen(ctx context.Context, round, k int, d *draft) bool {
	g, ok := e.call(ctx, Call{Role: core.RefineContinue, Draft: k, Round: round, Trace: d.text})
	if !ok {
		d.frozen = true
		return false
	}
	c, ok := e.call(ctx, Call{Role: core.RefineCritique, Draft: k, Round: round, Trace: g.Text})
	if !ok {
		d.frozen = true
		return false
	}
	if Confirmed(c.Text) && Extract(g.Text) != "" {
		d.set(g.Text)
		return true
	}
	r, ok := e.call(ctx, Call{Role: core.RefineCorrect, Draft: k, Round: round, Trace: g.Text, Critique: c.Text})
	if !ok {
		d.frozen = true
		return false
	}
	d.set(r.Text)
	return true
}

// call makes one call and records its entry; ok is false when the call was
// not made, failed or returned no text.
func (e *engine) call(ctx context.Context, c Call) (Reply, bool) {
	if e.stopped(ctx) {
		return Reply{}, false
	}
	rep, err := e.caller.Call(ctx, c)
	if h, isHalt := errors.AsType[*Halt](err); isHalt {
		e.halt(h.Reason)
		return Reply{}, false
	}
	rep.Text = strings.TrimSpace(rep.Text)
	if err != nil && ctx.Err() != nil {
		e.halt(FallbackStopped)
		if rep.CallID == "" && rep.Cost == 0 {
			return Reply{}, false // never reached a model
		}
	}
	ent := core.RefineEntry{Draft: c.Draft, Round: c.Round, Role: c.Role, CallID: rep.CallID, Model: rep.Model,
		Cost: rep.Cost, Abridged: rep.Abridged}
	switch {
	case err != nil:
		ent.Error = clipRunes(e.s.Redact(err.Error()), 300)
	case rep.Text == "":
		ent.Error = "The model returned no text."
	default:
		e.describe(&ent, rep.Text)
	}
	e.record(ent)
	return rep, err == nil && rep.Text != ""
}

func (e *engine) describe(ent *core.RefineEntry, text string) {
	red := e.s.Redact(text)
	ent.Text = e.clip(red)
	if ent.Role == core.RefineCritique {
		ent.Confirmed = Confirmed(text)
		return
	}
	ent.Answer = shownAnswer(red)
}

// shownAnswer is the final answer of an already redacted text as the trace
// shows it. Redaction has to come first: it matches secrets exactly, and
// normalizing lowercases and strips characters, which would let a secret
// through.
func shownAnswer(redacted string) string {
	return clipBytes(Normalize(Extract(redacted)), answerCap)
}

func (e *engine) recordFirst(first Reply, d *draft) {
	ent := core.RefineEntry{Role: core.RefineDraft, CallID: first.CallID, Model: first.Model, Cost: first.Cost}
	if d.ok {
		e.describe(&ent, d.text)
	} else {
		ent.Error = "The model returned no text."
	}
	e.record(ent)
}

func (e *engine) record(ent core.RefineEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = append(e.entries, ent)
	e.cost += ent.Cost
	e.onEntry(ent)
}

func (e *engine) halt(reason string) {
	e.mu.Lock()
	if e.reason == "" {
		e.reason = reason
	}
	e.mu.Unlock()
	e.halted.Store(true)
}

func (e *engine) stopped(ctx context.Context) bool {
	if ctx.Err() != nil {
		e.halt(FallbackStopped)
	}
	return e.halted.Load()
}

// pool runs fn for every item with at most Settings.Parallel at once; with
// one at a time the items run in order.
func (e *engine) pool(items []int, fn func(int)) {
	p := min(e.s.Parallel, len(items))
	if p <= 1 {
		for _, k := range items {
			fn(k)
		}
		return
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for range p {
		wg.Go(func() {
			for k := range next {
				fn(k)
			}
		})
	}
	for _, k := range items {
		next <- k
	}
	close(next)
	wg.Wait()
}

func (e *engine) finish(drafts []draft, rounds int, early bool) Outcome {
	answers := make([]string, len(drafts))
	for k, d := range drafts {
		if d.ok {
			answers[k] = d.answer
		}
	}
	winner, votes := Vote(answers)
	if winner < 0 {
		winner = max(slices.IndexFunc(drafts, func(d draft) bool { return d.ok }), 0)
	}
	shown := func(k int) string { return shownAnswer(e.s.Redact(drafts[k].text)) }
	for i := range votes {
		votes[i].Answer = shown(votes[i].Drafts[0])
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entries := slices.Clone(e.entries)
	slices.SortStableFunc(entries, func(a, b core.RefineEntry) int {
		return cmp.Or(cmp.Compare(a.Round, b.Round), cmp.Compare(a.Draft, b.Draft), cmp.Compare(roleOrder(a.Role), roleOrder(b.Role)))
	})
	t := core.RefineTrace{Drafts: e.s.Drafts, Depth: e.s.Depth, Rounds: rounds, EarlyStop: early, Winner: winner,
		Votes: votes, Entries: entries, Calls: len(entries), Cost: e.cost}
	if drafts[winner].answer != "" {
		t.Answer = shown(winner)
	}
	if !early && rounds < e.s.Depth {
		t.Fallback = e.reason
	}
	return Outcome{Text: drafts[winner].text, Trace: t}
}

func roleOrder(r core.RefineRole) int {
	switch r {
	case core.RefineDraft:
		return 0
	case core.RefineContinue:
		return 1
	case core.RefineCritique:
		return 2
	}
	return 3
}

// agree reports that at least two drafts exist and all of them state the
// same non-empty final answer.
func agree(drafts []draft) bool {
	n, first := 0, ""
	for _, d := range drafts {
		if !d.ok {
			continue
		}
		if d.answer == "" || (n > 0 && d.answer != first) {
			return false
		}
		first = d.answer
		n++
	}
	return n >= 2
}

// clip keeps a long text's head and its tail, where the final answer is.
func (e *engine) clip(s string) string {
	if len(s) <= e.textCap {
		return s
	}
	note := fmt.Sprintf("\n[… %d bytes omitted …]\n", len(s)-e.textCap)
	head := e.textCap * 6 / 10
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	from := len(s) - (e.textCap - head)
	for from < len(s) && !utf8.RuneStart(s[from]) {
		from++
	}
	return s[:head] + note + s[from:]
}

// clipBytes cuts s to at most n bytes on a rune boundary, marking the cut.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	n -= len("…")
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
