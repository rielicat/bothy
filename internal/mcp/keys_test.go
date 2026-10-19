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
	a, err := h.m.resolveHTTP(context.Background(), s, "", false)
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
