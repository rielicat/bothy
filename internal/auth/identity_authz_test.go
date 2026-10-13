package auth

import (
	"errors"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// linkOwn links a chat account the way its user does: a link_identity code
// from the web UI sent as /pair CODE.
func linkOwn(t *testing.T, e *env, by core.Actor, ident core.ChannelIdentity) {
	t.Helper()
	pc, err := e.s.NewPairingCode(ctx, by)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.RedeemPairing(ctx, ident, pc.Code); err != nil {
		t.Fatal(err)
	}
}

func resolved(t *testing.T, e *env, channel, ext string) core.IdentityResolution {
	t.Helper()
	res, err := e.s.ResolveIdentity(ctx, channel, ext)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Linking hands a chat account someone's principal and role, so an admin can
// link one only to themselves; only the owner may link to somebody else, and
// an account another user linked can never be moved to the admin.
func TestAdminCannotLinkChatIdentityToOthers(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	ada := e.user(t, owner, "ada", core.RoleAdmin)
	bob := e.user(t, owner, "bob", core.RoleMember)
	admin := core.Actor{User: ada.ID, Role: core.RoleAdmin, Via: "cookie"}

	n, err := e.s.SenderNotice(ctx, core.ChannelIdentity{Channel: "telegram", ExternalID: "900", DMChat: "900"})
	if err != nil || !n.Reply {
		t.Fatalf("notice = %+v, %v", n, err)
	}
	for _, target := range []core.UserID{owner.User, bob.ID} {
		if _, err := e.s.ApproveSender(ctx, admin, n.Code.Code, "link", target); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("admin linking a sender to %s: %v", target, err)
		}
		if _, err := e.s.SetIdentityStatus(ctx, admin, "telegram", "900", core.IdentityLinked, target); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("admin setting a sender linked to %s: %v", target, err)
		}
	}
	if res := resolved(t, e, "telegram", "900"); res.Status != core.IdentityPending {
		t.Fatalf("refused link changed the identity: %+v", res)
	}
	if id, err := e.s.ApproveSender(ctx, admin, n.Code.Code, "link", ada.ID); err != nil || id.User != ada.ID {
		t.Fatalf("admin linking a sender to themselves = %+v, %v", id, err)
	}

	bobActor := core.Actor{User: bob.ID, Role: core.RoleMember, Via: "cookie"}
	linkOwn(t, e, bobActor, core.ChannelIdentity{Channel: "telegram", ExternalID: "42", DMChat: "42"})
	for _, tc := range []struct {
		by     core.Actor
		status core.IdentityStatus
		uid    core.UserID
	}{
		{admin, core.IdentityGuest, ""},
		{admin, core.IdentityLinked, ada.ID},
		{owner, core.IdentityGuest, ""},
		{owner, core.IdentityLinked, owner.User},
	} {
		if _, err := e.s.SetIdentityStatus(ctx, tc.by, "telegram", "42", tc.status, tc.uid); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("%s moving bob's identity to %s/%s: %v", tc.by.Role, tc.status, tc.uid, err)
		}
	}
	if res := resolved(t, e, "telegram", "42"); res.Status != core.IdentityLinked || res.User != bob.ID {
		t.Fatalf("bob's identity = %+v", res)
	}
	if _, err := e.s.SetIdentityStatus(ctx, admin, "telegram", "42", core.IdentityBlocked, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetIdentityStatus(ctx, admin, "telegram", "42", core.IdentityLinked, bob.ID); err != nil {
		t.Fatalf("restoring bob's own link: %v", err)
	}
	if res := resolved(t, e, "telegram", "42"); res.Status != core.IdentityLinked || res.User != bob.ID {
		t.Fatalf("restored identity = %+v", res)
	}

	if _, err := e.store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: "slack", ExternalID: "U9", LastSeen: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetIdentityStatus(ctx, owner, "slack", "U9", core.IdentityLinked, bob.ID); err != nil {
		t.Fatalf("owner linking a new chat account to a member: %v", err)
	}
}

// An admin's block must hold: the member cannot delete the blocked row and
// pair the same chat account again.
func TestMemberCannotLiftBlockByUnlinking(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	bob := e.user(t, owner, "bob", core.RoleMember)
	bobActor := core.Actor{User: bob.ID, Role: core.RoleMember, Via: "cookie"}
	ident := core.ChannelIdentity{Channel: "telegram", ExternalID: "42", DMChat: "42"}
	linkOwn(t, e, bobActor, ident)
	if _, err := e.s.SetIdentityStatus(ctx, owner, "telegram", "42", core.IdentityBlocked, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.s.UnlinkIdentity(ctx, bobActor, "telegram", "42"); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("member unlinking a blocked identity: %v", err)
	}
	pc, _ := e.s.NewPairingCode(ctx, bobActor)
	if _, err := e.s.RedeemPairing(ctx, ident, pc.Code); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("re-pairing a blocked chat account: %v", err)
	}
	if res := resolved(t, e, "telegram", "42"); res.Status != core.IdentityBlocked {
		t.Fatalf("resolution = %+v", res)
	}
	if err := e.s.UnlinkIdentity(ctx, owner, "telegram", "42"); err != nil {
		t.Fatalf("admin unlink: %v", err)
	}
}
