package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		segs string // joined with "|"
		item bool
	}{
		{"budgets.daily_usd", "budgets|daily_usd", false},
		{"providers.ollama", "providers|ollama", true},
		{"providers.ollama.base_url", "providers|ollama|base_url", false},
		{"pricing.openai/gpt-5.1", "pricing|openai/gpt-5.1", false},
		{"pricing.openai/gpt-5.1.in", "pricing|openai/gpt-5.1|in", false},
		{"providers.openrouter.models.meta-llama/llama-3.1-8b.context", "providers|openrouter|models|meta-llama/llama-3.1-8b|context", false},
		{"mcp.servers.docs.headers.X-Api.Key", "mcp|servers|docs|headers|X-Api.Key", false},
		{"policy.rules.no-pipe-to-shell.match./argv/2", "policy|rules|no-pipe-to-shell|match|/argv/2", false},
		// No field "nothing": the remainder is read as a dotted id, which
		// validation then rejects for providers.
		{"providers.x.nothing", "providers|x.nothing", true},
	} {
		got, err := resolvePath(tc.path)
		if err != nil {
			t.Errorf("%s: %v", tc.path, err)
			continue
		}
		if strings.Join(got.segs, "|") != tc.segs || got.item != tc.item {
			t.Errorf("%s: segs %q item %v", tc.path, got.segs, got.item)
		}
	}
	for path, want := range map[string]string{
		"budgets.daily_usdd": `unknown key "daily_usdd" (did you mean "daily_usd"?)`,
		"servr.listen":       `unknown key "servr" (did you mean "server"?)`,
		"mode.":              "unknown key",
		"mode.x":             "has no sub-keys",
		"":                   "empty path",
		"server.tls.cert":    `unknown key "cert"`,
	} {
		if _, err := resolvePath(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", path, err, want)
		}
	}
}

func TestGlobs(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		ok         bool
	}{
		{"providers.*", "providers.ollama.base_url", true},
		{"policy.*", "policy.preset", true},
		{"policy.*", "policyx", false},
		{"*.api_base", "channels.telegram.api_base", true},
		{"budgets.?aily_usd", "budgets.daily_usd", true},
		{"budgets", "budgets.daily_usd", false},
		{"", "", true},
	} {
		if got := globMatch(tc.pattern, tc.s); got != tc.ok {
			t.Errorf("globMatch(%q, %q) = %v", tc.pattern, tc.s, got)
		}
	}
	locks := parseLocks(" providers.*, ,budgets ")
	for _, tc := range []struct {
		segs []string
		ok   bool
	}{
		{[]string{"providers"}, true},
		{[]string{"providers", "x", "id"}, true},
		{[]string{"budgets", "run", "max_usd"}, true},
		{[]string{"models", "main"}, false},
	} {
		if got := globLocked(locks, tc.segs); got != tc.ok {
			t.Errorf("globLocked(%v) = %v", tc.segs, got)
		}
	}
}

func TestSuggest(t *testing.T) {
	if s := suggest("antropic", []string{"openai", "anthropic"}); s != "anthropic" {
		t.Errorf("got %q", s)
	}
	if s := suggest("zzz", []string{"anthropic"}); s != "" {
		t.Errorf("got %q", s)
	}
}

func TestSchema(t *testing.T) {
	m := load(t, t.TempDir())
	raw := m.Schema()
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s["$schema"] != "https://json-schema.org/draft/2020-12/schema" || s["additionalProperties"] != false {
		t.Fatalf("root %v", s["$schema"])
	}
	prop := func(path ...string) map[string]any {
		cur := s
		for _, p := range path {
			switch p {
			case "[]":
				cur = cur["items"].(map[string]any)
			case "{}":
				cur = cur["additionalProperties"].(map[string]any)
			default:
				cur = cur["properties"].(map[string]any)[p].(map[string]any)
			}
		}
		return cur
	}
	if p := prop("server", "listen"); p["x-locked"] != true || p["x-reload"] != "restart" || p["type"] != "string" || p["default"] != "127.0.0.1:8080" {
		t.Errorf("server.listen %v", p)
	}
	if p := prop("budgets", "daily_usd"); p["x-reload"] != "hot" || p["type"] != "number" || p["default"] != 5.0 {
		t.Errorf("budgets.daily_usd %v", p)
	}
	if p := prop("providers"); p["x-key"] != "id" || p["type"] != "array" {
		t.Errorf("providers %v", p)
	}
	if p := prop("providers", "[]", "api_key"); p["x-secret"] != true || p["pattern"] == nil {
		t.Errorf("api_key %v", p)
	}
	if p := prop("providers", "[]", "type"); len(p["enum"].([]any)) != 6 {
		t.Errorf("type enum %v", p)
	}
	if p := prop("providers", "[]", "enabled"); p["type"].([]any)[1] != "null" {
		t.Errorf("*bool %v", p)
	}
	if p := prop("mcp", "servers", "[]", "headers", "{}"); p["x-secret"] != true {
		t.Errorf("header values %v", p)
	}
	if p := prop("agent", "coalesce_window"); p["format"] != "duration" || p["default"] != "1.2s" {
		t.Errorf("duration %v", p)
	}
	if p := prop("policy", "rules", "[]", "triggers"); p["items"].(map[string]any)["enum"] == nil {
		t.Errorf("triggers %v", p)
	}
	if p := prop("auth", "argon2", "parallelism"); p["maximum"] != 255.0 || p["minimum"] != 0.0 {
		t.Errorf("uint8 %v", p)
	}
}
