package httpapi

import (
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
		Votes: []dto.ApprovalVoteView{}, Approvers: s.approverText(ctx, req), Requester: req.Requester,
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
	out := dto.Releases{Environments: []dto.ReleaseEnvironment{}, Deploys: []*core.Deploy{}}
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
		out.Deploys = nonNil(ds)
	}
	return ok(w, out)
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
	return ok(w, listOf(ds, p, func(d *core.Deploy) int64 { return ms(d.CreatedAt) }))
}

func (s *Server) userName(ctx context.Context, id core.UserID) string {
	if id == "" {
		return ""
	}
	u, err := s.d.Store.Users().Get(ctx, id)
	switch {
	case err != nil:
		return string(id)
	case u.DisplayName != "":
		return u.DisplayName
	}
	return u.Username
}

// approverText describes who may vote: named users, then roles.
func (s *Server) approverText(ctx context.Context, r core.ApproverReq) string {
	var parts []string
	for _, u := range r.Users {
		parts = append(parts, s.userName(ctx, u))
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
func (s *Server) withVotes(ctx context.Context, aps []*core.Approval) error {
	for i, ap := range aps {
		full, err := s.d.Approvals.Get(ctx, core.SystemViewer(), ap.ID)
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
