package agent

import (
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// A turn queued or scheduled for an account that was turned off since does
// not run for it.
func TestTurnedOffAccountsTurnDoesNotRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"hi"}}}})
		defer h.close()
		u, err := h.store.Users().Get(h.ctx, h.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		u.Role, u.Disabled = core.RoleMember, true
		if err := h.store.Users().Update(h.ctx, u); err != nil {
			t.Fatal(err)
		}
		h.submit("deploy main to production")
		synctest.Wait()
		if n := len(h.requests()); n != 0 {
			t.Fatalf("the model was asked %d times for a turned-off account", n)
		}
		if runs := h.runs(); len(runs) != 0 {
			t.Fatalf("runs = %+v", runs)
		}
	})
}

// An approver who did approve a request that then expired is not "nobody".
func TestExpiredTextCountsApprovalsThatCameIn(t *testing.T) {
	req := core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: "usr_sam"}
	a := &core.Approval{Approver: req, Votes: []core.ApprovalVote{{User: "usr_priya", Choice: core.AllowOnce}, {User: "usr_sam", Choice: core.AllowOnce}}}
	if got := expiredText(a, approvalExpiry, "this action did not run."); got != "Only 1 of the 2 approvals needed came in before the request expired, so this action did not run." {
		t.Fatalf("partly approved = %q", got)
	}
	a.Votes = nil
	if got := expiredText(a, approvalExpiry, "x"); got != approvalExpiry {
		t.Fatalf("unanswered = %q", got)
	}
	if got := expiredText(&core.Approval{Approver: core.ApproverReq{User: "usr_sam"}}, approvalExpiry, "x"); got != approvalExpiry {
		t.Fatalf("single approver = %q", got)
	}
}
