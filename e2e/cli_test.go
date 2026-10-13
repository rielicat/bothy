//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// cli runs one offline admin command against dataDir and returns its stdout,
// stderr and exit code.
func cli(t *testing.T, dataDir, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), Binary(t), args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dataDir, "BOTHY_STORAGE__DATA_DIR=" + dataDir,
		"BOTHY_STORAGE__RUN_DIR=" + filepath.Join(t.TempDir(), "run"), "BOTHY_SANDBOX__BACKEND=none"}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bothy %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

func TestAdminCommands(t *testing.T) {
	g := Start(t, Options{})
	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: g.SetupToken(t)}, &claim); code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	g.SetCSRF(claim.CSRF)
	owner := dto.SetupOwnerReq{Username: "owner", Password: "Granite-River-42-Lantern", Timezone: "UTC"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, nil); code != http.StatusOK {
		t.Fatalf("owner = %d", code)
	}
	if err := g.Stop(); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	data := g.DataDir

	if out, _, code := cli(t, data, "", "user", "list"); code != 0 || !strings.Contains(out, "owner") || !strings.Contains(out, string(core.RoleOwner)) {
		t.Fatalf("user list = %d %q", code, out)
	}
	if _, errOut, code := cli(t, data, "", "setup-token"); code == 0 || !strings.Contains(errOut, "owner already exists") {
		t.Fatalf("setup-token with an owner = %d %q", code, errOut)
	}
	if _, errOut, code := cli(t, data, "Basalt-Harbor-77-Comet\n", "user", "reset-password", "owner"); code != 0 {
		t.Fatalf("reset-password = %d %q", code, errOut)
	}
	if out, errOut, code := cli(t, data, "", "audit", "verify"); code != 0 || !strings.Contains(out, "intact") {
		t.Fatalf("audit verify = %d %q %q", code, out, errOut)
	}
	if out, errOut, code := cli(t, data, "", "backup"); code != 0 || !strings.HasSuffix(strings.TrimSpace(out), ".tar") {
		t.Fatalf("backup = %d %q %q", code, out, errOut)
	}
	out, errOut, code := cli(t, data, "", "doctor", "--json")
	var report struct {
		Findings []core.Finding `json:"findings"`
	}
	if code > 1 || json.Unmarshal([]byte(out), &report) != nil {
		t.Fatalf("doctor --json = %d %q %q", code, out, errOut)
	}
	if _, _, code := cli(t, data, "", "user"); code != 2 {
		t.Fatalf("user without a subcommand = %d, want usage (2)", code)
	}

	g = Start(t, Options{DataDir: data})
	login := dto.LoginReq{Username: "owner", Password: "Basalt-Harbor-77-Comet"}
	if code := g.JSON(t, http.MethodPost, "/api/auth/login", login, nil); code != http.StatusOK {
		t.Fatalf("login with the reset password = %d", code)
	}
}
