package httpapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// teamEditor is the admin side of people in chat apps (internal/auth).
type teamEditor interface {
	SetTeammate(ctx context.Context, by core.Actor, channel, externalID string, p auth.TeammatePatch) (*core.ChannelIdentity, error)
	ForgetTeammate(ctx context.Context, by core.Actor, channel, externalID string, stats map[string]any) (core.PrincipalID, error)
}

// teamsInvalidator is the hub's cache of servers with a channel on.
type teamsInvalidator interface{ InvalidateTeams() }

// hubWait bounds the chat-app messages an admin's change sends.
const hubWait = 10 * time.Second

func (s *Server) listIdentities(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	ids, err := s.d.Store.Identities().List(r.Context(), core.IdentityStatus(r.URL.Query().Get("status")))
	if err != nil {
		return err
	}
	return ok(w, nonNil(ids))
}

// patchIdentity lets someone in, blocks or unblocks them, or marks whether
// they can approve. Their chat hears about it: the greeting once they are
// in, a note when they become an approver; blocking stops their tasks.
func (s *Server) patchIdentity(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ed, has := s.d.Auth.(teamEditor)
	if !has {
		return core.ErrNotImplemented
	}
	var req dto.IdentityPatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	channel, ext := r.PathValue("channel"), r.PathValue("external")
	before, err := s.d.Store.Identities().Get(ctx, channel, ext)
	if err != nil {
		return err
	}
	id, err := ed.SetTeammate(ctx, a, channel, ext, auth.TeammatePatch{Status: req.Status, CanApprove: req.CanApprove})
	if err != nil {
		return err
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hubWait)
	defer cancel()
	if s.d.Hub != nil && before.Status == core.IdentityPending && id.Status == core.IdentityTeammate {
		if err := s.d.Hub.Greet(hctx, channel, ext); err != nil {
			s.log.Warn("greeting someone let in failed", "channel", channel, "err", err)
		}
	}
	if id.Status == core.IdentityBlocked && before.Status != core.IdentityBlocked && id.Principal != "" && s.d.Runner != nil {
		if _, err := s.d.Runner.StopPrincipal(withOrigin(r, a, nil), id.Principal); err != nil {
			s.log.Warn("stopping a blocked person's tasks failed", "err", err)
		}
	}
	if s.d.Hub != nil && id.CanApprove && !before.CanApprove {
		if err := s.d.Hub.TellApprover(hctx, channel, ext); err != nil {
			s.log.Warn("telling a new approver failed", "channel", channel, "err", err)
		}
	}
	s.inboxChanged("sender:" + channel + ":" + ext)
	s.teamAccessReviewed(ctx)
	return ok(w, s.teammate(ctx, id, s.spentToday(ctx)))
}

// teamAccessReviewed retires the 0013 migration's report once an admin has
// acted on people in chat apps.
func (s *Server) teamAccessReviewed(ctx context.Context) {
	if err := s.d.Store.Meta().Delete(ctx, "team_access_report"); err != nil && !errors.Is(err, core.ErrNotFound) {
		s.log.Warn("clearing the team access report failed", "err", err)
	}
	if s.d.Bus != nil {
		s.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Code: "team_access", Clear: true}})
	}
}

func (s *Server) inboxChanged(item string) {
	if s.d.Bus != nil {
		s.d.Bus.Publish(core.Event{Type: core.EvInboxChanged, Data: core.InboxChangedData{Item: item}})
	}
}

// spentToday maps each team principal to what it spent today.
func (s *Server) spentToday(ctx context.Context) map[core.PrincipalID]core.Micros {
	out := map[core.PrincipalID]core.Micros{}
	day := s.now().In(s.location()).Format(time.DateOnly)
	ps, err := s.d.Store.Principals().Team(ctx)
	if err != nil {
		return out
	}
	for _, p := range ps {
		if m, err := s.d.Store.Usage().Spend(ctx, p.ID, day); err == nil {
			out[p.ID] = m
		}
	}
	return out
}

func (s *Server) teammate(_ context.Context, id *core.ChannelIdentity, spent map[core.PrincipalID]core.Micros) dto.Teammate {
	name := strings.TrimSpace(id.DisplayName)
	if name == "" && id.Username != "" {
		name = "@" + id.Username
	}
	return dto.Teammate{Channel: id.Channel, ExternalID: id.ExternalID, Name: cmp.Or(name, "Someone"), Handle: id.Username,
		Status: id.Status, CanApprove: id.CanApprove, Left: id.Member == core.MemberOutsider, LastSeen: id.LastSeen,
		SpentToday: spent[id.Principal], Principal: id.Principal}
}

// listTeammates is the People page's chat-app section: approvers first,
// then other teammates by when they were last active, then blocked people.
func (s *Server) listTeammates(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	ctx := r.Context()
	ids, err := s.d.Store.Identities().List(ctx, "")
	if err != nil {
		return err
	}
	spent := s.spentToday(ctx)
	out := []dto.Teammate{}
	for _, id := range ids {
		if id.Status == core.IdentityTeammate || id.Status == core.IdentityBlocked {
			out = append(out, s.teammate(ctx, id, spent))
		}
	}
	rank := func(t dto.Teammate) int {
		switch {
		case t.Status == core.IdentityBlocked:
			return 2
		case t.CanApprove:
			return 0
		}
		return 1
	}
	slices.SortStableFunc(out, func(x, y dto.Teammate) int {
		return cmp.Or(cmp.Compare(rank(x), rank(y)), y.LastSeen.Compare(x.LastSeen), cmp.Compare(x.Name, y.Name))
	})
	return ok(w, out)
}

// forgetTeammate deletes the data of someone blocked or no longer in the
// team: their chats, runs, schedules, grants, memory, project notes and
// files. The identity stays, so a blocked person stays blocked.
func (s *Server) forgetTeammate(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ed, has := s.d.Auth.(teamEditor)
	if !has {
		return core.ErrNotImplemented
	}
	ctx := context.WithoutCancel(r.Context())
	channel, ext := r.PathValue("channel"), r.PathValue("external")
	id, err := s.d.Store.Identities().Get(ctx, channel, ext)
	if err != nil {
		return err
	}
	if id.Status != core.IdentityBlocked && id.Member != core.MemberOutsider {
		return badRequest("Block them first.")
	}
	st := purgeStats{}
	var errs []error
	if p := id.Principal; p != "" {
		pr, err := s.d.Store.Principals().Get(ctx, onlyOf(p), p)
		if err != nil {
			return err
		}
		if s.d.Runner != nil {
			n, err := s.d.Runner.StopPrincipal(withOrigin(r, a, nil), p)
			if err != nil {
				errs = append(errs, fmt.Errorf("stop: %w", err))
			}
			st["runs_stopped"] += n
		}
		v := core.Viewer{Principals: []core.PrincipalID{p}}
		ps := []*core.Principal{pr}
		step := func(name string, fn func() error) {
			if err := fn(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
		step("runs", func() error { return s.purgeRuns(ctx, v, a, st) })
		var sessions []*core.Session
		step("sessions", func() (err error) { sessions, err = s.allSessions(ctx, v); return err })
		step("boxes", func() error { return s.purgeBoxes(ctx, ps, sessions, st) })
		step("schedules", func() error { return s.purgeSchedules(ctx, v, a, st) })
		step("grants", func() error { return s.purgeGrants(ctx, v, a, st) })
		step("memory", func() error { return s.purgeMemory(ctx, ps) })
		step("knowledge", func() error { return s.purgeKnowledge(ctx, ps) })
		step("files", func() error { return s.purgeFiles(ps, st) })
	}
	if len(errs) > 0 {
		return fmt.Errorf("deleting their data is incomplete: %w", errors.Join(errs...))
	}
	stats := map[string]any{}
	for k, n := range st {
		stats[k] = n
	}
	if _, err := ed.ForgetTeammate(ctx, a, channel, ext, stats); err != nil {
		return err
	}
	s.inboxChanged("sender:" + channel + ":" + ext)
	return noContent(w)
}

// adminViewer covers every user's principal and the team's (teammates and
// channels). It is used only for group chats, grants, inbox group items,
// team schedules and naming usage and audit rows, never for chats, runs,
// memory, uploads or transcripts.
func (s *Server) adminViewer(ctx context.Context, a core.Actor) (core.Viewer, error) {
	users, err := s.d.Store.Users().List(ctx)
	if err != nil {
		return core.Viewer{}, err
	}
	v := core.Viewer{User: a.User, Admin: true}
	for _, u := range users {
		ps, err := s.d.Store.Principals().ForUser(ctx, u.ID)
		if err != nil {
			return core.Viewer{}, err
		}
		for _, p := range ps {
			v.Principals = append(v.Principals, p.ID)
		}
	}
	team, err := s.d.Store.Principals().Team(ctx)
	if err != nil {
		return core.Viewer{}, err
	}
	for _, p := range team {
		v.Principals = append(v.Principals, p.ID)
	}
	return v, nil
}

func isAdmin(a core.Actor) bool { return a.Role.IsAdmin() && a.HasScope("admin") }

// onlyOf is a viewer of one principal, for an admin route acting on that
// principal alone (a group chat, a team schedule, the data being deleted).
func onlyOf(p core.PrincipalID) core.Viewer { return core.Viewer{Principals: []core.PrincipalID{p}} }

func (s *Server) spaceView(ctx context.Context, sp *core.Space) dto.SpaceView {
	view := dto.SpaceView{Space: *sp}
	if p, err := s.d.Store.Principals().Get(ctx, onlyOf(sp.Principal), sp.Principal); err == nil {
		view.ToolProfile = p.ToolProfile
	}
	return view
}

// listSpaces lists the group chats Bothy was added to (admins only).
func (s *Server) listSpaces(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	v, err := s.adminViewer(ctx, a)
	if err != nil {
		return err
	}
	sps, err := s.d.Store.Spaces().List(ctx, v)
	if err != nil {
		return err
	}
	out := []dto.SpaceView{}
	for _, sp := range sps {
		if sp.Kind == core.SpaceGroup {
			out = append(out, s.spaceView(ctx, sp))
		}
	}
	return ok(w, out)
}

// patchSpace changes a group chat. Turning it on or off records that an
// admin reviewed it; turning it off stops its tasks.
func (s *Server) patchSpace(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SpacePatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	id := core.PrincipalID(r.PathValue("id"))
	if req.ToolProfile != nil && *req.ToolProfile != "restricted" && *req.ToolProfile != "standard" {
		return badRequest("tool_profile must be restricted or standard")
	}
	var title string
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
		if utf8.RuneCountInString(title) > 128 {
			return badRequest("title must be at most 128 characters")
		}
	}
	changed := map[string]any{}
	var turnedOff bool
	err := s.d.Store.Tx(ctx, func(tx core.Store) error {
		sp, err := tx.Spaces().Get(ctx, onlyOf(id), id)
		if err != nil {
			return err
		}
		if sp.Kind != core.SpaceGroup {
			return fmt.Errorf("space %s: %w", id, core.ErrNotFound)
		}
		now := s.now()
		if req.Title != nil {
			sp.Title, changed["title"] = title, title
		}
		if req.Enabled != nil {
			turnedOff = sp.Enabled && !*req.Enabled
			sp.Enabled, sp.ReviewedAt, changed["enabled"] = *req.Enabled, now, *req.Enabled
		}
		if req.MentionOnly != nil {
			sp.MentionOnly, changed["mention_only"] = *req.MentionOnly, *req.MentionOnly
		}
		if req.ToolProfile != nil {
			p, err := tx.Principals().Get(ctx, onlyOf(id), id)
			if err != nil {
				return err
			}
			p.ToolProfile, changed["tool_profile"] = *req.ToolProfile, *req.ToolProfile
			if err := tx.Principals().Update(ctx, p); err != nil {
				return err
			}
		}
		sp.UpdatedAt = now
		return tx.Spaces().Update(ctx, sp)
	})
	if err != nil {
		return err
	}
	if req.Enabled != nil {
		if ti, has := s.d.Hub.(teamsInvalidator); has {
			ti.InvalidateTeams()
		}
	}
	if turnedOff && s.d.Runner != nil {
		if _, err := s.d.Runner.StopPrincipal(withOrigin(r, a, nil), id); err != nil {
			s.log.Warn("stopping a channel's tasks failed", "err", err)
		}
	}
	s.audit(r, a, "space.update", "space:"+string(id), "ok", changed)
	s.inboxChanged("group:" + string(id))
	sp, err := s.d.Store.Spaces().Get(ctx, onlyOf(id), id)
	if err != nil {
		return err
	}
	return ok(w, s.spaceView(ctx, sp))
}

// listTeamSchedules lists teammates' and channels' schedules for admins.
func (s *Server) listTeamSchedules(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	team, err := s.d.Store.Principals().Team(ctx)
	if err != nil {
		return err
	}
	out := []dto.TeamSchedule{}
	if len(team) == 0 {
		return ok(w, out)
	}
	v := core.Viewer{User: a.User}
	for _, p := range team {
		v.Principals = append(v.Principals, p.ID)
	}
	scs, err := s.d.Store.Schedules().List(ctx, v)
	if err != nil {
		return err
	}
	n := s.namer(ctx, v)
	for _, sc := range scs {
		owner, kind, app := n.teamOwner(sc.Principal)
		out = append(out, dto.TeamSchedule{ID: sc.ID, Name: sc.Name, Owner: owner, OwnerKind: kind, App: app, Kind: string(sc.Kind),
			Spec: sc.Spec, TZ: sc.TZ, NextRun: sc.NextRunAt, LastStatus: sc.LastStatus, Enabled: sc.Enabled})
	}
	return ok(w, out)
}

// teamSchedule reports whether id is a team principal's schedule, which an
// admin may turn on or off or delete.
func (s *Server) teamSchedule(ctx context.Context, a core.Actor, id core.ScheduleID) (*core.Schedule, bool) {
	if !isAdmin(a) {
		return nil, false
	}
	av, err := s.adminViewer(ctx, a)
	if err != nil {
		return nil, false
	}
	sc, err := s.d.Store.Schedules().Get(ctx, av, id)
	if err != nil {
		return nil, false
	}
	p, err := s.d.Store.Principals().Get(ctx, onlyOf(sc.Principal), sc.Principal)
	if err != nil || !p.TeamOwned() {
		return nil, false
	}
	return sc, true
}
