package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// 0009 adds state with defaults that keep today's behaviour: nobody has
// signed in yet, approval requests still reach chat apps, runs were not
// refined and deploys name no commit.
func TestUIStateMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	ctx := context.Background()
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 8), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	u := core.UserID(core.NewID(core.PrefixUser))
	pid := string(core.UserPrincipal(u))
	sid := core.NewID(core.PrefixSession)
	rid := core.NewID(core.PrefixRun)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id, username, role, pw_hash, created_at, updated_at) VALUES (?, 'owner', 'owner', 'h', 1, 1)`, []any{string(u)}},
		{`INSERT INTO principals(id, kind, user_id, workspace_id, tool_profile, created_at) VALUES (?, 'user', ?, 'ws_x', 'standard', 1)`,
			[]any{pid, string(u)}},
		{`INSERT INTO sessions(id, principal_id, origin, created_at, updated_at) VALUES (?, ?, 'web', 1, 1)`, []any{sid, pid}},
		{`INSERT INTO runs(id, session_id, principal_id, user_id, trigger, status, limits, created_at) VALUES (?, ?, ?, ?, 'message', 'done', '{}', 1)`,
			[]any{rid, sid, pid, string(u)}},
		{`INSERT INTO skills(name, description, path, hash, status, installed_at, updated_at) VALUES ('pdf', 'PDFs', 'skills/pdf', 'h', 'enabled', 1, 1)`, nil},
		{`INSERT INTO release_deploys(id, environment, repo, workflow, ref, principal_id, session_id, requested_by, status, created_at, updated_at)
			VALUES ('dep_01m4wa97e4s0p3qgcng95bcyhf', 'staging', 'acme/api', 'deploy.yml', 'main', ?, ?, ?, 'running', 1, 1)`, []any{pid, sid, string(u)}},
	} {
		if _, err := old.w.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	old.Close()

	db, err := Open(ctx, Options{Path: p, BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	usr, err := db.Users().Get(ctx, u)
	if err != nil || !usr.LastLoginAt.IsZero() || usr.PreferredChat != "" || usr.ApprovalsWebOnly {
		t.Fatalf("user after migration = %+v %v", usr, err)
	}
	run, err := db.Runs().Get(ctx, core.SystemViewer(), core.RunID(rid))
	if err != nil || run.Refined {
		t.Fatalf("run after migration = %+v %v", run, err)
	}
	d, err := db.Releases().Get(ctx, core.SystemViewer(), "dep_01m4wa97e4s0p3qgcng95bcyhf")
	if err != nil || d.SHA != "" || d.Subject != "" {
		t.Fatalf("deploy after migration = %+v %v", d, err)
	}
	if files, err := db.Skills().Approved(ctx, "pdf"); err != nil || len(files) != 0 {
		t.Fatalf("approved files after migration = %+v %v", files, err)
	}
	if err := db.Skills().SetApproved(ctx, "pdf", []core.SkillFile{{Path: "SKILL.md", SHA256: "x", Size: 1, Text: true, Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Skills().Delete(ctx, "pdf"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM skill_approved_files`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("approved files outlived their skill (%d, %v)", n, err)
	}
}
