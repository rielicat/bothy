package store

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rielicat/bothy/internal/core"
)

// migrationsUpTo is the embedded migrations through version n.
func migrationsUpTo(t *testing.T, n int) fstest.MapFS {
	t.Helper()
	ms, err := loadMigrations(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, m := range ms[:n] {
		out["migrations/"+m.name] = &fstest.MapFile{Data: []byte(m.sql)}
	}
	return out
}

// 0005 moves index state to the principal: a ready index keeps the commit it
// read as indexed_sha, the shared repos.last_indexed_sha goes, and deploys
// recorded before it follow through today's dispatcher choice ("").
func TestIndexStateMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	ctx := context.Background()
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 4), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	u := &core.User{Username: "owner", Role: core.RoleOwner, PWHash: "h", Timezone: "UTC", CreatedAt: t0, UpdatedAt: t0}
	if err := old.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	pid := string(core.UserPrincipal(u.ID))
	conn := core.NewID(core.PrefixGitHubConn)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO github_connections(id, auth, secret_ref, created_at, updated_at) VALUES (?, 'pat', 'secret:gh', 1, 1)`, []any{conn}},
		{`INSERT INTO repos(owner, name, connection_id, allowed, last_indexed_sha, created_at, updated_at) VALUES ('acme', 'api', ?, 1, 'abc', 1, 1)`, []any{conn}},
		{`INSERT INTO knowledge_repos(principal_id, repo, source, dir, sha, status, created_at, updated_at)
			VALUES (?, 'acme/api', 'github', 'repos/acme/api', 'abc', 'ready', 1, 1), (?, 'acme/web', 'github', 'repos/acme/web', 'def', 'queued', 1, 1)`, []any{pid, pid}},
		{`INSERT INTO release_deploys(id, environment, repo, workflow, ref, principal_id, session_id, requested_by, status, created_at, updated_at)
			VALUES ('dep_01m4wa97e4s0p3qgcng95bcyhf', 'staging', 'acme/api', 'deploy.yml', 'main', ?, 'ses_x', ?, 'running', 1, 1)`, []any{pid, string(u.ID)}},
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
	repos, err := db.Knowledge().List(ctx, core.SystemViewer(), "")
	if err != nil || len(repos) != 2 {
		t.Fatalf("knowledge repos = %+v %v", repos, err)
	}
	if repos[0].Repo != "acme/api" || repos[0].IndexedSHA != "abc" || repos[1].IndexedSHA != "" || repos[1].SHA != "def" {
		t.Fatalf("indexed sha after migration: %+v / %+v", repos[0], repos[1])
	}
	var cols int
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('repos') WHERE name = 'last_indexed_sha'`).Scan(&cols); err != nil || cols != 0 {
		t.Fatalf("repos.last_indexed_sha still there (%d, %v)", cols, err)
	}
	if g, err := db.GitHub().Repo(ctx, "ACME", "api"); err != nil || g.Connection != conn || !g.Allowed || g.LastIndexedSHA != "" {
		t.Fatalf("repo after migration = %+v %v", g, err)
	}
	d, err := db.Releases().Get(ctx, core.SystemViewer(), "dep_01m4wa97e4s0p3qgcng95bcyhf")
	if err != nil || d.Via != "" || d.Status != core.DeployRunning {
		t.Fatalf("deploy after migration = %+v %v", d, err)
	}
	if _, err := db.w.ExecContext(ctx, `UPDATE release_deploys SET via = 'elsewhere'`); err == nil {
		t.Fatal("release_deploys.via accepted an unknown dispatcher")
	}
}
