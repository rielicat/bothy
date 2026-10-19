package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func set(t *testing.T, m *Manager, changes map[string]string) ([]string, error) {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range changes {
		raw[k] = json.RawMessage(v)
	}
	return m.Set(context.Background(), raw, "user:alice")
}

func mustSet(t *testing.T, m *Manager, changes map[string]string) []string {
	t.Helper()
	restart, err := set(t, m, changes)
	if err != nil {
		t.Fatal(err)
	}
	return restart
}

func fieldErrors(t *testing.T, err error) FieldErrors {
	t.Helper()
	var fe FieldErrors
	if !errors.As(err, &fe) || !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("want FieldErrors matching core.ErrInvalid, got %v", err)
	}
	return fe
}

func TestSetWritesSparseAtomicOverlay(t *testing.T) {
	dir := t.TempDir()
	uiPath := filepath.Join(dir, "bothy.ui.yaml")
	writeFile(t, uiPath, "{}\n")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(uiPath, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var recs []ChangeRecord
	m, err := loadErr(t, Options{DataDir: dir, OnSet: func(r ChangeRecord) { recs = append(recs, r) }})
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC) }
	mustSet(t, m, map[string]string{"budgets.daily_usd": "2.5", "models.fast": "[]", "egress.allow": `["pypi.org"]`})
	fi, err := os.Stat(uiPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	names, _ := os.ReadDir(dir)
	for _, n := range names {
		if n.Name() != "bothy.ui.yaml" {
			t.Errorf("leftover file %s", n.Name())
		}
	}
	b, _ := os.ReadFile(uiPath)
	text := string(b)
	for _, want := range []string{"daily_usd: 2.5 # set by user:alice at 2026-10-12T08:00:00Z", "allow: [pypi.org] # set by", "fast: [] # set by"} {
		if !strings.Contains(text, want) {
			t.Errorf("bothy.ui.yaml lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "server") || strings.Contains(text, "warn_ratio") {
		t.Errorf("overlay is not sparse:\n%s", text)
	}
	if len(recs) != 3 || recs[0].Path != "budgets.daily_usd" || string(recs[0].Old) != "5" || string(recs[0].New) != "2.5" ||
		recs[0].Action != "set" || recs[0].By != "user:alice" {
		t.Fatalf("audit records %+v", recs)
	}

	m2 := load(t, dir)
	if c := m2.Current(); c.Budgets.DailyUSD != 2.5 || len(c.Egress.Allow) != 1 {
		t.Fatalf("overlay not reloaded: %+v", c.Budgets)
	}
	if e := entry(t, m2.Effective(), "egress.allow"); e.Source.By != "user:alice" || e.Source.At.IsZero() {
		t.Fatalf("provenance lost: %+v", e.Source)
	}

	if err := m.Reset(context.Background(), "budgets", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if m.Current().Budgets.DailyUSD != 5 || len(m.Current().Egress.Allow) != 1 {
		t.Fatal("Reset must remove only the subtree")
	}
	if r := recs[len(recs)-1]; r.Action != "reset" || r.Path != "budgets" || string(r.New) != "null" || r.By != "user:bob" ||
		!strings.Contains(string(r.Old), `"daily_usd":2.5`) {
		t.Fatalf("reset record %+v", r)
	}
	n := len(recs)
	if err := m.Reset(context.Background(), "budgets.daily_usd", "user:bob"); err != nil || len(recs) != n {
		t.Fatal("resetting an absent override is a silent no-op")
	}
	if err := m.Reset(context.Background(), "budgets.nope", "user:bob"); err == nil {
		t.Fatal("Reset of an unknown key must fail")
	}
}

func TestSetKeyedItems(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "providers:\n  - {id: anthropic, type: anthropic, api_key: \"secret:anthropic_api_key\"}\n")
	var recs []ChangeRecord
	m := load(t, dir)
	m.SetOnSet(func(r ChangeRecord) { recs = append(recs, r) })

	mustSet(t, m, map[string]string{"providers.ollama": `{"type":"ollama","base_url":"http://host.docker.internal:11434","models":[{"id":"qwen3:14b","context":16384}]}`})
	c := m.Current()
	if len(c.Providers) != 2 || c.Providers[1].ID != "ollama" || c.Providers[1].Models[0].Context != 16384 {
		t.Fatalf("item not created: %+v", c.Providers)
	}
	if r := recs[0]; string(r.Old) != "null" || !strings.Contains(string(r.New), `"base_url":"http://host.docker.internal:11434"`) {
		t.Fatalf("record %+v", r)
	}
	mustSet(t, m, map[string]string{
		"providers.ollama.models.qwen3:14b.context": "32768",
		"providers.anthropic.base_url":              `"https://proxy.example.com"`,
		"models.main":                               `["anthropic/claude-sonnet-4-6","ollama/qwen3:14b"]`,
	})
	c = m.Current()
	if c.Providers[1].Models[0].Context != 32768 || c.Providers[0].BaseURL != "https://proxy.example.com" || c.Providers[0].APIKey == "" {
		t.Fatalf("field overrides: %+v", c.Providers)
	}
	if _, err := set(t, m, map[string]string{"providers.ollama": `{"id":"other","type":"ollama"}`}); err == nil {
		t.Fatal("a mismatched id must be rejected")
	}
	// Removing the UI item would orphan models.main: rejected as a new error.
	if _, err := set(t, m, map[string]string{"providers.ollama": "null"}); err == nil {
		t.Fatal("removal that breaks models.main must be rejected")
	}
	mustSet(t, m, map[string]string{"providers.ollama": "null", "models.main": `["anthropic/claude-sonnet-4-6"]`})
	if c = m.Current(); len(c.Providers) != 1 || c.Providers[0].BaseURL != "https://proxy.example.com" {
		t.Fatalf("item removal: %+v", c.Providers)
	}
	if r := recs[len(recs)-1]; r.Path != "providers.ollama" || r.Action != "reset" || string(r.New) != "null" {
		t.Fatalf("null is audited as a reset: %+v", r)
	}

	mustSet(t, m, map[string]string{"policy.rules.mine": `{"tool":"shell","argv0":["git"],"verdict":"ask"}`})
	rules := m.Current().Policy.Rules
	if len(rules) != 4 || rules[3].ID != "mine" || rules[0].ID != "readonly-shell" {
		t.Fatalf("new UI rules go after lower-layer rules: %+v", rules)
	}
	mustSet(t, m, map[string]string{"policy.rules.wiki.verdict": `"ask"`})
	if rules = m.Current().Policy.Rules; rules[1].ID != "wiki" || rules[1].Verdict != "ask" {
		t.Fatalf("overriding a field must not reorder: %+v", rules)
	}
	mustSet(t, m, map[string]string{"pricing.openai/gpt-5.1": `{"in":1.25,"out":10}`})
	if p := m.Current().Pricing["openai/gpt-5.1"]; p.In != 1.25 {
		t.Fatalf("dotted map key: %+v", m.Current().Pricing)
	}
	mustSet(t, m, map[string]string{"pricing.openai/gpt-5.1.out": "12"})
	if p := m.Current().Pricing["openai/gpt-5.1"]; p.In != 1.25 || p.Out != 12 {
		t.Fatalf("dotted map key field: %+v", p)
	}
}

func TestSetRejections(t *testing.T) {
	dir := t.TempDir()
	m := load(t, dir)
	for _, tc := range []struct {
		name    string
		changes map[string]string
		want    string
	}{
		{"locked tag", map[string]string{"server.listen": `"0.0.0.0:9000"`}, "server.listen: locked"},
		{"locked parent", map[string]string{"server": `{"listen":"0.0.0.0:9000"}`}, "server.listen: locked"},
		{"locked struct", map[string]string{"server.tls": `{}`}, "server.tls: locked"},
		{"locked api_base", map[string]string{"channels.telegram.api_base": `"https://evil.example"`}, "locked"},
		{"unknown key", map[string]string{"budgets.daily_usdd": "1"}, `unknown key "daily_usdd" (did you mean "daily_usd"?)`},
		{"type", map[string]string{"budgets.daily_usd": `"lots"`}, `expected a number, got "lots"`},
		{"enum", map[string]string{"policy.preset": `"carefull"`}, `(did you mean "careful"?)`}, //nolint:misspell // deliberate typo under test
		{"semantic", map[string]string{"models.main": `["antropic/x"]`}, `models.main[0]: unknown provider "antropic"`},
		{"secret literal", map[string]string{"providers.anthropic": `{"type":"anthropic","api_key":"sk-ant-api03-secretvalue"}`},
			"Move this secret to the vault: Settings → Vault, or `BOTHY_SECRET_ANTHROPIC_API_KEY`."},
		{"header literal", map[string]string{"mcp.servers.docs": `{"transport":"http","url":"https://mcp.example.com/mcp","headers":{"Authorization":"Bearer abc123"}}`},
			"BOTHY_SECRET_DOCS_MCP_TOKEN"},
		{"invalid json", map[string]string{"budgets.daily_usd": `{`}, "invalid JSON"},
		{"key in the address", map[string]string{"mcp.servers.docs": `{"transport":"http","url":"https://mcp.example.com/mcp?api_key=abc123"}`},
			"plaintext secrets are not allowed in the address; write ?api_key=secret:<name>"},
		{"key over plain http", map[string]string{"mcp.servers.docs": `{"transport":"http","url":"http://mcp.example.com/mcp","headers":{"Authorization":"Bearer secret:docs_mcp_token"}}`},
			"use https: plain http would send the key unencrypted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := set(t, m, tc.changes)
			fe := fieldErrors(t, err)
			if !strings.Contains(fe.Error(), tc.want) {
				t.Fatalf("error %q lacks %q", fe.Error(), tc.want)
			}
			if strings.Contains(fe.Error(), "secretvalue") || strings.Contains(fe.Error(), "abc123") {
				t.Fatal("plaintext secret in error")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "bothy.ui.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected changes must not write bothy.ui.yaml")
	}
	mustSet(t, m, map[string]string{"mcp.servers.docs": `{"transport":"http","url":"https://mcp.example.com/mcp","headers":{"Authorization":"Bearer secret:docs_mcp_token"}}`})
	if h := m.Current().MCP.Servers[0].Headers["Authorization"]; h != "Bearer secret:docs_mcp_token" {
		t.Fatalf("embedded ref: %q", h)
	}
	// A reference in the address, plain http to the admin's own network, and
	// an ordinary query parameter are fine.
	mustSet(t, m, map[string]string{
		"mcp.servers.hosted":   `{"transport":"http","url":"https://mcp.example.com/mcp?api_key=secret:hosted_key&keyword=x"}`,
		"mcp.servers.internal": `{"transport":"http","url":"http://10.0.0.5/mcp","allow_private":true,"headers":{"Authorization":"secret:internal_key"}}`,
		"mcp.servers.open":     `{"transport":"http","url":"http://mcp.example.com/mcp"}`,
	})
}

func TestConfigLockGlobs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "providers:\n  - {id: ollama, type: ollama}\n")
	writeFile(t, filepath.Join(dir, "bothy.ui.yaml"), "policy:\n  preset: hands_off\nserver:\n  listen: 0.0.0.0:1\nbudgets:\n  daily_usd: 1\n")
	m := load(t, dir, "BOTHY_CONFIG_LOCK=providers.*, policy.*", "BOTHY_POLICY__PRESET=balanced")
	c, eff := m.Current(), m.Effective()
	if c.Policy.Preset != "balanced" || c.Server.Listen != "127.0.0.1:8080" || c.Budgets.DailyUSD != 1 {
		t.Fatalf("UI overrides of locked keys must be ignored: %+v %+v", c.Policy.Preset, c.Server.Listen)
	}
	if len(errorsMatching(eff, "policy.preset: locked")) != 1 || len(errorsMatching(eff, "server.listen: locked")) != 1 {
		t.Fatalf("errors %+v", eff.Errors)
	}
	for _, tc := range []struct {
		path   string
		locked bool
	}{{"policy.preset", true}, {"policy.trifecta.enforce", true}, {"providers.ollama.base_url", true}, {"budgets.daily_usd", false}, {"vault.kek", true}} {
		if e := entry(t, eff, tc.path); e.Locked != tc.locked {
			t.Errorf("%s locked=%v", tc.path, e.Locked)
		}
	}
	for _, p := range []string{"policy.preset", "providers.openai", "providers"} {
		if _, err := set(t, m, map[string]string{p: `{}`}); err == nil {
			t.Errorf("Set %s must be rejected", p)
		}
	}
	if err := m.Reset(context.Background(), "policy.preset", "user:alice"); err != nil {
		t.Fatalf("clearing a stale locked override is allowed: %v", err)
	}
}

func TestHotAndRestart(t *testing.T) {
	m := load(t, t.TempDir())
	if r := mustSet(t, m, map[string]string{"budgets.daily_usd": "3", "server.allowed_hosts": `["bothy.lan"]`}); len(r) != 0 {
		t.Fatalf("hot keys need no restart: %v", r)
	}
	if r := mustSet(t, m, map[string]string{"server.public_url": `"https://bothy.example.com"`, "auth.argon2.memory_kib": "65536"}); strings.Join(r, ",") != "auth.argon2.memory_kib,server.public_url" {
		t.Fatalf("restart = %v", r)
	}
	if got := m.Current().Server.PublicURL; got != "https://bothy.example.com" {
		t.Fatalf("Current reflects the change: %q", got)
	}
	if eff := m.Effective(); strings.Join(eff.Restart, ",") != "auth.argon2.memory_kib,server.public_url" {
		t.Fatalf("Effective.Restart = %v", eff.Restart)
	}
	eff := m.Effective()
	for _, tc := range []struct {
		path string
		hot  bool
	}{{"budgets.daily_usd", true}, {"server.allowed_hosts", true}, {"server.public_url", false}, {"storage.backups_keep", true},
		{"storage.data_dir", false}, {"auth.session_idle", true}, {"auth.argon2.memory_kib", false}, {"sandbox.profile", true},
		{"sandbox.backend", false}, {"channels.unknown_senders", true}, {"channels.telegram.enabled", true}, {"providers", true}, {"mode", true}} {
		if e := entry(t, eff, tc.path); e.Hot != tc.hot {
			t.Errorf("%s hot=%v", tc.path, e.Hot)
		}
	}
	for _, p := range []string{"server.public_url", "auth.argon2.memory_kib"} {
		if err := m.Reset(context.Background(), p, "user:alice"); err != nil {
			t.Fatal(err)
		}
	}
	if eff := m.Effective(); len(eff.Restart) != 0 {
		t.Fatalf("reverting clears Restart: %v", eff.Restart)
	}
}

func TestWatch(t *testing.T) {
	m := load(t, t.TempDir())
	var order []string
	var seen *Config
	c1 := m.Watch(func(old, cur *Config) {
		order = append(order, "a")
		if old.Budgets.DailyUSD != 5 || cur.Budgets.DailyUSD != 7 || m.Current() != cur {
			t.Errorf("watcher got old=%v cur=%v", old.Budgets.DailyUSD, cur.Budgets.DailyUSD)
		}
		_ = m.Effective() // watchers may read the manager
		seen = cur
	})
	m.Watch(func(old, cur *Config) { order = append(order, "b") })
	mustSet(t, m, map[string]string{"budgets.daily_usd": "7"})
	if strings.Join(order, "") != "ab" || seen == nil {
		t.Fatalf("order %v", order)
	}
	c1()
	c1()
	if err := m.Reset(context.Background(), "budgets.daily_usd", "user:alice"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, "") != "abb" {
		t.Fatalf("cancelled watcher still called: %v", order)
	}
	if err := m.Reload(context.Background()); err != nil || strings.Join(order, "") != "abb" {
		t.Fatalf("an unchanged Reload notifies nobody: %v %v", err, order)
	}
}

func TestPollReload(t *testing.T) {
	old := pollInterval
	pollInterval = 10 * time.Millisecond
	t.Cleanup(func() { pollInterval = old })
	dir := t.TempDir()
	path := filepath.Join(dir, "bothy.yaml")
	writeFile(t, path, "budgets: {daily_usd: 4}\n")
	m := load(t, dir)
	changed := make(chan float64, 4)
	m.Watch(func(_, cur *Config) { changed <- cur.Budgets.DailyUSD })
	writeFile(t, path, "budgets: {daily_usd: 6.5}\n")
	select {
	case v := <-changed:
		if v != 6.5 {
			t.Fatalf("reloaded %v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poller did not pick up the edit")
	}
}

func TestConcurrentCurrentAndSet(t *testing.T) {
	m := load(t, t.TempDir())
	m.Watch(func(old, cur *Config) { _ = cur.Budgets.DailyUSD })
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c := m.Current()
				if d := c.Budgets.DailyUSD; d != 5 && d != 1 && d != 2 {
					t.Errorf("torn read %v", d)
				}
				_ = m.Effective().Entries
				_ = Diff(c, m.Current())
			}
		}()
	}
	for i := range 20 {
		v := "1"
		if i%2 == 1 {
			v = "2"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := set(t, m, map[string]string{"budgets.daily_usd": v}); err != nil {
				t.Error(err)
			}
		}()
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = m.Reload(context.Background())
	}()
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	if d := m.Current().Budgets.DailyUSD; d != 1 && d != 2 {
		t.Fatalf("final %v", d)
	}
}

func TestBrokenUIFileBlocksWrites(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.ui.yaml"), "budgets: [\n")
	m := load(t, dir)
	if eff := m.Effective(); eff.SafeMode || len(errorsMatching(eff, "bothy.ui.yaml:")) != 1 {
		t.Fatalf("safe=%v errors=%+v", eff.SafeMode, eff.Errors)
	}
	if _, err := set(t, m, map[string]string{"budgets.daily_usd": "1"}); err == nil {
		t.Fatal("Set must not clobber an unparsable bothy.ui.yaml")
	}
}
