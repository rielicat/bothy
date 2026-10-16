package auth

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func resolved(t *testing.T, e *env, channel, ext string) core.IdentityResolution {
	t.Helper()
	res, err := e.s.ResolveIdentity(ctx, channel, ext)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func identity(t *testing.T, e *env, channel, ext string) *core.ChannelIdentity {
	t.Helper()
	id, err := e.store.Identities().Get(ctx, channel, ext)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// audited returns the audit rows of action, oldest first.
func (e *env) audited(action string) []core.AuditEntry {
	var out []core.AuditEntry
	for _, a := range e.audit.All() {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

// touch records a sender the way the hub does before anything else.
func (e *env) touch(t *testing.T, channel, ext, name string, m core.MemberStatus) core.ChannelIdentity {
	t.Helper()
	row, err := e.store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: channel, ExternalID: ext, DisplayName: name, Member: m,
		LastSeen: e.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return *row
}

func TestAdmittedTruthTable(t *testing.T) {
	for _, c := range []struct {
		status core.IdentityStatus
		space  bool
		member core.MemberStatus
		via    core.AdmittedVia
		want   bool
	}{
		{core.IdentityTeammate, true, core.MemberYes, core.ViaMembership, true},
		{core.IdentityTeammate, true, core.MemberYes, core.ViaAdmin, true},
		{core.IdentityTeammate, true, core.MemberUnknown, core.ViaAdmin, true},
		{core.IdentityTeammate, true, core.MemberUnknown, core.ViaMembership, false}, // Discord can't vouch for them any more
		{core.IdentityTeammate, true, core.MemberOutsider, core.ViaAdmin, false},
		{core.IdentityTeammate, true, core.MemberUnchecked, core.ViaAdmin, false},
		{core.IdentityTeammate, false, core.MemberYes, core.ViaMembership, false},
		{core.IdentityPending, true, core.MemberYes, core.ViaMembership, false},
		{core.IdentityBlocked, true, core.MemberYes, core.ViaAdmin, false},
		{"", false, "", "", false},
	} {
		r := core.IdentityResolution{Status: c.status, SpaceOn: c.space, Member: c.member, Via: c.via}
		if got := r.Admitted(); got != c.want {
			t.Errorf("Admitted(%+v) = %t, want %t", r, got, c.want)
		}
	}
}

func TestAdmitMemberBecomesTeammateOnce(t *testing.T) {
	e := newEnv(t)
	ident := e.touch(t, "slack", "U1", "Maya Chen", core.MemberUnchecked)
	res, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes, Team: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Admitted() || res.Via != core.ViaMembership || res.Principal == "" || res.Lost {
		t.Fatalf("Admit member = %+v, want an admitted teammate", res)
	}
	p, err := e.store.Principals().Get(ctx, core.SystemViewer(), res.Principal)
	if err != nil || p.Kind != core.PrincipalPerson || p.User != "" || p.ToolProfile != "standard" || p.DisplayName != "Maya Chen" {
		t.Fatalf("person principal = %+v, %v", p, err)
	}
	sp, err := e.store.Spaces().Get(ctx, core.SystemViewer(), res.Principal)
	if err != nil || sp.Kind != core.SpacePerson || !sp.Enabled || sp.MentionOnly || sp.Chat.ChatID != "U1" {
		t.Fatalf("person space = %+v, %v", sp, err)
	}
	if id := identity(t, e, "slack", "U1"); id.Greet != core.GreetHello || id.Member != core.MemberYes || !id.MemberAt.Equal(e.clock.Now()) {
		t.Fatalf("identity = %+v, want the hello still owed and the verdict recorded now", id)
	}
	if rows := e.audited("identity.teammate"); len(rows) != 1 || rows[0].Actor != "channel:slack:U1" || rows[0].Target != "channel:slack:U1" ||
		string(rows[0].Detail) != `{"via":"membership"}` {
		t.Fatalf("identity.teammate audit = %+v", rows)
	}

	e.clock.Advance(2 * time.Hour)
	again, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes})
	if err != nil || again.Principal != res.Principal {
		t.Fatalf("second Admit = %+v, %v; want the same person space", again, err)
	}
	if n := len(e.audited("identity.teammate")); n != 1 {
		t.Fatalf("identity.teammate audited %d times", n)
	}
	team, _ := e.store.Principals().Team(ctx)
	if len(team) != 1 {
		t.Fatalf("team principals = %d, want 1", len(team))
	}
}

// A verdict asked for without a message (the sweep, a schedule's check,
// a press) doesn't make someone look active in People.
func TestAdmitLeavesLastSeenAlone(t *testing.T) {
	e := newEnv(t)
	seen := e.clock.Now()
	ident := e.touch(t, "slack", "U1", "Maya Chen", core.MemberUnchecked)
	if _, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(5 * time.Hour)
	if _, err := e.s.Admit(ctx, *identity(t, e, "slack", "U1"), core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	if id := identity(t, e, "slack", "U1"); !id.LastSeen.Equal(seen) || !id.MemberAt.Equal(e.clock.Now()) {
		t.Fatalf("identity = %+v, want last seen at the message (%v) and the verdict now", id, seen)
	}
	if _, err := e.s.Admit(ctx, core.ChannelIdentity{Channel: "slack", ExternalID: "U2", DisplayName: "Sam", LastSeen: e.clock.Now()},
		core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	if id := identity(t, e, "slack", "U2"); id.Status != core.IdentityTeammate || !id.FirstSeen.Equal(e.clock.Now()) {
		t.Fatalf("someone the hub hadn't recorded = %+v, want them recorded and let in", id)
	}
}

func TestAdmitReusesPersonSpaceOnTheirChat(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	sp := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: "telegram", ChatID: "5001", Kind: core.ChatDM}, Title: "Old",
		CreatedAt: now, UpdatedAt: now}
	if err := e.store.Spaces().Create(ctx, sp, &core.Principal{DisplayName: "Old", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ident := e.touch(t, "telegram", "5001", "Olivia", core.MemberUnchecked)
	res, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes})
	if err != nil || res.Principal != sp.Principal || !res.SpaceOn {
		t.Fatalf("Admit = %+v, %v; want the existing person space %s, turned on", res, err, sp.Principal)
	}
}

func TestAdmitOutsiderLosesAccessAndApproval(t *testing.T) {
	e := newEnv(t)
	ident := e.touch(t, "slack", "U1", "Maya", core.MemberUnchecked)
	res, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes})
	if err != nil {
		t.Fatal(err)
	}
	id := identity(t, e, "slack", "U1")
	id.CanApprove = true
	if err := e.store.Identities().Update(ctx, id); err != nil {
		t.Fatal(err)
	}
	out, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberOutsider})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != core.IdentityTeammate || out.Admitted() || out.CanApprove || !out.Lost || out.Principal != res.Principal {
		t.Fatalf("outsider after member = %+v, want a teammate who lost access and approval, chats kept", out)
	}
	rows := e.audited("identity.approver")
	if len(rows) != 1 || rows[0].Actor != "system" {
		t.Fatalf("identity.approver audit = %+v", rows)
	}
	var d map[string]any
	_ = json.Unmarshal(rows[0].Detail, &d)
	if d["can_approve"] != false || d["via"] != "membership" {
		t.Fatalf("identity.approver detail = %s", rows[0].Detail)
	}
	if again, _ := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberOutsider}); again.Lost {
		t.Fatal("Lost reported again for someone already out")
	}

	stranger := e.touch(t, "slack", "U2", "Guest", core.MemberUnchecked)
	g, err := e.s.Admit(ctx, stranger, core.Membership{Status: core.MemberOutsider})
	if err != nil || g.Status != core.IdentityPending || g.Lost || g.Principal != "" {
		t.Fatalf("outsider never admitted = %+v, %v", g, err)
	}
}

func TestAdmitBlockedStaysBlocked(t *testing.T) {
	e := newEnv(t)
	ident := e.touch(t, "slack", "U1", "Maya", core.MemberUnchecked)
	id := identity(t, e, "slack", "U1")
	id.Status = core.IdentityBlocked
	if err := e.store.Identities().Update(ctx, id); err != nil {
		t.Fatal(err)
	}
	res, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes})
	if err != nil || res.Status != core.IdentityBlocked || res.Admitted() {
		t.Fatalf("Admit of a blocked member = %+v, %v", res, err)
	}
}

func TestAdmitRenamesTheirPrincipal(t *testing.T) {
	e := newEnv(t)
	ident := e.touch(t, "slack", "U1", "Maya", core.MemberUnchecked)
	res, _ := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes})
	ident.DisplayName = "Maya Chen"
	if _, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.store.Principals().Get(ctx, core.SystemViewer(), res.Principal)
	sp, _ := e.store.Spaces().Get(ctx, core.SystemViewer(), res.Principal)
	if p.DisplayName != "Maya Chen" || sp.Title != "Maya Chen" {
		t.Fatalf("after a rename: principal %q, space %q", p.DisplayName, sp.Title)
	}
}

func TestAdmitUnknownKeepsARecentMemberVerdict(t *testing.T) {
	e := newEnv(t)
	ident := e.touch(t, "discord", "42", "Ana", core.MemberUnchecked)
	if _, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(3 * time.Hour)
	res, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberUnknown})
	if err != nil || res.Member != core.MemberYes || !res.Admitted() {
		t.Fatalf("unknown within 4 h of a member verdict = %+v, %v; want the verdict kept", res, err)
	}
	e.clock.Advance(2 * time.Hour)
	res, err = e.s.Admit(ctx, ident, core.Membership{Status: core.MemberUnknown})
	if err != nil || res.Member != core.MemberUnknown || res.Admitted() {
		t.Fatalf("unknown 5 h after the member verdict = %+v, %v; want unknown, no longer admitted", res, err)
	}
	// Discord can't vouch for them any more: an admin can let them in.
	owner := e.owner(t)
	if id, err := e.s.SetTeammate(ctx, owner, "discord", "42", TeammatePatch{Status: new(core.IdentityTeammate)}); err != nil ||
		id.Via != core.ViaAdmin || !resolved(t, e, "discord", "42").Admitted() || len(e.audited("identity.let_in")) != 1 {
		t.Fatalf("letting in a teammate Discord can't vouch for = %+v, %v", id, err)
	}

	tg := e.touch(t, "telegram", "7", "Tom", core.MemberUnknown)
	if _, err := e.s.SetTeammate(ctx, owner, "telegram", "7", TeammatePatch{Status: new(core.IdentityTeammate)}); err != nil {
		t.Fatal(err)
	}
	if res, err := e.s.Admit(ctx, tg, core.Membership{Status: core.MemberUnknown}); err != nil || !res.Admitted() {
		t.Fatalf("unknown for someone an admin let in = %+v, %v; want still admitted", res, err)
	}
}

func TestNoticeSenderOncePerDay(t *testing.T) {
	e := newEnv(t)
	ident := core.ChannelIdentity{Channel: "slack", ExternalID: "U9", DMChat: "D9"}
	first, err := e.s.NoticeSender(ctx, ident)
	if err != nil || !first {
		t.Fatalf("first notice = %t, %v", first, err)
	}
	e.clock.Advance(23 * time.Hour)
	if again, _ := e.s.NoticeSender(ctx, ident); again {
		t.Fatal("a second line within 24 h")
	}
	e.clock.Advance(time.Hour)
	if later, _ := e.s.NoticeSender(ctx, ident); !later {
		t.Fatal("no line after 24 h")
	}
	if id := identity(t, e, "slack", "U9"); id.DMChat != "D9" || !id.LastNotice.Equal(e.clock.Now()) {
		t.Fatalf("identity after notices = %+v", id)
	}
}

func TestSetTeammate(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	member := e.user(t, owner, "sam", core.RoleMember)
	memberActor := core.Actor{User: member.ID, Role: core.RoleMember, Via: "cookie"}
	let := TeammatePatch{Status: new(core.IdentityTeammate)}
	block := TeammatePatch{Status: new(core.IdentityBlocked)}
	approve := TeammatePatch{CanApprove: new(true)}

	e.touch(t, "telegram", "7", "Jordan", core.MemberUnknown)
	if _, err := e.s.SetTeammate(ctx, memberActor, "telegram", "7", let); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("a member letting someone in: %v", err)
	}
	if _, err := e.s.SetTeammate(ctx, owner, "telegram", "7", approve); err == nil || err.Error() != "Let them in first." {
		t.Fatalf("can_approve on someone pending: %v", err)
	}
	id, err := e.s.SetTeammate(ctx, owner, "telegram", "7", let)
	if err != nil || id.Status != core.IdentityTeammate || id.Via != core.ViaAdmin || id.Principal == "" {
		t.Fatalf("let in = %+v, %v", id, err)
	}
	if !resolved(t, e, "telegram", "7").Admitted() {
		t.Fatal("someone let in where Telegram can't tell isn't admitted")
	}
	if rows := e.audited("identity.let_in"); len(rows) != 1 || rows[0].Target != "channel:telegram:7" {
		t.Fatalf("identity.let_in audit = %+v", rows)
	}
	if id, err = e.s.SetTeammate(ctx, owner, "telegram", "7", approve); err != nil || !id.CanApprove {
		t.Fatalf("can_approve = %+v, %v", id, err)
	}
	if _, err := e.s.SetTeammate(ctx, owner, "telegram", "7", approve); err != nil || len(e.audited("identity.approver")) != 1 {
		t.Fatalf("setting can_approve again: %v, audits %d (only changes are audited)", err, len(e.audited("identity.approver")))
	}

	if id, err = e.s.SetTeammate(ctx, owner, "telegram", "7", block); err != nil || id.Status != core.IdentityBlocked || id.CanApprove {
		t.Fatalf("block = %+v, %v", id, err)
	}
	sp, _ := e.store.Spaces().Get(ctx, core.SystemViewer(), id.Principal)
	if sp.Enabled || resolved(t, e, "telegram", "7").Admitted() {
		t.Fatal("a blocked teammate's space is still on")
	}
	if id, err = e.s.SetTeammate(ctx, owner, "telegram", "7", let); err != nil || id.Status != core.IdentityTeammate || id.CanApprove {
		t.Fatalf("unblock = %+v, %v; Can approve must be turned on again", id, err)
	}
	if !resolved(t, e, "telegram", "7").Admitted() || len(e.audited("identity.unblock")) != 1 || len(e.audited("identity.block")) != 1 {
		t.Fatal("unblock didn't turn their space back on, or wasn't audited")
	}

	for _, c := range []struct {
		ext    string
		member core.MemberStatus
		want   string
	}{
		{"U1", core.MemberUnchecked, "Slack decides who can use Bothy there. They get in when they message Bothy, if they're in the team."},
		{"U2", core.MemberYes, "Slack decides who can use Bothy there. They get in when they message Bothy, if they're in the team."},
		{"U3", core.MemberOutsider, "Slack says they aren't in the team."},
	} {
		e.touch(t, "slack", c.ext, "", c.member)
		if _, err := e.s.SetTeammate(ctx, owner, "slack", c.ext, let); !errors.Is(err, core.ErrInvalid) || err.Error() != c.want {
			t.Errorf("letting in a Slack sender (%q): %v, want %q", c.member, err, c.want)
		}
	}
	ident := e.touch(t, "slack", "U4", "Maya", core.MemberUnchecked)
	if _, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberOutsider}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetTeammate(ctx, owner, "slack", "U4", approve); err == nil || err.Error() != "They aren't in the team any more." {
		t.Fatalf("can_approve for an outsider: %v", err)
	}
	if _, err := e.s.SetTeammate(ctx, owner, "slack", "U4", TeammatePatch{Status: new(core.IdentityPending)}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("a bad status: %v", err)
	}
	if _, err := e.s.SetTeammate(ctx, owner, "slack", "nobody", let); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("an unknown identity: %v", err)
	}
}

func TestForgetTeammate(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	e.touch(t, "telegram", "7", "Jordan", core.MemberUnknown)
	id, err := e.s.SetTeammate(ctx, owner, "telegram", "7", TeammatePatch{Status: new(core.IdentityTeammate)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ForgetTeammate(ctx, owner, "telegram", "7", nil); err == nil || err.Error() != "Block them first." {
		t.Fatalf("forgetting a teammate in good standing: %v", err)
	}
	if _, err := e.s.SetTeammate(ctx, owner, "telegram", "7", TeammatePatch{Status: new(core.IdentityBlocked)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ForgetTeammate(ctx, core.Actor{User: "usr_x", Role: core.RoleMember, Via: "cookie"}, "telegram", "7", nil); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("a member forgetting someone: %v", err)
	}
	p, err := e.s.ForgetTeammate(ctx, owner, "telegram", "7", map[string]any{"sessions": 2})
	if err != nil || p != id.Principal {
		t.Fatalf("ForgetTeammate = %s, %v", p, err)
	}
	if _, err := e.store.Principals().Get(ctx, core.SystemViewer(), p); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("principal after forgetting: %v", err)
	}
	got := identity(t, e, "telegram", "7")
	if got.Status != core.IdentityBlocked || got.Principal != "" || got.Via != "" || got.CanApprove || got.Greet != core.GreetHello {
		t.Fatalf("identity after forgetting = %+v, want blocked with no principal", got)
	}
	rows := e.audited("identity.forget")
	if len(rows) != 1 {
		t.Fatalf("identity.forget audits = %d", len(rows))
	}
	var d map[string]any
	_ = json.Unmarshal(rows[0].Detail, &d)
	if d["principal"] != string(p) || d["sessions"] != float64(2) {
		t.Fatalf("identity.forget detail = %s", rows[0].Detail)
	}

	// Someone no longer in the team can be forgotten without blocking them.
	ident := e.touch(t, "slack", "U1", "Maya", core.MemberUnchecked)
	if _, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberOutsider}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ForgetTeammate(ctx, owner, "slack", "U1", nil); err != nil {
		t.Fatal(err)
	}
	if got := identity(t, e, "slack", "U1"); got.Status != core.IdentityPending || got.Principal != "" {
		t.Fatalf("forgotten outsider = %+v, want pending with no principal", got)
	}
}

func TestResolveIdentitySpaceOn(t *testing.T) {
	e := newEnv(t)
	if res := resolved(t, e, "telegram", "1"); res.Status != "" {
		t.Fatalf("unknown sender = %+v", res)
	}
	ident := e.touch(t, "slack", "U1", "Maya", core.MemberUnchecked)
	res, _ := e.s.Admit(ctx, ident, core.Membership{Status: core.MemberYes})
	sp, _ := e.store.Spaces().Get(ctx, core.SystemViewer(), res.Principal)
	sp.Enabled = false
	if err := e.store.Spaces().Update(ctx, sp); err != nil {
		t.Fatal(err)
	}
	if got := resolved(t, e, "slack", "U1"); got.SpaceOn || got.Admitted() || got.Status != core.IdentityTeammate {
		t.Fatalf("teammate with their space off = %+v", got)
	}
}
