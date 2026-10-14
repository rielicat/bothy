package policy

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/rielicat/bothy/internal/core"
)

const selfApproval = "you asked for this, so someone else has to approve it"

// vote records r.By's vote on a multi-approver approval and resolves it on a
// deny or once enough distinct eligible approvers have allowed. The status
// check, the vote, the count and the decision share one store transaction,
// so concurrent votes are each counted once and never resolve it twice.
func (ap *Approvals) vote(ctx context.Context, a *core.Approval, r core.Resolution) (*core.Approval, error) {
	if r.Choice != core.AllowOnce && r.Choice != core.Deny {
		return nil, fmt.Errorf("%w: %s was not offered for this approval; approvers allow once or deny", core.ErrInvalid, r.Choice)
	}
	if err := ap.authorizeVote(ctx, a, r); err != nil {
		if errors.Is(err, core.ErrForbidden) {
			ap.audit1(ctx, core.AuditEntry{
				Actor: "user:" + string(r.By), Action: "approval.vote", Target: string(a.ID), Outcome: "denied",
				Session: a.Session, Run: a.Run,
				Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "reason": err.Error()}),
			})
		}
		return nil, err
	}
	now := ap.clock.Now()
	var (
		votes []core.ApprovalVote
		count int
		to    core.ApprovalStatus
	)
	err := ap.store.Tx(ctx, func(s core.Store) error {
		cur, err := s.Approvals().Get(ctx, core.SystemViewer(), a.ID)
		if err != nil {
			return fmt.Errorf("approval %s: %w", a.ID, err)
		}
		if !slices.Contains(openStatuses, cur.Status) {
			return fmt.Errorf("approval %s: already decided: %w", a.ID, core.ErrConflict)
		}
		err = s.Approvals().AddVote(ctx, core.ApprovalVote{Approval: a.ID, User: r.By, Choice: r.Choice, Via: r.Via, At: now})
		if errors.Is(err, core.ErrConflict) {
			return fmt.Errorf("approval %s: you already voted: %w", a.ID, core.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("record vote on %s: %w", a.ID, err)
		}
		if votes, err = s.Approvals().Votes(ctx, a.ID); err != nil {
			return fmt.Errorf("votes of %s: %w", a.ID, err)
		}
		if count, err = countAllows(ctx, s, cur.Approver, votes); err != nil {
			return err
		}
		switch {
		case r.Choice == core.Deny:
			to = core.ApprovalDenied
		case count >= cur.Approver.Needed():
			to = core.ApprovalAllowed
		default:
			return nil
		}
		ok, err := s.Approvals().Decide(ctx, a.ID, openStatuses, to, r.Choice, r.By, r.Via, now)
		if err != nil {
			return fmt.Errorf("decide approval %s: %w", a.ID, err)
		}
		if !ok {
			return fmt.Errorf("approval %s: already decided: %w", a.ID, core.ErrConflict)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.Votes = votes
	ap.audit1(ctx, core.AuditEntry{
		Actor: "user:" + string(r.By), Action: "approval.vote", Target: string(a.ID), Outcome: "ok",
		Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "fingerprint": fingerprintOf(a),
			"approvals": count, "required": a.Approver.Needed()}),
	})
	if to == "" {
		ap.publish(ctx, core.EvApprovalVoted, a, func(cp *core.Approval) core.EventData { return core.ApprovalData{Approval: cp} })
		out := *a
		return &out, nil
	}
	a.Status, a.Choice, a.DecidedBy, a.DecidedVia, a.DecidedAt = to, r.Choice, r.By, r.Via, msTime(now)
	ap.audit1(ctx, core.AuditEntry{
		Actor: "user:" + string(r.By), Action: "approval.decide", Target: string(a.ID), Outcome: "ok",
		Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "fingerprint": fingerprintOf(a),
			"approvals": count, "required": a.Approver.Needed(), "voters": voterIDs(votes)}),
	})
	ap.resolved(ctx, a)
	out := *a
	return &out, nil
}

// authorizeVote enforces the two-person rule: only an enabled, currently
// eligible approver may allow, and the requester may only withdraw (deny).
// Roles come from the store, not from the caller.
func (ap *Approvals) authorizeVote(ctx context.Context, a *core.Approval, r core.Resolution) error {
	forbid := func(why string) error {
		return fmt.Errorf("%w: %s", core.ErrForbidden, why)
	}
	if r.By == "" {
		return forbid("not an approver")
	}
	u, err := ap.store.Users().Get(ctx, r.By)
	if err != nil || u.Disabled {
		return forbid("not an approver")
	}
	switch {
	case a.Approver.Eligible(u.ID, u.Role):
	case u.ID == a.Approver.Requester && r.Choice == core.Deny:
	case u.ID == a.Approver.Requester:
		return forbid(selfApproval)
	default:
		return forbid("not an approver")
	}
	return ap.checkVia(ctx, a, r)
}

// countAllows counts the distinct allow votes of users who are still
// eligible: a voter disabled, demoted or removed since voting no longer
// counts towards the approval.
func countAllows(ctx context.Context, s core.Store, r core.ApproverReq, votes []core.ApprovalVote) (int, error) {
	n := 0
	seen := map[core.UserID]bool{}
	for _, v := range votes {
		if v.Choice != core.AllowOnce || seen[v.User] {
			continue
		}
		seen[v.User] = true
		u, err := s.Users().Get(ctx, v.User)
		switch {
		case errors.Is(err, core.ErrNotFound):
			continue
		case err != nil:
			return 0, fmt.Errorf("approver %s: %w", v.User, err)
		}
		if !u.Disabled && r.Eligible(u.ID, u.Role) {
			n++
		}
	}
	return n, nil
}

// Voters returns the enabled users who may vote allow on a multi-approver
// approval (none for other approvals). The channel hub sends each of them
// the card.
func (ap *Approvals) Voters(ctx context.Context, r core.ApproverReq) ([]*core.User, error) {
	if !r.Multi() {
		return nil, nil
	}
	us, err := ap.store.Users().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list approvers: %w", err)
	}
	return slices.DeleteFunc(us, func(u *core.User) bool { return u.Disabled || !r.Eligible(u.ID, u.Role) }), nil
}

func (ap *Approvals) voters(ctx context.Context, r core.ApproverReq) []*core.User {
	us, err := ap.Voters(ctx, r)
	if err != nil {
		ap.log.WarnContext(ctx, "approvals: listing approvers failed; their inboxes miss this event", "err", err)
	}
	return us
}

// fillVotes sets Votes on multi-approver approvals.
func (ap *Approvals) fillVotes(ctx context.Context, as ...*core.Approval) error {
	for _, a := range as {
		if a == nil || !a.Approver.Multi() {
			continue
		}
		votes, err := ap.store.Approvals().Votes(ctx, a.ID)
		if err != nil {
			return fmt.Errorf("votes of %s: %w", a.ID, err)
		}
		a.Votes = votes
	}
	return nil
}

// userName returns a lookup of display names for cards; unknown users show
// their id.
func (ap *Approvals) userName(ctx context.Context) func(core.UserID) string {
	return func(id core.UserID) string {
		u, err := ap.store.Users().Get(ctx, id)
		switch {
		case err != nil:
			return string(id)
		case u.DisplayName != "":
			return u.DisplayName
		case u.Username != "":
			return u.Username
		}
		return string(id)
	}
}

func approverDetail(r core.ApproverReq) map[string]any {
	if !r.Multi() {
		return nil
	}
	return map[string]any{"users": r.Users, "roles": r.Roles, "required": r.Needed(), "requester": r.Requester}
}

func voterIDs(votes []core.ApprovalVote) []core.UserID {
	out := make([]core.UserID, 0, len(votes))
	for _, v := range votes {
		out = append(out, v.User)
	}
	return out
}
