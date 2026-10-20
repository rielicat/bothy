package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// A key replaced in the vault is sent at the next call, and a deleted one is
// never sent again: the session made with the old value is closed. Each call
// is a use of the key.
func TestReplacedOrDeletedKeyTakesEffectAtTheNextCall(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	srv := httpServerCfg("echo", ts.URL)
	srv.Headers = map[string]string{"Authorization": "secret:mcp_echo_header"}
	h := newHarness(t, []config.MCPServer{srv}, withClient(ts.Client()),
		withSecrets(map[string]string{"mcp_echo_header": "Bearer qa-good-value-0001"}))
	h.start()
	h.approve("echo", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	tool := h.tool("mcp__echo__search")
	env := userEnv(newUser("bob"))
	ctx := context.Background()

	if _, err := h.call(tool, env, `{"q":"1"}`); err != nil {
		t.Fatal(err)
	}
	if got := f.lastAuth(); got != "Bearer qa-good-value-0001" {
		t.Fatalf("first call sent %q", got)
	}
	uses := len(h.vault.Uses)
	if _, err := h.vault.Put(ctx, core.GlobalScope, "mcp_echo_header", []byte("Bearer qa-good-value-0002"), admin); err != nil {
		t.Fatal(err)
	}
	if _, err := h.call(tool, env, `{"q":"2"}`); err != nil {
		t.Fatal(err)
	}
	if got := f.lastAuth(); got != "Bearer qa-good-value-0002" {
		t.Fatalf("call after Replace sent %q", got)
	}
	if len(h.vault.Uses) <= uses {
		t.Fatal("a call with the key wasn't recorded as a use")
	}
	if err := h.vault.Delete(ctx, core.GlobalScope, "mcp_echo_header", admin); err != nil {
		t.Fatal(err)
	}
	sent := len(f.authHeadersAll())
	_, err := h.call(tool, env, `{"q":"3"}`)
	if err == nil || errCode(err) != "key_missing" {
		t.Fatalf("call after Delete = %v", err)
	}
	// Closing the session may tell the server so, without the key.
	for _, a := range f.authHeadersAll()[sent:] {
		if a != "" {
			t.Fatalf("the server was sent %q after its key was deleted", a)
		}
	}
}

// A server that stops accepting the key ends the session, so a rotated key
// works at the next call instead of staying broken until a reload.
func TestRejectedKeyDropsTheSession(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	srv := httpServerCfg("echo", ts.URL)
	srv.Headers = map[string]string{"Authorization": "secret:mcp_echo_header"}
	h := newHarness(t, []config.MCPServer{srv}, withClient(ts.Client()),
		withSecrets(map[string]string{"mcp_echo_header": "Bearer old"}))
	h.start()
	h.approve("echo", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	tool := h.tool("mcp__echo__search")
	env := userEnv(newUser("bob"))
	if _, err := h.call(tool, env, `{"q":"1"}`); err != nil {
		t.Fatal(err)
	}
	f.setAuth("Bearer new")
	if _, err := h.call(tool, env, `{"q":"2"}`); err == nil || !errors.Is(err, errKeyRejected) {
		t.Fatalf("call with a rejected key = %v", err)
	}
	// The vault value didn't change: only dropping the session lets the next
	// call try again (after the backoff) rather than reuse a dead session.
	s := h.m.server("echo")
	inst := s.instance("u:" + string(env.User))
	inst.mu.Lock()
	gone := inst.c == nil
	inst.mu.Unlock()
	if !gone {
		t.Fatal("the session the server rejected was kept")
	}
}

// Discovery that the server refuses says the key wasn't accepted, which the
// status names.
func TestRejectedKeyAtDiscoveryIsNamed(t *testing.T) {
	f := newFakeServer()
	f.setAuth("Bearer right")
	ts := f.httpServer(t, false)
	srv := httpServerCfg("echo", ts.URL)
	srv.Headers = map[string]string{"Authorization": "secret:mcp_echo_header"}
	h := newHarness(t, []config.MCPServer{srv}, withClient(ts.Client()),
		withSecrets(map[string]string{"mcp_echo_header": "Bearer wrong"}))
	h.start()
	if st := h.status("echo"); st.State != stError || st.Code != "key_rejected" {
		t.Fatalf("status = %+v", st)
	}
}

// A server added without a key that answers 401 needs one: it refused no
// key, so the status says to add one rather than to replace it.
func TestRefusalWithoutAnyKeyAsksForOne(t *testing.T) {
	f := newFakeServer()
	f.setAuth("Bearer right")
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("echo", ts.URL)}, withClient(ts.Client()))
	h.start()
	if st := h.status("echo"); st.State != stError || st.Code != "key_needed" {
		t.Fatalf("status = %+v", st)
	}
}

// A key in the server's address is a reference resolved per call, and an
// error that quotes the address shows the reference, not the key.
func TestAddressReferenceIsResolvedAndNeverShown(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	srv := httpServerCfg("echo", ts.URL+"/mcp?api_key=secret:echo_url_key")
	h := newHarness(t, []config.MCPServer{srv}, withClient(ts.Client()),
		withSecrets(map[string]string{"echo_url_key": "qa-url-key-UQ33-0001"}))
	h.start()
	if st := h.status("echo"); st.State != stPinsPending {
		t.Fatalf("status = %+v", st)
	}
	s := h.m.server("echo")
	a, err := h.m.resolveHTTP(context.Background(), s, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.endpoint.Reveal(), "api_key=qa-url-key-UQ33-0001") {
		t.Fatal("the address reference was not resolved")
	}
	masked := a.hideEndpoint(s.spec.url, errors.New(`Post "`+a.endpoint.Reveal()+`": refused`))
	if strings.Contains(masked.Error(), "qa-url-key") || !strings.Contains(masked.Error(), "secret:echo_url_key") {
		t.Fatalf("error = %v", masked)
	}
}

// A session whose key sits in its address is closed without sending that
// address once the key is deleted: the close would carry the old key.
func TestDeletedAddressKeyIsNotSentToClose(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	srv := httpServerCfg("echo", ts.URL+"/mcp?api_key=secret:echo_url_key")
	h := newHarness(t, []config.MCPServer{srv}, withClient(ts.Client()),
		withSecrets(map[string]string{"echo_url_key": "qa-url-key-UQ33-0001"}))
	h.start()
	h.approve("echo", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	tool := h.tool("mcp__echo__search")
	env := userEnv(newUser("bob"))
	if _, err := h.call(tool, env, `{"q":"1"}`); err != nil {
		t.Fatal(err)
	}
	if err := h.vault.Delete(context.Background(), core.GlobalScope, "echo_url_key", admin); err != nil {
		t.Fatal(err)
	}
	sent := len(f.queriesAll())
	if _, err := h.call(tool, env, `{"q":"2"}`); errCode(err) != "key_missing" {
		t.Fatalf("call after Delete = %v", err)
	}
	for _, q := range f.queriesAll()[sent:] {
		if strings.Contains(q, "qa-url-key") {
			t.Fatalf("the server was sent %q after its key was deleted", q)
		}
	}
}

// A server that won't list its tools without a key, where only personal keys
// exist, is discovered with the own key of the admin who chose so. That key
// lists tools only: everyone else's calls go with their own key or none.
func TestDiscoveryWithAnAdminsOwnKey(t *testing.T) {
	ctx := context.Background()
	f := newFakeServer()
	f.needKey = true
	ts := f.httpServer(t, false)
	cfg := httpServerCfg("docs", ts.URL)
	cfg.Headers = map[string]string{"Authorization": "secret:docs_key"}
	carol, bob, other := newUser("carol"), newUser("bob"), newUser("other-admin")
	h := newHarness(t, []config.MCPServer{cfg}, withClient(ts.Client()), withSecrets(map[string]string{
		"user:" + string(admin) + "/docs_key": "Bearer admin-own",
		"user:" + string(carol) + "/docs_key": "Bearer carol-own",
	}))
	for _, u := range []*core.User{{ID: admin, Username: "admin", Role: core.RoleAdmin}, {ID: other, Username: "other", Role: core.RoleAdmin},
		{ID: bob, Username: "bob", Role: core.RoleMember}} {
		if err := h.store.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	h.start()
	if st := h.status("docs"); st.State != stError || st.Code != "no_shared_key" {
		t.Fatalf("status without a shared key = %+v", st)
	}
	if err := h.m.DiscoverWith(ctx, "docs", bob); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("a member's own key = %v", err)
	}
	if err := h.m.DiscoverWith(ctx, "docs", other); !errors.Is(err, core.ErrConflict) || !strings.Contains(err.Error(), "docs_key") {
		t.Fatalf("an admin without a key of their own = %v", err)
	}

	if err := h.m.DiscoverWith(ctx, "docs", admin); err != nil {
		t.Fatal(err)
	}
	h.waitFor("discovery with the admin's key", func() bool { return h.status("docs").State == stPinsPending })
	if got := f.lastAuth(); got != "Bearer admin-own" {
		t.Fatalf("discovery sent %q", got)
	}
	d := h.auditActions("mcp.discover")
	if len(d) != 1 || !strings.Contains(string(d[0].Detail), string(admin)) || !strings.Contains(string(d[0].Detail), `"tools":["block","search"]`) {
		t.Fatalf("mcp.discover audit = %+v", d)
	}
	h.approve("docs", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	tool := h.tool("mcp__docs__search")

	sent := len(f.authHeadersAll())
	if _, err := h.call(tool, userEnv(bob), `{"q":"1"}`); errCode(err) != "key_missing" {
		t.Fatalf("a call by someone without a key = %v", err)
	}
	if res, err := h.call(tool, userEnv(carol), `{"q":"2"}`); err != nil || res.Content != "results for 2" {
		t.Fatalf("a call with carol's own key = %+v, %v", res, err)
	}
	for _, a := range f.authHeadersAll()[sent:] {
		if a != "Bearer carol-own" && a != "" {
			t.Fatalf("a call was sent %q", a)
		}
	}

	// The choice holds across rediscoveries of the same entry, and across
	// turning the server off and on, never for an entry whose address or
	// headers someone changed since.
	h.rediscover("docs")
	if got := f.lastAuth(); got != "Bearer admin-own" || h.status("docs").Code != "" {
		t.Fatalf("rediscovery sent %q; status %+v", got, h.status("docs"))
	}
	off, on := cfg, cfg
	off.Enabled, on.Enabled = new(false), new(true)
	h.m.applyConfig([]config.MCPServer{off}, true)
	h.waitFor("turned off", func() bool { return h.status("docs").State == stDisabled })
	h.m.applyConfig([]config.MCPServer{on}, true)
	h.waitFor("turned on again", func() bool { st := h.status("docs"); return st.State != stConnecting && st.State != stDisabled })
	if st := h.status("docs"); st.Code != "" || f.lastAuth() != "Bearer admin-own" {
		t.Fatalf("after turning it off and on: status %+v, sent %q", st, f.lastAuth())
	}
	changed := cfg
	changed.Headers = map[string]string{"Authorization": "secret:docs_key", "X-Team": "acme"}
	h.m.applyConfig([]config.MCPServer{changed}, true)
	h.waitFor("rediscovery of the changed entry", func() bool { return h.status("docs").State == stError })
	if st := h.status("docs"); st.Code != "no_shared_key" {
		t.Fatalf("status of a changed entry = %+v", st)
	}

	h.cfg.MCP.Servers = nil
	if err := h.m.Forget(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Meta().Get(ctx, keyHolderMeta+"docs"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("a removed server's key holder was kept: %v", err)
	}
}
