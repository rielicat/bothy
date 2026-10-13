package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

var wildcardRE = regexp.MustCompile(`\{[a-z]+(\.\.\.)?\}`)

func concretePath(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	path = wildcardRE.ReplaceAllStringFunc(path, func(w string) string {
		if strings.HasSuffix(w, "...}") {
			return "a/b"
		}
		return "x"
	})
	return method, path
}

// expected returns 401 or 403 when the access class must refuse the actor,
// or 0 when it must be let through to the handler.
func expected(rt route, method string, c *client) int {
	if rt.access == accessNone {
		return 0
	}
	if c == nil {
		return http.StatusUnauthorized
	}
	token := c.token != ""
	has := func(s string) bool { return slices.Contains(c.scopes, s) }
	admin := c.name == "owner" || c.name == "admin" || strings.HasPrefix(c.name, "owner/")
	switch rt.access {
	case accessUser:
		if token && !has("chat") && !(has("read") && method == http.MethodGet) {
			return http.StatusForbidden
		}
	case accessUserCookie:
		if token {
			return http.StatusForbidden
		}
	case accessRead:
		if token && !has("read") {
			return http.StatusForbidden
		}
	case accessAdmin:
		if !admin || token && !has("admin") {
			return http.StatusForbidden
		}
	case accessV1:
		if !token {
			return http.StatusUnauthorized
		}
		if !has("chat") && !has("llm") {
			return http.StatusForbidden
		}
	}
	return 0
}

func TestAuthzMatrix(t *testing.T) {
	h := newHarness(t).setup()
	actors := []*client{nil, h.member, h.admin, h.owner,
		h.tokenFor(h.owner, "chat"), h.tokenFor(h.owner, "llm"), h.tokenFor(h.owner, "read"), h.tokenFor(h.owner, "admin"),
		h.tokenFor(h.member, "chat"), h.tokenFor(h.member, "read")}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, rt := range h.srv.routes {
		if rt.setup {
			continue // TestSetupRoutes covers the setup-only rules
		}
		method, path := concretePath(rt.pattern)
		for _, c := range actors {
			name := "anonymous"
			if c != nil {
				name = c.name
			}
			t.Run(rt.pattern+"/"+name, func(t *testing.T) {
				var opts []reqOpt
				if path == "/api/events" {
					opts = append(opts, withCtx(canceled))
				}
				if rt.pattern == "POST /api/auth/logout" && c != nil && c.cookie != "" {
					c = h.newUser(c.name+"-logout-"+strings.ReplaceAll(t.Name()[len(t.Name())-6:], "/", ""), core.RoleMember)
				}
				rec := h.do(method, path, nil, c, opts...)
				// Downloads carry the stricter "sandbox" policy instead.
				if got := rec.Header().Get("Content-Security-Policy"); got != csp && got != "sandbox" {
					t.Errorf("CSP = %q", got)
				}
				if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Error("missing nosniff")
				}
				for k := range rec.Header() {
					if strings.HasPrefix(strings.ToLower(k), "access-control-") {
						t.Errorf("CORS header %s emitted", k)
					}
				}
				want := expected(rt, method, c)
				switch {
				case want != 0 && rec.Code != want:
					t.Fatalf("status = %d, want %d (body %s)", rec.Code, want, rec.Body.String())
				case want == 0 && (rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden):
					t.Fatalf("refused with %d, want the handler to run (body %s)", rec.Code, rec.Body.String())
				case rec.Code >= 500 && rec.Code != http.StatusServiceUnavailable:
					t.Fatalf("server error %d: %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestApprovalResolveRefusesEveryToken(t *testing.T) {
	h := newHarness(t).setup()
	act := &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: core.SELocalWrite, Argv: []string{"ls"}, Payload: []byte(`{}`)}
	ap, err := h.approvals.Request(context.Background(), core.ApprovalRequest{Run: "run_x", Principal: core.UserPrincipal(h.owner.user), Action: act,
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{User: h.owner.user}}})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"choice": "allow_once"}
	for _, scopes := range [][]string{{"chat"}, {"read"}, {"llm"}, {"admin"}, {"chat", "read", "llm", "admin"}} {
		rec := h.do("POST", "/api/approvals/"+string(ap.ID), body, h.tokenFor(h.owner, scopes...))
		expect(t, rec, http.StatusForbidden)
	}
	rec := h.do("POST", "/api/approvals/"+string(ap.ID), body, h.owner)
	expect(t, rec, http.StatusOK)
	if got := decodeAs[core.Approval](t, rec); got.Status != core.ApprovalAllowed || got.DecidedVia != "web" {
		t.Fatalf("approval = %+v", got)
	}
}

func TestSafeModeGating(t *testing.T) {
	h := newHarness(t).setup()
	h.cfg.eff.SafeMode = true
	admin := h.tokenFor(h.owner, "admin", "chat", "read", "llm")
	for _, rt := range h.srv.routes {
		method, path := concretePath(rt.pattern)
		if !isAPI(path) {
			continue
		}
		c := admin
		if rt.access == accessUserCookie || rt.access == accessSetup || path == "/api/auth/logout" {
			c = h.admin
		}
		opts := []reqOpt{}
		if path == "/api/events" {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			opts = append(opts, withCtx(ctx))
		}
		rec := h.do(method, path, nil, c, opts...)
		if rt.safe {
			if rec.Code == http.StatusServiceUnavailable && errCode(t, rec) == "unavailable" && strings.Contains(rec.Body.String(), "safe mode") {
				t.Errorf("%s: safe route answered safe-mode 503", rt.pattern)
			}
			continue
		}
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "safe mode") {
			t.Errorf("%s: %d %s, want 503 safe mode", rt.pattern, rec.Code, rec.Body.String())
		}
		// The distroless image has no bothy on PATH: the fix names /bothy too.
		if strings.HasPrefix(path, "/api/") && !strings.Contains(rec.Body.String(), "docker compose exec bothy /bothy config check") {
			t.Errorf("%s: fix in %s", rt.pattern, rec.Body.String())
		}
	}
	// Unknown /api paths and the SPA still behave normally.
	expect(t, h.do("GET", "/", nil, nil), http.StatusOK)
	expect(t, h.do("GET", "/healthz", nil, nil), http.StatusOK)
	expect(t, h.do("GET", "/readyz", nil, nil), http.StatusServiceUnavailable)
}
