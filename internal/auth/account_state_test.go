package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// Someone who types their real password into a turned-off account is told
// so, and retrying never locks them out.
func TestTurnedOffSignInSaysSoWithoutLockout(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	m := e.user(t, owner, "mel", core.RoleMember)
	up := *m
	up.Disabled = true
	if err := e.s.UpdateUser(ctx, owner, &up); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		_, _, err := e.s.Login(ctx, "mel", goodPassword, "10.0.0.9")
		if !isUnauthenticated(err) || err.Error() != "this account is turned off" {
			t.Fatalf("turned-off sign-in = %v", err)
		}
	}
	if u, _ := e.store.Users().Get(ctx, m.ID); u.FailedLogins != 0 || !u.LockedUntil.IsZero() {
		t.Fatalf("correct password counted: %d failures, locked until %v", u.FailedLogins, u.LockedUntil)
	}
	if _, _, err := e.s.Login(ctx, "mel", "not the password at all", "10.0.0.9"); err == nil || err.Error() != "invalid username or password" {
		t.Fatalf("wrong password on a turned-off account = %v", err)
	}
}

func TestResetRefusesATurnedOffAccount(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	m := e.user(t, owner, "mel", core.RoleMember)
	up := *m
	up.Disabled = true
	if err := e.s.UpdateUser(ctx, owner, &up); err != nil {
		t.Fatal(err)
	}
	if pw, err := e.s.ResetUserPassword(ctx, owner, m.ID); !errors.Is(err, core.ErrInvalid) || pw != "" {
		t.Fatalf("reset of a turned-off account = %q, %v", pw, err)
	}
}

func TestDemotionRevokesAdminTokens(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	a := e.user(t, owner, "ada", core.RoleAdmin)
	_, aa, err := e.s.Login(ctx, "ada", goodPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	adminTok, _, err := e.s.CreateToken(ctx, aa, core.TokenSpec{Name: "ops", Scopes: []string{"admin", "chat"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	chatTok, _, err := e.s.CreateToken(ctx, aa, core.TokenSpec{Name: "chat", Scopes: []string{"chat"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	up := *a
	up.Role = core.RoleMember
	if err := e.s.UpdateUser(ctx, owner, &up); err != nil {
		t.Fatal(err)
	}
	up.Role = core.RoleAdmin
	if err := e.s.UpdateUser(ctx, owner, &up); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromBearer(ctx, adminTok); !isUnauthenticated(err) {
		t.Fatalf("admin token after demotion and promotion: %v", err)
	}
	if _, err := e.s.FromBearer(ctx, chatTok); err != nil {
		t.Fatalf("chat token after demotion: %v", err)
	}
}

func TestTokenLifetimeBounds(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	for _, ttl := range []time.Duration{90, time.Second, 401 * 24 * time.Hour, 9000000000000000000} {
		if _, _, err := e.s.CreateToken(ctx, owner, core.TokenSpec{Name: "t", Scopes: []string{"read"}, TTL: ttl}); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("ttl %v: %v", ttl, err)
		}
	}
	for _, ttl := range []time.Duration{time.Minute, 365 * 24 * time.Hour} {
		if _, _, err := e.s.CreateToken(ctx, owner, core.TokenSpec{Name: "t", Scopes: []string{"read"}, TTL: ttl}); err != nil {
			t.Errorf("ttl %v: %v", ttl, err)
		}
	}
}
