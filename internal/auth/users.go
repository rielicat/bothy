package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

func validUsername(name string) error {
	if !usernameRE.MatchString(name) {
		return fmt.Errorf("%w: username must be 1-32 letters, digits, '.', '_' or '-' and start with a letter or digit", core.ErrInvalid)
	}
	return nil
}

func validDisplayName(name string) error {
	if utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, isControl) || !utf8.ValidString(name) {
		return fmt.Errorf("%w: display name must be at most 64 printable characters", core.ErrInvalid)
	}
	return nil
}

func requireAdmin(by core.Actor) error {
	if !by.Role.IsAdmin() || !by.HasScope("admin") {
		return fmt.Errorf("%w: admin only", core.ErrForbidden)
	}
	return nil
}

// CreateUser adds an admin or member. The owner exists exactly once and is
// created only through setup.
func (s *Service) CreateUser(ctx context.Context, by core.Actor, username, password, displayName string, role core.Role) (*core.User, error) {
	return s.createUser(ctx, by, username, password, displayName, role, false)
}

// CreateUserOneTime adds an admin or member with a generated one-time
// password, returned once, that its owner must replace at the first
// sign-in. It is not part of core.Auth.
func (s *Service) CreateUserOneTime(ctx context.Context, by core.Actor, username, displayName string, role core.Role) (*core.User, string, error) {
	pw := oneTimePassword(s.conf().Auth.PasswordMinLength)
	u, err := s.createUser(ctx, by, username, pw, displayName, role, true)
	if err != nil {
		return nil, "", err
	}
	return u, pw, nil
}

func (s *Service) createUser(ctx context.Context, by core.Actor, username, password, displayName string, role core.Role, oneTime bool) (*core.User, error) {
	if err := requireAdmin(by); err != nil {
		return nil, err
	}
	if role != core.RoleAdmin && role != core.RoleMember {
		return nil, fmt.Errorf("%w: role must be admin or member", core.ErrInvalid)
	}
	if err := validUsername(username); err != nil {
		return nil, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if err := validDisplayName(displayName); err != nil {
		return nil, err
	}
	if err := checkPassword(password, s.conf().Auth.PasswordMinLength); err != nil {
		return nil, err
	}
	phc, err := s.hash.hash(ctx, password)
	if err != nil {
		return nil, err
	}
	now := s.now()
	tz := s.conf().Timezone
	if tz == "" {
		tz = "UTC"
	}
	u := &core.User{Username: username, DisplayName: displayName, Role: role, PWHash: phc, Timezone: tz, MustChangePassword: oneTime,
		CreatedAt: now, UpdatedAt: now}
	if err := s.store.Users().Create(ctx, u); err != nil {
		if errors.Is(err, core.ErrConflict) {
			return nil, fmt.Errorf("%w: username %q is taken", core.ErrConflict, username)
		}
		return nil, err
	}
	s.record(ctx, actorOf(by), "user.create", userTarget(u.ID), "ok", map[string]any{"username": username, "role": role, "one_time_password": oneTime})
	return s.store.Users().Get(ctx, u.ID)
}

// oneTimeAlphabet is Crockford's base32 in lower case: no i, l, o or u to
// misread when the password is read out or copied by hand.
const oneTimeAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// oneTimePassword returns groups of four random characters joined by dashes
// ("7k2m-9qxd-…"), at least four groups (80 bits) and at least minLen long.
func oneTimePassword(minLen int) string {
	var b strings.Builder
	for groups := 0; groups < 4 || b.Len() < minLen; groups++ {
		if groups > 0 {
			b.WriteByte('-')
		}
		var r [4]byte
		_, _ = rand.Read(r[:])
		for _, c := range r {
			b.WriteByte(oneTimeAlphabet[c&31])
		}
	}
	return b.String()
}

// ResetUserPassword gives another account a new one-time password, returned
// once, and signs it out everywhere; its owner chooses their own at the next
// sign-in. The owner may reset anyone else, admins only members. It is not
// part of core.Auth.
func (s *Service) ResetUserPassword(ctx context.Context, by core.Actor, uid core.UserID) (string, error) {
	if err := requireAdmin(by); err != nil {
		return "", err
	}
	u, err := s.store.Users().Get(ctx, uid)
	if err != nil {
		return "", err
	}
	switch {
	case u.ID == by.User:
		return "", fmt.Errorf("%w: change your own password instead", core.ErrForbidden)
	case u.Role == core.RoleOwner:
		return "", fmt.Errorf("%w: the owner's password can only be reset with bothy user reset-password on the server", core.ErrForbidden)
	case u.Role == core.RoleAdmin && by.Role != core.RoleOwner:
		return "", fmt.Errorf("%w: only the owner can reset an admin's password", core.ErrForbidden)
	}
	pw := oneTimePassword(s.conf().Auth.PasswordMinLength)
	if err := s.replacePassword(ctx, u, pw, true, actorOf(by), "admin"); err != nil {
		return "", err
	}
	return pw, nil
}

// UpdateUser applies the role, disabled flag and display name of u. Nothing
// else of u is read. A role change or disabling deletes the user's sessions;
// disabling also revokes every token.
func (s *Service) UpdateUser(ctx context.Context, by core.Actor, u *core.User) error {
	if err := requireAdmin(by); err != nil {
		return err
	}
	cur, err := s.store.Users().Get(ctx, u.ID)
	if err != nil {
		return err
	}
	next := *cur
	next.DisplayName = strings.TrimSpace(u.DisplayName)
	next.Role = u.Role
	next.Disabled = u.Disabled
	if next.DisplayName == "" {
		next.DisplayName = cur.Username
	}
	if err := validDisplayName(next.DisplayName); err != nil {
		return err
	}
	changed := map[string]any{}
	if next.DisplayName != cur.DisplayName {
		changed["display_name"] = next.DisplayName
	}
	if next.Role != cur.Role {
		switch {
		case cur.Role == core.RoleOwner:
			return fmt.Errorf("%w: the owner's role cannot change", core.ErrForbidden)
		case next.Role != core.RoleAdmin && next.Role != core.RoleMember:
			return fmt.Errorf("%w: role must be admin or member", core.ErrInvalid)
		}
		changed["role"] = next.Role
	}
	if next.Disabled != cur.Disabled {
		switch {
		case cur.Role == core.RoleOwner:
			return fmt.Errorf("%w: the owner cannot be disabled", core.ErrForbidden)
		case cur.ID == by.User:
			return fmt.Errorf("%w: you cannot disable your own account", core.ErrForbidden)
		}
		changed["disabled"] = next.Disabled
	}
	if cur.Role == core.RoleOwner && by.Role != core.RoleOwner && len(changed) > 0 {
		return fmt.Errorf("%w: only the owner can edit the owner", core.ErrForbidden)
	}
	if len(changed) == 0 {
		return nil
	}
	now := s.now()
	next.UpdatedAt = now
	if err := s.store.Users().Update(ctx, &next); err != nil {
		return err
	}
	if _, ok := changed["role"]; ok || next.Disabled {
		if err := s.store.AuthSessions().DeleteUser(ctx, next.ID); err != nil {
			return err
		}
	}
	if next.Disabled && !cur.Disabled {
		if err := s.revokeAllTokens(ctx, next.ID, now); err != nil {
			return err
		}
	}
	s.record(ctx, actorOf(by), "user.update", userTarget(next.ID), "ok", changed)
	return nil
}

func (s *Service) revokeAllTokens(ctx context.Context, u core.UserID, now time.Time) error {
	ts, err := s.store.Tokens().List(ctx, u)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if !t.Revoked.IsZero() {
			continue
		}
		if err := s.store.Tokens().Revoke(ctx, u, t.ID, now); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
	}
	return nil
}

// SetPassword changes a password after verifying the old one. Wrong old
// passwords count toward the username lockout, so a stolen session cannot be
// used to brute-force the password. All sessions are deleted; the caller
// signs in again to rotate its cookie. An account that must change its
// one-time password may leave old empty: its session was just opened with
// that password.
func (s *Service) SetPassword(ctx context.Context, uid core.UserID, old, newPW string) error {
	if old == "" {
		u, err := s.store.Users().Get(ctx, uid)
		if err != nil {
			return err
		}
		if u.MustChangePassword {
			// The admin who handed out the one-time password knows it, so
			// keeping it would leave the account in their hands.
			if err := s.hash.acquire(ctx); err != nil {
				return err
			}
			same, err := s.hash.verifyHeld(u.PWHash, newPW)
			s.hash.release()
			if err != nil && !errors.Is(err, errPHC) {
				return err
			}
			if same {
				return fmt.Errorf("%w: choose a new password, not the one-time password", core.ErrInvalid)
			}
			return s.replacePassword(ctx, u, newPW, false, "user:"+string(uid), "web")
		}
	}
	// As in Login, the lock check and the counted failure share the slot.
	if err := s.hash.acquire(ctx); err != nil {
		return err
	}
	u, err := s.checkOldPassword(ctx, uid, old)
	s.hash.release()
	if err != nil {
		return err
	}
	if old == newPW {
		return fmt.Errorf("%w: the new password must differ from the current one", core.ErrInvalid)
	}
	return s.replacePassword(ctx, u, newPW, false, "user:"+string(uid), "web")
}

// checkOldPassword runs with the hashing slot held.
func (s *Service) checkOldPassword(ctx context.Context, uid core.UserID, old string) (*core.User, error) {
	u, err := s.store.Users().Get(ctx, uid)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if now.Before(u.LockedUntil) {
		return nil, limited("account temporarily locked", u.LockedUntil.Sub(now))
	}
	ok, err := s.hash.verifyHeld(u.PWHash, old)
	if err != nil && !errors.Is(err, errPHC) {
		return nil, err
	}
	if !ok {
		s.loginFailed(ctx, u, u.Username, "", now)
		return nil, fmt.Errorf("%w: the current password is incorrect", core.ErrInvalid)
	}
	return u, nil
}

// ResetPassword sets a password without the old one (offline CLI).
func (s *Service) ResetPassword(ctx context.Context, uid core.UserID, newPW string) error {
	u, err := s.store.Users().Get(ctx, uid)
	if err != nil {
		return err
	}
	return s.replacePassword(ctx, u, newPW, false, "system", "cli")
}

// replacePassword sets pw and signs the account out everywhere; oneTime
// marks pw as one its owner must replace.
func (s *Service) replacePassword(ctx context.Context, u *core.User, pw string, oneTime bool, actor, via string) error {
	if err := checkPassword(pw, s.conf().Auth.PasswordMinLength); err != nil {
		return err
	}
	phc, err := s.hash.hash(ctx, pw)
	if err != nil {
		return err
	}
	cur, err := s.store.Users().Get(ctx, u.ID)
	if err != nil {
		return err
	}
	cur.PWHash = phc
	cur.MustChangePassword = oneTime
	cur.UpdatedAt = s.now()
	if err := s.store.Users().Update(ctx, cur); err != nil {
		return err
	}
	if err := s.store.Users().RecordLogin(ctx, u.ID, true, time.Time{}); err != nil {
		return err
	}
	s.locks.reset("name:" + strings.ToLower(u.Username))
	if err := s.store.AuthSessions().DeleteUser(ctx, u.ID); err != nil {
		return err
	}
	detail := map[string]any{"via": via}
	if oneTime {
		detail["one_time"] = true
	}
	s.record(ctx, actor, "auth.password_changed", userTarget(u.ID), "ok", detail)
	return nil
}
