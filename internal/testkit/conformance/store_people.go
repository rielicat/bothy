package conformance

import (
	"crypto/sha256"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func testMeta(f *fx) {
	m := f.s.Meta()
	_, err := m.Get(f.ctx, "conformance.key")
	f.want(err, core.ErrNotFound, "Get missing key")
	f.must(m.Set(f.ctx, "conformance.key", "a"), "Set")
	f.must(m.Set(f.ctx, "conformance.key", "b"), "Set overwrite")
	v, err := m.Get(f.ctx, "conformance.key")
	f.must(err, "Get")
	f.check(v == "b", "Get after overwrite = %q, want b", v)
	f.must(m.Delete(f.ctx, "conformance.key"), "Delete")
	_, err = m.Get(f.ctx, "conformance.key")
	f.want(err, core.ErrNotFound, "Get after Delete")
	f.must(m.Delete(f.ctx, "conformance.key"), "Delete of a missing key is idempotent")
}

func testUsers(f *fx) {
	r := f.s.Users()
	n, err := r.Count(f.ctx)
	f.must(err, "Count")
	f.check(n == 0, "Count of an empty store = %d", n)

	owner := &core.User{
		Username: "Alice", DisplayName: "Alice A", Role: core.RoleOwner, PWHash: "$argon2id$x",
		Timezone: "Europe/London", FailedLogins: 2, LockedUntil: at(100), CreatedAt: at(1), UpdatedAt: at(2),
	}
	f.must(r.Create(f.ctx, owner), "Create")
	f.check(core.ValidID(core.PrefixUser, string(owner.ID)), "Create assigned id %q, want a usr_ id", owner.ID)
	got, err := r.Get(f.ctx, owner.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, owner)
	got, err = r.ByUsername(f.ctx, "aLiCe")
	f.must(err, "ByUsername is case-insensitive")
	f.eq("ByUsername round trip", got, owner)
	_, err = r.ByUsername(f.ctx, "bob")
	f.want(err, core.ErrNotFound, "ByUsername missing")
	_, err = r.Get(f.ctx, core.UserID(core.NewID(core.PrefixUser)))
	f.want(err, core.ErrNotFound, "Get missing")

	member := &core.User{ID: core.UserID(core.NewID(core.PrefixUser)), Username: "bob", Role: core.RoleMember, PWHash: "h", Timezone: "UTC",
		MustChangePassword: true, CreatedAt: at(3), UpdatedAt: at(3)}
	wantID := member.ID
	f.must(r.Create(f.ctx, member), "Create with explicit id")
	f.check(member.ID == wantID, "Create replaced explicit id %q with %q", wantID, member.ID)

	f.want(r.Create(f.ctx, &core.User{ID: member.ID, Username: "carol", Role: core.RoleMember, CreatedAt: at(4)}), core.ErrConflict, "duplicate id")
	f.want(r.Create(f.ctx, &core.User{Username: "ALICE", Role: core.RoleMember, CreatedAt: at(4)}), core.ErrConflict, "username is unique without case")
	f.want(r.Create(f.ctx, &core.User{Username: "dave", Role: "root", CreatedAt: at(4)}), core.ErrInvalid, "role CHECK")
	f.want(r.Create(f.ctx, &core.User{Username: "erin", Role: core.RoleOwner, CreatedAt: at(4)}), core.ErrConflict, "users_one_owner")

	n, err = r.Count(f.ctx)
	f.must(err, "Count")
	f.check(n == 2, "Count = %d, want 2 (failed creates must not insert)", n)
	list, err := r.List(f.ctx)
	f.must(err, "List")
	f.eq("List", sortBy(list, func(u *core.User) core.UserID { return u.ID }),
		sortBy([]*core.User{owner, member}, func(u *core.User) core.UserID { return u.ID }))

	member.DisplayName = "Bobby"
	member.Role = core.RoleAdmin
	member.Disabled = true
	member.Timezone = "America/New_York"
	member.PWHash = "h2"
	member.MustChangePassword = false
	member.UpdatedAt = at(50)
	f.must(r.Update(f.ctx, member), "Update")
	got, err = r.Get(f.ctx, member.ID)
	f.must(err, "Get after Update")
	f.eq("Update round trip", got, member)

	promote := *member
	promote.Role = core.RoleOwner
	f.want(r.Update(f.ctx, &promote), core.ErrConflict, "Update to a second owner")
	rename := *member
	rename.Username = "alice"
	f.want(r.Update(f.ctx, &rename), core.ErrConflict, "Update to a taken username")
	got, err = r.Get(f.ctx, member.ID)
	f.must(err, "Get")
	f.eq("rejected Updates change nothing", got, member)
	f.want(r.Update(f.ctx, &core.User{ID: core.UserID(core.NewID(core.PrefixUser)), Username: "zed", Role: core.RoleMember}), core.ErrNotFound, "Update missing")

	f.must(r.RecordLogin(f.ctx, member.ID, false, time.Time{}), "RecordLogin failure")
	f.must(r.RecordLogin(f.ctx, member.ID, false, at(60)), "RecordLogin failure with lock")
	got, err = r.Get(f.ctx, member.ID)
	f.must(err, "Get")
	f.check(got.FailedLogins == 2 && got.LockedUntil.Equal(at(60)), "after two failures: failed=%d locked=%v", got.FailedLogins, got.LockedUntil)
	f.must(r.RecordLogin(f.ctx, member.ID, true, time.Time{}), "RecordLogin success")
	got, err = r.Get(f.ctx, member.ID)
	f.must(err, "Get")
	f.check(got.FailedLogins == 0 && got.LockedUntil.IsZero(), "after success: failed=%d locked=%v", got.FailedLogins, got.LockedUntil)
	f.want(r.RecordLogin(f.ctx, core.UserID(core.NewID(core.PrefixUser)), true, time.Time{}), core.ErrNotFound, "RecordLogin missing")

	po, err := f.s.Principals().Get(f.ctx, core.SystemViewer(), core.UserPrincipal(owner.ID))
	f.must(err, "Create also creates the user principal")
	f.check(po.Kind == core.PrincipalUser && po.User == owner.ID && po.ToolProfile == "standard" &&
		core.ValidID(core.PrefixWorkspace, string(po.Workspace)),
		"user principal = %+v, want kind user, user_id %s, tool_profile standard, a ws_ workspace", po, owner.ID)
	pm, err := f.s.Principals().Get(f.ctx, core.SystemViewer(), core.UserPrincipal(member.ID))
	f.must(err, "user principal")
	f.check(pm.Workspace != po.Workspace, "users share workspace %s", po.Workspace)
}

func testPrincipals(f *fx) {
	r := f.s.Principals()
	u := f.user(core.RoleMember)
	loner := f.user(core.RoleMember)
	s1 := f.space(u)
	s2 := f.space(u)
	v := viewer(u, s1.Principal, s2.Principal)

	ps, err := r.ForUser(f.ctx, u.ID)
	f.must(err, "ForUser")
	f.check(len(ps) == 3, "ForUser returned %d principals, want 3", len(ps))
	if len(ps) == 3 {
		f.check(ps[0].ID == core.UserPrincipal(u.ID), "ForUser lists the user's own principal first, got %s", ps[0].ID)
		rest := map[core.PrincipalID]bool{ps[1].ID: true, ps[2].ID: true}
		f.check(rest[s1.Principal] && rest[s2.Principal], "ForUser spaces = %v, want %s and %s", rest, s1.Principal, s2.Principal)
	}
	ps, err = r.ForUser(f.ctx, loner.ID)
	f.must(err, "ForUser")
	f.check(len(ps) == 1 && ps[0].ID == core.UserPrincipal(loner.ID), "ForUser of a user without spaces = %v", ps)

	p, err := r.Get(f.ctx, v, s1.Principal)
	f.must(err, "Get space principal")
	f.check(p.Kind == core.PrincipalSpace && p.User == u.ID && p.ToolProfile == "restricted" && p.DisplayName == s1.Title,
		"space principal = %+v", p)
	p.DisplayName = "Renamed"
	p.ToolProfile = "standard"
	f.must(r.Update(f.ctx, p), "Update")
	got, err := r.Get(f.ctx, v, s1.Principal)
	f.must(err, "Get after Update")
	f.eq("Update round trip", got, p)

	bad := *p
	bad.ToolProfile = "unrestricted"
	f.want(r.Update(f.ctx, &bad), core.ErrInvalid, "tool_profile CHECK")
	f.want(r.Update(f.ctx, &core.Principal{ID: core.PrincipalID(core.NewID(core.PrefixSpace)), ToolProfile: "standard"}), core.ErrNotFound, "Update missing")
}

func testSpaces(f *fx) {
	r := f.s.Spaces()
	owner := f.user(core.RoleAdmin)
	other := f.user(core.RoleMember)

	s := &core.Space{
		Kind: "guest", Chat: core.ChatRef{Channel: "slack", ChatID: "D123", Kind: core.ChatDM}, Title: "Guest",
		Owner: owner.ID, CreatedAt: at(10), UpdatedAt: at(11),
	}
	p := &core.Principal{DisplayName: "Guest Bob", CreatedAt: at(10)}
	f.must(r.Create(f.ctx, s, p), "Create")
	f.check(core.ValidID(core.PrefixSpace, string(p.ID)) && s.Principal == p.ID,
		"Create assigned principal %q / space principal %q, want the same spc_ id", p.ID, s.Principal)
	f.check(p.Kind == core.PrincipalSpace && p.User == owner.ID && p.ToolProfile == "restricted" &&
		core.ValidID(core.PrefixWorkspace, string(p.Workspace)), "Create filled principal %+v", p)
	v := viewer(owner, s.Principal)

	got, err := r.Get(f.ctx, v, s.Principal)
	f.must(err, "Get")
	f.eq("Get round trip", got, s)
	got, err = r.ByChat(f.ctx, "slack", "D123")
	f.must(err, "ByChat")
	f.eq("ByChat round trip", got, s)
	_, err = r.ByChat(f.ctx, "slack", "D999")
	f.want(err, core.ErrNotFound, "ByChat missing")
	_, err = r.Get(f.ctx, viewer(other), s.Principal)
	f.want(err, core.ErrNotFound, "Get with a foreign viewer")
	pr, err := f.s.Principals().Get(f.ctx, v, s.Principal)
	f.must(err, "Create also creates the space principal")
	f.eq("space principal round trip", pr, p)

	list, err := r.List(f.ctx, v)
	f.must(err, "List")
	f.eq("List", list, []*core.Space{s})
	list, err = r.List(f.ctx, viewer(other))
	f.must(err, "List foreign")
	f.check(len(list) == 0, "List with a foreign viewer returned %d spaces", len(list))
	list, err = r.List(f.ctx, core.SystemViewer())
	f.must(err, "List system")
	f.check(len(list) == 1, "List with SystemViewer returned %d spaces, want 1", len(list))

	s.Title = "Renamed"
	s.Enabled = true
	s.MentionOnly = true
	s.UpdatedAt = at(20)
	f.must(r.Update(f.ctx, s), "Update")
	got, err = r.Get(f.ctx, v, s.Principal)
	f.must(err, "Get after Update")
	f.eq("Update round trip", got, s)
	missing := *s
	missing.Principal = core.PrincipalID(core.NewID(core.PrefixSpace))
	f.want(r.Update(f.ctx, &missing), core.ErrNotFound, "Update missing")

	g := f.space(owner)
	newSpace := func(mod func(*core.Space)) *core.Space {
		x := &core.Space{Kind: "group", Chat: core.ChatRef{Channel: "discord", ChatID: core.NewID("c"), Kind: core.ChatGroup}, Owner: owner.ID, CreatedAt: at(30), UpdatedAt: at(30)}
		mod(x)
		return x
	}
	f.want(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Chat = g.Chat }), &core.Principal{}), core.ErrConflict, "UNIQUE (channel, chat_id)")
	f.must(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Chat.ChatID = g.Chat.ChatID }), &core.Principal{}), "same chat id on another channel")
	f.want(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Kind = "channel" }), &core.Principal{}), core.ErrInvalid, "spaces.kind CHECK")
	f.want(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Chat.Kind = core.ChatWeb }), &core.Principal{}), core.ErrInvalid, "spaces.chat_kind CHECK")
	f.want(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Owner = core.UserID(core.NewID(core.PrefixUser)) }), &core.Principal{}), core.ErrInvalid, "owner foreign key")
	f.want(r.Create(f.ctx, newSpace(func(*core.Space) {}), &core.Principal{Workspace: p.Workspace}), core.ErrConflict, "principals.workspace_id UNIQUE")
	f.want(r.Create(f.ctx, newSpace(func(*core.Space) {}), &core.Principal{ToolProfile: "open"}), core.ErrInvalid, "principals.tool_profile CHECK")
	std := &core.Principal{ToolProfile: "standard"}
	f.must(r.Create(f.ctx, newSpace(func(*core.Space) {}), std), "Create with an explicit tool profile")
	f.check(std.ToolProfile == "standard", "Create overrode tool_profile standard with %q", std.ToolProfile)

	moved := *g
	moved.Chat = s.Chat
	moved.Chat.Kind = core.ChatGroup
	f.want(r.Update(f.ctx, &moved), core.ErrConflict, "Update onto a taken chat")
}

func testIdentities(f *fx) {
	r := f.s.Identities()
	u := f.user(core.RoleMember)
	guest := f.space(u)

	got, err := r.Touch(f.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "1001", Username: "bob", DisplayName: "Bob", LastSeen: at(5)})
	f.must(err, "Touch new")
	want := &core.ChannelIdentity{Channel: "telegram", ExternalID: "1001", Username: "bob", DisplayName: "Bob",
		Status: core.IdentityPending, FirstSeen: at(5), LastSeen: at(5)}
	f.eq("Touch inserts a pending identity first seen at last_seen", got, want)
	stored, err := r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get")
	f.eq("Get round trip", stored, want)

	got, err = r.Touch(f.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "1001", Username: "bobby", DisplayName: "Bobby",
		Status: core.IdentityLinked, User: u.ID, LastSeen: at(9)})
	f.must(err, "Touch existing")
	want.Username, want.DisplayName, want.LastSeen = "bobby", "Bobby", at(9)
	f.eq("Touch refreshes names and last_seen only", got, want)

	want.Status, want.User, want.DMChat, want.LastNotice = core.IdentityLinked, u.ID, "1001", at(7)
	f.must(r.Update(f.ctx, want), "Update")
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get after Update")
	f.eq("Update round trip", stored, want)
	byUser, err := r.ForUser(f.ctx, u.ID)
	f.must(err, "ForUser")
	f.eq("ForUser", byUser, []*core.ChannelIdentity{want})

	edit := func(mod func(*core.ChannelIdentity)) *core.ChannelIdentity {
		x := *want
		mod(&x)
		return &x
	}
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.User = "" })), core.ErrInvalid, "linked requires user_id")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Status, x.Principal = core.IdentityGuest, "" })), core.ErrInvalid, "guest requires principal_id")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Status = "trusted" })), core.ErrInvalid, "status CHECK")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.User = core.UserID(core.NewID(core.PrefixUser)) })), core.ErrInvalid, "user_id foreign key")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.ExternalID = "nope" })), core.ErrNotFound, "Update missing")
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get")
	f.eq("rejected Updates change nothing", stored, want)

	g := &core.ChannelIdentity{Channel: "slack", ExternalID: "U9", Status: core.IdentityGuest, Principal: guest.Principal,
		FirstSeen: at(1), LastSeen: at(2)}
	got, err = r.Touch(f.ctx, g)
	f.must(err, "Touch new with an explicit status")
	f.eq("Touch insert keeps the given status and first_seen", got, g)
	_, err = r.Touch(f.ctx, &core.ChannelIdentity{Channel: "slack", ExternalID: "U10", Status: core.IdentityLinked, LastSeen: at(3)})
	f.want(err, core.ErrInvalid, "Touch insert of linked without user_id")
	_, err = r.Touch(f.ctx, &core.ChannelIdentity{Channel: "discord", ExternalID: "77", LastSeen: at(4)})
	f.must(err, "Touch another")

	all, err := r.List(f.ctx, "")
	f.must(err, "List all")
	f.check(len(all) == 3, "List(\"\") returned %d identities, want 3", len(all))
	pending, err := r.List(f.ctx, core.IdentityPending)
	f.must(err, "List pending")
	f.check(len(pending) == 1 && pending[0].ExternalID == "77", "List(pending) = %v", pending)
	blocked, err := r.List(f.ctx, core.IdentityBlocked)
	f.must(err, "List blocked")
	f.check(len(blocked) == 0, "List(blocked) returned %d identities", len(blocked))

	f.must(r.Delete(f.ctx, "discord", "77"), "Delete")
	_, err = r.Get(f.ctx, "discord", "77")
	f.want(err, core.ErrNotFound, "Get after Delete")
	f.want(r.Delete(f.ctx, "discord", "77"), core.ErrNotFound, "Delete missing")
}

func testPairings(f *fx) {
	r := f.s.Pairings()
	u := f.user(core.RoleMember)
	now := at(100)

	link := &core.Pairing{CodeHash: sum("7KQ2MX4D"), Purpose: core.PairLinkIdentity, User: u.ID, CreatedBy: u.ID, ExpiresAt: now.Add(10 * time.Minute)}
	f.must(r.Create(f.ctx, link), "Create")
	f.want(r.Create(f.ctx, &core.Pairing{CodeHash: sum("7KQ2MX4D"), Purpose: core.PairLinkIdentity, ExpiresAt: now}), core.ErrConflict, "duplicate code_hash")
	f.want(r.Create(f.ctx, &core.Pairing{CodeHash: sum("x1"), Purpose: "invite", ExpiresAt: now}), core.ErrInvalid, "purpose CHECK")
	f.want(r.Create(f.ctx, &core.Pairing{CodeHash: sum("x2"), Purpose: core.PairLinkIdentity, User: core.UserID(core.NewID(core.PrefixUser)), ExpiresAt: now}), core.ErrInvalid, "user_id foreign key")

	_, err := r.Redeem(f.ctx, sum("7KQ2MX4D"), core.PairApproveSender, now)
	f.want(err, core.ErrNotFound, "Redeem with the wrong purpose")
	_, err = r.Redeem(f.ctx, sum("WRONG"), core.PairLinkIdentity, now)
	f.want(err, core.ErrNotFound, "Redeem a wrong code")
	_, err = r.Redeem(f.ctx, sum("7KQ2MX4D"), core.PairLinkIdentity, now.Add(11*time.Minute))
	f.want(err, core.ErrNotFound, "Redeem an expired code")
	got, err := r.Redeem(f.ctx, sum("7KQ2MX4D"), core.PairLinkIdentity, now)
	f.must(err, "Redeem")
	want := *link
	want.UsedAt = now
	f.eq("Redeem returns the row marked used", got, &want)
	_, err = r.Redeem(f.ctx, sum("7KQ2MX4D"), core.PairLinkIdentity, now)
	f.want(err, core.ErrNotFound, "Redeem twice")

	approve := &core.Pairing{CodeHash: sum("AAAABBBB"), Purpose: core.PairApproveSender, Channel: "telegram", ExternalID: "1001", ExpiresAt: now.Add(time.Hour)}
	f.must(r.Create(f.ctx, approve), "Create approve_sender")
	got, err = r.ForIdentity(f.ctx, "telegram", "1001", core.PairApproveSender, now)
	f.must(err, "ForIdentity")
	f.eq("ForIdentity round trip", got, approve)
	_, err = r.ForIdentity(f.ctx, "telegram", "1001", core.PairApproveSender, now.Add(2*time.Hour))
	f.want(err, core.ErrNotFound, "ForIdentity after expiry")
	_, err = r.ForIdentity(f.ctx, "telegram", "1002", core.PairApproveSender, now)
	f.want(err, core.ErrNotFound, "ForIdentity of another identity")
	_, err = r.ForIdentity(f.ctx, "telegram", "1001", core.PairLinkIdentity, now)
	f.want(err, core.ErrNotFound, "ForIdentity with another purpose")
	_, err = r.Redeem(f.ctx, sum("AAAABBBB"), core.PairApproveSender, now)
	f.must(err, "Redeem approve_sender")
	_, err = r.ForIdentity(f.ctx, "telegram", "1001", core.PairApproveSender, now)
	f.want(err, core.ErrNotFound, "ForIdentity after Redeem")
}

func testAuthSessions(f *fx) {
	r := f.s.AuthSessions()
	u := f.user(core.RoleMember)
	u2 := f.user(core.RoleMember)
	mk := func(id string, user core.UserID, idle, exp int) *core.AuthSession {
		return &core.AuthSession{IDHash: sum(id), User: user, CSRF: "csrf-" + id, IP: "203.0.113.5", UA: "test/1",
			CreatedAt: at(1), LastSeen: at(1), IdleExpires: at(idle), Expires: at(exp)}
	}

	s1 := mk("s1", u.ID, 2000, 5000)
	f.must(r.Create(f.ctx, s1), "Create")
	got, err := r.Get(f.ctx, sum("s1"))
	f.must(err, "Get")
	f.eq("Get round trip", got, s1)
	f.want(r.Create(f.ctx, mk("s1", u.ID, 1, 1)), core.ErrConflict, "duplicate id_hash")
	f.want(r.Create(f.ctx, mk("s9", core.UserID(core.NewID(core.PrefixUser)), 1, 1)), core.ErrInvalid, "user_id foreign key")
	_, err = r.Get(f.ctx, sum("nope"))
	f.want(err, core.ErrNotFound, "Get missing")

	f.must(r.Touch(f.ctx, sum("s1"), at(10), at(2500)), "Touch")
	got, err = r.Get(f.ctx, sum("s1"))
	f.must(err, "Get after Touch")
	s1.LastSeen, s1.IdleExpires = at(10), at(2500)
	f.eq("Touch round trip", got, s1)
	f.want(r.Touch(f.ctx, sum("nope"), at(10), at(20)), core.ErrNotFound, "Touch missing")

	f.must(r.Create(f.ctx, mk("idle", u.ID, 500, 5000)), "Create idle-expiring")
	f.must(r.Create(f.ctx, mk("hard", u2.ID, 5000, 900)), "Create hard-expiring")
	f.must(r.Create(f.ctx, mk("live", u2.ID, 5000, 5000)), "Create live")
	n, err := r.DeleteExpired(f.ctx, at(1000))
	f.must(err, "DeleteExpired")
	f.check(n == 2, "DeleteExpired removed %d sessions, want 2 (idle and hard)", n)
	for _, id := range []string{"idle", "hard"} {
		_, err = r.Get(f.ctx, sum(id))
		f.want(err, core.ErrNotFound, "Get expired "+id)
	}

	f.must(r.DeleteUser(f.ctx, u2.ID), "DeleteUser")
	_, err = r.Get(f.ctx, sum("live"))
	f.want(err, core.ErrNotFound, "Get after DeleteUser")
	_, err = r.Get(f.ctx, sum("s1"))
	f.must(err, "DeleteUser keeps other users' sessions")

	f.must(r.Delete(f.ctx, sum("s1")), "Delete")
	_, err = r.Get(f.ctx, sum("s1"))
	f.want(err, core.ErrNotFound, "Get after Delete")
	f.must(r.Delete(f.ctx, sum("s1")), "Delete of a missing session is idempotent")
}

func testTokens(f *fx) {
	r := f.s.Tokens()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)

	tok := &core.TokenRecord{Name: "ci", User: u.ID, SecretHash: sum("secret-1"), Scopes: []string{"chat", "read"},
		OnApproval: "wait", RatePerMin: 30, CreatedAt: at(1), Expires: at(9000)}
	f.must(r.Create(f.ctx, tok), "Create")
	f.check(core.ValidID(core.PrefixToken, tok.ID), "Create assigned id %q, want a tok_ id", tok.ID)
	got, err := r.Get(f.ctx, tok.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, tok)
	list, err := r.List(f.ctx, u.ID)
	f.must(err, "List")
	f.eq("List", list, []*core.TokenRecord{tok})
	list, err = r.List(f.ctx, other.ID)
	f.must(err, "List other user")
	f.check(len(list) == 0, "List of another user returned %d tokens", len(list))

	mk := func(mod func(*core.TokenRecord)) *core.TokenRecord {
		x := &core.TokenRecord{Name: "x", User: u.ID, SecretHash: sum(core.NewID("s")), Scopes: []string{"llm"}, OnApproval: "deny", CreatedAt: at(2), Expires: at(3)}
		mod(x)
		return x
	}
	f.want(r.Create(f.ctx, mk(func(x *core.TokenRecord) { x.ID = tok.ID })), core.ErrConflict, "duplicate id")
	f.want(r.Create(f.ctx, mk(func(x *core.TokenRecord) { x.SecretHash = sum("secret-1") })), core.ErrConflict, "secret_hash UNIQUE")
	f.want(r.Create(f.ctx, mk(func(x *core.TokenRecord) { x.OnApproval = "ask" })), core.ErrInvalid, "on_approval CHECK")
	f.want(r.Create(f.ctx, mk(func(x *core.TokenRecord) { x.User = core.UserID(core.NewID(core.PrefixUser)) })), core.ErrInvalid, "user_id foreign key")

	f.must(r.Touch(f.ctx, tok.ID, at(5)), "Touch")
	f.want(r.Touch(f.ctx, core.NewID(core.PrefixToken), at(5)), core.ErrNotFound, "Touch missing")
	f.want(r.Revoke(f.ctx, other.ID, tok.ID, at(6)), core.ErrNotFound, "Revoke by another user")
	got, err = r.Get(f.ctx, tok.ID)
	f.must(err, "Get")
	f.check(got.Revoked.IsZero(), "a foreign Revoke revoked the token at %v", got.Revoked)
	f.must(r.Revoke(f.ctx, u.ID, tok.ID, at(7)), "Revoke")
	got, err = r.Get(f.ctx, tok.ID)
	f.must(err, "Get after Revoke")
	tok.LastUsed, tok.Revoked = at(5), at(7)
	f.eq("Touch and Revoke round trip", got, tok)
	f.want(r.Revoke(f.ctx, u.ID, core.NewID(core.PrefixToken), at(7)), core.ErrNotFound, "Revoke missing")
	_, err = r.Get(f.ctx, core.NewID(core.PrefixToken))
	f.want(err, core.ErrNotFound, "Get missing")
}

// testUserDelete checks the purge cascade: deleting a user removes its
// principals and everything they own, keeps other users' rows, and refuses
// while a linked identity still points at the user.
func testUserDelete(f *fx) {
	alice := f.user(core.RoleMember)
	bob := f.user(core.RoleMember)
	pa, pb := core.UserPrincipal(alice.ID), core.UserPrincipal(bob.ID)
	sp := f.space(alice)
	s := f.session(pa)
	ss := f.session(sp.Principal)
	sb := f.session(pb)
	f.must(f.s.Messages().Append(f.ctx, textMsg(s.ID, core.MsgUser, core.ProvUser, "alice secret", 1)), "Append")
	f.must(f.s.Messages().Append(f.ctx, textMsg(sb.ID, core.MsgUser, core.ProvUser, "bob keeps this", 1)), "Append")
	run := f.run(s, core.RunWaitingApproval)
	apr := f.approval(run, core.ApprovalPending, at(2))
	f.must(f.s.Approvals().AddMessage(f.ctx, apr.ID, core.CardMessage{Ref: core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "1"}, ID: "9"}}), "AddMessage")
	sch := f.schedule(pa, at(100), true)
	_, err := f.s.MemoryEvents().Add(f.ctx, &core.MemoryEvent{Principal: pa, File: core.FileMemory, Op: "add", After: "x", Actor: "user:x", At: at(3)})
	f.must(err, "Add memory event")
	tok := &core.TokenRecord{User: alice.ID, Name: "t", SecretHash: []byte("0123456789abcdef"), Scopes: []string{"chat"}, OnApproval: "deny"}
	f.must(f.s.Tokens().Create(f.ctx, tok), "Create token")
	f.must(f.s.AuthSessions().Create(f.ctx, &core.AuthSession{IDHash: sum("alice-session"), User: alice.ID, CSRF: "c",
		CreatedAt: at(1), LastSeen: at(1), IdleExpires: at(9000), Expires: at(9000)}), "Create auth session")
	_, err = f.s.Identities().Touch(f.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "77", LastSeen: at(4)})
	f.must(err, "Touch identity")
	f.must(f.s.Identities().Update(f.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "77", Status: core.IdentityLinked,
		User: alice.ID, FirstSeen: at(4), LastSeen: at(4)}), "link identity")

	f.want(f.s.Users().Delete(f.ctx, alice.ID), core.ErrInvalid, "Delete with a linked identity")
	_, err = f.s.Users().Get(f.ctx, alice.ID)
	f.must(err, "a refused Delete keeps the user")
	f.must(f.s.Identities().Delete(f.ctx, "telegram", "77"), "Delete identity")
	f.must(f.s.Users().Delete(f.ctx, alice.ID), "Delete")
	f.want(f.s.Users().Delete(f.ctx, alice.ID), core.ErrNotFound, "Delete of a missing user")

	sys := core.SystemViewer()
	_, err = f.s.Users().Get(f.ctx, alice.ID)
	f.want(err, core.ErrNotFound, "user after Delete")
	ps, err := f.s.Principals().ForUser(f.ctx, alice.ID)
	f.must(err, "ForUser")
	f.check(len(ps) == 0, "principals after Delete = %d", len(ps))
	_, err = f.s.Spaces().Get(f.ctx, sys, sp.Principal)
	f.want(err, core.ErrNotFound, "space after Delete")
	for _, id := range []core.SessionID{s.ID, ss.ID} {
		_, err = f.s.Sessions().Get(f.ctx, sys, id)
		f.want(err, core.ErrNotFound, "session after Delete")
	}
	_, err = f.s.Runs().Get(f.ctx, sys, run.ID)
	f.want(err, core.ErrNotFound, "run after Delete")
	_, err = f.s.Approvals().Get(f.ctx, sys, apr.ID)
	f.want(err, core.ErrNotFound, "approval after Delete")
	msgs, err := f.s.Approvals().Messages(f.ctx, apr.ID)
	f.must(err, "Approvals.Messages")
	f.check(len(msgs) == 0, "card messages after Delete = %d", len(msgs))
	_, err = f.s.Schedules().Get(f.ctx, sys, sch.ID)
	f.want(err, core.ErrNotFound, "schedule after Delete")
	evs, err := f.s.MemoryEvents().List(f.ctx, sys, pa, "", core.Page{})
	f.must(err, "MemoryEvents.List")
	f.check(len(evs) == 0, "memory events after Delete = %d", len(evs))
	_, err = f.s.Tokens().Get(f.ctx, tok.ID)
	f.want(err, core.ErrNotFound, "token after Delete")
	_, err = f.s.AuthSessions().Get(f.ctx, sum("alice-session"))
	f.want(err, core.ErrNotFound, "auth session after Delete")
	hits, err := f.s.Messages().Search(f.ctx, sys, "secret", 10)
	f.must(err, "Search")
	f.check(len(hits) == 0, "search still finds the deleted user's messages: %+v", hits)

	_, err = f.s.Users().Get(f.ctx, bob.ID)
	f.must(err, "other user after Delete")
	bm, err := f.s.Messages().Range(f.ctx, viewer(bob), sb.ID, 1, 0)
	f.must(err, "other user's messages")
	f.check(len(bm) == 1, "other user's messages after Delete = %d", len(bm))
}
