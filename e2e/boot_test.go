//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRestartBeforeSetup: a gateway restarted while setup is pending (the
// first token is still valid, so none is minted) still says where to go,
// without repeating the token, and every boot logs the listen address.
func TestRestartBeforeSetup(t *testing.T) {
	g := Start(t, Options{})
	if logs := g.Logs(); !strings.Contains(logs, "/setup#t=") {
		t.Fatalf("first boot printed no setup link:\n%s", logs)
	}
	if err := g.Stop(); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}

	g = Start(t, Options{DataDir: g.DataDir})
	logs := g.Logs()
	for _, want := range []string{
		"waiting for setup",
		g.BaseURL + "/setup",
		filepath.Join(g.DataDir, "setup-token"),
		"bothy setup-token",
		"listen=" + strings.TrimPrefix(g.BaseURL, "http://"),
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("restart logs lack %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "#t=") {
		t.Errorf("restart repeated the setup token:\n%s", logs)
	}
}

// TestOfflineDoctorSaysWhatItCannotSee: the CLI doctor builds a fresh app, so
// checks fed by the running gateway's calls and connections never fire there.
func TestOfflineDoctorSaysWhatItCannotSee(t *testing.T) {
	_, errOut, code := cli(t, t.TempDir(), "", "doctor")
	if code > 1 || !strings.Contains(errOut, "channels.auth_failed") || !strings.Contains(errOut, "Health and security") {
		t.Fatalf("doctor = %d, stderr %q", code, errOut)
	}
}
