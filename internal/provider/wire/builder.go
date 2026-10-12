package wire

import (
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

// callSeq numbers synthesized tool call ids. Seeding from the clock keeps ids
// unique across restarts too, since a later fallback to another vendor sends
// the whole history and some vendors reject duplicate call ids.
var callSeq atomic.Int64

func init() { callSeq.Store(time.Now().UnixMilli() * 1000) }

// SynthesizedID returns a fresh "call_<n>" id for a provider that sent none.
func SynthesizedID() string { return "call_" + strconv.FormatInt(callSeq.Add(1), 10) }

var synthesizedRe = regexp.MustCompile(`^call_[0-9]+$`)

// IsSynthesized reports whether id came from SynthesizedID (or has its shape).
func IsSynthesized(id string) bool { return synthesizedRe.MatchString(id) }

// Builder assembles the assistant message while forwarding stream events in
// arrival order. Text and thinking deltas extend the open part of their type
// until Close; tool calls reserve their slot when they start so the message
// keeps the order the model produced.
type Builder struct {
	kind  core.ProviderKind
	on    func(core.StreamEvent) error
	parts []core.Part
	open  int // index of the open text/thinking part, -1 when none
	calls int
}

// Call is a tool call between StartCall and FinishCall.
type Call struct {
	ID, Name string
	slot     int
	done     bool
}

func NewBuilder(kind core.ProviderKind, on func(core.StreamEvent) error) *Builder {
	if on == nil {
		on = func(core.StreamEvent) error { return nil }
	}
	return &Builder{kind: kind, on: on, open: -1}
}

func (b *Builder) delta(t core.PartType, ev core.StreamEventType, s string) error {
	if s == "" {
		return nil
	}
	if b.open < 0 || b.parts[b.open].Type != t {
		p := core.Part{Type: t}
		if t == core.PartThinking {
			p.SigKind = b.kind
		}
		b.parts = append(b.parts, p)
		b.open = len(b.parts) - 1
	}
	b.parts[b.open].Text += s
	return b.on(core.StreamEvent{Type: ev, Text: s})
}

func (b *Builder) Text(s string) error { return b.delta(core.PartText, core.StreamTextDelta, s) }

func (b *Builder) Thinking(s string) error {
	return b.delta(core.PartThinking, core.StreamThinkingDelta, s)
}

// OpenThinking starts an (initially empty) thinking part, for blocks that may
// carry only a signature. Thinking parts always record the producing kind:
// the facade strips them before a call to any other kind.
func (b *Builder) OpenThinking() {
	b.parts = append(b.parts, core.Part{Type: core.PartThinking, SigKind: b.kind})
	b.open = len(b.parts) - 1
}

// Sign attaches provider-opaque data to the open text or thinking part, or to
// a new empty thinking part when none is open, so a signature never lands on
// a tool call it was not sent with.
func (b *Builder) Sign(sig string) {
	if sig == "" {
		return
	}
	if b.open < 0 {
		b.OpenThinking()
	}
	p := &b.parts[b.open]
	p.Sig = sig
	p.SigKind = b.kind
}

// Close ends the open text or thinking part.
func (b *Builder) Close() { b.open = -1 }

// StartCall emits StreamToolCallStart; an empty id is synthesized.
func (b *Builder) StartCall(id, name string) (*Call, error) {
	if id == "" {
		id = SynthesizedID()
	}
	b.Close()
	b.parts = append(b.parts, core.Part{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: id, Name: name}})
	c := &Call{ID: id, Name: name, slot: len(b.parts) - 1}
	b.calls++
	return c, b.on(core.StreamEvent{Type: core.StreamToolCallStart, Call: &core.ToolCall{ID: id, Name: name}})
}

// FinishCall repairs the arguments and emits StreamToolCallDone.
func (b *Builder) FinishCall(c *Call, rawArgs, sig string) error {
	if c.done {
		return nil
	}
	c.done = true
	args, _ := RepairArgs(rawArgs)
	p := &b.parts[c.slot]
	p.ToolCall.Args = args
	if sig != "" {
		p.Sig, p.SigKind = sig, b.kind
	}
	done := *p.ToolCall
	return b.on(core.StreamEvent{Type: core.StreamToolCallDone, Call: &done})
}

// Calls reports how many tool calls were started.
func (b *Builder) Calls() int { return b.calls }

// Response returns the assembled message. Calls that never finished (output
// cut off mid-arguments) are dropped: executing a truncated call could act on
// half an argument.
func (b *Builder) Response(model string, stop core.StopReason, u core.Usage, estimated bool) *core.Response {
	parts := make([]core.Part, 0, len(b.parts))
	finished := 0
	for _, p := range b.parts {
		if p.Type == core.PartToolCall && len(p.ToolCall.Args) == 0 {
			continue
		}
		if p.Type == core.PartToolCall {
			finished++
		}
		if (p.Type == core.PartText || p.Type == core.PartThinking) && p.Text == "" && p.Sig == "" {
			continue
		}
		parts = append(parts, p)
	}
	if finished > 0 && stop == core.StopEnd {
		stop = core.StopTool
	}
	return &core.Response{
		Message:        core.Message{Role: core.MsgAssistant, Kind: core.KindNormal, Parts: parts, Tokens: u.Output},
		Usage:          u,
		UsageEstimated: estimated,
		Stop:           stop,
		Model:          model,
	}
}

// Usage emits the usage event.
func (b *Builder) Usage(u core.Usage) error {
	uu := u
	return b.on(core.StreamEvent{Type: core.StreamUsage, Usage: &uu})
}
