//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/release/actionstest"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

const relToken = "ghp_e2eReleaseTokenAAAAAAAAAAAAAAAAAAAAAA"

// relAdmin creates an admin account and signs it in.
func relAdmin(t *testing.T, owner *pltUser, username string) *pltUser {
	t.Helper()
	req := dto.UserCreateReq{Username: username, Password: pltPassword, DisplayName: strings.ToUpper(username[:1]) + username[1:], Role: core.RoleAdmin}
	if code := owner.JSON(t, http.MethodPost, "/api/users", req, nil); code != http.StatusOK {
		t.Fatalf("create admin %s = %d", username, code)
	}
	return pltLogin(t, owner.g, username, pltPassword)
}

// TestReleaseTwoApprovers: the owner asks for a staging deploy that needs two
// admins besides the requester. The owner cannot approve it, Alice's vote
// leaves it at "1 of 2", Bob's dispatches the workflow on the fake Actions
// API, and the run's outcome is posted back to the session.
func TestReleaseTwoApprovers(t *testing.T) {
	gh := actionstest.New(relToken)
	t.Cleanup(gh.Close)
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `REL-`, MatchToolResult: `.`, Text: []string{"Deploy handled."}},
		mockllm.Rule{MatchUser: `REL-DEPLOY`, ToolCalls: []core.ToolCall{pltCall("call_rel", "release_deploy", `{"environment":"staging","ref":"main"}`)}},
	)
	g := Start(t, Options{
		Config: pltYAML(llm.URL(), nil, fmt.Sprintf(`mode: multi
releases:
  github_api: %q
  poll_interval: 200ms
  environments:
    - name: staging
      repo: acme/web
      workflow: deploy.yml
      ref_pattern: main
      inputs: {target: staging}
      approvers: [admin]
      required: 2
`, gh.URL())),
		Env: map[string]string{"BOTHY_SECRET_GITHUB_TOKEN": relToken},
	})
	owner := pltOwner(t, g)
	alice, bob := relAdmin(t, owner, "alice"), relAdmin(t, owner, "bob")
	member := owner.CreateUser(t, "mallory")
	aliceEvents := pltRecord(alice.Events(t))

	sess := owner.NewSession(t)
	run := owner.Send(t, sess.ID, "REL-DEPLOY ship main to staging")
	a := owner.WaitApproval(t, run, core.ApprovalPending)
	if a.Decision.Rule != "floor:release" || !a.Decision.Floor || !a.Approver.Multi() || a.Approver.Needed() != 2 ||
		a.Approver.Requester != owner.ID() || !slices.Equal(a.Approver.Roles, []core.Role{core.RoleAdmin}) ||
		!slices.Equal(a.Card.Choices, []core.Choice{core.AllowOnce, core.Deny}) || a.Card.Risk != core.SEIrreversible {
		t.Fatalf("deploy approval = %+v / %+v", a.Decision, a.Card)
	}
	fields := map[string]string{}
	for _, f := range a.Card.Fields {
		fields[f.Label] = f.Value
	}
	if fields["Environment"] != "staging" || fields["Repository"] != "acme/web" || fields["Ref"] != "main" ||
		fields["Inputs"] != "target=staging" || fields["Approvers"] != "any admin" || fields["Requested by"] != "owner" || a.Card.Context != "" {
		t.Fatalf("card fields = %+v, context %q", fields, a.Card.Context)
	}

	ev := aliceEvents.WaitFor(t, core.EvApprovalRequested, 10*time.Second, func(e pltEvent) bool { return e.Run == run })
	if ev.Principal != core.UserPrincipal(alice.ID()) {
		t.Fatalf("alice got the card on principal %s", ev.Principal)
	}
	if got := alice.WaitApproval(t, run, core.ApprovalPending); got.ID != a.ID {
		t.Fatalf("alice's inbox shows %s, want %s", got.ID, a.ID)
	}
	if resp := member.Do(t, http.MethodGet, "/api/approvals/"+string(a.ID), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a member who cannot approve read the card: %s", resp.Status)
	}

	if resp := owner.Do(t, http.MethodPost, "/api/approvals/"+string(a.ID), dto.ApprovalResolveReq{Choice: core.AllowOnce}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("requester's own approval = %s %s, want 403", resp.Status, resp.Body)
	}
	first := alice.Resolve(t, a.ID, core.AllowOnce)
	if first.Status != core.ApprovalPending || len(first.Votes) != 1 || first.Votes[0].User != alice.ID() {
		t.Fatalf("after alice = %+v", first)
	}
	if resp := alice.Do(t, http.MethodPost, "/api/approvals/"+string(a.ID), dto.ApprovalResolveReq{Choice: core.AllowOnce}, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("alice voting twice = %s, want 409", resp.Status)
	}
	var votes dto.ApprovalVotes
	if code := bob.JSON(t, http.MethodGet, "/api/approvals/"+string(a.ID)+"/votes", nil, &votes); code != http.StatusOK {
		t.Fatalf("votes = %d", code)
	}
	if votes.Approvals != 1 || votes.Required != 2 || !votes.CanVote || votes.RequesterName != "owner" || len(votes.Votes) != 1 || votes.Votes[0].Name != "Alice" {
		t.Fatalf("bob's view of the votes = %+v", votes)
	}
	aliceEvents.WaitFor(t, core.EvApprovalVoted, 10*time.Second, func(e pltEvent) bool { return e.Run == run })
	if n := len(gh.Dispatches()); n != 0 {
		t.Fatalf("%d dispatches after one of two approvals", n)
	}

	if second := bob.Resolve(t, a.ID, core.AllowOnce); second.Status != core.ApprovalAllowed || len(second.Votes) != 2 {
		t.Fatalf("after bob = %+v", second)
	}
	d := owner.Finished(t, run)
	if d.Run.Status != core.RunDone || len(d.ToolCalls) != 1 || d.ToolCalls[0].Status != core.TCOK || d.ToolCalls[0].Rule != "floor:release" {
		t.Fatalf("run = %+v tool calls %+v", d.Run, d.ToolCalls)
	}
	if !strings.Contains(d.ToolCalls[0].ResultPreview, "approved by Alice and Bob") {
		t.Fatalf("tool result = %q", d.ToolCalls[0].ResultPreview)
	}
	disp := gh.Dispatches()
	if len(disp) != 1 || disp[0].Repo != "acme/web" || disp[0].Workflow != "deploy.yml" || disp[0].Ref != "main" || disp[0].Inputs["target"] != "staging" {
		t.Fatalf("dispatches = %+v", disp)
	}

	runID := gh.Runs()[0].ID
	gh.SetRun(runID, "in_progress", "")
	waitDeploy(t, owner, core.DeployRunning)
	gh.SetRun(runID, "completed", "success")
	dep := waitDeploy(t, owner, core.DeploySucceeded)
	if dep.WorkflowRun != runID || dep.Approval != a.ID || !slices.Equal(dep.ApprovedBy, []core.UserID{alice.ID(), bob.ID()}) ||
		dep.RequestedBy != owner.ID() || dep.Conclusion != "success" {
		t.Fatalf("deploy = %+v", dep)
	}
	want := fmt.Sprintf("Deploy to staging succeeded: acme/web at main. Run %d: %s/acme/web/actions/runs/%d", runID, gh.URL(), runID)
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Contains(owner.Notices(t, sess.ID), want) {
		if time.Now().After(deadline) {
			t.Fatalf("session notices = %q, want %q", owner.Notices(t, sess.ID), want)
		}
		time.Sleep(50 * time.Millisecond)
	}

	owner.WaitAudit(t, "approval.vote", func(e core.AuditEntry) bool { return e.Outcome == "denied" && e.Actor == "user:"+string(owner.ID()) })
	owner.WaitAudit(t, "approval.vote", func(e core.AuditEntry) bool { return e.Outcome == "ok" && e.Actor == "user:"+string(bob.ID()) })
	owner.WaitAudit(t, "release.dispatch", func(e core.AuditEntry) bool { return e.Outcome == "ok" && e.Target == dep.ID })
	owner.WaitAudit(t, "release.finish", func(e core.AuditEntry) bool { return e.Target == dep.ID })
	for _, e := range owner.Audit(t, "") {
		if strings.Contains(string(e.Detail), relToken) {
			t.Fatalf("the GitHub token reached the audit log: %+v", e)
		}
	}
}

// waitDeploy polls GET /api/releases until the newest deploy has status st.
func waitDeploy(t *testing.T, u *pltUser, st core.DeployStatus) *core.Deploy {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var r dto.Releases
		if code := u.JSON(t, http.MethodGet, "/api/releases", nil, &r); code != http.StatusOK {
			t.Fatalf("releases = %d", code)
		}
		if len(r.Deploys) > 0 && r.Deploys[0].Status == st {
			return r.Deploys[0]
		}
		if time.Now().After(deadline) {
			b, _ := json.Marshal(r.Deploys)
			t.Fatalf("no deploy with status %s within 15s: %s", st, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
