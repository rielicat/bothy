package treehash

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func build(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "a", "b"), 0o750))
	must(os.WriteFile(filepath.Join(dir, "a", "b", "f.txt"), []byte("hello"), 0o640))
	must(os.WriteFile(filepath.Join(dir, "top"), []byte("x"), 0o640))
	must(os.Symlink("a/b/f.txt", filepath.Join(dir, "link")))
	return dir
}

func sum(t *testing.T, dir string) string {
	t.Helper()
	s, err := Sum(dir)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	return s
}

func TestSumDetectsEveryKindOfChange(t *testing.T) {
	base := sum(t, build(t))
	if again := sum(t, build(t)); again != base {
		t.Fatal("identical trees hash differently")
	}
	for name, mutate := range map[string]func(dir string) error{
		"content":   func(d string) error { return os.WriteFile(filepath.Join(d, "top"), []byte("y"), 0o640) },
		"mode":      func(d string) error { return os.Chmod(filepath.Join(d, "top"), 0o600) },
		"rename":    func(d string) error { return os.Rename(filepath.Join(d, "top"), filepath.Join(d, "top2")) },
		"new file":  func(d string) error { return os.WriteFile(filepath.Join(d, "a", "new"), nil, 0o640) },
		"empty dir": func(d string) error { return os.Mkdir(filepath.Join(d, "a", "empty"), 0o750) },
		"link target": func(d string) error {
			if err := os.Remove(filepath.Join(d, "link")); err != nil {
				return err
			}
			return os.Symlink("top", filepath.Join(d, "link"))
		},
		"file to link": func(d string) error {
			if err := os.Remove(filepath.Join(d, "top")); err != nil {
				return err
			}
			return os.Symlink("x", filepath.Join(d, "top"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := build(t)
			if err := mutate(dir); err != nil {
				t.Fatal(err)
			}
			if sum(t, dir) == base {
				t.Fatal("change not detected")
			}
		})
	}
}

func TestSumDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	before := sum(t, dir)
	if err := os.WriteFile(filepath.Join(outside, "f"), []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}
	if sum(t, dir) != before {
		t.Fatal("hash followed a symlink out of the tree")
	}
}

// SumRootFiles hashes like SumRoot and hands on the bytes it hashed, so a
// copy taken from it is the tree the sum stands for.
func TestSumRootFilesHandsOnWhatItHashed(t *testing.T) {
	dir := build(t)
	if err := os.WriteFile(filepath.Join(dir, "big"), []byte("0123456789"), 0o640); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	type seen struct {
		sum  string
		size int64
		data []byte
	}
	got := map[string]seen{}
	s, err := SumRootFiles(root, 5, func(p string, sum []byte, size int64, data []byte) {
		got[p] = seen{hex.EncodeToString(sum), size, data}
	})
	if err != nil {
		t.Fatal(err)
	}
	if s != sum(t, dir) {
		t.Fatal("SumRootFiles and Sum disagree")
	}
	hello := sha256.Sum256([]byte("hello"))
	if f := got["a/b/f.txt"]; string(f.data) != "hello" || f.size != 5 || f.sum != hex.EncodeToString(hello[:]) {
		t.Fatalf("a/b/f.txt = %+v", f)
	}
	if f := got["big"]; f.data != nil || f.size != 10 || f.sum == "" {
		t.Fatalf("a file over keep = %+v; want its hash and size only", f)
	}
	if _, ok := got["link"]; ok || len(got) != 3 {
		t.Fatalf("visited %v; want the regular files only", got)
	}
}
