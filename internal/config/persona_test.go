package config

import "testing"

// PUT /api/persona overwrites agent.persona_file, so the setting must never
// name a file the gateway itself reads: pointed at bothy.yaml it would set
// locked keys, pointed at bothy.db it would leak and destroy the database.
func TestSetPersonaFileOnlyBareMarkdown(t *testing.T) {
	m := load(t, t.TempDir())
	for _, bad := range []string{"bothy.yaml", "bothy.ui.yaml", "bothy.db", "bothy.db-wal", "setup-token", "keys/kek",
		"memory/x.md", "../SOUL.md", "SOUL"} {
		_, err := set(t, m, map[string]string{"agent.persona_file": `"` + bad + `"`})
		if fe := fieldErrors(t, err); len(fe) != 1 || fe[0].Path != "agent.persona_file" {
			t.Errorf("%s: %v", bad, err)
		}
	}
	mustSet(t, m, map[string]string{"agent.persona_file": `"Persona.MD"`})
	if got := m.Current().Agent.PersonaFile; got != "Persona.MD" {
		t.Fatalf("persona_file = %q", got)
	}
	for name, want := range map[string]bool{"SOUL.md": true, "bothy.yaml": false, "a/b.md": false, `a\b.md`: false, ".md": false, "": false} {
		if ValidPersonaFile(name) != want {
			t.Errorf("ValidPersonaFile(%q) = %v", name, !want)
		}
	}
}
