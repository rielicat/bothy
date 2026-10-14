package store

import (
	"context"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// GitHub returns the store of migration 0002 (GitHub connections and
// repositories). It is not part of core.Store; cmd/bothy hands it to the
// github module.
func (h *handle) GitHub() core.GitHubStore { return githubRepo{h} }

var _ core.GitHubStore = githubRepo{}

type githubRepo struct{ h *handle }

const githubConnCols = `id, auth, account, account_type, app_id, app_slug, installation_id, secret_ref, owns_secret,
	status, error, token_expires_at, checked_at, created_by, created_at, updated_at`

func scanGitHubConn(sc scanner) (*core.GitHubConnection, error) {
	var c core.GitHubConnection
	var owns int
	var exp, checked, created, updated int64
	if err := sc.Scan(&c.ID, &c.Auth, &c.Account, &c.AccountType, &c.AppID, &c.AppSlug, &c.InstallationID, &c.SecretRef,
		&owns, &c.Status, &c.Error, &exp, &checked, &c.CreatedBy, &created, &updated); err != nil {
		return nil, err
	}
	c.OwnsSecret = owns == 1
	c.TokenExpires, c.CheckedAt, c.CreatedAt, c.UpdatedAt = fromMS(exp), fromMS(checked), fromMS(created), fromMS(updated)
	return &c, nil
}

func (r githubRepo) PutConnection(ctx context.Context, c *core.GitHubConnection) error {
	if !core.ValidID(core.PrefixGitHubConn, c.ID) {
		return invalid("github_connections.id")
	}
	now := r.h.now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	status := c.Status
	if status == "" {
		status = "ok"
	}
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO github_connections(`+githubConnCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET auth = excluded.auth, account = excluded.account, account_type = excluded.account_type,
		app_id = excluded.app_id, app_slug = excluded.app_slug, installation_id = excluded.installation_id,
		secret_ref = excluded.secret_ref, owns_secret = excluded.owns_secret, status = excluded.status, error = excluded.error,
		token_expires_at = excluded.token_expires_at, checked_at = excluded.checked_at, updated_at = excluded.updated_at`,
		c.ID, string(c.Auth), c.Account, c.AccountType, c.AppID, c.AppSlug, c.InstallationID, c.SecretRef, b2i(c.OwnsSecret),
		status, c.Error, ms(c.TokenExpires), ms(c.CheckedAt), string(c.CreatedBy), ms(c.CreatedAt), ms(c.UpdatedAt))
	if err != nil {
		return dbErr("put github connection", err)
	}
	c.Status = status
	return nil
}

func (r githubRepo) Connection(ctx context.Context, id string) (*core.GitHubConnection, error) {
	return queryOne(ctx, r.h.rq, "github connection "+id, scanGitHubConn,
		`SELECT `+githubConnCols+` FROM github_connections WHERE id = ?`, id)
}

func (r githubRepo) Connections(ctx context.Context) ([]*core.GitHubConnection, error) {
	return queryAll(ctx, r.h.rq, "list github connections", scanGitHubConn,
		`SELECT `+githubConnCols+` FROM github_connections ORDER BY created_at, id`)
}

func (r githubRepo) DeleteConnection(ctx context.Context, id string) error {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM github_connections WHERE id = ?`, id)
	return mustAffect("delete github connection "+id, res, err)
}

const repoCols = `owner, name, connection_id, github_id, default_branch, private, allowed, last_indexed_sha, created_at, updated_at`

func scanRepo(sc scanner) (*core.GitHubRepo, error) {
	var g core.GitHubRepo
	var private, allowed int
	var created, updated int64
	if err := sc.Scan(&g.Owner, &g.Name, &g.Connection, &g.GitHubID, &g.DefaultBranch, &private, &allowed, &g.LastIndexedSHA,
		&created, &updated); err != nil {
		return nil, err
	}
	g.Private, g.Allowed = private == 1, allowed == 1
	g.CreatedAt, g.UpdatedAt = fromMS(created), fromMS(updated)
	return &g, nil
}

// PutRepo keeps created_at and last_indexed_sha of an existing row; the
// conditional update leaves a repository another connection allows
// untouched, which RowsAffected reports as a conflict. A row its connection
// no longer allows moves to the new one.
func (r githubRepo) PutRepo(ctx context.Context, g *core.GitHubRepo) error {
	if strings.TrimSpace(g.Owner) == "" || strings.TrimSpace(g.Name) == "" || g.Connection == "" {
		return invalid("repos.owner/name/connection_id")
	}
	now := r.h.now()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	res, err := r.h.wq.ExecContext(ctx, `INSERT INTO repos(`+repoCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(owner, name) DO UPDATE SET owner = excluded.owner, name = excluded.name, connection_id = excluded.connection_id,
		github_id = excluded.github_id, default_branch = excluded.default_branch, private = excluded.private,
		allowed = excluded.allowed, updated_at = excluded.updated_at
		WHERE repos.connection_id = excluded.connection_id OR repos.allowed = 0`,
		g.Owner, g.Name, g.Connection, g.GitHubID, g.DefaultBranch, b2i(g.Private), b2i(g.Allowed), g.LastIndexedSHA,
		ms(g.CreatedAt), ms(g.UpdatedAt))
	if err != nil {
		return dbErr("put repo", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return dbErr("put repo", err)
	}
	if n == 0 {
		return conflict("repo " + g.FullName() + " belongs to another connection")
	}
	return nil
}

func (r githubRepo) Repo(ctx context.Context, owner, name string) (*core.GitHubRepo, error) {
	return queryOne(ctx, r.h.rq, "repo "+owner+"/"+name, scanRepo,
		`SELECT `+repoCols+` FROM repos WHERE owner = ? AND name = ?`, owner, name)
}

func (r githubRepo) Repos(ctx context.Context, connection string) ([]*core.GitHubRepo, error) {
	if connection == "" {
		return queryAll(ctx, r.h.rq, "list repos", scanRepo, `SELECT `+repoCols+` FROM repos ORDER BY owner, name`)
	}
	return queryAll(ctx, r.h.rq, "list repos", scanRepo,
		`SELECT `+repoCols+` FROM repos WHERE connection_id = ? ORDER BY owner, name`, connection)
}

func (r githubRepo) SetIndexed(ctx context.Context, owner, name, sha string) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE repos SET last_indexed_sha = ?, updated_at = ? WHERE owner = ? AND name = ?`,
		sha, ms(r.h.now()), owner, name)
	return mustAffect("set indexed "+owner+"/"+name, res, err)
}
