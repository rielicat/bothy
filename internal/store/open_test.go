package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time {
	c.t = c.t.Add(time.Second)
	return c.t
}

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data dir") // a space exercises URI escaping
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "bothy.db"), BackupsKeep: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// latestVersion is the schema version of the embedded migrations.
func latestVersion(t *testing.T) int {
	t.Helper()
	ms, err := loadMigrations(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	return ms[len(ms)-1].version
}

// initOnly holds just 0001_init.sql, so migrator tests can add numbered
// migrations of their own whatever the embedded set holds.
func initOnly(t *testing.T) fstest.MapFS {
	t.Helper()
	init, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"migrations/0001_init.sql": {Data: init}}
}

func TestOpenFresh(t *testing.T) {
	db, dir := openTemp(t)
	if v, want := userVersion(t, db.w), latestVersion(t); v != want {
		t.Fatalf("user_version = %d, want %d", v, want)
	}
	if c, _ := compareVersions(db.SQLiteVersion(), minSQLiteVersion); c < 0 {
		t.Fatalf("SQLiteVersion = %q", db.SQLiteVersion())
	}
	for _, table := range []string{"meta", "users", "principals", "sessions", "messages", "messages_fts", "approvals", "grants", "audit_log", "egress_log"} {
		var n int
		if err := db.w.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name = ?", table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (%v)", table, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); !os.IsNotExist(err) {
		t.Errorf("fresh database took a backup: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"bothy.db", "bothy.db-wal", "bothy.db-shm"} {
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v, want 0600", name, fi.Mode().Perm())
			}
		}
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("data dir mode = %v, want 0700", fi.Mode().Perm())
		}
	}
}

func TestPragmas(t *testing.T) {
	db, _ := openTemp(t)
	want := map[string]string{
		"journal_mode":    "wal",
		"synchronous":     "1",
		"foreign_keys":    "1",
		"busy_timeout":    "5000",
		"cache_size":      "-2048",
		"mmap_size":       "0",
		"temp_store":      "2",
		"trusted_schema":  "0",
		"cell_size_check": "1",
	}
	for name, conns := range map[string]*sql.DB{"writer": db.w, "reader": db.r} {
		for p, v := range want {
			var got string
			if err := conns.QueryRow("PRAGMA " + p).Scan(&got); err != nil {
				t.Fatalf("%s: PRAGMA %s: %v", name, p, err)
			}
			if got != v {
				t.Errorf("%s: PRAGMA %s = %s, want %s", name, p, got, v)
			}
		}
	}
	var qo int
	if err := db.r.QueryRow("PRAGMA query_only").Scan(&qo); err != nil || qo != 1 {
		t.Fatalf("reader query_only = %d, %v", qo, err)
	}
	if _, err := db.r.Exec("INSERT INTO meta(k, v) VALUES ('x', 'y')"); err == nil {
		t.Fatal("reader accepted a write")
	}
	if db.w.Stats().MaxOpenConnections != 1 || db.r.Stats().MaxOpenConnections != 2 {
		t.Fatalf("pool sizes: writer %d, reader %d", db.w.Stats().MaxOpenConnections, db.r.Stats().MaxOpenConnections)
	}
}

func TestSchemaSmoke(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	stmts := []string{
		`INSERT INTO users(id, username, role, pw_hash, created_at, updated_at) VALUES ('usr_a', 'alice', 'owner', 'x', 1, 1)`,
		`INSERT INTO principals(id, kind, user_id, workspace_id, tool_profile, created_at) VALUES ('usr_a', 'user', 'usr_a', 'ws_a', 'standard', 1)`,
		`INSERT INTO sessions(id, principal_id, origin, created_at, updated_at) VALUES ('ses_a', 'usr_a', 'web', 1, 1)`,
		`INSERT INTO messages(session_id, seq, role, provenance, parts, text, created_at) VALUES ('ses_a', 1, 'user', 'user', '[]', 'the quick brown fox', 1)`,
		`INSERT INTO audit_log(ts, actor, action, prev_hash, hash) VALUES (1, 'system', 'boot', x'', x'00')`,
	}
	for _, s := range stmts {
		if _, err := db.w.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	var seq int
	if err := db.r.QueryRowContext(ctx, `SELECT m.seq FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid WHERE messages_fts MATCH '"brown"'`).Scan(&seq); err != nil || seq != 1 {
		t.Fatalf("fts search: seq=%d err=%v", seq, err)
	}
	if _, err := db.w.ExecContext(ctx, `UPDATE audit_log SET actor = 'x'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("audit update: %v", err)
	}
	if _, err := db.w.ExecContext(ctx, `INSERT INTO sessions(id, principal_id, origin, created_at, updated_at) VALUES ('ses_b', 'usr_missing', 'web', 1, 1)`); err == nil {
		t.Fatal("foreign keys not enforced")
	}
	// SQLITE_DBCONFIG_DEFENSIVE: FTS shadow tables are read-only to ordinary SQL.
	if _, err := db.w.ExecContext(ctx, `DELETE FROM messages_fts_data`); err == nil || !strings.Contains(err.Error(), "may not be modified") {
		t.Fatalf("defensive mode is off: shadow table delete returned %v", err)
	}
}

func TestReopen(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.Meta().Get(ctx, "install_id")
	if err != nil || len(id) != 32 {
		t.Fatalf("install_id after first open = %q, %v", id, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.r.QueryRow(`SELECT v FROM meta WHERE k = 'install_id'`).Scan(&v); err != nil || v != id {
		t.Fatalf("install_id changed across reopen: %q -> %q %v", id, v, err)
	}
	if n := userVersion(t, db.w); n != latestVersion(t) {
		t.Fatalf("user_version = %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("reopen without pending migrations took a backup: %v", err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	db, err := Open(context.Background(), Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.w.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(context.Background(), Options{Path: p}); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open = %v, want a newer-schema error", err)
	}
}

func testMigrations(t *testing.T, extra map[string]string) []migration {
	t.Helper()
	fsys := initOnly(t)
	for name, body := range extra {
		fsys["migrations/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestMigratorBackupAndPrune(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	backups := filepath.Join(dir, "backups")
	ctx := context.Background()
	clock := &stepClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}

	db, err := open(ctx, Options{Path: p, BackupDir: backups, BackupsKeep: 2, Clock: clock}, initOnly(t), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.w.Exec(`INSERT INTO meta(k, v) VALUES ('test.marker', 'abc')`); err != nil {
		t.Fatal(err)
	}
	backup := func(ctx context.Context, target int) error {
		_, err := backupBeforeMigration(ctx, db.w, backups, 2, clock, target)
		return err
	}

	ms := testMigrations(t, map[string]string{"0002_extra.sql": `CREATE TABLE extra (id INTEGER PRIMARY KEY) STRICT;`})
	n, err := migrate(ctx, db.w, ms, backup)
	if err != nil || n != 1 {
		t.Fatalf("migrate = %d, %v", n, err)
	}
	if v := userVersion(t, db.w); v != 2 {
		t.Fatalf("user_version = %d", v)
	}
	if n, err := migrate(ctx, db.w, ms, backup); err != nil || n != 0 {
		t.Fatalf("second migrate = %d, %v (not idempotent)", n, err)
	}
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "pre-2-") {
		t.Fatalf("backups after one migration: %v %v", entries, err)
	}
	snap := filepath.Join(backups, entries[0].Name())
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(snap); fi.Mode().Perm() != 0o600 {
			t.Fatalf("backup mode = %v", fi.Mode().Perm())
		}
	}
	bdb, err := sql.Open("sqlite", "file:"+snap+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := bdb.QueryRow(`SELECT v FROM meta WHERE k = 'test.marker'`).Scan(&id); err != nil || id != "abc" {
		t.Fatalf("backup content: %q %v", id, err)
	}
	if v := userVersion(t, bdb); v != 1 {
		t.Fatalf("backup user_version = %d, want the pre-migration 1", v)
	}
	bdb.Close()

	extra := map[string]string{"0002_extra.sql": `CREATE TABLE extra (id INTEGER PRIMARY KEY) STRICT;`}
	for _, next := range []string{"0003_e3.sql", "0004_e4.sql"} {
		extra[next] = "CREATE TABLE " + next[5:7] + " (id INTEGER PRIMARY KEY) STRICT;"
		if _, err := migrate(ctx, db.w, testMigrations(t, extra), backup); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ = os.ReadDir(backups)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || !strings.HasPrefix(names[0], "pre-3-") || !strings.HasPrefix(names[1], "pre-4-") {
		t.Fatalf("pruned backups = %v, want the newest pre-3 and pre-4", names)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := open(ctx, Options{Path: filepath.Join(dir, "bothy.db")}, initOnly(t), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms := testMigrations(t, map[string]string{"0002_bad.sql": `CREATE TABLE half (id INTEGER) STRICT; SELECT * FROM no_such_table;`})
	if _, err := migrate(ctx, db.w, ms, nil); err == nil {
		t.Fatal("bad migration applied")
	}
	if v := userVersion(t, db.w); v != 1 {
		t.Fatalf("user_version = %d after failed migration", v)
	}
	var n int
	if err := db.w.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name = 'half'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial migration left table behind (n=%d, %v)", n, err)
	}
}

func TestLoadMigrations(t *testing.T) {
	ms, err := loadMigrations(migrationsFS)
	if err != nil || len(ms) == 0 || ms[0].name != "0001_init.sql" {
		t.Fatalf("embedded migrations = %+v, %v", ms, err)
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Fatalf("embedded migration %s has version %d, want %d", m.name, m.version, i+1)
		}
	}
	bad := []fstest.MapFS{
		{},
		{"migrations/init.sql": {}},
		{"migrations/1_init.sql": {}},
		{"migrations/0001_a.sql": {}, "migrations/0003_c.sql": {}},
		{"migrations/0001_a.sql": {}, "migrations/0001_b.sql": {}},
		{"migrations/0000_zero.sql": {}},
	}
	for i, fsys := range bad {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("case %d: accepted %v", i, fsys)
		}
	}
}

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		v  string
		ok bool
	}{
		{"3.53.4", true},
		{"3.53.5", true},
		{"3.54.0", true},
		{"3.100.0", true},
		{"4.0.0", true},
		{"3.53.3", false},
		{"3.53", false},
		{"3.9.9", false},
		{"2.99.99", false},
		{"", false},
		{"3.53.x", false},
		{"3.53.4.1", false},
	}
	for _, tt := range tests {
		if err := checkVersion(tt.v); (err == nil) != tt.ok {
			t.Errorf("checkVersion(%q) = %v, want ok=%v", tt.v, err, tt.ok)
		}
	}
}

func TestDSN(t *testing.T) {
	d := dsn("/data dir/bothy?.db", false)
	if !strings.HasPrefix(d, "file:///data%20dir/bothy%3F.db?") {
		t.Fatalf("dsn = %s", d)
	}
	if !strings.Contains(d, "_txlock=immediate") || !strings.Contains(d, "_defensive=1") || strings.Contains(d, "query_only") {
		t.Fatalf("writer dsn = %s", d)
	}
	if r := dsn("/data/bothy.db", true); !strings.Contains(r, "query_only%281%29") || strings.Contains(r, "_txlock") {
		t.Fatalf("reader dsn = %s", r)
	}
}

func TestOpenEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), Options{}); err == nil {
		t.Fatal("empty path accepted")
	}
}
