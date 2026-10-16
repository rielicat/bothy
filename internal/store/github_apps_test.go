package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// 0012 adds github_apps next to existing connections: they survive, an app
// can point at one, and deleting the connection leaves the app without it.
func TestGitHubAppsMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	ctx := context.Background()
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 11), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	conn := core.NewID(core.PrefixGitHubConn)
	if _, err := old.w.ExecContext(ctx, `INSERT INTO github_connections(id, auth, app_id, installation_id, secret_ref, owns_secret, created_at, updated_at)
		VALUES (?, 'app', '7', 42, 'secret:app', 1, 1, 2)`, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := old.w.ExecContext(ctx, `INSERT INTO repos(owner, name, connection_id, allowed, created_at, updated_at) VALUES ('acme', 'web', ?, 1, 3, 4)`, conn); err != nil {
		t.Fatal(err)
	}
	old.Close()

	db, err := Open(ctx, Options{Path: p, BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs := db.GitHub()
	if c, err := gs.Connection(ctx, conn); err != nil || c.InstallationID != 42 || !c.OwnsSecret {
		t.Fatalf("connection after migration = %+v %v", c, err)
	}
	a := &core.GitHubOwnApp{ID: core.NewID(core.PrefixGitHubApp), AppID: 7, Slug: "bothy-acme", Name: "Bothy (acme)", Owner: "acme",
		OwnerType: "Organization", URL: "https://github.com/apps/bothy-acme", KeyRef: "secret:app", Connection: conn}
	if err := gs.PutApp(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := gs.DeleteConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	got, err := gs.App(ctx, a.ID)
	if err != nil || got.Connection != "" {
		t.Fatalf("app after its connection went = %+v %v", got, err)
	}
	var problems int
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&problems); err != nil || problems != 0 {
		t.Fatalf("foreign key check: %d %v", problems, err)
	}
}
