package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func (h *harness) env(t *testing.T) *core.ToolEnv {
	t.Helper()
	return &core.ToolEnv{
		User: h.user, Role: core.RoleOwner,
		Principal: core.Principal{ID: h.p, Kind: core.PrincipalUser, User: h.user, ToolProfile: "standard"},
		Session:   "ses_1", Run: "run_7", CallID: core.NewID(core.PrefixToolCall),
		Trigger: core.TrigMessage, Workspace: t.TempDir(), Interactive: true,
	}
}

func tool(t *testing.T, h *harness, name string) core.Tool {
	t.Helper()
	for _, tl := range h.svc.Tools() {
		if tl.Spec().Def.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %q", name)
	return nil
}

func TestMemoryToolConformance(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Memory.MemoryMaxBytes = 64 })
	h.apply(t, core.FileMemory, "add", "", "Lives in Leeds")
	env := h.env(t)
	conformance.Tool(t, tool(t, h, "memory"), []conformance.ToolCase{
		{Name: "add", Env: env, Args: json.RawMessage(`{"file":"USER","op":"add","new":"Prefers tea"}`)},
		{Name: "add on fresh principal", Args: json.RawMessage(`{"file":"MEMORY","op":"add","new":"x"}`)},
		{Name: "replace", Env: env, Args: json.RawMessage(`{"file":"MEMORY","op":"replace","old":"Lives in Leeds","new":"Lives in York"}`)},
		{Name: "remove", Env: env, Args: json.RawMessage(`{"op":"remove","file":"MEMORY","old":" Lives in Leeds "}`)},
		{Name: "missing entry", Env: env, Args: json.RawMessage(`{"file":"MEMORY","op":"remove","old":"Lives in Paris"}`), WantErr: true},
		{Name: "over cap", Env: env, Args: json.RawMessage(`{"file":"MEMORY","op":"add","new":"` + strings.Repeat("z", 60) + `"}`), WantErr: true},
		{Name: "add without new", Env: env, Args: json.RawMessage(`{"file":"MEMORY","op":"add"}`), WantErr: true},
		{Name: "unknown field", Env: env, Args: json.RawMessage(`{"file":"MEMORY","op":"add","new":"x","path":"/etc"}`), WantErr: true},
		{Name: "bad file", Env: env, Args: json.RawMessage(`{"file":"SOUL","op":"add","new":"x"}`), WantErr: true},
	})
}

func TestSessionSearchToolConformance(t *testing.T) {
	h := newHarness(t)
	conformance.Tool(t, tool(t, h, "session_search"), []conformance.ToolCase{
		{Name: "query", Args: json.RawMessage(`{"query":"invoice  overdue"}`)},
		{Name: "limit", Args: json.RawMessage(`{"query":"invoice","limit":3}`)},
		{Name: "empty", Args: json.RawMessage(`{"query":"   "}`), WantErr: true},
		{Name: "limit too high", Args: json.RawMessage(`{"query":"x","limit":99}`), WantErr: true},
		{Name: "not an object", Args: json.RawMessage(`["x"]`), WantErr: true},
	})
}

func TestToolSpecs(t *testing.T) {
	h := newHarness(t)
	mem, search := tool(t, h, "memory").Spec(), tool(t, h, "session_search").Spec()
	if mem.Manifest.SideEffect != core.SELocalWrite || mem.Manifest.SpaceOK || !mem.Manifest.Core || mem.Manifest.Taint != 0 {
		t.Errorf("memory manifest = %+v", mem.Manifest)
	}
	if search.Manifest.SideEffect != core.SERead || search.Manifest.SpaceOK || search.Manifest.Taint != core.TaintPrivate {
		t.Errorf("session_search manifest = %+v", search.Manifest)
	}
	for _, s := range []core.ToolSpec{mem, search} {
		if s.Source != "builtin" || len(s.Hash) != 64 {
			t.Errorf("%s: source %q hash %q", s.Def.Name, s.Source, s.Hash)
		}
	}
	env := h.env(t)
	space := *env
	space.Principal = core.Principal{ID: core.PrincipalID(core.NewID(core.PrefixSpace)), Kind: core.PrincipalSpace, ToolProfile: "standard"}
	for _, tl := range h.svc.Tools() {
		g, ok := tl.(core.Gated)
		if !ok || !g.Enabled(env) || g.Enabled(&space) {
			t.Errorf("%s: must be enabled for users and disabled for every space", tl.Spec().Def.Name)
		}
	}
}

func planExec(t *testing.T, tl core.Tool, env *core.ToolEnv, args string) (*core.Action, *core.ToolResult) {
	t.Helper()
	a, err := tl.Plan(context.Background(), env, json.RawMessage(args))
	if err != nil {
		t.Fatalf("Plan(%s): %v", args, err)
	}
	res, err := tl.Exec(context.Background(), env, a)
	if err != nil {
		t.Fatalf("Exec(%s): %v", args, err)
	}
	return a, res
}

func TestMemoryToolExec(t *testing.T) {
	h := newHarness(t)
	tl := tool(t, h, "memory")
	env := h.env(t)
	a, res := planExec(t, tl, env, `{"file":"MEMORY","op":"add","new":"Lives in Leeds"}`)
	if a.Kind != core.ActMemory || a.Diff != "--- MEMORY.md\n+++ MEMORY.md\n+Lives in Leeds\n" || string(a.Payload) != `{"file":"MEMORY","new":"Lives in Leeds","op":"add"}` {
		t.Fatalf("action = %+v (payload %s)", a, a.Payload)
	}
	if res.IsError || res.Content != "Saved to MEMORY.md (15/256 bytes used)." {
		t.Fatalf("result = %+v", res)
	}

	// The file filled up between Plan and Exec: Exec reports it to the model.
	stale, err := tl.Plan(context.Background(), env, json.RawMessage(`{"file":"MEMORY","op":"add","new":"`+strings.Repeat("b", 200)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	h.apply(t, core.FileMemory, "add", "", strings.Repeat("a", 100))
	res, err = tl.Exec(context.Background(), env, stale)
	if err != nil || !res.IsError || !strings.Contains(res.Content, "bytes are free now") {
		t.Fatalf("Exec over cap = %+v, %v", res, err)
	}

	tainted := *env
	tainted.Taint = core.TaintPrivate | core.TaintUntrusted
	planExec(t, tl, &tainted, `{"file":"USER","op":"add","new":"from a web page"}`)
	evs, _ := h.store.MemoryEvents().Since(context.Background(), h.p, 0)
	if last := evs[len(evs)-1]; last.Provenance != core.ProvUntrusted || last.Run != "run_7" || last.Actor != "agent:run_7" {
		t.Fatalf("tainted write event = %+v", last)
	}
	if evs[0].Provenance != core.ProvUser {
		t.Fatalf("clean write provenance = %q", evs[0].Provenance)
	}
}

func TestSessionSearchToolExec(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	add := func(p core.PrincipalID, title, text string, prov core.Provenance) {
		s := &core.Session{Principal: p, Origin: "web", Title: title, CreatedAt: time.Now()}
		if err := h.store.Sessions().Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		m := &core.Message{Session: s.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: prov,
			Parts: []core.Part{{Type: core.PartText, Text: text}}, CreatedAt: time.Now()}
		if err := h.store.Messages().Append(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	add(h.p, "Q3 report", "the invoice is overdue", core.ProvUser)
	tl := tool(t, h, "session_search")
	env := h.env(t)
	_, res := planExec(t, tl, env, `{"query":"invoice"}`)
	if res.Untrusted || !strings.Contains(res.Content, "1 matches") || !strings.Contains(res.Content, "[Q3 report · ") {
		t.Fatalf("trusted result = %+v", res)
	}
	add(h.p, "", "forwarded invoice from a stranger", core.ProvUntrusted)
	_, res = planExec(t, tl, env, `{"query":"invoice","limit":5}`)
	if !res.Untrusted || !strings.Contains(res.Content, "· untrusted]") || !strings.Contains(res.Content, "[Untitled · ") {
		t.Fatalf("untrusted hit must mark the result: %+v", res)
	}
	_, res = planExec(t, tl, env, `{"query":"nothing like this"}`)
	if res.Content != `No past messages match "nothing like this".` {
		t.Fatalf("no hits = %+v", res)
	}

	// A space the user owns is not searched from the user's own runs.
	sp := &core.Space{Kind: "group", Chat: core.ChatRef{Channel: "telegram", ChatID: "-1", Kind: core.ChatGroup}, Owner: h.user, CreatedAt: time.Now()}
	spp := &core.Principal{Kind: core.PrincipalSpace, User: h.user, ToolProfile: "restricted", CreatedAt: time.Now()}
	if err := h.store.Spaces().Create(ctx, sp, spp); err != nil {
		t.Fatal(err)
	}
	add(sp.Principal, "Group", "group invoice chatter", core.ProvUntrusted)
	_, res = planExec(t, tl, env, `{"query":"chatter"}`)
	if !strings.HasPrefix(res.Content, "No past messages") {
		t.Fatalf("space content leaked into a user run: %+v", res)
	}
}
