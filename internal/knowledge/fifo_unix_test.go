//go:build unix

package knowledge

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within fails the test when fn has not returned after a few seconds: the
// box can plant FIFOs in the workspace, and opening one for reading blocks
// until a writer appears unless it is opened non-blocking.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked on a FIFO", what)
	}
}

func TestFIFOsNeverBlockTheIndexerOrTheDraft(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	dir := filepath.Join(h.ws, "repos", "acme", "api")
	for _, name := range []string{"pipe.go", "go.mod"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// The walk skips FIFOs by type; a file swapped for one after that check
	// reaches read, which must refuse it at once.
	ix := &indexer{root: root, caps: caps{fileBytes: 1 << 20, files: 100, totalBytes: 1 << 30}}
	within(t, "read", func() {
		if _, ok := ix.read("pipe.go"); ok {
			t.Error("read returned a FIFO's content")
		}
	})
	within(t, "openRegular", func() {
		if f, err := openRegular(root, "pipe.go"); err == nil {
			f.Close()
			t.Error("openRegular opened a FIFO")
		}
	})
	within(t, "index", func() {
		r := h.index("repos/acme/api", "acme/api", "")
		if r.Status != "ready" {
			t.Errorf("index with FIFOs = %+v", r)
		}
	})
	within(t, "draftInput", func() {
		in, err := draftInput(h.ws, "repos/acme/api", nil)
		if err != nil {
			t.Errorf("draftInput = %v", err)
		}
		if len(in) == 0 {
			t.Error("draftInput read nothing")
		}
	})
}
