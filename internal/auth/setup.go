package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// Meta keys (SPEC §6). The setup token and grant are stored only as sha256
// hex and re-read on every use, so a rotation by `bothy setup-token` in
// another process takes effect at once.
const (
	metaTokenHash = "setup_token_hash"
	metaTokenExp  = "setup_token_exp"
	metaGrantHash = "setup_grant_hash"
	metaGrantExp  = "setup_grant_exp"
	metaGrantCSRF = "setup_grant_csrf"
	metaSetupDone = "setup_done"

	setupTokenFile = "setup-token"
	setupGrantTTL  = 30 * time.Minute
	redactSetupID  = "auth:setup-token"
)

func (s *Service) setupDone(ctx context.Context, m core.MetaRepo) (bool, error) {
	_, err := m.Get(ctx, metaSetupDone)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) ownerExists(ctx context.Context, st core.Store) (bool, error) {
	users, err := st.Users().List(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.Role == core.RoleOwner {
			return true, nil
		}
	}
	return false, nil
}

// NeedsSetup reports whether the first-run wizard has not been completed:
// setup routes stay available (to local peers) until CompleteSetup.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	done, err := s.setupDone(ctx, s.store.Meta())
	return !done, err
}

// EnsureSetupToken mints the one-time setup token while no owner exists and
// no valid token is on disk. It returns created=false when an unexpired token
// already exists, so the banner prints only for a fresh token.
func (s *Service) EnsureSetupToken(ctx context.Context) (string, bool, error) {
	owner, err := s.ownerExists(ctx, s.store)
	if err != nil {
		return "", false, err
	}
	if owner {
		s.removeTokenFile()
		return "", false, nil
	}
	m := s.store.Meta()
	hash, _ := m.Get(ctx, metaTokenHash)
	if hash != "" && s.now().Before(metaTime(ctx, m, metaTokenExp)) && s.tokenFileExists() {
		return "", false, nil
	}
	tok, err := s.mintSetupToken(ctx)
	if err != nil {
		return "", false, err
	}
	return tok, true, nil
}

// RotateSetupToken replaces the setup token (CLI `bothy setup-token`).
func (s *Service) RotateSetupToken(ctx context.Context) (string, error) {
	owner, err := s.ownerExists(ctx, s.store)
	if err != nil {
		return "", err
	}
	if owner {
		return "", fmt.Errorf("%w: an owner already exists; sign in instead", core.ErrConflict)
	}
	return s.mintSetupToken(ctx)
}

func (s *Service) mintSetupToken(ctx context.Context) (string, error) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	tok := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	ttl := s.conf().Auth.SetupTokenTTL.D()
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	exp := s.now().Add(ttl)
	if err := s.writeTokenFile(tok); err != nil {
		return "", err
	}
	err := s.store.Tx(ctx, func(tx core.Store) error {
		m := tx.Meta()
		if err := m.Set(ctx, metaTokenHash, hex.EncodeToString(sha(tok))); err != nil {
			return err
		}
		return m.Set(ctx, metaTokenExp, strconv.FormatInt(exp.UnixMilli(), 10))
	})
	if err != nil {
		return "", err
	}
	if s.redactor != nil {
		s.redactor.Add(redactSetupID, []byte(tok))
	}
	return tok, nil
}

func (s *Service) tokenPath() string { return filepath.Join(s.dataDir, setupTokenFile) }

func (s *Service) tokenFileExists() bool {
	if s.dataDir == "" {
		return true
	}
	_, err := os.Stat(s.tokenPath())
	return err == nil
}

func (s *Service) writeTokenFile(tok string) error {
	if s.dataDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dataDir, ".setup-token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(tok + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.tokenPath())
}

func (s *Service) removeTokenFile() {
	if s.dataDir == "" {
		return
	}
	if err := os.Remove(s.tokenPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("remove setup token file", "err", err)
	}
}

func metaTime(ctx context.Context, m core.MetaRepo, k string) time.Time {
	v, err := m.Get(ctx, k)
	if err != nil {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// hashMatches compares sha256(secret) with a stored hex digest in constant time.
func hashMatches(secret, storedHex string) bool {
	want, err := hex.DecodeString(storedHex)
	if err != nil || len(want) != 32 {
		return false
	}
	return subtle.ConstantTimeCompare(sha(secret), want) == 1
}

// ClaimSetup exchanges the setup token for a 30-minute setup grant. The token
// is single-use: a successful claim deletes it.
func (s *Service) ClaimSetup(ctx context.Context, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 128 {
		return "", errSetupToken
	}
	grant, csrf := randomToken(32), randomToken(32)
	now := s.now()
	err := s.store.Tx(ctx, func(tx core.Store) error {
		m := tx.Meta()
		done, err := s.setupDone(ctx, m)
		if err != nil {
			return err
		}
		owner, err := s.ownerExists(ctx, tx)
		if err != nil {
			return err
		}
		if done || owner {
			return errSetupToken
		}
		hash, _ := m.Get(ctx, metaTokenHash)
		if !hashMatches(token, hash) || !now.Before(metaTime(ctx, m, metaTokenExp)) {
			return errSetupToken
		}
		for _, k := range []string{metaTokenHash, metaTokenExp} {
			if err := m.Delete(ctx, k); err != nil {
				return err
			}
		}
		for k, v := range map[string]string{
			metaGrantHash: hex.EncodeToString(sha(grant)),
			metaGrantCSRF: csrf,
			metaGrantExp:  strconv.FormatInt(now.Add(setupGrantTTL).UnixMilli(), 10),
		} {
			if err := m.Set(ctx, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.record(ctx, "system", "auth.setup_claim", "", "denied", nil)
		return "", err
	}
	s.record(ctx, "system", "auth.setup_claim", "", "ok", nil)
	return grant, nil
}

// SetupGrant validates a setup grant cookie and returns its CSRF token. It is
// not part of core.Auth; httpapi reaches it through an interface assertion.
func (s *Service) SetupGrant(ctx context.Context, grant string) (string, error) {
	return s.checkGrant(ctx, s.store.Meta(), grant)
}

func (s *Service) checkGrant(ctx context.Context, m core.MetaRepo, grant string) (string, error) {
	if grant == "" || len(grant) > 128 {
		return "", errSetupGrant
	}
	hash, _ := m.Get(ctx, metaGrantHash)
	if !hashMatches(grant, hash) || !s.now().Before(metaTime(ctx, m, metaGrantExp)) {
		return "", errSetupGrant
	}
	csrf, err := m.Get(ctx, metaGrantCSRF)
	if err != nil {
		return "", errSetupGrant
	}
	return csrf, nil
}

func clearSetupMeta(ctx context.Context, m core.MetaRepo) error {
	for _, k := range []string{metaTokenHash, metaTokenExp, metaGrantHash, metaGrantExp, metaGrantCSRF} {
		if err := m.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// CreateOwner creates the owner account with a valid setup grant and signs
// them in.
func (s *Service) CreateOwner(ctx context.Context, grantCookie, username, password, timezone string) (string, core.Actor, error) {
	return s.CreateOwnerNamed(ctx, grantCookie, username, "", password, timezone)
}

// CreateOwnerNamed is CreateOwner with the owner's display name ("" = the
// username). It is not part of core.Auth; httpapi reaches it through an
// interface assertion.
func (s *Service) CreateOwnerNamed(ctx context.Context, grantCookie, username, displayName, password, timezone string) (string, core.Actor, error) {
	if _, err := s.checkGrant(ctx, s.store.Meta(), grantCookie); err != nil {
		return "", core.Actor{}, err
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if err := validUsername(username); err != nil {
		return "", core.Actor{}, err
	}
	if displayName = strings.TrimSpace(displayName); displayName == "" {
		displayName = username
	}
	if err := validDisplayName(displayName); err != nil {
		return "", core.Actor{}, err
	}
	if err := validTimezone(timezone); err != nil {
		return "", core.Actor{}, err
	}
	if err := checkPassword(password, s.conf().Auth.PasswordMinLength); err != nil {
		return "", core.Actor{}, err
	}
	phc, err := s.hash.hash(ctx, password)
	if err != nil {
		return "", core.Actor{}, err
	}
	now := s.now()
	u := &core.User{Username: username, DisplayName: displayName, Role: core.RoleOwner, PWHash: phc, Timezone: timezone,
		CreatedAt: now, UpdatedAt: now}
	err = s.store.Tx(ctx, func(tx core.Store) error {
		if _, err := s.checkGrant(ctx, tx.Meta(), grantCookie); err != nil {
			return err
		}
		n, err := tx.Users().Count(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: an owner already exists", core.ErrConflict)
		}
		if err := tx.Users().Create(ctx, u); err != nil {
			return err
		}
		return clearSetupMeta(ctx, tx.Meta())
	})
	if err != nil {
		return "", core.Actor{}, err
	}
	s.removeTokenFile()
	if s.redactor != nil {
		s.redactor.Remove(redactSetupID)
	}
	s.record(ctx, userTarget(u.ID), "auth.owner_created", userTarget(u.ID), "ok", map[string]any{"username": u.Username})
	cookie, a, err := s.startSession(ctx, u, "")
	return cookie, a, err
}

// CompleteSetup ends the first-run wizard; setup routes answer 404 afterwards.
func (s *Service) CompleteSetup(ctx context.Context, a core.Actor) error {
	if !a.Role.IsAdmin() {
		return fmt.Errorf("%w: only an admin can complete setup", core.ErrForbidden)
	}
	err := s.store.Tx(ctx, func(tx core.Store) error {
		m := tx.Meta()
		if done, err := s.setupDone(ctx, m); err != nil || done {
			return err
		}
		owner, err := s.ownerExists(ctx, tx)
		if err != nil {
			return err
		}
		if !owner {
			return fmt.Errorf("%w: create the owner account first", core.ErrConflict)
		}
		if err := clearSetupMeta(ctx, m); err != nil {
			return err
		}
		return m.Set(ctx, metaSetupDone, strconv.FormatInt(s.now().UnixMilli(), 10))
	})
	if err != nil {
		return err
	}
	s.removeTokenFile()
	if s.redactor != nil {
		s.redactor.Remove(redactSetupID)
	}
	return nil
}
