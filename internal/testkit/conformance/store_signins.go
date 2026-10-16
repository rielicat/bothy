package conformance

import (
	"errors"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// testSignIns pins core.SignInStore, which is optional (the suite skips it):
// one link per (provider, subject) and per (user, provider), no Viewer, and
// the users row cascading to its links.
func testSignIns(f *fx) {
	ss, ok := f.s.(core.SignInStore)
	if !ok {
		f.t.Skip("store keeps no sign-in links")
	}
	r := ss.SignIns()
	alice, bob := f.user(core.RoleMember), f.user(core.RoleMember)

	g := &core.SignIn{Provider: core.SignInGoogle, Subject: "1100229384756", User: alice.ID, Email: "alice@acme.com",
		CreatedAt: at(1).Add(123456)}
	f.must(r.Create(f.ctx, g), "Create")
	got, err := r.Get(f.ctx, core.SignInGoogle, "1100229384756")
	f.must(err, "Get")
	want := *g
	want.CreatedAt = at(1)
	f.eq("round trip at millisecond precision", got, &want)
	f.check(got.LastUsed.IsZero(), "a zero LastUsed came back as %v", got.LastUsed)

	f.want(r.Create(f.ctx, &core.SignIn{Provider: core.SignInGoogle, Subject: "1100229384756", User: bob.ID, CreatedAt: at(2)}),
		core.ErrConflict, "the same provider account for someone else")
	f.want(r.Create(f.ctx, &core.SignIn{Provider: core.SignInGoogle, Subject: "999", User: alice.ID, CreatedAt: at(2)}),
		core.ErrConflict, "a second Google account for one person")
	gh := &core.SignIn{Provider: core.SignInGitHub, Subject: "583231", User: alice.ID, Login: "alice-gh", CreatedAt: at(3), LastUsed: at(4)}
	f.must(r.Create(f.ctx, gh), "Google and GitHub for one person")

	for name, bad := range map[string]*core.SignIn{
		"unknown user":   {Provider: core.SignInGitHub, Subject: "1", User: core.UserID(core.NewID(core.PrefixUser)), CreatedAt: at(5)},
		"provider okta":  {Provider: "okta", Subject: "1", User: bob.ID, CreatedAt: at(5)},
		"empty subject":  {Provider: core.SignInGitHub, User: bob.ID, CreatedAt: at(5)},
		"long subject":   {Provider: core.SignInGitHub, Subject: strings.Repeat("1", 256), User: bob.ID, CreatedAt: at(5)},
		"321-char email": {Provider: core.SignInGitHub, Subject: "2", User: bob.ID, Email: strings.Repeat("e", 321), CreatedAt: at(5)},
		"65-char login":  {Provider: core.SignInGitHub, Subject: "3", User: bob.ID, Login: strings.Repeat("l", 65), CreatedAt: at(5)},
	} {
		f.want(r.Create(f.ctx, bad), core.ErrInvalid, name)
	}

	list, err := r.ForUser(f.ctx, alice.ID)
	f.must(err, "ForUser")
	f.need(len(list) == 2, "ForUser = %d links, want 2", len(list))
	f.check(list[0].Provider == core.SignInGitHub && list[1].Provider == core.SignInGoogle, "ForUser order %s, %s", list[0].Provider, list[1].Provider)
	none, err := r.ForUser(f.ctx, bob.ID)
	f.must(err, "ForUser without links")
	f.check(none != nil && len(none) == 0, "ForUser of someone without links = %#v", none)

	f.must(r.Touch(f.ctx, core.SignInGitHub, "583231", at(10), "alice@acme.com", "alice-renamed"), "Touch")
	got, err = r.Get(f.ctx, core.SignInGitHub, "583231")
	f.must(err, "Get after Touch")
	f.check(got.LastUsed.Equal(at(10)) && got.Email == "alice@acme.com" && got.Login == "alice-renamed" && got.CreatedAt.Equal(at(3)),
		"after Touch = %+v", got)
	f.want(r.Touch(f.ctx, core.SignInGitHub, "nope", at(10), "", ""), core.ErrNotFound, "Touch of a missing link")

	got.Login = "mutated"
	again, err := r.Get(f.ctx, core.SignInGitHub, "583231")
	f.must(err, "Get")
	f.check(again.Login == "alice-renamed", "mutating a returned link changed the store: %q", again.Login)
	list[0].Email = "mutated"
	list, err = r.ForUser(f.ctx, alice.ID)
	f.must(err, "ForUser")
	f.check(list[0].Email == "alice@acme.com", "mutating a listed link changed the store: %q", list[0].Email)

	f.want(r.Delete(f.ctx, bob.ID, core.SignInGoogle), core.ErrNotFound, "Delete of another person's link")
	_, err = r.Get(f.ctx, core.SignInGoogle, "1100229384756")
	f.must(err, "a refused Delete keeps the link")
	f.must(r.Delete(f.ctx, alice.ID, core.SignInGoogle), "Delete")
	_, err = r.Get(f.ctx, core.SignInGoogle, "1100229384756")
	f.want(err, core.ErrNotFound, "Get after Delete")
	f.want(r.Delete(f.ctx, alice.ID, core.SignInGoogle), core.ErrNotFound, "second Delete")

	stop := errors.New("stop")
	err = f.s.Tx(f.ctx, func(tx core.Store) error {
		txs, ok := tx.(core.SignInStore)
		if !ok {
			return errors.New("the Tx store is not a SignInStore")
		}
		if err := txs.SignIns().Create(f.ctx, &core.SignIn{Provider: core.SignInGoogle, Subject: "rolled", User: bob.ID, CreatedAt: at(20)}); err != nil {
			return err
		}
		return stop
	})
	f.want(err, stop, "Tx returns fn's error")
	_, err = r.Get(f.ctx, core.SignInGoogle, "rolled")
	f.want(err, core.ErrNotFound, "a failed Tx leaves no link")

	f.must(f.s.Users().Delete(f.ctx, alice.ID), "Users.Delete")
	_, err = r.Get(f.ctx, core.SignInGitHub, "583231")
	f.want(err, core.ErrNotFound, "links after Users.Delete")
}
