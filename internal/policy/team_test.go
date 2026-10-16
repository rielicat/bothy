package policy

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// teammate adds a person in a chat app with their person space and a
// session; canApprove marks them "Can approve".
func (h *harness) teammate(channel, ext, name string, canApprove bool) (core.Principal, core.SessionID) {
	h.t.Helper()
	now := time.Now()
	sp := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: channel, ChatID: ext, Kind: core.ChatDM}, Title: name,
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	pr := &core.Principal{DisplayName: name, CreatedAt: now}
	if err := h.store.Spaces().Create(h.ctx, sp, pr); err != nil {
		h.t.Fatal(err)
	}
	id, err := h.store.Identities().Touch(h.ctx, &core.ChannelIdentity{Channel: channel, ExternalID: ext, DisplayName: name,
		Username: strings.ToLower(name), LastSeen: now})
	if err != nil {
		h.t.Fatal(err)
	}
	id.Status, id.Principal, id.Via, id.CanApprove, id.Member, id.MemberAt = core.IdentityTeammate, sp.Principal, core.ViaMembership,
		canApprove, core.MemberYes, now
	if err := h.store.Identities().Update(h.ctx, id); err != nil {
		h.t.Fatal(err)
	}
	return *pr, h.newSession(sp.Principal)
}

// personIn is an interactive input of a teammate's own message.
func personIn(p core.Principal, s core.SessionID, a *core.Action, sp core.ToolSpec) core.PolicyInput {
	return core.PolicyInput{Role: core.RoleMember, Principal: p, Session: s, Trigger: core.TrigMessage, Action: a, Spec: sp,
		Provenance: core.ProvUser}
}

func TestTeammateFloor(t *testing.T) {
	h := newHarness(t, nil)
	maya, sess := h.teammate("slack", "U1", "Maya Chen", false)
	write := action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"path": "notes.txt", "content": "x"})
	writeSpec := spec("fs_write", core.SELocalWrite)

	d := h.decide(personIn(maya, sess, write, writeSpec))
	if d.Verdict != core.VerdictAsk || d.Rule != floorTeammate || !d.Floor || !d.Approver.Team || d.Approver.User != "" {
		t.Fatalf("a teammate's local write = %+v, want FP asking the team", d)
	}
	if !hasChoice(d, core.AllowSession) || !hasChoice(d, core.AllowAlways) || !hasChoice(d, core.AllowOnce) {
		t.Errorf("FP choices = %v, want grants offered", d.Choices)
	}
	if d.Reason != teammateReason {
		t.Errorf("FP reason = %q", d.Reason)
	}

	memory := action(toolMemory, core.ActMemory, core.SELocalWrite, map[string]any{"op": "add", "new": "x"})
	if d := h.decide(personIn(maya, sess, memory, spec(toolMemory, core.SELocalWrite))); d.Verdict != core.VerdictAuto {
		t.Errorf("a teammate's own memory write = %+v, want auto", d)
	}
	read := action("fs_read", core.ActFSRead, core.SERead, map[string]any{"path": "notes.txt"})
	if d := h.decide(personIn(maya, sess, read, spec("fs_read", core.SERead))); d.Verdict != core.VerdictAuto {
		t.Errorf("a teammate's read = %+v, want auto", d)
	}
	checkout := action(toolRepoCheckout, core.ActGeneric, core.SELocalWrite, map[string]any{"repo": "acme/web"})
	if d := h.decide(personIn(maya, sess, checkout, spec(toolRepoCheckout, core.SELocalWrite))); d.Verdict != core.VerdictAuto {
		t.Errorf("a fresh checkout = %+v, want auto", d)
	}
	discard := action(toolRepoCheckout, core.ActGeneric, core.SEIrreversible, map[string]any{"repo": "acme/web", "discard": true})
	if d := h.decide(personIn(maya, sess, discard, spec(toolRepoCheckout, core.SEIrreversible))); d.Verdict != core.VerdictAsk {
		t.Errorf("a discarding checkout = %+v, want an ask", d)
	}

	h.cfg.Policy.Rules = []config.Rule{{ID: "ok", Tool: "fs_write", Verdict: "auto"}}
	if d := h.decide(personIn(maya, sess, write, writeSpec)); d.Verdict != core.VerdictAsk || d.Rule != floorTeammate {
		t.Errorf("an auto rule lifted FP: %+v", d)
	}
	h.cfg.Policy.Rules = []config.Rule{{ID: "no", Tool: "fs_write", Verdict: "deny", Reason: "not here"}}
	if d := h.decide(personIn(maya, sess, write, writeSpec)); d.Verdict != core.VerdictDeny || d.Rule != "rule:no" {
		t.Errorf("a deny rule under FP = %+v, want the rule's deny", d)
	}
	h.cfg.Policy.Rules = nil
	h.grant(core.Grant{Principal: maya.ID, Scope: core.GrantSession, ScopeID: string(sess), Kind: core.GrantTool, Tool: "fs_write",
		Pattern: map[string]string{"/path": "notes.txt"}, MaxSE: core.SELocalWrite})
	if d := h.decide(personIn(maya, sess, write, writeSpec)); d.Verdict != core.VerdictAuto || !strings.HasPrefix(d.Rule, "grant:") {
		t.Errorf("a grant under FP = %+v, want it to answer", d)
	}
}

func TestTeammateEarlierFloorsDecideFirst(t *testing.T) {
	h := newHarness(t, nil)
	maya, sess := h.teammate("slack", "U1", "Maya Chen", false)

	net := action(toolNetAllow, core.ActNet, core.SEExternalSend, map[string]any{"host": "api.example.com", "port": 443},
		func(a *core.Action) { a.Host, a.Port = "api.example.com", 443 })
	d := h.decide(personIn(maya, sess, net, spec(toolNetAllow, core.SEExternalSend)))
	if d.Rule != "floor:net_allow" || hasChoice(d, core.AllowOnce) || !d.Approver.Team {
		t.Errorf("net_allow for a teammate = %+v, want F9 with its own choices", d)
	}

	send := sendAction("message", "slack:C9:", core.SEExternalSend)
	in := personIn(maya, sess, send, spec("message", core.SEExternalSend))
	in.Taint = core.TaintPrivate | core.TaintUntrusted
	if d := h.decide(in); d.Rule != floorTrifecta || hasChoice(d, core.AllowSession) || !d.Approver.Team {
		t.Errorf("an exposed teammate's send = %+v, want the trifecta floor first", d)
	}

	memory := action(toolMemory, core.ActMemory, core.SELocalWrite, map[string]any{"op": "add", "new": "x"})
	in = personIn(maya, sess, memory, spec(toolMemory, core.SELocalWrite))
	in.Taint = core.TaintUntrusted
	if d := h.decide(in); d.Rule != "floor:tainted_memory" || !d.Approver.Team {
		t.Errorf("a teammate's memory write in a tainted session = %+v, want F7", d)
	}

	forwarded := action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"path": "a", "content": "x"})
	in = personIn(maya, sess, forwarded, spec("fs_write", core.SELocalWrite))
	in.Provenance = core.ProvUntrusted
	if d := h.decide(in); d.Rule != "floor:untrusted_origin" || hasChoice(d, core.AllowSession) {
		t.Errorf("forwarded content in a teammate's DM = %+v, want F6 with no grants", d)
	}

	post := httpAction("http_request", "POST", "https://hooks.example.com/x", core.SEExternalSend)
	d = h.decide(personIn(maya, sess, post, spec("http_request", core.SEExternalSend)))
	if d.Rule != floorTeammate || !slices.Contains(d.Warnings, "Bothy hasn't sent data to hooks.example.com before.") {
		t.Errorf("a teammate's first POST = %+v, want FP with the first-contact warning", d)
	}
	pr := action(toolRepoOpenPR, core.ActSend, core.SEExternalSend, map[string]any{"repo": "acme/web"}, func(a *core.Action) { a.Recipient = "github:acme/web" })
	d = h.decide(personIn(maya, sess, pr, spec(toolRepoOpenPR, core.SEExternalSend)))
	if !slices.ContainsFunc(d.Warnings, func(w string) bool { return strings.HasSuffix(w, "for Maya Chen.") }) {
		t.Errorf("first pull request warning = %v, want it to name the teammate", d.Warnings)
	}
}

func TestChannelAndUserApprovers(t *testing.T) {
	h := newHarness(t, nil)
	standard := h.space
	standard.ToolProfile = "standard"
	write := action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"path": "a", "content": "x"})
	in := core.PolicyInput{Role: core.RoleMember, Principal: standard, Session: h.spaceSession, Trigger: core.TrigMessage, Action: write,
		Spec: spec("fs_write", core.SELocalWrite), Provenance: core.ProvUntrusted}
	d := h.decide(in)
	if d.Rule != "floor:untrusted_origin" || !d.Approver.Team || hasChoice(d, core.AllowSession) || hasChoice(d, core.AllowAlways) {
		t.Errorf("a channel's write = %+v, want F6 asking the team with no grants", d)
	}
	send := sendAction("send_mail", "bob@example.com", core.SEExternalSend)
	if d := h.decide(h.in(send, spec("send_mail", core.SEExternalSend))); d.Approver.User != h.owner.ID || d.Approver.Team {
		t.Errorf("an account's request = %+v, want the account to approve", d.Approver)
	}
}

func TestReleaseFromTeammate(t *testing.T) {
	h := newReleaseHarness(t)
	maya, sess := h.teammate("slack", "U1", "Maya Chen", true)
	in := personIn(maya, sess, releaseAction("staging", "main", stagingInputs), releaseSpec())

	h.cfg.Releases.Environments[0].Required = 1
	d := h.decide(in)
	if d.Verdict != core.VerdictAsk || d.Approver.Requester != "" || d.Approver.Required != 2 ||
		!strings.Contains(d.Reason, "2 approvers must approve, one more than usual") {
		t.Fatalf("deploy from a teammate = %+v, want one more vote than the environment's 1", d)
	}
	h.cfg.Releases.Environments[0].Required = 2
	d = h.decide(in)
	if d.Verdict != core.VerdictAsk || d.Approver.Required != 3 {
		t.Fatalf("with 3 eligible admins = %+v, want 3 votes", d)
	}
	h.cfg.Releases.Environments[0].Required = 3
	d = h.decide(in)
	if d.Verdict != core.VerdictDeny || !strings.Contains(d.Reason, "asked from a chat app needs 4 approvers, one more than usual") ||
		!strings.Contains(d.Reason, "only 3 other people can approve it") {
		t.Fatalf("without enough approvers = %+v, want the chat-app refusal", d)
	}
}

// teamRequest files a team approval for a channel's or teammate's send.
func (h *harness) teamRequest(p core.Principal, sess core.SessionID, sender string, choices ...core.Choice) *core.Approval {
	h.t.Helper()
	a := sendAction("send_mail", "bob@example.com", core.SEExternalSend)
	s := spec("send_mail", core.SEExternalSend)
	d := core.Decision{Verdict: core.VerdictAsk, Rule: floorTeammate, Approver: core.ApproverReq{Team: true},
		Choices: append(choices, core.AllowOnce, core.Deny)}
	run := h.newRun(sess, p.ID, "")
	chat := core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: sess, Principal: p.ID, ToolCallID: core.NewID(core.PrefixToolCall),
		Action: a, Spec: s, Decision: d, Chat: &chat, Context: "Slack · #eng · session “secret plans”", Sender: sender})
	if err != nil {
		h.t.Fatal(err)
	}
	return ap
}

// cardRef records a card message for ap so a chat press on it counts.
func (h *harness) cardRef(ap *core.Approval, channel, chatID string) *core.MsgRef {
	h.t.Helper()
	ref := core.MsgRef{Chat: core.ChatRef{Channel: channel, ChatID: chatID, Kind: core.ChatDM}, ID: core.NewID("m")}
	if err := h.ap.AddMessageRef(h.ctx, ap.ID, ref, false); err != nil {
		h.t.Fatal(err)
	}
	return &ref
}

func TestTeamRequest(t *testing.T) {
	h := newHarness(t, nil)
	maya, sess := h.teammate("slack", "U1", "Maya Chen", false)
	sub := h.bus.Subscribe(core.Filter{System: true}, 32)
	defer sub.Close()

	ap := h.teamRequest(maya, sess, "slack:U1", core.AllowSession, core.AllowAlways)
	if ap.Approver.RequesterIdentity != "slack:U1" || ap.Decision.Approver.RequesterIdentity != "slack:U1" {
		t.Errorf("requester identity = %q / %q", ap.Approver.RequesterIdentity, ap.Decision.Approver.RequesterIdentity)
	}
	if ap.Card.Context != "Maya Chen's direct messages on Slack" {
		t.Errorf("team card context = %q, want whose chat it is, never its title", ap.Card.Context)
	}
	if r := ap.Requester; r == nil || r.Name != "Maya Chen" || r.App != "slack" || r.Identity != "slack:U1" || r.Handle != "maya chen" || r.Chat != "" {
		t.Errorf("requester = %+v", ap.Requester)
	}
	if !slices.Contains(ap.Decision.Choices, core.AllowAlways) {
		t.Errorf("team choices = %v, want grants kept", ap.Decision.Choices)
	}
	var toAdmins bool
	for done := false; !done; {
		select {
		case ev := <-sub.C():
			toAdmins = toAdmins || ev.Type == core.EvApprovalRequested && ev.Principal == ""
		default:
			done = true
		}
	}
	if !toAdmins {
		t.Error("a team request isn't published to the admins' inboxes")
	}

	ch := h.teamRequest(h.space, h.spaceSession, "slack:U2")
	if ch.Card.Context != "g on Telegram" {
		t.Errorf("channel card context = %q", ch.Card.Context)
	}
	if r := ch.Requester; r == nil || r.Identity != "slack:U2" || r.Chat != "g" || r.Name != "U2" {
		t.Errorf("channel requester = %+v", ch.Requester)
	}
	sched := h.teamRequest(h.space, h.spaceSession, "schedule:sch_1")
	if sched.Approver.RequesterIdentity != "" || sched.Requester == nil || sched.Requester.Name != "g" || sched.Requester.App != "telegram" {
		t.Errorf("a schedule's team request = %+v / %+v, want the channel named", sched.Approver, sched.Requester)
	}

	for range 2 {
		h.teamRequest(maya, sess, "slack:U1")
	}
	h.teamRequest(maya, sess, "slack:U1")
	h.teamRequest(maya, sess, "slack:U1")
	run := h.newRun(sess, maya.ID, "")
	_, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: sess, Principal: maya.ID, ToolCallID: "x",
		Action: sendAction("send_mail", "bob@example.com", core.SEExternalSend), Spec: spec("send_mail", core.SEExternalSend),
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{Team: true}}})
	if !errors.Is(err, core.ErrBusy) || !strings.Contains(err.Error(), "still waiting on earlier requests") {
		t.Fatalf("a sixth open team request = %v, want ErrBusy", err)
	}

	for _, bad := range []core.ApproverReq{
		{Team: true, User: h.owner.ID},
		{Team: true, AdminOnly: true},
		{Team: true, Roles: []core.Role{core.RoleAdmin}},
		{Users: []core.UserID{h.owner.ID}},
	} {
		r := h.newRun(h.session, h.ownerP.ID, h.owner.ID)
		_, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: r, Session: h.session, Principal: h.ownerP.ID, ToolCallID: "x",
			Action: sendAction("send_mail", "bob@example.com", core.SEExternalSend), Spec: spec("send_mail", core.SEExternalSend),
			Decision: core.Decision{Verdict: core.VerdictAsk, Approver: bad}})
		if !errors.Is(err, core.ErrInvalid) {
			t.Errorf("approver %+v: %v, want invalid", bad, err)
		}
	}
	tom, tomSess := h.teammate("telegram", "7", "Tom", false)
	r := h.newRun(tomSess, tom.ID, "")
	if _, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: r, Session: tomSess, Principal: tom.ID, ToolCallID: "x",
		Action: sendAction("send_mail", "bob@example.com", core.SEExternalSend), Spec: spec("send_mail", core.SEExternalSend),
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2}}}); err != nil {
		t.Errorf("a teammate's multi-approver request without a requester account: %v", err)
	}
}

func TestTeamAuthorize(t *testing.T) {
	h := newHarness(t, nil)
	maya, sess := h.teammate("slack", "U1", "Maya Chen", false)
	_, _ = h.teammate("slack", "U9", "Sam", true)
	_, _ = h.teammate("slack", "U8", "Lee", false)
	admin := h.user("ada", core.RoleAdmin)

	resolve := func(ap *core.Approval, r core.Resolution) error {
		r.ID = ap.ID
		_, err := h.ap.Resolve(h.ctx, r)
		return err
	}
	ap := h.teamRequest(maya, sess, "slack:U1", core.AllowSession, core.AllowAlways)
	if err := resolve(ap, core.Resolution{Choice: core.AllowOnce, By: h.member.ID, Via: "web"}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a member deciding a team request: %v", err)
	}
	if err := resolve(ap, core.Resolution{Choice: core.AllowOnce, ByIdentity: "slack:U8", Via: "slack", ViaMsg: h.cardRef(ap, "slack", "D8")}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a teammate who can't approve: %v", err)
	}
	if err := resolve(ap, core.Resolution{Choice: core.AllowOnce, ByIdentity: "slack:U9", Via: "slack"}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a chat press from an unrecorded message: %v", err)
	}
	if err := resolve(ap, core.Resolution{Choice: core.AllowOnce, ByIdentity: "slack:U9", Via: "telegram", ViaMsg: h.cardRef(ap, "telegram", "D9")}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("an identity pressing on another chat app: %v", err)
	}
	if err := resolve(ap, core.Resolution{Choice: core.AllowAlways, ByIdentity: "slack:U9", Via: "slack", ViaMsg: h.cardRef(ap, "slack", "D9")}); err != nil {
		t.Fatalf("a chat approver always-allowing for a teammate: %v", err)
	}
	got := h.status(ap.ID)
	if got.Status != core.ApprovalAllowed || got.DecidedBy != "" || got.DecidedByIdentity != "slack:U9" {
		t.Fatalf("decided = %+v, want the identity recorded", got)
	}
	g, ok := h.engine.NarrowGrant(got.Action, got.Principal, core.GrantPrincipal, "", core.Decider{Identity: got.DecidedByIdentity})
	if !ok {
		t.Fatal("no narrow grant")
	}
	if _, err := h.engine.Grant(h.ctx, g); err != nil {
		t.Fatalf("a chat approver's grant: %v", err)
	}
	if gs, _ := h.store.Grants().List(h.ctx, core.Viewer{Principals: []core.PrincipalID{maya.ID}}, "", ""); len(gs) != 1 ||
		gs[0].CreatedByIdentity != "slack:U9" || gs[0].CreatedBy != "" {
		t.Fatalf("grants = %+v, want one created by the chat approver", gs)
	}

	web := h.teamRequest(maya, sess, "slack:U1")
	if err := resolve(web, core.Resolution{Choice: core.AllowOnce, By: admin.ID, Via: "web"}); err != nil {
		t.Fatalf("an admin deciding a team request on the web: %v", err)
	}

	standard := h.space
	channel := h.teamRequest(standard, h.spaceSession, "slack:U2", core.AllowSession, core.AllowAlways)
	err := resolve(channel, core.Resolution{Choice: core.AllowAlways, ByIdentity: "slack:U9", Via: "slack", ViaMsg: h.cardRef(channel, "slack", "D9")})
	if !errors.Is(err, core.ErrForbidden) || !strings.Contains(err.Error(), notChannelAlways) {
		t.Fatalf("always-allow for a channel from chat: %v", err)
	}

	withdraw := h.teamRequest(maya, sess, "slack:U1")
	if err := resolve(withdraw, core.Resolution{Choice: core.Deny, ByIdentity: "slack:U8", Via: "slack", ViaMsg: h.cardRef(withdraw, "slack", "C1")}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("someone else withdrawing: %v", err)
	}
	if err := resolve(withdraw, core.Resolution{Choice: core.AllowOnce, ByIdentity: "slack:U1", Via: "slack", ViaMsg: h.cardRef(withdraw, "slack", "C2")}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("the requester approving their own request without Can approve: %v", err)
	}
	if err := resolve(withdraw, core.Resolution{Choice: core.Deny, ByIdentity: "slack:U1", Via: "slack", ViaMsg: h.cardRef(withdraw, "slack", "C3")}); err != nil {
		t.Fatalf("the requester withdrawing: %v", err)
	}
	if got := h.status(withdraw.ID); got.Status != core.ApprovalDenied || got.DecidedByIdentity != "slack:U1" {
		t.Fatalf("withdrawn = %+v", got)
	}

	r := h.newRun(sess, maya.ID, "")
	ao, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: r, Session: sess, Principal: maya.ID, ToolCallID: "x",
		Action: sendAction("send_mail", "bob@example.com", core.SEExternalSend), Spec: spec("send_mail", core.SEExternalSend),
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{AdminOnly: true}}, Sender: "slack:U1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := resolve(ao, core.Resolution{Choice: core.AllowOnce, ByIdentity: "slack:U9", Via: "slack", ViaMsg: h.cardRef(ao, "slack", "D9")}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a chat approver deciding an admin-only request: %v", err)
	}
	if err := resolve(ao, core.Resolution{Choice: core.Deny, ByIdentity: "slack:U1", Via: "slack", ViaMsg: h.cardRef(ao, "slack", "C4")}); err != nil {
		t.Errorf("the requester withdrawing an admin-only request: %v", err)
	}

	// A member verdict older than a full card allows can't approve: the
	// hub couldn't renew it. Where the chat app can't tell (Telegram), an
	// admin's let-in has no verdict to age.
	id, _ := h.store.Identities().Get(h.ctx, "slack", "U9")
	id.MemberAt = time.Now().Add(-core.ApproverFresh - time.Minute)
	if err := h.store.Identities().Update(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	old := h.teamRequest(maya, sess, "slack:U1")
	if err := resolve(old, core.Resolution{Choice: core.AllowOnce, ByIdentity: "slack:U9", Via: "slack", ViaMsg: h.cardRef(old, "slack", "D9")}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a chat approver with a stale verdict: %v", err)
	}
	_, _ = h.teammate("telegram", "55", "Tess", true)
	tess, _ := h.store.Identities().Get(h.ctx, "telegram", "55")
	tess.Member, tess.MemberAt, tess.Via = core.MemberUnknown, time.Now().Add(-48*time.Hour), core.ViaAdmin
	if err := h.store.Identities().Update(h.ctx, tess); err != nil {
		t.Fatal(err)
	}
	if err := resolve(old, core.Resolution{Choice: core.AllowOnce, ByIdentity: "telegram:55", Via: "telegram", ViaMsg: h.cardRef(old, "telegram", "55")}); err != nil {
		t.Errorf("a Telegram approver an admin let in: %v", err)
	}
	id.MemberAt = time.Now()

	// An outsider who can still approve on paper can't press.
	id.Member = core.MemberOutsider
	if err := h.store.Identities().Update(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	left := h.teamRequest(maya, sess, "slack:U1")
	if err := resolve(left, core.Resolution{Choice: core.AllowOnce, ByIdentity: "slack:U9", Via: "slack", ViaMsg: h.cardRef(left, "slack", "D9")}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("a chat approver no longer in the team: %v", err)
	}
}

func TestTeammateReleaseVotesFromChat(t *testing.T) {
	h := newReleaseHarness(t)
	maya, sess := h.teammate("slack", "U1", "Maya Chen", true)
	run := h.newRun(sess, maya.ID, "")
	a := releaseAction("staging", "main", stagingInputs)
	d := h.decide(personIn(maya, sess, a, releaseSpec()))
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: sess, Principal: maya.ID, ToolCallID: "x", Action: a,
		Spec: releaseSpec(), Decision: d, Sender: "slack:U1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, ByIdentity: "slack:U1", Via: "slack",
		ViaMsg: h.cardRef(ap, "slack", "D1")}); !errors.Is(err, core.ErrForbidden) || !strings.Contains(err.Error(), webVotesOnly) {
		t.Fatalf("a chat vote: %v", err)
	}
	got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, ByIdentity: "slack:U1", Via: "slack",
		ViaMsg: h.cardRef(ap, "slack", "D1b")})
	if err != nil || got.Status != core.ApprovalDenied || got.DecidedByIdentity != "slack:U1" {
		t.Fatalf("the requester's withdraw = %+v, %v", got, err)
	}
}

// A waiting line's Cancel request withdraws for the person who asked only,
// even when a chat approver presses it.
func TestNoticeOnlyWithdraws(t *testing.T) {
	h := newHarness(t, nil)
	h.teammate("slack", "U1", "Maya Chen", false)
	h.teammate("slack", "U9", "Sam", true)
	ap := h.teamRequest(h.space, h.spaceSession, "slack:U1")
	line := core.MsgRef{Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, ID: "notice-1"}
	if err := h.store.Approvals().AddMessage(h.ctx, ap.ID, core.CardMessage{Ref: line, Truncated: true, Kind: core.CardMsgNotice}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, ByIdentity: "slack:U9", Via: "slack", ViaMsg: &line}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("a chat approver pressing Cancel request: %v", err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, ByIdentity: "slack:U1", Via: "slack", ViaMsg: &line}); err != nil {
		t.Fatalf("the requester pressing Cancel request: %v", err)
	}
}
