package vault

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// A single-container install keeps its auto KEK in <data>/keys/kek. Moving to
// compose mounts an empty /keys volume, so the auto keyfile becomes /keys/kek:
// the vault must take the existing key along instead of sealing.
func TestAutoKeyMovesToTheNewKeysVolume(t *testing.T) {
	f := newFixture(t)
	legacy := f.cfg.Vault.Keyfile
	v := f.open()
	mustPut(t, v, core.GlobalScope, "anthropic_key", "sk-ant-moving-day-0123456789")

	keys := filepath.Join(t.TempDir(), "keys", "kek")
	f.cfg.Vault.Keyfile = ""
	v2, err := newVault(Deps{Store: f.store, Config: config.Static(f.cfg), Audit: f.audit}, options{
		secretsDir: f.secretsDir,
		resolveDirs: func(*config.Config) config.Dirs {
			return config.Dirs{Data: f.dir, Keyfile: keys, LegacyKeyfile: legacy}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st := v2.Status(); st.Sealed {
		t.Fatalf("vault sealed after the move: %+v", st)
	}
	if got := mustGet(t, v2, core.GlobalScope, "anthropic_key"); got != "sk-ant-moving-day-0123456789" {
		t.Fatalf("secret = %q", got)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the key is still next to the database: %v", err)
	}
	fi, err := os.Stat(keys)
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("moved keyfile = %v, %v", fi, err)
	}
}

// A Docker secret holding a new key seals the vault; the reason must point at
// the auto-generated key that still opens it, and the fix text must say to
// reuse that key rather than invent one.
func TestNewDockerSecretPointsAtTheWorkingKey(t *testing.T) {
	f := newFixture(t)
	f.colocated = true
	v := f.open()
	mustPut(t, v, core.GlobalScope, "k", "value-0123456789")
	if fs := v.checkColocated(); len(fs) != 1 || !strings.Contains(fs[0].Fix, "docker cp") || !strings.Contains(fs[0].Fix, f.cfg.Vault.Keyfile) {
		t.Fatalf("colocated finding = %+v", fs)
	}
	if err := writeKeyfile(filepath.Join(f.secretsDir, "bothy_kek"), newKey(), false); err != nil {
		t.Fatal(err)
	}
	v2 := f.open()
	if st, reason := v2.Status(), v2.reason; !st.Sealed || !strings.Contains(reason, f.cfg.Vault.Keyfile) {
		t.Fatalf("status = %+v (%s)", st, reason)
	}
}

// A key that does not open this vault is left where it is.
func TestForeignLegacyKeyIsNotAdopted(t *testing.T) {
	f := newFixture(t)
	v := f.open()
	mustPut(t, v, core.GlobalScope, "k", "value-0123456789")
	other := filepath.Join(t.TempDir(), "kek")
	if err := writeKeyfile(other, newKey(), false); err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(t.TempDir(), "keys", "kek")
	v2, err := newVault(Deps{Store: f.store, Config: config.Static(f.cfg), Audit: f.audit}, options{
		secretsDir: f.secretsDir,
		resolveDirs: func(*config.Config) config.Dirs {
			return config.Dirs{Data: f.dir, Keyfile: keys, LegacyKeyfile: other}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, reason := v2.Status(), v2.reason; !st.Sealed || !strings.Contains(reason, "KEK unavailable") {
		t.Fatalf("status = %+v (%s)", st, reason)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("foreign key removed: %v", err)
	}
}
