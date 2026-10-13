package egress

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	flushEvery = time.Second
	flushAt    = 256
	maxPending = 8192
)

type logQueue struct {
	mu      sync.Mutex
	pending []core.EgressRecord
	dropped int
	wake    chan struct{}
}

// record queues one egress_log row; Run writes them in batches.
func (s *Service) record(r core.EgressRecord) {
	if r.At.IsZero() {
		r.At = s.clock.Now()
	}
	q := &s.logq
	q.mu.Lock()
	if len(q.pending) >= maxPending {
		q.dropped++
		q.mu.Unlock()
		return
	}
	q.pending = append(q.pending, r)
	n := len(q.pending)
	q.mu.Unlock()
	if n >= flushAt {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) flush(ctx context.Context) error {
	q := &s.logq
	q.mu.Lock()
	batch, dropped := q.pending, q.dropped
	q.pending, q.dropped = nil, 0
	q.mu.Unlock()
	if dropped > 0 {
		s.log.Warn("egress: log queue full, rows dropped", "count", dropped)
	}
	if len(batch) == 0 {
		return nil
	}
	if err := s.store.Egress().Add(ctx, batch); err != nil {
		s.log.Warn("egress: writing egress_log failed; will retry", "rows", len(batch), "err", err)
		s.requeue(batch)
		return err
	}
	return nil
}

// requeue puts a batch that could not be written back in front of the queue,
// within maxPending.
func (s *Service) requeue(batch []core.EgressRecord) {
	q := &s.logq
	q.mu.Lock()
	defer q.mu.Unlock()
	all := append(batch, q.pending...)
	if over := len(all) - maxPending; over > 0 {
		q.dropped += over
		all = all[:maxPending]
	}
	q.pending = all
}

func (s *Service) audit(ctx context.Context, action, target, outcome string, sess core.SessionID, run core.RunID, detail map[string]any) {
	if s.auditor == nil {
		return
	}
	actor := "system"
	if run != "" {
		actor = "agent:" + string(run)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte("{}")
	}
	e := core.AuditEntry{Actor: actor, Action: action, Target: target, Outcome: outcome, Session: sess, Run: run, Detail: raw}
	if err := s.auditor.Record(context.WithoutCancel(ctx), e); err != nil {
		s.log.Warn("egress: audit failed", "action", action, "err", err)
	}
}

func (s *Service) publishBlocked(p core.PrincipalID, sess core.SessionID, run core.RunID, host string, port int, reason string) {
	if s.bus == nil || p == "" {
		return
	}
	s.bus.Publish(core.Event{Type: core.EvEgressBlocked, Principal: p, Session: sess, Run: run,
		Data: core.EgressBlockedData{Host: host, Port: port, Reason: reason}})
}
