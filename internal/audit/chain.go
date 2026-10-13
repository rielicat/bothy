package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/bothyhq/bothy/internal/core"
)

// ActionArchived is the action of the anchor row an offline archive appends.
const ActionArchived = "audit.archived"

// genesis is the prev_hash of the first row of a log that was never archived.
func genesis() []byte { return make([]byte, sha256.Size) }

// chainRow is what a row's hash covers: every column except the hashes.
type chainRow struct {
	ID      int64           `json:"id"`
	TS      int64           `json:"ts"`
	Actor   string          `json:"actor"`
	Action  string          `json:"action"`
	Target  string          `json:"target"`
	Outcome string          `json:"outcome"`
	Session string          `json:"session_id"`
	Run     string          `json:"run_id"`
	IP      string          `json:"ip"`
	Detail  json.RawMessage `json:"detail"`
}

// chainHash is sha256(prev_hash || canonical JSON of the row without hashes).
func chainHash(prev []byte, e *core.AuditEntry) ([]byte, error) {
	detail := e.Detail
	if len(detail) == 0 {
		detail = json.RawMessage("{}")
	}
	var ts int64
	if !e.At.IsZero() {
		ts = e.At.UnixMilli()
	}
	b, err := core.CanonicalJSON(chainRow{ID: e.ID, TS: ts, Actor: e.Actor, Action: e.Action, Target: e.Target, Outcome: e.Outcome,
		Session: string(e.Session), Run: string(e.Run), IP: e.IP, Detail: detail})
	if err != nil {
		return nil, fmt.Errorf("audit: canonical row: %w", err)
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(b)
	return h.Sum(nil), nil
}

// anchorDetail is the detail of an audit.archived row.
type anchorDetail struct {
	FromID   int64  `json:"from_id"`
	ToID     int64  `json:"to_id"`
	Count    int64  `json:"count"`
	LastHash string `json:"last_hash"` // hex hash of the last archived row
	File     string `json:"file"`
}

// Verify walks the log in id order and returns the id of the first row whose
// hash does not match its contents or whose prev_hash is not the previous
// row's hash; 0 means the chain is intact. The first row may start from the
// genesis hash, or from the last archived hash recorded by an audit.archived
// anchor row later in the log.
func (w *Writer) Verify(ctx context.Context) (int64, error) {
	if err := w.Flush(ctx); err != nil {
		return 0, err
	}
	return verify(ctx, w.store)
}

func verify(ctx context.Context, s core.Store) (int64, error) {
	var (
		firstID   int64
		firstPrev []byte
		prev      []byte
		bad       int64
		anchors   = map[string]bool{}
	)
	err := s.Audit().Range(ctx, 0, 0, func(e *core.AuditEntry, prevHash, hash []byte) error {
		if firstID == 0 {
			firstID, firstPrev = e.ID, bytes.Clone(prevHash)
		} else if bad == 0 && !bytes.Equal(prevHash, prev) {
			bad = e.ID
		}
		// A detail that no longer parses (edited with CHECK constraints
		// ignored) is a tampered row to report, not a reason to fail.
		h, err := chainHash(prevHash, e)
		if bad == 0 && (err != nil || !bytes.Equal(h, hash)) {
			bad = e.ID
		}
		if e.Action == ActionArchived && bytes.Equal(h, hash) {
			var d anchorDetail
			if json.Unmarshal(e.Detail, &d) == nil && d.LastHash != "" {
				anchors[d.LastHash] = true
			}
		}
		prev = bytes.Clone(hash)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("audit: verify: %w", err)
	}
	if firstID != 0 && !bytes.Equal(firstPrev, genesis()) && !anchors[hex.EncodeToString(firstPrev)] {
		return firstID, nil
	}
	return bad, nil
}
