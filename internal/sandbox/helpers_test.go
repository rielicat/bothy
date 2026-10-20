package sandbox

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/sandboxd"
	"github.com/rielicat/bothy/internal/sandboxd/enginetest"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

var (
	testImage = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	key1      = core.DerivedID(core.PrefixBox, "session:one")
	key2      = core.DerivedID(core.PrefixBox, "session:two")
	ws1       = core.WorkspaceID(core.DerivedID(core.PrefixWorkspace, "one"))
)

// egress records calls and creates no sockets; OpenBox answers with the
// subpath boxes mount unless subpath is set.
type egress struct {
	core.Egress
	mu      sync.Mutex
	opened  map[string]core.BoxEgress
	closed  []string
	subpath string
	err     error
}

func (e *egress) OpenBox(_ context.Context, key string, be core.BoxEgress) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return "", e.err
	}
	if e.opened == nil {
		e.opened = map[string]core.BoxEgress{}
	}
	e.opened[key] = be
	if e.subpath != "" {
		return e.subpath, nil
	}
	return proto.EgressSubpath(key), nil
}

func (e *egress) CloseBox(key string) {
	e.mu.Lock()
	e.closed = append(e.closed, key)
	e.mu.Unlock()
}

func (e *egress) closedKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.closed)
}

// shortTempDir keeps unix socket paths under macOS's 104-byte limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base) > 20 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// bothImages is what the compose file gives sandboxd.
var bothImages = "base=" + testImage + ",node=" + testImage

// startSandboxd serves the real sandboxd API, backed by an enginetest.Fake,
// on a unix socket and waits for its self-test. images is BOTHY_SBX_IMAGES,
// bothImages when empty.
func startSandboxd(t *testing.T, sock, images string, mut func(*enginetest.Fake)) *enginetest.Fake {
	t.Helper()
	if images == "" {
		images = bothImages
	}
	runDir := shortTempDir(t)
	cfg, err := sandboxd.ParseConfig([]string{"BOTHY_SBX_IMAGES=" + images,
		"BOTHY_SBX_RUNTIME=runc", "BOTHY_SBX_SOCKET=" + sock, "BOTHY_SBX_RUN_DIR=" + runDir})
	if err != nil {
		t.Fatal(err)
	}
	eng := enginetest.New()
	eng.RunDir = runDir
	eng.AddImage(testImage)
	if mut != nil {
		mut(eng)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sandboxd.Serve(ctx, cfg, eng, ln, slog.New(slog.DiscardHandler)) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("sandboxd.Serve: %v", err)
		}
	})
	be := newSandboxdBackend(sock)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h, err := be.health(context.Background()); err == nil && h.OK {
			return eng
		}
		if time.Now().After(deadline) {
			t.Fatal("sandboxd did not become healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type env struct {
	c      *Client
	eg     *egress
	bus    *events.Bus
	audit  *fakes.Auditor
	clock  *fakes.Clock
	work   string
	run    string
	cfg    *config.Config
	socket string
}

type opts struct {
	sandboxd bool   // start a sidecar on the configured socket first
	images   string // the sidecar's BOTHY_SBX_IMAGES; bothImages when empty
	cfg      func(*config.Config)
	eng      func(*enginetest.Fake)
}

func newEnv(t *testing.T, o opts) (*env, *enginetest.Fake) {
	t.Helper()
	dir := shortTempDir(t)
	e := &env{eg: &egress{}, bus: events.New(events.Options{}), audit: &fakes.Auditor{},
		// Adopted boxes carry sandboxd's wall-clock times, so start at now.
		clock: fakes.NewClock(time.Now()), work: filepath.Join(dir, "work"), run: filepath.Join(dir, "run"),
		socket: filepath.Join(dir, "sd.sock")}
	for _, d := range []string{e.work, e.run} {
		if err := os.Mkdir(d, 0o770); err != nil {
			t.Fatal(err)
		}
	}
	var eng *enginetest.Fake
	if o.sandboxd {
		eng = startSandboxd(t, e.socket, o.images, o.eng)
	}
	e.cfg = config.Default()
	e.cfg.Sandbox.Backend = "sandboxd"
	e.cfg.Sandbox.Socket = e.socket
	if o.cfg != nil {
		o.cfg(e.cfg)
	}
	c, err := New(context.Background(), Deps{Config: config.Static(e.cfg), Egress: e.eg, Bus: e.bus, Audit: e.audit,
		WorkDir: e.work, RunDir: e.run, Clock: e.clock, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if sd, ok := c.svc.be.(*sandboxdBackend); ok {
		sd.group = os.Getgid() // the test user is not in group 10001
		// A pooled connection that never carried a request holds up the
		// sidecar's graceful shutdown for 5s.
		t.Cleanup(sd.hc.CloseIdleConnections)
	}
	e.c = c
	return e, eng
}

func (e *env) spec(key string) core.BoxSpec {
	return core.BoxSpec{Key: key, Workspace: ws1, Egress: core.BoxEgress{Session: "ses_x", Principal: "usr_x"}}
}

func (e *env) banners() map[string]core.BannerData {
	out := map[string]core.BannerData{}
	for _, ev := range e.bus.Banners(core.Filter{Admin: true}) {
		d := ev.Data.(core.BannerData)
		out[d.Code] = d
	}
	return out
}

// sandboxdPost calls the sidecar directly, as an operator would.
func sandboxdPost(t *testing.T, sock, path string) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	resp, err := hc.Post("http://sandboxd/v1"+path, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %d", path, resp.StatusCode)
	}
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
