package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// ProfileUpdate is what people may change about themselves; nil fields stay
// as they are.
type ProfileUpdate struct {
	DisplayName *string
	// Timezone is an IANA name; the person's new schedules and chats use it.
	Timezone *string
	// PreferredChat is a chat app the person has linked, or "" for the
	// linked one they used last.
	PreferredChat    *string
	ApprovalsWebOnly *bool
}

// UpdateProfile changes the caller's own name, time zone and where Bothy
// reaches them. It needs no admin: the account changed is always the
// caller's.
func (s *Service) UpdateProfile(ctx context.Context, by core.Actor, p ProfileUpdate) (*core.User, error) {
	cur, err := s.store.Users().Get(ctx, by.User)
	if err != nil {
		return nil, err
	}
	next := *cur
	changed := map[string]any{}
	if p.DisplayName != nil {
		name := strings.TrimSpace(*p.DisplayName)
		if name == "" {
			return nil, fmt.Errorf("%w: enter a name", core.ErrInvalid)
		}
		if err := validDisplayName(name); err != nil {
			return nil, err
		}
		if name != cur.DisplayName {
			if err := s.nameFree(ctx, by.User, name); err != nil {
				return nil, err
			}
			next.DisplayName, changed["display_name"] = name, name
		}
	}
	if p.Timezone != nil {
		tz := strings.TrimSpace(*p.Timezone)
		if err := validTimezone(tz); err != nil {
			return nil, err
		}
		if tz != cur.Timezone {
			next.Timezone, changed["timezone"] = tz, tz
		}
	}
	if p.PreferredChat != nil {
		ch := *p.PreferredChat
		if ch != "" {
			ok, err := s.linkedOn(ctx, by.User, ch)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("%w: link %s to your account first", core.ErrInvalid, ch)
			}
		}
		if ch != cur.PreferredChat {
			next.PreferredChat, changed["preferred_chat"] = ch, ch
		}
	}
	if p.ApprovalsWebOnly != nil && *p.ApprovalsWebOnly != cur.ApprovalsWebOnly {
		next.ApprovalsWebOnly, changed["approvals_web_only"] = *p.ApprovalsWebOnly, *p.ApprovalsWebOnly
	}
	if len(changed) == 0 {
		return cur, nil
	}
	next.UpdatedAt = s.now()
	if err := s.store.Users().SetProfile(ctx, &next); err != nil {
		return nil, err
	}
	if _, renamed := changed["display_name"]; renamed {
		s.renamePrincipal(ctx, next.ID, next.DisplayName)
	}
	s.record(ctx, actorOf(by), "user.profile", userTarget(next.ID), "ok", changed)
	return s.store.Users().Get(ctx, next.ID)
}

// nameFree refuses a name another person already goes by, as a display
// name or a username: approval cards and vote prompts name who asked, and a
// member must not be able to pass as a colleague there. Admins naming
// people (UpdateUser) are trusted with look-alikes.
func (s *Service) nameFree(ctx context.Context, self core.UserID, name string) error {
	users, err := s.store.Users().List(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.ID != self && (strings.EqualFold(strings.TrimSpace(u.DisplayName), name) || strings.EqualFold(u.Username, name)) {
			return fmt.Errorf("%w: someone else here goes by %q; add an initial or a surname", core.ErrInvalid, name)
		}
	}
	return nil
}

// linkedOn reports whether u has a linked identity on channel that Bothy
// can message (a known DM).
func (s *Service) linkedOn(ctx context.Context, u core.UserID, channel string) (bool, error) {
	ids, err := s.store.Identities().ForUser(ctx, u)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id.Status == core.IdentityLinked && id.User == u && id.Channel == channel && id.DMChat != "" {
			return true, nil
		}
	}
	return false, nil
}

// renamePrincipal keeps the user principal's name, which the agent's
// prompt uses, in step with the account's.
func (s *Service) renamePrincipal(ctx context.Context, u core.UserID, name string) {
	p, err := s.store.Principals().Get(ctx, core.SystemViewer(), core.UserPrincipal(u))
	if err != nil {
		s.log.Warn("rename user principal", "user", u, "err", err)
		return
	}
	p.DisplayName = name
	if err := s.store.Principals().Update(ctx, p); err != nil {
		s.log.Warn("rename user principal", "user", u, "err", err)
	}
}
