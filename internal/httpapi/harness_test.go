package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/auth"
	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/doctor"
	"github.com/bothyhq/bothy/internal/events"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
	"github.com/bothyhq/bothy/internal/testkit/memstore"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

const testPassword = "violin staple orbit meadow"

// testConfig is a mutable config.Admin.
type testConfig struct {
	mu   sync.Mutex
	cfg  *config.Config
	eff  config.Effective
	sets []map[string]json.RawMessage
}

func (c *testConfig) Current() *config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

func (c *testConfig) Watch(func(old, cur *config.Config)) func() { return func() {} }

func (c *testConfig) Effective() config.Effective {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eff
}

func (c *testConfig) Set(_ context.Context, changes map[string]json.RawMessage, _ string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, bad := changes["bad.path"]; bad {
		return nil, config.FieldErrors{{Path: "bad.path", Message: "unknown key bad.path"}}
	}
	c.sets = append(c.sets, changes)
	return []string{}, nil
}

func (c *testConfig) Reset(context.Context, string, string) error { return nil }
func (c *testConfig) Schema() json.RawMessage                     { return json.RawMessage(`{"type":"object"}`) }

// update replaces the config with a modified copy (handlers see snapshots).
func (c *testConfig) update(fn func(*config.Config)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := *c.cfg
	fn(&cp)
	c.cfg = &cp
}

type harness struct {
	t         *testing.T
	srv       *Server
	h         http.Handler
	cfg       *testConfig
	store     core.Store
	auth      *auth.Service
	bus       *events.Bus
	vault     *fakes.Vault
	runner    *fakes.Runner
	approvals *fakes.Approvals
	policy    *fakes.Policy
	llm       *fakes.LLM
	budget    *fakes.Budget
	memory    *fakes.Memory
	skills    *fakes.Skills
	mcp       *fakes.MCP
	scheduler *fakes.Scheduler
	hub       *fakes.Hub
	sandbox   *sandboxtest.Sandbox
	audit     *fakes.Auditor
	dataDir   string
	workDir   string
	restarts  atomic.Int32
	env       map[string]string

	owner, member, admin *client
}

// client is an authenticated caller: a cookie session or a bearer token.
type client struct {
	name   string
	user   core.UserID
	cookie string
	csrf   string
	token  string
	scopes []string
}

func newHarness(t *testing.T, mut ...func(*config.Config)) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.Mode = "multi"
	cfg.Auth.Argon2 = config.Argon2{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1}
	cfg.Server.MaxBodyMB = 1
	cfg.Server.UploadMaxMB = 1
	for _, m := range mut {
		m(cfg)
	}
	h := &harness{t: t, cfg: &testConfig{cfg: cfg}, store: memstore.New(), bus: events.New(events.Options{}),
		vault: fakes.NewVault(nil), approvals: &fakes.Approvals{Manual: true}, policy: &fakes.Policy{},
		llm: &fakes.LLM{}, budget: &fakes.Budget{}, memory: &fakes.Memory{}, skills: &fakes.Skills{Bodies: map[string]string{}},
		mcp: &fakes.MCP{}, scheduler: &fakes.Scheduler{}, hub: &fakes.Hub{}, sandbox: &sandboxtest.Sandbox{},
		audit: &fakes.Auditor{}, dataDir: t.TempDir(), workDir: t.TempDir(), env: map[string]string{}}
	h.runner = &fakes.Runner{Store: h.store}
	a, err := auth.New(auth.Deps{Store: h.store, Config: h.cfg, Audit: h.audit, Bus: h.bus, Redactor: h.vault.Redactor(),
		DataDir: h.dataDir})
	if err != nil {
		t.Fatal(err)
	}
	h.auth = a
	srv, err := New(Deps{Config: h.cfg, Store: h.store, Auth: a, Vault: h.vault, Bus: h.bus, Runner: h.runner,
		Approvals: h.approvals, Policy: h.policy, LLM: h.llm, Budget: h.budget, Memory: h.memory, Skills: h.skills,
		MCP: h.mcp, Scheduler: h.scheduler, Hub: h.hub, Sandbox: h.sandbox, Egress: &sandboxtest.Egress{},
		Doctor: doctor.New(a.Checks()), Audit: h.audit, WebUI: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>Bothy</title>")
		}), Version: "test", DataDir: h.dataDir, WorkDir: h.workDir, Restart: func() { h.restarts.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	srv.getenv = func(k string) string { return h.env[k] }
	srv.gateways = func() []netip.Addr { return nil }
	h.srv, h.h = srv, srv.Handler()
	return h
}

// setup runs the first-run flow through the auth service and signs in an
// owner, an admin and a member.
func (h *harness) setup() *harness {
	h.t.Helper()
	ctx := context.Background()
	tok, _, err := h.auth.EnsureSetupToken(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	grant, err := h.auth.ClaimSetup(ctx, tok)
	if err != nil {
		h.t.Fatal(err)
	}
	cookie, oa, err := h.auth.CreateOwner(ctx, grant, "owner", testPassword, "UTC")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.auth.CompleteSetup(ctx, oa); err != nil {
		h.t.Fatal(err)
	}
	h.owner = &client{name: "owner", user: oa.User, cookie: cookie, csrf: oa.CSRF}
	h.admin = h.newUser("admin", core.RoleAdmin)
	h.member = h.newUser("member", core.RoleMember)
	return h
}

func (h *harness) newUser(name string, role core.Role) *client {
	h.t.Helper()
	ctx := context.Background()
	oa, err := h.auth.FromCookie(ctx, h.owner.cookie)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.auth.CreateUser(ctx, oa, name, testPassword, "", role); err != nil {
		h.t.Fatal(err)
	}
	cookie, a, err := h.auth.Login(ctx, name, testPassword, "127.0.0.1")
	if err != nil {
		h.t.Fatal(err)
	}
	return &client{name: name, user: a.User, cookie: cookie, csrf: a.CSRF}
}

// tokenFor issues a PAT for c with the given scopes.
func (h *harness) tokenFor(c *client, scopes ...string) *client {
	h.t.Helper()
	ctx := context.Background()
	a, err := h.auth.FromCookie(ctx, c.cookie)
	if err != nil {
		h.t.Fatal(err)
	}
	plain, _, err := h.auth.CreateToken(ctx, a, core.TokenSpec{Name: "t-" + strings.Join(scopes, "-"), Scopes: scopes, TTL: time.Hour, RatePerMin: 10000})
	if err != nil {
		h.t.Fatal(err)
	}
	return &client{name: c.name + "/token:" + strings.Join(scopes, "+"), user: c.user, token: plain, scopes: scopes}
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func fromAddr(addr string) reqOpt   { return func(r *http.Request) { r.RemoteAddr = addr } }
func withHost(host string) reqOpt   { return func(r *http.Request) { r.Host = host } }
func overTLS() reqOpt               { return func(r *http.Request) { r.TLS = &tls.ConnectionState{} } }
func withCtx(ctx context.Context) reqOpt {
	return func(r *http.Request) { *r = *r.WithContext(ctx) }
}
func noCSRF() reqOpt { return func(r *http.Request) { r.Header.Del("X-CSRF-Token") } }

func (h *harness) request(method, path string, body any, c *client, opts ...reqOpt) *http.Request {
	h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Host = "127.0.0.1:8080"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		switch {
		case c.token != "":
			req.Header.Set("Authorization", "Bearer "+c.token)
		case c.cookie != "":
			req.AddCookie(&http.Cookie{Name: cookiePlain, Value: c.cookie})
			req.AddCookie(&http.Cookie{Name: cookieSecure, Value: c.cookie})
			req.Header.Set("X-CSRF-Token", c.csrf)
		}
	}
	for _, o := range opts {
		o(req)
	}
	return req
}

func (h *harness) do(method, path string, body any, c *client, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, h.request(method, path, body, c, opts...))
	return rec
}

// decodeAs decodes a JSON response body into T, failing the test on error.
func decodeAs[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T from %d %q: %v", v, rec.Code, rec.Body.String(), err)
	}
	return v
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Error.Code
}
