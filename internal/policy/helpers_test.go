package policy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/events"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
	"github.com/bothyhq/bothy/internal/testkit/memstore"
)

// harness is a policy engine and approvals over memstore with one owner,
// one member, a group space owned by the owner and a session for each.
type harness struct {
	t      *testing.T
	ctx    context.Context
	store  core.Store
	cfg    *config.Config
	bus    *events.Bus
	audit  *fakes.Auditor
	engine *Engine
	ap     *Approvals

	owner, member     *core.User
	ownerP, memberP   core.Principal
	space             core.Principal
	session, session2 core.SessionID
	memberSession     core.SessionID
	spaceSession      core.SessionID
}

func newHarness(t *testing.T, mod func(*config.Config)) *harness {
	t.Helper()
	cfg := config.Default()
	if mod != nil {
		mod(cfg)
	}
	h := &harness{t: t, ctx: context.Background(), store: memstore.New(), cfg: cfg, audit: &fakes.Auditor{}}
	h.bus = events.New(events.Options{})
	var err error
	h.engine, h.ap, err = New(Deps{Store: h.store, Config: config.Static(cfg), Bus: h.bus, Audit: h.audit, Clock: core.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	h.owner = h.user("owner", core.RoleOwner)
	h.member = h.user("member", core.RoleMember)
	h.ownerP = h.principal(core.UserPrincipal(h.owner.ID))
	h.memberP = h.principal(core.UserPrincipal(h.member.ID))
	sp := &core.Space{Kind: "group", Chat: core.ChatRef{Channel: "telegram", ChatID: "-100", Kind: core.ChatGroup}, Title: "g", Owner: h.owner.ID, Enabled: true}
	pr := &core.Principal{DisplayName: "g"}
	if err := h.store.Spaces().Create(h.ctx, sp, pr); err != nil {
		t.Fatal(err)
	}
	h.space = *pr
	h.session = h.newSession(h.ownerP.ID)
	h.session2 = h.newSession(h.ownerP.ID)
	h.memberSession = h.newSession(h.memberP.ID)
	h.spaceSession = h.newSession(h.space.ID)
	return h
}

func (h *harness) user(name string, role core.Role) *core.User {
	h.t.Helper()
	u := &core.User{Username: name, Role: role, CreatedAt: time.Now()}
	if err := h.store.Users().Create(h.ctx, u); err != nil {
		h.t.Fatal(err)
	}
	return u
}

func (h *harness) principal(id core.PrincipalID) core.Principal {
	h.t.Helper()
	p, err := h.store.Principals().Get(h.ctx, core.SystemViewer(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return *p
}

func (h *harness) newSession(p core.PrincipalID) core.SessionID {
	h.t.Helper()
	s := &core.Session{Principal: p, Origin: "web", CreatedAt: time.Now()}
	if err := h.store.Sessions().Create(h.ctx, s); err != nil {
		h.t.Fatal(err)
	}
	return s.ID
}

func (h *harness) newRun(s core.SessionID, p core.PrincipalID, u core.UserID) core.RunID {
	h.t.Helper()
	r := &core.Run{Session: s, Principal: p, User: u, Trigger: core.TrigMessage, Status: core.RunQueued, CreatedAt: time.Now()}
	if err := h.store.Runs().Create(h.ctx, r); err != nil {
		h.t.Fatal(err)
	}
	return r.ID
}

// spec builds a tool spec; SpaceOK is on so that only the side-effect class
// decides what restricted spaces may do unless a test says otherwise.
func spec(name string, se core.SideEffect, mods ...func(*core.Manifest)) core.ToolSpec {
	m := core.Manifest{Title: "Run " + name, SideEffect: se, SpaceOK: true}
	for _, f := range mods {
		f(&m)
	}
	return core.ToolSpec{
		Def:      core.ToolDef{Name: name, Description: name, Schema: json.RawMessage(`{"type":"object"}`)},
		Source:   "builtin",
		Manifest: m,
	}
}

func action(tool string, kind core.ActionKind, se core.SideEffect, payload any, mods ...func(*core.Action)) *core.Action {
	b, err := core.CanonicalJSON(payload)
	if err != nil {
		panic(err)
	}
	a := &core.Action{Tool: tool, Kind: kind, SideEffect: se, Payload: b}
	for _, f := range mods {
		f(a)
	}
	a.Fingerprint = core.ActionFingerprint(a.Tool, a.Payload)
	return a
}

func shellAction(cmd string) *core.Action {
	argv := []string{"bash", "-lc", cmd}
	return action("shell", core.ActExec, core.SELocalWrite, map[string]any{"argv": argv, "cwd": ""}, func(a *core.Action) { a.Argv = argv })
}

func httpAction(tool, method, rawURL string, se core.SideEffect) *core.Action {
	hp, _ := urlHostPort(rawURL)
	return action(tool, core.ActHTTP, se, map[string]any{"url": rawURL, "method": method}, func(a *core.Action) {
		a.URL, a.Method, a.Host, a.Port = rawURL, method, hp.host, hp.port
	})
}

func sendAction(tool, recipient string, se core.SideEffect) *core.Action {
	return action(tool, core.ActSend, se, map[string]any{"to": recipient, "text": "hi"}, func(a *core.Action) { a.Recipient = recipient })
}

// in builds an interactive owner input for the action.
func (h *harness) in(a *core.Action, s core.ToolSpec) core.PolicyInput {
	return core.PolicyInput{
		User: h.owner.ID, Role: core.RoleOwner, Principal: h.ownerP, Session: h.session,
		Trigger: core.TrigMessage, Action: a, Spec: s, Provenance: core.ProvUser,
	}
}

func (h *harness) decide(in core.PolicyInput) core.Decision {
	h.t.Helper()
	return h.engine.Decide(h.ctx, in)
}

func (h *harness) grant(g core.Grant) core.GrantID {
	h.t.Helper()
	if g.CreatedBy == "" {
		g.CreatedBy = h.owner.ID
	}
	id, err := h.engine.Grant(h.ctx, g)
	if err != nil {
		h.t.Fatalf("grant %+v: %v", g, err)
	}
	return id
}

func hasChoice(d core.Decision, c core.Choice) bool {
	for _, x := range d.Choices {
		if x == c {
			return true
		}
	}
	return false
}
