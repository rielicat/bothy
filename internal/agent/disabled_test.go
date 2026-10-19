package agent

import (
	"encoding/json"
	"fmt"
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

// A task stopped while it waited for an approval doesn't go on saying it
// waits for one.
func TestStopWhileWaitingEndsTheWaitingStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := newHarness(t, opts{tools: []core.Tool{act}, pol: &fakes.Policy{Verdicts: map[string]core.Verdict{"act": core.VerdictAsk}},
			appr: &fakes.Approvals{Manual: true}, steps: []fakes.LLMStep{{ToolCalls: []core.ToolCall{call("act", `{}`)}}}})
		defer h.close()
		res := h.submit("deploy main to production")
		synctest.Wait()
		if _, err := h.r.Stop(h.ctx, ownerViewer(h), h.sess.ID, h.user.ID); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		recs := h.toolCalls(res.Run)
		if len(recs) != 1 || recs[0].Status != core.TCStopped {
			t.Fatalf("tool calls after Stop = %+v", recs)
		}
	})
}

// "Allow for this schedule" on a scheduled run's card covers the schedule:
// its next run starts in a new chat, where a chat grant would not reach.
func TestScheduledAllowSessionCoversTheSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{})
		defer h.close()
		rs := &runState{r: h.r, ctx: h.ctx, id: "run_1", req: core.RunRequest{Session: h.sess.ID, Principal: h.pid, User: h.user.ID,
			Trigger: core.TrigSchedule, Schedule: "sch_digest"}}
		if err := h.r.loadRunContext(h.ctx, rs); err != nil {
			t.Fatal(err)
		}
		action := &core.Action{Tool: "act", SideEffect: core.SELocalWrite, Payload: json.RawMessage(`{}`)}
		pc := &pcall{action: action, dec: core.Decision{Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}}}
		h.r.grantFor(h.ctx, rs, pc, core.AllowSession, nil)
		if len(h.pol.Grants) != 1 || h.pol.Grants[0].Scope != core.GrantSchedule || h.pol.Grants[0].ScopeID != "sch_digest" {
			t.Fatalf("grants = %+v, want one for the schedule", h.pol.Grants)
		}
	})
}

// A call refused for its arguments keeps what was asked, so its step reads
// "Couldn't get acme/web", not "Couldn't get".
func TestInvalidCallKeepsWhatWasAsked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tool := &testTool{name: "repo_checkout", planErr: fmt.Errorf("%w: the checkout has local changes", core.ErrInvalid)}
		h := newHarness(t, opts{tools: []core.Tool{tool}, steps: []fakes.LLMStep{
			{ToolCalls: []core.ToolCall{call("repo_checkout", `{"repo":"acme/web"}`)}}, {Text: []string{"done"}}}})
		defer h.close()
		res := h.submit("fix the typo")
		synctest.Wait()
		recs := h.toolCalls(res.Run)
		if len(recs) != 1 || recs[0].Status != core.TCInvalid || recs[0].Action == nil || string(recs[0].Action.Payload) != `{"repo":"acme/web"}` {
			t.Fatalf("records = %+v", recs)
		}
	})
}
