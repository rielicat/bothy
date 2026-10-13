package store

import (
	"context"
	"errors"
	"time"

	"github.com/rielicat/bothy/internal/config"
)

const (
	retentionEvery    = time.Hour
	pairingKeep       = 7 * 24 * time.Hour
	hiddenSessionKeep = 7 * 24 * time.Hour
	defaultDedupKeep  = 24 * time.Hour
)

// Stateless /v1 requests leave one hidden api session each. The pass runs in
// one write transaction, so it must only touch the not yet archived ones
// through sessions_hidden_api (same expression), never scan every session
// ever made. Archived ones are never shown or resumed with their history,
// so their snapshot and summary are cleared too; their runs keep the costs.
const (
	hiddenDue           = `hidden = 1 AND origin = 'api' AND archived = 0 AND max(created_at, updated_at, last_message_at) < ?`
	pruneHiddenMessages = `DELETE FROM messages WHERE session_id IN (SELECT id FROM sessions WHERE ` + hiddenDue + `)`
	archiveHidden       = `UPDATE sessions SET archived = 1, system_snapshot = '', tools_snapshot = '[]', snapshot_hash = '',
		summary = '' WHERE ` + hiddenDue
)

// RunRetention prunes egress_log, inbound_dedup, expired auth sessions and old
// runs per config every hour. Blocks until ctx is done.
func (db *DB) RunRetention(ctx context.Context, cfg config.View) error {
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	for {
		if err := db.retain(ctx, cfg.Current()); err != nil && ctx.Err() == nil {
			db.log.Error("retention pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// retain runs one retention pass. Every step runs even when an earlier one
// fails; the errors are joined. audit_log is never touched.
func (db *DB) retain(ctx context.Context, c *config.Config) error {
	now := db.clock.Now()
	r := c.Storage.Retention
	var errs []error
	run := func(op, query string, args ...any) {
		if _, err := db.w.ExecContext(ctx, query, args...); err != nil {
			errs = append(errs, dbErr(op, err))
		}
	}
	if d := r.EgressLog.D(); d > 0 {
		if _, err := db.Egress().Prune(ctx, now.Add(-d)); err != nil {
			errs = append(errs, err)
		}
	}
	dedup := r.InboundDedup.D()
	if dedup <= 0 {
		dedup = defaultDedupKeep
	}
	if _, err := db.Dedup().Prune(ctx, now.Add(-dedup)); err != nil {
		errs = append(errs, err)
	}
	if _, err := db.AuthSessions().DeleteExpired(ctx, now); err != nil {
		errs = append(errs, err)
	}
	cutoff := ms(now.Add(-pairingKeep))
	run("prune pairings", `DELETE FROM pairing_codes WHERE (used_at > 0 AND used_at < ?1) OR expires_at < ?1`, cutoff)
	if d := r.Runs.D(); d > 0 {
		run("prune runs", `DELETE FROM runs WHERE created_at < ? AND status NOT IN ('queued', 'running', 'waiting_approval', 'parked')`,
			ms(now.Add(-d)))
	}
	err := db.write(ctx, func(q querier) error {
		cut := ms(now.Add(-hiddenSessionKeep))
		if _, err := q.ExecContext(ctx, pruneHiddenMessages, cut); err != nil {
			return dbErr("prune hidden sessions", err)
		}
		_, err := q.ExecContext(ctx, archiveHidden, cut)
		return dbErr("archive hidden sessions", err)
	})
	if err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
