package github

import (
	"crypto/rsa"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/github/githubtest"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestTestPAT(t *testing.T) {
	f := newFx(t)
	f.gh.AddRepo("octo-org", "public-site", false, map[string]string{"index.html": "<h1>hi</h1>\n"})
	f.gh.TokenExpires = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	p, err := f.svc.Test(f.ctx, f.patCreds())
	if err != nil {
		t.Fatal(err)
	}
	if !p.OK || p.Error != "" || p.Account != "octocat" || p.AccountType != "User" || p.Auth != core.GitHubPAT ||
		!p.TokenExpires.Equal(f.gh.TokenExpires) || p.Truncated {
		t.Fatalf("probe = %+v", p)
	}
	if len(p.Repos) != 2 || p.Repos[0].Owner != "octo-org" || p.Repos[1].Name != "Hello-World" || !p.Repos[1].Private ||
		p.Repos[1].DefaultBranch != "main" || p.Repos[0].Private || p.Repos[1].GitHubID == 0 || p.Repos[1].Allowed {
		t.Errorf("repos = %+v", p.Repos)
	}
	if p.RateLimit.Limit != 5000 || p.RateLimit.Remaining >= 5000 || p.RateLimit.Reset.IsZero() {
		t.Errorf("rate limit = %+v", p.RateLimit)
	}
	if conns, _ := f.store.Connections(f.ctx); len(conns) != 0 {
		t.Errorf("Test stored a connection: %+v", conns)
	}
	if secrets, _ := f.vault.List(f.ctx, core.GlobalScope); len(secrets) != 0 {
		t.Errorf("Test stored a secret: %+v", secrets)
	}

	p, err = f.svc.Test(f.ctx, core.GitHubCredentials{Auth: core.GitHubPAT, Token: core.NewSecret([]byte("github_pat_wrong"))})
	if err != nil || p.OK || !strings.Contains(p.Error, "rejected the credentials") {
		t.Fatalf("bad token probe = %+v %v", p, err)
	}
}

func TestProbePaginatesRepos(t *testing.T) {
	f := newFx(t)
	for i := 0; i < 120; i++ {
		f.gh.AddRepo("octo-org", "repo-"+string(rune('a'+i/26))+string(rune('a'+i%26)), true, map[string]string{"x": "y"})
	}
	p, err := f.svc.Test(f.ctx, f.patCreds())
	if err != nil || !p.OK || len(p.Repos) != 121 || p.Truncated {
		t.Fatalf("probe: %d repos, truncated %v, %v", len(p.Repos), p.Truncated, err)
	}
	if n := f.gh.Count("GET", "/user/repos"); n != 2 {
		t.Errorf("/user/repos pages = %d, want 2", n)
	}
}

func TestValidateCreds(t *testing.T) {
	key := core.NewSecret([]byte("-----BEGIN RSA PRIVATE KEY-----"))
	tok := core.NewSecret([]byte("github_pat_x"))
	for name, c := range map[string]core.GitHubCredentials{
		"no secret":          {Auth: core.GitHubPAT},
		"two secrets":        {Auth: core.GitHubPAT, Token: tok, SecretRef: "secret:x"},
		"bad ref":            {Auth: core.GitHubPAT, SecretRef: "ghp_plain"},
		"pat with app id":    {Auth: core.GitHubPAT, Token: tok, AppID: "1"},
		"pat with key":       {Auth: core.GitHubPAT, PrivateKey: key},
		"app with token":     {Auth: core.GitHubApp, Token: tok, AppID: "1"},
		"app without id":     {Auth: core.GitHubApp, PrivateKey: key},
		"app bad id":         {Auth: core.GitHubApp, PrivateKey: key, AppID: "1; drop"},
		"negative install":   {Auth: core.GitHubApp, PrivateKey: key, AppID: "1", InstallationID: -1},
		"unknown auth":       {Auth: "oauth", Token: tok},
		"pat with install":   {Auth: core.GitHubPAT, Token: tok, InstallationID: 3},
		"ref with uppercase": {Auth: core.GitHubPAT, SecretRef: "secret:GITHUB"},
	} {
		if err := validateCreds(c); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if err := validateCreds(core.GitHubCredentials{Auth: core.GitHubApp, PrivateKey: key, AppID: "Iv1.ab1112223334445c"}); err != nil {
		t.Errorf("client id: %v", err)
	}
}

func TestConnectPAT(t *testing.T) {
	f := newFx(t)
	if f.svc.enabled.Load() {
		t.Fatal("tools enabled before any connection")
	}
	c := f.connect("OCTOCAT/hello-world")
	if c.Account != "octocat" || c.Auth != core.GitHubPAT || !c.OwnsSecret || c.SecretRef != "secret:github_"+c.ID || c.Status != "ok" || c.CreatedBy != "usr_owner" {
		t.Fatalf("connection = %+v", c)
	}
	sec, err := f.vault.Get(f.ctx, core.GlobalScope, "github_"+c.ID, "test")
	if err != nil || sec.Reveal() != f.gh.PAT {
		t.Fatalf("vault secret: %v", err)
	}
	r, err := f.store.Repo(f.ctx, "octocat", "Hello-World")
	if err != nil || !r.Allowed || r.Connection != c.ID || r.DefaultBranch != "main" || !r.Private || r.Owner != "octocat" || r.GitHubID == 0 {
		t.Fatalf("repo = %+v %v", r, err)
	}
	if !f.svc.enabled.Load() || !f.tool(repoListName).(core.Gated).Enabled(nil) {
		t.Error("tools not enabled after connecting")
	}
	if !slices.Contains(f.audit.Actions(), "github.connect") {
		t.Errorf("audit = %v", f.audit.Actions())
	}
	for _, e := range f.audit.All() {
		if strings.Contains(string(e.Detail), f.gh.PAT) {
			t.Fatal("PAT in an audit row")
		}
	}

	// A repository the token cannot reach, or one another connection holds.
	_, err = f.svc.Connect(f.ctx, f.patCreds(), []string{"octocat/nope"}, "usr_owner")
	if !errors.Is(err, core.ErrInvalid) {
		t.Errorf("unreachable repo: %v", err)
	}
	_, err = f.svc.Connect(f.ctx, f.patCreds(), []string{"octocat/Hello-World"}, "usr_owner")
	if !errors.Is(err, core.ErrConflict) {
		t.Errorf("repo of another connection: %v", err)
	}
	if conns, _ := f.store.Connections(f.ctx); len(conns) != 1 {
		t.Errorf("failed connects left connections: %d", len(conns))
	}
	if secrets, _ := f.vault.List(f.ctx, core.GlobalScope); len(secrets) != 1 {
		t.Errorf("failed connects left secrets: %+v", secrets)
	}
	_, err = f.svc.Connect(f.ctx, core.GitHubCredentials{Auth: core.GitHubPAT, Token: core.NewSecret([]byte("bad"))}, nil, "usr_owner")
	if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("bad token connect: %v", err)
	}
}

func TestConnectWithSecretRef(t *testing.T) {
	f := newFx(t)
	if _, err := f.vault.Put(f.ctx, core.GlobalScope, "github_pat", []byte(f.gh.PAT), ""); err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.Connect(f.ctx, core.GitHubCredentials{Auth: core.GitHubPAT, SecretRef: "secret:github_pat"}, []string{"octocat/Hello-World"}, "usr_owner")
	if err != nil || c.OwnsSecret || c.SecretRef != "secret:github_pat" {
		t.Fatalf("connect = %+v %v", c, err)
	}
	if err := f.svc.Disconnect(f.ctx, c.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.Get(f.ctx, core.GlobalScope, "github_pat", "test"); err != nil {
		t.Errorf("Disconnect deleted a secret it does not own: %v", err)
	}
	p, err := f.svc.Test(f.ctx, core.GitHubCredentials{Auth: core.GitHubPAT, SecretRef: "secret:missing"})
	if err != nil || p.OK || !strings.Contains(p.Error, "names no stored secret") {
		t.Errorf("missing ref probe = %+v %v", p, err)
	}
}

func TestSetReposAndDisconnect(t *testing.T) {
	f := newFx(t)
	f.gh.AddRepo("octocat", "Spoon-Knife", false, map[string]string{"index.html": "x"})
	c := f.connect("octocat/Hello-World")

	repos, err := f.svc.SetRepos(f.ctx, c.ID, []string{"octocat/spoon-knife"}, "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Name != "Hello-World" || repos[0].Allowed || repos[1].Name != "Spoon-Knife" || !repos[1].Allowed {
		t.Fatalf("after SetRepos = %+v", repos)
	}
	allowed, _ := f.svc.Repos(f.ctx)
	if len(allowed) != 1 || allowed[0].Name != "Spoon-Knife" {
		t.Errorf("Repos = %+v", allowed)
	}
	if _, err := f.svc.SetRepos(f.ctx, c.ID, []string{"octocat/nope"}, "usr_owner"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("unreachable repo: %v", err)
	}
	if _, err := f.svc.SetRepos(f.ctx, c.ID, nil, "usr_owner"); err != nil || f.svc.enabled.Load() {
		t.Errorf("empty allowlist: %v enabled=%v", err, f.svc.enabled.Load())
	}
	conn, held, err := f.svc.Connection(f.ctx, c.ID)
	if err != nil || conn.ID != c.ID || len(held) != 2 {
		t.Errorf("Connection = %+v %d %v", conn, len(held), err)
	}
	if err := f.svc.Disconnect(f.ctx, c.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Connection(f.ctx, c.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("connection after disconnect: %v", err)
	}
	if all, _ := f.store.Repos(f.ctx, ""); len(all) != 0 {
		t.Errorf("repos after disconnect: %+v", all)
	}
	if _, err := f.vault.Get(f.ctx, core.GlobalScope, "github_"+c.ID, "test"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("owned secret after disconnect: %v", err)
	}
	if err := f.svc.Disconnect(f.ctx, c.ID, "usr_owner"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("second disconnect: %v", err)
	}
	acts := f.audit.Actions()
	if !slices.Contains(acts, "github.repos") || !slices.Contains(acts, "github.disconnect") {
		t.Errorf("audit = %v", acts)
	}
}

func TestProbeRecordsStatusAndChecks(t *testing.T) {
	f := newFx(t)
	c := f.connect("octocat/Hello-World")
	p, err := f.svc.Probe(f.ctx, c.ID)
	if err != nil || !p.OK || len(p.Repos) != 1 || !p.Repos[0].Allowed || p.Repos[0].Connection != c.ID {
		t.Fatalf("probe = %+v %v", p, err)
	}
	if fs := f.svc.Checks()[0].Run(f.ctx); len(fs) != 0 {
		t.Errorf("healthy findings: %+v", fs)
	}
	// The default branch moved on GitHub: a probe updates the stored row.
	f.repo.DefaultBranch = "trunk"
	if _, err := f.svc.Probe(f.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.store.Repo(f.ctx, "octocat", "Hello-World"); r.DefaultBranch != "trunk" || !r.Allowed {
		t.Errorf("repo after probe = %+v", r)
	}

	f.gh.PAT = "github_pat_rotated"
	f.svc.dropSource(c.ID)
	p, err = f.svc.Probe(f.ctx, c.ID)
	if err != nil || p.OK {
		t.Fatalf("probe with a revoked token = %+v %v", p, err)
	}
	stored, _ := f.store.Connection(f.ctx, c.ID)
	if stored.Status != "error" || !strings.Contains(stored.Error, "rejected the credentials") {
		t.Errorf("stored status = %+v", stored)
	}
	fs := f.svc.Checks()[0].Run(f.ctx)
	if len(fs) != 1 || fs[0].ID != "github.connection_error" || fs[0].Severity != core.SevMedium {
		t.Errorf("findings = %+v", fs)
	}
	stored.Status, stored.Error, stored.TokenExpires = "ok", "", f.clock.Now().Add(48*time.Hour)
	if err := f.store.PutConnection(f.ctx, stored); err != nil {
		t.Fatal(err)
	}
	if fs := f.svc.Checks()[0].Run(f.ctx); len(fs) != 1 || fs[0].ID != "github.token_expiring" {
		t.Errorf("expiring findings = %+v", fs)
	}
}

func appFx(t *testing.T, installs ...githubtest.Installation) (*fx, *rsa.PrivateKey) {
	t.Helper()
	f := newFx(t)
	k := testKey(t)
	f.gh.App = &githubtest.App{ID: 42, Slug: "bothy-test", Key: &k.PublicKey, Installations: installs}
	f.gh.AddRepo("octo-org", "widgets", true, map[string]string{"go.mod": "module widgets\n"})
	return f, k
}

func appCreds(k *rsa.PrivateKey, install int64) core.GitHubCredentials {
	return core.GitHubCredentials{Auth: core.GitHubApp, AppID: "42", PrivateKey: core.NewSecret(pkcs1PEM(k)), InstallationID: install}
}

func TestGitHubApp(t *testing.T) {
	f, k := appFx(t, githubtest.Installation{ID: 7, Account: "octo-org", Type: "Organization", Repos: []string{"octo-org/widgets"}})
	p, err := f.svc.Test(f.ctx, appCreds(k, 0))
	if err != nil || !p.OK || p.InstallationID != 7 || p.Account != "octo-org" || p.AccountType != "Organization" || p.AppSlug != "bothy-test" {
		t.Fatalf("probe = %+v %v", p, err)
	}
	if len(p.Repos) != 1 || p.Repos[0].Owner != "octo-org" || p.Repos[0].Name != "widgets" {
		t.Errorf("installation repos = %+v", p.Repos)
	}
	c, err := f.svc.Connect(f.ctx, appCreds(k, 0), []string{"octo-org/widgets"}, "usr_owner")
	if err != nil || c.Auth != core.GitHubApp || c.AppID != "42" || c.InstallationID != 7 || c.AppSlug != "bothy-test" || !c.OwnsSecret {
		t.Fatalf("connect = %+v %v", c, err)
	}
	minted := f.gh.MintedTokens()
	probeTokens := f.gh.InstallationTokens()
	env := f.toolEnv()
	for i := 0; i < 3; i++ {
		f.mustCall(githubIssuesName, env, map[string]any{"repo": "octo-org/widgets"})
	}
	if got := f.gh.MintedTokens() - minted; got != 1 {
		t.Errorf("tokens minted for three calls = %d, want 1 (cached)", got)
	}
	var toks []string
	for _, tok := range f.gh.InstallationTokens() {
		if !slices.Contains(probeTokens, tok) {
			toks = append(toks, tok)
		}
	}
	red := f.vault.Redactor()
	for _, tok := range toks {
		if !strings.Contains(red.Redact("x "+tok+" y"), fakes.Mask) {
			t.Errorf("installation token %s… is not registered with the redactor", tok[:8])
		}
	}
	// A revoked token is replaced once, transparently.
	f.gh.RevokeInstallationTokens()
	if _, _, err := f.call(githubIssuesName, env, map[string]any{"repo": "octo-org/widgets"}); err != nil {
		t.Fatalf("after revocation: %v", err)
	}
	if got := f.gh.MintedTokens() - minted; got != 2 {
		t.Errorf("tokens after revocation = %d, want 2", got)
	}
	for _, r := range f.gh.Requests() {
		if strings.HasPrefix(r.Path, "/repos/") && r.Auth != "installation" && r.Auth != "bad" {
			t.Errorf("repository call %s %s authenticated as %s", r.Method, r.Path, r.Auth)
		}
		if strings.HasPrefix(r.Path, "/app") && r.Auth != "jwt" {
			t.Errorf("app call %s authenticated as %s", r.Path, r.Auth)
		}
	}
	if err := f.svc.Disconnect(f.ctx, c.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range toks {
		if strings.Contains(red.Redact(tok), fakes.Mask) {
			t.Error("disconnect left the installation token registered")
		}
	}
}

func TestGitHubAppInstallations(t *testing.T) {
	f, k := appFx(t,
		githubtest.Installation{ID: 7, Account: "octo-org", Type: "Organization"},
		githubtest.Installation{ID: 8, Account: "octocat", Type: "User"})
	p, err := f.svc.Test(f.ctx, appCreds(k, 0))
	if err != nil || p.OK || !strings.Contains(p.Error, "2 installations") || len(p.Installations) != 2 || p.Installations[1].Account != "octocat" {
		t.Fatalf("probe = %+v %v", p, err)
	}
	p, err = f.svc.Test(f.ctx, appCreds(k, 8))
	if err != nil || !p.OK || p.Account != "octocat" || len(p.Repos) != 2 {
		t.Fatalf("chosen installation probe = %+v %v", p, err)
	}
	p, _ = f.svc.Test(f.ctx, appCreds(k, 99))
	if p.OK || !strings.Contains(p.Error, "found nothing") {
		t.Errorf("unknown installation = %+v", p)
	}
	other := testKey(t)
	p, _ = f.svc.Test(f.ctx, appCreds(other, 7))
	if p.OK || !strings.Contains(p.Error, "rejected the credentials") {
		t.Errorf("wrong key = %+v", p)
	}
	f.gh.App.Installations = nil
	p, _ = f.svc.Test(f.ctx, appCreds(k, 0))
	if p.OK || !strings.Contains(p.Error, "not installed") {
		t.Errorf("no installation = %+v", p)
	}
}

func TestGitHubAppShortTokensRenew(t *testing.T) {
	f, k := appFx(t, githubtest.Installation{ID: 7, Account: "octo-org", Type: "Organization"})
	f.gh.App.TokenTTL = 4 * time.Minute // inside the renewal margin: every call mints
	if _, err := f.svc.Connect(f.ctx, appCreds(k, 7), []string{"octo-org/widgets"}, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	before := f.gh.MintedTokens()
	env := f.toolEnv()
	f.mustCall(githubIssuesName, env, map[string]any{"repo": "octo-org/widgets"})
	f.mustCall(githubIssuesName, env, map[string]any{"repo": "octo-org/widgets"})
	if got := f.gh.MintedTokens() - before; got != 2 {
		t.Errorf("tokens = %d, want one per call near expiry", got)
	}
}

func TestEndpointFromConfig(t *testing.T) {
	f := newFx(t)
	f.connect("octocat/Hello-World")
	f.mustCall(githubIssuesName, f.toolEnv(), map[string]any{"repo": "octocat/Hello-World"})
	dests := f.egress.Dests
	if len(dests) == 0 {
		t.Fatal("no system client requested")
	}
	d := dests[len(dests)-1]
	if d.Name != "github" || !d.AllowPrivate || len(d.Hosts) != 1 || !strings.HasPrefix(f.gh.URL, "http://"+d.Hosts[0]) {
		t.Errorf("system dest = %+v", d)
	}
	cfg := config.Default()
	ep, _ := newEndpoint(cfg.GitHub.APIBase)
	if ep.private || !slices.Equal(ep.hosts, []string{"api.github.com:443", "codeload.github.com:443"}) {
		t.Errorf("default endpoint = %+v", ep)
	}
}

func TestKeyedMutex(t *testing.T) {
	var k keyedMutex
	unlock := k.lock("a")
	done := make(chan struct{})
	go func() {
		u := k.lock("a")
		u()
		close(done)
	}()
	other := k.lock("b")
	other()
	select {
	case <-done:
		t.Fatal("second lock of a key did not wait")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	<-done
	if len(k.locks) != 0 {
		t.Errorf("locks left: %d", len(k.locks))
	}
}
