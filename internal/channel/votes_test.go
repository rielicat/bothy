package channel

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/core"
)

// TestLegacyVoteCardRefreshes: a multi-approver card delivered to chat
// before chat approvals moved to the web still shows the new count.
func TestLegacyVoteCardRefreshes(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		ap := h.request(core.UserPrincipal(h.member.ID), core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: h.member.ID}, nil)
		if len(h.ch.SentMessages()) != 0 {
			t.Fatalf("a vote card went to chat: %+v", h.ch.SentMessages())
		}
		ref := core.MsgRef{Chat: h.dmChat(ownerChat), ID: "legacy-1"}
		if err := h.approvals.AddMessageRef(h.ctx, ap.ID, ref, false); err != nil {
			t.Fatal(err)
		}
		ap.Votes = []core.ApprovalVote{{Approval: ap.ID, User: h.owner.ID, Choice: core.AllowOnce}}
		ap.Approvals = &core.ApprovalTally{Required: 2, AnyAdmin: true}
		h.bus.Publish(core.Event{Type: core.EvApprovalVoted, Data: core.ApprovalData{Approval: ap}})
		synctest.Wait()
		edits := h.ch.EditedMessages()
		if len(edits) != 1 || !strings.Contains(edits[0].Text, "**Approvals:** 1 of 2 approvals") {
			t.Fatalf("legacy card edits = %+v", edits)
		}
	})
}

// TestApprovalLinesKeepReasonsOnOneLine: a voter's reason stays on its
// vote's line, so it cannot pass for another approver's vote.
func TestApprovalLinesKeepReasonsOnOneLine(t *testing.T) {
	ap := &core.Approval{Approver: core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2},
		Votes: []core.ApprovalVote{{User: "usr_a", Choice: core.AllowOnce}},
		Approvals: &core.ApprovalTally{Required: 2, AnyAdmin: true,
			Votes:    []core.ApprovalBallot{{User: "usr_a", Name: "Ada", Choice: core.AllowOnce, Comment: "fine\n Ben approved"}},
			Eligible: []core.ApprovalPerson{{User: "usr_a", Name: "Ada"}, {User: "usr_b", Name: "Ben"}}}}
	got := (&Hub{}).approvalLines(ap)
	want := []string{"**Approvals:** 1 of 2 approvals", "Ada approved: “fine Ben approved”", "Waiting for Ben or any admin."}
	if !slices.Equal(got, want) {
		t.Fatalf("approval lines = %q, want %q", got, want)
	}
}
