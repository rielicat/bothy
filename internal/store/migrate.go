package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads migrations/NNNN_*.sql; versions must run 1..N without gaps.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, n := range names {
		base := path.Base(n)
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || len(num) != 4 || v <= 0 {
			return nil, fmt.Errorf("store: bad migration name %q (want NNNN_name.sql)", base)
		}
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: base, sql: string(b)})
	}
	slices.SortFunc(ms, func(a, b migration) int { return a.version - b.version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration %s out of sequence (want version %d)", m.name, i+1)
		}
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("store: no migrations embedded")
	}
	return ms, nil
}

// migrate applies every migration above PRAGMA user_version, each in its own
// transaction together with the user_version bump. backup runs first when
// anything is pending and the database already holds objects.
func migrate(ctx context.Context, db *sql.DB, ms []migration, backup func(ctx context.Context, target int) error) (int, error) {
	var cur int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&cur); err != nil {
		return 0, fmt.Errorf("store: read user_version: %w", err)
	}
	latest := ms[len(ms)-1].version
	if cur > latest {
		return 0, fmt.Errorf("store: database schema version %d is newer than this binary supports (%d)", cur, latest)
	}
	if cur == latest {
		return 0, nil
	}
	var objects int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&objects); err != nil {
		return 0, fmt.Errorf("store: inspect schema: %w", err)
	}
	if objects > 0 && backup != nil {
		if err := backup(ctx, latest); err != nil {
			return 0, fmt.Errorf("store: pre-migration backup: %w", err)
		}
	}
	applied := 0
	for _, m := range ms[cur:] {
		if err := applyMigration(ctx, db, m); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("store: migration %s: set user_version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migration %s: commit: %w", m.name, err)
	}
	return nil
}

const backupTimeFormat = "20060102T150405.000Z"

// backupBeforeMigration writes <dir>/pre-<target>-<ts>.db with VACUUM INTO and
// keeps the newest keep pre-migration backups.
func backupBeforeMigration(ctx context.Context, db *sql.DB, dir string, keep int, clock core.Clock, target int) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("pre-%d-%s.db", target, clock.Now().UTC().Format(backupTimeFormat)))
	// VACUUM INTO accepts an existing empty file, which lets us fix its mode.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", p); err != nil {
		_ = os.Remove(p)
		return "", err
	}
	return p, prunePreMigrationBackups(dir, keep)
}

func prunePreMigrationBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type backup struct{ name, ts string }
	var bs []backup
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "pre-") || !strings.HasSuffix(n, ".db") {
			continue
		}
		i := strings.LastIndexByte(n, '-')
		bs = append(bs, backup{n, strings.TrimSuffix(n[i+1:], ".db")})
	}
	// Fixed-width timestamps sort chronologically; the version part does not.
	slices.SortFunc(bs, func(a, b backup) int { return strings.Compare(b.ts, a.ts) })
	for _, b := range bs[min(keep, len(bs)):] {
		if err := os.Remove(filepath.Join(dir, b.name)); err != nil {
			return err
		}
	}
	return nil
}
