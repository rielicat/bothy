package store

import (
	"context"
	"fmt"

	"github.com/rielicat/bothy/internal/core"
)

// AnchorFunc builds the audit.archived anchor row that PruneAudit stores under
// anchorID, from the head of the log that remains after pruning (id 0 and nil
// hash when nothing remains).
type AnchorFunc func(anchorID, headID int64, headHash []byte) (e *core.AuditEntry, prevHash, hash []byte, err error)

// PruneAudit deletes audit_log rows with id <= throughID and appends the
// anchor row, all in one transaction. The append-only triggers are dropped
// and re-created inside that transaction (DDL is transactional in SQLite), so
// a failure leaves both the rows and the triggers in place. The anchor gets
// an id above every archived one, never reusing an archived id. It exists
// only for the offline `bothy audit archive`; the gateway never prunes.
func (db *DB) PruneAudit(ctx context.Context, throughID int64, anchor AnchorFunc) (anchorID int64, deleted int64, err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, dbErr("prune audit", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	type trigger struct{ name, sql string }
	triggers, err := queryAll(ctx, tx, "prune audit", func(sc scanner) (trigger, error) {
		var t trigger
		err := sc.Scan(&t.name, &t.sql)
		return t, err
	}, `SELECT name, sql FROM sqlite_schema WHERE type = 'trigger' AND tbl_name = 'audit_log' ORDER BY name`)
	if err != nil {
		return 0, 0, err
	}
	for _, t := range triggers {
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+t.name+`"`); err != nil {
			return 0, 0, dbErr("prune audit: drop trigger", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE id <= ?`, throughID)
	if err != nil {
		return 0, 0, dbErr("prune audit", err)
	}
	if deleted, err = res.RowsAffected(); err != nil {
		return 0, 0, dbErr("prune audit", err)
	}
	for _, t := range triggers {
		if _, err = tx.ExecContext(ctx, t.sql); err != nil {
			return 0, 0, dbErr("prune audit: restore trigger", err)
		}
	}
	var headID int64
	var headHash []byte
	if err = tx.QueryRowContext(ctx, `SELECT id, hash FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&headID, &headHash); err != nil && !isNoRows(err) {
		return 0, 0, dbErr("prune audit: head", err)
	}
	anchorID = max(throughID, headID) + 1
	e, prev, hash, err := anchor(anchorID, headID, headHash)
	if err != nil {
		return 0, 0, fmt.Errorf("store: prune audit: anchor: %w", err)
	}
	detail := string(e.Detail)
	if detail == "" {
		detail = "{}"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(id, ts, actor, action, target, outcome, session_id, run_id, ip, detail,
		prev_hash, hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, anchorID, ms(e.At), e.Actor, e.Action, e.Target, e.Outcome,
		string(e.Session), string(e.Run), e.IP, detail, blob(prev), blob(hash)); err != nil {
		return 0, 0, dbErr("prune audit: anchor", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, dbErr("prune audit: commit", err)
	}
	return anchorID, deleted, nil
}
