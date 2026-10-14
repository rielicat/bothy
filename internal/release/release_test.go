package release

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/release/actionstest"
	"github.com/rielicat/bothy/internal/testkit/conformance"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

const testToken = "ghp_testtokenAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type fixture struct {
	t     *testing.T
	ctx   context.Context
	store core.Store
	cfg   *config.Config
	gh    *actionstest.Server
	svc   *Service
	hub   *fakes.Hub
	bus   *events.Bus
	audit *fakes.Auditor
	clock *fakes.Clock
	dests []core.SystemDest

	owner, alice, bob *core.User
	session           core.SessionID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: t.Context(), store: memstore.New(), gh: actionstest.New(testToken), hub: &fakes.Hub{},
		bus: events.New(events.Options{}), audit: &fakes.Auditor{}, clock: fakes.NewClock(time.Now().UTC().Truncate(time.Millisecond))}
	t.Cleanup(f.gh.Close)
	f.cfg = config.Default()
	f.cfg.Releases.GitHubAPI = f.gh.URL()
	f.cfg.Releases.Environments = []config.ReleaseEnv{{Name: "staging", Repo: "acme/web", Workflow: "deploy.yml", RefPattern: "main",
		Inputs: map[string]string{"target": "staging"}, Approvers: []string{"admin"}, Required: 2}}
	f.owner, f.alice, f.bob = f.user("owner", core.RoleOwner), f.user("alice", core.RoleAdmin), f.user("bob", core.RoleAdmin)
	s := &core.Session{Principal: core.UserPrincipal(f.owner.ID), Origin: "telegram", CreatedAt: f.clock.Now()}
	if err := f.store.Sessions().Create(f.ctx, s); err != nil {
		t.Fatal(err)
	}
	f.session = s.ID
	svc, err := New(Deps{
		Config: config.Static(f.cfg), Store: f.store, Deploys: f.store.(core.ReleaseStore).Releases(),
		Vault: fakes.NewVault(map[string]string{"github_token": testToken}),
		HTTP: func(d core.SystemDest) *http.Client {
			f.dests = append(f.dests, d)
			return &http.Client{Timeout: 5 * time.Second}
		},
		Messenger: f.hub, Bus: f.bus, Audit: f.audit, Redactor: fakes.NewRedactor(), Clock: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

func (f *fixture) user(name string, role core.Role) *core.User {
	f.t.Helper()
	u := &core.User{Username: name, DisplayName: strings.ToUpper(name[:1]) + name[1:], Role: role, CreatedAt: time.Now()}
	if err := f.store.Users().Create(f.ctx, u); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fixture) tool(name string) core.Tool {
	for _, t := range f.svc.Tools() {
		if t.Spec().Def.Name == name {
			return t
		}
	}
	f.t.Fatalf("no tool %s", name)
	return nil
}

// env is the owner's interactive tool env in the Telegram DM session.
func (f *fixture) env() *core.ToolEnv {
	f.t.Helper()
	run := &core.Run{Session: f.session, Principal: core.UserPrincipal(f.owner.ID), User: f.owner.ID, Trigger: core.TrigMessage,
		Status: core.RunQueued, CreatedAt: f.clock.Now()}
	if err := f.store.Runs().Create(f.ctx, run); err != nil {
		f.t.Fatal(err)
	}
	p, err := f.store.Principals().Get(f.ctx, core.SystemViewer(), core.UserPrincipal(f.owner.ID))
	if err != nil {
		f.t.Fatal(err)
	}
	return &core.ToolEnv{User: f.owner.ID, Role: core.RoleOwner, Principal: *p, Session: f.session, Run: run.ID,
		CallID: core.NewID(core.PrefixToolCall), Trigger: core.TrigMessage, Interactive: true,
		Chat: &core.ChatRef{Channel: "telegram", ChatID: "1001", Kind: core.ChatDM}}
}

func (f *fixture) plan(env *core.ToolEnv, args string) *core.Action {
	f.t.Helper()
	a, err := f.tool(deployName).Plan(f.ctx, env, json.RawMessage(args))
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

// approve stores the approval of env's call with the given status and votes.
func (f *fixture) approve(env *core.ToolEnv, a *core.Action, st core.ApprovalStatus, voters ...*core.User) *core.Approval {
	f.t.Helper()
	req := core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: env.User}
	ap := &core.Approval{Run: env.Run, Session: env.Session, Principal: env.Principal.ID, ToolCallID: env.CallID, Action: a,
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: req}, Card: core.Card{Title: "Deploy a release", Risk: core.SEIrreversible},
		Status: st, Approver: req, OnTimeout: core.TimeoutDeny,
		CreatedAt: f.clock.Now(), ExpiresAt: f.clock.Now().Add(15 * time.Minute)}
	if err := f.store.Approvals().Create(f.ctx, ap); err != nil {
		f.t.Fatal(err)
	}
	for i, u := range voters {
		at := f.clock.Now().Add(time.Duration(i) * time.Millisecond)
		if err := f.store.Approvals().AddVote(f.ctx, core.ApprovalVote{Approval: ap.ID, User: u.ID, Choice: core.AllowOnce, Via: "web", At: at}); err != nil {
			f.t.Fatal(err)
		}
	}
	return ap
}

func (f *fixture) deploys() []*core.Deploy {
	f.t.Helper()
	ds, err := f.svc.d.Deploys.List(f.ctx, core.SystemViewer(), core.DeployFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	return ds
}

func (f *fixture) notices() []string {
	f.t.Helper()
	ms, err := f.store.Messages().Range(f.ctx, core.SystemViewer(), f.session, 0, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		if m.Kind == core.KindNotice && m.Provenance == core.ProvSystem {
			out = append(out, m.PlainText())
		}
	}
	return out
}

// outcome is what the runner would show for an Exec: the error text or the
// result, and whether the call failed.
func outcome(res *core.ToolResult, err error) (string, bool) {
	if err != nil {
		return err.Error(), true
	}
	return res.Content, res.IsError
}

func TestToolConformance(t *testing.T) {
	f := newFixture(t)
	conformance.Tool(t, f.tool(deployName), []conformance.ToolCase{
		{Name: "default ref", Args: json.RawMessage(`{"environment":"staging"}`)},
		{Name: "explicit ref", Args: json.RawMessage(`{"environment":"staging","ref":"main"}`)},
		{Name: "unknown environment", Args: json.RawMessage(`{"environment":"prod"}`), WantErr: true},
		{Name: "ref outside the pattern", Args: json.RawMessage(`{"environment":"staging","ref":"feature/x"}`), WantErr: true},
		{Name: "extra argument", Args: json.RawMessage(`{"environment":"staging","inputs":{"x":"y"}}`), WantErr: true},
		{Name: "no environment", Args: json.RawMessage(`{}`), WantErr: true},
	})
	conformance.Tool(t, f.tool(statusName), []conformance.ToolCase{
		{Name: "all", Args: json.RawMessage(`{}`)},
		{Name: "one", Args: json.RawMessage(`{"environment":"staging"}`)},
		{Name: "unknown", Args: json.RawMessage(`{"environment":"prod"}`), WantErr: true},
	})
}

func TestDeployPlan(t *testing.T) {
	f := newFixture(t)
	spec := f.tool(deployName).Spec()
	if spec.Manifest.SideEffect != core.SEIrreversible || !slices.Equal(spec.Manifest.Triggers, []core.Trigger{core.TrigMessage}) || spec.Manifest.SpaceOK {
		t.Fatalf("manifest = %+v", spec.Manifest)
	}
	a := f.plan(f.env(), `{"environment":"staging"}`)
	if a.Kind != core.ActRelease || a.SideEffect != core.SEIrreversible ||
		string(a.Payload) != `{"environment":"staging","inputs":{"target":"staging"},"ref":"main","repo":"acme/web","workflow":"deploy.yml"}` {
		t.Fatalf("action = %+v %s", a, a.Payload)
	}
	f.cfg.Releases.Environments[0].RefPattern = "release/*"
	if _, err := f.tool(deployName).Plan(f.ctx, f.env(), json.RawMessage(`{"environment":"staging"}`)); !errors.Is(err, core.ErrInvalid) ||
		!strings.Contains(err.Error(), "say which branch or tag") {
		t.Fatalf("glob without a ref = %v", err)
	}
	if a := f.plan(f.env(), `{"environment":"staging","ref":"release/1.2"}`); !strings.Contains(string(a.Payload), `"ref":"release/1.2"`) {
		t.Fatalf("payload = %s", a.Payload)
	}
	gate := f.tool(deployName).(core.Gated)
	if !gate.Enabled(f.env()) {
		t.Fatal("tool hidden with an environment configured")
	}
	f.cfg.Releases.Environments = nil
	if gate.Enabled(f.env()) || f.tool(statusName).(core.Gated).Enabled(f.env()) {
		t.Fatal("tools offered without environments")
	}
}

func TestDeployExecDispatchesAfterApproval(t *testing.T) {
	f := newFixture(t)
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	ap := f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	res, err := f.tool(deployName).Exec(f.ctx, env, a)
	if err != nil || res.IsError {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	ds := f.gh.Dispatches()
	if len(ds) != 1 || ds[0].Repo != "acme/web" || ds[0].Workflow != "deploy.yml" || ds[0].Ref != "main" ||
		ds[0].Inputs["target"] != "staging" || !ds[0].ReturnRunDetails {
		t.Fatalf("dispatches = %+v", ds)
	}
	runs := f.gh.Runs()
	if !strings.Contains(res.Content, "approved by Alice and Bob") || !strings.Contains(res.Content, "Run 9001") || strings.Contains(res.Content, "evil.example") {
		t.Fatalf("result = %q", res.Content)
	}
	deps := f.deploys()
	if len(deps) != 1 {
		t.Fatalf("deploys = %+v", deps)
	}
	d := deps[0]
	if d.Status != core.DeployDispatched || d.WorkflowRun != runs[0].ID || d.RunURL != f.gh.URL()+"/acme/web/actions/runs/9001" ||
		d.Approval != ap.ID || !slices.Equal(d.ApprovedBy, []core.UserID{f.alice.ID, f.bob.ID}) || d.RequestedBy != f.owner.ID ||
		d.Chat == nil || d.Chat.ChatID != "1001" || d.Session != f.session || d.Run != env.Run {
		t.Fatalf("deploy = %+v", d)
	}
	if len(f.dests) == 0 || f.dests[0].Name != "release:github" || !f.dests[0].AllowPrivate || len(f.dests[0].Hosts) != 1 {
		t.Fatalf("system destinations = %+v", f.dests)
	}
	var dispatched bool
	for _, e := range f.audit.All() {
		dispatched = dispatched || e.Action == "release.dispatch" && e.Outcome == "ok" && e.Target == d.ID
	}
	if !dispatched {
		t.Fatalf("audit = %v", f.audit.Actions())
	}
	select {
	case <-f.svc.wake:
	default:
		t.Fatal("a dispatch did not wake the follower")
	}
}

func TestDeployExecRefusesWithoutApproval(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(f *fixture, env *core.ToolEnv, a *core.Action)
		want string
	}{
		{"no approval", func(*fixture, *core.ToolEnv, *core.Action) {}, "no approval was recorded"},
		{"pending approval", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			f.approve(env, a, core.ApprovalPending, f.alice)
		}, "not approved"},
		{"one vote short", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			f.approve(env, a, core.ApprovalAllowed, f.alice)
		}, "only 1 of 2 approvals"},
		{"single-approver approval", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			ap := &core.Approval{Run: env.Run, Session: env.Session, Principal: env.Principal.ID, ToolCallID: env.CallID, Action: a,
				Status: core.ApprovalAllowed, Approver: core.ApproverReq{User: env.User}, OnTimeout: core.TimeoutDeny, Card: core.Card{Risk: core.SEIrreversible},
				CreatedAt: f.clock.Now(), ExpiresAt: f.clock.Now()}
			if err := f.store.Approvals().Create(f.ctx, ap); err != nil {
				f.t.Fatal(err)
			}
		}, "not approved"},
		{"approval of another call", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			other := *env
			other.CallID = core.NewID(core.PrefixToolCall)
			f.approve(&other, a, core.ApprovalAllowed, f.alice, f.bob)
		}, "no approval was recorded"},
		{"environment changed", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
			f.cfg.Releases.Environments[0].Inputs = map[string]string{"target": "prod"}
		}, "changed after this deploy was approved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			env := f.env()
			a := f.plan(env, `{"environment":"staging"}`)
			tc.prep(f, env, a)
			if text, failed := outcome(f.tool(deployName).Exec(f.ctx, env, a)); !failed || !strings.Contains(text, tc.want) {
				t.Fatalf("Exec = %q (failed %v); want a refusal containing %q", text, failed, tc.want)
			}
			if n := len(f.gh.Dispatches()); n != 0 {
				t.Fatalf("%d dispatches without a valid approval", n)
			}
			if n := len(f.deploys()); n != 0 {
				t.Fatalf("%d deploy records", n)
			}
		})
	}
}

func TestDeployDispatchFailure(t *testing.T) {
	f := newFixture(t)
	f.gh.RejectRef("main")
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	if text, failed := outcome(f.tool(deployName).Exec(f.ctx, env, a)); !failed || !strings.Contains(text, "No ref found for: main") {
		t.Fatalf("Exec = %q (failed %v)", text, failed)
	}
	deps := f.deploys()
	if len(deps) != 1 || deps[0].Status != core.DeployFailed || !strings.Contains(deps[0].Error, "422") || deps[0].FinishedAt.IsZero() {
		t.Fatalf("deploys = %+v", deps)
	}
	if outcome := outcomeText(deps[0], time.Hour); !strings.Contains(outcome, "did not start") {
		t.Fatalf("outcome = %q", outcome)
	}
}

func TestFollowerReportsOutcome(t *testing.T) {
	f := newFixture(t)
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	if res, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil || res.IsError {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	sub := f.bus.Subscribe(core.Filter{System: true}, 8)
	defer sub.Close()
	id := f.gh.Runs()[0].ID

	f.svc.poll(f.ctx)
	if d := f.deploys()[0]; d.Status != core.DeployDispatched {
		t.Fatalf("queued run: %s", d.Status)
	}
	f.gh.SetRun(id, "in_progress", "")
	f.svc.poll(f.ctx)
	if d := f.deploys()[0]; d.Status != core.DeployRunning || len(f.notices()) != 0 {
		t.Fatalf("running: %s, notices %q", d.Status, f.notices())
	}
	f.gh.SetRun(id, "completed", "success")
	f.svc.poll(f.ctx)
	d := f.deploys()[0]
	if d.Status != core.DeploySucceeded || d.Conclusion != "success" || d.FinishedAt.IsZero() {
		t.Fatalf("finished deploy = %+v", d)
	}
	notices := f.notices()
	want := "Deploy to staging succeeded: acme/web at main. Run 9001: " + f.gh.URL() + "/acme/web/actions/runs/9001"
	if !slices.Equal(notices, []string{want}) {
		t.Fatalf("session notices = %q, want %q", notices, want)
	}
	if len(f.hub.Sent) != 1 || f.hub.Sent[0] != "telegram:1001:|"+want {
		t.Fatalf("chat posts = %q", f.hub.Sent)
	}
	select {
	case ev := <-sub.C():
		if ev.Type != core.EvMessageCreated || ev.Principal != core.UserPrincipal(f.owner.ID) || ev.Session != f.session {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("no message.created event")
	}
	f.svc.poll(f.ctx)
	if len(f.notices()) != 1 {
		t.Fatal("a finished deploy was reported twice")
	}
}

func TestFollowerEndings(t *testing.T) {
	cases := []struct {
		name   string
		act    func(f *fixture, run int64)
		status core.DeployStatus
		want   string
	}{
		{"failure", func(f *fixture, run int64) { f.gh.SetRun(run, "completed", "failure") }, core.DeployFailed, "ended with “failure”"},
		{"cancelled", func(f *fixture, run int64) { f.gh.SetRun(run, "completed", "cancelled") }, core.DeployFailed, "“cancelled”"},
		{"run deleted", func(f *fixture, run int64) { f.gh.DeleteRun(run) }, core.DeployUnknown, "no longer knows"},
		{"followed too long", func(f *fixture, run int64) { f.clock.Advance(7 * time.Hour) }, core.DeployUnknown, "stopped following it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			env := f.env()
			a := f.plan(env, `{"environment":"staging"}`)
			f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
			if _, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil {
				t.Fatal(err)
			}
			tc.act(f, f.gh.Runs()[0].ID)
			f.svc.poll(f.ctx)
			d := f.deploys()[0]
			if d.Status != tc.status || len(f.notices()) != 1 || !strings.Contains(f.notices()[0], tc.want) {
				t.Fatalf("deploy %s, notices %q; want %s with %q", d.Status, f.notices(), tc.status, tc.want)
			}
		})
	}
}

func TestLegacyDispatchFindsRun(t *testing.T) {
	f := newFixture(t)
	f.gh.SetLegacy(true)
	run, err := f.svc.d.Dispatcher.Dispatch(f.ctx, core.WorkflowDispatch{Repo: "acme/web", Workflow: "deploy.yml", Ref: "main"})
	if err != nil || run.ID != 9001 || run.Status != "queued" || run.HeadSHA != strings.Repeat("ab", 20) {
		t.Fatalf("legacy dispatch = %+v, %v", run, err)
	}
	g := f.svc.d.Dispatcher.(*GitHub)
	g.findFor, g.findEvery = 0, time.Millisecond
	run, err = g.Dispatch(f.ctx, core.WorkflowDispatch{Repo: "acme/web", Workflow: "deploy.yml", Ref: "other"})
	if err != nil || run.ID == 0 {
		t.Fatalf("second legacy dispatch = %+v, %v", run, err)
	}

	missing := &core.Deploy{Environment: "staging", Repo: "acme/web", Workflow: "deploy.yml", Ref: "main",
		Principal: core.UserPrincipal(f.owner.ID), Session: f.session, Status: core.DeployDispatched, CreatedAt: f.clock.Now()}
	if err := f.svc.d.Deploys.Create(f.ctx, missing); err != nil {
		t.Fatal(err)
	}
	f.svc.poll(f.ctx)
	if got, _ := f.svc.d.Deploys.Get(f.ctx, core.SystemViewer(), missing.ID); got.Status != core.DeployUnknown ||
		!strings.Contains(f.notices()[0], "could not find its workflow run") {
		t.Fatalf("unidentified run: %+v %q", got, f.notices())
	}
}

func TestGitHubClient(t *testing.T) {
	f := newFixture(t)
	g := f.svc.d.Dispatcher.(*GitHub)
	if _, err := g.RunStatus(f.ctx, "acme/web", 4242); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("RunStatus of a missing run = %v", err)
	}
	if _, err := g.RunStatus(f.ctx, "acme", 1); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad repo = %v", err)
	}
	run := ghRun{ID: 5, Status: "Weird Status!", Conclusion: new("x y"), HeadSHA: "nothex"}
	if c := run.core("https://github.com", "acme/web"); c.Status != "unknown" || c.Conclusion != "unknown" || c.HeadSHA != "" ||
		c.URL != "https://github.com/acme/web/actions/runs/5" {
		t.Fatalf("sanitized run = %+v", c)
	}

	bad := newFixture(t)
	bad.svc.d.Dispatcher = NewGitHub(config.Static(bad.cfg), fakes.NewVault(map[string]string{"github_token": "wrong"}), bad.svc.d.HTTP, nil)
	if _, err := bad.svc.d.Dispatcher.Dispatch(bad.ctx, core.WorkflowDispatch{Repo: "acme/web", Workflow: "deploy.yml", Ref: "main"}); err == nil ||
		!strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("wrong token = %v", err)
	}
	noSecret := NewGitHub(config.Static(bad.cfg), fakes.NewVault(nil), bad.svc.d.HTTP, nil)
	if _, err := noSecret.RunStatus(bad.ctx, "acme/web", 1); err == nil || !strings.Contains(err.Error(), "GitHub token") {
		t.Fatalf("missing secret = %v", err)
	}
	bad.cfg.Releases.GitHubAPI = "ftp://example.com"
	if _, err := noSecret.RunStatus(bad.ctx, "acme/web", 1); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad api base = %v", err)
	}

	pub := newFixture(t)
	pub.cfg.Releases.GitHubAPI = "https://api.github.com"
	c, err := pub.svc.d.Dispatcher.(*GitHub).connect(pub.ctx)
	if err != nil || c.web != "https://github.com" || pub.dests[0].AllowPrivate || pub.dests[0].Hosts[0] != "api.github.com:443" {
		t.Fatalf("public API connection = %+v %+v, %v", c, pub.dests, err)
	}
}

func TestStatusTool(t *testing.T) {
	f := newFixture(t)
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	if _, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil {
		t.Fatal(err)
	}
	f.gh.SetRun(f.gh.Runs()[0].ID, "in_progress", "")
	st := f.tool(statusName)
	sa, err := st.Plan(f.ctx, env, json.RawMessage(`{"environment":"staging"}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.Exec(f.ctx, env, sa)
	if err != nil || res.IsError || res.Untrusted {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	for _, want := range []string{"staging: acme/web, workflow deploy.yml, refs main, needs 2 approval(s) from any admin, never the requester",
		"acme/web at main: dispatched (GitHub: in_progress), run 9001"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("status = %q, want %q", res.Content, want)
		}
	}
	if strings.Contains(res.Content, "untrusted name") || strings.Contains(res.Content, "commit message") {
		t.Fatalf("status leaked repository-authored text: %q", res.Content)
	}
	other := *env
	other.User, other.Role = f.alice.ID, core.RoleMember
	other.Principal = core.Principal{ID: core.UserPrincipal(f.alice.ID), Kind: core.PrincipalUser, User: f.alice.ID}
	res, err = st.Exec(f.ctx, &other, sa)
	if err != nil || !strings.Contains(res.Content, "no deploys yet") {
		t.Fatalf("another principal's status = %+v, %v", res, err)
	}
}

func TestTokenCheck(t *testing.T) {
	f := newFixture(t)
	check := f.svc.Checks()[0]
	if got := check.Run(f.ctx); len(got) != 0 {
		t.Fatalf("findings with a token = %+v", got)
	}
	f.svc.d.Vault = fakes.NewVault(nil)
	if got := check.Run(f.ctx); len(got) != 1 || got[0].ID != "releases.github_token" || !strings.Contains(got[0].Detail, "github_token") {
		t.Fatalf("findings without the secret = %+v", got)
	}
	f.cfg.Releases.GitHubToken = ""
	if got := check.Run(f.ctx); len(got) != 1 || !strings.Contains(got[0].Detail, "empty") {
		t.Fatalf("findings with no reference = %+v", got)
	}
	f.cfg.Releases.Environments = nil
	if got := check.Run(f.ctx); len(got) != 0 {
		t.Fatalf("findings without environments = %+v", got)
	}
}

func TestRunWakesOnDispatch(t *testing.T) {
	f := newFixture(t)
	f.cfg.Releases.PollInterval = config.Duration(time.Hour)
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	f.gh.SetLegacy(false)
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.svc.Run(ctx) }()
	if _, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil {
		t.Fatal(err)
	}
	f.gh.SetRun(f.gh.Runs()[0].ID, "completed", "success")
	f.svc.poke()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.notices()) == 0 && time.Now().Before(deadline) {
		f.svc.poke()
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := f.notices(); len(n) != 1 || !strings.Contains(n[0], "succeeded") {
		t.Fatalf("notices = %q", n)
	}
}

type failingDeploys struct{ core.ReleaseRepo }

func (failingDeploys) Create(context.Context, *core.Deploy) error { return errors.New("disk full") }

// TestDeployRecordFailure: a deploy that was started but could not be
// recorded is reported as started, with a warning that no outcome follows.
func TestDeployRecordFailure(t *testing.T) {
	f := newFixture(t)
	f.svc.d.Deploys = failingDeploys{f.svc.d.Deploys}
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	res, err := f.tool(deployName).Exec(f.ctx, env, a)
	if err != nil || res.IsError || !strings.Contains(res.Content, "could not record the deploy") || len(f.gh.Dispatches()) != 1 {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
}
