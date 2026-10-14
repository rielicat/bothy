package config

import (
	"path/filepath"
	"testing"
)

func TestProviderAPI(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), `providers:
  - {id: openai, type: openai, api: responses}
  - {id: compat, type: openai_compat, base_url: "http://localhost:1234/v1", api: chat}
  - {id: typo, type: openai, api: assistants}
  - {id: claude, type: anthropic, api: responses}
  - {id: plain, type: openai}
`)
	m := load(t, dir)
	c, eff := m.Current(), m.Effective()
	for _, w := range []string{
		`bothy.yaml:4: providers.typo.api: invalid value "assistants"; want one of auto | responses | chat`,
		`bothy.yaml:5: providers.claude.api: only providers of type openai or openai_compat choose an api`,
	} {
		if len(errorsMatching(eff, w)) != 1 {
			t.Errorf("missing error %q in %+v", w, eff.Errors)
		}
	}
	if len(eff.Errors) != 2 {
		t.Errorf("errors %+v", eff.Errors)
	}
	want := map[string]string{"openai": "responses", "compat": "chat", "typo": "", "claude": "", "plain": ""}
	if len(c.Providers) != len(want) {
		t.Fatalf("providers %+v", c.Providers)
	}
	for _, p := range c.Providers {
		if p.API != want[p.ID] {
			t.Errorf("%s api %q, want %q", p.ID, p.API, want[p.ID])
		}
	}
	// The setup wizard and Settings change it at runtime; providers reload hot.
	if r := mustSet(t, m, map[string]string{"providers.plain.api": `"chat"`}); len(r) != 0 {
		t.Fatalf("restart %v", r)
	}
	for _, p := range m.Current().Providers {
		if p.ID == "plain" && p.API != "chat" {
			t.Fatalf("plain api %q", p.API)
		}
	}
}
