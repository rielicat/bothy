package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type tarEntry struct {
	name string
	typ  byte
	body string
	mode int64
	link string
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": "7638417db6d59f3c431d3e1f261cc637155684cd"}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Format: tar.FormatPAX}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("header %s: %v", e.name, err)
		}
		if h.Size > 0 {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var roomy = archiveLimits{maxBytes: 1 << 20, maxFiles: 100}

func extractInto(t *testing.T, archive []byte, lim archiveLimits) (string, map[string]manifestFile, skips, error) {
	t.Helper()
	ws := t.TempDir()
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.MkdirAll("out", 0o770); err != nil {
		t.Fatal(err)
	}
	files, sk, err := extract(root, "out", bytes.NewReader(archive), lim)
	return ws, files, sk, err
}

func TestExtractWritesFilesAndManifest(t *testing.T) {
	arch := tarGz(t,
		tarEntry{name: "octocat-Hello-World-7638417/", typ: tar.TypeDir},
		tarEntry{name: "octocat-Hello-World-7638417/README.md", body: "# Hello\n"},
		tarEntry{name: "octocat-Hello-World-7638417/bin/run.sh", body: "#!/bin/sh\n", mode: 0o755},
		tarEntry{name: "octocat-Hello-World-7638417/src/deep/a.go", body: "package a\n"},
		tarEntry{name: "octocat-Hello-World-7638417/empty", body: ""},
	)
	ws, files, sk, err := extractInto(t, arch, roomy)
	if err != nil {
		t.Fatal(err)
	}
	if sk != (skips{}) || len(files) != 4 {
		t.Fatalf("skips %+v files %+v", sk, files)
	}
	want := map[string]manifestFile{
		"README.md":     {SHA256: sha("# Hello\n"), Size: 8},
		"bin/run.sh":    {SHA256: sha("#!/bin/sh\n"), Size: 10, Exec: true},
		"src/deep/a.go": {SHA256: sha("package a\n"), Size: 10},
		"empty":         {SHA256: sha(""), Size: 0},
	}
	for p, w := range want {
		if files[p] != w {
			t.Errorf("manifest[%s] = %+v, want %+v", p, files[p], w)
		}
	}
	b, err := os.ReadFile(filepath.Join(ws, "out", "src", "deep", "a.go"))
	if err != nil || string(b) != "package a\n" {
		t.Fatalf("written file = %q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(ws, "out", "bin", "run.sh"))
		if st.Mode().Perm()&0o100 == 0 {
			t.Errorf("executable lost its bit: %v", st.Mode())
		}
		st, _ = os.Stat(filepath.Join(ws, "out", "README.md"))
		if st.Mode().Perm()&0o111 != 0 || st.Mode().Perm()&0o007 != 0 {
			t.Errorf("plain file mode %v, want group-shared and not executable", st.Mode())
		}
	}
}

func TestExtractRefusesEscapesAndLinks(t *testing.T) {
	top := "o-r-1234567/"
	arch := tarGz(t,
		tarEntry{name: top, typ: tar.TypeDir},
		tarEntry{name: top + "../../escape.txt", body: "x"},
		tarEntry{name: top + "a/../../escape2.txt", body: "x"},
		tarEntry{name: "/etc/cron.d/evil", body: "x"},
		tarEntry{name: "other-top/file", body: "x"},
		tarEntry{name: top + "./dot", body: "x"},
		tarEntry{name: top + ".git/config", body: "[core]\n\tfsmonitor = touch /tmp/pwned\n"},
		tarEntry{name: top + "src/.git/hooks/post-checkout", body: "x"},
		tarEntry{name: top + "bad\nname", body: "x"},
		tarEntry{name: top + "bidi‮txt.exe", body: "x"},
		tarEntry{name: top + "back\\slash", body: "x"},
		tarEntry{name: top + "link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		tarEntry{name: top + "updir", typ: tar.TypeSymlink, link: "../.."},
		tarEntry{name: top + "updir/through-link.txt", body: "inside"},
		tarEntry{name: top + "hard", typ: tar.TypeLink, link: top + "ok.txt"},
		tarEntry{name: top + "fifo", typ: tar.TypeFifo},
		tarEntry{name: top + "dev", typ: tar.TypeChar},
		tarEntry{name: top + "ok.txt", body: "fine"},
	)
	ws, files, sk, err := extractInto(t, arch, roomy)
	if err != nil {
		t.Fatal(err)
	}
	if want := (skips{Symlinks: 2, Hardlinks: 1, Special: 2, Unsafe: 10}); sk != want {
		t.Errorf("skips = %+v, want %+v", sk, want)
	}
	if len(files) != 2 || files["ok.txt"].Size != 4 || files["updir/through-link.txt"].Size != 6 {
		t.Errorf("files = %+v", files)
	}
	// Nothing landed outside the extraction folder, and the dropped
	// symlink's name became a plain folder inside it.
	_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		if rel != "." && rel != "out" && !strings.HasPrefix(rel, "out"+string(filepath.Separator)) {
			t.Errorf("entry outside the extraction folder: %s", rel)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			t.Errorf("symlink written: %s", rel)
		}
		if strings.Contains(rel, ".git") {
			t.Errorf(".git entry written: %s", rel)
		}
		return nil
	})
	if st, err := os.Lstat(filepath.Join(ws, "out", "updir")); err != nil || !st.IsDir() {
		t.Errorf("updir = %v %v, want a real folder", st, err)
	}
}

func TestExtractCollisions(t *testing.T) {
	top := "o-r-1234567/"
	arch := tarGz(t,
		tarEntry{name: top + "a.txt", body: "first"},
		tarEntry{name: top + "a.txt", body: "second"},
		tarEntry{name: top + "f", body: "a file"},
		tarEntry{name: top + "f/child", body: "under a file"},
	)
	ws, files, sk, err := extractInto(t, arch, roomy)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Collisions != 2 || files["a.txt"].SHA256 != sha("first") {
		t.Errorf("skips %+v files %+v", sk, files)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "out", "a.txt")); string(b) != "first" {
		t.Errorf("a.txt = %q, want the first entry kept", b)
	}
}

func TestExtractCaps(t *testing.T) {
	top := "o-r-1234567/"
	many := []tarEntry{}
	for _, n := range []string{"a", "b", "c", "d"} {
		many = append(many, tarEntry{name: top + n, body: "x"})
	}
	if _, _, _, err := extractInto(t, tarGz(t, many...), archiveLimits{maxBytes: 1 << 20, maxFiles: 3}); !errors.Is(err, errTooMany) {
		t.Errorf("file cap: %v", err)
	}
	big := tarEntry{name: top + "big", body: strings.Repeat("z", 2048)}
	if _, _, _, err := extractInto(t, tarGz(t, big), archiveLimits{maxBytes: 1024, maxFiles: 10}); !errors.Is(err, errTooLarge) {
		t.Errorf("size cap: %v", err)
	}
	// A bomb: entries the walk skips are still read, so the decompressed
	// stream is capped as well.
	defer func(old int64) { archiveSlack = old }(archiveSlack)
	archiveSlack = 256 << 10
	var bomb []tarEntry
	for i := 0; i < 16; i++ {
		bomb = append(bomb, tarEntry{name: "/abs/pad" + strings.Repeat("x", i), body: strings.Repeat("\x00", 64<<10)})
	}
	if _, files, sk, err := extractInto(t, tarGz(t, bomb...), archiveLimits{maxBytes: 1024, maxFiles: 10}); !errors.Is(err, errTooLarge) || len(files) != 0 || sk.Unsafe == 0 {
		t.Errorf("bomb: %v, %d files, %+v", err, len(files), sk)
	}
	if _, err := walkArchive(bytes.NewReader([]byte("not gzip")), roomy, func(*archiveFile) error { return nil }); err == nil {
		t.Error("garbage accepted")
	}
}

func TestCapReader(t *testing.T) {
	r := &capReader{r: strings.NewReader(strings.Repeat("a", 100)), left: 10}
	b, err := io.ReadAll(r)
	if len(b) != 10 || !errors.Is(err, errOverRead) {
		t.Fatalf("read %d, %v", len(b), err)
	}
}

func TestReadBase(t *testing.T) {
	top := "o-r-1234567/"
	arch := tarGz(t,
		tarEntry{name: top + "a.txt", body: "alpha"},
		tarEntry{name: top + "link", typ: tar.TypeSymlink, link: "a.txt"},
		tarEntry{name: top + "dir/b.txt", body: "beta"},
		tarEntry{name: top + "c.txt", body: "gamma"},
	)
	p := filepath.Join(t.TempDir(), "base.tar.gz")
	if err := os.WriteFile(p, arch, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readBase(p, map[string]bool{"dir/b.txt": true, "a.txt": true, "link": true}, roomy)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["a.txt"]) != "alpha" || string(got["dir/b.txt"]) != "beta" {
		t.Fatalf("base = %q", got)
	}
	if _, err := readBase(filepath.Join(t.TempDir(), "missing"), map[string]bool{"a": true}, roomy); err == nil {
		t.Error("missing archive accepted")
	}
}

func TestSkipsString(t *testing.T) {
	if s := (skips{Symlinks: 2, Unsafe: 1, Collisions: 3}).String(); s != "2 symbolic links, 1 unsafe name, 3 name collisions" {
		t.Errorf("String = %q", s)
	}
	if (skips{}).String() != "" {
		t.Error("empty skips")
	}
}
