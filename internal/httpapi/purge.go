package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/bothyhq/bothy/internal/core"
)

type purgeStats map[string]int

// purgeUser wipes a disabled user's data: runs are stopped, boxes destroyed,
// vault user-scope secrets, grants, schedules, identities, memory files and
// workspaces removed, then the users row is deleted, which cascades to
// principals, spaces, sessions with their messages and runs, memory events,
// uploads, pairing codes, auth sessions and tokens (audit_log has no foreign
// key and keeps the user id). Every step runs even if an earlier one failed,
// and the outcome is audited. A client hanging up does not stop a purge
// halfway.
func (s *Server) purgeUser(r *http.Request, by core.Actor, u *core.User) error {
	ctx := context.WithoutCancel(r.Context())
	st := purgeStats{}
	ps, err := s.d.Store.Principals().ForUser(ctx, u.ID)
	if err != nil {
		return err
	}
	v := core.Viewer{User: u.ID}
	for _, p := range ps {
		v.Principals = append(v.Principals, p.ID)
	}
	var errs []error
	step := func(name string, fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	step("runs", func() error { return s.purgeRuns(ctx, v, by, st) })
	var sessions []*core.Session
	step("sessions", func() (err error) { sessions, err = s.allSessions(ctx, v); return err })
	step("boxes", func() error { return s.purgeBoxes(ctx, ps, sessions, st) })
	step("schedules", func() error { return s.purgeSchedules(ctx, v, by, st) })
	step("grants", func() error { return s.purgeGrants(ctx, v, by, st) })
	step("vault", func() error { return s.purgeVault(ctx, u.ID, by, st) })
	step("identities", func() error { return s.purgeIdentities(ctx, u.ID, v, st) })
	step("memory", func() error { return s.purgeMemory(ctx, ps) })
	step("files", func() error { return s.purgeFiles(ps, st) })
	step("user", func() error {
		if err := s.d.Store.Users().Delete(ctx, u.ID); err != nil {
			// Whatever blocked the cascade, the sessions must not stay open.
			return errors.Join(err, s.archiveAll(ctx, v, sessions, st))
		}
		st["users"] = 1
		return nil
	})
	outcome := "ok"
	if len(errs) > 0 {
		outcome = "error"
	}
	detail := map[string]any{"purge": true, "username": u.Username}
	for k, n := range st {
		detail[k] = n
	}
	s.audit(r, by, "user.update", "user:"+string(u.ID), outcome, detail)
	if len(errs) > 0 {
		return fmt.Errorf("purge incomplete: %w", errors.Join(errs...))
	}
	return nil
}

func (s *Server) purgeRuns(ctx context.Context, v core.Viewer, by core.Actor, st purgeStats) error {
	if s.d.Runner == nil {
		return nil
	}
	for _, ri := range s.d.Runner.Active() {
		if !v.Can(ri.Principal) {
			continue
		}
		if _, err := s.d.Runner.Stop(ctx, v, ri.Session, by.User); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		st["runs_stopped"]++
	}
	return nil
}

// allSessions lists every session of the principals, archived and hidden
// ones included.
func (s *Server) allSessions(ctx context.Context, v core.Viewer) ([]*core.Session, error) {
	var out []*core.Session
	seen := map[core.SessionID]bool{}
	for _, archived := range []bool{false, true} {
		var before int64
		for {
			page, err := s.d.Store.Sessions().List(ctx, v, core.SessionFilter{Archived: archived, IncludeHidden: true,
				Page: core.Page{Before: before, Limit: 500}})
			if err != nil {
				return nil, err
			}
			added := 0
			for _, sess := range page {
				if !seen[sess.ID] {
					seen[sess.ID] = true
					out = append(out, sess)
					added++
				}
			}
			next := int64(0)
			if len(page) > 0 {
				next = ms(page[len(page)-1].LastMessageAt)
			}
			// Keyset paging cannot step past rows sharing one timestamp.
			if len(page) < 500 || added == 0 || next == 0 || next == before {
				break
			}
			before = next
		}
	}
	return out, nil
}

func (s *Server) purgeBoxes(ctx context.Context, ps []*core.Principal, sessions []*core.Session, st purgeStats) error {
	if s.d.Sandbox == nil {
		return nil
	}
	var keys []string
	for _, sess := range sessions {
		keys = append(keys, core.DerivedID(core.PrefixBox, "session:"+string(sess.ID)))
	}
	for _, srv := range s.cfg().MCP.Servers {
		for _, p := range ps {
			keys = append(keys, core.DerivedID(core.PrefixBox, "mcp:"+srv.Name+":"+string(p.ID)))
		}
	}
	var errs []error
	for _, k := range keys {
		err := s.d.Sandbox.Destroy(ctx, k)
		switch {
		case err == nil:
			st["boxes"]++
		case errors.Is(err, core.ErrNoSandbox):
			return nil
		case !errors.Is(err, core.ErrNotFound):
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) purgeSchedules(ctx context.Context, v core.Viewer, by core.Actor, st purgeStats) error {
	scs, err := s.d.Store.Schedules().List(ctx, v)
	if err != nil {
		return err
	}
	for _, sc := range scs {
		if s.d.Scheduler != nil {
			err = s.d.Scheduler.Delete(ctx, v, sc.ID, by.User)
		} else {
			err = s.d.Store.Schedules().Delete(ctx, v, sc.ID)
		}
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		st["schedules"]++
	}
	return nil
}

func (s *Server) purgeGrants(ctx context.Context, v core.Viewer, by core.Actor, st purgeStats) error {
	gs, err := s.d.Store.Grants().List(ctx, v, "", "")
	if err != nil {
		return err
	}
	for _, g := range gs {
		err := s.d.Policy.Revoke(ctx, v, g.ID, by.User)
		if errors.Is(err, core.ErrNotFound) {
			// The row is still active, so revoke it directly.
			err = s.d.Store.Grants().Revoke(ctx, v, g.ID, s.now())
		}
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		st["grants"]++
	}
	return nil
}

func (s *Server) purgeVault(ctx context.Context, u core.UserID, by core.Actor, st purgeStats) error {
	if s.d.Vault == nil {
		return nil
	}
	sc := core.UserScope(u)
	metas, err := s.d.Vault.List(ctx, sc)
	if err != nil {
		return err
	}
	for _, m := range metas {
		if m.Source == "env" {
			continue // environment secrets are not stored and cannot be deleted
		}
		if err := s.d.Vault.Delete(ctx, sc, m.Name, by.User); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		st["secrets"]++
	}
	return nil
}

func (s *Server) purgeIdentities(ctx context.Context, u core.UserID, v core.Viewer, st purgeStats) error {
	repo := s.d.Store.Identities()
	ids, err := repo.ForUser(ctx, u)
	if err != nil {
		return err
	}
	all, err := repo.List(ctx, "")
	if err != nil {
		return err
	}
	for _, id := range all {
		if id.Principal != "" && v.Can(id.Principal) && !slices.ContainsFunc(ids, func(x *core.ChannelIdentity) bool {
			return x.Channel == id.Channel && x.ExternalID == id.ExternalID
		}) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := repo.Delete(ctx, id.Channel, id.ExternalID); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		st["identities"]++
	}
	return nil
}

// purgeMemory lets the memory service drop each principal's files under its
// own lock, so a concurrent write cannot recreate them mid-purge.
func (s *Server) purgeMemory(ctx context.Context, ps []*core.Principal) error {
	if s.d.Memory == nil {
		return nil
	}
	var errs []error
	for _, p := range ps {
		if err := s.d.Memory.Purge(ctx, p.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// purgeFiles removes <data>/memory/<principal> (the layout core.Memory
// documents) and <work>/ws/<workspace> for every principal of the user.
func (s *Server) purgeFiles(ps []*core.Principal, st purgeStats) error {
	var errs []error
	for _, p := range ps {
		valid := core.ValidID(core.PrefixUser, string(p.ID)) || core.ValidID(core.PrefixSpace, string(p.ID))
		if s.d.DataDir != "" && valid {
			dir := filepath.Join(s.d.DataDir, "memory", string(p.ID))
			if _, err := os.Stat(dir); err == nil {
				st["memory_dirs"]++
			}
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
			}
		}
		if dir := s.workspaceDir(p.Workspace); dir != "" {
			if _, err := os.Stat(dir); err == nil {
				st["workspaces"]++
			}
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Server) archiveAll(ctx context.Context, v core.Viewer, sessions []*core.Session, st purgeStats) error {
	for _, sess := range sessions {
		if sess.Archived {
			continue
		}
		if err := s.d.Store.Sessions().Archive(ctx, v, sess.ID); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		st["sessions_archived"]++
	}
	return nil
}
