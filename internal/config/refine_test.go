package config

import (
	"path/filepath"
	"testing"
)

func TestRefineDefaultsAndValidation(t *testing.T) {
	d := Default().Agent.Refine
	if d != (Refine{Drafts: 3, Depth: 2, EarlyStop: true, CriticSlot: "main"}) {
		t.Fatalf("defaults %+v", d)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), `agent:
  refine:
    enabled: true
    early_stop: false
    critic_slot: fast
`)
	m := load(t, dir, "BOTHY_AGENT__REFINE__DRAFTS=8")
	got, eff := m.Current().Agent.Refine, m.Effective()
	if len(eff.Errors) != 0 {
		t.Fatalf("errors %+v", eff.Errors)
	}
	if got != (Refine{Enabled: true, Drafts: 8, Depth: 2, EarlyStop: false, CriticSlot: "fast"}) {
		t.Fatalf("refine %+v", got)
	}
	if e := entry(t, eff, "agent.refine.enabled"); !e.Hot || e.Source.Layer != LayerFile {
		t.Fatalf("agent.refine.enabled %+v", e)
	}

	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), `agent:
  refine:
    drafts: 9
    depth: 0
    critic_slot: cheap
`)
	m = load(t, dir)
	eff = m.Effective()
	for _, w := range []string{
		`bothy.yaml:3: agent.refine.drafts`,
		`bothy.yaml:4: agent.refine.depth`,
		`bothy.yaml:5: agent.refine.critic_slot: invalid value "cheap"; want one of main | fast`,
	} {
		if len(errorsMatching(eff, w)) != 1 {
			t.Errorf("missing error %q in %+v", w, eff.Errors)
		}
	}
	if m.Current().Agent.Refine != d {
		t.Fatalf("invalid values fall back to the defaults: %+v", m.Current().Agent.Refine)
	}
}
