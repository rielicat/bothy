package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// 0008 rebuilds github_connections so secret_ref may name an AWS Secrets
// Manager secret. The rebuild must keep every connection and repository and
// the cascade between them.
func TestExternalSecretRefMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	ctx := context.Background()
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 7), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	a, b := core.NewID(core.PrefixGitHubConn), core.NewID(core.PrefixGitHubConn)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO github_connections(id, auth, account, secret_ref, owns_secret, created_at, updated_at) VALUES (?, 'pat', 'octocat', 'secret:gh', 1, 1, 2)`, []any{a}},
		{`INSERT INTO github_connections(id, auth, app_id, installation_id, secret_ref, created_at, updated_at) VALUES (?, 'app', '7', 42, 'secret:app', 3, 4)`, []any{b}},
		{`INSERT INTO repos(owner, name, connection_id, github_id, allowed, created_at, updated_at) VALUES ('Acme', 'API', ?, 9, 1, 5, 6)`, []any{a}},
		{`INSERT INTO repos(owner, name, connection_id, created_at, updated_at) VALUES ('acme', 'web', ?, 7, 8)`, []any{b}},
	} {
		if _, err := old.w.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if _, err := old.w.ExecContext(ctx, `UPDATE github_connections SET secret_ref = 'aws-sm:x' WHERE id = ?`, a); err == nil {
		t.Fatal("before 0008 an aws-sm secret_ref passes the CHECK")
	}
	old.Close()

	db, err := Open(ctx, Options{Path: p, BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := db.GitHub()
	conns, err := gs.Connections(ctx)
	if err != nil || len(conns) != 2 || conns[0].ID != a || !conns[0].OwnsSecret || conns[0].Account != "octocat" || conns[1].InstallationID != 42 {
		t.Fatalf("connections after migration = %+v %v", conns, err)
	}
	if g, err := gs.Repo(ctx, "acme", "api"); err != nil || g.Connection != a || g.GitHubID != 9 || !g.Allowed {
		t.Fatalf("repo after migration = %+v %v", g, err)
	}

	c := conns[1]
	c.SecretRef = "aws-sm:prod/github#app_key"
	if err := gs.PutConnection(ctx, c); err != nil {
		t.Fatalf("aws-sm secret_ref after 0008: %v", err)
	}
	c.OwnsSecret = true
	if err := gs.PutConnection(ctx, c); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Bothy never owns an AWS secret: %v", err)
	}
	c.OwnsSecret, c.SecretRef = false, "ghp_plaintext"
	if err := gs.PutConnection(ctx, c); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("plaintext secret_ref: %v", err)
	}

	var problems, idx int
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&problems); err != nil || problems != 0 {
		t.Fatalf("foreign key check: %d %v", problems, err)
	}
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = 'repos_connection'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("repos_connection index: %d %v", idx, err)
	}
	if err := gs.DeleteConnection(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := gs.Repo(ctx, "acme", "web"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleting a connection still deletes its repositories: %v", err)
	}
}
