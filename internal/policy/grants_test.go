package policy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

func narrowOf(t *testing.T, h *harness, a *core.Action, scope core.GrantScope, scopeID string) core.Grant {
	t.Helper()
	g, ok := h.engine.NarrowGrant(a, h.ownerP.ID, scope, scopeID, h.owner.ID)
	if !ok {
		t.Fatalf("no narrow grant for %s %s", a.Tool, a.Payload)
	}
	return g
}

// matches reports whether grant g covers action a (side-effect ceiling included).
func matches(g core.Grant, a *core.Action, s core.ToolSpec) bool {
	x, err := newInput(core.PolicyInput{Action: a, Spec: s}, config.Default())
	if err != nil {
		panic(err)
	}
	return g.MaxSE >= x.se && grantMatches(&g, x)
}

func TestNarrowGrantNeverWholeTool(t *testing.T) {
	h := newHarness(t, nil)
	actions := []*core.Action{
		shellAction("ls -la"),
		action("git", core.ActExec, core.SELocalWrite, map[string]any{"argv": []string{"git", "status"}}),
		action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"path": "notes/a.txt", "content": "x"}),
		action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"path": "a.txt", "content": "x"}),
		action("fs_delete", core.ActFSDelete, core.SEIrreversible, map[string]any{"path": "notes/a.txt"}),
		httpAction("web_fetch", "GET", "https://example.com/x", core.SERead),
		netAction("pypi.org", 443),
		sendAction("send_mail", "bob@example.com", core.SEExternalSend),
		action("mcp__mail__send", core.ActMCP, core.SEExternalSend, map[string]any{"to": "bob@example.com", "body": "hi"}, func(a *core.Action) { a.Recipient = "bob@example.com" }),
		action("mcp__docs__search", core.ActMCP, core.SERead, map[string]any{"q": "go"}),
		action("memory", core.ActMemory, core.SELocalWrite, map[string]any{"op": "add", "new": "x"}),
		action("generic", core.ActGeneric, core.SELocalWrite, map[string]any{"a": []int{1, 2}}),
	}
	for _, a := range actions {
		for _, scope := range []core.GrantScope{core.GrantSession, core.GrantPrincipal} {
			id := ""
			if scope == core.GrantSession {
				id = string(h.session)
			}
			g, ok := h.engine.NarrowGrant(a, h.ownerP.ID, scope, id, h.owner.ID)
			if !ok {
				if scope == core.GrantPrincipal && a.SideEffect == core.SEIrreversible {
					continue
				}
				t.Errorf("%s %s: no narrow grant", a.Tool, scope)
				continue
			}
			switch g.Kind {
			case core.GrantTool:
				trivial := true
				for _, glob := range g.Pattern {
					trivial = trivial && trivialGlob(glob)
				}
				if g.Tool != a.Tool || len(g.Pattern) == 0 || trivial {
					t.Errorf("%s: whole-tool grant %+v", a.Tool, g)
				}
			case core.GrantDomain, core.GrantRecipient:
				if g.Value == "" || hasGlobMeta(g.Value) {
					t.Errorf("%s: unbounded %s grant %+v", a.Tool, g.Kind, g)
				}
			}
			if g.MaxSE != a.SideEffect {
				t.Errorf("%s: max_se %s", a.Tool, g.MaxSE)
			}
			if _, err := h.engine.Grant(h.ctx, g); err != nil {
				t.Errorf("%s %s: narrow grant rejected: %v", a.Tool, scope, err)
			}
		}
	}
	for _, a := range []*core.Action{
		nil,
		action("pay", core.ActGeneric, core.SEFinancial, map[string]any{"x": 1}),
		action("schedule_create", core.ActSchedule, core.SEAdmin, map[string]any{"x": 1}),
		action("noargs", core.ActGeneric, core.SERead, map[string]any{}),
		action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"content": "no path"}),
	} {
		if g, ok := h.engine.NarrowGrant(a, h.ownerP.ID, core.GrantSession, string(h.session), h.owner.ID); ok {
			t.Errorf("unexpected narrow grant %+v", g)
		}
	}
	a := shellAction("ls")
	for _, c := range []struct {
		scope core.GrantScope
		id    string
	}{{core.GrantSession, ""}, {core.GrantPrincipal, "x"}, {"forever", "x"}} {
		if _, ok := h.engine.NarrowGrant(a, h.ownerP.ID, c.scope, c.id, h.owner.ID); ok {
			t.Errorf("scope %q id %q accepted", c.scope, c.id)
		}
	}
}

func TestNarrowGrantExec(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("shell", core.SELocalWrite)
	g := narrowOf(t, h, shellAction("ls -la"), core.GrantSession, string(h.session))
	if !matches(g, shellAction("ls -la"), s) {
		t.Error("exact argv not matched")
	}
	for _, cmd := range []string{"ls -la; rm -rf /", "ls -l", "ls -la ", "rm -rf /"} {
		if matches(g, shellAction(cmd), s) {
			t.Errorf("bash -lc grant matched %q", cmd)
		}
	}
	longer := action("shell", core.ActExec, core.SELocalWrite, map[string]any{"argv": []string{"bash", "-lc", "ls -la", "x"}, "cwd": ""})
	if matches(g, longer, s) {
		t.Error("bash grant matched a longer argv")
	}

	gitSpec := spec("git", core.SELocalWrite)
	git := func(args ...string) *core.Action {
		return action("git", core.ActExec, core.SELocalWrite, map[string]any{"argv": append([]string{"git"}, args...)})
	}
	gg := narrowOf(t, h, git("status"), core.GrantSession, string(h.session))
	if !matches(gg, git("log"), gitSpec) {
		t.Error("program grant should cover the program")
	}
	other := action("git", core.ActExec, core.SELocalWrite, map[string]any{"argv": []string{"gitx", "status"}})
	if matches(gg, other, gitSpec) {
		t.Error("program grant matched another program")
	}
	envAct := action("x", core.ActExec, core.SELocalWrite, map[string]any{"argv": []string{"/usr/bin/env", "python3", "-c", "1"}})
	eg := narrowOf(t, h, envAct, core.GrantSession, string(h.session))
	if _, ok := eg.Pattern["/argv"]; !ok {
		t.Errorf("env is an interpreter: %+v", eg.Pattern)
	}
	glob := action("x", core.ActExec, core.SELocalWrite, map[string]any{"argv": []string{"rm*", "-rf"}})
	rg := narrowOf(t, h, glob, core.GrantSession, string(h.session))
	if matches(rg, action("x", core.ActExec, core.SELocalWrite, map[string]any{"argv": []string{"rmdir", "-rf"}}), spec("x", core.SELocalWrite)) {
		t.Error("glob characters in argv must be matched literally")
	}
}

func TestNarrowGrantPaths(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("fs_write", core.SELocalWrite)
	w := func(p string) *core.Action {
		return action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"path": p, "content": "x", "mode": "overwrite"})
	}
	g := narrowOf(t, h, w("notes/a.txt"), core.GrantSession, string(h.session))
	for p, want := range map[string]bool{"notes/a.txt": true, "notes/b.md": true, "notes/sub/c.txt": true, "other/a.txt": false, "notesX/a.txt": false, "a.txt": false} {
		if got := matches(g, w(p), s); got != want {
			t.Errorf("notes grant vs %s: %v", p, got)
		}
	}
	root := narrowOf(t, h, w("a.txt"), core.GrantSession, string(h.session))
	if !matches(root, w("a.txt"), s) || matches(root, w("b.txt"), s) || matches(root, w("x/a.txt"), s) {
		t.Errorf("root-level grant is not exact: %+v", root.Pattern)
	}
}

func TestNarrowGrantDestinations(t *testing.T) {
	h := newHarness(t, nil)
	g := narrowOf(t, h, httpAction("web_fetch", "GET", "https://Example.com/x", core.SERead), core.GrantSession, string(h.session))
	if g.Kind != core.GrantDomain || g.Value != "example.com:443" {
		t.Errorf("http grant %+v", g)
	}
	fetch := spec("web_fetch", core.SERead)
	if !matches(g, httpAction("web_fetch", "GET", "https://example.com/other", core.SERead), fetch) {
		t.Error("domain grant must cover the host")
	}
	if matches(g, httpAction("web_fetch", "GET", "http://example.com/other", core.SERead), fetch) {
		t.Error("domain grant matched another port")
	}
	if matches(g, httpAction("web_fetch", "POST", "https://example.com/x", core.SEExternalSend), spec("web_fetch", core.SEExternalSend)) {
		t.Error("a read grant covered an external send")
	}
	if _, ok := h.engine.NarrowGrant(httpAction("web_fetch", "GET", "https://user.github.io/x", core.SERead), h.ownerP.ID, core.GrantPrincipal, "", h.owner.ID); ok {
		t.Error("allow_always for a shared-tenant host")
	}
	if _, ok := h.engine.NarrowGrant(netAction("files.pages.dev", 443), h.ownerP.ID, core.GrantSession, string(h.session), h.owner.ID); !ok {
		t.Error("session grant for a shared-tenant host should exist")
	}
	idn := narrowOf(t, h, httpAction("web_fetch", "GET", "https://ex\u0430mple.com/", core.SERead), core.GrantSession, string(h.session))
	if !strings.HasPrefix(idn.Value, "xn--") {
		t.Errorf("IDN host not punycoded: %s", idn.Value)
	}

	send := narrowOf(t, h, sendAction("send_mail", "bob@example.com", core.SEExternalSend), core.GrantSession, string(h.session))
	if send.Kind != core.GrantRecipient || send.Value != "bob@example.com" {
		t.Errorf("send grant %+v", send)
	}

	mcpSpec := spec("mcp__mail__send", core.SEExternalSend)
	mail := func(to, body string) *core.Action {
		return action("mcp__mail__send", core.ActMCP, core.SEExternalSend, map[string]any{"to": to, "body": body}, func(a *core.Action) { a.Recipient = to })
	}
	mg := narrowOf(t, h, mail("bob@example.com", "hi"), core.GrantSession, string(h.session))
	if mg.Kind != core.GrantTool || mg.Pattern["/to"] != "bob@example.com" || len(mg.Pattern) != 1 {
		t.Errorf("mcp recipient grant %+v", mg)
	}
	if !matches(mg, mail("bob@example.com", "other body"), mcpSpec) || matches(mg, mail("eve@example.com", "hi"), mcpSpec) {
		t.Error("mcp recipient grant mismatch")
	}
	search := func(q string) *core.Action {
		return action("mcp__docs__search", core.ActMCP, core.SERead, map[string]any{"q": q})
	}
	sg := narrowOf(t, h, search("go"), core.GrantSession, string(h.session))
	if !matches(sg, search("go"), spec("mcp__docs__search", core.SERead)) || matches(sg, search("rust"), spec("mcp__docs__search", core.SERead)) {
		t.Errorf("exact payload grant %+v", sg.Pattern)
	}
}

func TestGrantValidation(t *testing.T) {
	h := newHarness(t, nil)
	base := core.Grant{Principal: h.ownerP.ID, Scope: core.GrantSession, ScopeID: string(h.session), CreatedBy: h.owner.ID, MaxSE: core.SELocalWrite}
	with := func(f func(*core.Grant)) core.Grant {
		g := base
		f(&g)
		return g
	}
	bad := map[string]core.Grant{
		"whole tool": with(func(g *core.Grant) { g.Kind, g.Tool = core.GrantTool, "shell" }),
		"match-all pattern": with(func(g *core.Grant) {
			g.Kind, g.Tool, g.Pattern = core.GrantTool, "shell", map[string]string{"/argv/0": "*", "": "**"}
		}),
		"bad pointer": with(func(g *core.Grant) {
			g.Kind, g.Tool, g.Pattern = core.GrantTool, "shell", map[string]string{"argv": "ls"}
		}),
		"bad glob": with(func(g *core.Grant) {
			g.Kind, g.Tool, g.Pattern = core.GrantTool, "shell", map[string]string{"/argv/0": "[ls"}
		}),
		"admin class":        with(func(g *core.Grant) { g.Kind, g.Value, g.MaxSE = core.GrantRecipient, "x", core.SEAdmin }),
		"financial class":    with(func(g *core.Grant) { g.Kind, g.Value, g.MaxSE = core.GrantRecipient, "x", core.SEFinancial }),
		"no principal":       with(func(g *core.Grant) { g.Kind, g.Value, g.Principal = core.GrantRecipient, "x", "" }),
		"no creator":         with(func(g *core.Grant) { g.Kind, g.Value, g.CreatedBy = core.GrantRecipient, "x", "" }),
		"session without id": with(func(g *core.Grant) { g.Kind, g.Value, g.ScopeID = core.GrantRecipient, "x", "" }),
		"principal with id":  with(func(g *core.Grant) { g.Kind, g.Value, g.Scope = core.GrantRecipient, "x", core.GrantPrincipal }),
		"no port":            with(func(g *core.Grant) { g.Kind, g.Value = core.GrantDomain, "example.com" }),
		"session wildcard":   with(func(g *core.Grant) { g.Kind, g.Value = core.GrantDomain, "*.example.com:443" }),
		"member wildcard": with(func(g *core.Grant) {
			g.Kind, g.Value, g.Scope, g.ScopeID, g.CreatedBy = core.GrantDomain, "*.example.com:443", core.GrantPrincipal, "", h.member.ID
		}),
		"member shared tenant": with(func(g *core.Grant) {
			g.Kind, g.Value, g.Scope, g.ScopeID, g.CreatedBy = core.GrantDomain, "x.github.io:443", core.GrantPrincipal, "", h.member.ID
		}),
		"tainted tool grant": with(func(g *core.Grant) {
			g.Kind, g.Tool, g.Pattern, g.AllowWhenTainted = core.GrantTool, "send", map[string]string{"/to": "x"}, true
		}),
		"tainted glob recipient": with(func(g *core.Grant) { g.Kind, g.Value, g.AllowWhenTainted = core.GrantRecipient, "*@example.com", true }),
		"unknown kind":           with(func(g *core.Grant) { g.Kind, g.Value = "everything", "x" }),
	}
	for name, g := range bad {
		if _, err := h.engine.Grant(h.ctx, g); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	good := map[string]core.Grant{
		"admin wildcard": with(func(g *core.Grant) {
			g.Kind, g.Value, g.Scope, g.ScopeID = core.GrantDomain, "*.example.com:443", core.GrantPrincipal, ""
		}),
		"admin shared tenant": with(func(g *core.Grant) {
			g.Kind, g.Value, g.Scope, g.ScopeID = core.GrantDomain, "x.github.io:443", core.GrantPrincipal, ""
		}),
		"session shared tenant": with(func(g *core.Grant) { g.Kind, g.Value = core.GrantDomain, "x.github.io:443" }),
		"tainted recipient":     with(func(g *core.Grant) { g.Kind, g.Value, g.AllowWhenTainted = core.GrantRecipient, "telegram:42:", true }),
	}
	for name, g := range good {
		if _, err := h.engine.Grant(h.ctx, g); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestGrantMatchingAndScopes(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("send_mail", core.SEExternalSend)
	a := sendAction("send_mail", "bob@example.com", core.SEExternalSend)
	if d := h.decide(h.in(a, s)); d.Verdict != core.VerdictAsk {
		t.Fatalf("baseline: %+v", d)
	}
	low := h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantSession, ScopeID: string(h.session), Kind: core.GrantRecipient, Value: "bob@example.com", MaxSE: core.SERead})
	if d := h.decide(h.in(a, s)); d.Verdict != core.VerdictAsk {
		t.Errorf("max_se below the class: %+v", d)
	}
	if err := h.engine.Revoke(h.ctx, core.Viewer{Principals: []core.PrincipalID{h.ownerP.ID}}, low, h.owner.ID); err != nil {
		t.Fatal(err)
	}
	id := h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantSession, ScopeID: string(h.session), Kind: core.GrantRecipient, Value: "bob@example.com", MaxSE: core.SEExternalSend})
	if d := h.decide(h.in(a, s)); d.Verdict != core.VerdictAuto || d.Rule != "grant:"+string(id) {
		t.Errorf("session grant: %+v", d)
	}
	in := h.in(a, s)
	in.Session = h.session2
	if d := h.decide(in); d.Verdict != core.VerdictAsk {
		t.Errorf("session grant leaked into another session: %+v", d)
	}
	gs, err := h.store.Grants().List(h.ctx, core.SystemViewer(), core.GrantSession, string(h.session))
	if err != nil || len(gs) != 1 || gs[0].Uses != 1 {
		t.Errorf("uses not recorded: %+v %v", gs, err)
	}

	sched := &core.Schedule{Principal: h.ownerP.ID, Name: "s", Kind: core.SchedCron, Spec: "* * * * *", TZ: "UTC", Prompt: "p", Slot: core.SlotMain, MissedRun: "skip", Enabled: true}
	if err := h.store.Schedules().Upsert(h.ctx, sched); err != nil {
		t.Fatal(err)
	}
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantSchedule, ScopeID: string(sched.ID), Kind: core.GrantRecipient, Value: "carol@example.com", MaxSE: core.SEExternalSend})
	carol := sendAction("send_mail", "carol@example.com", core.SEExternalSend)
	in = h.in(carol, s)
	in.Session, in.Trigger, in.Schedule = h.session2, core.TrigSchedule, sched.ID
	if d := h.decide(in); d.Verdict != core.VerdictAuto {
		t.Errorf("schedule grant: %+v", d)
	}
	in.Schedule = ""
	if d := h.decide(in); d.Verdict != core.VerdictAsk {
		t.Errorf("schedule grant without the schedule: %+v", d)
	}

	expired := core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "dave@example.com", MaxSE: core.SEExternalSend, ExpiresAt: time.Now().Add(-time.Minute)}
	h.grant(expired)
	if d := h.decide(h.in(sendAction("send_mail", "dave@example.com", core.SEExternalSend), s)); d.Verdict != core.VerdictAsk {
		t.Errorf("expired grant: %+v", d)
	}

	once := h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "erin@example.com", MaxSE: core.SEExternalSend, MaxUses: 1})
	erin := sendAction("send_mail", "erin@example.com", core.SEExternalSend)
	if d := h.decide(h.in(erin, s)); d.Rule != "grant:"+string(once) {
		t.Errorf("max_uses grant: %+v", d)
	}
	if d := h.decide(h.in(erin, s)); d.Verdict != core.VerdictAsk {
		t.Errorf("spent grant: %+v", d)
	}
}

func TestAllowAlwaysAcrossSessionsAndRevoke(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("send_mail", core.SEExternalSend)
	a := sendAction("send_mail", "bob@example.com", core.SEExternalSend)
	d := h.decide(h.in(a, s))
	if !hasChoice(d, core.AllowAlways) {
		t.Fatalf("allow_always not offered: %v", d.Choices)
	}
	id := h.grant(narrowOf(t, h, a, core.GrantPrincipal, ""))
	in := h.in(a, s)
	in.Session = h.newSession(h.ownerP.ID)
	if d := h.decide(in); d.Verdict != core.VerdictAuto || d.Rule != "grant:"+string(id) {
		t.Errorf("principal grant in a new session: %+v", d)
	}
	created := h.audit.All()
	last := created[len(created)-1]
	var detail map[string]any
	if err := json.Unmarshal(last.Detail, &detail); err != nil || last.Action != "grant.create" || detail["via"] != "card" || detail["scope"] != "principal" {
		t.Errorf("audit %+v %v", last, err)
	}
	other := core.Viewer{Principals: []core.PrincipalID{h.memberP.ID}}
	if err := h.engine.Revoke(h.ctx, other, id, h.member.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("foreign revoke: %v", err)
	}
	if err := h.engine.Revoke(h.ctx, core.Viewer{Principals: []core.PrincipalID{h.ownerP.ID}}, id, h.owner.ID); err != nil {
		t.Fatal(err)
	}
	if d := h.decide(in); d.Verdict != core.VerdictAsk {
		t.Errorf("revoked grant still applies: %+v", d)
	}
	if acts := h.audit.Actions(); acts[len(acts)-1] != "grant.revoke" {
		t.Errorf("revoke not audited: %v", acts)
	}
	// A grant made outside a card is audited as such.
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "zed@example.com", MaxSE: core.SEExternalSend})
	all := h.audit.All()
	if err := json.Unmarshal(all[len(all)-1].Detail, &detail); err != nil || detail["via"] != "api" {
		t.Errorf("api grant audit: %v %v", detail, err)
	}
}

func TestAllowAlwaysExclusions(t *testing.T) {
	h := newHarness(t, nil)
	send := spec("send_mail", core.SEExternalSend)
	a := sendAction("send_mail", "bob@example.com", core.SEExternalSend)
	for _, trig := range []core.Trigger{core.TrigSchedule, core.TrigHeartbeat, core.TrigAPI} {
		in := h.in(a, send)
		in.Trigger = trig
		d := h.decide(in)
		if hasChoice(d, core.AllowAlways) || !hasChoice(d, core.AllowSession) {
			t.Errorf("%s: %v", trig, d.Choices)
		}
	}
	in := h.in(a, send)
	in.Taint = core.TaintPrivate | core.TaintUntrusted
	if d := h.decide(in); hasChoice(d, core.AllowAlways) || hasChoice(d, core.AllowSession) {
		t.Errorf("exposed: %v", d.Choices)
	}
	del := action("fs_delete", core.ActFSDelete, core.SEIrreversible, map[string]any{"path": "a/b.txt"}, func(a *core.Action) { a.Paths = []string{"a/b.txt"} })
	if d := h.decide(h.in(del, spec("fs_delete", core.SEIrreversible))); hasChoice(d, core.AllowAlways) || !hasChoice(d, core.AllowSession) {
		t.Errorf("irreversible: %v", d.Choices)
	}
	adm := action("schedule_create", core.ActSchedule, core.SEAdmin, map[string]any{"x": 1})
	if d := h.decide(h.in(adm, spec("schedule_create", core.SEAdmin))); hasChoice(d, core.AllowAlways) || hasChoice(d, core.AllowSession) {
		t.Errorf("admin: %v", d.Choices)
	}
	post := spec("http_post", core.SEExternalSend)
	if d := h.decide(h.in(httpAction("http_post", "POST", "https://x.workers.dev/", core.SEExternalSend), post)); hasChoice(d, core.AllowAlways) || !hasChoice(d, core.AllowSession) {
		t.Errorf("shared tenant: %v", d.Choices)
	}
}

func TestAllowWhenTaintedException(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("send_mail", core.SEExternalSend)
	exposed := core.TaintPrivate | core.TaintUntrusted
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "telegram:42:", MaxSE: core.SEExternalSend, AllowWhenTainted: true})
	h.grant(core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "bob@example.com", MaxSE: core.SEExternalSend})
	// A glob value cannot be created through Grant; one planted directly in
	// the store must still never lift the floor.
	planted := &core.Grant{Principal: h.ownerP.ID, Scope: core.GrantPrincipal, Kind: core.GrantRecipient, Value: "*", MaxSE: core.SEExternalSend, AllowWhenTainted: true, CreatedBy: h.owner.ID, CreatedAt: time.Now()}
	if err := h.store.Grants().Add(h.ctx, planted); err != nil {
		t.Fatal(err)
	}
	for recipient, want := range map[string]core.Verdict{
		"telegram:42:":    core.VerdictAuto,
		"telegram:42:7":   core.VerdictAsk,
		"telegram:4":      core.VerdictAsk,
		"bob@example.com": core.VerdictAsk,
		"*":               core.VerdictAsk,
	} {
		in := h.in(sendAction("send_mail", recipient, core.SEExternalSend), s)
		in.Taint = exposed
		d := h.decide(in)
		if d.Verdict != want {
			t.Errorf("%q: %+v", recipient, d)
		}
		if want == core.VerdictAsk && d.Rule != "floor:trifecta" {
			t.Errorf("%q: rule %s", recipient, d.Rule)
		}
	}
	// The exception lifts the trifecta floor only: an untrusted origin still asks.
	in := h.in(sendAction("send_mail", "telegram:42:", core.SEExternalSend), s)
	in.Taint, in.Provenance = exposed, core.ProvUntrusted
	if d := h.decide(in); d.Verdict != core.VerdictAsk || d.Rule != "floor:untrusted_origin" {
		t.Errorf("untrusted origin: %+v", d)
	}
}

func TestNextTaint(t *testing.T) {
	h := newHarness(t, nil)
	e := h.engine
	read := spec("fs_read", core.SERead, func(m *core.Manifest) { m.Taint = core.TaintPrivate })
	fetch := spec("web_fetch", core.SERead, func(m *core.Manifest) { m.Taint = core.TaintUntrusted })
	shell := spec("shell", core.SELocalWrite)
	if got := e.NextTaint(0, read, nil, &core.ToolResult{}, false); got != core.TaintPrivate {
		t.Errorf("fs_read: %d", got)
	}
	if got := e.NextTaint(core.TaintPrivate, fetch, nil, &core.ToolResult{}, false); !got.Exposed() {
		t.Errorf("fetch after read: %d", got)
	}
	if got := e.NextTaint(0, shell, shellAction("x"), &core.ToolResult{}, true); got != core.TaintUntrusted {
		t.Errorf("box egress: %d", got)
	}
	if got := e.NextTaint(0, shell, shellAction("x"), &core.ToolResult{Untrusted: true}, false); got != core.TaintUntrusted {
		t.Errorf("untrusted result: %d", got)
	}
	if got := e.NextTaint(core.TaintPrivate|core.TaintUntrusted, shell, shellAction("x"), nil, false); got != core.TaintPrivate|core.TaintUntrusted {
		t.Errorf("taint must never shrink: %d", got)
	}
	send := spec("send_mail", core.SEExternalSend)
	if got := e.NextTaint(0, send, sendAction("send_mail", "x", core.SEExternalSend), &core.ToolResult{}, false); !got.Has(core.TaintSend) {
		t.Errorf("send leg: %d", got)
	}
	if got := e.NextTaint(0, spec("x", core.SERead, func(m *core.Manifest) { m.Taint = 0xF0 }), nil, nil, false); got != 0 {
		t.Errorf("unknown bits leaked: %d", got)
	}
}
