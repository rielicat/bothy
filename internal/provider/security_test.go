package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// TestKeysStayInHeaders records every request of every client kind (a
// success and a rejected key whose error message echoes it) and checks the
// key appears only in that vendor's auth header: never in a URL, a log line,
// an error, CallInfo or health.
func TestKeysStayInHeaders(t *testing.T) {
	anthropicOK, _ := os.ReadFile("anthropic/testdata/sdk_basic.sse")
	geminiOK, _ := os.ReadFile("gemini/testdata/js_text_stream.sse")
	ollamaOK, _ := os.ReadFile("ollama/testdata/docs_chat_stream.ndjson")
	responsesOK, _ := os.ReadFile("openai/testdata/spec_stream.sse")
	echo := "invalid key " + canary
	for _, tc := range []struct {
		kind   core.ProviderKind
		ok     reply
		denied reply
		header string
		value  string
	}{
		{core.KindAnthropic, sse(string(anthropicOK)), jsonReply(401, `{"type":"error","error":{"type":"authentication_error","message":"`+echo+`"}}`), "X-Api-Key", canary},
		{core.KindOpenAI, sse(string(responsesOK)), jsonReply(401, errBody(`"invalid_api_key"`, "invalid_request_error", echo)), "Authorization", "Bearer " + canary},
		{core.KindOpenAICompat, sse(okStream), jsonReply(401, errBody(`"invalid_api_key"`, "invalid_request_error", echo)), "Authorization", "Bearer " + canary},
		{core.KindGemini, sse(string(geminiOK)), jsonReply(400, `{"error":{"code":400,"message":"`+echo+`","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`), "X-Goog-Api-Key", canary},
		{core.KindOllama, reply{status: 200, ctype: "application/x-ndjson", body: string(ollamaOK)}, jsonReply(401, `{"error":"`+echo+`"}`), "Authorization", "Bearer " + canary},
	} {
		srv := newServer(t, tc.ok, tc.denied)
		h := newHarness(t, []config.Provider{cfgProvider("p", tc.kind, srv.URL, config.ProviderModel{ID: "m", Context: 8192})}, "p/m")
		if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		_, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != core.ErrKindAuth {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		leaks := []string{err.Error(), ci.Error, h.logs.String(), h.f.Health()[0].LastError}
		for _, a := range ci.Attempts {
			leaks = append(leaks, a.Err)
		}
		if contains(leaks, canary) {
			t.Fatalf("%s: key leaked into %q", tc.kind, leaks)
		}
		if !strings.Contains(err.Error(), "[redacted]") {
			t.Fatalf("%s: echoed key not masked: %v", tc.kind, err)
		}
		for i := range srv.count() {
			srv.mu.Lock()
			u, hd := srv.urls[i], srv.headers[i]
			srv.mu.Unlock()
			if strings.Contains(u, canary) || strings.Contains(u, url.QueryEscape(canary)) || strings.Contains(u, "key=") {
				t.Fatalf("%s: key in URL %s", tc.kind, u)
			}
			for name, vals := range hd {
				for _, v := range vals {
					if strings.Contains(v, canary) && (name != tc.header || v != tc.value) {
						t.Fatalf("%s: key in header %s", tc.kind, name)
					}
				}
			}
			if hd.Get(tc.header) != tc.value {
				t.Fatalf("%s: auth header %s missing", tc.kind, tc.header)
			}
		}
	}
}

func TestClientHardening(t *testing.T) {
	h := newHarness(t, nil)
	h.egress.Client = &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS10}},
		Timeout:   30,
	}
	c, err := h.f.httpClient("provider:x", "https://api.example.com/v1", false)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("transport %+v", c.Transport)
	}
	if c.Timeout != 0 || c.Jar != nil || !errors.Is(c.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("client must not time out streams, keep cookies or follow redirects")
	}
	// The egress client itself is left alone.
	if orig := h.egress.Client.Transport.(*http.Transport); orig.Proxy == nil || orig.TLSClientConfig.MinVersion != tls.VersionTLS10 {
		t.Fatal("hardening mutated the egress transport")
	}
	d := h.egress.Dests[0]
	if d.Name != "provider:x" || len(d.Hosts) != 1 || d.Hosts[0] != "api.example.com:443" || d.AllowPrivate {
		t.Fatalf("dest %+v", d)
	}
	for base, want := range map[string]string{
		"http://host.docker.internal:11434": "host.docker.internal:11434",
		"http://[::1]/v1":                   "[::1]:80",
		"HTTPS://API.X.AI/v1":               "api.x.ai:443",
	} {
		if got, err := hostPort(base); err != nil || got != want {
			t.Fatalf("%s: %q %v", base, got, err)
		}
	}
	// A nil transport would mean http.DefaultTransport and its env proxy.
	h.egress.Client = &http.Client{}
	c, err = h.f.httpClient("provider:y", "https://api.example.com/v1", false)
	if err != nil {
		t.Fatal(err)
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("nil transport not hardened: %+v", c.Transport)
	}
	for _, bad := range []string{"ftp://x", "https://user:pw@x", "::nope", "https://x/v1?key=1", "https://x/v1#f", "https://x/v1?"} {
		if _, err := hostPort(bad); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestRedirectsNotFollowed(t *testing.T) {
	target := newServer(t, sse(okStream))
	redirect := newServer(t, reply{status: http.StatusTemporaryRedirect, ctype: "text/plain", header: map[string]string{"Location": target.URL + "/chat/completions"}})
	h := newHarness(t, []config.Provider{cfgProvider("p", core.KindOpenAICompat, redirect.URL)}, "p/m")
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err == nil {
		t.Fatal("redirect followed")
	}
	if target.count() != 0 {
		t.Fatal("the key headers reached the redirect target")
	}
}

func TestSecretCaching(t *testing.T) {
	srv := newServer(t, sse(okStream))
	h := newHarness(t, []config.Provider{cfgProvider("p", core.KindOpenAICompat, srv.URL)}, "p/m")
	h.view.c.Providers[0].Headers = map[string]string{"X-Org": "org secret:test_key"}
	for range 3 {
		if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
			t.Fatal(err)
		}
	}
	// One resolution each for the key and the header within the TTL.
	if len(h.vault.Uses) != 2 || h.vault.Uses[0] != "test_key|provider:p" {
		t.Fatalf("uses %v", h.vault.Uses)
	}
	srv.mu.Lock()
	hdr := srv.headers[0].Get("X-Org")
	srv.mu.Unlock()
	if hdr != "org "+canary {
		t.Fatalf("header %q", hdr)
	}
	h.clock.Advance(secretTTL)
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.vault.Uses) != 4 {
		t.Fatalf("not re-resolved after the TTL: %v", h.vault.Uses)
	}
	// A key replaced in the vault is read again at the next call, inside the TTL.
	if _, err := h.vault.Put(context.Background(), core.GlobalScope, "test_key", []byte(canary+"-rotated"), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.vault.Uses) != 6 {
		t.Fatalf("not re-resolved after the key was replaced: %v", h.vault.Uses)
	}
	srv.mu.Lock()
	hdr = srv.headers[len(srv.headers)-1].Get("X-Org")
	srv.mu.Unlock()
	if hdr != "org "+canary+"-rotated" {
		t.Fatalf("header after the replace %q", hdr)
	}
	// A sealed vault is a server-side condition, not an auth failure.
	h.vault.Seal()
	h.clock.Advance(secretTTL)
	_, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer || !errors.Is(err, core.ErrSealed) {
		t.Fatalf("sealed: %v", err)
	}
}
