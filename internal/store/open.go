package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rielicat/bothy/internal/core"

	_ "modernc.org/sqlite"
)

const minSQLiteVersion = "3.53.4"

// pragmas run on every pooled connection; modernc sorts them (busy_timeout
// first) and applies them after SQLITE_DBCONFIG_DEFENSIVE.
var pragmas = []string{
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
	"busy_timeout(5000)",
	"cache_size(-2048)",
	"mmap_size(0)",
	"temp_store(MEMORY)",
	"trusted_schema(OFF)",
	"cell_size_check(ON)",
}

// versionFunc reports the linked SQLite version; tests inject old versions.
type versionFunc func(ctx context.Context, db *sql.DB) (string, error)

func queryVersion(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v)
	return v, err
}

func open(ctx context.Context, o Options, fsys fs.FS, version versionFunc) (*DB, error) {
	if o.Path == "" {
		return nil, fmt.Errorf("store: %w: empty database path", core.ErrInvalid)
	}
	if o.Clock == nil {
		o.Clock = core.SystemClock{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.BackupDir == "" {
		o.BackupDir = filepath.Join(filepath.Dir(o.Path), "backups")
	}
	if o.BackupsKeep <= 0 {
		o.BackupsKeep = 3
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	// SQLite creates -wal and -shm with the database file's mode, so creating
	// the file first is what makes all three 0600 regardless of umask.
	f, err := os.OpenFile(o.Path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	db := &DB{path: o.Path, backupDir: o.BackupDir, workDir: o.WorkDir, clock: o.Clock, log: o.Log}
	db.handle = handle{db: db}
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()
	if db.w, err = sql.Open("sqlite", dsn(o.Path, false)); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db.w.SetMaxOpenConns(1)
	db.w.SetMaxIdleConns(1)
	db.w.SetConnMaxLifetime(0)
	db.w.SetConnMaxIdleTime(0)
	if db.version, err = version(ctx, db.w); err != nil {
		return nil, fmt.Errorf("store: open %s: %w", o.Path, err)
	}
	if err := checkVersion(db.version); err != nil {
		return nil, err
	}
	backup := func(ctx context.Context, target int) error {
		p, err := backupBeforeMigration(ctx, db.w, o.BackupDir, o.BackupsKeep, o.Clock, target)
		if err == nil {
			o.Log.Info("pre-migration backup written", "path", p)
		}
		return err
	}
	applied, err := migrate(ctx, db.w, ms, backup)
	if err != nil {
		return nil, err
	}
	if applied > 0 {
		o.Log.Info("database migrated", "applied", applied, "version", ms[len(ms)-1].version)
	}
	if db.r, err = sql.Open("sqlite", dsn(o.Path, true)); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db.r.SetMaxOpenConns(2)
	db.r.SetMaxIdleConns(2)
	if err := db.r.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("store: reader: %w", err)
	}
	db.rq, db.wq = db.r, db.w
	if err := ensureInstallID(ctx, db.w); err != nil {
		return nil, err
	}
	ok = true
	return db, nil
}

// dsn builds a file: URI so paths with spaces, '?' or '#' survive; SQLite
// ignores the driver's underscore parameters.
func dsn(path string, reader bool) string {
	q := url.Values{}
	q.Set("_defensive", "1")
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if reader {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive paths: file:///C:/...
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String()
}

func checkVersion(v string) error {
	cmp, err := compareVersions(v, minSQLiteVersion)
	if err != nil {
		return fmt.Errorf("store: unrecognised SQLite version %q: %w", v, err)
	}
	if cmp < 0 {
		return fmt.Errorf("store: SQLite %s is too old: Bothy requires %s or newer", v, minSQLiteVersion)
	}
	return nil
}

// compareVersions compares dotted numeric versions; missing parts count as 0.
func compareVersions(a, b string) (int, error) {
	pa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersion(s string) ([3]int, error) {
	var v [3]int
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return v, errors.New("want major.minor.patch")
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("bad component %q", p)
		}
		v[i] = n
	}
	return v, nil
}

// ensureInstallID writes meta.install_id once, on the first open (SPEC §6).
func ensureInstallID(ctx context.Context, w *sql.DB) error {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("store: install id: %w", err)
	}
	if _, err := w.ExecContext(ctx, `INSERT INTO meta(k, v) VALUES ('install_id', ?) ON CONFLICT(k) DO NOTHING`, hex.EncodeToString(b[:])); err != nil {
		return fmt.Errorf("store: install id: %w", err)
	}
	return nil
}
