package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
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
	// maxHeld bounds what stays queued while the database refuses writes;
	// entries beyond it are dropped with an error instead of growing forever.
	maxHeld = 4 * maxQueue
)

// syncPrefixes are the actions Record writes before returning (§4.14).
var syncPrefixes = []string{"auth.", "config.", "approval.", "secret."}

// Writer implements core.Auditor. Within the process it is the only writer of
// audit_log: every append goes through flush, which holds flushMu while it
// chains the rows.
type Writer struct {
	store core.Store
	red   core.Redactor
	clock core.Clock
	log   *slog.Logger

	mu    sync.Mutex
	queue []core.AuditEntry
	kick  chan struct{}

	flushMu sync.Mutex
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
// approval.* and secret.* entries are written before Record returns, so the
// caller learns whether they were written; one that fails is not kept for a
// later flush, because the caller has already acted on the failure (the vault
// withholds the secret, for example).
func (w *Writer) Record(ctx context.Context, e core.AuditEntry) error {
	if err := w.normalize(&e); err != nil {
		return err
	}
	if isSync(e.Action) {
		return w.flush(ctx, &e)
	}
	w.mu.Lock()
	w.queue = append(w.queue, e)
	n := len(w.queue)
	w.mu.Unlock()
	if n >= maxQueue {
		return w.flush(ctx, nil)
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
	detail, err := w.redactDetail(e.Detail)
	if err != nil {
		return fmt.Errorf("audit: detail is not JSON: %w", core.ErrInvalid)
	}
	e.Detail = detail
	return nil
}

// redactDetail returns detail as canonical JSON with secrets masked inside
// its strings, keys and numbers. Masking the raw bytes instead could cut
// through JSON syntax (a registered value such as "1234" inside a number) and
// turn every later entry that mentions it into an invalid one.
func (w *Writer) redactDetail(detail json.RawMessage) (json.RawMessage, error) {
	if len(detail) == 0 {
		return json.RawMessage("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(detail))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	if w.red != nil {
		tree = w.redactValue(tree)
	}
	return core.CanonicalJSON(tree)
}

func (w *Writer) redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return w.red.Redact(x)
	case json.Number:
		if s := w.red.Redact(string(x)); s != string(x) {
			return s
		}
		return x
	case []any:
		for i := range x {
			x[i] = w.redactValue(x[i])
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[w.red.Redact(k)] = w.redactValue(val)
		}
		return out
	}
	return v
}

// Flush writes every queued entry in one transaction, chaining each to the
// previous row. On failure the entries go back to the front of the queue.
func (w *Writer) Flush(ctx context.Context) error { return w.flush(ctx, nil) }

// flush writes the queue plus extra (a synchronous entry, which is not
// re-queued when the write fails).
func (w *Writer) flush(ctx context.Context, extra *core.AuditEntry) error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	w.mu.Lock()
	batch := w.queue
	w.queue = nil
	w.mu.Unlock()
	if extra != nil {
		batch = append(batch, *extra)
	}
	if len(batch) == 0 {
		return nil
	}
	err := w.store.Tx(ctx, func(tx core.Store) error { return appendChain(ctx, tx, batch) })
	if err == nil {
		return nil
	}
	if extra != nil {
		batch = batch[:len(batch)-1]
	}
	w.requeue(batch)
	return fmt.Errorf("audit: flush: %w", err)
}

// requeue puts a failed batch back in front of what was recorded meanwhile.
// Ids need no reset: appendChain assigns them on every attempt.
func (w *Writer) requeue(batch []core.AuditEntry) {
	w.mu.Lock()
	q := append(batch, w.queue...)
	dropped := max(len(q)-maxHeld, 0)
	w.queue = q[:len(q)-dropped]
	w.mu.Unlock()
	if dropped > 0 {
		w.log.Error("audit: database unavailable; dropped audit entries", "dropped", dropped, "queued", maxHeld)
	}
}

// appendChain appends batch after the current head of the log. The head is
// read inside the write transaction rather than cached, because other
// processes (the CLI) append to the same log while the gateway runs.
func appendChain(ctx context.Context, tx core.Store, batch []core.AuditEntry) error {
	id, prev, err := tx.Audit().Head(ctx)
	if err != nil {
		return err
	}
	if id == 0 {
		prev = genesis()
	}
	for i := range batch {
		e := &batch[i]
		// SQLite gives a rowid table max(id)+1, and the hash covers the id, so
		// a row stored under any other id must not be committed.
		e.ID = id + 1
		h, err := chainHash(prev, e)
		if err != nil {
			return err
		}
		got, err := tx.Audit().Append(ctx, e, prev, h)
		if err != nil {
			return err
		}
		if got != id+1 {
			return fmt.Errorf("audit: row stored as id %d, chained as id %d", got, id+1)
		}
		id, prev = got, h
	}
	return nil
}
