package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func releaseHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, func(c *config.Config) {
		c.Releases.Environments = []config.ReleaseEnv{{Name: "staging", Repo: "acme/web", Workflow: "deploy.yml", RefPattern: "main",
			Approvers: []string{"admin"}, Required: 2}}
	}).setup()
	h.srv.d.Deploys = h.store.(core.ReleaseStore).Releases()
	return h
}

func (h *harness) multiApproval(requester *client, approver core.ApproverReq) *core.Approval {
	h.t.Helper()
	ap, err := h.approvals.Request(context.Background(), core.ApprovalRequest{
		Run: core.RunID(core.NewID(core.PrefixRun)), Session: core.SessionID(core.NewID(core.PrefixSession)),
		Principal: core.UserPrincipal(requester.user), ToolCallID: core.NewID(core.PrefixToolCall),
		Action: &core.Action{Tool: "release_deploy", Kind: core.ActRelease, SideEffect: core.SEIrreversible},
		Spec:   core.ToolSpec{Def: core.ToolDef{Name: "release_deploy"}},
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: approver, Choices: []core.Choice{core.AllowOnce, core.Deny},
			Timeout: 15 * time.Minute},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return ap
}

func TestApprovalVotesRoute(t *testing.T) {
	h := releaseHarness(t)
	other := h.newUser("other", core.RoleMember)
	ap := h.multiApproval(h.member, core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: h.member.user})
	path := "/api/approvals/" + string(ap.ID) + "/votes"

	rec := h.do(http.MethodGet, path, nil, h.admin)
	expect(t, rec, http.StatusOK)
	v := decodeAs[dto.ApprovalVotes](t, rec)
	if v.Approval != ap.ID || v.Status != core.ApprovalPending || v.Required != 2 || v.Approvals != 0 || len(v.Votes) != 0 ||
		v.Approvers != "any admin" || v.Requester != h.member.user || v.RequesterName != "member" || !v.CanVote || v.CanWithdraw {
		t.Fatalf("admin view = %+v", v)
	}
	v = decodeAs[dto.ApprovalVotes](t, h.do(http.MethodGet, path, nil, h.member))
	if v.CanVote || !v.CanWithdraw {
		t.Fatalf("requester view = %+v", v)
	}
	expect(t, h.do(http.MethodGet, path, nil, other), http.StatusNotFound)

	rec = h.do(http.MethodPost, "/api/approvals/"+string(ap.ID), dto.ApprovalResolveReq{Choice: core.AllowOnce}, h.admin)
	expect(t, rec, http.StatusOK)
	if got := decodeAs[core.Approval](t, rec); got.Status != core.ApprovalPending || len(got.Votes) != 1 {
		t.Fatalf("first vote = %+v", got)
	}
	v = decodeAs[dto.ApprovalVotes](t, h.do(http.MethodGet, path, nil, h.admin))
	if v.Approvals != 1 || v.YourVote != core.AllowOnce || v.CanVote || len(v.Votes) != 1 || v.Votes[0].Name != "admin" || v.Votes[0].Via != "web" {
		t.Fatalf("after voting = %+v", v)
	}
	v = decodeAs[dto.ApprovalVotes](t, h.do(http.MethodGet, path, nil, h.owner))
	if !v.CanVote || v.YourVote != "" {
		t.Fatalf("owner view = %+v", v)
	}

	single := h.multiApproval(h.member, core.ApproverReq{User: h.member.user})
	if rec := h.do(http.MethodGet, "/api/approvals/"+string(single.ID)+"/votes", nil, h.member); rec.Code != http.StatusBadRequest {
		t.Fatalf("votes of a single-approver approval = %d %s", rec.Code, rec.Body)
	}
}

func TestReleasesRoutes(t *testing.T) {
	h := releaseHarness(t)
	ctx := context.Background()
	deploys := h.store.(core.ReleaseStore).Releases()
	for i, c := range []*client{h.member, h.admin} {
		d := &core.Deploy{Environment: "staging", Repo: "acme/web", Workflow: "deploy.yml", Ref: "main",
			Principal: core.UserPrincipal(c.user), Session: core.SessionID(core.NewID(core.PrefixSession)), RequestedBy: c.user,
			Status: core.DeploySucceeded, CreatedAt: time.UnixMilli(int64(1_780_000_000_000 + i*1000))}
		if err := deploys.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	rec := h.do(http.MethodGet, "/api/releases", nil, h.member)
	expect(t, rec, http.StatusOK)
	r := decodeAs[dto.Releases](t, rec)
	if len(r.Environments) != 1 || r.Environments[0].Name != "staging" || r.Environments[0].Required != 2 ||
		len(r.Environments[0].Approvers) != 1 || r.Environments[0].Approvers[0] != (dto.ReleaseApprover{Kind: "role", ID: "admin", Name: "admin"}) ||
		r.Environments[0].Inputs == nil {
		t.Fatalf("environments = %+v", r.Environments)
	}
	if len(r.Deploys) != 1 || r.Deploys[0].RequestedBy != h.member.user {
		t.Fatalf("member sees deploys %+v; want only their own", r.Deploys)
	}
	l := decodeAs[dto.List[*core.Deploy]](t, h.do(http.MethodGet, "/api/releases/deploys?environment=staging", nil, h.admin))
	if len(l.Items) != 2 || l.Items[0].RequestedBy != h.admin.user {
		t.Fatalf("admin deploys = %+v", l.Items)
	}
	l = decodeAs[dto.List[*core.Deploy]](t, h.do(http.MethodGet, "/api/releases/deploys?environment=prod", nil, h.admin))
	if len(l.Items) != 0 {
		t.Fatalf("prod deploys = %+v", l.Items)
	}
	expect(t, h.do(http.MethodGet, "/api/releases/deploys?limit=0", nil, h.admin), http.StatusBadRequest)
	expect(t, h.do(http.MethodGet, "/api/releases", nil, nil), http.StatusUnauthorized)
}

// A user that no longer exists is named nowhere by id: no name on the
// environment's approver and left out of who can still vote.
func TestReleasesWithRemovedApprover(t *testing.T) {
	gone := core.UserID(core.NewID(core.PrefixUser))
	h := newHarness(t, func(c *config.Config) {
		c.Releases.Environments = []config.ReleaseEnv{{Name: "staging", Repo: "acme/web", Workflow: "deploy.yml", RefPattern: "main",
			Approvers: []string{string(gone), "admin"}, Required: 1}}
	}).setup()
	r := decodeAs[dto.Releases](t, h.do(http.MethodGet, "/api/releases", nil, h.admin))
	if len(r.Environments) != 1 || len(r.Environments[0].Approvers) != 2 ||
		r.Environments[0].Approvers[0] != (dto.ReleaseApprover{Kind: "user", ID: string(gone)}) {
		t.Fatalf("approvers = %+v", r.Environments)
	}

	ap := h.multiApproval(h.member, core.ApproverReq{Users: []core.UserID{gone, h.owner.user}, Required: 2, Requester: h.member.user})
	v := decodeAs[dto.ApprovalVotes](t, h.do(http.MethodGet, "/api/approvals/"+string(ap.ID)+"/votes", nil, h.owner))
	if v.Approvers != "owner" {
		t.Fatalf("approvers = %q; want only the owner", v.Approvers)
	}
}
