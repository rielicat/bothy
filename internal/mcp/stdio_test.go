package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/skills/treehash"
)

const (
	npmIntegrity  = "sha512-dGVzdC1pbnRlZ3JpdHk="
	pypiIntegrity = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func notesCfg() config.MCPServer {
	return config.MCPServer{Name: "notes", Transport: "stdio",
		Package: &config.MCPPackage{Ecosystem: "npm", Name: "@acme/mcp-notes", Version: "1.2.3", Integrity: npmIntegrity},
		Command: []string{"node", "node_modules/@acme/mcp-notes/index.js", "/workspace/notes"},
		Egress:  []string{"api.notes.example"}, Workspace: "principal",
		SideEffects: map[string]string{"default": "local_write", "search": "read"}}
}

// installer fakes npm and uv inside the installer box by writing what they
// would write into the mounted install tree.
type installer struct {
	h         *harness
	integrity string
	gate      chan struct{}

	mu     sync.Mutex
	argvs  [][]string
	ciLock string // when set, "npm ci" rewrites package-lock.json with it
}

func (in *installer) exec(ctx context.Context, key string, req core.ExecRequest) (*core.ExecResult, error) {
	in.mu.Lock()
	in.argvs = append(in.argvs, slices.Clone(req.Argv))
	gate, ciLock := in.gate, in.ciLock
	in.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var tree string
	for _, s := range slices.Backward(in.h.sb.Ensured) {
		if s.Key == key {
			tree = filepath.Join(in.h.work, filepath.FromSlash(s.MCPTree))
			break
		}
	}
	write := func(rel, content string) error {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(content), 0o660)
	}
	var err error
	switch strings.Join(req.Argv[:2], " ") {
	case "npm install":
		lock := fmt.Sprintf(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@acme/mcp-notes":"1.2.3"}},`+
			`"node_modules/@acme/mcp-notes":{"version":"1.2.3","integrity":%q}}}`, in.integrity)
		err = errors.Join(write("package-lock.json", lock), write("node_modules/@acme/mcp-notes/index.js", "server()"))
	case "npm ci":
		if !exists(filepath.Join(tree, "package-lock.json")) {
			return &core.ExecResult{Exit: 1, Stderr: []byte("npm ci needs a lockfile")}, nil
		}
		err = write("node_modules/@acme/mcp-notes/index.js", "server()")
		if ciLock != "" {
			err = errors.Join(err, write("package-lock.json", ciLock))
		}
	case "uv pip":
		switch req.Argv[2] {
		case "compile":
			err = write("lock.txt", "# generated\nmcp-notes==0.4.0 \\\n    --hash="+in.integrity+"\nanyio==4.0.0 \\\n    --hash=sha256:aa\n")
		case "install":
			err = write("mcp_notes/__init__.py", "")
		}
	default:
		return &core.ExecResult{Exit: 127, Stderr: []byte("unexpected command")}, nil
	}
	return &core.ExecResult{}, err
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (in *installer) commands() [][]string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.argvs)
}

type stdioCall struct {
	key  string
	argv []string
	env  map[string]string
}

// stdioRec serves the fake server over pipes and records each Stdio call.
type stdioRec struct {
	f    *fakeServer
	t    *testing.T
	mu   sync.Mutex
	call []stdioCall
	ends []net.Conn
	fail bool
}

func (r *stdioRec) stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error) {
	r.mu.Lock()
	r.call = append(r.call, stdioCall{key, slices.Clone(argv), env})
	fail := r.fail
	r.mu.Unlock()
	if fail {
		return nil, errors.New("box exited")
	}
	client, srv := net.Pipe()
	sctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	go func() { _ = r.f.server().Run(sctx, &mcpsdk.IOTransport{Reader: srv, Writer: srv}) }()
	r.mu.Lock()
	r.ends = append(r.ends, srv)
	r.mu.Unlock()
	return client, nil
}

func (r *stdioRec) calls() []stdioCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.call)
}

func (r *stdioRec) setFail(v bool) {
	r.mu.Lock()
	r.fail = v
	r.mu.Unlock()
}

// killLast closes the server end of the newest stdio stream, as if the
// server process died.
func (r *stdioRec) killLast() {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.ends[len(r.ends)-1].Close()
}

func newStdioHarness(t *testing.T, cfg config.MCPServer, opts ...option) (*harness, *installer, *stdioRec) {
	h := newHarness(t, []config.MCPServer{cfg}, opts...)
	in := &installer{h: h, integrity: cfg.Package.Integrity}
	rec := &stdioRec{f: newFakeServer(), t: t}
	h.sb.ExecFunc = in.exec
	h.sb.StdioFunc = rec.stdio
	return h, in, rec
}

func (h *harness) install(name string) {
	h.t.Helper()
	if err := h.m.Install(context.Background(), name, admin); err != nil {
		h.t.Fatalf("Install: %v", err)
	}
	h.waitFor("install", func() bool { st := h.status(name).State; return st != stInstalling && st != stConnecting })
}

func (h *harness) ensuredFor(key string) []core.BoxSpec {
	var out []core.BoxSpec
	for _, s := range h.sb.Ensured {
		if s.Key == key {
			out = append(out, s)
		}
	}
	return out
}

func TestStdioInstallDiscoverAndCall(t *testing.T) {
	h, in, rec := newStdioHarness(t, notesCfg())
	h.start()
	if st := h.status("notes"); st.State != stError || !strings.Contains(st.Err, "Install") {
		t.Fatalf("status before install = %+v", st)
	}

	in.gate = make(chan struct{})
	if err := h.m.Install(context.Background(), "notes", admin); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Install(context.Background(), "notes", admin); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("concurrent Install = %v, want ErrConflict", err)
	}
	if st := h.status("notes"); st.State != stInstalling {
		t.Fatalf("state during install = %s", st.State)
	}
	close(in.gate)
	h.waitFor("install", func() bool { st := h.status("notes").State; return st != stInstalling && st != stConnecting })
	if st := h.status("notes"); st.State != stPinsPending || st.Pending != 2 || st.Transport != "stdio" || st.Protocol != "2026-07-28" {
		t.Fatalf("status after install = %+v", st)
	}

	inst := h.ensuredFor(installBox("notes"))
	if len(inst) != 1 {
		t.Fatalf("installer box specs = %+v", inst)
	}
	ispec := inst[0]
	if !ispec.MCPTreeRW || ispec.Workspace != "" || ispec.Profile != core.ProfileNode || !ispec.Egress.NoGrants ||
		!slices.Equal(ispec.Egress.Extra, []string{"registry.npmjs.org:443"}) || !strings.HasPrefix(ispec.MCPTree, "mcp/notes/in_") {
		t.Fatalf("installer spec = %+v", ispec)
	}
	want := []string{"npm", "install", "--ignore-scripts", "--no-audit", "--no-fund", "--save-exact", "--prefix", "/opt/mcp", "@acme/mcp-notes@1.2.3"}
	if cmds := in.commands(); len(cmds) != 1 || !slices.Equal(cmds[0], want) {
		t.Fatalf("installer commands = %q", cmds)
	}
	rec1, err := h.store.MCP().Install(context.Background(), "notes")
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(h.work, filepath.FromSlash(ispec.MCPTree))
	sum, _ := treehash.Sum(tree)
	if rec1.Status != "ready" || rec1.TreeHash != sum || rec1.LockHash != sha256hex(rec1.Lock) || !strings.Contains(string(rec1.Lock), npmIntegrity) {
		t.Fatalf("install record = %+v", rec1)
	}
	if !slices.Contains(h.sb.Destroyed, installBox("notes")) || !slices.Contains(h.sb.Destroyed, discoverBox("notes")) {
		t.Fatalf("destroyed = %v, want installer and discovery boxes", h.sb.Destroyed)
	}
	if got := h.auditActions("mcp.install"); len(got) != 1 || got[0].Outcome != "ok" {
		t.Fatalf("mcp.install audit = %+v", got)
	}
	disc := h.ensuredFor(discoverBox("notes"))
	if len(disc) != 1 || disc[0].Workspace != "" || disc[0].MCPTreeRW || disc[0].MCPTree != ispec.MCPTree ||
		!slices.Equal(disc[0].Egress.Extra, []string{"api.notes.example:443"}) || !disc[0].Egress.NoGrants {
		t.Fatalf("discovery spec = %+v", disc)
	}
	calls := rec.calls()
	wantArgv := []string{"/bin/sh", "-c", `cd /opt/mcp && exec "$@"`, "mcp-notes", "node", "node_modules/@acme/mcp-notes/index.js", "/workspace/notes"}
	if len(calls) != 1 || calls[0].key != discoverBox("notes") || !slices.Equal(calls[0].argv, wantArgv) || len(calls[0].env) != 0 {
		t.Fatalf("stdio calls = %+v", calls)
	}

	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	tool := h.tool("mcp__notes__search")
	if tool.Spec().Manifest.SideEffect != core.SERead {
		t.Fatalf("class = %s, want the suggested read", tool.Spec().Manifest.SideEffect)
	}
	alice := userEnv(newUser("alice"))
	res, err := h.call(tool, alice, `{"q":"todo"}`)
	if err != nil || res.Content != "results for todo" || !res.Untrusted {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	box := runtimeBox("notes", alice.Principal.ID)
	rt := h.ensuredFor(box)
	if len(rt) != 1 || rt[0].Workspace != alice.Principal.Workspace || rt[0].MCPTreeRW || rt[0].MCPTree != ispec.MCPTree ||
		rt[0].Egress.Principal != alice.Principal.ID || !rt[0].Egress.NoGrants || !slices.Equal(rt[0].Egress.Extra, []string{"api.notes.example:443"}) {
		t.Fatalf("runtime spec = %+v", rt)
	}
	if _, err := h.call(tool, alice, `{"q":"again"}`); err != nil {
		t.Fatal(err)
	}
	bob := userEnv(newUser("bob"))
	if _, err := h.call(tool, bob, `{"q":"b"}`); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.calls()); n != 3 {
		t.Fatalf("%d stdio streams, want discovery + one per principal", n)
	}
	for _, c := range rec.calls() {
		if len(c.env) != 0 {
			t.Fatalf("stdio env = %v; stdio servers get no environment", c.env)
		}
	}

	h.m.reapIdle(h.clock.Now().Add(9 * time.Minute))
	if slices.Contains(h.sb.Destroyed, box) {
		t.Fatal("instance reaped before the idle timeout")
	}
	h.m.reapIdle(h.clock.Now().Add(11 * time.Minute))
	if !slices.Contains(h.sb.Destroyed, box) || !slices.Contains(h.sb.Destroyed, runtimeBox("notes", bob.Principal.ID)) {
		t.Fatalf("idle instances not stopped: destroyed %v", h.sb.Destroyed)
	}
	if _, err := h.call(tool, alice, `{"q":"restart"}`); err != nil {
		t.Fatalf("restart after idle stop: %v", err)
	}

	h.install("notes")
	cmds := in.commands()
	if last := cmds[len(cmds)-1]; !slices.Equal(last, []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--prefix", "/opt/mcp"}) {
		t.Fatalf("reinstall command = %q, want npm ci from the recorded lock", last)
	}
	rec2, _ := h.store.MCP().Install(context.Background(), "notes")
	if rec2.InstallID == rec1.InstallID || rec2.LockHash != rec1.LockHash || rec2.Status != "ready" {
		t.Fatalf("reinstall record = %+v (first %+v)", rec2, rec1)
	}
	if _, err := os.Stat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old install tree not removed")
	}
	pj, _ := os.ReadFile(filepath.Join(h.work, "mcp", "notes", rec2.InstallID, "package.json"))
	if !strings.Contains(string(pj), `"@acme/mcp-notes": "1.2.3"`) {
		t.Fatalf("package.json for npm ci = %s", pj)
	}
	h.waitFor("ready", func() bool { return h.status("notes").State == stPinsPending })
	if _, err := h.call(h.tool("mcp__notes__search"), alice, `{"q":"new tree"}`); err != nil {
		t.Fatal(err)
	}

	h.sb.SetStatus(core.SandboxStatus{Backend: "none"})
	if st := h.status("notes"); st.State != stNoSandbox {
		t.Fatalf("state without sandbox = %+v", st)
	}
	if h.tool("mcp__notes__search").(core.Gated).Enabled(alice) {
		t.Fatal("stdio tool offered without a sandbox")
	}
	if _, err := h.call(h.tool("mcp__notes__search"), alice, `{"q":"x"}`); !errors.Is(err, core.ErrNoSandbox) {
		t.Fatalf("Exec without sandbox = %v", err)
	}
}

func TestStdioRefusedWithoutSandbox(t *testing.T) {
	h, in, rec := newStdioHarness(t, notesCfg(), noSandbox)
	h.start()
	if st := h.status("notes"); st.State != stNoSandbox {
		t.Fatalf("status = %+v", st)
	}
	if err := h.m.Install(context.Background(), "notes", admin); !errors.Is(err, core.ErrNoSandbox) {
		t.Fatalf("Install without sandbox = %v", err)
	}
	if len(rec.calls()) != 0 || len(in.commands()) != 0 || len(h.sb.Ensured) != 0 {
		t.Fatal("sandbox used without a sandbox")
	}
	h.sb.SetStatus(core.SandboxStatus{Backend: "fake", Available: true})
	h.waitFor("rediscovery", func() bool { return h.status("notes").State == stError })
	h.install("notes")
	if st := h.status("notes"); st.State != stPinsPending {
		t.Fatalf("status once the sandbox is back = %+v", st)
	}
}

func TestStdioTreeDrift(t *testing.T) {
	h, _, _ := newStdioHarness(t, notesCfg())
	h.start()
	h.install("notes")
	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	alice := userEnv(newUser("alice"))
	if _, err := h.call(h.tool("mcp__notes__search"), alice, `{"q":"x"}`); err != nil {
		t.Fatal(err)
	}
	if f := h.m.Checks()[1].Run(context.Background()); len(f) != 0 {
		t.Fatalf("drift finding on an intact tree: %+v", f)
	}
	in, _ := h.store.MCP().Install(context.Background(), "notes")
	evil := filepath.Join(h.work, "mcp", "notes", in.InstallID, "node_modules", "@acme", "mcp-notes", "index.js")
	if err := os.WriteFile(evil, []byte("exfiltrate()"), 0o660); err != nil {
		t.Fatal(err)
	}
	findings := h.m.Checks()[1].Run(context.Background())
	if len(findings) != 1 || findings[0].ID != "mcp.drifted" || !findings[0].AutoFix || !strings.Contains(findings[0].Detail, "notes") {
		t.Fatalf("drift findings = %+v", findings)
	}

	if err := h.m.Reload(context.Background(), "notes"); err != nil {
		t.Fatal(err)
	}
	h.waitFor("disabled", func() bool { return h.status("notes").State == stDisabled })
	if st := h.status("notes"); !strings.Contains(st.Err, "drifted") || st.Tools != 0 {
		t.Fatalf("status = %+v", st)
	}
	if got, _ := h.store.MCP().Install(context.Background(), "notes"); got.Status != "drifted" {
		t.Fatalf("install status = %s", got.Status)
	}
	if _, ok := h.reg.Get("mcp__notes__search"); ok {
		t.Fatal("tools of a drifted server still registered")
	}
	banners := h.bus.Banners(core.Filter{Admin: true})
	if !slices.ContainsFunc(banners, func(e core.Event) bool { return e.Data.(core.BannerData).Code == "mcp_drifted" }) {
		t.Fatalf("banners = %+v", banners)
	}
	changed := h.auditActions("mcp.changed")
	if len(changed) != 1 || !strings.Contains(string(changed[0].Detail), "tree_drift") {
		t.Fatalf("mcp.changed audit = %+v", changed)
	}

	h.install("notes")
	if st := h.status("notes"); st.State != stPinsPending || st.Tools != 1 {
		t.Fatalf("status after reinstall = %+v", st)
	}
	if b := h.bus.Banners(core.Filter{Admin: true}); slices.ContainsFunc(b, func(e core.Event) bool { return e.Data.(core.BannerData).Code == "mcp_drifted" }) {
		t.Fatal("drift banner not cleared by a reinstall")
	}
}

func TestDriftCheckFixDisables(t *testing.T) {
	h, _, _ := newStdioHarness(t, notesCfg())
	h.start()
	h.install("notes")
	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	in, _ := h.store.MCP().Install(context.Background(), "notes")
	if err := os.WriteFile(filepath.Join(h.work, "mcp", "notes", in.InstallID, "extra.js"), nil, 0o660); err != nil {
		t.Fatal(err)
	}
	check := h.m.Checks()[1]
	findings := check.Run(context.Background())
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if err := check.Fix(context.Background(), findings[0]); err != nil {
		t.Fatal(err)
	}
	if st := h.status("notes"); st.State != stDisabled {
		t.Fatalf("status after Fix = %+v", st)
	}
	if _, ok := h.reg.Get("mcp__notes__search"); ok {
		t.Fatal("tool still registered")
	}
	if err := h.m.Checks()[0].Fix(context.Background(), core.Finding{ID: pendingID}); !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("pending pins Fix = %v", err)
	}
}

func TestStdioIntegrityMismatchFailsInstall(t *testing.T) {
	h, in, rec := newStdioHarness(t, notesCfg())
	in.integrity = "sha512-c29tZXRoaW5nLWVsc2U="
	h.start()
	h.install("notes")
	st := h.status("notes")
	if st.State != stError || !strings.Contains(st.Err, "install failed") || !strings.Contains(st.Err, "integrity") {
		t.Fatalf("status = %+v", st)
	}
	got, _ := h.store.MCP().Install(context.Background(), "notes")
	if got.Status != "failed" || got.LockHash != "" {
		t.Fatalf("install record = %+v", got)
	}
	if a := h.auditActions("mcp.install"); len(a) != 1 || a[0].Outcome != "error" {
		t.Fatalf("audit = %+v", a)
	}
	if len(rec.calls()) != 0 {
		t.Fatal("a failed install was started")
	}
	if f := h.m.Checks()[0].Run(context.Background()); len(f) != 0 {
		t.Fatalf("pending findings = %+v", f)
	}
}

func TestPyPIInstall(t *testing.T) {
	cfg := notesCfg()
	cfg.Package = &config.MCPPackage{Ecosystem: "pypi", Name: "mcp_notes", Version: "0.4.0", Integrity: pypiIntegrity}
	cfg.Command = []string{"python3", "-m", "mcp_notes"}
	h, in, rec := newStdioHarness(t, cfg)
	h.start()
	h.install("notes")
	if st := h.status("notes"); st.State != stPinsPending {
		t.Fatalf("status = %+v", st)
	}
	cmds := in.commands()
	if len(cmds) != 2 || cmds[0][2] != "compile" || !slices.Contains(cmds[0], "--generate-hashes") ||
		!slices.Equal(cmds[1], []string{"uv", "pip", "install", "--python", "python3", "--no-deps", "--require-hashes", "-r", "/opt/mcp/lock.txt", "--target", "/opt/mcp"}) {
		t.Fatalf("commands = %q", cmds)
	}
	if spec := h.ensuredFor(installBox("notes"))[0]; spec.Profile != core.ProfileBase ||
		!slices.Equal(spec.Egress.Extra, []string{"pypi.org:443", "files.pythonhosted.org:443"}) {
		t.Fatalf("installer spec = %+v", spec)
	}
	if argv := rec.calls()[0].argv; argv[2] != `cd /opt/mcp && export PYTHONPATH=/opt/mcp && exec "$@"` || argv[4] != "python3" {
		t.Fatalf("stdio argv = %q", argv)
	}

	h.install("notes")
	if cmds := in.commands(); len(cmds) != 3 || cmds[2][2] != "install" {
		t.Fatalf("reinstall should install from the recorded lock without compiling: %q", cmds)
	}
}

func TestStdioBackoffAndCrashRestart(t *testing.T) {
	h, _, rec := newStdioHarness(t, notesCfg())
	h.start()
	h.install("notes")
	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	tool := h.tool("mcp__notes__search")
	alice := userEnv(newUser("alice"))

	rec.setFail(true)
	if _, err := h.call(tool, alice, `{"q":"1"}`); err == nil {
		t.Fatal("Exec succeeded with a failing box")
	}
	n := len(rec.calls())
	if _, err := h.call(tool, alice, `{"q":"2"}`); err == nil || !strings.Contains(err.Error(), "restarting") {
		t.Fatalf("Exec during backoff = %v", err)
	}
	if len(rec.calls()) != n {
		t.Fatal("restart attempted during backoff")
	}
	h.clock.Advance(1100 * time.Millisecond)
	if _, err := h.call(tool, alice, `{"q":"3"}`); err == nil || len(rec.calls()) != n+1 {
		t.Fatalf("retry after backoff: %v, calls %d", err, len(rec.calls()))
	}
	h.clock.Advance(1100 * time.Millisecond)
	if _, err := h.call(tool, alice, `{"q":"4"}`); err == nil || !strings.Contains(err.Error(), "restarting") {
		t.Fatalf("backoff did not grow: %v", err)
	}
	rec.setFail(false)
	h.clock.Advance(time.Second)
	if res, err := h.call(tool, alice, `{"q":"5"}`); err != nil || res.Content != "results for 5" {
		t.Fatalf("Exec after recovery = %+v, %v", res, err)
	}

	rec.killLast()
	h.waitFor("crash noticed", func() bool {
		_, err := h.call(tool, alice, `{"q":"6"}`)
		return err != nil && strings.Contains(err.Error(), "restarting")
	})
	h.clock.Advance(2 * time.Second)
	if res, err := h.call(tool, alice, `{"q":"7"}`); err != nil || res.Content != "results for 7" {
		t.Fatalf("Exec after crash restart = %+v, %v", res, err)
	}
	if got := backoff(1); got != time.Second {
		t.Fatalf("backoff(1) = %s", got)
	}
	if got := backoff(30); got != time.Minute {
		t.Fatalf("backoff(30) = %s", got)
	}
}

func TestCloseDestroysBoxes(t *testing.T) {
	h, _, _ := newStdioHarness(t, notesCfg())
	h.start()
	h.install("notes")
	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	alice := userEnv(newUser("alice"))
	if _, err := h.call(h.tool("mcp__notes__search"), alice, `{"q":"x"}`); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.sb.Destroyed, runtimeBox("notes", alice.Principal.ID)) {
		t.Fatalf("Close left the instance box: %v", h.sb.Destroyed)
	}
	a, _ := h.tool("mcp__notes__search").Plan(context.Background(), alice, json.RawMessage(`{}`))
	if _, err := h.tool("mcp__notes__search").Exec(context.Background(), alice, a); err == nil {
		t.Fatal("Exec after Close succeeded")
	}
}

// A server that only speaks an older spec revision works over stdio too.
func TestStdioOldSpecEra(t *testing.T) {
	h, _, rec := newStdioHarness(t, notesCfg())
	rec.f.versions = []string{"2025-03-26"}
	h.start()
	h.install("notes")
	if st := h.status("notes"); st.State != stPinsPending || st.Protocol != "2025-03-26" {
		t.Fatalf("status = %+v", st)
	}
	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	if res, err := h.call(h.tool("mcp__notes__search"), userEnv(newUser("old")), `{"q":"era"}`); err != nil || res.Content != "results for era" {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
}
