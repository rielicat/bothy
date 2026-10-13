package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

// handle is what every repository runs through. Outside a transaction reads go
// to the query_only readers and writes to the single writer connection; inside
// Tx both go to the transaction so reads see its own writes. Code running
// inside Tx must use the Store it was given: the writer pool has one
// connection, so calling the outer DB from fn blocks forever.
type handle struct {
	db *DB
	rq querier
	wq querier
	tx *sql.Tx
}

// txStore is the core.Store handed to Tx callbacks.
type txStore struct{ *handle }

func (t txStore) Backup(context.Context, string) (string, error) {
	return "", fmt.Errorf("store: backup inside a transaction: %w", core.ErrInvalid)
}

func (t txStore) SQLiteVersion() string { return t.db.version }

func (t txStore) Close() error {
	return fmt.Errorf("store: close inside a transaction: %w", core.ErrInvalid)
}

func (h *handle) Meta() core.MetaRepo                { return metaRepo{h} }
func (h *handle) Users() core.UserRepo               { return userRepo{h} }
func (h *handle) Principals() core.PrincipalRepo     { return principalRepo{h} }
func (h *handle) Spaces() core.SpaceRepo             { return spaceRepo{h} }
func (h *handle) Identities() core.IdentityRepo      { return identityRepo{h} }
func (h *handle) Pairings() core.PairingRepo         { return pairingRepo{h} }
func (h *handle) AuthSessions() core.AuthSessionRepo { return authSessionRepo{h} }
func (h *handle) Tokens() core.TokenRepo             { return tokenRepo{h} }
func (h *handle) Sessions() core.SessionRepo         { return sessionRepo{h} }
func (h *handle) Messages() core.MessageRepo         { return messageRepo{h} }
func (h *handle) Pins() core.PinRepo                 { return pinRepo{h} }
func (h *handle) Runs() core.RunRepo                 { return runRepo{h} }
func (h *handle) Approvals() core.ApprovalRepo       { return approvalRepo{h} }
func (h *handle) Grants() core.GrantRepo             { return grantRepo{h} }
func (h *handle) Schedules() core.ScheduleRepo       { return scheduleRepo{h} }
func (h *handle) Usage() core.UsageRepo              { return usageRepo{h} }
func (h *handle) Audit() core.AuditRepo              { return auditRepo{h} }
func (h *handle) Egress() core.EgressRepo            { return egressRepo{h} }
func (h *handle) MCP() core.MCPRepo                  { return mcpRepo{h} }
func (h *handle) Skills() core.SkillRepo             { return skillRepo{h} }
func (h *handle) MemoryEvents() core.MemoryEventRepo { return memoryEventRepo{h} }
func (h *handle) Vault() core.VaultRepo              { return vaultRepo{h} }
func (h *handle) Dedup() core.DedupRepo              { return dedupRepo{h} }
func (h *handle) Uploads() core.UploadRepo           { return uploadRepo{h} }

// Tx runs fn inside BEGIN IMMEDIATE on the writer (the DSN sets _txlock). A
// nested Tx joins the outer transaction.
func (h *handle) Tx(ctx context.Context, fn func(core.Store) error) error {
	if h.tx != nil {
		return fn(txStore{h})
	}
	tx, err := h.db.w.BeginTx(ctx, nil)
	if err != nil {
		return dbErr("begin", err)
	}
	done := false
	defer func() {
		if !done {
			_ = tx.Rollback()
		}
	}()
	if err := fn(txStore{&handle{db: h.db, rq: tx, wq: tx, tx: tx}}); err != nil {
		return err
	}
	done = true
	if err := tx.Commit(); err != nil {
		return dbErr("commit", err)
	}
	return nil
}

// write runs a multi-statement change atomically: in its own transaction, or
// under a savepoint inside Tx so a failed step leaves the caller's
// transaction exactly as it was.
func (h *handle) write(ctx context.Context, fn func(q querier) error) error {
	if h.tx == nil {
		return h.Tx(ctx, func(s core.Store) error { return fn(s.(txStore).wq) })
	}
	if _, err := h.tx.ExecContext(ctx, "SAVEPOINT w"); err != nil {
		return dbErr("savepoint", err)
	}
	if err := fn(h.tx); err != nil {
		bg := context.WithoutCancel(ctx)
		_, _ = h.tx.ExecContext(bg, "ROLLBACK TO w")
		_, _ = h.tx.ExecContext(bg, "RELEASE w")
		return err
	}
	if _, err := h.tx.ExecContext(ctx, "RELEASE w"); err != nil {
		return dbErr("release", err)
	}
	return nil
}

func (h *handle) now() time.Time { return h.db.clock.Now() }

// dbErr maps driver errors onto the core sentinels the conformance suite pins:
// UNIQUE and PRIMARY KEY => ErrConflict; CHECK, FOREIGN KEY, NOT NULL, STRICT
// datatype and trigger aborts => ErrInvalid; no rows => ErrNotFound.
func dbErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: %s: %w", op, core.ErrNotFound)
	}
	if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrInvalid) {
		return err
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_CONSTRAINT {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_ROWID:
			return fmt.Errorf("store: %s: %w: %w", op, core.ErrConflict, err)
		default:
			return fmt.Errorf("store: %s: %w: %w", op, core.ErrInvalid, err)
		}
	}
	return fmt.Errorf("store: %s: %w", op, err)
}

func notFound(what string) error { return fmt.Errorf("store: %s: %w", what, core.ErrNotFound) }

func invalid(what string) error { return fmt.Errorf("store: %s: %w", what, core.ErrInvalid) }

func conflict(what string) error { return fmt.Errorf("store: %s: %w", what, core.ErrConflict) }

// mustAffect turns "no row changed" into ErrNotFound.
func mustAffect(op string, res sql.Result, err error) error {
	if err != nil {
		return dbErr(op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return dbErr(op, err)
	}
	if n == 0 {
		return notFound(op)
	}
	return nil
}

func queryOne[T any](ctx context.Context, q querier, op string, scan func(scanner) (T, error), query string, args ...any) (T, error) {
	v, err := scan(q.QueryRowContext(ctx, query, args...))
	if err != nil {
		var zero T
		return zero, dbErr(op, err)
	}
	return v, nil
}

func queryAll[T any](ctx context.Context, q querier, op string, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbErr(op, err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, dbErr(op, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(op, err)
	}
	return out, nil
}

func exists(ctx context.Context, q querier, op, query string, args ...any) (bool, error) {
	var ok bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS("+query+")", args...).Scan(&ok); err != nil {
		return false, dbErr(op, err)
	}
	return ok, nil
}

// viewerCond restricts col to v's principals. ok=false means v sees nothing:
// an empty principal list must never become "IN ()".
func viewerCond(col string, v core.Viewer) (cond string, args []any, ok bool) {
	if v.System {
		return "1", nil, true
	}
	if len(v.Principals) == 0 {
		return "", nil, false
	}
	args = make([]any, len(v.Principals))
	for i, p := range v.Principals {
		args[i] = string(p)
	}
	return col + " IN (" + placeholders(len(args)) + ")", args, true
}

func placeholders(n int) string { return strings.Repeat(",?", n)[1:] }

func pageLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 500:
		return 500
	}
	return n
}

// ms is the unix-ms INTEGER encoding of a time; the zero time is 0.
func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func durMS(d time.Duration) int64 { return d.Milliseconds() }

func fromDurMS(v int64) time.Duration { return time.Duration(v) * time.Millisecond }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullStr maps "" to NULL for nullable columns such as channel_key, where
// UNIQUE indexes and foreign keys must not see an empty string.
func nullStr[T ~string](s T) any {
	if s == "" {
		return nil
	}
	return string(s)
}

// jsonText encodes v for a TEXT JSON column. STRICT tables reject []byte
// (BLOB) in TEXT columns, so JSON is always bound as a string.
func jsonText(op string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: %s: %w: %w", op, core.ErrInvalid, err)
	}
	return string(b), nil
}

// jsonNullable is jsonText for nullable columns: a nil pointer is NULL.
func jsonNullable[T any](op string, v *T) (any, error) {
	if v == nil {
		return nil, nil
	}
	return jsonText(op, v)
}

func decodeJSON[T any](op, s string, v *T) error {
	if s == "" || s == "null" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fmt.Errorf("store: decode %s: %w", op, err)
	}
	return nil
}

func decodeNullable[T any](op string, s sql.NullString) (*T, error) {
	if !s.Valid || s.String == "null" {
		return nil, nil
	}
	v := new(T)
	if err := decodeJSON(op, s.String, v); err != nil {
		return nil, err
	}
	return v, nil
}

// seText stores a side effect by name; an invalid class becomes NULL so the
// NOT NULL constraint rejects it.
func seText(se core.SideEffect) any {
	if !se.Valid() {
		return nil
	}
	return se.String()
}

func parseSE(s string) core.SideEffect {
	se, err := core.ParseSideEffect(s)
	if err != nil {
		return 0
	}
	return se
}

// blob binds a byte slice for a NOT NULL BLOB column; nil would bind NULL.
func blob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func oneOf[T comparable](v T, set ...T) bool {
	for _, x := range set {
		if v == x {
			return true
		}
	}
	return false
}

func idOr[T ~string](id T, prefix string) T {
	if id == "" {
		return T(core.NewID(prefix))
	}
	return id
}

// escapeLike escapes LIKE wildcards for use with ESCAPE '\'.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// escapeGlob makes s match literally in a GLOB pattern.
func escapeGlob(s string) string {
	return strings.NewReplacer(`[`, `[[]`, `*`, `[*]`, `?`, `[?]`).Replace(s)
}
