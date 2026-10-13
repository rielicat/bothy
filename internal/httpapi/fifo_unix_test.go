//go:build unix

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

// A box can plant a FIFO in its workspace; downloading it must answer at
// once instead of parking the handler until something writes to the pipe.
func TestWorkspaceDownloadRefusesFIFO(t *testing.T) {
	h := newHarness(t).setup()
	p, err := h.store.Principals().Get(context.Background(), core.SystemViewer(), core.UserPrincipal(h.member.user))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.workDir, "ws", string(p.Workspace))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "report.pdf"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- h.do("GET", "/api/workspace/files?path=report.pdf", nil, h.member) }()
	select {
	case rec := <-done:
		expect(t, rec, http.StatusNotFound)
	case <-time.After(3 * time.Second):
		t.Fatal("download of a FIFO blocked")
	}
}
