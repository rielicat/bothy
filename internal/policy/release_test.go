package policy

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

var stagingInputs = map[string]string{"target": "staging"}

// releaseHarness adds two admins and a staging environment that needs two
// admins (any admin, owners included) besides the requester.
type releaseHarness struct {
	*harness
	admin1, admin2 *core.User
}

func newReleaseHarness(t *testing.T) *releaseHarness {
	t.Helper()
	h := newHarness(t, func(c *config.Config) {
		c.Policy.Rules = nil
		c.Releases.Environments = []config.ReleaseEnv{{
			Name: "staging", Repo: "acme/web", Workflow: "deploy.yml", RefPattern: "main",
			Inputs: map[string]string{"target": "staging"}, Approvers: []string{"admin"}, Required: 2,
		}}
	})
	return &releaseHarness{harness: h, admin1: h.user("alice", core.RoleAdmin), admin2: h.user("bob", core.RoleAdmin)}
}

func releaseAction(env, ref string, inputs map[string]string, mods ...func(map[string]any)) *core.Action {
	p := map[string]any{"environment": env, "repo": "acme/web", "workflow": "deploy.yml", "ref": ref, "inputs": inputs}
	for _, m := range mods {
		m(p)
	}
	return action(toolReleaseDeploy, core.ActRelease, core.SEIrreversible, p)
}

func releaseSpec(mods ...func(*core.Manifest)) core.ToolSpec {
	return spec(toolReleaseDeploy, core.SEIrreversible, append([]func(*core.Manifest){func(m *core.Manifest) { m.SpaceOK = false }}, mods...)...)
}

// TestReleaseDecisionTable is the release_deploy row of the §5.8 table: an
// ask floor that no preset, default, rule or grant relaxes, denied outside
// interactive runs of a user's own principal.
func TestReleaseDecisionTable(t *testing.T) {
	h := newReleaseHarness(t)
	gid := h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantTool, Tool: toolReleaseDeploy,
		Pattern: map[string]string{"/environment": "staging"}, MaxSE: core.SEIrreversible})
	standard := h.space
	standard.ToolProfile = "standard"

	for _, tc := range []struct {
		name    string
		in      func(*core.PolicyInput)
		cfg     func(*config.Config)
		verdict core.Verdict
		rule    string
		reason  string
		warn    string
	}{
		// Every approver reads the reason, so it must not say "you".
		{name: "interactive owner", verdict: core.VerdictAsk, rule: floorRelease,
			reason: "deploy to staging: 2 approvers other than the person who asked must approve"},
		{name: "interactive member", in: func(in *core.PolicyInput) {
			in.User, in.Role, in.Principal, in.Session = h.member.ID, core.RoleMember, h.memberP, h.memberSession
		}, verdict: core.VerdictAsk, rule: floorRelease},
		{name: "schedule", in: func(in *core.PolicyInput) { in.Trigger = core.TrigSchedule }, verdict: core.VerdictDeny, rule: floorRelease, reason: "not from a schedule"},
		{name: "heartbeat", in: func(in *core.PolicyInput) { in.Trigger = core.TrigHeartbeat }, verdict: core.VerdictDeny, rule: floorRelease},
		{name: "api", in: func(in *core.PolicyInput) { in.Trigger = core.TrigAPI }, verdict: core.VerdictDeny, rule: floorRelease, reason: "API request"},
		{name: "manifest limits triggers", in: func(in *core.PolicyInput) {
			in.Trigger = core.TrigSchedule
			in.Spec = releaseSpec(func(m *core.Manifest) { m.Triggers = []core.Trigger{core.TrigMessage} })
		}, verdict: core.VerdictDeny, rule: "floor:unavailable", reason: "Bothy can't deploy acme/web to staging from a schedule"},
		{name: "hands_off preset", cfg: func(c *config.Config) { c.Policy.Preset = "hands_off" }, verdict: core.VerdictAsk, rule: floorRelease},
		{name: "balanced preset", cfg: func(c *config.Config) { c.Policy.Preset = "balanced" }, verdict: core.VerdictAsk, rule: floorRelease},
		{name: "irreversible default auto", cfg: func(c *config.Config) { c.Policy.Defaults = map[string]string{"irreversible": "auto"} },
			verdict: core.VerdictAsk, rule: floorRelease},
		{name: "auto rule", cfg: func(c *config.Config) {
			c.Policy.Rules = []config.Rule{{ID: "ship-it", Tool: "release_*", Verdict: "auto"}}
		}, verdict: core.VerdictAsk, rule: floorRelease},
		{name: "ask rule", cfg: func(c *config.Config) {
			c.Policy.Rules = []config.Rule{{ID: "careful", Tool: toolReleaseDeploy, Verdict: "ask"}}
		}, verdict: core.VerdictAsk, rule: floorRelease},
		{name: "deny rule", cfg: func(c *config.Config) {
			c.Policy.Rules = []config.Rule{{ID: "freeze", Tool: toolReleaseDeploy, Match: map[string]string{"/environment": "staging"}, Verdict: "deny", Reason: "release freeze"}}
		}, verdict: core.VerdictDeny, rule: "rule:freeze", reason: "release freeze"},
		{name: "exposed session", in: func(in *core.PolicyInput) { in.Taint = core.TaintPrivate | core.TaintUntrusted },
			verdict: core.VerdictAsk, rule: floorRelease, warn: "private information and content from outside"},
		{name: "untrusted origin", in: func(in *core.PolicyInput) { in.Provenance = core.ProvUntrusted },
			verdict: core.VerdictAsk, rule: floorRelease, warn: "not from a person"},
		{name: "standard space", in: func(in *core.PolicyInput) { in.Principal, in.Session = standard, h.spaceSession },
			verdict: core.VerdictDeny, rule: floorRelease, reason: "group"},
		{name: "restricted space", in: func(in *core.PolicyInput) { in.Principal, in.Session = h.space, h.spaceSession },
			verdict: core.VerdictDeny, rule: "floor:unavailable", reason: "Bothy can't deploy acme/web to staging in this group chat"},
		{name: "unknown environment", in: func(in *core.PolicyInput) { in.Action = releaseAction("dev", "main", stagingInputs) },
			verdict: core.VerdictDeny, rule: floorRelease, reason: "there's no environment called “dev”"},
		{name: "ref outside the pattern", in: func(in *core.PolicyInput) { in.Action = releaseAction("staging", "feature/x", stagingInputs) },
			verdict: core.VerdictDeny, rule: floorRelease, reason: "the staging environment doesn't deploy feature/x"},
		{name: "repository differs", in: func(in *core.PolicyInput) {
			in.Action = releaseAction("staging", "main", stagingInputs, func(p map[string]any) { p["repo"] = "evil/web" })
		}, verdict: core.VerdictDeny, rule: floorRelease, reason: "doesn't deploy the repository evil/web"},
		{name: "workflow differs", in: func(in *core.PolicyInput) {
			in.Action = releaseAction("staging", "main", stagingInputs, func(p map[string]any) { p["workflow"] = "other.yml" })
		}, verdict: core.VerdictDeny, rule: floorRelease, reason: "doesn't use the workflow other.yml"},
		{name: "inputs differ", in: func(in *core.PolicyInput) {
			in.Action = releaseAction("staging", "main", map[string]string{"target": "prod"})
		}, verdict: core.VerdictDeny, rule: floorRelease, reason: "inputs"},
		{name: "requester is not counted", in: func(in *core.PolicyInput) {
			in.User, in.Role, in.Principal = h.admin1.ID, core.RoleAdmin, h.principal(core.UserPrincipal(h.admin1.ID))
		}, verdict: core.VerdictAsk, rule: floorRelease},
		{name: "not enough approvers", cfg: func(c *config.Config) { c.Releases.Environments[0].Approvers = []string{"owner"} },
			verdict: core.VerdictDeny, rule: floorRelease, reason: "nobody else can approve it"},
		{name: "one approver short", cfg: func(c *config.Config) {
			c.Releases.Environments[0].Approvers = []string{string(h.admin1.ID), string(h.owner.ID)}
		}, verdict: core.VerdictDeny, rule: floorRelease, reason: "only 1 other person"},
		{name: "named approvers", cfg: func(c *config.Config) {
			c.Releases.Environments[0].Approvers = []string{string(h.admin1.ID), string(h.member.ID)}
		}, verdict: core.VerdictAsk, rule: floorRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := *h.cfg
			saved.Releases.Environments = slices.Clone(h.cfg.Releases.Environments)
			saved.Releases.Environments[0].Approvers = slices.Clone(h.cfg.Releases.Environments[0].Approvers)
			defer func() { *h.cfg = saved }()
			if tc.cfg != nil {
				tc.cfg(h.cfg)
			}
			in := h.in(releaseAction("staging", "main", stagingInputs), releaseSpec())
			if tc.in != nil {
				tc.in(&in)
			}
			d := h.decide(in)
			if d.Verdict != tc.verdict || d.Rule != tc.rule || !strings.Contains(d.Reason, tc.reason) {
				t.Fatalf("decision = %s %s %q, want %s %s containing %q", d.Verdict, d.Rule, d.Reason, tc.verdict, tc.rule, tc.reason)
			}
			if tc.warn != "" && !slices.ContainsFunc(d.Warnings, func(w string) bool { return strings.Contains(w, tc.warn) }) {
				t.Errorf("warnings = %q, want one containing %q", d.Warnings, tc.warn)
			}
			if d.Verdict != core.VerdictAsk {
				return
			}
			env, _ := h.cfg.Releases.Env("staging")
			users, roles := env.ApproverSet()
			want := core.ApproverReq{Users: users, Roles: roles, Required: 2, Requester: in.User}
			if !d.Floor || !slices.Equal(d.Choices, []core.Choice{core.AllowOnce, core.Deny}) ||
				fmt.Sprint(d.Approver) != fmt.Sprint(want) || d.Timeout != 15*time.Minute || d.OnTimeout != core.TimeoutDeny {
				t.Errorf("ask = %+v, want a floor with only allow_once/deny and approver %+v", d, want)
			}
		})
	}
	gs, err := h.store.Grants().List(h.ctx, core.SystemViewer(), core.GrantPrincipal, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.ID == gid && g.Uses != 0 {
			t.Errorf("a grant answered a deploy %d times", g.Uses)
		}
	}
	if err := h.store.Meta().Set(h.ctx, metaPaused, "1"); err != nil {
		t.Fatal(err)
	}
	if d := h.decide(h.in(releaseAction("staging", "main", stagingInputs), releaseSpec())); d.Verdict != core.VerdictDeny || d.Rule != "floor:paused" {
		t.Errorf("paused: %+v", d)
	}
}

// requestRelease files the approval the engine asks for when requester (a
// user principal) deploys to staging.
func (h *releaseHarness) requestRelease(requester *core.User, mod ...func(*core.Decision)) *core.Approval {
	h.t.Helper()
	p := h.principal(core.UserPrincipal(requester.ID))
	sess := h.newSession(p.ID)
	a, s := releaseAction("staging", "main", stagingInputs), releaseSpec()
	in := h.in(a, s)
	in.User, in.Role, in.Principal, in.Session = requester.ID, requester.Role, p, sess
	d := h.decide(in)
	if d.Verdict != core.VerdictAsk {
		h.t.Fatalf("expected ask, got %+v", d)
	}
	for _, m := range mod {
		m(&d)
	}
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{
		Run: h.newRun(sess, p.ID, requester.ID), Session: sess, Principal: p.ID, ToolCallID: core.NewID(core.PrefixToolCall),
		Action: a, Spec: s, Decision: d, Context: "Web · session",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return ap
}

func (h *releaseHarness) vote(id core.ApprovalID, u *core.User, c core.Choice) (*core.Approval, error) {
	return h.ap.Resolve(h.ctx, core.Resolution{ID: id, Choice: c, By: u.ID, ByRole: u.Role, Via: "web"})
}

func TestReleaseCard(t *testing.T) {
	h := newReleaseHarness(t)
	ap := h.requestRelease(h.member)
	want := []core.CardField{
		{Label: "Environment", Value: "staging"},
		{Label: "Version", Value: "main", Mono: true},
		{Label: "Workflow", Value: "deploy.yml", Mono: true},
		{Label: "Inputs", Value: "target=staging", Mono: true},
	}
	c := ap.Card
	if !slices.Equal(c.Fields, want) || c.Risk != core.SEIrreversible || c.Title != "Run release_deploy" ||
		!slices.Equal(c.Choices, []core.Choice{core.AllowOnce, core.Deny}) || c.Code != "" {
		t.Fatalf("card = %+v", c)
	}
	if c.Question != "Deploy acme/web to staging?" || c.Consequence != "This updates staging for everyone using it." ||
		c.PrimaryLabel != "Approve deploy" || c.Ing != "deploying acme/web to staging" || c.Kind != core.CardDeploy || c.Tone != core.ToneDefault {
		t.Errorf("plain copy = %+v; a deploy's safeguard is its approvers, so it is not destructive", c)
	}
	// The requester's session title is theirs; approvers see who asked instead.
	if c.Context != "" {
		t.Errorf("a multi-approver card carries the requester's session context %q", c.Context)
	}
	wantTally := &core.ApprovalTally{Required: 2, Votes: []core.ApprovalBallot{}, Eligible: []core.ApprovalPerson{}, AnyAdmin: true}
	if !reflect.DeepEqual(ap.Approvals, wantTally) || ap.Requester == nil || *ap.Requester != (core.ApprovalPerson{User: h.member.ID, Name: "member"}) {
		t.Errorf("approvals %+v requester %+v", ap.Approvals, ap.Requester)
	}

	h.cfg.Releases.Environments[0].Approvers = []string{string(h.admin1.ID), "‮bob", "member"}
	h.cfg.Releases.Environments[0].Required = 1
	carol := h.user("carol", core.RoleMember)
	if err := h.store.Users().Update(h.ctx, &core.User{ID: h.member.ID, Username: "member", DisplayName: "Mel ‮M", Role: core.RoleMember}); err != nil {
		t.Fatal(err)
	}
	ap = h.requestRelease(h.member)
	wantTally = &core.ApprovalTally{Required: 1, Votes: []core.ApprovalBallot{},
		Eligible: []core.ApprovalPerson{{User: carol.ID, Name: "carol"}}, AnyAdmin: true}
	if !reflect.DeepEqual(ap.Approvals, wantTally) || ap.Requester.Name != "Mel ‮M" {
		t.Errorf("approvals %+v requester %+v; admins are covered by any_admin, names stay verbatim for renderers", ap.Approvals, ap.Requester)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, By: carol.ID, ByRole: carol.Role, Via: "web",
		Comment: "Not before the freeze ends"}); err != nil {
		t.Fatal(err)
	}
	got, err := h.ap.Get(h.ctx, core.SystemViewer(), ap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Comment != "Not before the freeze ends" || len(got.Approvals.Votes) != 1 ||
		got.Approvals.Votes[0] != (core.ApprovalBallot{User: carol.ID, Name: "carol", Choice: core.Deny, At: got.Votes[0].At, Comment: "Not before the freeze ends"}) {
		t.Errorf("denied approval = comment %q tally %+v", got.Comment, got.Approvals)
	}
}

// TestTwoPersonRule: the requester cannot approve their own deploy, k
// distinct eligible approvers must allow, a repeat vote is refused and every
// vote is audited.
func TestTwoPersonRule(t *testing.T) {
	h := newReleaseHarness(t)
	var log statusLog
	h.ap.OnResolved(log.add)
	sub := h.bus.Subscribe(core.Filter{System: true}, 64)
	defer sub.Close()
	ap := h.requestRelease(h.admin1)

	var requested []core.PrincipalID
	for len(sub.C()) > 0 {
		if ev := <-sub.C(); ev.Type == core.EvApprovalRequested {
			requested = append(requested, ev.Principal)
		}
	}
	slices.Sort(requested)
	wantP := []core.PrincipalID{core.UserPrincipal(h.owner.ID), core.UserPrincipal(h.admin1.ID), core.UserPrincipal(h.admin2.ID)}
	slices.Sort(wantP)
	if !slices.Equal(requested, wantP) {
		t.Errorf("approval.requested went to %v, want the requester and each eligible approver %v", requested, wantP)
	}

	if _, err := h.vote(ap.ID, h.admin1, core.AllowOnce); !errors.Is(err, core.ErrForbidden) || !strings.Contains(err.Error(), selfApproval) {
		t.Fatalf("requester's own allow = %v, want forbidden", err)
	}
	if _, err := h.vote(ap.ID, h.member, core.AllowOnce); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("a member's allow = %v, want forbidden", err)
	}
	if _, err := h.vote(ap.ID, h.admin2, core.AllowSession); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("allow_session vote = %v, want invalid", err)
	}
	got, err := h.vote(ap.ID, h.admin2, core.AllowOnce)
	if err != nil || got.Status != core.ApprovalPending || len(got.Votes) != 1 || got.Votes[0].User != h.admin2.ID {
		t.Fatalf("first vote = %+v, %v; want pending with one vote", got, err)
	}
	// The API shows these words to the person who pressed the button.
	if _, err := h.vote(ap.ID, h.admin2, core.AllowOnce); !errors.Is(err, core.ErrConflict) || err.Error() != "conflict: you already voted" {
		t.Fatalf("second vote by the same approver = %v, want conflict", err)
	}
	if st := h.status(ap.ID).Status; st != core.ApprovalPending {
		t.Fatalf("status after one of two = %s", st)
	}
	viaGet, err := h.ap.Get(h.ctx, core.Viewer{User: h.owner.ID, Principals: []core.PrincipalID{h.ownerP.ID}, Admin: true}, ap.ID)
	if err != nil || len(viaGet.Votes) != 1 {
		t.Fatalf("an eligible approver's Get = %+v, %v", viaGet, err)
	}
	if _, err := h.ap.Get(h.ctx, core.Viewer{User: h.member.ID, Principals: []core.PrincipalID{h.memberP.ID}}, ap.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("a non-approver's Get = %v, want not found", err)
	}
	if len(log.list()) != 0 {
		t.Fatalf("a partial vote ran OnResolved: %v", log.list())
	}

	got, err = h.vote(ap.ID, h.owner, core.AllowOnce)
	if err != nil || got.Status != core.ApprovalAllowed || got.Choice != core.AllowOnce || got.DecidedBy != h.owner.ID || len(got.Votes) != 2 {
		t.Fatalf("second approver = %+v, %v; want allowed", got, err)
	}
	if c, err := h.ap.Await(h.ctx, ap.ID); err != nil || c != core.AllowOnce {
		t.Fatalf("Await = %s, %v", c, err)
	}
	if !slices.Equal(log.list(), []core.ApprovalStatus{core.ApprovalAllowed}) {
		t.Fatalf("OnResolved = %v", log.list())
	}
	if _, err := h.vote(ap.ID, h.member, core.Deny); !errors.Is(err, core.ErrConflict) || err.Error() != "conflict: already decided" {
		t.Fatalf("vote after resolution = %v", err)
	}

	var voted, decided, refused int
	for _, e := range h.audit.All() {
		switch {
		case e.Action == "approval.vote" && e.Outcome == "ok":
			voted++
		case e.Action == "approval.vote" && e.Outcome == "denied":
			refused++
		case e.Action == "approval.decide" && e.Target == string(ap.ID):
			decided++
			if !strings.Contains(string(e.Detail), string(h.admin2.ID)) || !strings.Contains(string(e.Detail), string(h.owner.ID)) {
				t.Errorf("decide audit names no voters: %s", e.Detail)
			}
		}
	}
	if voted != 2 || refused != 2 || decided != 1 {
		t.Errorf("audit: %d votes, %d refused, %d decisions; want 2, 2, 1", voted, refused, decided)
	}
	var votedEvents int
	for len(sub.C()) > 0 {
		ev := <-sub.C()
		if ev.Type == core.EvApprovalVoted && ev.Data.(core.ApprovalData).Approval.ID == ap.ID {
			votedEvents++
			if n := len(ev.Data.(core.ApprovalData).Approval.Votes); n != 1 {
				t.Errorf("approval.voted carries %d votes", n)
			}
		}
	}
	if votedEvents != 3 {
		t.Errorf("approval.voted copies = %d, want one per audience principal", votedEvents)
	}
}

func TestReleaseDenyShortCircuits(t *testing.T) {
	h := newReleaseHarness(t)
	ap := h.requestRelease(h.member)
	if got, err := h.vote(ap.ID, h.admin1, core.AllowOnce); err != nil || got.Status != core.ApprovalPending {
		t.Fatalf("allow = %+v, %v", got, err)
	}
	got, err := h.vote(ap.ID, h.admin2, core.Deny)
	if err != nil || got.Status != core.ApprovalDenied || got.DecidedBy != h.admin2.ID {
		t.Fatalf("deny = %+v, %v; want denied at once", got, err)
	}
	if c, err := h.ap.Await(h.ctx, ap.ID); err != nil || c != core.Deny {
		t.Fatalf("Await = %s, %v", c, err)
	}

	withdrawn := h.requestRelease(h.member)
	if got, err := h.vote(withdrawn.ID, h.member, core.Deny); err != nil || got.Status != core.ApprovalDenied {
		t.Fatalf("requester withdrawing = %+v, %v", got, err)
	}
}

// TestDisabledVoterNoLongerCounts: eligibility is checked when the
// deciding vote is counted, not only when each vote is cast.
func TestDisabledVoterNoLongerCounts(t *testing.T) {
	h := newReleaseHarness(t)
	ap := h.requestRelease(h.member)
	if _, err := h.vote(ap.ID, h.admin1, core.AllowOnce); err != nil {
		t.Fatal(err)
	}
	a1 := *h.admin1
	a1.Disabled = true
	if err := h.store.Users().Update(h.ctx, &a1); err != nil {
		t.Fatal(err)
	}
	got, err := h.vote(ap.ID, h.admin2, core.AllowOnce)
	if err != nil || got.Status != core.ApprovalPending || core.AllowVotes(got.Votes, got.Approver) != 2 {
		t.Fatalf("after a voter was disabled = %+v, %v; want still pending", got, err)
	}
	if _, err := h.vote(ap.ID, h.admin1, core.Deny); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("a disabled user's vote = %v", err)
	}
	if got, err := h.vote(ap.ID, h.owner, core.AllowOnce); err != nil || got.Status != core.ApprovalAllowed {
		t.Fatalf("third approver = %+v, %v", got, err)
	}
}

func TestReleaseVoteFromChat(t *testing.T) {
	h := newReleaseHarness(t)
	ap := h.requestRelease(h.member)
	card := core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "100", Kind: core.ChatDM}, ID: "7"}
	short := core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "200", Kind: core.ChatDM}, ID: "8"}
	if err := h.ap.AddMessageRef(h.ctx, ap.ID, card, false); err != nil {
		t.Fatal(err)
	}
	if err := h.ap.AddMessageRef(h.ctx, ap.ID, short, true); err != nil {
		t.Fatal(err)
	}
	press := func(u *core.User, c core.Choice, msg *core.MsgRef) error {
		_, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: c, By: u.ID, ByRole: u.Role, Via: "telegram", ViaMsg: msg})
		return err
	}
	stray := core.MsgRef{Chat: card.Chat, ID: "99"}
	if err := press(h.admin1, core.AllowOnce, &stray); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("press on an unrecorded message = %v", err)
	}
	if err := press(h.admin1, core.AllowOnce, nil); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("press without a message = %v", err)
	}
	if err := press(h.admin1, core.AllowOnce, &short); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("allow from a truncated card = %v", err)
	}
	if err := press(h.admin1, core.AllowOnce, &card); err != nil {
		t.Fatalf("allow from the card = %v", err)
	}
	if err := press(h.admin2, core.Deny, &short); err != nil {
		t.Fatalf("deny from a truncated card = %v", err)
	}
	if a := h.status(ap.ID); a.Status != core.ApprovalDenied || a.DecidedVia != "telegram" {
		t.Fatalf("status = %s via %s", a.Status, a.DecidedVia)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, By: h.owner.ID, Via: "timeout"}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("resolution after deny = %v", err)
	}
}

// TestConcurrentVotes runs every eligible approver's vote at once: exactly
// one resolution, exactly k counted allows, every other vote refused.
func TestConcurrentVotes(t *testing.T) {
	h := newReleaseHarness(t)
	voters := []*core.User{h.admin1, h.admin2, h.owner}
	for i := range 17 {
		voters = append(voters, h.user(fmt.Sprintf("admin%d", i+3), core.RoleAdmin))
	}
	for round := range 3 {
		var log statusLog
		h.ap.callbacks = nil
		h.ap.OnResolved(log.add)
		ap := h.requestRelease(h.member)
		var wg sync.WaitGroup
		errs := make([]error, len(voters))
		for i, u := range voters {
			c := core.AllowOnce
			if round == 2 && i == len(voters)-1 {
				c = core.Deny
			}
			wg.Go(func() { _, errs[i] = h.vote(ap.ID, u, c) })
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case !errors.Is(err, core.ErrConflict):
				t.Fatalf("round %d: vote failed: %v", round, err)
			}
		}
		a, err := h.ap.Get(h.ctx, core.SystemViewer(), ap.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := log.list(); len(got) != 1 || got[0] != a.Status {
			t.Fatalf("round %d: OnResolved = %v, status %s; want exactly one resolution", round, got, a.Status)
		}
		denies := slices.IndexFunc(a.Votes, func(v core.ApprovalVote) bool { return v.Choice == core.Deny }) >= 0
		switch a.Status {
		case core.ApprovalAllowed:
			if ok != 2 || len(a.Votes) != 2 || core.AllowVotes(a.Votes, a.Approver) != 2 || denies {
				t.Fatalf("round %d: allowed with %d accepted votes %+v", round, ok, a.Votes)
			}
		case core.ApprovalDenied:
			if !denies || ok != len(a.Votes) || core.AllowVotes(a.Votes, a.Approver) >= 2 {
				t.Fatalf("round %d: denied with votes %+v", round, a.Votes)
			}
		default:
			t.Fatalf("round %d: status %s", round, a.Status)
		}
	}
}

func TestMultiApprovalTimeouts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReleaseHarness(t)
		ap := h.requestRelease(h.member)
		if _, err := h.vote(ap.ID, h.admin1, core.AllowOnce); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ap.Await(h.ctx, ap.ID); !errors.Is(err, core.ErrApprovalExpired) {
			t.Fatalf("Await = %v, want expired", err)
		}
		a, err := h.ap.Get(h.ctx, core.SystemViewer(), ap.ID)
		if err != nil || a.Status != core.ApprovalExpired || len(a.Votes) != 1 {
			t.Fatalf("after timeout: %+v, %v; want expired with its vote kept", a, err)
		}
		if _, err := h.vote(ap.ID, h.admin2, core.AllowOnce); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("vote after expiry = %v", err)
		}

		parked := h.requestRelease(h.member, func(d *core.Decision) { d.OnTimeout = core.TimeoutPark })
		if _, err := h.vote(parked.ID, h.admin1, core.AllowOnce); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ap.Await(h.ctx, parked.ID); !errors.Is(err, core.ErrParked) {
			t.Fatalf("Await = %v, want parked", err)
		}
		got, err := h.vote(parked.ID, h.admin2, core.AllowOnce)
		if err != nil || got.Status != core.ApprovalAllowed {
			t.Fatalf("vote on a parked approval = %+v, %v; want allowed", got, err)
		}
	})
}

func TestMultiApproverRequestValidation(t *testing.T) {
	h := newReleaseHarness(t)
	a, s := releaseAction("staging", "main", stagingInputs), releaseSpec()
	run := h.newRun(h.memberSession, h.memberP.ID, h.member.ID)
	for _, approver := range []core.ApproverReq{
		{Roles: []core.Role{core.RoleAdmin}, Required: 2},
		{Roles: []core.Role{core.RoleAdmin}, User: h.member.ID, Requester: h.member.ID},
		{Users: []core.UserID{h.admin1.ID}, Required: -1, Requester: h.member.ID},
	} {
		_, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: h.memberSession, Principal: h.memberP.ID, Action: a, Spec: s,
			Decision: core.Decision{Verdict: core.VerdictAsk, Approver: approver, Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}}})
		if !errors.Is(err, core.ErrInvalid) {
			t.Errorf("approver %+v accepted: %v", approver, err)
		}
	}
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: h.memberSession, Principal: h.memberP.ID, Action: a, Spec: s,
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Requester: h.member.ID},
			Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny}}})
	if err != nil || !slices.Equal(ap.Decision.Choices, []core.Choice{core.AllowOnce, core.Deny}) {
		t.Fatalf("grant choices survived on a multi-approver card: %+v, %v", ap, err)
	}
}

func TestApproverReqEligible(t *testing.T) {
	r := core.ApproverReq{Users: []core.UserID{"usr_a"}, Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: "usr_r"}
	for _, c := range []struct {
		u    core.UserID
		role core.Role
		want bool
	}{
		{"usr_a", core.RoleMember, true},
		{"usr_b", core.RoleAdmin, true},
		{"usr_c", core.RoleOwner, true},
		{"usr_d", core.RoleMember, false},
		{"usr_r", core.RoleOwner, false},
		{"", core.RoleOwner, false},
		{"usr_e", "", false},
	} {
		if got := r.Eligible(c.u, c.role); got != c.want {
			t.Errorf("Eligible(%s, %s) = %v", c.u, c.role, got)
		}
	}
	if (core.ApproverReq{Roles: []core.Role{core.RoleMember}, AdminOnly: true}).Multi() {
		t.Error("an admin-only approval is not multi-approver")
	}
	if (core.ApproverReq{Users: []core.UserID{"usr_a"}}).Needed() != 1 || r.Needed() != 2 {
		t.Error("Needed")
	}
	votes := []core.ApprovalVote{{User: "usr_a", Choice: core.AllowOnce}, {User: "usr_a", Choice: core.AllowOnce},
		{User: "usr_r", Choice: core.AllowOnce}, {User: "usr_b", Choice: core.Deny}}
	if n := core.AllowVotes(votes, r); n != 1 {
		t.Errorf("AllowVotes = %d, want 1 (one user, requester and denies excluded)", n)
	}
}
