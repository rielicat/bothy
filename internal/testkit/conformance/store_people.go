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

	f.check(got.LastLoginAt.IsZero(), "LastLoginAt before any sign-in = %v", got.LastLoginAt)
	f.must(r.RecordLogin(f.ctx, member.ID, false, at(70)), "RecordLogin failure")
	f.must(r.SignedIn(f.ctx, member.ID, at(80)), "SignedIn")
	got, err = r.Get(f.ctx, member.ID)
	f.must(err, "Get after SignedIn")
	f.check(got.LastLoginAt.Equal(at(80)) && got.FailedLogins == 0 && got.LockedUntil.IsZero(),
		"after SignedIn: last=%v failed=%d locked=%v", got.LastLoginAt, got.FailedLogins, got.LockedUntil)
	f.want(r.SignedIn(f.ctx, core.UserID(core.NewID(core.PrefixUser)), at(80)), core.ErrNotFound, "SignedIn missing")
	// SetProfile writes the profile columns only: a stale copy cannot undo
	// a role, flag or password change made after it was read.
	stale := *got
	fresh := *got
	fresh.Role, fresh.Disabled, fresh.PWHash, fresh.MustChangePassword, fresh.UpdatedAt = core.RoleMember, !stale.Disabled, "hash-after-reset",
		!stale.MustChangePassword, at(95)
	f.must(r.Update(f.ctx, &fresh), "Update before the stale profile write")
	stale.DisplayName, stale.Timezone, stale.UpdatedAt = "Renamed", "Asia/Tokyo", at(96)
	f.must(r.SetProfile(f.ctx, &stale), "SetProfile")
	got, err = r.Get(f.ctx, member.ID)
	f.must(err, "Get after SetProfile")
	f.check(got.DisplayName == "Renamed" && got.Timezone == "Asia/Tokyo" && got.UpdatedAt.Equal(at(96)) &&
		got.Role == core.RoleMember && got.Disabled == fresh.Disabled && got.PWHash == "hash-after-reset" &&
		got.MustChangePassword == fresh.MustChangePassword && got.LastLoginAt.Equal(at(80)),
		"SetProfile wrote more than the profile: %+v", got)
	f.want(r.SetProfile(f.ctx, &core.User{ID: core.UserID(core.NewID(core.PrefixUser))}), core.ErrNotFound, "SetProfile missing")

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
	s1 := f.space()
	s2 := f.space()
	person, _ := f.teammate("slack", "U77")
	v := viewer(u, s1.Principal, s2.Principal)

	ps, err := r.ForUser(f.ctx, u.ID)
	f.must(err, "ForUser")
	f.check(len(ps) == 1 && ps[0].ID == core.UserPrincipal(u.ID), "ForUser returns the user's own principal only, got %v", ps)
	ps, err = r.ForUser(f.ctx, "")
	f.must(err, "ForUser without a user")
	f.check(len(ps) == 0, "ForUser(\"\") returned team principals: %v", ps)

	team, err := r.Team(f.ctx)
	f.must(err, "Team")
	f.eq("Team lists channels and teammates oldest first", principalIDs(team), []core.PrincipalID{s1.Principal, s2.Principal, person.Principal})

	p, err := r.Get(f.ctx, v, s1.Principal)
	f.must(err, "Get space principal")
	f.check(p.Kind == core.PrincipalSpace && p.User == "" && p.ToolProfile == "restricted" && p.DisplayName == s1.Title,
		"space principal = %+v, want kind space, no user, restricted", p)
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

	pp, err := r.Get(f.ctx, core.SystemViewer(), person.Principal)
	f.must(err, "Get person principal")
	f.check(pp.Kind == core.PrincipalPerson && pp.User == "" && pp.ToolProfile == "standard", "person principal = %+v", pp)
	restricted := *pp
	restricted.ToolProfile = "restricted"
	f.want(r.Update(f.ctx, &restricted), core.ErrInvalid, "a person is always standard")

	f.want(r.Delete(f.ctx, core.UserPrincipal(u.ID)), core.ErrInvalid, "Delete refuses a user principal")
	_, err = r.Get(f.ctx, core.SystemViewer(), core.UserPrincipal(u.ID))
	f.must(err, "a refused Delete keeps the user principal")
	f.want(r.Delete(f.ctx, core.PrincipalID(core.NewID(core.PrefixSpace))), core.ErrNotFound, "Delete missing")
	sess := f.session(s2.Principal)
	f.must(r.Delete(f.ctx, s2.Principal), "Delete a channel's principal")
	_, err = r.Get(f.ctx, core.SystemViewer(), s2.Principal)
	f.want(err, core.ErrNotFound, "principal after Delete")
	_, err = f.s.Spaces().Get(f.ctx, core.SystemViewer(), s2.Principal)
	f.want(err, core.ErrNotFound, "Delete cascades to the space")
	_, err = f.s.Sessions().Get(f.ctx, core.SystemViewer(), sess.ID)
	f.want(err, core.ErrNotFound, "Delete cascades to the sessions")
}

func principalIDs(ps []*core.Principal) []core.PrincipalID {
	out := make([]core.PrincipalID, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func testSpaces(f *fx) {
	r := f.s.Spaces()
	other := f.user(core.RoleMember)

	s := &core.Space{
		Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "discord", ChatID: "C123", Kind: core.ChatGroup}, Title: "#eng",
		Team: "G1", TeamName: "Acme Community", AddedBy: "42", Members: 4812, External: true, ReviewedAt: at(12),
		CreatedAt: at(10), UpdatedAt: at(11),
	}
	p := &core.Principal{DisplayName: "#eng", CreatedAt: at(10)}
	f.must(r.Create(f.ctx, s, p), "Create")
	f.check(core.ValidID(core.PrefixSpace, string(p.ID)) && s.Principal == p.ID,
		"Create assigned principal %q / space principal %q, want the same spc_ id", p.ID, s.Principal)
	f.check(p.Kind == core.PrincipalSpace && p.User == "" && p.ToolProfile == "restricted" &&
		core.ValidID(core.PrefixWorkspace, string(p.Workspace)), "Create filled principal %+v", p)
	v := core.Viewer{Principals: []core.PrincipalID{s.Principal}}

	got, err := r.Get(f.ctx, v, s.Principal)
	f.must(err, "Get")
	f.eq("Get round trip", got, s)
	got, err = r.ByChat(f.ctx, "discord", "C123")
	f.must(err, "ByChat")
	f.eq("ByChat round trip", got, s)
	_, err = r.ByChat(f.ctx, "discord", "C999")
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

	s.Title = "#engineering"
	s.Enabled = true
	s.MentionOnly = true
	s.TeamName, s.Members, s.External, s.ReviewedAt = "Acme", 12, false, at(19)
	s.UpdatedAt = at(20)
	f.must(r.Update(f.ctx, s), "Update")
	got, err = r.Get(f.ctx, v, s.Principal)
	f.must(err, "Get after Update")
	f.eq("Update round trip", got, s)
	moved := *s
	moved.Kind, moved.Chat = core.SpacePerson, core.ChatRef{Channel: "slack", ChatID: "D1", Kind: core.ChatDM}
	f.must(r.Update(f.ctx, &moved), "Update with another kind and chat")
	got, err = r.Get(f.ctx, v, s.Principal)
	f.must(err, "Get after Update")
	f.eq("Update never changes kind, channel or chat", got, s)
	missing := *s
	missing.Principal = core.PrincipalID(core.NewID(core.PrefixSpace))
	f.want(r.Update(f.ctx, &missing), core.ErrNotFound, "Update missing")

	person := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: "telegram", ChatID: "5001", Kind: core.ChatDM}, Title: "Maya",
		Enabled: true, CreatedAt: at(21), UpdatedAt: at(21)}
	pp := &core.Principal{DisplayName: "Maya", ToolProfile: "restricted", CreatedAt: at(21)}
	f.must(r.Create(f.ctx, person, pp), "Create a person space")
	f.check(pp.Kind == core.PrincipalPerson && pp.User == "" && pp.ToolProfile == "standard",
		"person principal = %+v, want kind person, no user, always standard", pp)

	g := f.space()
	newSpace := func(mod func(*core.Space)) *core.Space {
		x := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "discord", ChatID: core.NewID("c"), Kind: core.ChatGroup}, CreatedAt: at(30), UpdatedAt: at(30)}
		mod(x)
		return x
	}
	f.want(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Chat = g.Chat }), &core.Principal{}), core.ErrConflict, "UNIQUE (channel, chat_id)")
	f.must(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Chat.ChatID = g.Chat.ChatID }), &core.Principal{}), "same chat id on another channel")
	f.want(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Kind = "guest" }), &core.Principal{}), core.ErrInvalid, "spaces.kind CHECK")
	f.want(r.Create(f.ctx, newSpace(func(x *core.Space) { x.Chat.Kind = core.ChatWeb }), &core.Principal{}), core.ErrInvalid, "spaces.chat_kind CHECK")
	f.want(r.Create(f.ctx, newSpace(func(*core.Space) {}), &core.Principal{Workspace: p.Workspace}), core.ErrConflict, "principals.workspace_id UNIQUE")
	f.want(r.Create(f.ctx, newSpace(func(*core.Space) {}), &core.Principal{ToolProfile: "open"}), core.ErrInvalid, "principals.tool_profile CHECK")
	std := &core.Principal{ToolProfile: "standard"}
	f.must(r.Create(f.ctx, newSpace(func(*core.Space) {}), std), "Create with an explicit tool profile")
	f.check(std.ToolProfile == "standard", "Create overrode tool_profile standard with %q", std.ToolProfile)
}

func testIdentities(f *fx) {
	r := f.s.Identities()
	person, personID := f.teammate("slack", "U9")

	got, err := r.Touch(f.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "1001", Username: "bob", DisplayName: "Bob",
		Member: core.MemberUnknown, LastSeen: at(5)})
	f.must(err, "Touch new")
	want := &core.ChannelIdentity{Channel: "telegram", ExternalID: "1001", Username: "bob", DisplayName: "Bob",
		Status: core.IdentityPending, Member: core.MemberUnknown, MemberAt: at(5), Greet: core.GreetHello, FirstSeen: at(5), LastSeen: at(5)}
	f.eq("Touch inserts a pending identity with its first verdict, first seen at last_seen", got, want)
	stored, err := r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get")
	f.eq("Get round trip", stored, want)

	got, err = r.Touch(f.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "1001", Username: "bobby", DisplayName: "Bobby",
		Status: core.IdentityTeammate, Principal: person.Principal, Member: core.MemberYes, LastSeen: at(9)})
	f.must(err, "Touch existing")
	want.Username, want.DisplayName, want.LastSeen = "bobby", "Bobby", at(9)
	f.eq("Touch refreshes names and last_seen only", got, want)

	want.FirstText = "hi, I'm Bob from “accounts”"
	f.must(r.Update(f.ctx, want), "Update a pending identity's first text")
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get after setting the first text")
	f.eq("a pending identity keeps its first text", stored, want)

	bobSpace := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: "telegram", ChatID: "1001", Kind: core.ChatDM}, Title: "Bob",
		Enabled: true, CreatedAt: at(8), UpdatedAt: at(8)}
	f.must(f.s.Spaces().Create(f.ctx, bobSpace, &core.Principal{DisplayName: "Bob", CreatedAt: at(8)}), "Create Bob's person space")
	want.Status, want.Principal, want.Via, want.CanApprove = core.IdentityTeammate, bobSpace.Principal, core.ViaAdmin, true
	want.Member, want.MemberAt, want.DMChat, want.LastNotice, want.Greet = core.MemberYes, at(10), "1001", at(7), ""
	f.must(r.Update(f.ctx, want), "Update to teammate")
	want.FirstText = ""
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get after letting in")
	f.eq("letting in drops the first text and keeps every column", stored, want)
	byP, err := r.ByPrincipal(f.ctx, bobSpace.Principal)
	f.must(err, "ByPrincipal")
	f.eq("ByPrincipal", byP, want)
	_, err = r.ByPrincipal(f.ctx, core.PrincipalID(core.NewID(core.PrefixSpace)))
	f.want(err, core.ErrNotFound, "ByPrincipal of a principal without an identity")

	edit := func(mod func(*core.ChannelIdentity)) *core.ChannelIdentity {
		x := *want
		mod(&x)
		return &x
	}
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Principal = "" })), core.ErrInvalid, "a teammate needs principal_id")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Via = "" })), core.ErrInvalid, "a teammate needs admitted_via")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Status = core.IdentityPending })), core.ErrInvalid, "can_approve needs a teammate")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Status = "linked" })), core.ErrInvalid, "status CHECK")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Via = "invite" })), core.ErrInvalid, "admitted_via CHECK")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Member = "guest" })), core.ErrInvalid, "membership CHECK")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Greet = "welcome" })), core.ErrInvalid, "greet CHECK")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.Principal = core.PrincipalID(core.NewID(core.PrefixSpace)) })), core.ErrInvalid, "principal_id foreign key")
	f.want(r.Update(f.ctx, edit(func(x *core.ChannelIdentity) { x.ExternalID = "nope" })), core.ErrNotFound, "Update missing")
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get")
	f.eq("rejected Updates change nothing", stored, want)

	_, err = r.Touch(f.ctx, &core.ChannelIdentity{Channel: "discord", ExternalID: "77", LastSeen: at(4)})
	f.must(err, "Touch another")
	d, err := r.Get(f.ctx, "discord", "77")
	f.must(err, "Get")
	f.check(d.Member == core.MemberUnchecked && d.MemberAt.IsZero(), "Touch without a verdict = %+v, want never checked", d)

	all, err := r.List(f.ctx, "")
	f.must(err, "List all")
	f.check(len(all) == 3, "List(\"\") returned %d identities, want 3", len(all))
	pending, err := r.List(f.ctx, core.IdentityPending)
	f.must(err, "List pending")
	f.check(len(pending) == 1 && pending[0].ExternalID == "77", "List(pending) = %v", pending)
	mates, err := r.List(f.ctx, core.IdentityTeammate)
	f.must(err, "List teammates")
	f.check(len(mates) == 2, "List(teammate) returned %d identities, want 2", len(mates))

	// SetDMChat writes the DM chat alone: whatever else changed since the
	// caller read the row stays.
	before, err := r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get before SetDMChat")
	f.must(r.SetDMChat(f.ctx, "telegram", "1001", "dm-1001"), "SetDMChat")
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get after SetDMChat")
	wantDM := *before
	wantDM.DMChat = "dm-1001"
	f.eq("SetDMChat changes only the DM chat", stored, &wantDM)
	f.want(r.SetDMChat(f.ctx, "telegram", "nobody", "x"), core.ErrNotFound, "SetDMChat of an unknown identity")

	f.must(r.ResetMembership(f.ctx, "telegram"), "ResetMembership")
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "Get after ResetMembership")
	f.check(stored.Member == core.MemberYes && stored.MemberAt.IsZero(), "ResetMembership keeps the verdict and drops its time: %+v", stored)
	slackMate, err := r.Get(f.ctx, "slack", "U9")
	f.must(err, "Get")
	f.check(!personID.MemberAt.IsZero() && slackMate.MemberAt.Equal(personID.MemberAt), "ResetMembership of telegram touched slack: %+v", slackMate)

	// Forgetting a teammate updates the identity before deleting the
	// principal: deleting first would SET NULL a teammate's principal.
	f.want(f.s.Principals().Delete(f.ctx, bobSpace.Principal), core.ErrInvalid, "Delete a teammate's principal before moving the identity off")
	_, err = f.s.Principals().Get(f.ctx, core.SystemViewer(), bobSpace.Principal)
	f.must(err, "a refused Delete keeps the principal")
	gone := edit(func(x *core.ChannelIdentity) {
		x.Status, x.Principal, x.Via, x.CanApprove = core.IdentityBlocked, "", "", false
	})
	f.must(f.s.Tx(f.ctx, func(tx core.Store) error {
		if err := tx.Identities().Update(f.ctx, gone); err != nil {
			return err
		}
		return tx.Principals().Delete(f.ctx, bobSpace.Principal)
	}), "forget: identity first, then the principal")
	stored, err = r.Get(f.ctx, "telegram", "1001")
	f.must(err, "the identity outlives its principal")
	f.check(stored.Status == core.IdentityBlocked && stored.Principal == "", "forgotten identity = %+v", stored)

	f.must(r.Delete(f.ctx, "discord", "77"), "Delete")
	_, err = r.Get(f.ctx, "discord", "77")
	f.want(err, core.ErrNotFound, "Get after Delete")
	f.want(r.Delete(f.ctx, "discord", "77"), core.ErrNotFound, "Delete missing")
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
	f.want(r.Revoke(f.ctx, u.ID, tok.ID, at(8)), core.ErrNotFound, "Revoke twice")
	got, err = r.Get(f.ctx, tok.ID)
	f.must(err, "Get after a second Revoke")
	f.check(got.Revoked.Equal(at(7)), "a second Revoke moved revoked_at to %v", got.Revoked)
	f.want(r.Revoke(f.ctx, u.ID, core.NewID(core.PrefixToken), at(7)), core.ErrNotFound, "Revoke missing")
	_, err = r.Get(f.ctx, core.NewID(core.PrefixToken))
	f.want(err, core.ErrNotFound, "Get missing")
}

// testUserDelete checks the purge cascade: deleting a user removes its
// principal and everything it owns, keeps other users' rows, and never
// touches team-owned principals (channels, teammates).
func testUserDelete(f *fx) {
	alice := f.user(core.RoleMember)
	bob := f.user(core.RoleMember)
	pa, pb := core.UserPrincipal(alice.ID), core.UserPrincipal(bob.ID)
	sp := f.space()
	person, _ := f.teammate("telegram", "77")
	s := f.session(pa)
	ss := f.session(sp.Principal)
	ps := f.session(person.Principal)
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
	sis, signIns := f.s.(core.SignInStore)
	if signIns {
		f.must(sis.SignIns().Create(f.ctx, &core.SignIn{Provider: core.SignInGoogle, Subject: "alice-sub", User: alice.ID, CreatedAt: at(1)}), "Create sign-in")
	}

	f.must(f.s.Users().Delete(f.ctx, alice.ID), "Delete")
	f.want(f.s.Users().Delete(f.ctx, alice.ID), core.ErrNotFound, "Delete of a missing user")

	sys := core.SystemViewer()
	_, err = f.s.Users().Get(f.ctx, alice.ID)
	f.want(err, core.ErrNotFound, "user after Delete")
	got, err := f.s.Principals().ForUser(f.ctx, alice.ID)
	f.must(err, "ForUser")
	f.check(len(got) == 0, "principals after Delete = %d", len(got))
	_, err = f.s.Sessions().Get(f.ctx, sys, s.ID)
	f.want(err, core.ErrNotFound, "session after Delete")
	for _, id := range []core.SessionID{ss.ID, ps.ID} {
		_, err = f.s.Sessions().Get(f.ctx, sys, id)
		f.must(err, "a team-owned principal's session survives a user's Delete")
	}
	_, err = f.s.Spaces().Get(f.ctx, sys, sp.Principal)
	f.must(err, "a channel survives a user's Delete")
	ident, err := f.s.Identities().Get(f.ctx, "telegram", "77")
	f.must(err, "a teammate survives a user's Delete")
	f.check(ident.Principal == person.Principal, "teammate after a user's Delete = %+v", ident)
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
	if signIns {
		_, err = sis.SignIns().Get(f.ctx, core.SignInGoogle, "alice-sub")
		f.want(err, core.ErrNotFound, "sign-in after Delete")
	}
	hits, err := f.s.Messages().Search(f.ctx, sys, "secret", 10)
	f.must(err, "Search")
	f.check(len(hits) == 0, "search still finds the deleted user's messages: %+v", hits)

	_, err = f.s.Users().Get(f.ctx, bob.ID)
	f.must(err, "other user after Delete")
	bm, err := f.s.Messages().Range(f.ctx, viewer(bob), sb.ID, 1, 0)
	f.must(err, "other user's messages")
	f.check(len(bm) == 1, "other user's messages after Delete = %d", len(bm))
}
