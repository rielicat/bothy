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

// approve stores the approval of env's call with the given status and votes,
// needing as many approvals as staging does now.
func (f *fixture) approve(env *core.ToolEnv, a *core.Action, st core.ApprovalStatus, voters ...*core.User) *core.Approval {
	f.t.Helper()
	req := core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: f.cfg.Releases.Environments[0].Needed(), Requester: env.User}
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
	if !strings.Contains(res.Content, "Approved by Alice and Bob") || !strings.Contains(res.Content, "Run 9001") || strings.Contains(res.Content, "evil.example") {
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
		{"approvers replaced since", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
			carol := f.user("carol", core.RoleMember)
			f.cfg.Releases.Environments[0].Approvers = []string{string(f.alice.ID), string(carol.ID)}
		}, "1 of the 2 approvals it needs now still count"},
		{"required raised since", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			f.cfg.Releases.Environments[0].Required = 1
			f.approve(env, a, core.ApprovalAllowed, f.alice)
			f.cfg.Releases.Environments[0].Required = 2
		}, "1 of the 2 approvals it needs now still count"},
		{"approver disabled since", func(f *fixture, env *core.ToolEnv, a *core.Action) {
			f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
			b := *f.bob
			b.Disabled = true
			if err := f.store.Users().Update(f.ctx, &b); err != nil {
				f.t.Fatal(err)
			}
		}, "approvers changed since it was approved"},
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

// TestDeployExecAfterApproversLoosened: votes that satisfied the stricter
// rule still satisfy a looser one, so the deploy runs.
func TestDeployExecAfterApproversLoosened(t *testing.T) {
	f := newFixture(t)
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	f.cfg.Releases.Environments[0].Required = 1
	f.cfg.Releases.Environments[0].Approvers = []string{"admin", "member"}
	if res, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil || res.IsError || len(f.gh.Dispatches()) != 1 {
		t.Fatalf("Exec = %+v, %v; dispatches %d", res, err, len(f.gh.Dispatches()))
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
	if outcome := outcomeText(deps[0], time.Hour, 0); !strings.HasPrefix(outcome, "GitHub didn't start the deploy of acme/web to staging.") {
		t.Fatalf("outcome = %q", outcome)
	}
}

func TestFollowerReportsOutcome(t *testing.T) {
	f := newFixture(t)
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	dispatched := f.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvReleaseDispatched}}, 8)
	defer dispatched.Close()
	if res, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil || res.IsError {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	id := f.gh.Runs()[0].ID
	runURL := f.gh.URL() + "/acme/web/actions/runs/9001"
	dep := f.deploys()[0]
	wantDispatched := core.ReleaseDispatchedData{Deploy: dep.ID, Environment: "staging", Repo: "acme/web", Ref: "main", URL: runURL}
	for _, p := range []core.PrincipalID{core.UserPrincipal(f.owner.ID), ""} {
		ev := <-dispatched.C()
		if ev.Principal != p || ev.Session != f.session || ev.Run != env.Run || ev.Data != wantDispatched {
			t.Fatalf("release.dispatched = %+v, want %+v to %q", ev, wantDispatched, p)
		}
	}
	sub := f.bus.Subscribe(core.Filter{System: true}, 8)
	defer sub.Close()

	f.svc.poll(f.ctx)
	if d := f.deploys()[0]; d.Status != core.DeployDispatched {
		t.Fatalf("queued run: %s", d.Status)
	}
	f.gh.SetRun(id, "in_progress", "")
	f.svc.poll(f.ctx)
	if d := f.deploys()[0]; d.Status != core.DeployRunning || len(f.notices()) != 0 {
		t.Fatalf("running: %s, notices %q", d.Status, f.notices())
	}
	f.gh.FinishRun(id, "success", 3*time.Minute, "")
	f.svc.poll(f.ctx)
	d := f.deploys()[0]
	if d.Status != core.DeploySucceeded || d.Conclusion != "success" || d.FinishedAt.IsZero() {
		t.Fatalf("finished deploy = %+v", d)
	}
	notices := f.notices()
	want := "Deployed acme/web to staging in 3 min. View it on GitHub: " + runURL
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
	wantFinished := core.ReleaseFinishedData{Deploy: d.ID, Environment: "staging", Repo: "acme/web", Ref: "main",
		Status: core.DeploySucceeded, OK: true, URL: runURL, Duration: 3 * time.Minute}
	for _, p := range []core.PrincipalID{core.UserPrincipal(f.owner.ID), ""} {
		select {
		case ev := <-sub.C():
			if ev.Type != core.EvReleaseFinished || ev.Principal != p || ev.Data != wantFinished {
				t.Fatalf("release.finished = %+v, want %+v to %q", ev, wantFinished, p)
			}
		default:
			t.Fatal("no release.finished event")
		}
	}
	f.svc.poll(f.ctx)
	if len(f.notices()) != 1 {
		t.Fatal("a finished deploy was reported twice")
	}
}

// TestFailedDeployNamesTheStep: release.finished names the step a failed
// run stopped at, cleaned to one line; the posted notice never carries it,
// since step names come from whoever can push to the repository.
func TestFailedDeployNamesTheStep(t *testing.T) {
	f := newFixture(t)
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	if _, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil {
		t.Fatal(err)
	}
	sub := f.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvReleaseFinished}}, 8)
	defer sub.Close()
	step := "Run\nmigrations ‮[Approve](https://evil.example)"
	f.gh.FinishRun(f.gh.Runs()[0].ID, "failure", 90*time.Second, step)
	f.svc.poll(f.ctx)
	ev := <-sub.C()
	got := ev.Data.(core.ReleaseFinishedData)
	if got.OK || got.Status != core.DeployFailed || got.Duration != 90*time.Second ||
		got.FailedStep != "Run migrations [Approve](https://evil.example)" {
		t.Fatalf("release.finished = %+v", got)
	}
	if n := f.notices(); len(n) != 1 || strings.Contains(n[0], "migrations") || strings.Contains(n[0], "evil") {
		t.Fatalf("notice = %q", n)
	}

	refused := newFixture(t)
	refused.gh.RejectRef("main")
	sub2 := refused.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvReleaseFinished, core.EvReleaseDispatched}}, 8)
	defer sub2.Close()
	renv := refused.env()
	ra := refused.plan(renv, `{"environment":"staging"}`)
	refused.approve(renv, ra, core.ApprovalAllowed, refused.alice, refused.bob)
	_, _ = refused.tool(deployName).Exec(refused.ctx, renv, ra)
	ev = <-sub2.C()
	if fin, ok := ev.Data.(core.ReleaseFinishedData); !ok || fin.OK || fin.Status != core.DeployFailed || fin.URL != "" {
		t.Fatalf("refused dispatch event = %+v", ev)
	}
}

func TestStepName(t *testing.T) {
	for in, want := range map[string]string{
		"  Run   migrations ":       "Run migrations",
		"a\x00b​c\td":               "abc d",
		strings.Repeat("é", 150):    strings.Repeat("é", 99) + "…",
		"Deploy to ${{ env.NAME }}": "Deploy to ${{ env.NAME }}",
	} {
		if got := stepName(in); got != want {
			t.Errorf("stepName(%q) = %q, want %q", in, got, want)
		}
	}
	for d, want := range map[time.Duration]string{0: "1 s", 42 * time.Second: "42 s", 3 * time.Minute: "3 min",
		130 * time.Second: "2 min 10 s", time.Hour: "1 h", 65 * time.Minute: "1 h 5 min"} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestFollowerEndings(t *testing.T) {
	cases := []struct {
		name   string
		act    func(f *fixture, run int64)
		status core.DeployStatus
		want   string
	}{
		{"failure", func(f *fixture, run int64) { f.gh.SetRun(run, "completed", "failure") }, core.DeployFailed, "failed (GitHub says “failure”)"},
		{"cancelled", func(f *fixture, run int64) { f.gh.SetRun(run, "completed", "cancelled") }, core.DeployFailed, "was cancelled on GitHub"},
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
		!strings.Contains(f.notices()[0], "couldn't find its run") {
		t.Fatalf("unidentified run: %+v %q", got, f.notices())
	}
}

// TestDispatchOnServerWithoutRunDetails: a GitHub Enterprise Server that
// refuses return_run_details still gets exactly one dispatch, and the run is
// then found by listing.
func TestDispatchOnServerWithoutRunDetails(t *testing.T) {
	f := newFixture(t)
	f.gh.SetStrict(true)
	run, err := f.svc.d.Dispatcher.Dispatch(f.ctx, core.WorkflowDispatch{Repo: "acme/web", Workflow: "deploy.yml", Ref: "main",
		Inputs: map[string]string{"target": "staging"}})
	if err != nil || run.ID != 9001 {
		t.Fatalf("dispatch = %+v, %v", run, err)
	}
	ds := f.gh.Dispatches()
	if len(ds) != 1 || ds[0].ReturnRunDetails || ds[0].Inputs["target"] != "staging" {
		t.Fatalf("dispatches = %+v; want one without return_run_details", ds)
	}
	f.gh.RejectRef("gone")
	if _, err := f.svc.d.Dispatcher.Dispatch(f.ctx, core.WorkflowDispatch{Repo: "acme/web", Workflow: "deploy.yml", Ref: "gone"}); err == nil ||
		!strings.Contains(err.Error(), "No ref found") {
		t.Fatalf("other 422 = %v; want it reported, not retried away", err)
	}
	if n := len(f.gh.Dispatches()); n != 1 {
		t.Fatalf("%d dispatches", n)
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
	plain := newFixture(t)
	plain.cfg.Releases.GitHubAPI = "http://ghe.example.com/api/v3"
	if _, err := plain.svc.d.Dispatcher.RunStatus(plain.ctx, "acme/web", 1); !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "https") ||
		len(plain.dests) != 0 {
		t.Fatalf("plain http to a remote API = %v (destinations %+v); want refused before any connection", err, plain.dests)
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
	if n := f.notices(); len(n) != 1 || !strings.HasPrefix(n[0], "Deployed acme/web to staging in ") {
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

// connected is a RepoDispatcher serving one repository, as the GitHub
// module's connections do.
type connected struct {
	repo       string
	dispatches []core.WorkflowDispatch
	status     string
	conclusion string
}

func (c *connected) Serves(_ context.Context, repo string) bool {
	return strings.EqualFold(repo, c.repo)
}

func (c *connected) Dispatch(_ context.Context, d core.WorkflowDispatch) (*core.WorkflowRun, error) {
	c.dispatches = append(c.dispatches, d)
	return &core.WorkflowRun{ID: 77, Repo: d.Repo, Status: "queued", URL: "https://github.com/acme/web/actions/runs/77"}, nil
}

func (c *connected) RunStatus(_ context.Context, repo string, id int64) (*core.WorkflowRun, error) {
	if id != 77 {
		return nil, core.ErrNotFound
	}
	return &core.WorkflowRun{ID: id, Repo: repo, Status: c.status, Conclusion: c.conclusion}, nil
}

// A run is followed through the dispatcher that started it, whatever the
// allowlists say meanwhile: the other dispatcher may not see the run at all.
func TestFollowerKeepsItsDispatcher(t *testing.T) {
	deploy := func(f *fixture) *core.Deploy {
		t.Helper()
		env := f.env()
		a := f.plan(env, `{"environment":"staging"}`)
		f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
		if res, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil || res.IsError {
			t.Fatalf("Exec = %+v, %v", res, err)
		}
		return f.deploys()[0]
	}

	// Started through a connection; the repository then leaves its allowlist.
	f := newFixture(t)
	conn := &connected{repo: "acme/web", status: "in_progress"}
	f.svc.d.Connections = conn
	if d := deploy(f); d.Via != core.DeployViaConnection || d.WorkflowRun != 77 {
		t.Fatalf("deploy = %+v", d)
	}
	conn.repo, conn.status, conn.conclusion = "acme/other", "completed", "success"
	before := f.gh.Requests()
	f.svc.poll(f.ctx)
	if d := f.deploys()[0]; d.Status != core.DeploySucceeded || f.gh.Requests() != before {
		t.Fatalf("deploy %+v; token client asked %d times", d, f.gh.Requests()-before)
	}

	// Started with releases.github_token; the repository then joins a
	// connection whose credentials do not know the run.
	f = newFixture(t)
	conn = &connected{repo: "acme/other", status: "completed", conclusion: "failure"}
	f.svc.d.Connections = conn
	d := deploy(f)
	if d.Via != core.DeployViaToken || d.WorkflowRun == 0 || len(conn.dispatches) != 0 {
		t.Fatalf("deploy = %+v", d)
	}
	conn.repo = "acme/web"
	f.gh.SetRun(d.WorkflowRun, "completed", "success")
	f.svc.poll(f.ctx)
	if d = f.deploys()[0]; d.Status != core.DeploySucceeded || d.Conclusion != "success" {
		t.Fatalf("deploy after the run finished = %+v", d)
	}

	// A deploy recorded before Via existed follows today's choice.
	if got := f.svc.follower(f.ctx, &core.Deploy{Repo: "acme/web"}); got != core.WorkflowDispatcher(conn) {
		t.Fatalf("legacy deploy followed by %T", got)
	}
	if got := f.svc.follower(f.ctx, &core.Deploy{Repo: "acme/x"}); got != f.svc.d.Dispatcher {
		t.Fatalf("legacy deploy of an unconnected repository followed by %T", got)
	}

	// The failed step is read through the same dispatcher.
	f = newFixture(t)
	conn = &connected{repo: "acme/other"}
	f.svc.d.Connections = conn
	d = deploy(f)
	conn.repo = "acme/web"
	sub := f.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvReleaseFinished}}, 8)
	defer sub.Close()
	f.gh.FinishRun(d.WorkflowRun, "failure", time.Minute, "Run migrations")
	f.svc.poll(f.ctx)
	if got := (<-sub.C()).Data.(core.ReleaseFinishedData); got.Status != core.DeployFailed || got.FailedStep != "Run migrations" {
		t.Fatalf("release.finished of a token deploy whose repository joined a connection = %+v", got)
	}
}

func TestConnectionsDispatchTheirRepositories(t *testing.T) {
	f := newFixture(t)
	conn := &connected{repo: "Acme/Web", status: "in_progress"}
	f.svc.d.Connections = conn
	env := f.env()
	a := f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	if res, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil || res.IsError {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	if len(conn.dispatches) != 1 || conn.dispatches[0].Inputs["target"] != "staging" || len(f.gh.Dispatches()) != 0 {
		t.Fatalf("connection dispatches %+v, token client dispatches %+v", conn.dispatches, f.gh.Dispatches())
	}
	d := f.deploys()[0]
	if d.WorkflowRun != 77 || d.RunURL != "https://github.com/acme/web/actions/runs/77" {
		t.Fatalf("deploy = %+v", d)
	}
	f.svc.poll(f.ctx)
	if d = f.deploys()[0]; d.Status != core.DeployRunning {
		t.Fatalf("deploy after the first poll = %+v", d)
	}
	conn.status, conn.conclusion = "completed", "success"
	f.svc.poll(f.ctx)
	if d = f.deploys()[0]; d.Status != core.DeploySucceeded {
		t.Fatalf("deploy after the run finished = %+v", d)
	}

	// Without a token, the doctor only minds environments no connection serves.
	f.svc.d.Vault = fakes.NewVault(nil)
	if got := f.svc.Checks()[0].Run(f.ctx); len(got) != 0 {
		t.Fatalf("findings with every repository connected = %+v", got)
	}
	conn.repo = "acme/other"
	if got := f.svc.Checks()[0].Run(f.ctx); len(got) != 1 || !strings.Contains(got[0].Detail, "staging") {
		t.Fatalf("findings with staging unconnected = %+v", got)
	}

	// A repository no connection serves goes to releases.github_token.
	f.svc.d.Vault = fakes.NewVault(map[string]string{"github_token": testToken})
	env = f.env()
	a = f.plan(env, `{"environment":"staging"}`)
	f.approve(env, a, core.ApprovalAllowed, f.alice, f.bob)
	if res, err := f.tool(deployName).Exec(f.ctx, env, a); err != nil || res.IsError {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	if len(conn.dispatches) != 1 || len(f.gh.Dispatches()) != 1 {
		t.Fatalf("connection dispatches %+v, token client dispatches %+v", conn.dispatches, f.gh.Dispatches())
	}
}
