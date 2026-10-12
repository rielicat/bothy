package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/events"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
	"github.com/bothyhq/bothy/internal/testkit/memstore"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

const admin = core.UserID("usr_0000000000000000000000admn")

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t     *testing.T
	m     *Manager
	cfg   *config.Config
	store core.Store
	reg   *fakes.Registry
	sb    *sandboxtest.Sandbox
	eg    *sandboxtest.Egress
	vault *fakes.Vault
	bus   *events.Bus
	audit *fakes.Auditor
	clock *fakes.Clock
	work  string
	logs  *syncBuffer
}

type option func(*harness)

func withSecrets(s map[string]string) option { return func(h *harness) { h.vault = fakes.NewVault(s) } }

func withClient(c *http.Client) option { return func(h *harness) { h.eg.Client = c } }

func noSandbox(h *harness) { h.sb.StatusV = core.SandboxStatus{Backend: "none", Reason: "test"} }

func newHarness(t *testing.T, servers []config.MCPServer, opts ...option) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.MCP.Servers = servers
	h := &harness{t: t, cfg: cfg, store: memstore.New(), reg: &fakes.Registry{}, sb: &sandboxtest.Sandbox{},
		eg: &sandboxtest.Egress{}, vault: fakes.NewVault(nil), bus: events.New(events.Options{}), audit: &fakes.Auditor{},
		clock: fakes.NewClock(time.Unix(1_800_000_000, 0)), work: t.TempDir(), logs: &syncBuffer{}}
	for _, o := range opts {
		o(h)
	}
	m, err := New(Deps{Config: config.Static(cfg), Store: h.store, Sandbox: h.sb, Egress: h.eg, Registry: h.reg, Vault: h.vault,
		Bus: h.bus, Audit: h.audit, WorkDir: h.work, Clock: h.clock,
		Log: slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.m = m
	t.Cleanup(func() { _ = m.Close() })
	return h
}

func (h *harness) start() {
	h.t.Helper()
	if err := h.m.Start(context.Background()); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
}

func (h *harness) status(name string) core.MCPStatus {
	h.t.Helper()
	for _, s := range h.m.Status() {
		if s.Name == name {
			return s
		}
	}
	h.t.Fatalf("no status for %s", name)
	return core.MCPStatus{}
}

// waitFor polls until cond holds; background discovery and installs finish
// asynchronously.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; status %+v", what, h.m.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) approve(server string, tools map[string]core.MCPToolApproval) {
	h.t.Helper()
	if err := h.m.Approve(context.Background(), server, tools, admin); err != nil {
		h.t.Fatalf("Approve: %v", err)
	}
}

func (h *harness) tool(name string) core.Tool {
	h.t.Helper()
	t, ok := h.reg.Get(name)
	if !ok {
		h.t.Fatalf("tool %s not registered; have %v", name, h.toolNames())
	}
	return t
}

func (h *harness) toolNames() []string {
	var out []string
	for _, s := range h.reg.All() {
		out = append(out, s.Def.Name)
	}
	return out
}

func (h *harness) call(t core.Tool, env *core.ToolEnv, args string) (*core.ToolResult, error) {
	h.t.Helper()
	a, err := t.Plan(context.Background(), env, json.RawMessage(args))
	if err != nil {
		h.t.Fatalf("Plan: %v", err)
	}
	return t.Exec(context.Background(), env, a)
}

func (h *harness) auditActions(action string) []core.AuditEntry {
	var out []core.AuditEntry
	for _, e := range h.audit.All() {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func userEnv(u core.UserID) *core.ToolEnv {
	return &core.ToolEnv{User: u, Role: core.RoleMember, Trigger: core.TrigMessage, Interactive: true,
		Principal: core.Principal{ID: core.UserPrincipal(u), Kind: core.PrincipalUser, User: u,
			Workspace: core.WorkspaceID(core.DerivedID(core.PrefixWorkspace, string(u))), ToolProfile: "standard"},
		Selection: core.Selection{SandboxOK: true}}
}

func newUser(name string) core.UserID { return core.UserID(core.DerivedID(core.PrefixUser, name)) }

// fakeServer is an in-process MCP server built with the official SDK.
type fakeServer struct {
	mu       sync.Mutex
	desc     string
	headers  []http.Header
	versions []string // nil = every version the SDK supports
	// stateless serves Streamable HTTP statelessly, which the 2026-07-28
	// protocol requires; such a server learns of a cancelled call from the
	// client abandoning the HTTP request.
	stateless bool
	extra     []*mcpsdk.Tool

	requests  atomic.Int32
	started   chan struct{}
	cancelled chan struct{}
}

func newFakeServer() *fakeServer {
	return &fakeServer{desc: "Search the docs.", started: make(chan struct{}, 8), cancelled: make(chan struct{}, 8)}
}

func (f *fakeServer) setDesc(d string) {
	f.mu.Lock()
	f.desc = d
	f.mu.Unlock()
}

var objectSchema = map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}

func (f *fakeServer) server() *mcpsdk.Server {
	f.mu.Lock()
	desc, versions, extra := f.desc, f.versions, slices.Clone(f.extra)
	f.mu.Unlock()
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "1"},
		&mcpsdk.ServerOptions{SupportedProtocolVersions: versions, Logger: slog.New(slog.DiscardHandler)})
	s.AddTool(&mcpsdk.Tool{Name: "search", Description: desc, InputSchema: objectSchema},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			var args struct {
				Q string `json:"q"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "results for " + args.Q}}}, nil
		})
	s.AddTool(&mcpsdk.Tool{Name: "block", Description: "Blocks until cancelled.", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			f.started <- struct{}{}
			select {
			case <-ctx.Done():
				f.cancelled <- struct{}{}
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
				return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "not cancelled"}}}, nil
			}
		})
	for _, t := range extra {
		s.AddTool(t, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(req.Params.Arguments)}}}, nil
		})
	}
	return s
}

// httpServer serves f over Streamable HTTP or, with legacy set, over the
// pre-2025-03-26 HTTP+SSE transport.
func (f *fakeServer) httpServer(t *testing.T, legacy bool) *httptest.Server {
	get := func(*http.Request) *mcpsdk.Server { return f.server() }
	var h http.Handler = mcpsdk.NewStreamableHTTPHandler(get,
		&mcpsdk.StreamableHTTPOptions{Stateless: f.stateless, PropagateRequestCancellation: f.stateless})
	if legacy {
		h = mcpsdk.NewSSEHandler(get, nil)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (f *fakeServer) authHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, h := range f.headers {
		if v := h.Get("Authorization"); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func httpServerCfg(name, url string) config.MCPServer {
	return config.MCPServer{Name: name, Transport: "http", URL: url, AllowPrivate: true}
}
