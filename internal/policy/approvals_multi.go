package policy

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

const (
	selfApproval = "you asked for this, so someone else has to approve it"
	notApprover  = "you can't approve this request"
	webVotesOnly = "only accounts on the web can vote on this"
)

// vote records r.By's vote on a multi-approver approval and resolves it on a
// deny or once enough distinct eligible approvers have allowed. The status
// check, the vote, the count and the decision share one store transaction,
// so concurrent votes are each counted once and never resolve it twice.
func (ap *Approvals) vote(ctx context.Context, a *core.Approval, r core.Resolution) (*core.Approval, error) {
	if r.Choice != core.AllowOnce && r.Choice != core.Deny {
		return nil, fmt.Errorf("%w: %s was not offered for this approval; approvers allow once or deny", core.ErrInvalid, r.Choice)
	}
	if r.ByIdentity != "" {
		return ap.withdraw(ctx, a, r)
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
			return errDecided
		}
		err = s.Approvals().AddVote(ctx, core.ApprovalVote{Approval: a.ID, User: r.By, Choice: r.Choice, Via: r.Via, At: now, Comment: r.Comment})
		if errors.Is(err, core.ErrConflict) {
			return errVoted
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
		ok, err := s.Approvals().Decide(ctx, a.ID, openStatuses, to, r.Choice, core.Decider{User: r.By}, r.Via, now)
		if err != nil {
			return fmt.Errorf("decide approval %s: %w", a.ID, err)
		}
		if !ok {
			return errDecided
		}
		if r.Comment != "" {
			return s.Approvals().SetComment(ctx, a.ID, r.Comment)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.Votes = votes
	if to != "" {
		a.Comment = r.Comment
	}
	ap.audit1(ctx, core.AuditEntry{
		Actor: "user:" + string(r.By), Action: "approval.vote", Target: string(a.ID), Outcome: "ok",
		Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "fingerprint": fingerprintOf(a),
			"approvals": count, "required": a.Approver.Needed()}),
	})
	if to == "" {
		ap.fillQuiet(ctx, a)
		ap.publish(ctx, core.EvApprovalVoted, a, func(cp *core.Approval) core.EventData { return core.ApprovalData{Approval: cp} })
		out := *a
		return &out, nil
	}
	a.Status, a.Choice, a.DecidedBy, a.DecidedVia, a.DecidedAt = to, r.Choice, r.By, r.Via, msTime(now)
	ap.fillQuiet(ctx, a)
	ap.audit1(ctx, core.AuditEntry{
		Actor: "user:" + string(r.By), Action: "approval.decide", Target: string(a.ID), Outcome: "ok",
		Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "fingerprint": fingerprintOf(a),
			"approvals": count, "required": a.Approver.Needed(), "voters": voterIDs(votes),
			"withdrawn": r.Choice == core.Deny && r.By == a.Approver.Requester}),
	})
	ap.resolved(ctx, a)
	out := *a
	return &out, nil
}

// withdraw lets the teammate whose chat asked for a multi-approver request
// cancel it from their own chat app; nobody else votes from chat.
func (ap *Approvals) withdraw(ctx context.Context, a *core.Approval, r core.Resolution) (*core.Approval, error) {
	app, _, ok := strings.Cut(r.ByIdentity, ":")
	if r.Choice != core.Deny || r.By != "" || !ok || app != r.Via || r.ByIdentity != a.Approver.RequesterIdentity {
		ap.audit1(ctx, core.AuditEntry{
			Actor: "channel:" + r.ByIdentity, Action: "approval.vote", Target: string(a.ID), Outcome: "denied",
			Session: a.Session, Run: a.Run,
			Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "reason": webVotesOnly}),
		})
		return nil, fmt.Errorf("%w: %s", core.ErrForbidden, webVotesOnly)
	}
	if err := ap.checkVia(ctx, a, r); err != nil {
		return nil, err
	}
	now := ap.clock.Now()
	decided, err := ap.store.Approvals().Decide(ctx, a.ID, openStatuses, core.ApprovalDenied, core.Deny, core.Decider{Identity: r.ByIdentity}, r.Via, now)
	if err != nil {
		return nil, fmt.Errorf("decide approval %s: %w", a.ID, err)
	}
	if !decided {
		return nil, errDecided
	}
	if r.Comment != "" {
		if err := ap.store.Approvals().SetComment(ctx, a.ID, r.Comment); err != nil {
			ap.log.WarnContext(ctx, "approvals: saving the decision's reason failed", "approval", a.ID, "err", err)
		} else {
			a.Comment = r.Comment
		}
	}
	a.Status, a.Choice, a.DecidedBy, a.DecidedByIdentity, a.DecidedVia, a.DecidedAt = core.ApprovalDenied, core.Deny, "", r.ByIdentity, r.Via, msTime(now)
	ap.audit1(ctx, core.AuditEntry{
		Actor: "channel:" + r.ByIdentity, Action: "approval.decide", Target: string(a.ID), Outcome: "ok",
		Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"choice": core.Deny, "via": r.Via, "tool": toolOf(a), "fingerprint": fingerprintOf(a), "withdrawn": true}),
	})
	ap.fillQuiet(ctx, a)
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
		return forbid(notApprover)
	}
	u, err := ap.store.Users().Get(ctx, r.By)
	if err != nil || u.Disabled {
		return forbid(notApprover)
	}
	switch {
	case a.Approver.Eligible(u.ID, u.Role):
	case u.ID == a.Approver.Requester && r.Choice == core.Deny:
	case u.ID == a.Approver.Requester:
		return forbid(selfApproval)
	default:
		return forbid(notApprover)
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

// fill sets Votes and Approvals on multi-approver approvals and Requester on
// every approval (§4.5), looking each name up once per call.
func (ap *Approvals) fill(ctx context.Context, as ...*core.Approval) error {
	f := filler{ap: ap, names: map[core.UserID]string{}}
	for _, a := range as {
		if a == nil {
			continue
		}
		if a.Approver.Multi() {
			votes, err := ap.store.Approvals().Votes(ctx, a.ID)
			if err != nil {
				return fmt.Errorf("votes of %s: %w", a.ID, err)
			}
			a.Votes = votes
			if a.Approvals, err = f.tally(ctx, a); err != nil {
				return err
			}
		}
		a.Requester = f.requester(ctx, a)
	}
	return nil
}

// fillQuiet fills an approval about to be published or returned after a
// change: the change has happened, so a failed lookup only costs names.
func (ap *Approvals) fillQuiet(ctx context.Context, a *core.Approval) {
	if err := ap.fill(ctx, a); err != nil {
		ap.log.WarnContext(ctx, "approvals: reading votes and names failed; cards miss them", "approval", a.ID, "err", err)
	}
}

type filler struct {
	ap    *Approvals
	names map[core.UserID]string
	users []*core.User // listed once, for eligibility
}

// name is a user's display name, else username, else id.
func (f *filler) name(ctx context.Context, id core.UserID) string {
	if n, ok := f.names[id]; ok {
		return n
	}
	n := string(id)
	if u, err := f.ap.store.Users().Get(ctx, id); err == nil {
		n = cmp.Or(u.DisplayName, u.Username, n)
	}
	f.names[id] = n
	return n
}

// requester is who asked: the chat sender whose message started a channel's
// or teammate's run; else the account a multi-approver approval names, the
// approver of a personal approval or the run's user; else the teammate or
// channel itself.
func (f *filler) requester(ctx context.Context, a *core.Approval) *core.ApprovalPerson {
	if ri := a.Approver.RequesterIdentity; ri != "" {
		if p := f.chatPerson(ctx, a.Principal, ri); p != nil {
			return p
		}
	}
	id := a.Approver.Requester
	if id == "" {
		id = a.Approver.User
	}
	if id == "" {
		run, err := f.ap.store.Runs().Get(ctx, core.SystemViewer(), a.Run)
		if err == nil {
			id = run.User
		}
	}
	if id != "" {
		return &core.ApprovalPerson{User: id, Name: f.name(ctx, id)}
	}
	sp, err := f.ap.store.Spaces().Get(ctx, core.SystemViewer(), a.Principal)
	if err != nil {
		return nil
	}
	if sp.Kind == core.SpaceGroup {
		return &core.ApprovalPerson{Name: sp.Title, App: sp.Chat.Channel}
	}
	ident, err := f.ap.store.Identities().ByPrincipal(ctx, a.Principal)
	if err != nil {
		return &core.ApprovalPerson{Name: sp.Title, App: sp.Chat.Channel}
	}
	return f.chatPerson(ctx, a.Principal, ident.Key())
}

// chatPerson names a person in a chat app ("<channel>:<external id>"), and
// the channel a request came from when principal is a group chat.
func (f *filler) chatPerson(ctx context.Context, principal core.PrincipalID, key string) *core.ApprovalPerson {
	app, ext, ok := strings.Cut(key, ":")
	if !ok {
		return nil
	}
	p := &core.ApprovalPerson{App: app, Identity: key, Name: ext}
	if id, err := f.ap.store.Identities().Get(ctx, app, ext); err == nil {
		p.Name, p.Handle = cmp.Or(id.DisplayName, id.Username, ext), id.Username
	}
	if sp, err := f.ap.store.Spaces().Get(ctx, core.SystemViewer(), principal); err == nil && sp.Kind == core.SpaceGroup {
		p.Chat = sp.Title
	}
	return p
}

func (f *filler) tally(ctx context.Context, a *core.Approval) (*core.ApprovalTally, error) {
	r := a.Approver
	t := &core.ApprovalTally{Required: r.Needed(), Votes: []core.ApprovalBallot{}, Eligible: []core.ApprovalPerson{}}
	for _, ro := range r.Roles {
		t.AnyAdmin = t.AnyAdmin || ro == core.RoleAdmin || ro == core.RoleMember
	}
	for _, v := range a.Votes {
		t.Votes = append(t.Votes, core.ApprovalBallot{User: v.User, Name: f.name(ctx, v.User), Choice: v.Choice, At: v.At, Comment: v.Comment})
	}
	if f.users == nil {
		us, err := f.ap.store.Users().List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list approvers: %w", err)
		}
		f.users = us
	}
	for _, u := range f.users {
		if u.Disabled || !r.Eligible(u.ID, u.Role) || t.AnyAdmin && u.Role.IsAdmin() {
			continue
		}
		t.Eligible = append(t.Eligible, core.ApprovalPerson{User: u.ID, Name: f.name(ctx, u.ID)})
	}
	return t, nil
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
