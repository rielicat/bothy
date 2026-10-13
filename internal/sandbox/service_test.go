package sandbox

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/sandboxd/enginetest"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

var _ core.Sandbox = (*Client)(nil)
var _ core.ToolProvider = (*Client)(nil)
var _ core.CheckProvider = (*Client)(nil)

func TestNoneBackend(t *testing.T) {
	e, _ := newEnv(t, opts{cfg: func(c *config.Config) { c.Sandbox.Backend = "none" }})
	ctx := context.Background()
	st := e.c.Status(ctx)
	if st.Backend != "none" || st.Available || st.Reason == "" || st.CheckedAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	wantErr(t, e.c.Ensure(ctx, e.spec(key1)), core.ErrNoSandbox)
	_, err := e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"true"}}, nil)
	wantErr(t, err, core.ErrNoSandbox)
	_, err = e.c.Stdio(ctx, key1, []string{"cat"}, nil)
	wantErr(t, err, core.ErrNoSandbox)
	wantErr(t, e.c.Kill(ctx, key1), core.ErrNoSandbox)
	wantErr(t, e.c.Destroy(ctx, key1), core.ErrNoSandbox)
	if _, _, err := e.c.Usage(ctx, ws1); err != nil {
		t.Fatalf("Usage must work without a sandbox: %v", err)
	}
	called := false
	e.c.Watch(func(core.SandboxStatus) { called = true })
	_ = called
	if b, ok := e.banners()["no_sandbox"]; !ok || b.Level != "warn" || !strings.Contains(b.Text, "shell, python and MCP stdio are off") {
		t.Fatalf("banners = %+v", e.banners())
	}
	if len(e.c.Tools()) != 2 {
		t.Fatal("shell and python are always provided; the registry hides them without a sandbox")
	}
}

func TestSandboxdEndToEnd(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	st := e.c.Status(ctx)
	if !st.Available || st.Backend != "sandboxd" || st.Runtime != "runc" || st.Engine != "docker 27.4.0 (api 1.47)" {
		t.Fatalf("status = %+v", st)
	}
	if _, ok := e.banners()["no_sandbox"]; ok {
		t.Fatal("no_sandbox banner while available")
	}
	spec := e.spec(key1)
	spec.MCPTree = "mcp/docs/in_0123456789abcdefghjkmnpqrs"
	if err := e.c.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct {
		path string
		mode fs.FileMode
	}{
		{filepath.Join(e.work, "ws", string(ws1)), 0o770 | fs.ModeSetgid},
		{filepath.Join(e.work, "mcp", "docs", "in_0123456789abcdefghjkmnpqrs"), 0o770 | fs.ModeSetgid},
		{filepath.Join(e.work, "mcp", "docs"), 0o770 | fs.ModeSetgid},
		{filepath.Join(e.work, "skills"), 0o770 | fs.ModeSetgid},
		{filepath.Join(e.run, "egress"), 0o770},
		{filepath.Join(e.run, proto.EgressSubpath(key1)), 0o750},
	} {
		fi, err := os.Stat(d.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode() & (fs.ModePerm | fs.ModeSetgid); got != d.mode {
			t.Errorf("%s: mode %v, want %v", d.path, got, d.mode)
		}
	}
	if be := e.eg.opened[key1]; be.Session != "ses_x" || be.Principal != "usr_x" {
		t.Fatalf("OpenBox got %+v", be)
	}
	// Idempotent per key.
	if err := e.c.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if n := len(eng.Running()); n != 1 {
		t.Fatalf("%d boxes running", n)
	}
	box := eng.Created()[len(eng.Created())-1]
	if box.HostConfig.Memory != 512<<20 || box.Labels["bothy.profile"] != "base" {
		t.Fatalf("config defaults not applied: mem %d profile %q", box.HostConfig.Memory, box.Labels["bothy.profile"])
	}

	var streamed strings.Builder
	var mu sync.Mutex
	res, err := e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"echo", "hi"}, Cwd: "src"}, func(s int, b []byte) {
		mu.Lock()
		streamed.Write(b)
		mu.Unlock()
	})
	if err != nil || res.Exit != 0 || string(res.Stdout) != "hi\n" || streamed.String() != "hi\n" {
		t.Fatalf("exec = %+v, %v (streamed %q)", res, err, streamed.String())
	}
	res, err = e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"flood", "5000"}, MaxOutput: 100}, nil)
	if err != nil || len(res.Stdout) != 100 || !res.Truncated {
		t.Fatalf("flood = %d bytes truncated %v, %v", len(res.Stdout), res.Truncated, err)
	}
	res, err = e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"sleep"}, Timeout: 100 * time.Millisecond}, nil)
	if err != nil || !res.TimedOut || res.Exit != 124 {
		t.Fatalf("timeout = %+v, %v", res, err)
	}

	if err := e.c.Kill(ctx, key1); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Destroy(ctx, key1); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.eg.closedKeys(), key1) || len(eng.Running()) != 0 {
		t.Fatalf("destroy: closed %v running %v", e.eg.closedKeys(), eng.Running())
	}
	// Kill of an unknown box is not an error.
	if err := e.c.Kill(ctx, key2); err != nil {
		t.Fatal(err)
	}
}

func TestValidationHappensBeforeTheSidecar(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	for name, spec := range map[string]core.BoxSpec{
		"key":        {Key: "box_bad"},
		"workspace":  {Key: key1, Workspace: "ws_../../etc"},
		"tree":       {Key: key1, MCPTree: "mcp/../x"},
		"rw no tree": {Key: key1, MCPTreeRW: true},
		"profile":    {Key: key1, Profile: "../x"},
	} {
		if err := e.c.Ensure(ctx, spec); !isInvalid(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	before := len(eng.Created())
	for name, req := range map[string]core.ExecRequest{
		"empty argv": {},
		"dotdot cwd": {Argv: []string{"pwd"}, Cwd: "../ws_other"},
		"abs cwd":    {Argv: []string{"pwd"}, Cwd: "/etc"},
		"path env":   {Argv: []string{"env"}, Env: map[string]string{"PATH": "/tmp"}},
		"ld env":     {Argv: []string{"env"}, Env: map[string]string{"LD_PRELOAD": "x.so"}},
		"proxy env":  {Argv: []string{"env"}, Env: map[string]string{"HTTPS_PROXY": "http://evil"}},
		"big stdin":  {Argv: []string{"cat"}, Stdin: make([]byte, proto.MaxStdin+1)},
	} {
		if _, err := e.c.Exec(ctx, key1, req, nil); !isInvalid(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := e.c.Exec(ctx, "box_bad", core.ExecRequest{Argv: []string{"true"}}, nil); !isInvalid(err) {
		t.Errorf("bad key: %v", err)
	}
	if _, err := e.c.Stdio(ctx, key1, nil, nil); !isInvalid(err) {
		t.Errorf("stdio empty argv: %v", err)
	}
	if _, err := e.c.Stdio(ctx, key1, []string{"x"}, map[string]string{"NODE_OPTIONS": "--require x"}); !isInvalid(err) {
		t.Errorf("stdio env: %v", err)
	}
	if len(eng.Created()) != before {
		t.Fatal("an invalid request reached the engine")
	}
}

func isInvalid(err error) bool { return errors.Is(err, core.ErrInvalid) }

func TestEnsureRefusesSymlinksAndWrongEgress(t *testing.T) {
	e, _ := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(e.work, "ws"), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(e.work, "ws", string(ws1))); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Ensure(ctx, e.spec(key1)); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("symlinked workspace: %v", err)
	}
	e.eg.subpath = "egress/0123456789ab" // e.g. a fake with a shorter hash
	spec := e.spec(key2)
	spec.Workspace = ""
	if err := e.c.Ensure(ctx, spec); err == nil || !strings.Contains(err.Error(), "boxes mount") {
		t.Fatalf("egress subpath mismatch: %v", err)
	}
	if !slices.Contains(e.eg.closedKeys(), key2) {
		t.Fatal("mismatched egress socket left open")
	}
}

func TestExecRecreatesALostBox(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	name := "bothy_run-" + key1
	if !slices.Contains(eng.Running(), name) {
		t.Fatalf("no box container %s: %v", name, eng.Running())
	}
	eng.Vanish(name) // reaped or evicted by sandboxd
	res, err := e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"echo", "back"}}, nil)
	if err != nil || string(res.Stdout) != "back\n" {
		t.Fatalf("exec after loss = %+v, %v", res, err)
	}
	if n := len(eng.Created()); n != 3 { // canary, box, recreated box
		t.Fatalf("%d containers created", n)
	}
	if _, err := e.c.Exec(ctx, key2, core.ExecRequest{Argv: []string{"true"}}, nil); !strings.Contains(errString(err), "never ensured") {
		t.Fatalf("exec without Ensure: %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestExecTimeouts(t *testing.T) {
	var mu sync.Mutex
	var got []time.Duration
	e, _ := newEnv(t, opts{sandboxd: true, eng: func(f *enginetest.Fake) {
		f.Program = func(ctx context.Context, p *enginetest.Proc) int {
			mu.Lock()
			got = append(got, p.Timeout)
			mu.Unlock()
			return 0
		}
	}})
	ctx := context.Background()
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"x"}, Timeout: 2 * time.Hour}, nil); err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithTimeout(ctx, 16*time.Second)
	defer cancel()
	if _, err := e.c.Exec(dctx, key1, core.ExecRequest{Argv: []string{"x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got[0] != 120*time.Second || got[1] != maxExec || got[2] > 10*time.Second || got[2] < 9*time.Second {
		t.Fatalf("timeouts = %v; want config default, the 30m cap, and the caller deadline minus %v", got, execGrace)
	}
}

func TestExecContextCancellation(t *testing.T) {
	e, _ := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	// The caller's deadline is shorter than the exec grace: the box timeout
	// floors at 1s, so the HTTP call is cut by the deadline first.
	res, err := e.c.Exec(dctx, key1, core.ExecRequest{Argv: []string{"sleep"}}, nil)
	if err != nil || !res.TimedOut || res.Exit != 124 {
		t.Fatalf("deadline = %+v, %v", res, err)
	}
	// As the runner does on /stop: the abandoned process is killed.
	if err := e.c.Kill(ctx, key1); err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithCancelCause(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		ccancel(core.ErrStopped)
	}()
	res, err = e.c.Exec(cctx, key1, core.ExecRequest{Argv: []string{"sleep"}}, nil)
	if !res.Killed || !errors.Is(err, core.ErrStopped) {
		t.Fatalf("cancel = %+v, %v", res, err)
	}
	if err := e.c.Kill(ctx, key1); err != nil {
		t.Fatal(err)
	}
}

func TestStdio(t *testing.T) {
	e, _ := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	rwc, err := e.c.Stdio(ctx, key1, []string{"cat"}, map[string]string{"MCP_MODE": "stdio"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(rwc, `{"jsonrpc":"2.0","method":"ping"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(rwc).ReadString('\n')
	if err != nil || line != `{"jsonrpc":"2.0","method":"ping"}`+"\n" {
		t.Fatalf("stdio echo %q, %v", line, err)
	}
	_, err = e.c.Stdio(ctx, key1, []string{"cat"}, nil)
	wantErr(t, err, core.ErrBusy)
	// The open stream keeps the box from idle reaping.
	e.clock.Advance(time.Hour)
	e.c.svc.reapIdle(ctx)
	if slices.Contains(e.eg.closedKeys(), key1) {
		t.Fatal("box with an open stdio stream was reaped")
	}
	if err := rwc.Close(); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	e.c.svc.reapIdle(ctx)
	if !slices.Contains(e.eg.closedKeys(), key1) {
		t.Fatal("idle box not reaped after the stream closed")
	}
}

func TestStatusTransitions(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	var seen []core.SandboxStatus
	var smu sync.Mutex
	e.c.Watch(func(st core.SandboxStatus) {
		smu.Lock()
		seen = append(seen, st)
		smu.Unlock()
	})
	ctx := context.Background()
	eng.FailChecks = map[string][]string{"runc": {proto.CheckMounts}}
	sandboxdPost(t, e.socket, "/selftest")
	st := e.c.svc.refresh(ctx)
	if st.Available || len(isolationFailure(st)) != 1 {
		t.Fatalf("status after a failed self-test = %+v", st)
	}
	b := e.banners()
	if _, ok := b["no_sandbox"]; ok || b["sandbox_not_isolated"].Level != "error" {
		t.Fatalf("banners = %+v", b)
	}
	if acts := e.audit.Actions(); !slices.Contains(acts, "sandbox.selftest_failed") {
		t.Fatalf("audit = %v", acts)
	}
	f := findings(t, e.c, "sandbox.not_isolated")
	if len(f) != 1 || f[0].Severity != core.SevCritical || !f[0].Security {
		t.Fatalf("not_isolated findings = %+v", f)
	}
	if len(findings(t, e.c, "sandbox.unavailable")) != 0 {
		t.Fatal("unavailable reported alongside not_isolated")
	}
	_, err := e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"true"}}, nil)
	wantErr(t, err, core.ErrNoSandbox)

	eng.FailChecks = nil
	sandboxdPost(t, e.socket, "/selftest")
	if st := e.c.svc.refresh(ctx); !st.Available {
		t.Fatalf("status after recovery = %+v", st)
	}
	if len(e.banners()) != 0 {
		t.Fatalf("banners not cleared: %+v", e.banners())
	}
	smu.Lock()
	defer smu.Unlock()
	if len(seen) != 2 || seen[0].Available || !seen[1].Available {
		t.Fatalf("watchers saw %d changes", len(seen))
	}
	// Unchanged probes do not notify again.
	e.c.svc.refresh(ctx)
	if len(seen) != 2 {
		t.Fatal("watchers notified without a change")
	}
}

func TestMemberBanners(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	users := map[string]*core.User{"owner": {Role: core.RoleOwner}, "member": {Role: core.RoleMember},
		"gone": {Role: core.RoleMember, Disabled: true}}
	for name, u := range users {
		u.Username = name
		if err := st.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := newEnv(t, opts{cfg: func(c *config.Config) { c.Sandbox.Backend = "none" }})
	e.c.svc.store = st
	e.c.svc.syncMemberBanners(ctx)
	codes := func(p core.PrincipalID) []string {
		var out []string
		for _, ev := range e.bus.Banners(core.Filter{Principals: []core.PrincipalID{p}}) {
			out = append(out, ev.Data.(core.BannerData).Code)
		}
		return out
	}
	member := core.UserPrincipal(users["member"].ID)
	if got := codes(member); !slices.Equal(got, []string{"no_sandbox"}) {
		t.Fatalf("member banners = %v", got)
	}
	for _, name := range []string{"owner", "gone"} {
		if got := codes(core.UserPrincipal(users[name].ID)); len(got) != 0 {
			t.Fatalf("%s got per-principal banners %v (admins see the broadcast)", name, got)
		}
	}
	late := &core.User{Username: "late", Role: core.RoleMember}
	if err := st.Users().Create(ctx, late); err != nil {
		t.Fatal(err)
	}
	e.c.svc.syncMemberBanners(ctx)
	if got := codes(core.UserPrincipal(late.ID)); !slices.Equal(got, []string{"no_sandbox"}) {
		t.Fatalf("a user created later got %v", got)
	}
	e.c.svc.setStatus(core.SandboxStatus{Backend: "sandboxd", Available: true, Runtime: "runc"})
	e.c.svc.syncMemberBanners(ctx) // Run does this after every probe
	if got := codes(member); len(got) != 0 {
		t.Fatalf("member banner not cleared: %v", got)
	}
}

// New must not read the store: the skeleton's stub store (a nil embedded
// core.Store) would panic, and boot should not wait on the DB anyway.
func TestNewDoesNotReadTheStore(t *testing.T) {
	cfg := config.Default()
	cfg.Sandbox.Backend = "none"
	dir := shortTempDir(t)
	c, err := New(context.Background(), Deps{Config: config.Static(cfg), Egress: &egress{}, Store: panicStore{},
		Bus: events.New(events.Options{}), WorkDir: dir, RunDir: dir, Log: slog.New(slog.DiscardHandler)})
	if err != nil || c.Status(context.Background()).Backend != "none" {
		t.Fatalf("New = %v", err)
	}
}

type panicStore struct{ core.Store }

func TestIdleReapedBoxComesBackOnExec(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(e.cfg.Sandbox.IdleTimeout.D() + time.Second)
	e.c.svc.reapIdle(ctx)
	if len(eng.Running()) != 0 || !slices.Contains(e.eg.closedKeys(), key1) {
		t.Fatalf("idle box not reaped: running %v closed %v", eng.Running(), e.eg.closedKeys())
	}
	// The runner may Exec again without a new Ensure: the box, its
	// directories and its egress socket come back from the last spec.
	e.eg.mu.Lock()
	delete(e.eg.opened, key1)
	e.eg.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(e.run, proto.EgressSubpath(key1))); err != nil {
		t.Fatal(err)
	}
	res, err := e.c.Exec(ctx, key1, core.ExecRequest{Argv: []string{"echo", "back"}}, nil)
	if err != nil || string(res.Stdout) != "back\n" {
		t.Fatalf("exec after idle reap = %+v, %v", res, err)
	}
	e.eg.mu.Lock()
	reopened := e.eg.opened[key1]
	e.eg.mu.Unlock()
	if reopened.Session != "ses_x" || len(eng.Running()) != 1 {
		t.Fatalf("egress not reopened (%+v) or box not recreated (%v)", reopened, eng.Running())
	}
	if _, err := os.Stat(filepath.Join(e.run, proto.EgressSubpath(key1))); err != nil {
		t.Fatalf("egress directory not recreated: %v", err)
	}
	// A reaped box's spec is forgotten after a day of disuse.
	e.clock.Advance(e.cfg.Sandbox.IdleTimeout.D() + time.Second)
	e.c.svc.reapIdle(ctx)
	e.clock.Advance(specRetention + time.Second)
	e.c.svc.reapIdle(ctx)
	e.c.svc.mu.Lock()
	_, kept := e.c.svc.boxes[key1]
	e.c.svc.mu.Unlock()
	if kept {
		t.Fatal("spec kept past its retention")
	}
}

func TestReaperSparesABoxThatTurnedBusy(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(e.cfg.Sandbox.IdleTimeout.D() + time.Second)
	// An exec begins after the reaper listed the box as idle.
	release := e.c.svc.begin(key1)
	e.c.svc.reapOne(ctx, key1, e.cfg.Sandbox.IdleTimeout.D())
	release()
	if len(eng.Running()) != 1 || slices.Contains(e.eg.closedKeys(), key1) {
		t.Fatalf("busy box reaped: running %v closed %v", eng.Running(), e.eg.closedKeys())
	}
}

func TestEnsureFailureLeavesTheSocketToTheReaper(t *testing.T) {
	e, _ := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	spec := e.spec(key1)
	spec.Profile = "gpu" // valid syntax, unknown to sandboxd
	if err := e.c.Ensure(ctx, spec); !isInvalid(err) {
		t.Fatalf("Ensure = %v", err)
	}
	e.clock.Advance(e.cfg.Sandbox.IdleTimeout.D() + time.Second)
	e.c.svc.reapIdle(ctx)
	if !slices.Contains(e.eg.closedKeys(), key1) {
		t.Fatal("the egress socket of a box that was never created stays open")
	}
}

func TestPartialLimitsTakeConfigDefaults(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	spec := e.spec(key1)
	spec.Limits = core.BoxLimits{MemoryMB: 1024}
	if err := e.c.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	hc := eng.Created()[len(eng.Created())-1].HostConfig
	if hc.Memory != 1024<<20 || hc.NanoCPUs != 1e9 || *hc.PidsLimit != 256 {
		t.Fatalf("limits: mem %d cpu %d pids %d", hc.Memory, hc.NanoCPUs, *hc.PidsLimit)
	}
}

func TestCanaryKeyIsReserved(t *testing.T) {
	e, _ := newEnv(t, opts{sandboxd: true})
	if err := e.c.Ensure(context.Background(), e.spec(canaryKey)); !isInvalid(err) {
		t.Fatalf("Ensure(canary) = %v", err)
	}
}

// workingComposeFix: deploy/compose.yaml keeps digest placeholders and an
// install.sh deployment has no deploy/ directory, so a fix pointing at it
// fails everywhere; the README / install.sh compose flow works.
func workingComposeFix(fix string) bool {
	return !strings.Contains(fix, "deploy/compose.yaml") && strings.Contains(fix, "install.sh --compose")
}

func TestSandboxdUnreachable(t *testing.T) {
	e, _ := newEnv(t, opts{})
	st := e.c.Status(context.Background())
	if st.Available || st.Backend != "sandboxd" || !strings.Contains(st.Reason, "not reachable") ||
		strings.Contains(st.Fix, "deploy/compose.yaml") || !strings.Contains(st.Fix, "docker compose logs sandboxd") {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(e.banners()["no_sandbox"].Text, "not reachable") {
		t.Fatalf("banners = %+v", e.banners())
	}
	f := findings(t, e.c, "sandbox.unavailable")
	if len(f) != 1 || f[0].Severity != core.SevMedium || !f[0].Security {
		t.Fatalf("findings = %+v", f)
	}
}

func TestAutoSwitchesToSandboxd(t *testing.T) {
	e, _ := newEnv(t, opts{cfg: func(c *config.Config) { c.Sandbox.Backend = "auto" }})
	if name := e.c.svc.be.name(); name != "none" {
		t.Skipf("auto picked %s on this host", name)
	}
	e.c.svc.healthEvery, e.c.svc.retryEvery = 20*time.Millisecond, 20*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.c.Run(ctx) }()
	startSandboxd(t, e.socket, nil)
	waitFor(t, "the sandboxd backend", func() bool { return e.c.Status(ctx).Available })
	if e.c.Status(ctx).Backend != "sandboxd" {
		t.Fatalf("status = %+v", e.c.Status(ctx))
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReconcileAndReap(t *testing.T) {
	e, eng := newEnv(t, opts{sandboxd: true})
	ctx := context.Background()
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatal(err)
	}
	// A new gateway process finds the box and adopts it.
	c2, err := New(ctx, Deps{Config: config.Static(e.cfg), Egress: e.eg, Bus: e.bus, Audit: e.audit,
		WorkDir: e.work, RunDir: e.run, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	c2.svc.reconcile(ctx)
	c2.svc.mu.Lock()
	_, adopted := c2.svc.boxes[key1]
	c2.svc.mu.Unlock()
	if !adopted {
		t.Fatal("box not adopted")
	}
	e.clock.Advance(e.cfg.Sandbox.IdleTimeout.D() - time.Second)
	c2.svc.reapIdle(ctx)
	if len(eng.Running()) != 1 {
		t.Fatal("box reaped before its idle timeout")
	}
	e.clock.Advance(24 * time.Hour)
	c2.svc.reapIdle(ctx)
	if len(eng.Running()) != 0 || !slices.Contains(e.eg.closedKeys(), key1) {
		t.Fatalf("idle box not reaped: running %v closed %v", eng.Running(), e.eg.closedKeys())
	}
}

func TestUsage(t *testing.T) {
	e, _ := newEnv(t, opts{cfg: func(c *config.Config) { c.Sandbox.Backend = "none"; c.Sandbox.WorkspaceQuotaMB = 3 }})
	ctx := context.Background()
	dir := filepath.Join(e.work, "ws", string(ws1))
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), make([]byte, 1000), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b"), make([]byte, 24), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	used, quota, err := e.c.Usage(ctx, ws1)
	if err != nil || used != 1024 || quota != 3<<20 {
		t.Fatalf("Usage = %d, %d, %v", used, quota, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c"), make([]byte, 1024), 0o660); err != nil {
		t.Fatal(err)
	}
	if used, _, _ := e.c.Usage(ctx, ws1); used != 1024 {
		t.Fatal("Usage must come from the scan, not a fresh walk per call")
	}
	e.c.svc.scanQuota()
	if used, _, _ := e.c.Usage(ctx, ws1); used != 2048 {
		t.Fatalf("after scan: %d", used)
	}
	other := core.WorkspaceID(core.DerivedID(core.PrefixWorkspace, "other"))
	if used, _, err := e.c.Usage(ctx, other); err != nil || used != 0 {
		t.Fatalf("missing workspace = %d, %v", used, err)
	}
	if _, _, err := e.c.Usage(ctx, "ws_../../data"); !isInvalid(err) {
		t.Fatalf("invalid workspace: %v", err)
	}
}

func findings(t *testing.T, c *Client, id string) []core.Finding {
	t.Helper()
	for _, ch := range c.Checks() {
		if ch.ID() == id {
			if err := ch.Fix(context.Background(), core.Finding{}); !errors.Is(err, core.ErrNotImplemented) {
				t.Errorf("%s: Fix = %v", id, err)
			}
			return ch.Run(context.Background())
		}
	}
	t.Fatalf("no check %s", id)
	return nil
}

func TestChecks(t *testing.T) {
	e, _ := newEnv(t, opts{cfg: func(c *config.Config) { c.Sandbox.Backend = "none" }})
	ids := []string{}
	for _, ch := range e.c.Checks() {
		ids = append(ids, ch.ID())
	}
	if !slices.Equal(ids, []string{"sandbox.unavailable", "sandbox.not_isolated", "sandbox.rootful_runc", "sandbox.runsc_host_uds",
		"sandbox.bwrap_limits", "docker.socket_access"}) {
		t.Fatalf("check ids = %v", ids)
	}
	set := func(st core.SandboxStatus) { e.c.svc.setStatus(st) }

	set(core.SandboxStatus{Backend: "sandboxd", Available: true, Runtime: "runc"})
	if f := findings(t, e.c, "sandbox.rootful_runc"); len(f) != 1 || f[0].Severity != core.SevLow || !strings.Contains(f[0].Fix, "--host-uds=open") {
		t.Fatalf("rootful_runc = %+v", f)
	}
	set(core.SandboxStatus{Backend: "sandboxd", Available: true, Runtime: "runc", Rootless: true})
	if len(findings(t, e.c, "sandbox.rootful_runc")) != 0 {
		t.Fatal("rootful_runc on rootless docker")
	}
	set(core.SandboxStatus{Backend: "sandboxd", Available: true, Runtime: "runsc"})
	if len(findings(t, e.c, "sandbox.rootful_runc")) != 0 || len(findings(t, e.c, "sandbox.unavailable")) != 0 {
		t.Fatal("findings on a healthy runsc sandbox")
	}

	if len(findings(t, e.c, "sandbox.bwrap_limits")) != 0 {
		t.Fatal("bwrap_limits outside bwrap")
	}
	set(core.SandboxStatus{Backend: "bwrap", Available: true, Runtime: "bwrap"})
	if f := findings(t, e.c, "sandbox.bwrap_limits"); len(f) != 1 || f[0].Severity != core.SevLow || !f[0].Security || !workingComposeFix(f[0].Fix) {
		t.Fatalf("bwrap_limits = %+v", f)
	}

	set(core.SandboxStatus{Backend: "sandboxd", Available: true, Runtime: "runc", Rootless: true,
		SelfTest: []core.SelfTestCheck{{Name: proto.CheckRunsc, Detail: "egress_relay failed"}}})
	if f := findings(t, e.c, "sandbox.runsc_host_uds"); len(f) != 1 || f[0].Security || f[0].Severity != core.SevLow {
		t.Fatalf("runsc_host_uds = %+v", f)
	}

	set(core.SandboxStatus{Backend: "sandboxd", Reason: "cannot access /var/run/docker.sock (owner uid 0 gid 998, mode 0660)",
		Fix:      "Set DOCKER_GID=998 in the .env next to compose.yaml and run: docker compose up -d",
		SelfTest: []core.SelfTestCheck{{Name: proto.CheckEngine, Detail: "cannot access /var/run/docker.sock (owner uid 0 gid 998, mode 0660)"}}})
	f := findings(t, e.c, "docker.socket_access")
	if len(f) != 1 || f[0].Fix != "Set DOCKER_GID=998 in the .env next to compose.yaml and run: docker compose up -d" || f[0].Severity != core.SevMedium {
		t.Fatalf("socket_access = %+v", f)
	}
	if len(findings(t, e.c, "sandbox.unavailable")) != 1 {
		t.Fatal("unavailable not reported")
	}
}

func TestMkdirGroupRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	if err := mkdirGroup(root, filepath.Join(root, "..", "x"), 0o770, noGroup, true); err == nil {
		t.Fatal("created a directory outside root")
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mkdirGroup(root, filepath.Join(root, "file", "x"), 0o770, noGroup, true); err == nil {
		t.Fatal("descended through a file")
	}
	if err := mkdirGroup(root, filepath.Join(root, "a", "b"), sharedDir, noGroup, true); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(root, "a"))
	if fi.Mode()&fs.ModeSetgid == 0 || fi.Mode().Perm() != 0o770 {
		t.Fatalf("parent mode %v", fi.Mode())
	}
	existing := filepath.Join(root, "skills")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mkdirGroup(root, existing, sharedDir, noGroup, false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o700 {
		t.Fatalf("an existing directory was changed to %v", fi.Mode())
	}
	if err := mkdirGroup(root, existing, sharedDir, noGroup, true); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o770 {
		t.Fatalf("fixExisting left %v", fi.Mode())
	}
}
