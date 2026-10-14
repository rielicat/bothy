//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestVaultCLI(t *testing.T) {
	data := t.TempDir()
	const value = "sk-test-CANARY-7f3a9c2e"

	_, errOut, code := cli(t, data, value+"\n", "vault", "set", "openai_api_key")
	if code != 0 || !strings.Contains(errOut, "secret:openai_api_key") {
		t.Fatalf("vault set = %d %q", code, errOut)
	}
	if strings.Contains(errOut, value) {
		t.Fatal("vault set echoed the value")
	}

	out, errOut, code := cli(t, data, "", "vault", "list")
	if code != 0 || !strings.Contains(out, "openai_api_key") || !strings.Contains(out, "c2e") {
		t.Fatalf("vault list = %d %q %q", code, out, errOut)
	}
	if strings.Contains(out, value) || strings.Contains(errOut, value) {
		t.Fatal("vault list printed the value")
	}

	if _, errOut, code := cli(t, data, "", "vault", "set", "empty"); code == 0 || !strings.Contains(errOut, "nothing on standard input") {
		t.Fatalf("empty vault set = %d %q", code, errOut)
	}
	if _, errOut, code := cli(t, data, strings.Repeat("x", 65<<10), "vault", "set", "huge"); code == 0 || !strings.Contains(errOut, "KiB") {
		t.Fatalf("oversized vault set = %d %q", code, errOut)
	}

	if _, errOut, code := cli(t, data, "", "vault", "delete", "openai_api_key"); code != 0 {
		t.Fatalf("vault delete = %d %q", code, errOut)
	}
	if out, _, _ := cli(t, data, "", "vault", "list"); strings.Contains(out, "openai_api_key") {
		t.Fatalf("secret still listed after delete: %q", out)
	}
}
