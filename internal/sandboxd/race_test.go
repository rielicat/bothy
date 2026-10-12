package sandboxd

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/sandboxd/enginetest"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

// A box registered after the reaper listed the containers is not in the list
// without being gone; it must stay registered (else it runs on unreaped).
func TestReapKeepsBoxesCreatedDuringTheListing(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	h.eng.Vanish(containerName(testKey)) // stands in for "not listed yet"
	h.d.reap(context.Background())
	if _, err := h.d.lookup(testKey); err != nil {
		t.Fatal("a box created as the listing ran was forgotten")
	}
	h.advance(time.Second)
	h.d.reap(context.Background())
	if _, err := h.d.lookup(testKey); err == nil {
		t.Fatal("a box missing from a later listing is still registered")
	}
}

func TestRemoveIfSparesABoxThatTurnedBusy(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	b, _ := h.d.lookup(testKey)
	busy, err := h.d.acquire(testKey, false)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := h.d.removeIf(context.Background(), b, func(b *box) bool { return b.active == 0 })
	h.d.release(busy, false)
	if removed || err != nil || !slices.Contains(h.eng.Running(), containerName(testKey)) {
		t.Fatalf("removeIf = %v, %v; running %v", removed, err, h.eng.Running())
	}
}

func TestBoxBeingRemovedRefusesExecsAndDelaysCreate(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	b, _ := h.d.lookup(testKey)
	ch := make(chan struct{})
	h.d.mu.Lock()
	b.removing = ch
	h.d.mu.Unlock()
	h.wantError("POST", "/boxes/"+testKey+"/exec", execBody([]string{"echo"}, 1000, ""), http.StatusNotFound, "not_found")
	created := make(chan proto.CreateResp, 1)
	go func() {
		resp, err := h.d.create(context.Background(), proto.CreateReq{Key: testKey, Profile: "base", Workspace: testWS})
		if err != nil {
			t.Error(err)
		}
		created <- resp
	}()
	select {
	case <-created:
		t.Fatal("create did not wait for the removal")
	case <-time.After(100 * time.Millisecond):
	}
	// The removal failed: the box is still there and create returns it.
	h.d.mu.Lock()
	b.removing = nil
	h.d.mu.Unlock()
	close(ch)
	if resp := <-created; resp.Created || resp.ID != b.id {
		t.Fatalf("create after the removal = %+v", resp)
	}
}

func TestStdioCutsAServerThatIgnoresEOF(t *testing.T) {
	h := newHarness(t, func(_ *Config, e *enginetest.Fake) {
		e.Program = func(ctx context.Context, p *enginetest.Proc) int {
			<-ctx.Done() // never reads stdin, never exits on its own
			return 0
		}
	})
	h.d.stdioGrace = 50 * time.Millisecond
	h.create(testKey)
	t.Cleanup(func() { h.eng.Vanish(containerName(testKey)) })
	conn, _ := h.stdio(testKey, `{"argv":["mcp-server"]}`)
	conn.Close()
	waitFor(t, func() bool {
		b, err := h.d.acquire(testKey, true)
		if err != nil {
			return false
		}
		h.d.release(b, true)
		return true
	})
}

func TestSelfTestIgnoresCanceledCallers(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.d.selfTest(ctx)
	if ok, reason := h.d.healthy(); !ok {
		t.Fatalf("a canceled self-test replaced the health: %s", reason)
	}
	req := httptest.NewRequest("POST", "/v1/selftest", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.d.routes().ServeHTTP(rec, req)
	if ok, reason := h.d.healthy(); rec.Code != http.StatusOK || !ok {
		t.Fatalf("POST /selftest from a caller that hung up: %d, healthy %v (%s)", rec.Code, ok, reason)
	}
}

func TestNextSelfTest(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.d.nextSelfTest(); got != h.conf.SelfTestEvery {
		t.Fatalf("healthy: %v", got)
	}
	h.eng.NegotiateErr = errors.New("connection refused")
	h.d.selfTest(context.Background())
	if got := h.d.nextSelfTest(); got != selfTestRetry {
		t.Fatalf("engine unreachable: %v, want %v", got, selfTestRetry)
	}
	h.eng.NegotiateErr = nil
	h.eng.FailChecks = map[string][]string{"runc": {proto.CheckCaps}}
	h.d.selfTest(context.Background())
	if ok, _ := h.d.healthy(); ok {
		t.Fatal("isolation failure reported healthy")
	}
	if got := h.d.nextSelfTest(); got != h.conf.SelfTestEvery {
		t.Fatalf("isolation failure: %v", got)
	}
}

func TestCanaryKeyIsReserved(t *testing.T) {
	h := newHarness(t, nil)
	h.wantError("POST", "/boxes", createBody(canaryKey, testWS), http.StatusBadRequest, "bad_request")
}

// The gateway creates every box's socket directory under egress/; when
// sandboxd makes egress/ first it must stay writable for the run dir's group.
func TestEgressRootIsGroupWritable(t *testing.T) {
	h := newHarness(t, nil)
	fi, err := os.Stat(filepath.Join(h.conf.RunDir, "egress"))
	if err != nil || fi.Mode().Perm() != 0o770 {
		t.Fatalf("egress/ = %v, %v", fi.Mode(), err)
	}
	dir := shortTempDir(t)
	if err := os.Mkdir(filepath.Join(dir, "egress"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureEgressRoot(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "egress")); fi.Mode().Perm() != 0o700 {
		t.Fatalf("an existing egress/ was changed to %v", fi.Mode().Perm())
	}
	if err := ensureEgressRoot(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing run dir: %v", err)
	}
}
