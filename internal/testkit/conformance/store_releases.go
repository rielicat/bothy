package conformance

import (
	"slices"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

// multiApproval stores a pending multi-approver approval of a run.
func (f *fx) multiApproval(r *core.Run, approver core.ApproverReq, created int) *core.Approval {
	f.t.Helper()
	a := newApproval(r, core.ApprovalPending, at(created))
	a.Approver, a.Decision.Approver = approver, approver
	f.must(f.s.Approvals().Create(f.ctx, a), "create multi-approver approval")
	return a
}

func testApprovalVotes(f *fx) {
	r := f.s.Approvals()
	req := f.user(core.RoleMember)
	alice, bob := f.user(core.RoleAdmin), f.user(core.RoleMember)
	run := f.run(f.session(core.UserPrincipal(req.ID)), core.RunWaitingApproval)
	a := f.multiApproval(run, core.ApproverReq{Users: []core.UserID{alice.ID, bob.ID}, Required: 2, Requester: req.ID}, 1)
	other := f.approval(run, core.ApprovalPending, at(2))

	votes, err := r.Votes(f.ctx, a.ID)
	f.must(err, "Votes of an approval without votes")
	f.check(len(votes) == 0, "Votes = %+v before any vote", votes)

	v1 := core.ApprovalVote{Approval: a.ID, User: bob.ID, Choice: core.AllowOnce, Via: "web", At: at(10).Add(123456)}
	v2 := core.ApprovalVote{Approval: a.ID, User: alice.ID, Choice: core.Deny, Via: "telegram", At: at(5)}
	f.must(r.AddVote(f.ctx, v1), "AddVote bob")
	f.must(r.AddVote(f.ctx, v2), "AddVote alice")
	f.want(r.AddVote(f.ctx, core.ApprovalVote{Approval: a.ID, User: bob.ID, Choice: core.Deny, Via: "web", At: at(11)}),
		core.ErrConflict, "second vote by the same user")
	f.want(r.AddVote(f.ctx, core.ApprovalVote{Approval: core.ApprovalID(core.NewID(core.PrefixApproval)), User: bob.ID,
		Choice: core.AllowOnce, Via: "web", At: at(12)}), core.ErrInvalid, "vote on a missing approval")
	f.want(r.AddVote(f.ctx, core.ApprovalVote{Approval: other.ID, User: bob.ID, Choice: core.AllowSession, Via: "web", At: at(12)}),
		core.ErrInvalid, "vote with a grant choice")
	f.want(r.AddVote(f.ctx, core.ApprovalVote{Approval: other.ID, Choice: core.AllowOnce, Via: "web", At: at(12)}),
		core.ErrInvalid, "vote without a user")

	votes, err = r.Votes(f.ctx, a.ID)
	f.must(err, "Votes")
	v1.At = at(10)
	f.eq("Votes oldest first, at millisecond precision", votes, []core.ApprovalVote{v2, v1})
	votes, err = r.Votes(f.ctx, other.ID)
	f.must(err, "Votes of another approval")
	f.check(len(votes) == 0, "Votes leaked across approvals: %+v", votes)

	got, err := r.Get(f.ctx, core.SystemViewer(), a.ID)
	f.must(err, "Get")
	f.check(len(got.Votes) == 0, "Get filled Votes %+v; repositories leave it to Approvals", got.Votes)
	f.eq("Approver round trip", got.Approver, a.Approver)

	// Every concurrent vote of a distinct user lands exactly once.
	voters := make([]*core.User, 20)
	for i := range voters {
		voters[i] = f.user(core.RoleMember)
	}
	b := f.multiApproval(run, core.ApproverReq{Roles: []core.Role{core.RoleMember}, Required: 2, Requester: req.ID}, 3)
	var wg sync.WaitGroup
	errs := make([]error, len(voters)*2)
	for i, u := range voters {
		for j := range 2 {
			wg.Go(func() {
				errs[i*2+j] = r.AddVote(f.ctx, core.ApprovalVote{Approval: b.ID, User: u.ID, Choice: core.AllowOnce, Via: "web", At: at(20)})
			})
		}
	}
	wg.Wait()
	for i := range voters {
		a, b := errs[i*2], errs[i*2+1]
		f.check((a == nil) != (b == nil), "voter %d: two concurrent votes gave %v and %v, want exactly one success", i, a, b)
	}
	votes, err = r.Votes(f.ctx, b.ID)
	f.must(err, "Votes after concurrent voting")
	f.check(len(votes) == len(voters), "%d votes stored, want %d", len(votes), len(voters))
}

func testApprovalMultiVisibility(f *fx) {
	r := f.s.Approvals()
	f.user(core.RoleOwner)
	req := f.user(core.RoleMember)
	named, member, admin := f.user(core.RoleMember), f.user(core.RoleMember), f.user(core.RoleAdmin)
	run := f.run(f.session(core.UserPrincipal(req.ID)), core.RunWaitingApproval)

	byID := f.multiApproval(run, core.ApproverReq{Users: []core.UserID{named.ID}, Requester: req.ID}, 1)
	byAdmin := f.multiApproval(run, core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: req.ID}, 2)
	byMember := f.multiApproval(run, core.ApproverReq{Roles: []core.Role{core.RoleMember}, Requester: req.ID}, 3)
	decided := newApproval(run, core.ApprovalAllowed, at(4))
	decided.Approver = core.ApproverReq{Users: []core.UserID{named.ID}, Requester: req.ID}
	f.must(r.Create(f.ctx, decided), "Create decided multi-approver approval")

	vAdmin := viewer(admin)
	vAdmin.Admin = true
	ids := func(as []*core.Approval) []core.ApprovalID {
		return approvalIDs(sortBy(as, func(x *core.Approval) core.ApprovalID { return x.ID }))
	}
	sorted := func(as ...*core.Approval) []core.ApprovalID { return ids(as) }
	for _, c := range []struct {
		name    string
		v       core.Viewer
		pending []core.ApprovalID
		recent  []core.ApprovalID
	}{
		{"requester", viewer(req), sorted(byID, byAdmin, byMember), []core.ApprovalID{decided.ID}},
		{"named approver", viewer(named), sorted(byID, byMember), []core.ApprovalID{decided.ID}},
		{"member", viewer(member), sorted(byMember), []core.ApprovalID{}},
		{"admin", vAdmin, sorted(byAdmin, byMember), []core.ApprovalID{}},
		{"admin viewer without Admin set", viewer(admin), sorted(byMember), []core.ApprovalID{}},
		{"no user", core.Viewer{Principals: []core.PrincipalID{core.UserPrincipal(member.ID)}}, []core.ApprovalID{}, []core.ApprovalID{}},
	} {
		as, err := r.Pending(f.ctx, c.v)
		f.must(err, "Pending: "+c.name)
		f.eq("Pending: "+c.name, ids(as), c.pending)
		as, err = r.Recent(f.ctx, c.v, core.Page{})
		f.must(err, "Recent: "+c.name)
		f.eq("Recent: "+c.name, ids(as), c.recent)
		for _, a := range []*core.Approval{byID, byAdmin, byMember, decided} {
			_, err := r.Get(f.ctx, c.v, a.ID)
			want := slices.Contains(c.pending, a.ID) || slices.Contains(c.recent, a.ID)
			if want {
				f.must(err, "Get: "+c.name)
			} else {
				f.want(err, core.ErrNotFound, "Get: "+c.name)
			}
		}
	}
}

func testReleases(f *fx) {
	rs, ok := f.s.(core.ReleaseStore)
	if !ok {
		f.t.Skip("store keeps no deploy records")
	}
	r := rs.Releases()
	alice, bob := f.user(core.RoleMember), f.user(core.RoleMember)
	pa, pb := core.UserPrincipal(alice.ID), core.UserPrincipal(bob.ID)
	sa := f.session(pa)
	mk := func(p core.PrincipalID, env string, sec int) *core.Deploy {
		return &core.Deploy{Environment: env, Repo: "acme/web", Workflow: "deploy.yml", Ref: "main", Principal: p,
			Session: sa.ID, RequestedBy: alice.ID, Status: core.DeployDispatched, CreatedAt: at(sec), UpdatedAt: at(sec)}
	}
	d1 := mk(pa, "staging", 1)
	d1.Chat = &core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM}
	d1.ApprovedBy = []core.UserID{bob.ID}
	f.must(r.Create(f.ctx, d1), "Create")
	f.check(core.ValidID(core.PrefixDeploy, d1.ID), "Create assigned id %q, want a dep_ id", d1.ID)
	d2, d3 := mk(pa, "prod", 2), mk(pb, "staging", 3)
	f.must(r.Create(f.ctx, d2), "Create d2")
	f.must(r.Create(f.ctx, d3), "Create d3")
	f.want(r.Create(f.ctx, &core.Deploy{ID: d1.ID, Principal: pa, Status: core.DeployDispatched}), core.ErrConflict, "duplicate id")
	f.want(r.Create(f.ctx, func() *core.Deploy { d := mk(pa, "x", 4); d.Status = "lost"; return d }()), core.ErrInvalid, "status CHECK")
	f.want(r.Create(f.ctx, mk(core.PrincipalID(core.NewID(core.PrefixUser)), "x", 4)), core.ErrInvalid, "principal foreign key")

	got, err := r.Get(f.ctx, viewer(alice), d1.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, d1)
	_, err = r.Get(f.ctx, viewer(bob), d1.ID)
	f.want(err, core.ErrNotFound, "Get across principals")
	vAdmin := viewer(bob)
	vAdmin.Admin = true
	_, err = r.Get(f.ctx, vAdmin, d1.ID)
	f.must(err, "Get as an admin")

	got.Status, got.WorkflowRun, got.RunURL, got.Conclusion, got.UpdatedAt, got.FinishedAt =
		core.DeploySucceeded, 77, "https://github.com/acme/web/actions/runs/77", "success", at(9), at(9)
	f.must(r.Update(f.ctx, got), "Update")
	again, err := r.Get(f.ctx, core.SystemViewer(), d1.ID)
	f.must(err, "Get after Update")
	f.eq("Update round trip", again, got)
	f.want(r.Update(f.ctx, mk(pa, "x", 5)), core.ErrNotFound, "Update of a missing deploy")

	list := func(v core.Viewer, flt core.DeployFilter) []string {
		f.t.Helper()
		ds, err := r.List(f.ctx, v, flt)
		f.must(err, "List")
		out := []string{}
		for _, d := range ds {
			out = append(out, d.ID)
		}
		return out
	}
	f.eq("List newest first", list(viewer(alice), core.DeployFilter{}), []string{d2.ID, d1.ID})
	f.eq("List by environment", list(core.SystemViewer(), core.DeployFilter{Environment: "staging"}), []string{d3.ID, d1.ID})
	f.eq("List open only", list(core.SystemViewer(), core.DeployFilter{Open: true}), []string{d3.ID, d2.ID})
	f.eq("List for an admin", list(vAdmin, core.DeployFilter{}), []string{d3.ID, d2.ID, d1.ID})
	f.eq("List paging", list(core.SystemViewer(), core.DeployFilter{Page: core.Page{Limit: 1, Before: d3.CreatedAt.UnixMilli()}}), []string{d2.ID})
	f.eq("List for a viewer without principals", list(core.Viewer{User: alice.ID}, core.DeployFilter{}), []string{})
}
