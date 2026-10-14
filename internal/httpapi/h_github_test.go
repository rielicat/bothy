package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// fakeGitHub records what the handlers pass to core.GitHub.
type fakeGitHub struct {
	core.GitHub
	mu    sync.Mutex
	creds []core.GitHubCredentials
	repos [][]string
	by    []core.UserID
	err   error
}

const ghConnID = "ghc_01m4wa97e4s0p3qgcng95bcyhf"

func (f *fakeGitHub) note(c *core.GitHubCredentials, repos []string, by core.UserID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c != nil {
		f.creds = append(f.creds, *c)
	}
	f.repos = append(f.repos, repos)
	f.by = append(f.by, by)
	return f.err
}

func (f *fakeGitHub) conn() *core.GitHubConnection {
	return &core.GitHubConnection{ID: ghConnID, Auth: core.GitHubPAT, Account: "octocat", SecretRef: "secret:github_" + ghConnID, OwnsSecret: true, Status: "ok"}
}

func (f *fakeGitHub) Test(_ context.Context, c core.GitHubCredentials) (*core.GitHubProbe, error) {
	if err := f.note(&c, nil, ""); err != nil {
		return nil, err
	}
	return &core.GitHubProbe{OK: true, Auth: c.Auth, Account: "octocat", Repos: []core.GitHubRepoAccess{{Owner: "octocat", Name: "Hello-World"}}}, nil
}

func (f *fakeGitHub) Connect(_ context.Context, c core.GitHubCredentials, repos []string, by core.UserID) (*core.GitHubConnection, error) {
	if err := f.note(&c, repos, by); err != nil {
		return nil, err
	}
	return f.conn(), nil
}

func (f *fakeGitHub) Connections(context.Context) ([]*core.GitHubConnection, error) {
	return []*core.GitHubConnection{f.conn()}, f.err
}

func (f *fakeGitHub) Connection(_ context.Context, id string) (*core.GitHubConnection, []*core.GitHubRepo, error) {
	if id != ghConnID {
		return nil, nil, fmt.Errorf("connection %s: %w", id, core.ErrNotFound)
	}
	return f.conn(), nil, f.err
}

func (f *fakeGitHub) Probe(_ context.Context, id string) (*core.GitHubProbe, error) {
	if id != ghConnID {
		return nil, core.ErrNotFound
	}
	return &core.GitHubProbe{OK: false, Error: "GitHub rejected the credentials"}, f.err
}

func (f *fakeGitHub) SetRepos(_ context.Context, id string, repos []string, by core.UserID) ([]*core.GitHubRepo, error) {
	if err := f.note(nil, repos, by); err != nil {
		return nil, err
	}
	out := []*core.GitHubRepo{}
	for _, r := range repos {
		owner, name, _ := strings.Cut(r, "/")
		out = append(out, &core.GitHubRepo{Connection: id, Owner: owner, Name: name, Allowed: true})
	}
	return out, nil
}

func (f *fakeGitHub) Disconnect(_ context.Context, id string, by core.UserID) error {
	if id != ghConnID {
		return core.ErrNotFound
	}
	return f.note(nil, nil, by)
}

func (f *fakeGitHub) Repos(context.Context) ([]*core.GitHubRepo, error) {
	return []*core.GitHubRepo{{Connection: ghConnID, Owner: "octocat", Name: "Hello-World", Allowed: true}}, f.err
}

func TestGitHubRoutes(t *testing.T) {
	h := newHarness(t).setup()
	gh := &fakeGitHub{}
	h.srv.d.GitHub = gh
	const pat = "github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz0123456789ABCD"

	rec := h.do(http.MethodPost, "/api/github/connections/test", dto.GitHubConnectReq{Auth: core.GitHubPAT, Token: pat}, h.admin)
	var probe core.GitHubProbe
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &probe) != nil || !probe.OK || probe.Account != "octocat" {
		t.Fatalf("test = %d %s", rec.Code, rec.Body)
	}
	if len(gh.creds) != 1 || gh.creds[0].Token.Reveal() != pat || gh.creds[0].Auth != core.GitHubPAT {
		t.Fatalf("credentials passed = %+v", gh.creds)
	}

	rec = h.do(http.MethodPost, "/api/github/connections", dto.GitHubConnectReq{Auth: core.GitHubApp, AppID: " 42 ", PrivateKey: "-----BEGIN RSA PRIVATE KEY-----",
		InstallationID: 7, Repos: []string{"octocat/Hello-World"}}, h.owner)
	var conn core.GitHubConnection
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &conn) != nil || conn.ID != ghConnID {
		t.Fatalf("connect = %d %s", rec.Code, rec.Body)
	}
	c := gh.creds[1]
	if c.Auth != core.GitHubApp || c.AppID != "42" || c.InstallationID != 7 || c.PrivateKey.Reveal() != "-----BEGIN RSA PRIVATE KEY-----" ||
		!slices.Equal(gh.repos[1], []string{"octocat/Hello-World"}) || gh.by[1] != h.owner.user {
		t.Errorf("connect passed %+v %v %v", c, gh.repos, gh.by)
	}
	for _, body := range []string{rec.Body.String()} {
		if strings.Contains(body, pat) || strings.Contains(body, "BEGIN RSA") {
			t.Fatal("a secret came back in a response")
		}
	}

	rec = h.do(http.MethodGet, "/api/github/connections", nil, h.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"secret_ref":"secret:github_`+ghConnID+`"`) {
		t.Errorf("list = %d %s", rec.Code, rec.Body)
	}
	rec = h.do(http.MethodGet, "/api/github/connections/"+ghConnID, nil, h.admin)
	var detail dto.GitHubConnectionDetail
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil || detail.Connection.ID != ghConnID || detail.Repos == nil {
		t.Errorf("detail = %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodGet, "/api/github/connections/ghc_nope", nil, h.admin); rec.Code != http.StatusNotFound {
		t.Errorf("missing detail = %d", rec.Code)
	}
	rec = h.do(http.MethodPost, "/api/github/connections/"+ghConnID+"/test", nil, h.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Errorf("probe = %d %s", rec.Code, rec.Body)
	}
	rec = h.do(http.MethodPut, "/api/github/connections/"+ghConnID+"/repos", dto.GitHubReposReq{Repos: []string{"octocat/Spoon-Knife"}}, h.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Spoon-Knife"`) || gh.by[len(gh.by)-1] != h.admin.user {
		t.Errorf("set repos = %d %s", rec.Code, rec.Body)
	}
	rec = h.do(http.MethodPut, "/api/github/connections/"+ghConnID+"/repos", dto.GitHubReposReq{}, h.admin)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty allowlist = %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodDelete, "/api/github/connections/"+ghConnID, nil, h.admin); rec.Code != http.StatusNoContent {
		t.Errorf("disconnect = %d %s", rec.Code, rec.Body)
	}

	// Members see the allowlist but cannot manage connections.
	if rec := h.do(http.MethodGet, "/api/github/repos", nil, h.member); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Hello-World") {
		t.Errorf("member repos = %d %s", rec.Code, rec.Body)
	}
	for _, rt := range h.srv.githubRoutes() {
		method, path := concretePath(rt.pattern)
		if rt.access != accessAdmin {
			continue
		}
		if rec := h.do(method, path, nil, h.member); rec.Code != http.StatusForbidden {
			t.Errorf("member %s = %d", rt.pattern, rec.Code)
		}
	}
}

// Indexes are per principal: the repository list shows the commit the
// caller's own index read, never another user's, and ignores a workspace
// folder that only borrowed the repository's name.
func TestGitHubReposShowCallersIndexState(t *testing.T) {
	h := newHarness(t).setup()
	h.srv.d.GitHub = &fakeGitHub{}
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	const adminSHA = "7638417db6d59f3c431d3e1f261cc637155684cd"
	r := k.add(core.UserPrincipal(h.admin.user), "octocat/hello-world")
	r.Source, r.SHA, r.IndexedSHA = core.KnowledgeGitHub, "1111111111111111111111111111111111111111", adminSHA
	borrowed := k.add(core.UserPrincipal(h.member.user), "octocat/hello-world")
	borrowed.IndexedSHA = "2222222222222222222222222222222222222222"

	read := func(who *client) string {
		t.Helper()
		var repos []core.GitHubRepo
		rec := h.do(http.MethodGet, "/api/github/repos", nil, who)
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &repos) != nil || len(repos) != 1 {
			t.Fatalf("repos = %d %s", rec.Code, rec.Body)
		}
		return repos[0].LastIndexedSHA
	}
	if got := read(h.admin); got != adminSHA {
		t.Errorf("admin's last_indexed_sha = %q, want %q", got, adminSHA)
	}
	if got := read(h.member); got != "" {
		t.Errorf("member's last_indexed_sha = %q, want none", got)
	}
	if got := read(h.owner); got != "" {
		t.Errorf("owner's last_indexed_sha = %q, want none", got)
	}
}

func TestGitHubRouteErrors(t *testing.T) {
	h := newHarness(t).setup()
	if rec := h.do(http.MethodGet, "/api/github/connections", nil, h.admin); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without GitHub = %d", rec.Code)
	}
	gh := &fakeGitHub{}
	h.srv.d.GitHub = gh
	for err, want := range map[error]int{
		fmt.Errorf("%w: GitHub rejected the credentials", core.ErrInvalid): http.StatusBadRequest,
		fmt.Errorf("%w: octocat/x is already connected", core.ErrConflict): http.StatusConflict,
		core.ErrSealed: http.StatusServiceUnavailable,
	} {
		gh.err = err
		rec := h.do(http.MethodPost, "/api/github/connections", dto.GitHubConnectReq{Auth: core.GitHubPAT, Token: "github_pat_x"}, h.admin)
		if rec.Code != want {
			t.Errorf("%v: %d %s", err, rec.Code, rec.Body)
		}
	}
	gh.err = nil
	if rec := h.do(http.MethodPost, "/api/github/connections", "{not json", h.admin); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json = %d", rec.Code)
	}
}
