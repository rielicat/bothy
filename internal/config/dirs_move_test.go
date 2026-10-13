package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Moving from the single container to compose mounts /keys and /work; the
// resolved paths must name the old locations so they can be moved over.
func TestResolveDirsNamesTheSingleContainerPaths(t *testing.T) {
	c := Default()
	c.Storage.DataDir = "/data"
	d := resolveDirs(c, mounts{"/", "/data", "/work", "/keys"})
	if d.LegacyWork != "/data/work" || d.LegacyKeyfile != "/data/keys/kek" {
		t.Fatalf("compose: %+v", d)
	}
	if d = resolveDirs(c, mounts{"/", "/data"}); d.LegacyWork != "" || d.LegacyKeyfile != "" {
		t.Fatalf("single container: %+v", d)
	}
	c.Storage.WorkDir, c.Vault.Keyfile = "/w", "/k/kek"
	if d = resolveDirs(c, mounts{"/", "/data", "/work", "/keys"}); d.LegacyWork != "" || d.LegacyKeyfile != "" {
		t.Fatalf("explicit paths: %+v", d)
	}
}

func legacyTree(t *testing.T) Dirs {
	t.Helper()
	data, work := t.TempDir(), t.TempDir()
	legacy := filepath.Join(data, "work")
	for _, p := range []string{"workspaces/usr_a/notes.md", "skills/s/SKILL.md"} {
		full := filepath.Join(legacy, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("content of "+p), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("notes.md", filepath.Join(legacy, "workspaces/usr_a/link.md")); err != nil {
			t.Fatal(err)
		}
	}
	return Dirs{Data: data, Work: work, LegacyWork: legacy}
}

func checkMoved(t *testing.T, d Dirs) {
	t.Helper()
	for _, p := range []string{"workspaces/usr_a/notes.md", "skills/s/SKILL.md"} {
		b, err := os.ReadFile(filepath.Join(d.Work, p))
		if err != nil || string(b) != "content of "+p {
			t.Fatalf("%s = %q, %v", p, b, err)
		}
	}
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(filepath.Join(d.Work, "workspaces/usr_a/link.md")); err != nil || target != "notes.md" {
			t.Fatalf("symlink = %q, %v", target, err)
		}
	}
	if _, err := os.Stat(d.LegacyWork); !os.IsNotExist(err) {
		t.Fatalf("old work dir left: %v", err)
	}
	entries, _ := os.ReadDir(d.Work)
	if len(entries) != 2 {
		t.Fatalf("work dir holds %d entries", len(entries))
	}
}

func TestMoveLegacyWorkSameVolume(t *testing.T) {
	d := legacyTree(t)
	if moved, err := MoveLegacyWork(d); err != nil || !moved {
		t.Fatalf("move = %v, %v", moved, err)
	}
	checkMoved(t, d)
	if moved, err := MoveLegacyWork(d); err != nil || moved {
		t.Fatalf("second move = %v, %v", moved, err)
	}
}

func TestMoveLegacyWorkAcrossVolumes(t *testing.T) {
	d := legacyTree(t)
	rename = func(string, string) error { return errors.New("invalid cross-device link") }
	t.Cleanup(func() { rename = os.Rename })
	if moved, err := MoveLegacyWork(d); err != nil || !moved {
		t.Fatalf("move = %v, %v", moved, err)
	}
	checkMoved(t, d)
}

// A /work volume that already holds data is never merged into.
func TestMoveLegacyWorkLeavesABusyVolumeAlone(t *testing.T) {
	d := legacyTree(t)
	if err := os.WriteFile(filepath.Join(d.Work, "existing"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if moved, err := MoveLegacyWork(d); err != nil || moved {
		t.Fatalf("move = %v, %v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(d.LegacyWork, "workspaces/usr_a/notes.md")); err != nil {
		t.Fatal(err)
	}
}
