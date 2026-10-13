package audit

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/store"
)

// ArchiveResult describes one offline archive run. Count is 0 (and nothing
// changed) when no row was older than the cutoff.
type ArchiveResult struct {
	File     string // JSONL export (0600)
	FromID   int64
	ToID     int64
	Count    int64
	AnchorID int64
}

// archiveLine is one exported row.
type archiveLine struct {
	ID       int64           `json:"id"`
	TS       int64           `json:"ts"`
	Actor    string          `json:"actor"`
	Action   string          `json:"action"`
	Target   string          `json:"target"`
	Outcome  string          `json:"outcome"`
	Session  string          `json:"session_id"`
	Run      string          `json:"run_id"`
	IP       string          `json:"ip"`
	Detail   json.RawMessage `json:"detail"`
	PrevHash string          `json:"prev_hash"`
	Hash     string          `json:"hash"`
}

// Archive moves audit rows older than before out of the database at dbPath
// for `bothy audit archive` (gateway stopped). It refuses a broken chain, so
// tampering is never laundered into an archive. The oldest rows up to the
// newest one dated before the cutoff are exported to
// <db dir>/backups/audit-<from>-<to>-<UTC time>.jsonl (fsynced before
// anything is deleted), then deleted with an audit.archived anchor appended
// in the same transaction. The anchor chains to the remaining log and records
// the range, the count and the last archived hash, which keeps Verify green.
func Archive(ctx context.Context, dbPath string, before time.Time) (*ArchiveResult, error) {
	db, err := store.Open(ctx, store.Options{Path: dbPath})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return archive(ctx, db, filepath.Join(filepath.Dir(dbPath), "backups"), before, time.Now)
}

func archive(ctx context.Context, db *store.DB, dir string, before time.Time, now func() time.Time) (*ArchiveResult, error) {
	bad, err := verify(ctx, db)
	if err != nil {
		return nil, err
	}
	if bad != 0 {
		return nil, fmt.Errorf("audit: chain broken at id %d; refusing to archive", bad)
	}
	newest, err := db.Audit().Query(ctx, core.AuditFilter{To: before, Page: core.Page{Limit: 1}})
	if err != nil {
		return nil, err
	}
	if len(newest) == 0 {
		return &ArchiveResult{}, nil
	}
	res := &ArchiveResult{ToID: newest[0].ID}
	lastHash, err := exportRange(ctx, db, dir, now(), res)
	if err != nil {
		return nil, err
	}
	anchorID, deleted, err := db.PruneAudit(ctx, res.ToID, func(anchorID, headID int64, headHash []byte) (*core.AuditEntry, []byte, []byte, error) {
		prev := headHash
		if headID == 0 {
			prev = lastHash
		}
		d, err := core.CanonicalJSON(anchorDetail{FromID: res.FromID, ToID: res.ToID, Count: res.Count,
			LastHash: hex.EncodeToString(lastHash), File: filepath.Base(res.File)})
		if err != nil {
			return nil, nil, nil, err
		}
		e := &core.AuditEntry{ID: anchorID, At: time.UnixMilli(now().UnixMilli()).UTC(), Actor: "system", Action: ActionArchived,
			Target: "audit_log", Outcome: "ok", Detail: d}
		h, err := chainHash(prev, e)
		return e, prev, h, err
	})
	if err != nil {
		return nil, err
	}
	if deleted != res.Count {
		return nil, fmt.Errorf("audit: archived %d rows but deleted %d", res.Count, deleted)
	}
	res.AnchorID = anchorID
	return res, nil
}

// exportRange writes rows 1..res.ToID as JSONL and fills FromID, Count and
// File; it returns the hash of the last exported row.
func exportRange(ctx context.Context, db *store.DB, dir string, at time.Time, res *ArchiveResult) ([]byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit: archive: %w", err)
	}
	f, err := os.CreateTemp(dir, ".audit-*.jsonl.tmp")
	if err != nil {
		return nil, fmt.Errorf("audit: archive: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("audit: archive: %w", err)
	}
	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	var last []byte
	err = db.Audit().Range(ctx, 0, res.ToID, func(e *core.AuditEntry, prevHash, hash []byte) error {
		if res.Count == 0 {
			res.FromID = e.ID
		}
		res.Count++
		last = hash
		return enc.Encode(archiveLine{ID: e.ID, TS: e.At.UnixMilli(), Actor: e.Actor, Action: e.Action, Target: e.Target,
			Outcome: e.Outcome, Session: string(e.Session), Run: string(e.Run), IP: e.IP, Detail: e.Detail,
			PrevHash: hex.EncodeToString(prevHash), Hash: hex.EncodeToString(hash)})
	})
	if err != nil {
		return nil, fmt.Errorf("audit: archive export: %w", err)
	}
	if res.Count == 0 {
		return nil, errors.New("audit: archive: no rows to export")
	}
	if err := bw.Flush(); err != nil {
		return nil, fmt.Errorf("audit: archive: %w", err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("audit: archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("audit: archive: %w", err)
	}
	res.File = filepath.Join(dir, fmt.Sprintf("audit-%d-%d-%s.jsonl", res.FromID, res.ToID, at.UTC().Format("20060102T150405")))
	if err := os.Link(tmp, res.File); err != nil {
		return nil, fmt.Errorf("audit: archive: %w", err)
	}
	return last, nil
}
