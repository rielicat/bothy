package auth

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	crockfordUpper = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	pairCodeLen    = 8
	// approveSenderTTL matches the 24 h reply window of SenderNotice: an
	// unknown sender is shown one code per day, so a shorter code would
	// leave the owner nothing to approve most of the time.
	approveSenderTTL = 24 * time.Hour
)

var errPairCode = fmt.Errorf("%w: invalid or expired pairing code", core.ErrNotFound)

func newPairCode() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	out := make([]byte, pairCodeLen)
	for i := pairCodeLen - 1; i >= 0; i-- {
		out[i] = crockfordUpper[v&31]
		v >>= 5
	}
	return string(out)
}

func displayCode(c string) string { return c[:4] + "-" + c[4:] }

// normalizeCode upper-cases, drops separators and applies Crockford's
// decoding aliases (O->0, I/L->1) so codes survive being read aloud.
func normalizeCode(c string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(c) {
		switch {
		case r == '-' || r == ' ':
			continue
		case r == 'O':
			r = '0'
		case r == 'I' || r == 'L':
			r = '1'
		case !strings.ContainsRune(crockfordUpper, r):
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != pairCodeLen {
		return "", false
	}
	return b.String(), true
}

func (s *Service) pairingTTL() time.Duration {
	if d := s.conf().Auth.PairingTTL.D(); d > 0 {
		return d
	}
	return 15 * time.Minute
}

// NewPairingCode creates a link_identity code for by.User; the user sends it
// as "/pair CODE" (or "/start CODE" via a deep link) from a chat app.
func (s *Service) NewPairingCode(ctx context.Context, by core.Actor) (core.PairingCode, error) {
	code := newPairCode()
	now := s.now()
	p := &core.Pairing{CodeHash: sha(code), Purpose: core.PairLinkIdentity, User: by.User, CreatedBy: by.User,
		ExpiresAt: now.Add(s.pairingTTL())}
	if err := s.store.Pairings().Create(ctx, p); err != nil {
		return core.PairingCode{}, err
	}
	s.record(ctx, actorOf(by), "pairing.create", userTarget(by.User), "ok", map[string]any{"purpose": p.Purpose})
	return core.PairingCode{Code: displayCode(code), ExpiresAt: p.ExpiresAt, DeepLinks: map[string]string{}}, nil
}

func identActor(id core.ChannelIdentity) string { return "channel:" + id.Channel + ":" + id.ExternalID }

// RedeemPairing links a chat identity to the user who created the code. At
// most five attempts per identity per hour are accepted.
func (s *Service) RedeemPairing(ctx context.Context, ident core.ChannelIdentity, code string) (*core.ChannelIdentity, error) {
	now := s.now()
	if retry, ok := s.pairs.take(ident.Channel+":"+ident.ExternalID, now); !ok {
		return nil, limited("too many pairing attempts", retry)
	}
	norm, ok := normalizeCode(code)
	if !ok {
		s.record(ctx, identActor(ident), "pairing.redeem", "", "denied", nil)
		return nil, errPairCode
	}
	var linked *core.ChannelIdentity
	var user *core.User
	err := s.store.Tx(ctx, func(tx core.Store) error {
		p, err := tx.Pairings().Redeem(ctx, sha(norm), core.PairLinkIdentity, now)
		if errors.Is(err, core.ErrNotFound) {
			return errPairCode
		}
		if err != nil {
			return err
		}
		user, err = tx.Users().Get(ctx, p.User)
		if err != nil {
			return err
		}
		if user.Disabled {
			return fmt.Errorf("%w: the account that created this code is disabled", core.ErrForbidden)
		}
		cur, err := touchIdentity(ctx, tx, ident, now)
		if err != nil {
			return err
		}
		if cur.Status == core.IdentityBlocked {
			return fmt.Errorf("%w: this chat account is blocked", core.ErrForbidden)
		}
		cur.Status, cur.User, cur.Principal = core.IdentityLinked, user.ID, ""
		if ident.DMChat != "" {
			cur.DMChat = ident.DMChat
		}
		if err := tx.Identities().Update(ctx, cur); err != nil {
			return err
		}
		linked = cur
		return nil
	})
	if err != nil {
		s.record(ctx, identActor(ident), "pairing.redeem", "", "denied", nil)
		return nil, err
	}
	s.record(ctx, identActor(ident), "pairing.redeem", userTarget(user.ID), "ok", nil)
	s.linked(ctx, identActor(ident), linked)
	return linked, nil
}

// burnReason is the audit detail of pairing.burn: the hub burns only codes
// posted in a group chat.
const burnReason = "posted in a group"

// BurnPairing uses up a live link_identity code that ident posted in a group
// chat, linking no one: anyone there could otherwise redeem it first. It
// counts against the same five attempts per identity per hour as
// RedeemPairing, so a group is no way around that limit. Text that is not a
// code at all is ordinary chat and leaves no audit row.
func (s *Service) BurnPairing(ctx context.Context, ident core.ChannelIdentity, code string) error {
	now := s.now()
	if retry, ok := s.pairs.take(ident.Channel+":"+ident.ExternalID, now); !ok {
		return limited("too many pairing attempts", retry)
	}
	norm, ok := normalizeCode(code)
	if !ok {
		return errPairCode
	}
	p, err := s.store.Pairings().Redeem(ctx, sha(norm), core.PairLinkIdentity, now)
	if errors.Is(err, core.ErrNotFound) {
		s.record(ctx, identActor(ident), "pairing.burn", "", "denied", map[string]any{"reason": burnReason})
		return errPairCode
	}
	if err != nil {
		return err
	}
	creator := cmp.Or(p.CreatedBy, p.User)
	s.record(ctx, identActor(ident), "pairing.burn", userTarget(creator), "ok", map[string]any{"reason": burnReason})
	s.publish(core.Event{Type: core.EvPairingBurned, Principal: core.UserPrincipal(p.User),
		Data: core.PairingBurnedData{Channel: ident.Channel, Code: displayCode(norm)}})
	return nil
}

func touchIdentity(ctx context.Context, st core.Store, ident core.ChannelIdentity, now time.Time) (*core.ChannelIdentity, error) {
	in := ident
	in.LastSeen = now
	if in.FirstSeen.IsZero() {
		in.FirstSeen = now
	}
	in.Status, in.User, in.Principal = "", "", ""
	return st.Identities().Touch(ctx, &in)
}

func displayOf(id *core.ChannelIdentity) string {
	for _, s := range []string{id.DisplayName, id.Username, id.ExternalID} {
		if s != "" {
			return s
		}
	}
	return id.ExternalID
}

// linked audits identity.link and publishes pairing.linked to the user.
func (s *Service) linked(ctx context.Context, actor string, id *core.ChannelIdentity) {
	s.record(ctx, actor, "identity.link", identActor(*id), "ok", map[string]any{"user": id.User})
	s.publish(core.Event{Type: core.EvPairingLinked, Principal: core.UserPrincipal(id.User),
		Data: core.PairingLinkedData{Channel: id.Channel, ExternalID: id.ExternalID, Display: displayOf(id)}})
}

// SenderNotice records an unknown DM sender. At most once per 24 h it returns
// Reply=true with a fresh approve_sender code for the sender to pass on.
func (s *Service) SenderNotice(ctx context.Context, ident core.ChannelIdentity) (core.SenderNotice, error) {
	now := s.now()
	var notice core.SenderNotice
	err := s.store.Tx(ctx, func(tx core.Store) error {
		cur, err := touchIdentity(ctx, tx, ident, now)
		if err != nil {
			return err
		}
		if cur.Status != core.IdentityPending || (!cur.LastNotice.IsZero() && now.Sub(cur.LastNotice) < 24*time.Hour) {
			return nil
		}
		code := newPairCode()
		p := &core.Pairing{CodeHash: sha(code), Purpose: core.PairApproveSender, Channel: ident.Channel, ExternalID: ident.ExternalID,
			ExpiresAt: now.Add(approveSenderTTL)}
		if err := tx.Pairings().Create(ctx, p); err != nil {
			return err
		}
		cur.LastNotice = now
		if ident.DMChat != "" {
			cur.DMChat = ident.DMChat
		}
		if err := tx.Identities().Update(ctx, cur); err != nil {
			return err
		}
		notice = core.SenderNotice{Reply: true, Code: core.PairingCode{Code: displayCode(code), ExpiresAt: p.ExpiresAt}}
		return nil
	})
	if err != nil {
		return core.SenderNotice{}, err
	}
	if notice.Reply {
		s.record(ctx, identActor(ident), "pairing.create", identActor(ident), "ok", map[string]any{"purpose": core.PairApproveSender})
	}
	return notice, nil
}

// ApproveSender redeems an approve_sender code typed into the web UI by an
// admin, either as a guest (a restricted guest space owned by the admin) or
// linked to linkUser.
func (s *Service) ApproveSender(ctx context.Context, by core.Actor, code, as string, linkUser core.UserID) (*core.ChannelIdentity, error) {
	if err := requireAdmin(by); err != nil {
		return nil, err
	}
	if as != "guest" && as != "link" {
		return nil, fmt.Errorf("%w: as must be guest or link", core.ErrInvalid)
	}
	if as == "link" && linkUser == "" {
		return nil, fmt.Errorf("%w: user_id is required to link", core.ErrInvalid)
	}
	norm, ok := normalizeCode(code)
	if !ok {
		return nil, errPairCode
	}
	now := s.now()
	var out *core.ChannelIdentity
	err := s.store.Tx(ctx, func(tx core.Store) error {
		p, err := tx.Pairings().Redeem(ctx, sha(norm), core.PairApproveSender, now)
		if errors.Is(err, core.ErrNotFound) {
			return errPairCode
		}
		if err != nil {
			return err
		}
		id, err := tx.Identities().Get(ctx, p.Channel, p.ExternalID)
		if err != nil {
			return err
		}
		if as == "guest" {
			out, err = s.makeGuest(ctx, tx, by, id, now)
		} else {
			out, err = s.makeLinked(ctx, tx, by, id, linkUser)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, actorOf(by), "pairing.redeem", identActor(*out), "ok", map[string]any{"purpose": core.PairApproveSender, "as": as})
	s.identityChanged(ctx, actorOf(by), out)
	return out, nil
}

func (s *Service) identityChanged(ctx context.Context, actor string, id *core.ChannelIdentity) {
	switch id.Status {
	case core.IdentityLinked:
		s.linked(ctx, actor, id)
	case core.IdentityGuest:
		s.record(ctx, actor, "identity.guest", identActor(*id), "ok", map[string]any{"principal": id.Principal})
	case core.IdentityBlocked:
		s.record(ctx, actor, "identity.block", identActor(*id), "ok", nil)
	}
}

// errOthersIdentity: another user linked this chat account themselves, so an
// admin may block or unlink it but never move it into a principal they read.
var errOthersIdentity = fmt.Errorf("%w: this chat account is linked to another user; block or unlink it instead", core.ErrForbidden)

// makeLinked gives the chat account uid's principal and role. Admins link
// accounts to themselves; only the owner may link one to someone else (who
// can always pair it themselves with a code from Settings). An account that
// is already some user's may only be linked back to that user.
func (s *Service) makeLinked(ctx context.Context, tx core.Store, by core.Actor, id *core.ChannelIdentity, uid core.UserID) (*core.ChannelIdentity, error) {
	if id.User != "" && id.User != uid {
		return nil, errOthersIdentity
	}
	if uid != by.User && uid != id.User && by.Role != core.RoleOwner {
		return nil, fmt.Errorf("%w: only the owner can link a chat account to someone else; they can pair it themselves from Settings", core.ErrForbidden)
	}
	u, err := tx.Users().Get(ctx, uid)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("%w: no such user", core.ErrInvalid)
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, fmt.Errorf("%w: that account is disabled", core.ErrInvalid)
	}
	id.Status, id.User, id.Principal = core.IdentityLinked, u.ID, ""
	return id, tx.Identities().Update(ctx, id)
}

// makeGuest gives the identity its own restricted guest space (reusing one
// already bound to its DM chat) owned by the acting admin.
func (s *Service) makeGuest(ctx context.Context, tx core.Store, by core.Actor, id *core.ChannelIdentity, now time.Time) (*core.ChannelIdentity, error) {
	if id.User != "" && id.User != by.User {
		return nil, errOthersIdentity
	}
	owner := by.User
	chatID := id.DMChat
	if chatID == "" {
		chatID = id.ExternalID
	}
	sp, err := tx.Spaces().ByChat(ctx, id.Channel, chatID)
	switch {
	case err == nil && sp.Kind == "guest":
	case err == nil:
		return nil, fmt.Errorf("%w: chat %s is already a group space", core.ErrConflict, chatID)
	case errors.Is(err, core.ErrNotFound):
		title := displayOf(id)
		sp = &core.Space{Kind: "guest", Chat: core.ChatRef{Channel: id.Channel, ChatID: chatID, Kind: core.ChatDM}, Title: title,
			Owner: owner, Enabled: true, MentionOnly: false, CreatedAt: now, UpdatedAt: now}
		p := &core.Principal{Kind: core.PrincipalSpace, User: owner, DisplayName: title, ToolProfile: "restricted", CreatedAt: now}
		if err := tx.Spaces().Create(ctx, sp, p); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	id.Status, id.User, id.Principal = core.IdentityGuest, "", sp.Principal
	return id, tx.Identities().Update(ctx, id)
}

// SetIdentityStatus is the admin edit behind PATCH /api/identities: blocked,
// guest (creates the guest space like ApproveSender) or linked to uid. It is
// not part of core.Auth; httpapi reaches it through an interface assertion.
func (s *Service) SetIdentityStatus(ctx context.Context, by core.Actor, channel, externalID string, status core.IdentityStatus, uid core.UserID) (*core.ChannelIdentity, error) {
	if err := requireAdmin(by); err != nil {
		return nil, err
	}
	now := s.now()
	var out *core.ChannelIdentity
	err := s.store.Tx(ctx, func(tx core.Store) error {
		id, err := tx.Identities().Get(ctx, channel, externalID)
		if err != nil {
			return err
		}
		switch status {
		case core.IdentityBlocked:
			id.Status = core.IdentityBlocked
			out = id
			return tx.Identities().Update(ctx, id)
		case core.IdentityGuest:
			out, err = s.makeGuest(ctx, tx, by, id, now)
		case core.IdentityLinked:
			if uid == "" {
				return fmt.Errorf("%w: user_id is required to link", core.ErrInvalid)
			}
			out, err = s.makeLinked(ctx, tx, by, id, uid)
		default:
			return fmt.Errorf("%w: status must be blocked, guest or linked", core.ErrInvalid)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	s.identityChanged(ctx, actorOf(by), out)
	return out, nil
}

// UnlinkIdentity deletes an identity row: its owner may unlink their own,
// admins any. Others get ErrNotFound. A blocked row keeps its user, but only
// an admin may delete it: deleting it would lift the block. Not part of
// core.Auth.
func (s *Service) UnlinkIdentity(ctx context.Context, by core.Actor, channel, externalID string) error {
	id, err := s.store.Identities().Get(ctx, channel, externalID)
	if err != nil {
		return err
	}
	if !by.Role.IsAdmin() {
		if id.User != by.User {
			return fmt.Errorf("identity: %w", core.ErrNotFound)
		}
		if id.Status == core.IdentityBlocked {
			return fmt.Errorf("%w: an admin blocked this chat account", core.ErrForbidden)
		}
	}
	if err := s.store.Identities().Delete(ctx, channel, externalID); err != nil {
		return err
	}
	s.record(ctx, actorOf(by), "identity.unlink", identActor(*id), "ok", map[string]any{"user": id.User})
	return nil
}

// ResolveIdentity maps a chat sender to a principal. Identities linked to a
// missing or disabled account resolve as blocked so the router drops them.
func (s *Service) ResolveIdentity(ctx context.Context, channel, externalID string) (core.IdentityResolution, error) {
	id, err := s.store.Identities().Get(ctx, channel, externalID)
	if errors.Is(err, core.ErrNotFound) {
		return core.IdentityResolution{}, nil
	}
	if err != nil {
		return core.IdentityResolution{}, err
	}
	switch id.Status {
	case core.IdentityLinked:
		u, err := s.store.Users().Get(ctx, id.User)
		if errors.Is(err, core.ErrNotFound) || (err == nil && u.Disabled) {
			return core.IdentityResolution{Status: core.IdentityBlocked}, nil
		}
		if err != nil {
			return core.IdentityResolution{}, err
		}
		return core.IdentityResolution{Status: core.IdentityLinked, User: u.ID, Role: u.Role, Principal: core.UserPrincipal(u.ID)}, nil
	case core.IdentityGuest:
		return core.IdentityResolution{Status: core.IdentityGuest, Principal: id.Principal}, nil
	default:
		return core.IdentityResolution{Status: id.Status}, nil
	}
}
