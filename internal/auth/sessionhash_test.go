package auth

import (
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// A flow that kept only the session's hash resumes it under the same rules
// as the cookie: expiry, a disabled user and an unknown or malformed hash
// all refuse.
func TestFromSessionHash(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Mode = "multi"
		c.Auth.SessionIdle = config.Duration(time.Hour)
		c.Auth.SessionMax = config.Duration(3 * time.Hour)
	})
	owner := e.owner(t)
	m := e.user(t, owner, "member", core.RoleMember)
	cookie, _, err := e.s.Login(ctx, "member", goodPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.s.FromSessionHash(ctx, sha(cookie))
	if err != nil || a.User != m.ID || a.Via != "cookie" || a.CSRF == "" {
		t.Fatalf("FromSessionHash = %+v %v", a, err)
	}
	for _, h := range [][]byte{nil, sha(cookie)[:31], append(sha(cookie), 0), sha("someone else's cookie")} {
		if _, err := e.s.FromSessionHash(ctx, h); !isUnauthenticated(err) {
			t.Errorf("hash %x: %v", h, err)
		}
	}

	e.clock.Advance(61 * time.Minute)
	if _, err := e.s.FromSessionHash(ctx, sha(cookie)); !isUnauthenticated(err) {
		t.Fatalf("idle expiry: %v", err)
	}
	cookie, _, _ = e.s.Login(ctx, "member", goodPassword, "127.0.0.1")
	for i := 0; i < 3; i++ {
		e.clock.Advance(50 * time.Minute)
		if _, err := e.s.FromSessionHash(ctx, sha(cookie)); err != nil {
			t.Fatalf("activity keeps the session alive (step %d): %v", i, err)
		}
	}
	e.clock.Advance(50 * time.Minute)
	if _, err := e.s.FromSessionHash(ctx, sha(cookie)); !isUnauthenticated(err) {
		t.Fatalf("absolute expiry: %v", err)
	}

	cookie, _, _ = e.s.Login(ctx, "member", goodPassword, "127.0.0.1")
	m.Disabled = true
	if err := e.s.UpdateUser(ctx, owner, m); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromSessionHash(ctx, sha(cookie)); !isUnauthenticated(err) {
		t.Fatalf("disabled user: %v", err)
	}
}
