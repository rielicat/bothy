package auth

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureSetupTokenOnce(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Deps{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tok, created, err := s.EnsureSetupToken(context.Background())
	if err != nil || !created || len(tok) != 52 {
		t.Fatalf("first call = %q, %v, %v", tok, created, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "setup-token"))
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("token file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(dir, "setup-token")); fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode = %v", fi.Mode().Perm())
		}
	}
	if tok2, created, err := s.EnsureSetupToken(context.Background()); err != nil || created || tok2 != "" {
		t.Fatalf("second call = %q, %v, %v", tok2, created, err)
	}
}
