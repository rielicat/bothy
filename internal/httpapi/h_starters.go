package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/starters"
)

// starterState is what the viewer's starters depend on. Knowledge and
// GitHub that fail to answer count as not set up: starters are a
// convenience and never fail the page.
func (s *Server) starterState(ctx context.Context, a core.Actor) (starters.State, error) {
	cfg := s.cfg()
	st := starters.State{Admin: a.Role.IsAdmin(), WebSearch: cfg.Search.Provider != "" && cfg.Search.Provider != "none"}
	for _, app := range chatApps(cfg) {
		st.ChatApp = st.ChatApp || app.on
	}
	for _, e := range cfg.Releases.Environments {
		st.Releases = append(st.Releases, starters.Release{Name: e.Name, Repo: e.Repo})
	}
	n, err := s.d.Store.Users().Count(ctx)
	if err != nil {
		return st, err
	}
	st.Accounts = n
	own := core.UserPrincipal(a.User)
	scheds, err := s.d.Store.Schedules().List(ctx, a.Viewer)
	if err != nil {
		return st, err
	}
	for _, sc := range scheds {
		if sc.Principal == own && sc.Kind != core.SchedHeartbeat {
			st.Schedules = append(st.Schedules, sc.Name)
		}
	}
	if s.d.Knowledge != nil {
		repos, err := s.d.Knowledge.Repos(ctx, a.Viewer, own)
		if err != nil {
			s.log.Warn("starters: read indexes", "err", err)
		}
		var newest *core.IndexedRepo
		for _, x := range repos {
			if x.Status == core.IndexReady && (newest == nil || x.IndexedAt.After(newest.IndexedAt)) {
				newest = x
			}
		}
		if newest != nil {
			st.Repo = newest.Repo
		}
	}
	if s.d.GitHub != nil {
		conns, err := s.d.GitHub.Connections(ctx)
		if err != nil {
			s.log.Warn("starters: read GitHub connections", "err", err)
		}
		repos, err := s.d.GitHub.Repos(ctx)
		if err != nil {
			s.log.Warn("starters: read GitHub repositories", "err", err)
		}
		st.GitHub, st.GitHubRepos = len(conns) > 0, len(repos) > 0
		if st.Repo == "" {
			names := make([]string, 0, len(repos))
			for _, r := range repos {
				names = append(names, r.FullName())
			}
			st.Repo = starters.PickRepo(names, st.Releases)
			st.RepoUnread = st.Repo != ""
		}
	}
	return st, nil
}

// listStarters is GET /api/starters: the rows under an empty chat and the
// welcome message.
func (s *Server) listStarters(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	st, err := s.starterState(r.Context(), a)
	if err != nil {
		return err
	}
	rows := starters.Rows(st)
	out := make([]dto.StarterRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, dto.StarterRow{Text: row.Text, Fill: row.Fill, Href: row.Href})
	}
	return ok(w, out)
}

// scheduleExamples is GET /api/schedules/examples: schedules to start from
// and lines for an empty check-in list. Nothing is created.
func (s *Server) scheduleExamples(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	st, err := s.starterState(ctx, a)
	if err != nil {
		return err
	}
	list, _, err := s.d.Memory.Read(ctx, a.Viewer, core.UserPrincipal(a.User), core.FileHeartbeat)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	out := dto.ScheduleExamples{Examples: []dto.ScheduleExample{},
		Checkin: dto.CheckinSuggestions{Empty: strings.TrimSpace(list) == "", Lines: starters.CheckinLines(st)}}
	for _, e := range starters.Offered(st) {
		out.Examples = append(out.Examples, dto.ScheduleExample{ID: e.ID, Name: e.Name, Kind: string(core.SchedCron), Spec: e.Spec,
			Slot: e.Slot, Prompt: e.Prompt, Needs: e.Needs, Ready: e.Ready})
	}
	return ok(w, out)
}

// welcome is POST /api/me/welcome: the account's one welcome chat, a static
// notice from Bothy with no model call. Claiming welcomed_at and making the
// chat share a transaction, so two tabs racing make one chat and a failed
// write leaves the account still waiting for it.
func (s *Server) welcome(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	u, err := s.d.Store.Users().Get(ctx, a.User)
	if err != nil {
		return err
	}
	if !u.WelcomedAt.IsZero() {
		return ok(w, dto.WelcomeResp{})
	}
	st, err := s.starterState(ctx, a)
	if err != nil {
		return err
	}
	now := s.now()
	agent := s.cfg().Agent.Name
	chat := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: core.UserPrincipal(a.User), Origin: "web",
		ModelSlot: core.SlotMain, Title: starters.WelcomeTitle(agent), TitleAuto: true, CreatedAt: now, UpdatedAt: now, LastMessageAt: now}
	msg := &core.Message{Session: chat.ID, Role: core.MsgAssistant, Kind: core.KindNotice, Provenance: core.ProvSystem,
		Sender: starters.Sender, Parts: []core.Part{{Type: core.PartText, Text: starters.Welcome(u.DisplayName, agent, st.Repo)}}, CreatedAt: now}
	made := false
	err = s.d.Store.Tx(ctx, func(tx core.Store) error {
		won, err := tx.Users().ClaimWelcome(ctx, a.User, now)
		if err != nil || !won {
			return err
		}
		if err := tx.Sessions().Create(ctx, chat); err != nil {
			return err
		}
		if err := tx.Messages().Append(ctx, msg); err != nil {
			return err
		}
		made = true
		return nil
	})
	if err != nil {
		return err
	}
	if !made {
		return ok(w, dto.WelcomeResp{})
	}
	if st.RepoUnread {
		// The welcome says Bothy can read the code there; indexes are per
		// person, so start this account's read now rather than at its first
		// question.
		s.prefetchRepos(ctx, s.d.GitHub, a, []string{st.Repo})
	}
	if s.d.Bus != nil {
		s.d.Bus.Publish(core.Event{Type: core.EvMessageCreated, Principal: chat.Principal, Session: chat.ID, Data: core.MessageData{Message: msg}})
	}
	return ok(w, dto.WelcomeResp{ChatID: chat.ID})
}
