// Package fakes holds in-memory implementations of core interfaces for unit tests.
//
// Every fake is safe for concurrent use. Exported slices record calls: read
// them directly once the code under test has returned, or through the
// accessor methods while it may still be running. Methods a fake does not
// implement fall through to its embedded interface, which is nil unless the
// test sets it, so an unexpected call panics loudly.
package fakes

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var (
	_ core.Clock   = (*Clock)(nil)
	_ core.Auditor = (*Auditor)(nil)
	_ core.RunSink = (*Sink)(nil)
)

// Clock is a manually advanced core.Clock (use testing/synctest for timers).
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(t time.Time) *Clock        { return &Clock{now: t} }
func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// Auditor records entries; Verify always reports an intact chain.
type Auditor struct {
	mu      sync.Mutex
	Entries []core.AuditEntry
}

// Record assigns the next id (and At when zero) and appends the entry.
func (a *Auditor) Record(_ context.Context, e core.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	e.ID = int64(len(a.Entries)) + 1
	if e.At.IsZero() {
		e.At = time.Now()
	}
	a.Entries = append(a.Entries, e)
	return nil
}

func (a *Auditor) Verify(context.Context) (int64, error) { return 0, nil }
func (a *Auditor) Flush(context.Context) error           { return nil }

// All returns a copy of the recorded entries.
func (a *Auditor) All() []core.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]core.AuditEntry(nil), a.Entries...)
}

// Actions returns the Action of every recorded entry, in order.
func (a *Auditor) Actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.Entries))
	for i, e := range a.Entries {
		out[i] = e.Action
	}
	return out
}

// Sink records run events.
type Sink struct {
	mu      sync.Mutex
	Events  []core.Event
	changed chan struct{} // closed and cleared on every event; created by Wait
}

func (s *Sink) OnEvent(e core.Event) {
	s.mu.Lock()
	s.Events = append(s.Events, e)
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
	s.mu.Unlock()
}

// All returns a copy of the events received so far.
func (s *Sink) All() []core.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.Event(nil), s.Events...)
}

// Types returns the type of every event received so far, in order.
func (s *Sink) Types() []core.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.EventType, len(s.Events))
	for i, e := range s.Events {
		out[i] = e.Type
	}
	return out
}

// Text concatenates the text of every run.delta event.
func (s *Sink) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, e := range s.Events {
		if d, ok := e.Data.(core.DeltaData); ok {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

// Wait returns the first received event of type typ, blocking until one
// arrives or ctx is done.
func (s *Sink) Wait(ctx context.Context, typ core.EventType) (core.Event, error) {
	for {
		s.mu.Lock()
		for _, e := range s.Events {
			if e.Type == typ {
				s.mu.Unlock()
				return e, nil
			}
		}
		if s.changed == nil {
			s.changed = make(chan struct{})
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return core.Event{}, ctx.Err()
		}
	}
}
