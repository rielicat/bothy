package auth

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestCreateOwnerNamed(t *testing.T) {
	e := newEnv(t)
	tok, _, err := e.s.EnsureSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := e.s.ClaimSetup(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.CreateOwnerNamed(ctx, grant, "maya", "bad\x00name", goodPassword, "UTC"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("control character in the name: %v", err)
	}
	_, a, err := e.s.CreateOwnerNamed(ctx, grant, "maya", "  Maya Chen ", goodPassword, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := e.store.Users().Get(ctx, a.User)
	if u.DisplayName != "Maya Chen" || u.Role != core.RoleOwner || u.MustChangePassword {
		t.Fatalf("owner = %+v", u)
	}
}

var oneTimeRe = regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]{4}(-[0-9a-hjkmnp-tv-z]{4}){3,}$`)

func TestOneTimePassword(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		pw := oneTimePassword(12)
		if !oneTimeRe.MatchString(pw) || seen[pw] || checkPassword(pw, 12) != nil {
			t.Fatalf("one-time password %q", pw)
		}
		seen[pw] = true
	}
	if pw := oneTimePassword(30); len(pw) < 30 || !oneTimeRe.MatchString(pw) {
		t.Fatalf("long minimum: %q", pw)
	}
}

func TestCreateUserOneTimeAndFirstChange(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	u, pw, err := e.s.CreateUserOneTime(ctx, owner, "jordan", "Jordan Kim", core.RoleMember)
	if err != nil || pw == "" || !u.MustChangePassword || u.DisplayName != "Jordan Kim" {
		t.Fatalf("CreateUserOneTime = %+v %q %v", u, pw, err)
	}
	cookie, a, err := e.s.Login(ctx, "jordan", pw, "10.0.0.5")
	if err != nil || !a.MustChangePassword {
		t.Fatalf("login = %+v %v", a, err)
	}
	if a, err := e.s.FromCookie(ctx, cookie); err != nil || !a.MustChangePassword {
		t.Fatalf("FromCookie = %+v %v", a, err)
	}
	// The admin knows the one-time password, so it cannot become the
	// account's own.
	if err := e.s.SetPassword(ctx, u.ID, "", pw); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("keeping the one-time password: %v", err)
	}
	if got, _ := e.store.Users().Get(ctx, u.ID); !got.MustChangePassword {
		t.Fatal("a refused change cleared the flag")
	}
	// The one-time password replaces the old one: no old password needed.
	if err := e.s.SetPassword(ctx, u.ID, "", "harbour lantern quietly folds"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.Users().Get(ctx, u.ID)
	if got.MustChangePassword {
		t.Fatal("the flag survived")
	}
	if _, err := e.s.FromCookie(ctx, cookie); err == nil {
		t.Fatal("the one-time session survived the change")
	}
	if err := e.s.SetPassword(ctx, u.ID, "", "another fine long phrase"); err == nil {
		t.Fatal("an empty old password worked without the flag")
	}
	member := core.Actor{User: u.ID, Role: core.RoleMember, Via: "cookie"}
	if _, _, err := e.s.CreateUserOneTime(ctx, member, "x", "", core.RoleMember); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("member creating users: %v", err)
	}
	if !strings.Contains(lastAudit(e, "user.create"), `"one_time_password":true`) {
		t.Fatalf("audit %s", lastAudit(e, "user.create"))
	}
}

func lastAudit(e *env, action string) string {
	var out string
	for _, a := range e.audit.All() {
		if a.Action == action {
			out = string(a.Detail)
		}
	}
	return out
}

func TestResetUserPasswordRules(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	admin := e.user(t, owner, "ada", core.RoleAdmin)
	other := e.user(t, owner, "otto", core.RoleAdmin)
	member := e.user(t, owner, "mel", core.RoleMember)
	adminActor := core.Actor{User: admin.ID, Role: core.RoleAdmin, Via: "cookie"}
	cookie, _, err := e.s.Login(ctx, "mel", goodPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	pw, err := e.s.ResetUserPassword(ctx, adminActor, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromCookie(ctx, cookie); err == nil {
		t.Fatal("reset kept the member signed in")
	}
	if _, a, err := e.s.Login(ctx, "mel", pw, ""); err != nil || !a.MustChangePassword {
		t.Fatalf("login with the reset password = %+v %v", a, err)
	}
	for _, tc := range []struct {
		by     core.Actor
		target core.UserID
	}{
		{adminActor, owner.User},
		{adminActor, other.ID},
		{adminActor, admin.ID},
		{owner, owner.User},
		{core.Actor{User: member.ID, Role: core.RoleMember, Via: "cookie"}, admin.ID},
	} {
		if _, err := e.s.ResetUserPassword(ctx, tc.by, tc.target); !errors.Is(err, core.ErrForbidden) {
			t.Errorf("%s resetting %s: %v", tc.by.User, tc.target, err)
		}
	}
	if _, err := e.s.ResetUserPassword(ctx, owner, other.ID); err != nil {
		t.Fatalf("owner resetting an admin: %v", err)
	}
	if !strings.Contains(lastAudit(e, "auth.password_changed"), `"one_time":true`) {
		t.Fatalf("audit %s", lastAudit(e, "auth.password_changed"))
	}
}
