package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// noticeEvery bounds the refusal and waiting lines Bothy sends one person.
const noticeEvery = 24 * time.Hour

func touchIdentity(ctx context.Context, st core.Store, ident core.ChannelIdentity, now time.Time) (*core.ChannelIdentity, error) {
	in := ident
	in.LastSeen = now
	if in.FirstSeen.IsZero() {
		in.FirstSeen = now
	}
	in.Status, in.Principal, in.Via, in.CanApprove = "", "", "", false
	cur, err := st.Identities().Touch(ctx, &in)
	if err != nil {
		return nil, err
	}
	if ident.DMChat != "" {
		cur.DMChat = ident.DMChat
	}
	return cur, nil
}

func displayOf(id *core.ChannelIdentity) string {
	for _, s := range []string{id.DisplayName, id.Username, id.ExternalID} {
		if s != "" {
			return s
		}
	}
	return id.ExternalID
}

func identActor(id core.ChannelIdentity) string { return core.ChatActor(id.Channel, id.ExternalID) }

// appTitle names a chat app in a refusal an admin reads.
func appTitle(channel string) string {
	switch channel {
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	case "slack":
		return "Slack"
	}
	if channel == "" {
		return channel
	}
	return strings.ToUpper(channel[:1]) + channel[1:]
}

// resolution is what the hub needs to know about an identity, read inside st.
func resolution(ctx context.Context, st core.Store, id *core.ChannelIdentity) (core.IdentityResolution, error) {
	r := core.IdentityResolution{Status: id.Status, Principal: id.Principal, Via: id.Via, CanApprove: id.CanApprove,
		Member: id.Member, MemberAt: id.MemberAt}
	if id.Status != core.IdentityTeammate || id.Principal == "" {
		return r, nil
	}
	sp, err := st.Spaces().Get(ctx, core.SystemViewer(), id.Principal)
	switch {
	case errors.Is(err, core.ErrNotFound):
	case err != nil:
		return core.IdentityResolution{}, err
	default:
		r.SpaceOn = sp.Enabled
	}
	return r, nil
}

// ResolveIdentity says what Bothy knows about a chat sender: nothing for an
// unknown one.
func (s *Service) ResolveIdentity(ctx context.Context, channel, externalID string) (core.IdentityResolution, error) {
	id, err := s.store.Identities().Get(ctx, channel, externalID)
	if errors.Is(err, core.ErrNotFound) {
		return core.IdentityResolution{}, nil
	}
	if err != nil {
		return core.IdentityResolution{}, err
	}
	return resolution(ctx, s.store, id)
}

// Admit records a chat app's verdict on a sender (SPEC §5.1 step 3). A
// member who is pending becomes a teammate with a person space of their
// own; an outsider keeps their status and chats but can't approve any more.
func (s *Service) Admit(ctx context.Context, ident core.ChannelIdentity, m core.Membership) (core.IdentityResolution, error) {
	now := s.now()
	var (
		res                    core.IdentityResolution
		admitted, lostApprover bool
		cur                    *core.ChannelIdentity
	)
	err := s.store.Tx(ctx, func(tx core.Store) error {
		var err error
		ident.Member = core.MemberUnchecked
		if cur, err = touchIdentity(ctx, tx, ident, now); err != nil {
			return err
		}
		was, err := resolution(ctx, tx, cur)
		if err != nil {
			return err
		}
		switch m.Status {
		case core.MemberYes, core.MemberOutsider:
			cur.Member, cur.MemberAt = m.Status, now
		case core.MemberUnknown:
			// Someone a member check let in isn't dropped by a passing loss
			// of access to the platform: the last verdict holds a while.
			held := cur.Via == core.ViaMembership && cur.Member == core.MemberYes && now.Sub(cur.MemberAt) < core.MemberStale
			if !held {
				cur.Member, cur.MemberAt = core.MemberUnknown, now
			}
		}
		if m.Status == core.MemberYes && cur.Status == core.IdentityPending {
			if err := becomeTeammate(ctx, tx, cur, core.ViaMembership, now); err != nil {
				return err
			}
			admitted = true
		}
		if cur.Status == core.IdentityTeammate && cur.Member == core.MemberOutsider && cur.CanApprove {
			cur.CanApprove, lostApprover = false, true
		}
		if cur.Status == core.IdentityTeammate {
			if err := renameTeammate(ctx, tx, cur, now); err != nil {
				return err
			}
		}
		if err := tx.Identities().Update(ctx, cur); err != nil {
			return err
		}
		if res, err = resolution(ctx, tx, cur); err != nil {
			return err
		}
		res.Lost = was.Admitted() && !res.Admitted() && cur.Member == core.MemberOutsider
		return nil
	})
	if err != nil {
		return core.IdentityResolution{}, err
	}
	actor := identActor(*cur)
	if admitted {
		s.record(ctx, actor, "identity.teammate", actor, "ok", map[string]any{"via": core.ViaMembership})
	}
	if lostApprover {
		s.record(ctx, "system", "identity.approver", actor, "ok", map[string]any{"can_approve": false, "via": core.ViaMembership})
	}
	return res, nil
}

// becomeTeammate gives cur a person space (reusing a person space already
// on its chat) and makes it a teammate let in via how.
func becomeTeammate(ctx context.Context, tx core.Store, cur *core.ChannelIdentity, how core.AdmittedVia, now time.Time) error {
	sp, err := tx.Spaces().ByChat(ctx, cur.Channel, cur.ExternalID)
	switch {
	case err == nil && sp.Kind == core.SpacePerson:
		if !sp.Enabled {
			sp.Enabled, sp.UpdatedAt = true, now
			if err := tx.Spaces().Update(ctx, sp); err != nil {
				return err
			}
		}
	case err == nil:
		return fmt.Errorf("%w: chat %s is a group chat", core.ErrConflict, cur.ExternalID)
	case errors.Is(err, core.ErrNotFound):
		title := displayOf(cur)
		sp = &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: cur.Channel, ChatID: cur.ExternalID, Kind: core.ChatDM},
			Title: title, Enabled: true, CreatedAt: now, UpdatedAt: now}
		p := &core.Principal{Kind: core.PrincipalPerson, DisplayName: title, ToolProfile: "standard", CreatedAt: now}
		if err := tx.Spaces().Create(ctx, sp, p); err != nil {
			return err
		}
	default:
		return err
	}
	cur.Status, cur.Principal, cur.Via = core.IdentityTeammate, sp.Principal, how
	return nil
}

// renameTeammate keeps the person principal's name, which the agent's
// prompt and admins see, in step with the chat app's; the space's title
// follows while nobody renamed it.
func renameTeammate(ctx context.Context, tx core.Store, cur *core.ChannelIdentity, now time.Time) error {
	if cur.Principal == "" {
		return nil
	}
	p, err := tx.Principals().Get(ctx, core.SystemViewer(), cur.Principal)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	name := displayOf(cur)
	if p.DisplayName == name {
		return nil
	}
	old := p.DisplayName
	p.DisplayName = name
	if err := tx.Principals().Update(ctx, p); err != nil {
		return err
	}
	sp, err := tx.Spaces().Get(ctx, core.SystemViewer(), cur.Principal)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil
	case err != nil:
		return err
	case sp.Title != old:
		return nil
	}
	sp.Title, sp.UpdatedAt = name, now
	return tx.Spaces().Update(ctx, sp)
}

// NoticeSender reports whether ident may get a refusal or waiting line now:
// at most once per 24 h per identity.
func (s *Service) NoticeSender(ctx context.Context, ident core.ChannelIdentity) (bool, error) {
	now := s.now()
	reply := false
	err := s.store.Tx(ctx, func(tx core.Store) error {
		cur, err := touchIdentity(ctx, tx, ident, now)
		if err != nil {
			return err
		}
		if !cur.LastNotice.IsZero() && now.Sub(cur.LastNotice) < noticeEvery {
			return nil
		}
		cur.LastNotice, reply = now, true
		return tx.Identities().Update(ctx, cur)
	})
	return reply, err
}

// TeammatePatch is an admin's change to a person in a chat app; nil fields
// stay as they are.
type TeammatePatch struct {
	Status     *core.IdentityStatus // teammate (let in, unblock) | blocked
	CanApprove *bool
}

// SetTeammate is the admin edit behind PATCH /api/identities: let someone
// in where the chat app can't tell who is in the team, block or unblock
// them, and mark whether they can approve. It is not part of core.Auth;
// httpapi reaches it through an interface assertion.
func (s *Service) SetTeammate(ctx context.Context, by core.Actor, channel, externalID string, p TeammatePatch) (*core.ChannelIdentity, error) {
	if err := requireAdmin(by); err != nil {
		return nil, err
	}
	now := s.now()
	type row struct {
		action string
		detail map[string]any
	}
	var (
		out    *core.ChannelIdentity
		audits []row
	)
	err := s.store.Tx(ctx, func(tx core.Store) error {
		id, err := tx.Identities().Get(ctx, channel, externalID)
		if err != nil {
			return err
		}
		if p.Status != nil {
			a, err := setStatus(ctx, tx, id, *p.Status, now)
			if err != nil {
				return err
			}
			if a != "" {
				audits = append(audits, row{action: a})
			}
		}
		if p.CanApprove != nil && *p.CanApprove != id.CanApprove {
			if *p.CanApprove {
				switch {
				case id.Status != core.IdentityTeammate:
					return refuse("Let them in first.")
				case id.Member == core.MemberOutsider:
					return refuse("They aren't in the team any more.")
				}
			}
			id.CanApprove = *p.CanApprove
			audits = append(audits, row{action: "identity.approver", detail: map[string]any{"can_approve": id.CanApprove}})
		}
		out = id
		return tx.Identities().Update(ctx, id)
	})
	if err != nil {
		return nil, err
	}
	for _, a := range audits {
		s.record(ctx, actorOf(by), a.action, identActor(*out), "ok", a.detail)
	}
	return s.store.Identities().Get(ctx, channel, externalID)
}

// setStatus moves id to status and returns the audit action for the change
// ("" when there was none).
func setStatus(ctx context.Context, tx core.Store, id *core.ChannelIdentity, status core.IdentityStatus, now time.Time) (string, error) {
	switch status {
	case core.IdentityTeammate:
		switch id.Status {
		case core.IdentityTeammate:
			// Someone a member check let in whom the chat app can no longer
			// vouch for (Discord without access) waits for an admin too.
			if id.Via == core.ViaMembership && id.Member == core.MemberUnknown {
				id.Via = core.ViaAdmin
				return "identity.let_in", nil
			}
			return "", nil
		case core.IdentityPending:
			switch id.Member {
			case core.MemberUnknown:
			case core.MemberOutsider:
				return "", refuse("%s says they aren't in the team.", appTitle(id.Channel))
			default:
				return "", refuse("%s decides who can use Bothy there. They get in when they message Bothy, if they're in the team.",
					appTitle(id.Channel))
			}
			return "identity.let_in", becomeTeammate(ctx, tx, id, core.ViaAdmin, now)
		}
		// Blocked: a membership verdict still applies to whoever is unblocked.
		if id.Principal == "" {
			return "identity.unblock", becomeTeammate(ctx, tx, id, core.ViaAdmin, now)
		}
		sp, err := tx.Spaces().Get(ctx, core.SystemViewer(), id.Principal)
		if err != nil {
			return "", err
		}
		sp.Enabled, sp.UpdatedAt = true, now
		if err := tx.Spaces().Update(ctx, sp); err != nil {
			return "", err
		}
		id.Status = core.IdentityTeammate
		if id.Via == "" {
			id.Via = core.ViaAdmin
		}
		return "identity.unblock", nil
	case core.IdentityBlocked:
		if id.Status == core.IdentityBlocked {
			return "", nil
		}
		id.Status, id.CanApprove = core.IdentityBlocked, false
		if id.Principal != "" {
			sp, err := tx.Spaces().Get(ctx, core.SystemViewer(), id.Principal)
			if err != nil && !errors.Is(err, core.ErrNotFound) {
				return "", err
			}
			if err == nil && sp.Enabled {
				sp.Enabled, sp.UpdatedAt = false, now
				if err := tx.Spaces().Update(ctx, sp); err != nil {
					return "", err
				}
			}
		}
		return "identity.block", nil
	}
	return "", fmt.Errorf("%w: status must be teammate or blocked", core.ErrInvalid)
}

// ForgetTeammate is the last step of deleting a person's data: the
// identity stays (a blocked person stays blocked) but loses its principal,
// which is deleted with everything that cascades from it. httpapi purges
// the rest first and passes what it removed as stats for the audit row.
// Only someone blocked, or no longer in the team, can be forgotten.
func (s *Service) ForgetTeammate(ctx context.Context, by core.Actor, channel, externalID string, stats map[string]any) (core.PrincipalID, error) {
	if err := requireAdmin(by); err != nil {
		return "", err
	}
	var (
		p   core.PrincipalID
		cur core.ChannelIdentity
	)
	err := s.store.Tx(ctx, func(tx core.Store) error {
		id, err := tx.Identities().Get(ctx, channel, externalID)
		if err != nil {
			return err
		}
		if id.Status != core.IdentityBlocked && id.Member != core.MemberOutsider {
			return refuse("Block them first.")
		}
		p = id.Principal
		if id.Status != core.IdentityBlocked {
			id.Status = core.IdentityPending
		}
		// The identity moves off the principal first: the principal's
		// ON DELETE SET NULL would leave a teammate without one.
		id.Principal, id.Via, id.CanApprove, id.FirstText, id.Greet = "", "", false, "", core.GreetHello
		if err := tx.Identities().Update(ctx, id); err != nil {
			return err
		}
		cur = *id
		if p == "" {
			return nil
		}
		return tx.Principals().Delete(ctx, p)
	})
	if err != nil {
		return "", err
	}
	detail := map[string]any{"principal": p}
	for k, v := range stats {
		detail[k] = v
	}
	s.record(ctx, actorOf(by), "identity.forget", identActor(cur), "ok", detail)
	return p, nil
}
