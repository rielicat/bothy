package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	deltaInterval  = 50 * time.Millisecond  // run.delta/run.thinking <= 20/s per run
	outputInterval = 100 * time.Millisecond // run.tool_output <= 10/s per call
	outputChunkMax = 8 << 10
	holdbackMax    = 2 << 10
)

// publish sends a run event to the bus and then to the run's sink, under
// emitMu so both see the same order. A detached sink gets nothing. Status
// details carry URLs and command lines, so an exposed session's sink gets
// them with links neutralized like the text.
func (rs *runState) publish(typ core.EventType, data core.EventData) core.Event {
	rs.emitMu.Lock()
	defer rs.emitMu.Unlock()
	sinkData := data
	if st, ok := data.(core.RunStatusData); ok && rs.exposed && st.Detail != "" {
		st.Detail = neutralizeLinks(st.Detail)
		sinkData = st
	}
	return rs.publishLocked(typ, data, sinkData)
}

// publishLocked lets the sink get a different payload (neutralized links).
// A nil sinkData publishes to the bus only.
func (rs *runState) publishLocked(typ core.EventType, data, sinkData core.EventData) core.Event {
	ev := rs.r.d.Bus.Publish(core.Event{Type: typ, Principal: rs.req.Principal, Session: rs.req.Session, Run: rs.id, Data: data})
	if rs.sink != nil && !rs.detached && sinkData != nil {
		out := ev
		out.Data = sinkData
		rs.sink.OnEvent(out)
	}
	return ev
}

func (rs *runState) publishFinished() {
	rs.mu.Lock()
	if rs.finished || !rs.rowCreated {
		rs.mu.Unlock()
		return
	}
	rs.finished = true
	run := rs.run
	if rs.stopped {
		run.Status = core.RunStopped
	}
	rs.mu.Unlock()
	tot := rs.totals()
	rs.publish(core.EvRunFinished, core.RunFinishedData{Status: run.Status, Error: run.Error, Steps: tot.Steps, Usage: tot.Usage, Cost: tot.Cost})
}

func (rs *runState) setExposed(t core.Taint) {
	rs.emitMu.Lock()
	rs.exposed = t.Exposed()
	rs.emitMu.Unlock()
}

// streamer coalesces one LLM call's deltas into events at most every
// deltaInterval. When the session is exposed it releases text only up to the
// last whitespace, so a link split across deltas is neutralized whole on the
// sink side; the bus always gets the raw text (the web renders links inert).
type streamer struct {
	rs      *runState
	typ     core.EventType
	toSink  bool
	mu      sync.Mutex
	buf     strings.Builder
	timer   *time.Timer
	pending string // held back while exposed
}

func (rs *runState) newStreamer(typ core.EventType, toSink bool) *streamer {
	return &streamer{rs: rs, typ: typ, toSink: toSink}
}

func (s *streamer) add(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.WriteString(text)
	if s.timer == nil {
		s.timer = time.AfterFunc(deltaInterval, s.fire)
	}
}

func (s *streamer) fire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	s.flushLocked(false)
}

func (s *streamer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.flushLocked(true)
}

// flushLocked publishes while holding s.mu, so a timer flush racing close
// cannot reorder text behind the events that follow the call.
func (s *streamer) flushLocked(final bool) {
	raw := s.pending + s.buf.String()
	s.buf.Reset()
	s.pending = ""
	rs := s.rs
	rs.emitMu.Lock()
	defer rs.emitMu.Unlock()
	if rs.exposed && s.toSink && !final {
		cut := strings.LastIndexFunc(raw, unicode.IsSpace)
		if cut < 0 && len(raw) < holdbackMax {
			s.pending = raw
			return
		}
		if cut >= 0 {
			_, size := utf8.DecodeRuneInString(raw[cut:])
			s.pending = raw[cut+size:]
			raw = raw[:cut+size]
		}
	}
	if raw == "" {
		return
	}
	text := rs.r.redact(raw)
	var sinkData core.EventData
	if s.toSink {
		sinkText := text
		if rs.exposed {
			sinkText = neutralizeLinks(text)
		}
		sinkData = core.DeltaData{Text: sinkText}
	}
	rs.publishLocked(s.typ, core.DeltaData{Text: text}, sinkData)
}

// outputLimiter forwards a tool's progress output as run.tool_output: chunks
// of at most outputChunkMax bytes (the tail is kept), at most one event per
// outputInterval. Emit never blocks the tool.
type outputLimiter struct {
	rs     *runState
	callID string
	mu     sync.Mutex
	buf    [3][]byte // per stream (1 stdout, 2 stderr)
	last   time.Time
	timer  *time.Timer
	closed bool
}

func (rs *runState) newOutputLimiter(callID string) *outputLimiter {
	return &outputLimiter{rs: rs, callID: callID}
}

func (o *outputLimiter) emit(d core.EventData) {
	out, ok := d.(core.ToolOutputData)
	if !ok || out.Chunk == "" {
		return
	}
	stream := out.Stream
	if stream != 2 {
		stream = 1
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	b := append(o.buf[stream], out.Chunk...)
	if len(b) > outputChunkMax {
		b = b[len(b)-outputChunkMax:]
	}
	o.buf[stream] = b
	if o.timer != nil {
		return
	}
	wait := outputInterval - o.rs.r.now().Sub(o.last)
	if wait <= 0 {
		o.flushLocked()
		return
	}
	o.timer = time.AfterFunc(wait, func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.timer = nil
		if !o.closed {
			o.flushLocked()
		}
	})
}

func (o *outputLimiter) flushLocked() {
	for stream := 1; stream <= 2; stream++ {
		if len(o.buf[stream]) == 0 {
			continue
		}
		chunk := o.rs.r.redact(string(o.buf[stream]))
		o.buf[stream] = nil
		o.rs.publish(core.EvToolOutput, core.ToolOutputData{CallID: o.callID, Stream: stream, Chunk: chunk})
	}
	o.last = o.rs.r.now()
}

func (o *outputLimiter) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
	o.flushLocked()
	o.closed = true
}

// redactAction returns a copy of the Action with secrets masked for events.
func (r *Runner) redactAction(a *core.Action) *core.Action {
	if a == nil || r.d.Redactor == nil {
		return a
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil
	}
	var out core.Action
	if err := json.Unmarshal(r.d.Redactor.RedactBytes(b), &out); err != nil {
		return &core.Action{Tool: a.Tool, Kind: a.Kind, SideEffect: a.SideEffect, Fingerprint: a.Fingerprint}
	}
	return &out
}

// redactMessage copies a message for publishing with every text masked.
func (r *Runner) redactMessage(m *core.Message) *core.Message {
	out := *m
	out.Parts = make([]core.Part, len(m.Parts))
	for i, p := range m.Parts {
		p.Text = r.redact(p.Text)
		if p.ToolResult != nil {
			tr := *p.ToolResult
			tr.Content = r.redact(tr.Content)
			p.ToolResult = &tr
		}
		if p.ToolCall != nil && r.d.Redactor != nil {
			tc := *p.ToolCall
			tc.Args = r.d.Redactor.RedactBytes(tc.Args)
			p.ToolCall = &tc
		}
		out.Parts[i] = p
	}
	return &out
}

// appendMessage persists a message and publishes message.created.
func (r *Runner) appendMessage(ctx context.Context, p core.PrincipalID, m *core.Message, sink core.RunSink) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = r.now()
	}
	if m.Kind == "" {
		m.Kind = core.KindNormal
	}
	if err := r.d.Store.Messages().Append(context.WithoutCancel(ctx), m); err != nil {
		return err
	}
	ev := r.d.Bus.Publish(core.Event{Type: core.EvMessageCreated, Principal: p, Session: m.Session, Run: m.Run,
		Data: core.MessageData{Message: r.redactMessage(m)}})
	if sink != nil && m.Kind == core.KindNotice {
		sink.OnEvent(ev)
	}
	return nil
}

// notice persists a Bothy-authored message that is never sent to a model.
func (r *Runner) notice(ctx context.Context, s core.SessionID, p core.PrincipalID, run core.RunID, text string, sink core.RunSink) {
	m := &core.Message{Session: s, Run: run, Role: core.MsgAssistant, Kind: core.KindNotice, Provenance: core.ProvSystem,
		Parts: []core.Part{{Type: core.PartText, Text: text}}}
	if err := r.appendMessage(ctx, p, m, sink); err != nil {
		r.log.Error("notice", "session", s, "err", err)
	}
}

func (rs *runState) notice(ctx context.Context, text string) {
	rs.emitMu.Lock()
	sink := rs.sink
	if rs.detached {
		sink = nil
	}
	rs.emitMu.Unlock()
	rs.r.notice(ctx, rs.req.Session, rs.req.Principal, rs.id, text, sink)
}
