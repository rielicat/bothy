package github

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/github/githubtest"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
	"github.com/rielicat/bothy/internal/testkit/sandboxtest"
)

// fx is a Service against the fake GitHub with one private repository,
// octocat/Hello-World.
type fx struct {
	t      *testing.T
	ctx    context.Context
	gh     *githubtest.Server
	cfg    *config.Config
	store  core.GitHubStore
	vault  *fakes.Vault
	egress *sandboxtest.Egress
	bus    *events.Bus
	audit  *fakes.Auditor
	clock  *fakes.Clock
	svc    *Service
	data   string
	repo   *githubtest.Repo
}

var helloFiles = map[string]string{
	"README.md":   "# Hello World\n\nA test repository.\n",
	"src/main.go": "package main\n\nfunc main() {}\n",
	"run.sh*":     "#!/bin/sh\necho hi\n",
	".gitignore":  "node_modules/\n*.log\n",
	"docs/a.md":   "alpha\n",
}

func newFx(t *testing.T, mut ...func(*config.Config)) *fx {
	t.Helper()
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	cfg := config.Default()
	cfg.GitHub.APIBase = gh.URL
	for _, m := range mut {
		m(cfg)
	}
	f := &fx{t: t, ctx: context.Background(), gh: gh, cfg: cfg, store: memstore.NewGitHub(), vault: fakes.NewVault(nil),
		egress: &sandboxtest.Egress{Client: &http.Client{}}, bus: events.New(events.Options{}), audit: &fakes.Auditor{},
		clock: fakes.NewClock(time.Now()), data: t.TempDir()}
	f.repo = gh.AddRepo("octocat", "Hello-World", true, helloFiles)
	svc, err := New(Deps{Config: config.Static(cfg), Store: f.store, Vault: f.vault, Egress: f.egress, Bus: f.bus,
		Audit: f.audit, DataDir: f.data, Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

func (f *fx) patCreds() core.GitHubCredentials {
	return core.GitHubCredentials{Auth: core.GitHubPAT, Token: core.NewSecret([]byte(f.gh.PAT))}
}

func (f *fx) connect(repos ...string) *core.GitHubConnection {
	f.t.Helper()
	c, err := f.svc.Connect(f.ctx, f.patCreds(), repos, "usr_owner")
	if err != nil {
		f.t.Fatalf("connect: %v", err)
	}
	return c
}

func (f *fx) toolEnv() *core.ToolEnv {
	u := core.UserID(core.NewID(core.PrefixUser))
	s := core.SessionID(core.NewID(core.PrefixSession))
	return &core.ToolEnv{
		User: u, Role: core.RoleOwner, Session: s, Run: core.RunID(core.NewID(core.PrefixRun)), CallID: core.NewID(core.PrefixToolCall),
		Principal: core.Principal{ID: core.UserPrincipal(u), Kind: core.PrincipalUser, User: u,
			Workspace: core.WorkspaceID(core.NewID(core.PrefixWorkspace)), ToolProfile: "standard"},
		Trigger: core.TrigMessage, Workspace: f.t.TempDir(), Sandbox: &sandboxtest.Sandbox{}, Interactive: true,
	}
}

func (f *fx) tool(name string) core.Tool {
	f.t.Helper()
	for _, tl := range f.svc.Tools() {
		if tl.Spec().Def.Name == name {
			return tl
		}
	}
	f.t.Fatalf("no tool %s", name)
	return nil
}

func (f *fx) plan(name string, env *core.ToolEnv, args any) (*core.Action, error) {
	f.t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.tool(name).Plan(f.ctx, env, b)
}

// call plans and executes, failing the test on a Plan error.
func (f *fx) call(name string, env *core.ToolEnv, args any) (*core.Action, *core.ToolResult, error) {
	f.t.Helper()
	a, err := f.plan(name, env, args)
	if err != nil {
		f.t.Fatalf("plan %s: %v", name, err)
	}
	res, err := f.tool(name).Exec(f.ctx, env, a)
	return a, res, err
}

func (f *fx) mustCall(name string, env *core.ToolEnv, args any) (*core.Action, *core.ToolResult) {
	f.t.Helper()
	a, res, err := f.call(name, env, args)
	if err != nil {
		f.t.Fatalf("exec %s: %v", name, err)
	}
	return a, res
}

func (f *fx) checkout(env *core.ToolEnv) {
	f.t.Helper()
	f.mustCall(repoCheckoutName, env, map[string]any{"repo": "octocat/Hello-World"})
}

func wsPath(env *core.ToolEnv, rel string) string {
	return filepath.Join(env.Workspace, filepath.FromSlash(rel))
}

func writeWS(t *testing.T, env *core.ToolEnv, rel, content string) {
	t.Helper()
	p := wsPath(env, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o660); err != nil {
		t.Fatal(err)
	}
}

type configT = config.Config
