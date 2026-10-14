package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// cookieLen is the base64url length of a 32-byte session value.
const cookieLen = 43

// Login verifies a password under the single Argon2id slot. Unknown
// usernames run a dummy derivation so timing does not reveal them, and
// failures count against both the username and the client address.
func (s *Service) Login(ctx context.Context, username, password, ip string) (string, core.Actor, error) {
	ipKey := "ip:" + ip
	if d := s.locks.locked(ipKey, s.now()); ip != "" && d > 0 {
		return "", core.Actor{}, limited("too many failed sign-in attempts", d)
	}
	// The lockout checks, the derivation and the recorded outcome share the
	// hashing slot: otherwise a burst of concurrent guesses would all pass
	// the checks before the first failure is counted.
	if err := s.hash.acquire(ctx); err != nil {
		return "", core.Actor{}, err
	}
	u, err := s.checkLogin(ctx, username, password, ip)
	s.hash.release()
	if err != nil {
		return "", core.Actor{}, err
	}
	cookie, a, err := s.startSession(ctx, u, ip)
	if err != nil {
		return "", core.Actor{}, err
	}
	s.record(ctx, userTarget(u.ID), "auth.login", userTarget(u.ID), "ok", map[string]any{"ip": ip})
	return cookie, a, nil
}

// checkLogin runs with the hashing slot held.
func (s *Service) checkLogin(ctx context.Context, username, password, ip string) (*core.User, error) {
	now := s.now()
	ipKey, nameKey := "ip:"+ip, "name:"+strings.ToLower(username)
	if d := s.locks.locked(ipKey, now); ip != "" && d > 0 {
		return nil, limited("too many failed sign-in attempts", d)
	}
	u, err := s.store.Users().ByUsername(ctx, username)
	switch {
	case errors.Is(err, core.ErrNotFound):
		u = nil
	case err != nil:
		return nil, err
	}
	if u != nil && now.Before(u.LockedUntil) {
		return nil, limited("account temporarily locked", u.LockedUntil.Sub(now))
	}
	if d := s.locks.locked(nameKey, now); d > 0 {
		return nil, limited("account temporarily locked", d)
	}
	ok := false
	if u != nil && len(password) <= maxPassword {
		ok, err = s.hash.verifyHeld(u.PWHash, password)
		if err != nil && !errors.Is(err, errPHC) {
			return nil, err
		}
	} else {
		s.hash.dummyHeld(password)
	}
	if !ok || u.Disabled {
		s.loginFailed(ctx, u, username, ip, now)
		return nil, errCredentials
	}
	s.locks.reset(ipKey)
	s.locks.reset(nameKey)
	if u.FailedLogins > 0 || !u.LockedUntil.IsZero() {
		if err := s.store.Users().RecordLogin(ctx, u.ID, true, time.Time{}); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func (s *Service) loginFailed(ctx context.Context, u *core.User, username, ip string, now time.Time) {
	cfg := s.conf().Auth.Lockout
	target := username
	var lockIP, lockName time.Duration
	if ip != "" {
		lockIP = s.locks.fail("ip:"+ip, now, cfg)
	}
	// Known and unknown usernames share one counter with one forgiveness
	// rule, so a lockout never tells them apart; the users table only keeps
	// a known account's lock across restarts.
	lockName = s.locks.fail("name:"+strings.ToLower(username), now, cfg)
	if u != nil {
		target = userTarget(u.ID)
		var until time.Time
		if lockName > 0 {
			until = now.Add(lockName)
		}
		// Persist the failure even if the client hung up after the hash ran.
		if err := s.store.Users().RecordLogin(context.WithoutCancel(ctx), u.ID, false, until); err != nil {
			s.log.Error("record failed login", "err", err)
		}
	}
	s.record(ctx, "system", "auth.login_failed", target, "denied", map[string]any{"ip": ip})
	if lockIP > 0 || lockName > 0 {
		s.record(ctx, "system", "auth.lockout", target, "denied",
			map[string]any{"ip": ip, "ip_seconds": int(lockIP.Seconds()), "user_seconds": int(lockName.Seconds())})
	}
}

// startSession creates an auth_sessions row; the cookie value is returned
// once and only its sha256 is stored.
func (s *Service) startSession(ctx context.Context, u *core.User, ip string) (string, core.Actor, error) {
	now := s.now()
	c := s.conf().Auth
	idle, maxAge := c.SessionIdle.D(), c.SessionMax.D()
	if idle <= 0 {
		idle = 168 * time.Hour
	}
	if maxAge <= 0 {
		maxAge = 720 * time.Hour
	}
	cookie, csrf := randomToken(32), randomToken(32)
	sess := &core.AuthSession{IDHash: sha(cookie), User: u.ID, CSRF: csrf, IP: ip, CreatedAt: now, LastSeen: now,
		IdleExpires: now.Add(min(idle, maxAge)), Expires: now.Add(maxAge)}
	if err := s.store.AuthSessions().Create(ctx, sess); err != nil {
		return "", core.Actor{}, err
	}
	if err := s.store.Users().SignedIn(ctx, u.ID, now); err != nil {
		return "", core.Actor{}, err
	}
	v, err := s.viewerOf(ctx, u)
	if err != nil {
		return "", core.Actor{}, err
	}
	return cookie, core.Actor{User: u.ID, Role: u.Role, Via: "cookie", CSRF: csrf, Viewer: v, OnApproval: "deny", MustChangePassword: u.MustChangePassword}, nil
}

// FromCookie resolves a session cookie, enforcing idle and absolute expiry.
func (s *Service) FromCookie(ctx context.Context, cookie string) (core.Actor, error) {
	if len(cookie) != cookieLen {
		return core.Actor{}, errSession
	}
	repo := s.store.AuthSessions()
	h := sha(cookie)
	sess, err := repo.Get(ctx, h)
	if errors.Is(err, core.ErrNotFound) {
		return core.Actor{}, errSession
	}
	if err != nil {
		return core.Actor{}, err
	}
	now := s.now()
	if !now.Before(sess.IdleExpires) || !now.Before(sess.Expires) {
		_ = repo.Delete(ctx, h)
		return core.Actor{}, errSession
	}
	u, err := s.store.Users().Get(ctx, sess.User)
	if errors.Is(err, core.ErrNotFound) {
		_ = repo.Delete(ctx, h)
		return core.Actor{}, errSession
	}
	if err != nil {
		return core.Actor{}, err
	}
	if u.Disabled {
		_ = repo.DeleteUser(ctx, u.ID)
		return core.Actor{}, errSession
	}
	if now.Sub(sess.LastSeen) >= touchEvery {
		idle := s.conf().Auth.SessionIdle.D()
		if idle <= 0 {
			idle = 168 * time.Hour
		}
		next := now.Add(idle)
		if next.After(sess.Expires) {
			next = sess.Expires
		}
		if err := repo.Touch(ctx, h, now, next); err != nil {
			s.log.Warn("touch session", "err", err)
		}
	}
	v, err := s.viewerOf(ctx, u)
	if err != nil {
		return core.Actor{}, err
	}
	return core.Actor{User: u.ID, Role: u.Role, Via: "cookie", CSRF: sess.CSRF, Viewer: v, OnApproval: "deny", MustChangePassword: u.MustChangePassword}, nil
}

func (s *Service) Logout(ctx context.Context, cookie string) error {
	if len(cookie) != cookieLen {
		return nil
	}
	repo := s.store.AuthSessions()
	h := sha(cookie)
	sess, err := repo.Get(ctx, h)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := repo.Delete(ctx, h); err != nil {
		return err
	}
	s.record(ctx, userTarget(sess.User), "auth.logout", userTarget(sess.User), "ok", nil)
	return nil
}

// ViewerFor returns the user's own principal plus their spaces. Admin marks
// owner/admin users; repositories use it only for admin-only approvals and
// system egress rows, never for message content.
func (s *Service) ViewerFor(ctx context.Context, u core.UserID) (core.Viewer, error) {
	user, err := s.store.Users().Get(ctx, u)
	if err != nil {
		return core.Viewer{}, err
	}
	return s.viewerOf(ctx, user)
}

// viewerOf is ViewerFor for a user row already loaded by the caller (every
// authenticated request has one).
func (s *Service) viewerOf(ctx context.Context, user *core.User) (core.Viewer, error) {
	ps, err := s.store.Principals().ForUser(ctx, user.ID)
	if err != nil {
		return core.Viewer{}, err
	}
	v := core.Viewer{User: user.ID, Admin: user.Role.IsAdmin(), Principals: make([]core.PrincipalID, 0, len(ps))}
	for _, p := range ps {
		v.Principals = append(v.Principals, p.ID)
	}
	return v, nil
}
