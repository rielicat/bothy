package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The persona endpoints must never read or replace the gateway's own files,
// even when agent.persona_file names one (a value that bypassed validation).
func TestPersonaRefusesGatewayFiles(t *testing.T) {
	h := newHarness(t).setup()
	for _, name := range []string{"bothy.yaml", "bothy.ui.yaml", "bothy.db", "bothy.db-wal", "setup-token"} {
		p := filepath.Join(h.dataDir, name)
		if err := os.WriteFile(p, []byte("server:\n  insecure_lan: false\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.cfg.update(func(c *configT) { c.Agent.PersonaFile = name })
		expect(t, h.do("GET", "/api/persona", nil, h.member), http.StatusServiceUnavailable)
		expect(t, h.do("PUT", "/api/persona", map[string]string{"content": "server:\n  insecure_lan: true\n"}, h.admin),
			http.StatusServiceUnavailable)
		if b, _ := os.ReadFile(p); string(b) != "server:\n  insecure_lan: false\n" {
			t.Fatalf("%s was overwritten: %q", name, b)
		}
	}
}
