package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// GitHubStore runs the core.GitHubStore contract (migration 0002) against a
// fresh store per subtest. memstore.NewGitHub and the SQLite store run it.
//
// Pinned behaviour beyond the interface comments:
//   - PutConnection with an id that is not ghc_<26> => ErrInvalid; it stamps
//     CreatedAt (when zero) and UpdatedAt, and an empty Status becomes "ok".
//   - An App connection needs AppID and InstallationID > 0, and SecretRef
//     must be "secret:<name>" or "aws-sm:<secret>" (CHECKs, migration 0008);
//     only a "secret:" one may be OwnsSecret => ErrInvalid.
//   - PutRepo keeps CreatedAt of an existing row, takes GitHub's new
//     spelling of owner/name, and with an unknown connection => ErrInvalid
//     (foreign key). LastIndexedSHA is never stored (index state is per
//     principal), so reads return it empty.
//   - Repos sort by owner, then name, case-insensitively.
//   - Times are truncated to milliseconds.
func GitHubStore(t *testing.T, open func(t *testing.T) core.GitHubStore) {
	t.Helper()
	for _, c := range githubSuite {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, t.Context(), open(t))
		})
	}
}

var githubSuite = []struct {
	name string
	run  func(t *testing.T, ctx context.Context, s core.GitHubStore)
}{
	{"connections round trip", ghConnRoundTrip},
	{"connection constraints", ghConnConstraints},
	{"repos upsert and conflicts", ghRepos},
	{"delete cascades", ghDeleteCascade},
	{"index state not stored", ghIndexStateNotStored},
}

func ghConn(auth core.GitHubAuth) *core.GitHubConnection {
	c := &core.GitHubConnection{ID: core.NewID(core.PrefixGitHubConn), Auth: auth, Account: "octocat", AccountType: "User",
		SecretRef: "secret:github_pat", CreatedBy: "usr_00000000000000000000000000"}
	if auth == core.GitHubApp {
		c.AppID, c.AppSlug, c.InstallationID, c.SecretRef = "1", "octoapp", 42, "secret:github_app_key"
	}
	return c
}

func ghPutConn(t *testing.T, ctx context.Context, s core.GitHubStore, c *core.GitHubConnection) {
	t.Helper()
	if err := s.PutConnection(ctx, c); err != nil {
		t.Fatalf("PutConnection(%s): %v", c.ID, err)
	}
}

func ghConnRoundTrip(t *testing.T, ctx context.Context, s core.GitHubStore) {
	pat := ghConn(core.GitHubPAT)
	pat.TokenExpires = time.Date(2027, 1, 2, 3, 4, 5, 999_999_999, time.UTC)
	pat.OwnsSecret = true
	ghPutConn(t, ctx, s, pat)
	if pat.CreatedAt.IsZero() || pat.UpdatedAt.IsZero() || pat.Status != "ok" {
		t.Errorf("PutConnection did not stamp times or default status: %+v", pat)
	}
	app := ghConn(core.GitHubApp)
	app.CreatedAt = pat.CreatedAt.Add(time.Second) // ids minted in one millisecond do not sort by creation
	app.Status, app.Error = "error", "bad credentials"
	ghPutConn(t, ctx, s, app)

	got, err := s.Connection(ctx, pat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account != "octocat" || got.Auth != core.GitHubPAT || !got.OwnsSecret || got.SecretRef != "secret:github_pat" ||
		!got.TokenExpires.Equal(pat.TokenExpires.Truncate(time.Millisecond)) || got.CreatedBy != pat.CreatedBy {
		t.Errorf("round trip = %+v", got)
	}
	got, err = s.Connection(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AppID != "1" || got.AppSlug != "octoapp" || got.InstallationID != 42 || got.Status != "error" || got.Error != "bad credentials" {
		t.Errorf("app round trip = %+v", got)
	}
	created := got.CreatedAt
	got.Status, got.Error, got.Account = "ok", "", "octo-org"
	ghPutConn(t, ctx, s, got)
	again, err := s.Connection(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "ok" || again.Account != "octo-org" || !again.CreatedAt.Equal(created) {
		t.Errorf("update = %+v", again)
	}
	list, err := s.Connections(ctx)
	if err != nil || len(list) != 2 || list[0].ID != pat.ID || list[1].ID != app.ID {
		t.Errorf("Connections = %v %v, want pat then app", ids(list), err)
	}
	if _, err := s.Connection(ctx, core.NewID(core.PrefixGitHubConn)); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing connection: %v", err)
	}
}

func ids(cs []*core.GitHubConnection) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func ghConnConstraints(t *testing.T, ctx context.Context, s core.GitHubStore) {
	bad := []func(c *core.GitHubConnection){
		func(c *core.GitHubConnection) { c.ID = "conn1" },
		func(c *core.GitHubConnection) { c.SecretRef = "ghp_plaintext" },
		func(c *core.GitHubConnection) { c.Auth = "oauth" },
		func(c *core.GitHubConnection) { c.Auth, c.AppID, c.InstallationID = core.GitHubApp, "", 42 },
		func(c *core.GitHubConnection) { c.Auth, c.AppID, c.InstallationID = core.GitHubApp, "1", 0 },
		func(c *core.GitHubConnection) { c.Status = "broken" },
		func(c *core.GitHubConnection) { c.SecretRef = "aws-sm:" },
		func(c *core.GitHubConnection) { c.SecretRef, c.OwnsSecret = "aws-sm:prod/github#token", true },
	}
	for i, mut := range bad {
		c := ghConn(core.GitHubPAT)
		mut(c)
		if err := s.PutConnection(ctx, c); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("case %d: PutConnection(%+v) = %v, want ErrInvalid", i, c, err)
		}
	}
	if err := s.DeleteConnection(ctx, core.NewID(core.PrefixGitHubConn)); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
	aws := ghConn(core.GitHubPAT)
	aws.SecretRef = "aws-sm:prod/github#token"
	ghPutConn(t, ctx, s, aws)
	if got, err := s.Connection(ctx, aws.ID); err != nil || got.SecretRef != aws.SecretRef || got.OwnsSecret {
		t.Errorf("aws-sm connection = %+v %v", got, err)
	}
}

func ghRepos(t *testing.T, ctx context.Context, s core.GitHubStore) {
	a, b := ghConn(core.GitHubPAT), ghConn(core.GitHubApp)
	ghPutConn(t, ctx, s, a)
	ghPutConn(t, ctx, s, b)
	put := func(r *core.GitHubRepo) error { return s.PutRepo(ctx, r) }
	hello := &core.GitHubRepo{Connection: a.ID, GitHubID: 1296269, Owner: "octocat", Name: "Hello-World", DefaultBranch: "main",
		Private: true, Allowed: true}
	if err := put(hello); err != nil {
		t.Fatal(err)
	}
	if err := put(&core.GitHubRepo{Connection: a.ID, Owner: "Zed", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := put(&core.GitHubRepo{Connection: b.ID, Owner: "acme", Name: "Widgets", Allowed: true}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Repo(ctx, "OCTOCAT", "hello-world")
	if err != nil {
		t.Fatalf("case-insensitive Repo: %v", err)
	}
	if got.Owner != "octocat" || got.Name != "Hello-World" || got.GitHubID != 1296269 || got.DefaultBranch != "main" ||
		!got.Private || !got.Allowed || got.Connection != a.ID || got.FullName() != "octocat/Hello-World" {
		t.Errorf("Repo = %+v", got)
	}
	created := got.CreatedAt

	// Another connection while a allows it: conflict, row untouched.
	if err := put(&core.GitHubRepo{Connection: b.ID, Owner: "octocat", Name: "HELLO-WORLD", Allowed: true}); !errors.Is(err, core.ErrConflict) {
		t.Errorf("repo another connection allows: %v, want ErrConflict", err)
	}
	if got, _ := s.Repo(ctx, "octocat", "hello-world"); got == nil || got.Connection != a.ID || got.DefaultBranch != "main" {
		t.Errorf("conflicting put changed the row: %+v", got)
	}

	// Same connection: updates, takes the new spelling, keeps created_at.
	if err := put(&core.GitHubRepo{Connection: a.ID, GitHubID: 1296269, Owner: "Octocat", Name: "hello-world", DefaultBranch: "trunk",
		Allowed: false}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Repo(ctx, "octocat", "Hello-World")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "Octocat" || got.Name != "hello-world" || got.DefaultBranch != "trunk" || got.Allowed || got.Private ||
		!got.CreatedAt.Equal(created) {
		t.Errorf("after update = %+v", got)
	}

	// a no longer allows it: another connection takes it over, keeping
	// created_at.
	if err := put(&core.GitHubRepo{Connection: b.ID, GitHubID: 1296269, Owner: "octocat", Name: "Hello-World", DefaultBranch: "main",
		Allowed: true}); err != nil {
		t.Fatalf("taking over a repo its connection no longer allows: %v", err)
	}
	got, err = s.Repo(ctx, "octocat", "hello-world")
	if err != nil || got.Connection != b.ID || !got.Allowed || got.DefaultBranch != "main" || !got.CreatedAt.Equal(created) {
		t.Errorf("after takeover = %+v %v", got, err)
	}
	if err := put(&core.GitHubRepo{Connection: a.ID, Owner: "octocat", Name: "hello-world"}); !errors.Is(err, core.ErrConflict) {
		t.Errorf("former connection after takeover: %v, want ErrConflict", err)
	}
	if err := put(&core.GitHubRepo{Connection: core.NewID(core.PrefixGitHubConn), Owner: "x", Name: "y"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("unknown connection: %v, want ErrInvalid", err)
	}
	if err := put(&core.GitHubRepo{Connection: a.ID, Owner: "", Name: "y"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("empty owner: %v, want ErrInvalid", err)
	}

	all, err := s.Repos(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if names := repoNames(all); len(names) != 3 || names[0] != "acme/Widgets" || names[1] != "octocat/Hello-World" || names[2] != "Zed/alpha" {
		t.Errorf("Repos(\"\") = %v, want case-insensitive owner/name order", names)
	}
	mine, err := s.Repos(ctx, a.ID)
	if err != nil || len(mine) != 1 {
		t.Errorf("Repos(a) = %v %v", repoNames(mine), err)
	}
	theirs, err := s.Repos(ctx, b.ID)
	if err != nil || len(theirs) != 2 {
		t.Errorf("Repos(b) = %v %v", repoNames(theirs), err)
	}
	if _, err := s.Repo(ctx, "nobody", "nothing"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing repo: %v", err)
	}
}

func repoNames(rs []*core.GitHubRepo) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.FullName()
	}
	return out
}

func ghDeleteCascade(t *testing.T, ctx context.Context, s core.GitHubStore) {
	a, b := ghConn(core.GitHubPAT), ghConn(core.GitHubPAT)
	ghPutConn(t, ctx, s, a)
	ghPutConn(t, ctx, s, b)
	for _, r := range []*core.GitHubRepo{{Connection: a.ID, Owner: "o", Name: "one"}, {Connection: b.ID, Owner: "o", Name: "two"}} {
		if err := s.PutRepo(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteConnection(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connection(ctx, a.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted connection: %v", err)
	}
	all, err := s.Repos(ctx, "")
	if err != nil || len(all) != 1 || all[0].Name != "two" {
		t.Errorf("repos after delete = %v %v", repoNames(all), err)
	}
	// The name is free again for another connection.
	if err := s.PutRepo(ctx, &core.GitHubRepo{Connection: b.ID, Owner: "o", Name: "one"}); err != nil {
		t.Errorf("reuse after delete: %v", err)
	}
}

// The repository row is shared by every user while each principal indexes
// on its own, so a caller's index state never lands in it.
func ghIndexStateNotStored(t *testing.T, ctx context.Context, s core.GitHubStore) {
	a := ghConn(core.GitHubPAT)
	ghPutConn(t, ctx, s, a)
	r := &core.GitHubRepo{Connection: a.ID, Owner: "octocat", Name: "Hello-World", Allowed: true,
		LastIndexedSHA: "7638417db6d59f3c431d3e1f261cc637155684cd"}
	if err := s.PutRepo(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Repo(ctx, "octocat", "Hello-World")
	if err != nil || got.LastIndexedSHA != "" {
		t.Errorf("Repo = %+v %v, want no indexed sha", got, err)
	}
	all, err := s.Repos(ctx, "")
	if err != nil || len(all) != 1 || all[0].LastIndexedSHA != "" {
		t.Errorf("Repos = %+v %v", all, err)
	}
}
