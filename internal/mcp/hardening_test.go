package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

// A run can hold a tool across an approval wait. If an admin disables the
// tool meanwhile, the call must not reach the server.
func TestDisabledToolRefusedAtExec(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("docs", ts.URL)}, withClient(ts.Client()))
	h.start()
	h.approve("docs", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	tool := h.tool("mcp__docs__search")
	env := userEnv(newUser("alice"))
	a, err := tool.Plan(context.Background(), env, json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatal(err)
	}

	h.approve("docs", map[string]core.MCPToolApproval{"search": {Disable: true}})
	before := f.requests.Load()
	res, err := tool.Exec(context.Background(), env, a)
	if err != nil || res == nil || !res.IsError || strings.Contains(res.Content, "results for") {
		t.Fatalf("Exec of a disabled tool = %+v, %v", res, err)
	}
	if n := f.requests.Load() - before; n != 0 {
		t.Fatalf("%d requests reached the server for a disabled tool", n)
	}
	if tool.(core.Gated).Enabled(env) {
		t.Fatal("disabled tool still reports Enabled")
	}
}

// Disabling a server in config stops tools a run already holds.
func TestConfigDisabledServerRefusesHeldTool(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("docs", ts.URL)}, withClient(ts.Client()))
	h.start()
	h.approve("docs", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	tool := h.tool("mcp__docs__search")
	env := userEnv(newUser("alice"))
	a, err := tool.Plan(context.Background(), env, json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatal(err)
	}

	off := false
	srv := httpServerCfg("docs", ts.URL)
	srv.Enabled = &off
	cur := *h.cfg
	cur.MCP.Servers = []config.MCPServer{srv}
	h.m.onConfig(h.cfg, &cur)
	h.waitFor("disabled", func() bool { return h.status("docs").State == stDisabled })
	before := f.requests.Load()
	if res, err := tool.Exec(context.Background(), env, a); err != nil || !res.IsError {
		t.Fatalf("Exec on a disabled server = %+v, %v", res, err)
	}
	if n := f.requests.Load() - before; n != 0 {
		t.Fatalf("%d requests reached a server disabled in config", n)
	}
}

// A discovery of a server that was replaced by a config change finishes
// late; it must not unregister the tools its successor registered under the
// same source.
func TestReplacedServerLateSyncKeepsSuccessorTools(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("docs", ts.URL)}, withClient(ts.Client()))
	h.start()
	h.approve("docs", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	old := h.m.server("docs")

	srv := httpServerCfg("docs", ts.URL)
	srv.TrustedOutput = true
	h.m.applyConfig([]config.MCPServer{srv}, false)
	succ := h.m.server("docs")
	if succ == old {
		t.Fatal("config change kept the old server")
	}
	h.m.discover(context.Background(), succ)
	if _, ok := h.reg.Get("mcp__docs__search"); !ok {
		t.Fatal("successor did not register its approved tool")
	}

	h.m.syncTools(context.Background(), old)
	tool, ok := h.reg.Get("mcp__docs__search")
	if !ok {
		t.Fatal("late sync of the replaced server unregistered the successor's tool")
	}
	if res, err := h.call(tool, userEnv(newUser("bob")), `{"q":"still"}`); err != nil || res.Content != "results for still" {
		t.Fatalf("successor tool = %+v, %v", res, err)
	}
}

func TestFieldMapRefusesValuesTheActionCannotCarry(t *testing.T) {
	spec, err := newServerSpec(config.MCPServer{Name: "mail", Transport: "http", URL: "https://mail.example/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	def := json.RawMessage(`{"inputSchema":{"type":"object"},"name":"send"}`)
	h := newHarness(t, nil)
	tool, err := newTool(h.m, spec, &core.MCPPin{Server: "mail", Tool: "send", Hash: "h", Definition: def, Status: core.PinApproved,
		SideEffect: core.SEExternalSend, FieldMap: map[string]string{"recipient": "/to", "url": "/link"}})
	if err != nil {
		t.Fatal(err)
	}
	plan := func(args string) (*core.Action, error) {
		return tool.Plan(context.Background(), nil, json.RawMessage(args))
	}
	for args, want := range map[string]string{
		`{"to":["a@x.example","b@y.example"]}`: "a@x.example, b@y.example",
		`{"to":4242}`:                          "4242",
		`{"to":null}`:                          "",
		`{"other":"x"}`:                        "",
	} {
		a, err := plan(args)
		if err != nil || a.Recipient != want {
			t.Errorf("Plan(%s) recipient = %+v, %v; want %q", args, a, err, want)
		}
	}
	for _, args := range []string{`{"to":{"addr":"a@x.example"}}`, `{"to":[["nested"]]}`, `{"to":true}`,
		`{"link":["https://a.example/","https://b.example/"]}`} {
		if _, err := plan(args); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Plan(%s) = %v, want ErrInvalid", args, err)
		}
	}
}

func TestToASCIIRefusesHugeHosts(t *testing.T) {
	start := time.Now()
	if _, err := toASCII(strings.Repeat("é", 200_000)); err == nil {
		t.Fatal("200k-rune host accepted")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("refusing a huge host took %s", d)
	}
	if _, err := toASCII(strings.Repeat("a", 63) + ".example"); err != nil {
		t.Fatal(err)
	}
}

// A server cannot make the gateway buffer an unbounded JSON response.
func TestOversizedHTTPResponseRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"pad":"`)
		chunk := strings.Repeat("a", 1<<20)
		for range (maxResponseBytes >> 20) + 2 {
			if _, err := fmt.Fprint(w, chunk); err != nil {
				return
			}
		}
		_, _ = fmt.Fprint(w, `"}}`)
	}))
	t.Cleanup(ts.Close)
	h := newHarness(t, []config.MCPServer{httpServerCfg("big", ts.URL)}, withClient(ts.Client()))
	h.start()
	if st := h.status("big"); st.State != stError || !strings.Contains(st.Err, "larger than") {
		t.Fatalf("status = %+v", st)
	}
}

func TestOversizedDefinitionIgnored(t *testing.T) {
	f := newFakeServer()
	f.extra = []*mcpsdk.Tool{{Name: "huge", Description: strings.Repeat("x", maxDefinitionBytes), InputSchema: map[string]any{"type": "object"}}}
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("docs", ts.URL)}, withClient(ts.Client()))
	h.start()
	pins, err := h.m.Pins(context.Background(), "docs")
	if err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, p := range pins {
		tools = append(tools, p.Tool)
	}
	if !slices.Equal(tools, []string{"block", "search"}) {
		t.Fatalf("pinned tools = %v, want the oversized one dropped", tools)
	}
}

// A server that is down at start-up is rediscovered on its own, with
// backoff, once it comes back.
func TestDiscoveryRetriedAfterFailure(t *testing.T) {
	f := newFakeServer()
	var down atomic.Bool
	down.Store(true)
	inner := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return f.server() }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "maintenance", http.StatusBadGateway)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	h := newHarness(t, []config.MCPServer{httpServerCfg("docs", ts.URL)}, withClient(ts.Client()))
	h.start()
	if st := h.status("docs"); st.State != stError {
		t.Fatalf("status while down = %+v", st)
	}

	h.m.retryDiscovery(h.clock.Now().Add(30 * time.Second))
	time.Sleep(50 * time.Millisecond)
	if st := h.status("docs"); st.State != stError {
		t.Fatalf("rediscovered before the backoff: %+v", st)
	}
	h.m.retryDiscovery(h.clock.Now().Add(time.Minute))
	h.waitFor("second failure", func() bool {
		s := h.m.server("docs")
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.retries == 2
	})
	s := h.m.server("docs")
	s.mu.Lock()
	next := s.retryAt
	s.mu.Unlock()
	if want := h.clock.Now().Add(2 * time.Minute); !next.Equal(want) {
		t.Fatalf("second retry at %s, want %s (doubled backoff)", next, want)
	}

	down.Store(false)
	h.m.retryDiscovery(next)
	h.waitFor("rediscovered", func() bool { return h.status("docs").State == stPinsPending })
	s.mu.Lock()
	retries, at := s.retries, s.retryAt
	s.mu.Unlock()
	if retries != 0 || !at.IsZero() {
		t.Fatalf("retry state after success = %d, %s", retries, at)
	}
}

// A server can echo the vault-resolved Authorization header in an error;
// the status an admin sees and the logs never carry it.
func TestDiscoveryErrorRedactsEchoedSecrets(t *testing.T) {
	const secret = "ECHOED-SECRET-VALUE-0003"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		msg, _ := json.Marshal("invalid credentials: " + r.Header.Get("Authorization"))
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":%s}}`, msg)
	}))
	t.Cleanup(ts.Close)
	srv := httpServerCfg("docs", ts.URL)
	srv.Headers = map[string]string{"Authorization": "Bearer secret:docs_token"}
	h := newHarness(t, []config.MCPServer{srv}, withClient(ts.Client()), withSecrets(map[string]string{"docs_token": secret}))
	h.start()
	st := h.status("docs")
	if st.State != stError || !strings.Contains(st.Err, "invalid credentials") {
		t.Fatalf("status = %+v (want the server's error, redacted)", st)
	}
	if strings.Contains(st.Err, secret) || strings.Contains(h.logs.String(), secret) {
		t.Fatalf("echoed secret leaked: status %q", st.Err)
	}
}

// npm ci never rewrites the lock: a reinstall whose lock differs from the one
// recorded on first install fails, and its tree is removed.
func TestReinstallRefusesChangedLock(t *testing.T) {
	h, in, _ := newStdioHarness(t, notesCfg())
	h.start()
	h.install("notes")
	first, err := h.store.MCP().Install(context.Background(), "notes")
	if err != nil || first.Status != "ready" {
		t.Fatalf("first install = %+v, %v", first, err)
	}

	in.mu.Lock()
	in.ciLock = fmt.Sprintf(`{"lockfileVersion":3,"packages":{"node_modules/@acme/mcp-notes":{"version":"1.2.3","integrity":%q},`+
		`"node_modules/evil":{"version":"6.6.6"}}}`, npmIntegrity)
	in.mu.Unlock()
	h.install("notes")
	st := h.status("notes")
	if st.State != stError || !strings.Contains(st.Err, "differs from the lock recorded") {
		t.Fatalf("status = %+v", st)
	}
	cur, _ := h.store.MCP().Install(context.Background(), "notes")
	if cur.InstallID != first.InstallID || cur.Status != "ready" {
		t.Fatalf("current install = %+v, want the first one still ready", cur)
	}
	entries, err := os.ReadDir(filepath.Join(h.work, "mcp", "notes"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != first.InstallID {
		t.Fatalf("install trees = %v, want only the first (failed tree removed)", entries)
	}
}

func TestRuntimeFetcherCommandsRejected(t *testing.T) {
	for _, cmd := range [][]string{
		{"pnpx", "notes"}, {"npm", "exec", "notes"}, {"npm", "x", "notes"}, {"pnpm", "dlx", "notes"},
		{"yarn", "dlx", "notes"}, {"bun", "x", "notes"}, {"uv", "tool", "run", "notes"}, {"/usr/local/bin/uvx", "notes"},
	} {
		if _, err := newServerSpec(stdioServer(func(s *config.MCPServer) { s.Command = cmd })); err == nil || !strings.Contains(err.Error(), "fetches packages") {
			t.Errorf("command %q accepted: %v", cmd, err)
		}
	}
	for _, cmd := range [][]string{{"bun", "run", "dist/index.js"}, {"node", "dist/index.js"}, {"uv", "run", "--no-sync", "server.py"}} {
		if _, err := newServerSpec(stdioServer(func(s *config.MCPServer) { s.Command = cmd })); err != nil {
			t.Errorf("command %q rejected: %v", cmd, err)
		}
	}
}

type failingDestroy struct {
	*sandboxtest.Sandbox
	fail atomic.Bool
}

func (f *failingDestroy) Destroy(ctx context.Context, key string) error {
	if f.fail.Load() {
		return errors.New("sandboxd: remove failed")
	}
	return f.Sandbox.Destroy(ctx, key)
}

// A box that could not be removed is never reused for a new instance: its
// /opt/mcp could be an older or tampered tree.
func TestStaleBoxNeverReused(t *testing.T) {
	h, _, rec := newStdioHarness(t, notesCfg())
	fd := &failingDestroy{Sandbox: h.sb}
	h.m.d.Sandbox = fd
	h.start()
	h.install("notes")
	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	tool := h.tool("mcp__notes__search")
	alice := userEnv(newUser("alice"))
	box := runtimeBox("notes", alice.Principal.ID)
	if _, err := h.call(tool, alice, `{"q":"x"}`); err != nil {
		t.Fatal(err)
	}

	fd.fail.Store(true)
	h.m.reapIdle(h.clock.Now().Add(time.Hour))
	ensured, streams := len(h.ensuredFor(box)), len(rec.calls())
	if _, err := h.call(tool, alice, `{"q":"y"}`); err == nil || !strings.Contains(err.Error(), "stale box") {
		t.Fatalf("Exec with an unremovable old box = %v", err)
	}
	if len(h.ensuredFor(box)) != ensured || len(rec.calls()) != streams {
		t.Fatal("a box that could not be removed was reused")
	}
	fd.fail.Store(false)
	h.clock.Advance(2 * time.Second)
	if res, err := h.call(tool, alice, `{"q":"z"}`); err != nil || res.Content != "results for z" {
		t.Fatalf("Exec once the box is gone = %+v, %v", res, err)
	}
}

// lockingSandbox runs Watch callbacks under the same lock its Destroy takes,
// as a sandbox client may.
type lockingSandbox struct {
	*sandboxtest.Sandbox
	mu  sync.Mutex
	fns []func(core.SandboxStatus)
}

func (l *lockingSandbox) Watch(fn func(core.SandboxStatus)) {
	l.mu.Lock()
	l.fns = append(l.fns, fn)
	l.mu.Unlock()
}

func (l *lockingSandbox) Destroy(ctx context.Context, key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Sandbox.Destroy(ctx, key)
}

func (l *lockingSandbox) set(st core.SandboxStatus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Sandbox.SetStatus(st)
	for _, fn := range l.fns {
		fn(l.Sandbox.Status(context.Background()))
	}
}

func TestSandboxWatchCallbackDoesNotCallBack(t *testing.T) {
	h, _, _ := newStdioHarness(t, notesCfg())
	ls := &lockingSandbox{Sandbox: h.sb}
	h.m.d.Sandbox = ls
	h.start()
	h.install("notes")
	h.approve("notes", map[string]core.MCPToolApproval{"search": {}})
	alice := userEnv(newUser("alice"))
	if _, err := h.call(h.tool("mcp__notes__search"), alice, `{"q":"x"}`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		ls.set(core.SandboxStatus{Backend: "none"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sandbox status callback deadlocked calling back into the sandbox")
	}
	h.waitFor("no_sandbox", func() bool { return h.status("notes").State == stNoSandbox })
	h.waitFor("instance closed", func() bool {
		inst := h.m.server("notes").instance("p:" + string(alice.Principal.ID))
		inst.mu.Lock()
		defer inst.mu.Unlock()
		return inst.c == nil
	})
}
