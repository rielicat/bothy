package config

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func load(t *testing.T, dir string, env ...string) *Manager {
	t.Helper()
	m, err := loadErr(t, Options{DataDir: dir, Env: func() []string { return env }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func loadErr(t *testing.T, o Options) (*Manager, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return Load(ctx, o)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func entry(t *testing.T, eff Effective, path string) Entry {
	t.Helper()
	for _, e := range eff.Entries {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("no entry %q", path)
	return Entry{}
}

func errorsMatching(eff Effective, substr string) []FieldError {
	var out []FieldError
	for _, fe := range eff.Errors {
		if strings.Contains(fe.Message, substr) {
			out = append(out, fe)
		}
	}
	return out
}

func lineOfText(t *testing.T, content, substr string) int {
	t.Helper()
	for i, l := range strings.Split(content, "\n") {
		if strings.Contains(l, substr) {
			return i + 1
		}
	}
	t.Fatalf("%q not found", substr)
	return 0
}

func TestDefault(t *testing.T) {
	c := Default()
	if c.Server.Listen != "127.0.0.1:8080" || c.Storage.DataDir != "/data" || c.Auth.SetupTokenTTL.D() != 24*time.Hour ||
		c.Storage.BackupsKeep != 3 || !c.Policy.Trifecta.Enforce || c.Agent.CoalesceWindow.D() != 1200*time.Millisecond {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if Default() == Default() {
		t.Fatal("Default must return a fresh value")
	}
	v := &vctx{layerOf: func(string) int { return 0 }}
	validate(Default(), v)
	if len(v.errs) != 0 {
		t.Fatalf("defaults fail validation: %+v", v.errs)
	}
}

func TestExampleStrictDecode(t *testing.T) {
	b, err := os.ReadFile("testdata/bothy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 5 || c.Providers[3].ID != "ollama" || !c.Providers[3].AllowPrivate ||
		c.Providers[3].Models[0].ID != "qwen3:14b" || !*c.Providers[3].Models[0].Tools || *c.Providers[1].Enabled {
		t.Errorf("providers: %+v", c.Providers)
	}
	if c.Models.Main[1] != "ollama/qwen3:14b" || c.Pricing["ollama/qwen3:14b"] != (PriceO{}) {
		t.Errorf("models/pricing: %+v %+v", c.Models, c.Pricing)
	}
	if c.Policy.Rules[2].Match["/argv/2"] != "*curl*|*sh*" || c.Policy.Approval.APIWait.D() != 120*time.Second {
		t.Errorf("policy: %+v", c.Policy)
	}
	if s := c.MCP.Servers[1]; s.Package.Version != "2026.7.1" || s.SideEffects["read_*"] != "read" || *s.Enabled {
		t.Errorf("mcp: %+v", s)
	}
	if len(c.Egress.SharedTenantSuffixes) != 8 || c.Channels.Discord.APIBase != "https://discord.com/api/v10" {
		t.Errorf("egress/channels: %+v %+v", c.Egress, c.Channels)
	}
}

// The testdata copy must stay identical to the SPEC §7.1 block.
func TestExampleMatchesSpec(t *testing.T) {
	spec, err := os.ReadFile("../../docs/SPEC.md")
	if err != nil {
		t.Skip("docs/SPEC.md not available")
	}
	_, rest, ok := strings.Cut(string(spec), "### 7.1 ")
	if !ok {
		t.Fatal("no §7.1 in SPEC.md")
	}
	_, rest, _ = strings.Cut(rest, "```yaml\n")
	block, _, _ := strings.Cut(rest, "```\n")
	b, _ := os.ReadFile("testdata/bothy.yaml")
	if string(b) != block {
		t.Fatal("testdata/bothy.yaml differs from the SPEC §7.1 example")
	}
}

func TestExampleLoadsCleanly(t *testing.T) {
	dir := t.TempDir()
	b, _ := os.ReadFile("testdata/bothy.yaml")
	writeFile(t, filepath.Join(dir, "bothy.yaml"), string(b))
	m := load(t, dir)
	eff := m.Effective()
	if len(eff.Errors) != 0 || eff.SafeMode {
		t.Fatalf("errors: %+v", eff.Errors)
	}
	// Only the keys marked "example" in §7.1 may differ from Default().
	for _, p := range Diff(Default(), m.Current()) {
		if !strings.HasPrefix(p, "providers") && !strings.HasPrefix(p, "models.") &&
			!strings.HasPrefix(p, "pricing") && !strings.HasPrefix(p, "mcp.") {
			t.Errorf("non-example key differs from Default(): %s", p)
		}
	}
	e := entry(t, eff, "budgets.daily_usd")
	if e.Source.Layer != LayerFile || e.Source.Line != lineOfText(t, string(b), "daily_usd: 5.00") {
		t.Errorf("provenance %+v", e.Source)
	}
}

func TestPrecedenceMatrix(t *testing.T) {
	const key = "budgets.daily_usd"
	for _, tc := range []struct {
		name             string
		file, env, ui    bool
		want             float64
		layer            Layer
		shadowedFromHigh []Layer
	}{
		{"default only", false, false, false, 5, LayerDefault, nil},
		{"file", true, false, false, 4, LayerFile, []Layer{LayerDefault}},
		{"env over file", true, true, false, 3, LayerEnv, []Layer{LayerFile, LayerDefault}},
		{"env without file", false, true, false, 3, LayerEnv, []Layer{LayerDefault}},
		{"ui over all", true, true, true, 2, LayerUI, []Layer{LayerEnv, LayerFile, LayerDefault}},
		{"ui over file", true, false, true, 2, LayerUI, []Layer{LayerFile, LayerDefault}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var env []string
			if tc.file {
				writeFile(t, filepath.Join(dir, "bothy.yaml"), "budgets:\n  daily_usd: 4\n")
			}
			if tc.env {
				env = append(env, "BOTHY_BUDGETS__DAILY_USD=3")
			}
			if tc.ui {
				writeFile(t, filepath.Join(dir, "bothy.ui.yaml"), "budgets:\n  daily_usd: 2 # set by user:alice at 2026-10-12T10:00:00Z\n")
			}
			m := load(t, dir, env...)
			if got := m.Current().Budgets.DailyUSD; got != tc.want {
				t.Fatalf("daily_usd = %v, want %v", got, tc.want)
			}
			e := entry(t, m.Effective(), key)
			if e.Source.Layer != tc.layer || string(e.Value) != jsonNum(tc.want) {
				t.Fatalf("entry %+v", e)
			}
			var got []Layer
			for _, s := range e.Shadowed {
				got = append(got, s.Source.Layer)
			}
			if strings.Join(layersStr(got), ",") != strings.Join(layersStr(tc.shadowedFromHigh), ",") {
				t.Fatalf("shadowed %v, want %v", got, tc.shadowedFromHigh)
			}
		})
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func layersStr(ls []Layer) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = string(l)
	}
	return out
}

func TestProvenance(t *testing.T) {
	dir := t.TempDir()
	file := `mode: multi
budgets:
  daily_usd: 4
providers:
  - id: ollama
    type: ollama
    base_url: "http://host:11434"
models:
  main:
    - ollama/qwen3:14b
`
	writeFile(t, filepath.Join(dir, "bothy.yaml"), file)
	m := load(t, dir, "BOTHY_BUDGETS__DAILY_USD=3")
	eff := m.Effective()
	path := filepath.Join(dir, "bothy.yaml")
	for _, tc := range []struct {
		key  string
		line int
	}{{"mode", 1}, {"providers.ollama.id", 5}, {"providers.ollama.base_url", 7}, {"models.main", 9}} {
		e := entry(t, eff, tc.key)
		if e.Source != (Source{Layer: LayerFile, File: path, Line: tc.line}) {
			t.Errorf("%s: source %+v, want line %d", tc.key, e.Source, tc.line)
		}
	}
	e := entry(t, eff, "budgets.daily_usd")
	if e.Source != (Source{Layer: LayerEnv, EnvVar: "BOTHY_BUDGETS__DAILY_USD"}) || string(e.Value) != "3" {
		t.Fatalf("env entry %+v", e)
	}
	if len(e.Shadowed) != 2 || e.Shadowed[0].Source.Line != 3 || string(e.Shadowed[0].Value) != "4" ||
		e.Shadowed[1].Source.Layer != LayerDefault || string(e.Shadowed[1].Value) != "5" {
		t.Fatalf("shadowed %+v", e.Shadowed)
	}
	if e := entry(t, eff, "server.listen"); e.Source.Layer != LayerDefault || !e.Locked || e.Hot {
		t.Errorf("server.listen %+v", e)
	}
	if e := entry(t, eff, "agent.queue_depth"); string(e.Value) != "8" || !e.Hot || e.Locked {
		t.Errorf("agent.queue_depth %+v", e)
	}
	if e := entry(t, eff, "search.brave_api_key"); !e.Secret || string(e.Value) != `"secret:brave_api_key"` {
		t.Errorf("search.brave_api_key %+v", e)
	}
	if e := entry(t, eff, "agent.coalesce_window"); string(e.Value) != `"1.2s"` {
		t.Errorf("duration value %s", e.Value)
	}
	if e := entry(t, eff, "pricing"); string(e.Value) != "{}" || e.Source.Layer != LayerDefault {
		t.Errorf("empty map entry %+v", e)
	}

	at := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	if _, err := m.Set(context.Background(), map[string]json.RawMessage{"budgets.daily_usd": json.RawMessage("2")}, "user:alice"); err != nil {
		t.Fatal(err)
	}
	e = entry(t, m.Effective(), "budgets.daily_usd")
	if e.Source.Layer != LayerUI || e.Source.By != "user:alice" || !e.Source.At.Equal(at) || e.Source.Line == 0 || len(e.Shadowed) != 3 {
		t.Fatalf("ui entry %+v", e)
	}
	// Provenance survives a restart through the file's line comments.
	e2 := entry(t, load(t, dir, "BOTHY_BUDGETS__DAILY_USD=3").Effective(), "budgets.daily_usd")
	if e2.Source != e.Source {
		t.Fatalf("after reload %+v, want %+v", e2.Source, e.Source)
	}
}

func TestEnvOverlay(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), `
providers:
  - {id: ollama, type: ollama, base_url: "http://host:11434"}
mcp:
  servers:
    - {name: my-docs, transport: http, url: "https://docs.example.com/mcp"}
`)
	env := []string{
		"BOTHY_SERVER__LISTEN=127.0.0.1:18080",
		"BOTHY_BUDGETS__DAILY_USD=2",
		"BOTHY_AGENT__COALESCE_WINDOW=2s",
		"BOTHY_LOG__LEVEL=debug",
		"BOTHY_AUTH__ARGON2__ITERATIONS=3",
		"BOTHY_SCHEDULER__ENABLED=false",
		"BOTHY_API__OPENAI_COMPAT=0",
		"BOTHY_AGENT__QUEUE_DEPTH=lots",
		"BOTHY_MODE=multi",
		"BOTHY_SERVER__PUBLIC_URL=",
		"BOTHY_PROVIDERS__OLLAMA__BASE_URL=http://gpu:11434",
		`BOTHY_PROVIDERS=[{"id":"openai","type":"openai","api_key":"secret:openai_api_key"}]`,
		"BOTHY_MCP__SERVERS__MY_DOCS__URL=https://docs2.example.com/mcp",
		`BOTHY_MODELS__MAIN=["openai/gpt-5.1","ollama/qwen3:14b"]`,
		`BOTHY_EGRESS__ALLOW=["pypi.org", "*.example.com:8443"]`,
		`BOTHY_PRICING={"openai/gpt-5.1":{"in":1.25,"out":10}}`,
		"BOTHY_POLICY__DEFAULTS__EXTERNAL_SEND=ask",
		"BOTHY_BUDGETS__DAILYUSD=1",
		"BOTHY_SECRET_DOCS_MCP_TOKEN=xyz",
		"BOTHY_SBX_IMAGES=whatever",
		"BOTHY_UNRELATED_TOOL=1",
		"BOTHY_DEPLOYMENT=container",
	}
	m := load(t, dir, env...)
	c := m.Current()
	checks := []struct {
		name string
		ok   bool
	}{
		{"listen", c.Server.Listen == "127.0.0.1:18080"},
		{"daily usd", c.Budgets.DailyUSD == 2},
		{"duration", c.Agent.CoalesceWindow.D() == 2*time.Second},
		{"log level", c.Log.Level == "debug"},
		{"nested uint", c.Auth.Argon2.Iterations == 3},
		{"bool", !c.Scheduler.Enabled},
		{"bool from 0", !c.API.OpenAICompat},
		{"bad value keeps default", c.Agent.QueueDepth == 8},
		{"top level", c.Mode == "multi"},
		{"empty string", c.Server.PublicURL == ""},
		{"id-keyed item", len(c.Providers) == 2 && c.Providers[0].BaseURL == "http://gpu:11434" && c.Providers[0].Type == "ollama"},
		{"json list merges by id", c.Providers[1].ID == "openai" && c.Providers[1].APIKey == "secret:openai_api_key"},
		{"name-keyed item", c.MCP.Servers[0].URL == "https://docs2.example.com/mcp"},
		{"scalar list replaces", strings.Join(c.Models.Main, ",") == "openai/gpt-5.1,ollama/qwen3:14b"},
		{"egress list", len(c.Egress.Allow) == 2},
		{"json map", c.Pricing["openai/gpt-5.1"].Out == 10},
		{"map entry", c.Policy.Defaults["external_send"] == "ask"},
		{"data dir", c.Storage.DataDir == dir},
	}
	for _, ck := range checks {
		if !ck.ok {
			t.Errorf("%s not applied: %+v", ck.name, c)
		}
	}
	eff := m.Effective()
	if len(eff.Errors) != 2 {
		t.Fatalf("errors: %+v", eff.Errors)
	}
	if e := errorsMatching(eff, "env BOTHY_AGENT__QUEUE_DEPTH: agent.queue_depth: expected an integer"); len(e) != 1 {
		t.Errorf("queue depth error: %+v", eff.Errors)
	}
	if e := errorsMatching(eff, `budgets.dailyusd: unknown key (did you mean "daily_usd"?)`); len(e) != 1 || e[0].Source.EnvVar != "BOTHY_BUDGETS__DAILYUSD" {
		t.Errorf("unknown key error: %+v", eff.Errors)
	}
	if e := entry(t, eff, "providers.ollama.base_url"); e.Source.EnvVar != "BOTHY_PROVIDERS__OLLAMA__BASE_URL" || e.Shadowed[0].Source.Layer != LayerFile {
		t.Errorf("item provenance %+v", e)
	}
	if e := entry(t, eff, "providers.ollama.id"); e.Source.Layer != LayerFile {
		t.Errorf("an item's id belongs to its lowest layer: %+v", e)
	}
}

func TestLoadContainerDefaults(t *testing.T) {
	m, err := loadErr(t, Options{
		DataDir:  t.TempDir(),
		Env:      func() []string { return []string{"BOTHY_DEPLOYMENT=container"} },
		Defaults: func(c *Config) { c.Timezone = "Europe/London" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Current(); c.Server.Listen != "0.0.0.0:8080" || c.Timezone != "Europe/London" {
		t.Fatalf("got listen=%q tz=%q", c.Server.Listen, c.Timezone)
	}
	if e := entry(t, m.Effective(), "server.listen"); e.Source.Layer != LayerDefault {
		t.Fatalf("source %+v", e.Source)
	}
}

func TestLoadDataDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadErr(t, Options{DataDir: filepath.Join(dir, "missing")}); err != nil {
		t.Fatalf("a missing data dir is created later, not an error: %v", err)
	}
	f := filepath.Join(dir, "file")
	writeFile(t, f, "")
	if _, err := loadErr(t, Options{DataDir: f}); err == nil {
		t.Fatal("a data dir that is a file must fail")
	}
}

func TestSafeMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bothy.yaml")
	writeFile(t, path, "budgets:\n  daily_usd: 4\nserver: [unclosed\n")
	writeFile(t, filepath.Join(dir, "bothy.ui.yaml"), "budgets:\n  warn_ratio: 0.5\n")
	m := load(t, dir, "BOTHY_BUDGETS__MEMBER_DAILY_USD=3")
	eff := m.Effective()
	if !eff.SafeMode || len(eff.Errors) != 1 || !strings.HasPrefix(eff.Errors[0].Message, "bothy.yaml:3: cannot parse") ||
		eff.Errors[0].Source.Line != 3 {
		t.Fatalf("safe=%v errors=%+v", eff.SafeMode, eff.Errors)
	}
	c := m.Current()
	if c.Budgets.DailyUSD != 5 || c.Budgets.MemberDailyUSD != 3 || c.Budgets.WarnRatio != 0.5 || c.Server.Listen != "127.0.0.1:8080" {
		t.Fatalf("safe mode must use defaults + env + ui: %+v", c.Budgets)
	}

	var calls int
	m.Watch(func(old, cur *Config) { calls++ })
	writeFile(t, path, "budgets:\n  daily_usd: 4\n")
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	eff = m.Effective()
	if !eff.SafeMode || len(eff.Errors) != 0 || m.Current().Budgets.DailyUSD != 4 || calls != 1 {
		t.Fatalf("after fix: safe=%v errs=%+v daily=%v calls=%d", eff.SafeMode, eff.Errors, m.Current().Budgets.DailyUSD, calls)
	}

	// A file that breaks after a good boot keeps its last good contents.
	dir2 := t.TempDir()
	path2 := filepath.Join(dir2, "bothy.yaml")
	writeFile(t, path2, "budgets:\n  daily_usd: 4\n")
	m2 := load(t, dir2)
	writeFile(t, path2, "budgets: {daily_usd: 1\n")
	if err := m2.Reload(context.Background()); err == nil {
		t.Fatal("Reload must report the parse error")
	}
	if eff := m2.Effective(); eff.SafeMode || len(eff.Errors) != 1 || m2.Current().Budgets.DailyUSD != 4 {
		t.Fatalf("broken reload: safe=%v errors=%+v daily=%v", eff.SafeMode, eff.Errors, m2.Current().Budgets.DailyUSD)
	}
}

func TestSafeModeExplicitFile(t *testing.T) {
	dir := t.TempDir()
	m, err := loadErr(t, Options{DataDir: dir, File: filepath.Join(dir, "nope.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if eff := m.Effective(); !eff.SafeMode || len(errorsMatching(eff, "nope.yaml: cannot read")) != 1 {
		t.Fatalf("safe=%v errors=%+v", eff.SafeMode, eff.Errors)
	}
	notMapping := filepath.Join(dir, "list.yaml")
	writeFile(t, notMapping, "- a\n- b\n")
	m, _ = loadErr(t, Options{DataDir: dir, File: notMapping})
	if !m.Effective().SafeMode {
		t.Fatal("a non-mapping document is unparsable")
	}
}

func TestFileErrorsAndFallback(t *testing.T) {
	dir := t.TempDir()
	file := `budgets:
  daily_usdd: 3
agent:
  busy_mode: hurry
sandbox:
  backend: none
providers:
  - {id: anthropic, type: anthropic, api_key: "secret:anthropic_api_key"}
  - {id: broken}
  - {id: anthropic, type: openai}
models:
  main:
    - anthropic/claude-sonnet-4-6
    - antropic/claude-opus-4-1
context:
  compact_below: 1000
channels:
  telegram:
    token: "123456:plaintext-token"
`
	writeFile(t, filepath.Join(dir, "bothy.yaml"), file)
	writeFile(t, filepath.Join(dir, "bothy.ui.yaml"), "sandbox:\n  backend: nonsense\n")
	m := load(t, dir, "BOTHY_SANDBOX__BACKEND=bogus")
	c, eff := m.Current(), m.Effective()
	if eff.SafeMode {
		t.Fatal("per-key errors never cause safe mode")
	}
	want := []string{
		`bothy.yaml:2: budgets.daily_usdd: unknown key (did you mean "daily_usd"?)`,
		`bothy.yaml:4: agent.busy_mode: invalid value "hurry"; want one of steer | queue`,
		`bothy.yaml:9: providers.broken: type is required`,
		`bothy.yaml:10: providers.anthropic: duplicate id "anthropic"`,
		`bothy.yaml:14: models.main[1]: unknown provider "antropic" (did you mean "anthropic"?)`,
		`bothy.yaml:16: context.compact_below: compact_below (1000) must be at least degraded_below (16384)`,
		`bothy.yaml:19: channels.telegram.token: plaintext secrets are not allowed`,
		`env BOTHY_SANDBOX__BACKEND: sandbox.backend: invalid value "bogus"`,
		`bothy.ui.yaml:2: sandbox.backend: invalid value "nonsense"`,
	}
	for _, w := range want {
		if len(errorsMatching(eff, w)) != 1 {
			t.Errorf("missing error %q", w)
		}
	}
	if len(eff.Errors) != len(want) {
		t.Errorf("got %d errors: %+v", len(eff.Errors), eff.Errors)
	}
	if c.Agent.BusyMode != "steer" || c.Sandbox.Backend != "none" || c.Context.CompactBelow != 32768 ||
		len(c.Models.Main) != 0 || len(c.Providers) != 1 || c.Channels.Telegram.Token != "secret:telegram_bot_token" {
		t.Fatalf("fallbacks not applied: %+v", c)
	}
	for _, fe := range eff.Errors {
		if strings.Contains(fe.Message, "plaintext-token") {
			t.Fatal("a plaintext secret leaked into an error")
		}
	}
	e := entry(t, eff, "channels.telegram.token")
	if e.Source.Layer != LayerDefault || len(e.Shadowed) != 0 {
		t.Fatalf("token entry %+v", e)
	}
	e = entry(t, eff, "sandbox.backend")
	if e.Source.Layer != LayerFile || len(e.Shadowed) != 1 {
		t.Fatalf("sandbox.backend entry %+v", e)
	}
}

func TestSecretShadowRedacted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "search:\n  brave_api_key: BSA-plaintext\n")
	m := load(t, dir, "BOTHY_SEARCH__BRAVE_API_KEY=secret:brave_env")
	e := entry(t, m.Effective(), "search.brave_api_key")
	if string(e.Value) != `"secret:brave_env"` || len(e.Shadowed) != 2 || string(e.Shadowed[0].Value) != `"[redacted]"` {
		t.Fatalf("entry %+v", e)
	}
	if b, _ := json.Marshal(m.Effective()); strings.Contains(string(b), "BSA-plaintext") {
		t.Fatal("plaintext secret in Effective")
	}
}

func TestDiff(t *testing.T) {
	a, b := Default(), Default()
	b.Budgets.DailyUSD = 9
	b.Providers = []Provider{{ID: "x", Type: "ollama"}}
	b.Models.Main = []string{"x/y"}
	got := strings.Join(Diff(a, b), " ")
	for _, p := range []string{"budgets.daily_usd", "models.main", "providers", "providers.x.id", "providers.x.type"} {
		if !strings.Contains(" "+got+" ", " "+p+" ") {
			t.Errorf("Diff missing %s: %s", p, got)
		}
	}
	if len(Diff(a, Default())) != 0 {
		t.Error("equal configs differ")
	}
	if len(Diff(nil, a)) == 0 {
		t.Error("nil vs defaults must differ")
	}
}

func TestResolveDirs(t *testing.T) {
	c := Default()
	c.Storage.DataDir = "/srv/data"
	d := resolveDirs(c, nil)
	if d.Data != "/srv/data" || d.Work != filepath.Join("/srv/data", "work") || d.Run != "/run/bothy" ||
		d.Keyfile != filepath.Join("/srv/data", "keys", "kek") || !d.KeyfileColocated {
		t.Fatalf("no mount table: %+v", d)
	}
	vols := mounts{"/", "/work", "/keys", "/srv/data"}
	if d = resolveDirs(c, vols); d.Work != "/work" || d.Keyfile != "/keys/kek" || d.KeyfileColocated {
		t.Fatalf("separate volumes: %+v", d)
	}
	if d = resolveDirs(c, mounts{"/", "/srv/data"}); d.Keyfile != "/srv/data/keys/kek" || !d.KeyfileColocated {
		t.Fatalf("keys under data: %+v", d)
	}
	c.Storage.DataDir = "/data"
	if d = resolveDirs(c, mounts{"/"}); !d.KeyfileColocated {
		t.Fatalf("one root volume: %+v", d)
	}
	c.Storage.WorkDir, c.Vault.Keyfile = "/w", "/k/kek"
	if d = resolveDirs(c, mounts{"/", "/k"}); d.Work != "/w" || d.Keyfile != "/k/kek" || d.KeyfileColocated {
		t.Fatalf("explicit: %+v", d)
	}
}

func TestMountinfo(t *testing.T) {
	info := `22 1 0:21 / / rw,relatime - overlay overlay rw
35 22 8:1 /var/lib/docker/volumes/bothy_work/_data /work rw,relatime - ext4 /dev/sda1 rw
36 22 8:1 /x /my\040dir rw - ext4 /dev/sda1 rw
`
	mt := parseMountinfo(bufio.NewReader(strings.NewReader(info)))
	if !mt.isMountPoint("/work") || !mt.isMountPoint("/my dir") || mt.isMountPoint("/keys") || mt.isMountPoint("/wor") {
		t.Fatalf("mountinfo parsing: %v", mt)
	}
	if mt.mountOf("/work/x/y") != "/work" || mt.mountOf("/workshop") != "/" || mt.mountOf("/data/bothy.db") != "/" {
		t.Fatal("mountOf")
	}
}
