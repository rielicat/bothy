package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// staleUsers runs after once, right after Get hands out its copy: an admin
// change landing between a profile update's read and its write.
type staleUsers struct {
	core.UserRepo
	after func()
}

func (u *staleUsers) Get(ctx context.Context, id core.UserID) (*core.User, error) {
	got, err := u.UserRepo.Get(ctx, id)
	if f := u.after; f != nil {
		u.after = nil
		f()
	}
	return got, err
}

type staleStore struct {
	core.Store
	users *staleUsers
}

func (s staleStore) Users() core.UserRepo { return s.users }

// A rename racing an admin's turn-off, demotion or password reset never
// writes the old role, flag or hash back.
func TestUpdateProfileKeepsConcurrentAdminChanges(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	admin := e.user(t, owner, "jordan", core.RoleAdmin)
	users := &staleUsers{UserRepo: e.store.Users()}
	s, err := New(Deps{Store: staleStore{Store: e.store, users: users}, Config: config.Static(e.cfg), Audit: e.audit, Bus: e.bus,
		Redactor: fakes.NewRedactor(), DataDir: e.dir, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	users.after = func() {
		u, err := e.store.Users().Get(ctx, admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		u.Role, u.Disabled, u.PWHash, u.MustChangePassword = core.RoleMember, true, "reset-hash", true
		if err := e.store.Users().Update(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	by := core.Actor{User: admin.ID, Role: core.RoleAdmin, Via: "cookie"}
	if _, err := s.UpdateProfile(ctx, by, ProfileUpdate{DisplayName: new("Jordan Kim")}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.Users().Get(ctx, admin.ID)
	if got.DisplayName != "Jordan Kim" || got.Role != core.RoleMember || !got.Disabled || got.PWHash != "reset-hash" || !got.MustChangePassword {
		t.Fatalf("after the rename = %+v; the admin's change was undone", got)
	}
}

// Nobody renames themselves to a colleague's name or username, or hides a
// bidi override in their name.
func TestUpdateProfileRefusesLookalikeNames(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	maya := e.user(t, owner, "maya", core.RoleMember)
	if err := e.s.UpdateUser(ctx, owner, &core.User{ID: maya.ID, DisplayName: "Maya Chen", Role: core.RoleMember}); err != nil {
		t.Fatal(err)
	}
	sam := e.user(t, owner, "sam", core.RoleMember)
	by := core.Actor{User: sam.ID, Role: core.RoleMember, Via: "cookie"}
	for _, name := range []string{"maya chen", "Maya Chen", "OWNER", "Maya", "Sam‮admin", "Sam​"} {
		if _, err := e.s.UpdateProfile(ctx, by, ProfileUpdate{DisplayName: new(name)}); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("rename to %q: %v", name, err)
		}
	}
	if u, err := e.s.UpdateProfile(ctx, by, ProfileUpdate{DisplayName: new("Sam")}); err != nil || u.DisplayName != "Sam" {
		t.Fatalf("own username as a name = %+v %v", u, err)
	}
	if u, err := e.s.UpdateProfile(ctx, by, ProfileUpdate{DisplayName: new("Sam Patel")}); err != nil || u.DisplayName != "Sam Patel" {
		t.Fatalf("rename = %+v %v", u, err)
	}
}

// People set their own time zone; only IANA names are kept, so a schedule
// never quietly runs on UTC or this server's clock.
func TestUpdateProfileTimezone(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi"; c.Timezone = "Europe/London" })
	owner := e.owner(t)
	sam := e.user(t, owner, "sam", core.RoleMember)
	if sam.Timezone != "Europe/London" {
		t.Fatalf("new account zone = %q; want the configured one", sam.Timezone)
	}
	by := core.Actor{User: sam.ID, Role: core.RoleMember, Via: "cookie"}
	for _, tz := range []string{"", "  ", "Local", "local", "Mars/Olympus", "../../etc/passwd", "Europe/" + strings.Repeat("x", 64)} {
		if _, err := e.s.UpdateProfile(ctx, by, ProfileUpdate{Timezone: new(tz)}); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("time zone %q: %v", tz, err)
		}
	}
	u, err := e.s.UpdateProfile(ctx, by, ProfileUpdate{Timezone: new(" America/Santiago ")})
	if err != nil || u.Timezone != "America/Santiago" {
		t.Fatalf("set time zone = %+v %v", u, err)
	}
	if u, err := e.s.UpdateProfile(ctx, by, ProfileUpdate{DisplayName: new("Sam Patel")}); err != nil || u.Timezone != "America/Santiago" {
		t.Fatalf("a rename changed the time zone: %+v %v", u, err)
	}
	audited := false
	for _, a := range e.audit.All() {
		audited = audited || a.Action == "user.profile" && strings.Contains(string(a.Detail), `"timezone":"America/Santiago"`)
	}
	if !audited {
		t.Fatalf("time zone change not audited: %+v", e.audit.All())
	}
}
