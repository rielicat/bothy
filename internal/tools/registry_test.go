package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

// stub stands in for tools other modules own, with schemas of realistic size.
type stub struct {
	spec  core.ToolSpec
	gated func(*core.ToolEnv) bool
}

func (s stub) Spec() core.ToolSpec { return s.spec }

func (s stub) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	return &core.Action{Tool: s.spec.Def.Name, Kind: core.ActGeneric, SideEffect: 0, Payload: args}, nil
}

func (s stub) Exec(context.Context, *core.ToolEnv, *core.Action) (*core.ToolResult, error) {
	return &core.ToolResult{Content: "ok"}, nil
}

type gatedStub struct{ stub }

func (g gatedStub) Enabled(env *core.ToolEnv) bool { return g.gated(env) }

func newStub(name string, se core.SideEffect, desc, props string, mods ...func(*core.Manifest)) core.Tool {
	m := core.Manifest{Title: "Use " + name, SideEffect: se}
	for _, f := range mods {
		f(&m)
	}
	return stub{spec: core.ToolSpec{
		Def:    core.ToolDef{Name: name, Description: desc, Schema: json.RawMessage(`{"type":"object","properties":{` + props + `}}`)},
		Source: "builtin", Manifest: m,
	}}
}

func coreTool(m *core.Manifest) { m.Core = true }
func spaceOK(m *core.Manifest)  { m.SpaceOK = true }
func execTool(m *core.Manifest) { m.Exec = true }
func messageOnly(m *core.Manifest) {
	m.Triggers = []core.Trigger{core.TrigMessage}
}

// othersTools mirrors the standard core set of SPEC §5.6 owned by other
// modules, plus lazy ones, so profiles can be checked end to end.
func othersTools() []core.Tool {
	skill := newStub("skill_load", core.SERead, "Load an enabled skill's instructions by name.", `"name":{"type":"string"}`, coreTool, spaceOK)
	return []core.Tool{
		newStub("shell", core.SELocalWrite, "Run a bash command in your sandbox. Network goes through an allowlisting proxy.", `"command":{"type":"string"},"cwd":{"type":"string"},"timeout_s":{"type":"integer"}`, coreTool, execTool),
		newStub("python", core.SELocalWrite, "Run Python 3 code in your sandbox.", `"code":{"type":"string"},"cwd":{"type":"string"}`, coreTool, execTool),
		newStub("web_fetch", core.SERead, "Fetch a public web page as text.", `"url":{"type":"string"}`, coreTool, spaceOK),
		newStub("web_search", core.SERead, "Search the web.", `"query":{"type":"string"},"count":{"type":"integer"}`, coreTool, spaceOK),
		newStub("memory", core.SELocalWrite, "Add, replace or remove an entry in MEMORY.md or USER.md.", `"file":{"type":"string","enum":["MEMORY","USER"]},"op":{"type":"string","enum":["add","replace","remove"]},"old":{"type":"string"},"new":{"type":"string"}`, coreTool),
		newStub("session_search", core.SERead, "Search earlier conversations.", `"query":{"type":"string"},"limit":{"type":"integer"}`, coreTool),
		gatedStub{stub: stub{spec: skill.Spec(), gated: func(env *core.ToolEnv) bool { return env.Principal.ID != "no-skills" }}},
		newStub("skill_read", core.SERead, "Read a file of a skill.", `"name":{"type":"string"},"path":{"type":"string"}`, spaceOK),
		newStub("net_allow", core.SEExternalSend, "Ask the user to let the sandbox reach a host.", `"host":{"type":"string"},"port":{"type":"integer"}`),
		newStub("schedule_list", core.SERead, "List schedules.", ``, messageOnly),
		newStub("schedule_create", core.SEAdmin, "Create a cron schedule.", `"cron":{"type":"string"},"prompt":{"type":"string"}`, messageOnly),
		newStub("schedule_delete", core.SEAdmin, "Delete a schedule.", `"id":{"type":"string"}`, messageOnly),
	}
}

func fullRegistry(t *testing.T, order func([]core.Tool)) *Registry {
	t.Helper()
	all := append(Builtins(BuiltinDeps{}), othersTools()...)
	if order != nil {
		order(all)
	}
	r := NewRegistry(RegistryDeps{Config: config.Static(config.Default())})
	for _, tool := range all {
		if err := r.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Spec().Def.Name, err)
		}
	}
	return r
}

func ownerEnv() *core.ToolEnv {
	u := core.UserID(core.NewID(core.PrefixUser))
	return &core.ToolEnv{
		User: u, Role: core.RoleOwner, Trigger: core.TrigMessage, Interactive: true,
		Principal: core.Principal{ID: core.UserPrincipal(u), Kind: core.PrincipalUser, User: u, ToolProfile: "standard"},
		Chat:      &core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM},
	}
}

func names(specs []core.ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Def.Name
	}
	return out
}

// estimateTokens is the facade's bytes/3.6 estimate (SPEC §5.6) applied to
// the tool definitions as they are sent.
func estimateTokens(specs []core.ToolSpec) int {
	n := 0
	for _, s := range specs {
		b, _ := json.Marshal(s.Def)
		n += len(b)
	}
	return int(float64(n) / 3.6)
}

func TestForRunProfiles(t *testing.T) {
	r := fullRegistry(t, nil)
	env := ownerEnv()
	sel := core.Selection{SandboxOK: true}

	std := r.ForRun(env, sel)
	want := []string{"fs_list", "fs_read", "fs_write", "memory", "message", "python", "session_search", "shell", "skill_load", "tool_search", "use_tool", "web_fetch", "web_search"}
	if got := names(std); !slices.Equal(got, want) {
		t.Errorf("standard:\n got %v\nwant %v", got, want)
	}
	if n := estimateTokens(std); n > 1500 {
		t.Errorf("standard core schemas are %d estimated tokens (> 1500)", n)
	}
	mine := 0
	for _, s := range std {
		if slices.Contains([]string{"fs_list", "fs_read", "fs_write", "message", "tool_search", "use_tool"}, s.Def.Name) {
			mine += estimateTokens([]core.ToolSpec{s})
		}
	}
	if mine > 700 {
		t.Errorf("M4's core schemas use %d of the 1,500-token budget", mine)
	}

	compact := r.ForRun(env, core.Selection{SandboxOK: true, SmallModel: true})
	want = []string{"fs_read", "fs_write", "memory", "shell", "tool_search", "use_tool", "web_fetch", "web_search"}
	if got := names(compact); !slices.Equal(got, want) || len(compact) > 8 {
		t.Errorf("compact: %v", got)
	}
	degraded := r.ForRun(env, core.Selection{SandboxOK: true, Degraded: true, SmallModel: true})
	want = []string{"fs_read", "fs_write", "memory", "shell", "web_search"}
	if got := names(degraded); !slices.Equal(got, want) {
		t.Errorf("degraded: %v", got)
	}
}

func TestForRunDeterministic(t *testing.T) {
	a := fullRegistry(t, nil)
	b := fullRegistry(t, func(ts []core.Tool) { slices.Reverse(ts) })
	env := ownerEnv()
	for _, sel := range []core.Selection{{SandboxOK: true}, {SmallModel: true}, {Degraded: true}, {Paused: true}} {
		ja, _ := json.Marshal(a.ForRun(env, sel))
		jb, _ := json.Marshal(b.ForRun(env, sel))
		ja2, _ := json.Marshal(a.ForRun(env, sel))
		if !bytes.Equal(ja, jb) || !bytes.Equal(ja, ja2) {
			t.Errorf("selection %+v: ForRun bytes differ", sel)
		}
	}
	// Specs handed out are copies: mutating one must not leak back.
	specs := a.ForRun(env, core.Selection{SandboxOK: true})
	specs[0].Manifest.FSPaths = append(specs[0].Manifest.FSPaths[:0], "/etc")
	specs[0].Def.Schema[0] = 'X'
	again := a.ForRun(env, core.Selection{SandboxOK: true})
	if again[0].Def.Schema[0] != '{' || slices.Contains(again[0].Manifest.FSPaths, "/etc") {
		t.Error("registry state was mutated through a returned spec")
	}
}

func TestForRunFilters(t *testing.T) {
	r := fullRegistry(t, nil)
	env := ownerEnv()

	noBox := names(r.ForRun(env, core.Selection{}))
	if slices.Contains(noBox, "shell") || slices.Contains(noBox, "python") {
		t.Errorf("exec tools without a sandbox: %v", noBox)
	}
	if r.Available("shell", env, core.Selection{}) || !r.Available("shell", env, core.Selection{SandboxOK: true}) {
		t.Error("Available ignores the sandbox")
	}

	sched := *env
	sched.Trigger = core.TrigSchedule
	for _, name := range []string{"schedule_list", "schedule_create", "schedule_delete"} {
		if r.Available(name, &sched, core.Selection{SandboxOK: true}) {
			t.Errorf("%s available to a schedule run", name)
		}
		if !r.Available(name, env, core.Selection{SandboxOK: true}) {
			t.Errorf("%s unavailable to a message run", name)
		}
	}
	if hits := names(r.Search("schedule", &sched, core.Selection{SandboxOK: true}, 0)); len(hits) != 0 {
		t.Errorf("schedule run searched up %v", hits)
	}

	space := *env
	space.Principal = core.Principal{ID: "spc_x", Kind: core.PrincipalSpace, User: env.User, ToolProfile: "restricted"}
	got := r.ForRun(&space, core.Selection{SandboxOK: true})
	for _, s := range got {
		if s.Manifest.SideEffect != core.SERead || !s.Manifest.SpaceOK {
			t.Errorf("restricted space offered %s (%s)", s.Def.Name, s.Manifest.SideEffect)
		}
	}
	if gotNames := names(got); !slices.Equal(gotNames, []string{"fs_list", "fs_read", "skill_load", "tool_search", "use_tool", "web_fetch", "web_search"}) {
		t.Errorf("restricted space: %v", gotNames)
	}
	for _, name := range []string{"fs_write", "memory", "session_search", "shell", "message", "schedule_create"} {
		if r.Available(name, &space, core.Selection{SandboxOK: true}) {
			t.Errorf("restricted space can call %s", name)
		}
	}
	standard := space
	standard.Principal.ToolProfile = "standard"
	if !r.Available("fs_write", &standard, core.Selection{SandboxOK: true}) {
		t.Error("standard space lacks fs_write")
	}

	paused := names(r.ForRun(env, core.Selection{SandboxOK: true, Paused: true}))
	for _, n := range paused {
		if spec := specByName(r, n); spec.Manifest.SideEffect != core.SERead {
			t.Errorf("paused run offered %s", n)
		}
	}

	webEnv := *env
	webEnv.Chat = &core.ChatRef{Channel: "web", ChatID: "x"}
	if slices.Contains(names(r.ForRun(&webEnv, core.Selection{SandboxOK: true})), "message") {
		t.Error("message offered without a channel chat")
	}
	noSkills := *env
	noSkills.Principal.ID = "no-skills"
	if slices.Contains(names(r.ForRun(&noSkills, core.Selection{SandboxOK: true})), "skill_load") {
		t.Error("Gated tool offered while disabled")
	}

	member := *env
	member.Role = core.RoleMember
	if err := r.Register(newStub("admin_only", core.SERead, "x", "", func(m *core.Manifest) { m.MinRole = core.RoleAdmin })); err != nil {
		t.Fatal(err)
	}
	if r.Available("admin_only", &member, core.Selection{}) || !r.Available("admin_only", env, core.Selection{}) {
		t.Error("MinRole not applied")
	}
}

func specByName(r *Registry, name string) core.ToolSpec {
	for _, s := range r.All() {
		if s.Def.Name == name {
			return s
		}
	}
	return core.ToolSpec{}
}

func TestMetaToolsOnlyWithLazyTools(t *testing.T) {
	r := NewRegistry(RegistryDeps{})
	if err := r.Register(fsRead{}); err != nil {
		t.Fatal(err)
	}
	if got := names(r.ForRun(ownerEnv(), core.Selection{})); !slices.Equal(got, []string{"fs_read"}) {
		t.Errorf("no lazy tools: %v", got)
	}
	if err := r.Register(fsDelete{}); err != nil {
		t.Fatal(err)
	}
	if got := names(r.ForRun(ownerEnv(), core.Selection{})); !slices.Equal(got, []string{"fs_read", "tool_search", "use_tool"}) {
		t.Errorf("with a lazy tool: %v", got)
	}
	if got := names(r.All()); !slices.Equal(got, []string{"fs_delete", "fs_read", "tool_search", "use_tool"}) {
		t.Errorf("All: %v", got)
	}
}

func TestRegisterValidation(t *testing.T) {
	r := NewRegistry(RegistryDeps{})
	ok := func() core.ToolSpec {
		return core.ToolSpec{
			Def:    core.ToolDef{Name: "x_tool", Description: "does x", Schema: json.RawMessage(`{"type":"object"}`)},
			Source: "mcp:docs", Manifest: core.Manifest{Title: "X", SideEffect: core.SERead},
		}
	}
	bad := map[string]func(*core.ToolSpec){
		"name":         func(s *core.ToolSpec) { s.Def.Name = "bad name!" },
		"long name":    func(s *core.ToolSpec) { s.Def.Name = strings.Repeat("a", 65) },
		"description":  func(s *core.ToolSpec) { s.Def.Description = " " },
		"source":       func(s *core.ToolSpec) { s.Source = "mcp:" },
		"title":        func(s *core.ToolSpec) { s.Manifest.Title = "" },
		"side effect":  func(s *core.ToolSpec) { s.Manifest.SideEffect = 0 },
		"trigger":      func(s *core.ToolSpec) { s.Manifest.Triggers = []core.Trigger{core.TrigResume} },
		"role":         func(s *core.ToolSpec) { s.Manifest.MinRole = "root" },
		"timeout":      func(s *core.ToolSpec) { s.Manifest.Timeout = -time.Second },
		"taint":        func(s *core.ToolSpec) { s.Manifest.Taint = 1 << 6 },
		"schema json":  func(s *core.ToolSpec) { s.Def.Schema = json.RawMessage(`{`) },
		"schema type":  func(s *core.ToolSpec) { s.Def.Schema = json.RawMessage(`{"type":"string"}`) },
		"schema array": func(s *core.ToolSpec) { s.Def.Schema = json.RawMessage(`[]`) },
		"schema bad": func(s *core.ToolSpec) {
			s.Def.Schema = json.RawMessage(`{"type":"object","properties":{"a":{"type":"nope"}}}`)
		},
		"remote ref": func(s *core.ToolSpec) {
			s.Def.Schema = json.RawMessage(`{"type":"object","$ref":"https://example.com/s.json"}`)
		},
		"version": func(s *core.ToolSpec) {
			s.Def.Schema = json.RawMessage(`{"$schema":"http://json-schema.org/draft-04/schema#","type":"object"}`)
		},
	}
	for name, mod := range bad {
		s := ok()
		mod(&s)
		if err := r.Register(stub{spec: s}); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := r.Register(nil); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("nil tool: %v", err)
	}
	s := ok()
	s.Def.Schema = json.RawMessage("{ \"type\" : \"object\" ,\n \"properties\": {} }")
	if err := r.Register(stub{spec: s}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(stub{spec: ok()}); !errors.Is(err, core.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	if err := r.Register(stub{spec: core.ToolSpec{Def: core.ToolDef{Name: "tool_search", Description: "x", Schema: json.RawMessage(`{"type":"object"}`)}, Source: "builtin", Manifest: core.Manifest{Title: "x", SideEffect: core.SERead}}}); !errors.Is(err, core.ErrConflict) {
		t.Errorf("shadowing tool_search: %v", err)
	}
	got := specByName(r, "x_tool")
	def, _ := core.CanonicalJSON(got.Def)
	if string(got.Def.Schema) != `{"properties":{},"type":"object"}` || got.Hash == "" || len(got.Hash) != 64 {
		t.Errorf("normalised spec %+v", got)
	}
	if !strings.HasPrefix(string(def), `{"description":"does x"`) {
		t.Errorf("canonical def %s", def)
	}
	r.UnregisterSource("mcp:docs")
	if _, ok := r.Get("x_tool"); ok {
		t.Error("UnregisterSource left the tool")
	}
	r.UnregisterSource("builtin")
	if _, ok := r.Get("tool_search"); !ok {
		t.Error("UnregisterSource removed tool_search")
	}
}

func TestReplaceSource(t *testing.T) {
	r := NewRegistry(RegistryDeps{})
	mcpTool := func(name string) core.Tool {
		st := newStub(name, core.SERead, "does "+name, "").(stub)
		st.spec.Source = "mcp:docs"
		return st
	}
	for _, tl := range []core.Tool{newStub("fs_read", core.SERead, "reads", ""), mcpTool("mcp__docs__old"), mcpTool("mcp__docs__kept")} {
		if err := r.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	bad := mcpTool("mcp__docs__bad").(stub)
	bad.spec.Def.Description = ""
	err := r.ReplaceSource("mcp:docs", []core.Tool{mcpTool("mcp__docs__kept"), mcpTool("mcp__docs__new"), bad,
		newStub("fs_read", core.SERead, "steals", ""), mcpTool("fs_read")})
	if !errors.Is(err, core.ErrInvalid) || !errors.Is(err, core.ErrConflict) {
		t.Fatalf("ReplaceSource error = %v, want invalid and conflict", err)
	}
	for name, want := range map[string]bool{"mcp__docs__old": false, "mcp__docs__kept": true, "mcp__docs__new": true, "mcp__docs__bad": false} {
		if _, ok := r.Get(name); ok != want {
			t.Errorf("Get(%s) = %v, want %v", name, ok, want)
		}
	}
	if tl, ok := r.Get("fs_read"); !ok || tl.Spec().Source != "builtin" {
		t.Error("ReplaceSource touched another source's tool")
	}
	if err := r.ReplaceSource("mcp:docs", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("mcp__docs__kept"); ok {
		t.Error("ReplaceSource with no tools left the source registered")
	}
}

func TestGetValidatesAndNormalises(t *testing.T) {
	r := NewRegistry(RegistryDeps{})
	lowball := stub{spec: core.ToolSpec{
		Def:    core.ToolDef{Name: "sender", Description: "sends", Schema: json.RawMessage(`{"type":"object","properties":{"to":{"type":"string"}},"required":["to"],"additionalProperties":false}`)},
		Source: "builtin", Manifest: core.Manifest{Title: "Send", SideEffect: core.SEExternalSend},
	}}
	if err := r.Register(lowball); err != nil {
		t.Fatal(err)
	}
	tool, ok := r.Get("sender")
	if !ok {
		t.Fatal("missing")
	}
	for name, args := range map[string]string{
		"missing field": `{}`,
		"wrong type":    `{"to":5}`,
		"extra field":   `{"to":"a","x":1}`,
		"not object":    `["a"]`,
		"not json":      `{`,
	} {
		if _, err := tool.Plan(context.Background(), ownerEnv(), json.RawMessage(args)); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	a, err := tool.Plan(context.Background(), ownerEnv(), json.RawMessage(`{ "to" : "a" }`))
	if err != nil {
		t.Fatal(err)
	}
	if a.SideEffect != core.SEExternalSend || string(a.Payload) != `{"to":"a"}` || a.Fingerprint != core.ActionFingerprint("sender", a.Payload) {
		t.Errorf("not normalised: %+v", a)
	}
	if _, err := tool.Exec(context.Background(), ownerEnv(), &core.Action{Tool: "other"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("exec of another tool's action: %v", err)
	}
	ut, _ := r.Get("use_tool")
	if _, err := ut.Plan(context.Background(), ownerEnv(), json.RawMessage(`{"name":"sender","args":{"to":"a"}}`)); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("use_tool planned: %v", err)
	}
	if _, err := ut.Exec(context.Background(), ownerEnv(), &core.Action{Tool: "use_tool"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("use_tool executed: %v", err)
	}
}

func TestSearch(t *testing.T) {
	r := fullRegistry(t, nil)
	env := ownerEnv()
	sel := core.Selection{SandboxOK: true}
	hits := names(r.Search("delete a file", env, sel, 8))
	if len(hits) == 0 || hits[0] != "fs_delete" {
		t.Errorf("delete a file: %v", hits)
	}
	if hits := names(r.Search("schedule", env, sel, 2)); len(hits) != 2 || !strings.HasPrefix(hits[0], "schedule_") {
		t.Errorf("limit: %v", hits)
	}
	if hits := names(r.Search("fs_list", env, sel, 0)); hits[0] != "fs_list" {
		t.Errorf("exact name: %v", hits)
	}
	if hits := names(r.Search("zzz-nothing", env, sel, 0)); len(hits) != 0 {
		t.Errorf("no match: %v", hits)
	}
	all := names(r.Search("", env, sel, 0))
	if slices.Contains(all, "tool_search") || slices.Contains(all, "use_tool") || !slices.Contains(all, "fs_delete") {
		t.Errorf("empty query: %v", all)
	}
	if slices.Contains(names(r.Search("python", env, core.Selection{}, 0)), "python") {
		t.Error("search returned an unavailable tool")
	}
}

func TestToolSearchTool(t *testing.T) {
	r := fullRegistry(t, nil)
	ts, _ := r.Get("tool_search")
	env := ownerEnv()
	env.Selection = core.Selection{SandboxOK: true}
	a, err := ts.Plan(context.Background(), env, json.RawMessage(`{"query":"network host"}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Exec(context.Background(), env, a)
	if err != nil {
		t.Fatal(err)
	}
	head, body, _ := strings.Cut(res.Content, "\n")
	var hits []searchHit
	if err := json.Unmarshal([]byte(body), &hits); err != nil || len(hits) == 0 || !strings.Contains(head, "use_tool") {
		t.Fatalf("result %q: %v", res.Content, err)
	}
	if hits[0].Name != "net_allow" || len(hits[0].Schema) == 0 {
		t.Errorf("hits %+v", hits)
	}
	none, err := ts.Exec(context.Background(), env, &core.Action{Tool: "tool_search", Payload: json.RawMessage(`{"query":"qqqq"}`)})
	if err != nil || !strings.HasPrefix(none.Content, "No tools matched") {
		t.Errorf("no hits: %v %v", none, err)
	}
	conformance.Tool(t, &toolSearch{reg: r}, []conformance.ToolCase{
		{Name: "query", Args: json.RawMessage(`{"query":"files"}`)},
		{Name: "empty query", Args: json.RawMessage(`{"query":""}`)},
		{Name: "missing", Args: json.RawMessage(`{}`), WantErr: true},
		{Name: "extra", Args: json.RawMessage(`{"query":"x","n":1}`), WantErr: true},
		{Name: "too long", Args: json.RawMessage(`{"query":"` + strings.Repeat("x", 201) + `"}`), WantErr: true},
	})
}
