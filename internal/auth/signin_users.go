package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/rielicat/bothy/internal/auth/oauth"
	"github.com/rielicat/bothy/internal/core"
)

// createFromSignIn makes a member account without a password for someone
// on the sign-in list who signed in with p for the first time, and links p
// to it. If a concurrent callback linked the same provider account first,
// that account is returned with created false.
func (s *Service) createFromSignIn(ctx context.Context, p core.SignInProvider, id oauth.Identity) (*core.User, bool, error) {
	for attempt := 0; ; attempt++ {
		u, created, err := s.createFromSignInTx(ctx, p, id)
		// A lost race on the link or the username: the second pass sees the
		// winner's link, or picks the next free name.
		if errors.Is(err, core.ErrConflict) && attempt == 0 {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if created {
			s.record(ctx, "system", "user.create", userTarget(u.ID), "ok",
				map[string]any{"username": u.Username, "role": core.RoleMember, "via": string(p)})
		}
		return u, created, nil
	}
}

func (s *Service) createFromSignInTx(ctx context.Context, p core.SignInProvider, id oauth.Identity) (*core.User, bool, error) {
	var out *core.User
	created := false
	err := s.store.Tx(ctx, func(tx core.Store) error {
		ss, ok := tx.(core.SignInStore)
		if !ok {
			return fmt.Errorf("sign-in links: %w", core.ErrNotImplemented)
		}
		repo := ss.SignIns()
		if link, err := repo.Get(ctx, p, id.Subject); err == nil {
			u, err := tx.Users().Get(ctx, link.User)
			out = u
			return err
		} else if !errors.Is(err, core.ErrNotFound) {
			return err
		}
		users, err := tx.Users().List(ctx)
		if err != nil {
			return err
		}
		username := freeUsername(users, usernameBase(p, id))
		now := s.now()
		tz := s.conf().Timezone
		if tz == "" {
			tz = "UTC"
		}
		u := &core.User{Username: username, DisplayName: signInDisplayName(users, p, id, username), Role: core.RoleMember,
			PWHash: noPassword, Timezone: tz, CreatedAt: now, UpdatedAt: now}
		if err := tx.Users().Create(ctx, u); err != nil {
			return err
		}
		if err := repo.Create(ctx, &core.SignIn{Provider: p, Subject: id.Subject, User: u.ID, Email: id.Email, Login: id.Login,
			CreatedAt: now, LastUsed: now}); err != nil {
			return err
		}
		if out, err = tx.Users().Get(ctx, u.ID); err != nil {
			return err
		}
		created = true
		return nil
	})
	return out, created, err
}

// usernameBase derives a username from the email's local part (Google) or
// the login (GitHub): lower case, other characters as '-', at most 27 so
// freeUsername's longest suffix ("-k7qx") still fits the 32 allowed.
func usernameBase(p core.SignInProvider, id oauth.Identity) string {
	raw := id.Login
	if p == core.SignInGoogle {
		raw, _, _ = strings.Cut(id.Email, "@")
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(raw) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !ok || r == '-' {
			if !dash {
				b.WriteByte('-')
			}
			dash = true
			continue
		}
		dash = false
		b.WriteRune(r)
	}
	base := strings.TrimLeft(b.String(), "._-")
	if len(base) > 27 {
		base = base[:27]
	}
	base = strings.TrimRight(base, "._-")
	if base == "" {
		return "user"
	}
	return base
}

// freeUsername picks base, base-2 … base-99, then base-<4 random
// characters>, whichever nobody has.
func freeUsername(users []*core.User, base string) string {
	taken := map[string]bool{}
	for _, u := range users {
		taken[strings.ToLower(u.Username)] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; n <= 99; n++ {
		if c := fmt.Sprintf("%s-%d", base, n); !taken[c] {
			return c
		}
	}
	for {
		var r [4]byte
		_, _ = rand.Read(r[:])
		c := base + "-"
		for _, x := range r {
			c += string(oneTimeAlphabet[x&31])
		}
		if !taken[c] {
			return c
		}
	}
}

// signInDisplayName is the provider's profile name when it is a valid
// display name nobody here goes by: the name is chosen by the person, and
// approval cards and vote prompts name who asked, so it must not pass as a
// colleague. Otherwise the username.
func signInDisplayName(users []*core.User, p core.SignInProvider, id oauth.Identity, username string) string {
	name := strings.TrimSpace(id.Name)
	if name == "" {
		name = id.Login
		if p == core.SignInGoogle {
			name, _, _ = strings.Cut(id.Email, "@")
		}
	}
	if name == "" || validDisplayName(name) != nil {
		return username
	}
	for _, u := range users {
		if strings.EqualFold(strings.TrimSpace(u.DisplayName), name) || strings.EqualFold(u.Username, name) {
			return username
		}
	}
	return name
}
