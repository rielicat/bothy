package httpapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func TestHostCheck(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Server.AllowedHosts = []string{"bothy.lan", "box.example:9443"}
		c.Server.PublicURL = "https://bothy.example.com"
	})
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "LOCALHOST", "bothy.lan:8080", "box.example:9443",
		"bothy.example.com", "bothy.example.com:443"} {
		if rec := h.do("GET", "/healthz", nil, nil, withHost(host)); rec.Code != http.StatusOK {
			t.Errorf("Host %q = %d", host, rec.Code)
		}
	}
	for _, host := range []string{"evil.example", "box.example:8443", "127.0.0.1.nip.io:8080", ""} {
		rec := h.do("GET", "/api/auth/me", nil, nil, withHost(host))
		if rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q = %d, want 421", host, rec.Code)
		}
		if rec.Header().Get("Content-Security-Policy") != csp {
			t.Errorf("421 without CSP")
		}
	}
}

func TestExposureGate(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*config.Config)
		addr    string
		opts    []reqOpt
		allowed bool
	}{
		{"loopback", nil, "127.0.0.1:5000", nil, true},
		{"ipv6 loopback", nil, "[::1]:5000", nil, true},
		{"lan plain", nil, "192.168.1.20:5000", nil, false},
		{"ula plain", nil, "[fd00::5]:5000", nil, false},
		{"lan with insecure_lan", func(c *config.Config) { c.Server.InsecureLAN = true }, "10.1.2.3:5000", nil, true},
		{"public plain even with insecure_lan", func(c *config.Config) { c.Server.InsecureLAN = true }, "203.0.113.9:5000", nil, false},
		{"public over tls", nil, "203.0.113.9:5000", []reqOpt{overTLS()}, true},
		{"local_cidrs", func(c *config.Config) { c.Server.LocalCIDRs = []string{"100.64.0.0/10"} }, "100.100.1.1:5000", nil, true},
		{"spoofed forwarded proto from untrusted peer", nil, "203.0.113.9:5000",
			[]reqOpt{withHeader("X-Forwarded-Proto", "https"), withHeader("X-Forwarded-For", "127.0.0.1")}, false},
		{"trusted proxy https", func(c *config.Config) { c.Server.TrustedProxies = []string{"10.0.0.2/32"} }, "10.0.0.2:5000",
			[]reqOpt{withHeader("X-Forwarded-Proto", "https"), withHeader("X-Forwarded-For", "203.0.113.9")}, true},
		{"trusted proxy plain public client", func(c *config.Config) { c.Server.TrustedProxies = []string{"10.0.0.2/32"} }, "10.0.0.2:5000",
			[]reqOpt{withHeader("X-Forwarded-For", "203.0.113.9")}, false},
		// Host never matters: a loopback Host header from a public peer is refused.
		{"host header is ignored", nil, "203.0.113.9:5000", []reqOpt{withHost("127.0.0.1:8080")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var muts []func(*config.Config)
			if tc.mut != nil {
				muts = append(muts, tc.mut)
			}
			h := newHarness(t, muts...)
			opts := append([]reqOpt{fromAddr(tc.addr)}, tc.opts...)
			rec := h.do("GET", "/api/auth/me", nil, nil, opts...)
			if tc.allowed && rec.Code == http.StatusForbidden {
				t.Fatalf("refused: %s", rec.Body.String())
			}
			if !tc.allowed {
				expect(t, rec, http.StatusForbidden)
				if !strings.Contains(rec.Body.String(), "Tailscale") {
					t.Fatalf("no fix line: %s", rec.Body.String())
				}
				page := h.do("GET", "/", nil, nil, opts...)
				if page.Code != http.StatusForbidden || !strings.Contains(page.Header().Get("Content-Type"), "text/html") {
					t.Fatalf("SPA refusal = %d %s", page.Code, page.Header().Get("Content-Type"))
				}
			}
		})
	}
}

func TestDefaultGatewayIsLocal(t *testing.T) {
	h := newHarness(t)
	h.srv.gateways = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("172.17.0.1")} }
	if rec := h.do("GET", "/healthz", nil, nil, fromAddr("172.17.0.1:5000")); rec.Code != http.StatusOK {
		t.Fatalf("docker gateway = %d", rec.Code)
	}
	if rec := h.do("GET", "/healthz", nil, nil, fromAddr("172.17.0.5:5000")); rec.Code != http.StatusForbidden {
		t.Fatalf("other bridge peer = %d", rec.Code)
	}
}

func TestParseRoutes(t *testing.T) {
	v4 := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	got := parseRoute4(bufio.NewScanner(strings.NewReader(v4)))
	if len(got) != 1 || got[0] != netip.MustParseAddr("172.17.0.1") {
		t.Fatalf("route4 = %v", got)
	}
	v6 := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003 eth0\n"
	got = parseRoute6(bufio.NewScanner(strings.NewReader(v6)))
	if len(got) != 1 || got[0] != netip.MustParseAddr("fe80::1") {
		t.Fatalf("route6 = %v", got)
	}
}

func TestForwardedForOnlyFromTrustedProxies(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Server.TrustedProxies = []string{"10.0.0.0/24"} }).setup()
	// The audit IP of a login shows which address the server believed.
	login := func(opts ...reqOpt) string {
		t.Helper()
		before := len(h.audit.All())
		rec := h.do("POST", "/api/auth/login", dto.LoginReq{Username: "owner", Password: testPassword}, nil, opts...)
		expect(t, rec, http.StatusOK)
		for _, e := range h.audit.All()[before:] {
			if e.Action == "auth.login" {
				return e.IP
			}
		}
		t.Fatal("no auth.login audit row")
		return ""
	}
	if d := login(fromAddr("127.0.0.1:1"), withHeader("X-Forwarded-For", "198.51.100.1")); !strings.Contains(d, "127.0.0.1") {
		t.Fatalf("untrusted peer's XFF honoured: %s", d)
	}
	if d := login(fromAddr("10.0.0.5:1"), withHeader("X-Forwarded-For", "198.51.100.1, 10.0.0.9"), withHeader("X-Forwarded-Proto", "https")); !strings.Contains(d, "198.51.100.1") {
		t.Fatalf("right-most untrusted hop not used: %s", d)
	}
	if d := login(fromAddr("10.0.0.5:1"), withHeader("X-Forwarded-For", "6.6.6.6, 198.51.100.7"), withHeader("X-Forwarded-Proto", "https")); !strings.Contains(d, "198.51.100.7") {
		t.Fatalf("spoofed left-most hop used: %s", d)
	}
}

func TestQueryCredentialsRejected(t *testing.T) {
	h := newHarness(t).setup()
	for _, q := range []string{"token=x", "access_token=x", "api_key=x", "key=x", "auth=x", "password=x", "cookie=x", "gatewayUrl=x", "TOKEN=x"} {
		rec := h.do("GET", "/api/auth/me?"+q, nil, h.owner)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d", q, rec.Code)
		}
		if rec := h.do("GET", "/v1/models?"+q, nil, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("/v1 ?%s = %d", q, rec.Code)
		}
	}
	expect(t, h.do("GET", "/api/sessions?q=token", nil, h.owner), http.StatusOK)
	expect(t, h.do("GET", "/?token=x", nil, nil), http.StatusOK)
}

func TestCookieFlags(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Server.TrustedProxies = []string{"10.0.0.2/32"} }).setup()
	body := dto.LoginReq{Username: "owner", Password: testPassword}
	check := func(rec *httptest.ResponseRecorder, name string, secure bool) {
		t.Helper()
		expect(t, rec, http.StatusOK)
		cs := rec.Result().Cookies()
		if len(cs) != 1 {
			t.Fatalf("cookies = %v", cs)
		}
		c := cs[0]
		if c.Name != name || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure != secure || c.MaxAge <= 0 || len(c.Value) != 43 {
			t.Fatalf("cookie = %+v", c)
		}
	}
	check(h.do("POST", "/api/auth/login", body, nil), "bothy_session", false)
	check(h.do("POST", "/api/auth/login", body, nil, overTLS(), fromAddr("203.0.113.4:1")), "__Host-bothy", true)
	rec := h.do("POST", "/api/auth/login", body, nil, fromAddr("10.0.0.2:1"), withHeader("X-Forwarded-For", "203.0.113.4"),
		withHeader("X-Forwarded-Proto", "https"))
	check(rec, "__Host-bothy", true)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS missing under trusted-proxy https")
	}
	if h.do("GET", "/healthz", nil, nil).Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS on plain HTTP")
	}
	// A plain-HTTP cookie is not accepted over TLS and vice versa.
	cookie := rec.Result().Cookies()[0].Value
	req := h.request("GET", "/api/auth/me", nil, nil, overTLS())
	req.AddCookie(&http.Cookie{Name: cookiePlain, Value: cookie})
	r2 := httptest.NewRecorder()
	h.h.ServeHTTP(r2, req)
	expect(t, r2, http.StatusUnauthorized)
}

func TestCSRF(t *testing.T) {
	h := newHarness(t).setup()
	body := dto.SessionCreateReq{}
	expect(t, h.do("POST", "/api/sessions", body, h.owner, noCSRF()), http.StatusForbidden)
	expect(t, h.do("POST", "/api/sessions", body, h.owner, withHeader("X-CSRF-Token", h.owner.csrf+"x")), http.StatusForbidden)
	expect(t, h.do("POST", "/api/sessions", body, h.owner), http.StatusOK)
	// Cross-site requests are refused for cookies, bearer tokens and login.
	cross := withHeader("Sec-Fetch-Site", "cross-site")
	expect(t, h.do("POST", "/api/sessions", body, h.owner, cross), http.StatusForbidden)
	tok := h.tokenFor(h.owner, "chat")
	expect(t, h.do("POST", "/api/sessions", body, tok, cross), http.StatusForbidden)
	expect(t, h.do("POST", "/api/sessions", body, tok), http.StatusOK)
	rec := h.do("POST", "/api/auth/login", dto.LoginReq{Username: "owner", Password: testPassword}, nil, cross)
	expect(t, rec, http.StatusForbidden)
	rec = h.do("POST", "/api/auth/login", dto.LoginReq{Username: "owner", Password: testPassword}, nil,
		withHeader("Origin", "https://evil.example"))
	expect(t, rec, http.StatusForbidden)
	expect(t, h.do("POST", "/api/sessions", body, h.owner, withHeader("Sec-Fetch-Site", "same-origin")), http.StatusOK)
	// Safe methods never need the token.
	expect(t, h.do("GET", "/api/sessions", nil, h.owner, noCSRF()), http.StatusOK)
}

func TestLoginLockoutOverHTTP(t *testing.T) {
	h := newHarness(t).setup()
	for i := 0; i < 5; i++ {
		rec := h.do("POST", "/api/auth/login", dto.LoginReq{Username: "member", Password: "wrong password!!"}, nil,
			fromAddr("127.0.0."+string(rune('2'+i))+":1"))
		expect(t, rec, http.StatusUnauthorized)
		if errCode(t, rec) != "unauthorized" || !strings.Contains(rec.Body.String(), "invalid username or password") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	}
	rec := h.do("POST", "/api/auth/login", dto.LoginReq{Username: "member", Password: testPassword}, nil, fromAddr("127.0.0.9:1"))
	expect(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" || errCode(t, rec) != "rate_limited" {
		t.Fatalf("locked response = %v %s", rec.Header(), rec.Body.String())
	}
	// Unknown users get the same answer as wrong passwords.
	rec = h.do("POST", "/api/auth/login", dto.LoginReq{Username: "nobody", Password: "wrong password!!"}, nil, fromAddr("127.0.0.50:1"))
	expect(t, rec, http.StatusUnauthorized)
	if !strings.Contains(rec.Body.String(), "invalid username or password") {
		t.Fatalf("unknown user body = %s", rec.Body.String())
	}
}

func TestLoginRateLimitPerIP(t *testing.T) {
	h := newHarness(t).setup()
	for i := 0; i < loginsPerMin; i++ {
		expect(t, h.do("POST", "/api/auth/login", dto.LoginReq{Username: "owner", Password: testPassword}, nil, fromAddr("127.0.0.77:1")), http.StatusOK)
	}
	rec := h.do("POST", "/api/auth/login", dto.LoginReq{Username: "owner", Password: testPassword}, nil, fromAddr("127.0.0.77:1"))
	expect(t, rec, http.StatusTooManyRequests)
	expect(t, h.do("POST", "/api/auth/login", dto.LoginReq{Username: "owner", Password: testPassword}, nil, fromAddr("127.0.0.78:1")), http.StatusOK)
}

func TestSetupRoutes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tok, _, err := h.auth.EnsureSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st := decodeAs[dto.SetupState](t, h.do("GET", "/api/setup/state", nil, nil))
	if !st.NeedsSetup || st.Claimed || st.OwnerSet || !st.Sandbox {
		t.Fatalf("state = %+v", st)
	}
	if body := h.do("GET", "/api/setup/state", nil, nil).Body.String(); strings.Contains(body, "version") || strings.Contains(body, "test") {
		t.Fatalf("pre-auth state discloses the version: %s", body)
	}
	remote := fromAddr("192.168.1.5:1")
	for _, p := range []string{"/api/setup/state"} {
		rec := h.do("GET", p, nil, nil, remote, overTLS())
		expect(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), "ssh -L") {
			t.Fatalf("fix line missing: %s", rec.Body.String())
		}
	}
	expect(t, h.do("POST", "/api/setup/claim", dto.SetupClaimReq{Token: tok}, nil, remote, overTLS()), http.StatusForbidden)
	expect(t, h.do("POST", "/api/setup/claim", dto.SetupClaimReq{Token: "nope"}, nil), http.StatusUnauthorized)
	rec := h.do("POST", "/api/setup/claim", dto.SetupClaimReq{Token: tok}, nil)
	expect(t, rec, http.StatusOK)
	claim := decodeAs[dto.SetupClaimResp](t, rec)
	var grant *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieSetup {
			grant = c
		}
	}
	if grant == nil || !grant.HttpOnly || grant.SameSite != http.SameSiteStrictMode || grant.MaxAge != 1800 || grant.Path != "/api/setup" {
		t.Fatalf("grant cookie = %+v", grant)
	}
	// Single use: the same token cannot be claimed twice.
	expect(t, h.do("POST", "/api/setup/claim", dto.SetupClaimReq{Token: tok}, nil), http.StatusUnauthorized)
	withGrant := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieSetup, Value: grant.Value}) }
	st = decodeAs[dto.SetupState](t, h.do("GET", "/api/setup/state", nil, nil, withGrant))
	if !st.Claimed || st.CSRF != claim.CSRF {
		t.Fatalf("state with grant = %+v, want claimed with the claim's CSRF token", st)
	}
	if st := decodeAs[dto.SetupState](t, h.do("GET", "/api/setup/state", nil, nil)); st.CSRF != "" {
		t.Fatalf("CSRF token disclosed without the grant cookie: %+v", st)
	}
	owner := dto.SetupOwnerReq{Username: "owner", Password: testPassword, Timezone: "Europe/Madrid"}
	expect(t, h.do("POST", "/api/setup/owner", owner, nil, withGrant), http.StatusForbidden) // no CSRF token
	expect(t, h.do("POST", "/api/setup/owner", owner, nil), http.StatusUnauthorized)         // no grant
	rec = h.do("POST", "/api/setup/owner", owner, nil, withGrant, withHeader("X-CSRF-Token", claim.CSRF))
	expect(t, rec, http.StatusOK)
	me := decodeAs[dto.Me](t, rec)
	if me.User.Role != core.RoleOwner || me.CSRF == "" || len(me.Principals) != 1 {
		t.Fatalf("me = %+v", me)
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookiePlain {
			session = c.Value
		}
	}
	owner2 := &client{name: "owner", user: me.User.ID, cookie: session, csrf: me.CSRF}
	expect(t, h.do("POST", "/api/setup/complete", nil, owner2, remote, overTLS()), http.StatusForbidden)
	expect(t, h.do("POST", "/api/setup/complete", nil, owner2), http.StatusNoContent)
	for _, p := range []string{"GET /api/setup/state", "POST /api/setup/claim", "POST /api/setup/owner", "POST /api/setup/complete"} {
		m, path, _ := strings.Cut(p, " ")
		if rec := h.do(m, path, nil, owner2); rec.Code != http.StatusNotFound {
			t.Errorf("%s after completion = %d", p, rec.Code)
		}
	}
}

func TestSetupClaimRateLimit(t *testing.T) {
	h := newHarness(t)
	var last int
	for i := 0; i < claimsPerMin+1; i++ {
		last = h.do("POST", "/api/setup/claim", dto.SetupClaimReq{Token: "wrong"}, nil).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("6th claim = %d", last)
	}
}

func TestTokenRateLimit(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	a, _ := h.auth.FromCookie(ctx, h.owner.cookie)
	plain, _, err := h.auth.CreateToken(ctx, a, core.TokenSpec{Name: "slow", Scopes: []string{"read"}, TTL: 3600e9, RatePerMin: 2})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{name: "slow", token: plain, scopes: []string{"read"}}
	expect(t, h.do("GET", "/api/sessions", nil, c), http.StatusOK)
	expect(t, h.do("GET", "/api/sessions", nil, c), http.StatusOK)
	rec := h.do("GET", "/api/sessions", nil, c)
	expect(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
}

func TestPanicRecovery(t *testing.T) {
	h := newHarness(t).setup()
	h.srv.d.Runner = panicky{}
	rec := h.do("GET", "/api/constitution", nil, h.owner)
	expect(t, rec, http.StatusInternalServerError)
	if errCode(t, rec) != "internal" || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

type panicky struct{ core.Runner }

func (panicky) Constitution() (string, string) { panic("boom") }
