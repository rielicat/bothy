package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store    core.Store
	Redactor core.Redactor
	Clock    core.Clock
	Log      *slog.Logger
}

const (
	flushEvery = 250 * time.Millisecond
	// kickAt wakes Run early once this many entries are queued; maxQueue makes
	// Record flush inline, bounding memory when Run is not running (CLI).
	kickAt   = 256
	maxQueue = 4096
)

// syncPrefixes are the actions Record writes before returning (§4.14).
var syncPrefixes = []string{"auth.", "config.", "approval.", "secret."}

// Writer implements core.Auditor. It is the only writer of audit_log: every
// append goes through flush, which holds flushMu while it chains the rows, so
// prev_hash always names the row before.
type Writer struct {
	store core.Store
	red   core.Redactor
	clock core.Clock
	log   *slog.Logger

	mu    sync.Mutex
	queue []core.AuditEntry
	kick  chan struct{}

	flushMu sync.Mutex
	head    []byte // hash of the last row; nil until loaded
}

var _ core.Auditor = (*Writer)(nil)

func New(d Deps) (*Writer, error) {
	if d.Store == nil {
		return nil, errors.New("audit: no store")
	}
	w := &Writer{store: d.Store, red: d.Redactor, clock: d.Clock, log: d.Log, kick: make(chan struct{}, 1)}
	if w.clock == nil {
		w.clock = core.SystemClock{}
	}
	if w.log == nil {
		w.log = slog.New(slog.DiscardHandler)
	}
	return w, nil
}

// Run flushes batched entries every 250ms until ctx is done, then flushes once more.
func (w *Writer) Run(ctx context.Context) error {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := w.Flush(fctx); err != nil {
				w.log.Error("audit: final flush failed", "err", err)
			}
			return nil
		case <-t.C:
		case <-w.kick:
		}
		if err := w.Flush(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("audit: flush failed", "err", err)
		}
	}
}

// Record validates and redacts e, then queues it. auth.*, config.*,
// approval.* and secret.* entries are flushed before Record returns, so the
// caller learns whether they were written.
func (w *Writer) Record(ctx context.Context, e core.AuditEntry) error {
	if err := w.normalize(&e); err != nil {
		return err
	}
	w.mu.Lock()
	w.queue = append(w.queue, e)
	n := len(w.queue)
	w.mu.Unlock()
	if isSync(e.Action) || n >= maxQueue {
		return w.Flush(ctx)
	}
	if n >= kickAt {
		select {
		case w.kick <- struct{}{}:
		default:
		}
	}
	return nil
}

func isSync(action string) bool {
	for _, p := range syncPrefixes {
		if strings.HasPrefix(action, p) {
			return true
		}
	}
	return false
}

// normalize applies the column defaults and constraints up front, so a queued
// entry can never fail at the database and block the entries behind it.
func (w *Writer) normalize(e *core.AuditEntry) error {
	if e.Actor == "" || e.Action == "" {
		return fmt.Errorf("audit: actor and action are required: %w", core.ErrInvalid)
	}
	if e.At.IsZero() {
		e.At = w.clock.Now()
	}
	e.At = time.UnixMilli(e.At.UnixMilli()).UTC()
	if e.Outcome == "" {
		e.Outcome = "ok"
	}
	switch e.Outcome {
	case "ok", "denied", "error":
	default:
		return fmt.Errorf("audit: outcome %q: %w", e.Outcome, core.ErrInvalid)
	}
	e.ID = 0
	detail := []byte(e.Detail)
	if len(detail) == 0 {
		detail = []byte("{}")
	}
	if w.red != nil {
		detail = w.red.RedactBytes(detail)
	}
	canon, err := core.CanonicalJSON(detail)
	if err != nil {
		return fmt.Errorf("audit: detail is not JSON: %w", core.ErrInvalid)
	}
	e.Detail = canon
	return nil
}

// Flush writes every queued entry in one transaction, chaining each to the
// previous row. On failure the entries go back to the front of the queue and
// the head is re-read on the next attempt.
func (w *Writer) Flush(ctx context.Context) error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	w.mu.Lock()
	batch := w.queue
	w.queue = nil
	w.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	head, err := w.loadHead(ctx)
	if err == nil {
		err = w.store.Tx(ctx, func(tx core.Store) error {
			prev := head
			for i := range batch {
				h, err := chainHash(prev, &batch[i])
				if err != nil {
					return err
				}
				if _, err := tx.Audit().Append(ctx, &batch[i], prev, h); err != nil {
					return err
				}
				prev = h
			}
			head = prev
			return nil
		})
	}
	if err != nil {
		w.head = nil
		for i := range batch {
			batch[i].ID = 0
		}
		w.mu.Lock()
		w.queue = append(batch, w.queue...)
		w.mu.Unlock()
		return fmt.Errorf("audit: flush: %w", err)
	}
	w.head = head
	return nil
}

func (w *Writer) loadHead(ctx context.Context) ([]byte, error) {
	if w.head != nil {
		return w.head, nil
	}
	id, hash, err := w.store.Audit().Head(ctx)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		return genesis(), nil
	}
	return hash, nil
}
