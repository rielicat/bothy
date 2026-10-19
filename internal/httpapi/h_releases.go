package httpapi

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const releaseDeploysShown = 20

// approvalVotes is GET /api/approvals/{id}/votes.
func (s *Server) approvalVotes(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	ap, err := s.d.Approvals.Get(ctx, a.Viewer, core.ApprovalID(r.PathValue("id")))
	if err != nil {
		return err
	}
	req := ap.Approver
	if !req.Multi() {
		return badRequest("this approval is decided by one approver, not by votes")
	}
	out := dto.ApprovalVotes{Approval: ap.ID, Status: ap.Status, Required: req.Needed(), Approvals: core.AllowVotes(ap.Votes, req),
		Votes: []dto.ApprovalVoteView{}, Approvers: s.approverText(ctx, req, a.User, ap.Votes), Requester: req.Requester,
		RequesterName: s.userName(ctx, req.Requester)}
	for _, v := range ap.Votes {
		out.Votes = append(out.Votes, dto.ApprovalVoteView{User: v.User, Name: s.userName(ctx, v.User), Choice: v.Choice, Via: v.Via, At: v.At,
			Comment: v.Comment})
		if v.User == a.User {
			out.YourVote = v.Choice
		}
	}
	open := ap.Status == core.ApprovalPending || ap.Status == core.ApprovalParked
	if open && out.YourVote == "" {
		u, err := s.d.Store.Users().Get(ctx, a.User)
		if err == nil && !u.Disabled {
			out.CanVote = req.Eligible(u.ID, u.Role)
			out.CanWithdraw = u.ID == req.Requester
		}
	}
	return ok(w, out)
}

// releases is GET /api/releases.
func (s *Server) releases(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	out := dto.Releases{Environments: []dto.ReleaseEnvironment{}, Deploys: []dto.DeployView{}}
	for _, e := range s.d.Config.Current().Releases.Environments {
		env := dto.ReleaseEnvironment{Name: e.Name, Repo: e.Repo, Workflow: e.Workflow, RefPattern: e.RefPattern,
			Inputs: e.Inputs, Required: e.Needed(), Approvers: []dto.ReleaseApprover{}}
		if env.Inputs == nil {
			env.Inputs = map[string]string{}
		}
		users, roles := e.ApproverSet()
		for _, u := range users {
			env.Approvers = append(env.Approvers, dto.ReleaseApprover{Kind: "user", ID: string(u), Name: s.userName(ctx, u)})
		}
		for _, ro := range roles {
			env.Approvers = append(env.Approvers, dto.ReleaseApprover{Kind: "role", ID: string(ro), Name: string(ro)})
		}
		out.Environments = append(out.Environments, env)
	}
	if s.d.Deploys != nil {
		ds, err := s.d.Deploys.List(ctx, a.Viewer, core.DeployFilter{Page: core.Page{Limit: releaseDeploysShown}})
		if err != nil {
			return err
		}
		if !a.Viewer.Admin {
			if ds, err = s.withApproved(ctx, a.Viewer, ds); err != nil {
				return err
			}
		}
		n := s.namer(ctx, a.Viewer)
		out.Deploys = ownLinks(a.Viewer, n.deployViews(ds))
		// Deploys are operational records an admin viewer reads in full
		// (core.ReleaseRepo); what an environment runs is everyone's to see.
		all := core.Viewer{User: a.User, Admin: true}
		for i, e := range out.Environments {
			ds, err := s.d.Deploys.List(ctx, all, core.DeployFilter{Environment: e.Name, Page: core.Page{Limit: releaseDeploysShown}})
			if err != nil {
				return err
			}
			views := ownLinks(a.Viewer, n.deployViews(ds))
			if len(views) > 0 {
				out.Environments[i].Last = &views[0]
			}
			for _, d := range views {
				if !d.Status.Terminal() {
					out.Environments[i].Running = append(out.Environments[i].Running, d)
				}
			}
		}
	}
	return ok(w, out)
}

// withApproved adds to a viewer's own deploys the newest of everyone's whose
// approval they may see: they follow those from their approvals (§8.7),
// though the deploys are someone else's.
func (s *Server) withApproved(ctx context.Context, v core.Viewer, own []*core.Deploy) ([]*core.Deploy, error) {
	all, err := s.d.Deploys.List(ctx, core.Viewer{User: v.User, Admin: true}, core.DeployFilter{Page: core.Page{Limit: releaseDeploysShown}})
	if err != nil {
		return nil, err
	}
	out := own
	for _, d := range all {
		if d.Approval == "" || v.Can(d.Principal) {
			continue
		}
		switch _, err := s.d.Approvals.Get(ctx, v, d.Approval); {
		case err == nil:
			out = append(out, d)
		case !errors.Is(err, core.ErrNotFound):
			return nil, err
		}
	}
	slices.SortStableFunc(out, func(a, b *core.Deploy) int { return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID)) })
	return out[:min(len(out), releaseDeploysShown)], nil
}

// ownLinks leaves out of another person's deploy where it came from: an
// admin sees everyone's deploys, but not their chats or tasks. The approval
// stays, so an approver's Done list can follow the deploy they approved.
func ownLinks(v core.Viewer, ds []dto.DeployView) []dto.DeployView {
	for i := range ds {
		if !v.Can(ds[i].Principal) {
			ds[i].Session, ds[i].Run, ds[i].Chat = "", "", nil
		}
	}
	return ds
}

// releaseDeploys is GET /api/releases/deploys?environment=&before=&limit=.
func (s *Server) releaseDeploys(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if s.d.Deploys == nil {
		return core.ErrNotImplemented
	}
	p, err := page(r)
	if err != nil {
		return err
	}
	ds, err := s.d.Deploys.List(r.Context(), a.Viewer, core.DeployFilter{Environment: r.URL.Query().Get("environment"), Page: p})
	if err != nil {
		return err
	}
	return ok(w, listOf(s.namer(r.Context(), a.Viewer).deployViews(ds), p, func(d dto.DeployView) int64 { return ms(d.CreatedAt) }))
}

// userName is "" for a user that no longer exists (deleted with purge, or
// named in config by mistake), so a user id never reaches a person.
func (s *Server) userName(ctx context.Context, id core.UserID) string {
	if id == "" {
		return ""
	}
	u, err := s.d.Store.Users().Get(ctx, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return ""
	case err != nil:
		// A failed lookup is not a removed person: keep them, unnamed.
		s.log.Warn("look up a user's name", "user", id, "err", err)
		return "someone on the team"
	case u.DisplayName != "":
		return u.DisplayName
	}
	return u.Username
}

// approverText describes who may vote: named users, then roles. A user
// that no longer exists can't vote, so it isn't named.
func (s *Server) approverText(ctx context.Context, r core.ApproverReq, me core.UserID, votes []core.ApprovalVote) string {
	var parts []string
	for _, u := range r.Users {
		// The requester's vote never counts and a voter can't vote again, so
		// neither is someone the request still waits for.
		if u == r.Requester || slices.ContainsFunc(votes, func(v core.ApprovalVote) bool { return v.User == u }) {
			continue
		}
		if u == me {
			parts = append([]string{"you"}, parts...)
		} else if name := s.userName(ctx, u); name != "" {
			parts = append(parts, name)
		}
	}
	roles := slices.Clone(r.Roles)
	slices.Sort(roles)
	for _, ro := range roles {
		switch ro {
		case core.RoleMember:
			parts = append(parts, "anyone")
		case core.RoleOwner:
			parts = append(parts, "the owner")
		default:
			parts = append(parts, "any "+string(ro))
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

// withVotes rereads approvals taken straight from the store (run details,
// already filtered for the caller) through core.Approvals, which fills
// Votes, Approvals and Requester.
func (s *Server) withVotes(ctx context.Context, v core.Viewer, aps []*core.Approval) error {
	for i, ap := range aps {
		full, err := s.d.Approvals.Get(ctx, v, ap.ID)
		switch {
		case err == nil:
			aps[i] = full
		case errors.Is(err, core.ErrNotFound) && ap.Approver.Multi():
			if ap.Votes, err = s.d.Store.Approvals().Votes(ctx, ap.ID); err != nil {
				return err
			}
		case !errors.Is(err, core.ErrNotFound):
			return err
		}
	}
	return nil
}
