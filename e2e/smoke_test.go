//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmoke(t *testing.T) {
	g := Start(t, Options{})

	resp := g.Do(t, http.MethodGet, "/healthz", nil, nil)
	if resp.StatusCode != http.StatusOK || string(resp.Body) != "ok" {
		t.Fatalf("/healthz = %s %q, want 200 \"ok\"", resp.Status, resp.Body)
	}

	resp = g.Do(t, http.MethodGet, "/readyz", nil, nil)
	var ready struct {
		OK bool `json:"ok"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &ready) != nil || !ready.OK {
		t.Fatalf("/readyz = %s %q, want 200 {\"ok\":true}", resp.Status, resp.Body)
	}

	if tok := g.SetupToken(t); len(tok) < 32 {
		t.Fatalf("setup token %q is too short", tok)
	}
	if !strings.Contains(g.Logs(), "/setup#t=") {
		t.Errorf("setup banner missing from the boot log")
	}
	if _, err := os.Stat(filepath.Join(g.DataDir, "bothy.db")); err != nil {
		t.Errorf("database not created: %v", err)
	}

	if err := g.Stop(); err != nil {
		t.Fatalf("SIGTERM: unclean exit: %v", err)
	}
	t.Logf("cold start to /healthz: %s", g.Boot)
}
