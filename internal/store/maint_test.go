package store

import (
	"archive/tar"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func openAt(t *testing.T, dir string, clock core.Clock) *DB {
	t.Helper()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "bothy.db"), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRefusesOldSQLite(t *testing.T) {
	dir := t.TempDir()
	old := func(context.Context, *sql.DB) (string, error) { return "3.53.3", nil }
	_, err := open(context.Background(), Options{Path: filepath.Join(dir, "bothy.db")}, migrationsFS, old)
	if err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("open with SQLite 3.53.3 = %v", err)
	}
	ok := func(context.Context, *sql.DB) (string, error) { return "3.60.0", nil }
	db, err := open(context.Background(), Options{Path: filepath.Join(dir, "bothy.db")}, migrationsFS, ok)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestBackup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	clock := fixedClock{time.Date(2026, 10, 12, 9, 30, 15, 0, time.UTC)}
	db := openAt(t, dir, clock)
	ctx := context.Background()
	if err := db.Meta().Set(ctx, "marker", "in-the-snapshot"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "memory", "usr_a", "MEMORY.md"), "likes tea")
	writeFile(t, filepath.Join(dir, "memory", "spc_b", "USER.md"), "group")
	writeFile(t, filepath.Join(dir, "knowledge", "usr_a", "acme", "api", "NOTES.md"), "# acme/api")
	writeFile(t, filepath.Join(dir, "SOUL.md"), "persona")
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "log: {level: info}")
	writeFile(t, filepath.Join(dir, "bothy.ui.yaml"), "{}")
	writeFile(t, filepath.Join(dir, "keys", "kek"), "deadbeef")
	writeFile(t, filepath.Join(dir, "work", "ws_a", "notes.txt"), "workspace")
	writeFile(t, filepath.Join(dir, "setup-token"), "token")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "must not be copied")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(dir, "memory", "usr_a", "link.md")); err != nil {
			t.Fatal(err)
		}
	}

	backups := filepath.Join(dir, "backups")
	p, err := db.Backup(ctx, backups)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "bothy-20261012T093015.tar" {
		t.Errorf("backup name = %s", filepath.Base(p))
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("backup mode = %v, %v", fi.Mode().Perm(), err)
		}
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := map[string]string{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = string(b)
		if h.Mode != 0o600 {
			t.Errorf("%s mode %o", h.Name, h.Mode)
		}
	}
	for name, want := range map[string]string{
		"memory/usr_a/MEMORY.md": "likes tea", "memory/spc_b/USER.md": "group", "SOUL.md": "persona",
		"bothy.yaml": "log: {level: info}", "bothy.ui.yaml": "{}", "knowledge/usr_a/acme/api/NOTES.md": "# acme/api",
	} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
	for name := range got {
		if strings.HasPrefix(name, "keys/") || strings.HasPrefix(name, "work/") || strings.Contains(name, "link.md") ||
			name == "setup-token" || strings.HasPrefix(name, "backups/") {
			t.Errorf("backup contains %s", name)
		}
	}
	snap := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(snap, []byte(got["bothy.db"]), 0o600); err != nil {
		t.Fatal(err)
	}
	rdb, err := Open(ctx, Options{Path: snap})
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if v, err := rdb.Meta().Get(ctx, "marker"); err != nil || v != "in-the-snapshot" {
		t.Errorf("snapshot marker = %q, %v", v, err)
	}
	if _, err := db.Backup(ctx, backups); !errors.Is(err, core.ErrConflict) {
		t.Errorf("second backup in the same second = %v", err)
	}
	entries, _ := os.ReadDir(backups)
	if len(entries) != 1 {
		t.Errorf("backups dir holds %d entries (temp files left?)", len(entries))
	}
}

func TestRetention(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	db := openAt(t, t.TempDir(), fixedClock{now})
	w := &world{t: t, ctx: context.Background(), db: db}
	ctx := w.ctx
	u := w.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	day := 24 * time.Hour

	w.must(db.Egress().Add(ctx, []core.EgressRecord{
		{At: now.Add(-31 * day), Dialer: "box", Host: "old.example", Port: 443, Decision: "allow"},
		{At: now.Add(-day), Dialer: "box", Host: "new.example", Port: 443, Decision: "allow"},
	}))
	for _, x := range []struct {
		id string
		at time.Time
	}{{"old", now.Add(-2 * day)}, {"new", now.Add(-time.Hour)}} {
		if _, err := db.Dedup().Seen(ctx, "telegram", x.id, x.at); err != nil {
			t.Fatal(err)
		}
	}
	w.must(db.AuthSessions().Create(ctx, &core.AuthSession{IDHash: []byte("expired"), User: u.ID, Expires: now.Add(-time.Minute), IdleExpires: now.Add(time.Hour)}))
	w.must(db.AuthSessions().Create(ctx, &core.AuthSession{IDHash: []byte("live"), User: u.ID, Expires: now.Add(time.Hour), IdleExpires: now.Add(time.Hour)}))
	w.must(db.Pairings().Create(ctx, &core.Pairing{CodeHash: []byte("used-old"), Purpose: core.PairLinkIdentity, ExpiresAt: now.Add(day), UsedAt: now.Add(-8 * day)}))
	w.must(db.Pairings().Create(ctx, &core.Pairing{CodeHash: []byte("expired-old"), Purpose: core.PairLinkIdentity, ExpiresAt: now.Add(-8 * day)}))
	w.must(db.Pairings().Create(ctx, &core.Pairing{CodeHash: []byte("fresh"), Purpose: core.PairLinkIdentity, ExpiresAt: now.Add(-day)}))

	s := w.session(pu)
	oldDone := &core.Run{Session: s.ID, Principal: pu, User: u.ID, Trigger: core.TrigMessage, Status: core.RunDone, CreatedAt: now.Add(-40 * day)}
	oldParked := &core.Run{Session: s.ID, Principal: pu, User: u.ID, Trigger: core.TrigSchedule, Status: core.RunParked, CreatedAt: now.Add(-40 * day)}
	newDone := &core.Run{Session: s.ID, Principal: pu, User: u.ID, Trigger: core.TrigMessage, Status: core.RunDone, CreatedAt: now.Add(-day)}
	for _, r := range []*core.Run{oldDone, oldParked, newDone} {
		w.must(db.Runs().Create(ctx, r))
	}
	w.must(db.Runs().AddToolCall(ctx, &core.ToolCallRecord{Run: oldDone.ID, Session: s.ID, Principal: pu, Tool: "t", Source: "builtin", SideEffect: core.SERead, Status: core.TCOK}))

	hiddenOld := w.session(pu, func(x *core.Session) {
		x.Origin, x.Hidden, x.CreatedAt, x.UpdatedAt, x.LastMessageAt = "api", true, now.Add(-10*day), now.Add(-10*day), now.Add(-9*day)
	})
	hiddenNew := w.session(pu, func(x *core.Session) {
		x.Origin, x.Hidden, x.CreatedAt, x.UpdatedAt, x.LastMessageAt = "api", true, now.Add(-10*day), now.Add(-10*day), now.Add(-time.Hour)
	})
	w.message(hiddenOld.ID, core.ProvUser, "old api chat")
	w.message(hiddenNew.ID, core.ProvUser, "new api chat")
	w.message(s.ID, core.ProvUser, "normal chat")
	audit := &core.AuditEntry{At: now.Add(-400 * day), Actor: "system", Action: "boot", Outcome: "ok"}
	if _, err := db.Audit().Append(ctx, audit, make([]byte, 32), make([]byte, 32)); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Storage.Retention.Runs = config.Duration(30 * day)
	if err := db.retain(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	eg, _ := db.Egress().Query(ctx, core.SystemViewer(), "", core.Page{})
	if len(eg) != 1 || eg[0].Host != "new.example" {
		t.Errorf("egress after retention = %+v", eg)
	}
	if seen, _ := db.Dedup().Seen(ctx, "telegram", "old", now); seen {
		t.Error("old dedup row kept")
	}
	if seen, _ := db.Dedup().Seen(ctx, "telegram", "new", now); !seen {
		t.Error("recent dedup row pruned")
	}
	if _, err := db.AuthSessions().Get(ctx, []byte("expired")); !errors.Is(err, core.ErrNotFound) {
		t.Error("expired auth session kept")
	}
	if _, err := db.AuthSessions().Get(ctx, []byte("live")); err != nil {
		t.Error("live auth session pruned")
	}
	var codes int
	if err := db.r.QueryRow(`SELECT count(*) FROM pairing_codes`).Scan(&codes); err != nil || codes != 1 {
		t.Errorf("pairing codes left = %d, %v; want only the recent one", codes, err)
	}
	for r, want := range map[*core.Run]bool{oldDone: false, oldParked: true, newDone: true} {
		_, err := db.Runs().Get(ctx, core.SystemViewer(), r.ID)
		if (err == nil) != want {
			t.Errorf("run %s (%s) kept=%t, want %t", r.ID, r.Status, err == nil, want)
		}
	}
	var tcs int
	if err := db.r.QueryRow(`SELECT count(*) FROM tool_calls`).Scan(&tcs); err != nil || tcs != 0 {
		t.Errorf("tool calls of the pruned run = %d (cascade), %v", tcs, err)
	}
	if got, _ := db.Sessions().Get(ctx, core.SystemViewer(), hiddenOld.ID); got == nil || !got.Archived {
		t.Error("old hidden API session not archived")
	}
	if ms, _ := db.Messages().Range(ctx, core.SystemViewer(), hiddenOld.ID, 1, 0); len(ms) != 0 {
		t.Error("old hidden API session kept its messages")
	}
	if ms, _ := db.Messages().Range(ctx, core.SystemViewer(), hiddenNew.ID, 1, 0); len(ms) != 1 {
		t.Error("recent hidden API session lost its messages")
	}
	if ms, _ := db.Messages().Range(ctx, core.SystemViewer(), s.ID, 1, 0); len(ms) != 1 {
		t.Error("a normal session lost its messages")
	}
	if id, _, _ := db.Audit().Head(ctx); id != audit.ID {
		t.Error("retention touched audit_log")
	}

	cfg.Storage.Retention.Runs = 0
	if err := db.retain(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Runs().Get(ctx, core.SystemViewer(), newDone.ID); err != nil {
		t.Error("runs retention 0 must keep runs")
	}
}

func TestRetentionDropsEmptyWebSessions(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	db := openAt(t, t.TempDir(), fixedClock{now})
	w := &world{t: t, ctx: context.Background(), db: db}
	ctx := w.ctx
	u := w.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	old := now.Add(-2 * 24 * time.Hour)
	empty := func(mod ...func(*core.Session)) *core.Session {
		return w.session(pu, append([]func(*core.Session){func(x *core.Session) {
			x.CreatedAt, x.UpdatedAt, x.LastMessageAt = old, old, time.Time{}
		}}, mod...)...)
	}
	gone := empty()
	goneArchived := empty(func(x *core.Session) { x.Archived = true })
	fresh := w.session(pu, func(x *core.Session) {
		x.CreatedAt, x.UpdatedAt, x.LastMessageAt = now.Add(-time.Hour), now, time.Time{}
	})
	withNotice := empty()
	w.message(withNotice.ID, core.ProvSystem, "Clean fork of “x”.")
	withRun := empty()
	w.run(withRun, core.RunQueued)
	withPin := empty()
	w.must(db.Pins().Add(ctx, &core.Pin{Session: withPin.ID, Text: "keep", Source: "user", CreatedAt: old}))
	scheduled := empty()
	w.must(db.Schedules().Upsert(ctx, &core.Schedule{Principal: pu, Name: "s", Kind: core.SchedCron, Spec: "0 9 * * *", TZ: "UTC",
		Prompt: "p", Session: scheduled.ID, Slot: core.SlotMain, MissedRun: "skip", Enabled: true, CreatedBy: "user:" + string(u.ID),
		CreatedAt: old, UpdatedAt: old}))
	channel := empty(func(x *core.Session) { x.Origin = "telegram" })
	w.must(db.Meta().Set(ctx, core.RefineMetaKey(gone.ID), core.RefineOn))
	w.must(db.Meta().Set(ctx, core.RefineMetaKey(fresh.ID), core.RefineOn))

	if err := db.retain(ctx, config.Default()); err != nil {
		t.Fatal(err)
	}
	for s, keep := range map[*core.Session]bool{gone: false, goneArchived: false, fresh: true, withNotice: true, withRun: true,
		withPin: true, scheduled: true, channel: true} {
		_, err := db.Sessions().Get(ctx, core.SystemViewer(), s.ID)
		if (err == nil) != keep {
			t.Errorf("session %s kept=%t, want %t (err %v)", s.ID, err == nil, keep, err)
		}
	}
	if _, err := db.Meta().Get(ctx, core.RefineMetaKey(gone.ID)); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("refine setting of a pruned session: %v", err)
	}
	if _, err := db.Meta().Get(ctx, core.RefineMetaKey(fresh.ID)); err != nil {
		t.Errorf("refine setting of a kept session: %v", err)
	}
}

func TestRunRetentionStops(t *testing.T) {
	db, _ := openTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- db.RunRetention(ctx, config.Static(config.Default())) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunRetention did not stop")
	}
}

func findings(t *testing.T, db *DB) map[string]core.Finding {
	t.Helper()
	out := map[string]core.Finding{}
	for _, c := range db.Checks() {
		for _, f := range c.Run(context.Background()) {
			if f.ID != c.ID() {
				t.Errorf("check %s produced %s", c.ID(), f.ID)
			}
			out[f.ID] = f
		}
	}
	return out
}

func TestChecks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	db := openAt(t, dir, fixedClock{now})
	fs := findings(t, db)
	if _, ok := fs["sqlite.version"]; ok {
		t.Error("sqlite.version fired on a supported SQLite")
	}
	// The distroless image has no bothy on PATH: the container form names /bothy.
	if f, ok := fs["backups.none_recent"]; !ok || f.Severity != core.SevLow || !strings.Contains(f.Fix, "docker compose exec bothy /bothy backup") {
		t.Errorf("backups.none_recent = %+v", f)
	}
	if runtime.GOOS != "windows" {
		if _, ok := fs["data.permissions"]; ok {
			t.Errorf("data.permissions fired on a fresh data dir: %+v", fs["data.permissions"])
		}
		writeFile(t, filepath.Join(dir, "SOUL.md"), "x")
		writeFile(t, filepath.Join(dir, "work", "ws_a", "f"), "shared on purpose")
		if err := os.Chmod(filepath.Join(dir, "SOUL.md"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, "memory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, "work", "ws_a", "f"), 0o660); err != nil {
			t.Fatal(err)
		}
		f, ok := findings(t, db)["data.permissions"]
		if !ok || f.Severity != core.SevHigh || !f.Security || !f.AutoFix || !strings.Contains(f.Detail, "SOUL.md") || strings.Contains(f.Detail, "ws_a") ||
			!strings.Contains(f.Fix, "docker compose exec bothy /bothy security-audit --fix") {
			t.Fatalf("data.permissions = %+v", f)
		}
		for _, c := range db.Checks() {
			if c.ID() == "data.permissions" {
				if err := c.Fix(context.Background(), f); err != nil {
					t.Fatal(err)
				}
			}
		}
		if fi, _ := os.Stat(filepath.Join(dir, "SOUL.md")); fi.Mode().Perm() != 0o600 {
			t.Errorf("fix left SOUL.md %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Join(dir, "memory")); fi.Mode().Perm() != 0o700 {
			t.Errorf("fix left memory/ %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Join(dir, "work", "ws_a", "f")); fi.Mode().Perm() != 0o660 {
			t.Errorf("fix touched the workspace: %v", fi.Mode().Perm())
		}
		if _, ok := findings(t, db)["data.permissions"]; ok {
			t.Error("data.permissions still fires after the fix")
		}
	}
	if _, err := db.Backup(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	if _, ok := findings(t, db)["backups.none_recent"]; ok {
		t.Error("backups.none_recent fired right after a backup")
	}
	for _, c := range db.Checks() {
		if c.ID() != "data.permissions" {
			if err := c.Fix(context.Background(), core.Finding{ID: c.ID()}); !errors.Is(err, core.ErrNotImplemented) {
				t.Errorf("%s Fix = %v", c.ID(), err)
			}
		}
	}
	db.version = "3.40.0"
	if f, ok := findings(t, db)["sqlite.version"]; !ok || f.Severity != core.SevCritical {
		t.Errorf("sqlite.version = %+v", f)
	}
}

func TestPermissionsSkipConfiguredWorkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := filepath.Join(t.TempDir(), "data")
	work := filepath.Join(dir, "spaces")
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "bothy.db"), WorkDir: work, Clock: fixedClock{time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	writeFile(t, filepath.Join(work, "ws_a", "f"), "shared on purpose")
	writeFile(t, filepath.Join(dir, "work", "stray"), "not the work dir")
	for _, p := range []string{filepath.Join(work, "ws_a", "f"), filepath.Join(dir, "work", "stray")} {
		if err := os.Chmod(p, 0o660); err != nil {
			t.Fatal(err)
		}
	}
	f, ok := findings(t, db)["data.permissions"]
	if !ok || strings.Contains(f.Detail, "ws_a") || !strings.Contains(f.Detail, "stray") {
		t.Fatalf("data.permissions = %+v", f)
	}
}
