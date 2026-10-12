package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Credentials are never read from query strings (CVE-2026-25253 class).
var credentialParams = []string{"token", "access_token", "api_key", "key", "auth", "password", "cookie", "gatewayurl"}

const (
	cookieSecure = "__Host-bothy"
	cookiePlain  = "bothy_session"
	cookieSetup  = "bothy_setup"
	maxUserSSE   = 4
)

type handlerFunc func(w http.ResponseWriter, r *http.Request, a core.Actor) error

type access uint8

const (
	accessNone       access = iota // anyone
	accessLocal                    // anyone on a local peer (setup state and claim)
	accessSetup                    // setup grant cookie, local peers
	accessUser                     // cookie, or bearer chat (read covers GET)
	accessUserCookie               // cookie only
	accessRead                     // cookie or bearer read
	accessAdmin                    // owner/admin cookie or bearer admin
	accessV1                       // bearer chat or llm
)

type route struct {
	pattern string
	access  access
	h       handlerFunc
	setup   bool  // 404 once setup is complete; local peers only
	safe    bool  // still served in safe mode
	limit   int64 // body cap; 0 = server.max_body_mb, -1 = server.upload_max_mb
}

func isAPI(path string) bool {
	return path == "/api" || path == "/v1" || strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/v1/")
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// build assembles the outer chain: request id, panic recovery, security
// headers, Host check, exposure gate and query-credential rejection, in
// front of the mux. Body limit, authentication, CSRF, authorization and rate
// limiting run per route in wrap.
func (s *Server) build() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes {
		mux.Handle(rt.pattern, s.wrap(rt))
	}
	notFound := s.wrap(route{access: accessNone, safe: true, h: func(http.ResponseWriter, *http.Request, core.Actor) error {
		return errNotFound
	}})
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		mux.Handle(m+" /api/", notFound)
		mux.Handle(m+" /v1/", notFound)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := &reqInfo{id: requestID()}
		ri.ip, ri.secure = s.peer(r)
		ri.class = s.classify(ri.ip)
		r = r.WithContext(withInfo(r.Context(), ri))
		w.Header().Set("X-Request-Id", ri.id)
		defer s.recoverPanic(w, r)
		s.securityHeaders(w, r, ri)
		if !s.hostAllowed(r.Host) {
			s.writeError(w, r, newError(http.StatusMisdirectedRequest, "bad_request", "unknown Host header").
				withFix("add this host name to server.allowed_hosts or server.public_url"))
			return
		}
		if !ri.secure && !s.plainAllowed(ri.class) {
			s.refuseInsecure(w, r)
			return
		}
		if isAPI(r.URL.Path) {
			for k := range r.URL.Query() {
				for _, bad := range credentialParams {
					if strings.EqualFold(k, bad) {
						s.writeError(w, r, badRequest("credentials are never accepted in the query string (%s)", k))
						return
					}
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// plainAllowed: plain HTTP goes to local peers, to LAN peers only with the
// locked insecure_lan, and never to public peers.
func (s *Server) plainAllowed(c peerClass) bool {
	return c == classLocal || c == classLAN && s.cfg().Server.InsecureLAN
}

func requestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) recoverPanic(w http.ResponseWriter, r *http.Request) {
	p := recover()
	if p == nil {
		return
	}
	if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(p)
	}
	s.log.Error("handler panic", "method", r.Method, "path", r.URL.Path, "request_id", infoOf(r).id, "panic", fmt.Sprint(p))
	s.writeError(w, r, errors.New("panic"))
}

func (s *Server) securityHeaders(w http.ResponseWriter, r *http.Request, ri *reqInfo) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if isAPI(r.URL.Path) || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		h.Set("Cache-Control", "no-store")
	}
	if ri.secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

const insecureFix = "Use HTTPS: put Bothy behind Caddy or Tailscale (tailscale serve) and list the proxy in server.trusted_proxies, " +
	"or set server.tls. From this machine, tunnel instead: ssh -L 8080:127.0.0.1:8080 <host>, then open http://127.0.0.1:8080."

// refuseInsecure answers a plain-HTTP request from a lan or public peer.
func (s *Server) refuseInsecure(w http.ResponseWriter, r *http.Request) {
	msg := "plain HTTP is only served to this machine"
	if isAPI(r.URL.Path) {
		s.writeError(w, r, newError(http.StatusForbidden, "forbidden", msg).withFix(insecureFix))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Bothy: HTTPS required</title></head>"+
		"<body><h1>HTTPS required</h1><p>Bothy %s.</p><p>%s</p></body></html>\n", html.EscapeString(msg), html.EscapeString(insecureFix))
}

func (s *Server) uploadLimit() int64 { return int64(max(s.cfg().Server.UploadMaxMB, 1)) << 20 }

func (s *Server) cookieName(r *http.Request) string {
	if infoOf(r).secure {
		return cookieSecure
	}
	return cookiePlain
}

// setCookie always sets HttpOnly and SameSite=Strict. Secure follows the
// transport: plain HTTP reaches only local (or opted-in LAN) peers, where a
// Secure cookie would never be sent back.
func setCookie(w http.ResponseWriter, r *http.Request, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, //nolint:gosec // Secure is set per transport, see above
		Secure: infoOf(r).secure, SameSite: http.SameSiteStrictMode})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	maxAge := int(s.cfg().Auth.SessionMax.D().Seconds())
	if maxAge <= 0 {
		maxAge = 30 * 24 * 3600
	}
	setCookie(w, r, s.cookieName(r), value, "/", maxAge)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	setCookie(w, r, s.cookieName(r), "", "/", -1)
}

func (s *Server) setSetupCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	setCookie(w, r, cookieSetup, value, "/api/setup", maxAge)
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// setupGrants is the auth.Service extra that validates the setup grant.
type setupGrants interface {
	SetupGrant(ctx context.Context, grant string) (csrf string, err error)
}

// wrap runs the per-route part of the chain (SPEC §8.1).
func (s *Server) wrap(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := rt.limit
		switch {
		case limit < 0:
			limit = s.uploadLimit() + 64<<10 // multipart framing
		case limit == 0:
			limit = int64(max(s.cfg().Server.MaxBodyMB, 1)) << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if err := s.gate(w, r, rt); err != nil {
			s.writeError(w, r, err)
			return
		}
		a, err := s.authenticate(r, rt)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if err := s.csrf(r, rt, a); err != nil {
			s.writeError(w, r, err)
			return
		}
		if err := authorize(r, rt, a); err != nil {
			s.writeError(w, r, err)
			return
		}
		if a.Via == "token" && a.RatePerMin > 0 {
			if ok, retry := s.tokens.allow(a.TokenID, a.RatePerMin, a.RatePerMin); !ok {
				e := newError(http.StatusTooManyRequests, "rate_limited", "token rate limit exceeded")
				e.retry = retry
				s.writeError(w, r, e)
				return
			}
		}
		if err := rt.h(w, r, a); err != nil {
			s.writeError(w, r, err)
		}
	})
}

// gate applies safe mode and the setup-route rules before authentication.
func (s *Server) gate(w http.ResponseWriter, r *http.Request, rt route) error {
	if !rt.safe && isAPI(r.URL.Path) && s.safeMode() {
		return errSafeMode
	}
	if rt.access == accessV1 && !s.cfg().API.OpenAICompat {
		return errNotFound
	}
	if rt.setup || rt.access == accessLocal || rt.access == accessSetup {
		if rt.setup {
			need, err := s.d.Auth.NeedsSetup(r.Context())
			if err != nil {
				return err
			}
			if !need {
				return errNotFound
			}
		}
		if infoOf(r).class != classLocal {
			return newError(http.StatusForbidden, "forbidden", "setup is only available from this machine").
				withFix("ssh -L 8080:127.0.0.1:8080 <host>, then open the printed setup URL on http://127.0.0.1:8080")
		}
	}
	return nil
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, tok, found := strings.Cut(h, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(tok), true
}

func (s *Server) authenticate(r *http.Request, rt route) (core.Actor, error) {
	ctx := r.Context()
	switch rt.access {
	case accessNone, accessLocal:
		return core.Actor{}, nil
	case accessSetup:
		sg, ok := s.d.Auth.(setupGrants)
		if !ok {
			return core.Actor{}, core.ErrNotImplemented
		}
		csrf, err := sg.SetupGrant(ctx, cookieValue(r, cookieSetup))
		if err != nil {
			return core.Actor{}, err
		}
		return core.Actor{Via: "setup", CSRF: csrf}, nil
	case accessV1:
		tok, present := bearer(r)
		if !present || tok == "" {
			return core.Actor{}, newError(http.StatusUnauthorized, "unauthorized", "missing bearer token")
		}
		return s.d.Auth.FromBearer(ctx, tok)
	}
	if tok, present := bearer(r); present {
		if tok == "" {
			return core.Actor{}, newError(http.StatusUnauthorized, "unauthorized", "malformed Authorization header")
		}
		return s.d.Auth.FromBearer(ctx, tok)
	}
	c := cookieValue(r, s.cookieName(r))
	if c == "" {
		return core.Actor{}, errUnauthorized
	}
	return s.d.Auth.FromCookie(ctx, c)
}

// csrf: Go's cross-origin protection guards every unsafe /api and /v1
// request (login, setup and bearer requests included); cookie and setup
// sessions also need their synchroniser token in X-CSRF-Token.
func (s *Server) csrf(r *http.Request, rt route, a core.Actor) error {
	if !unsafeMethod(r.Method) || !isAPI(r.URL.Path) {
		return nil
	}
	if err := s.cop.Check(r); err != nil {
		return newError(http.StatusForbidden, "forbidden", "cross-origin request refused")
	}
	if a.Via != "cookie" && a.Via != "setup" {
		return nil
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" || a.CSRF == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.CSRF)) != 1 {
		return newError(http.StatusForbidden, "forbidden", "missing or invalid X-CSRF-Token").withFix("GET /api/auth/me returns the token")
	}
	return nil
}

func authorize(r *http.Request, rt route, a core.Actor) error {
	token := a.Via == "token"
	switch rt.access {
	case accessUser:
		readOnly := a.HasScope("read") && !unsafeMethod(r.Method)
		if token && !a.HasScope("chat") && !readOnly {
			return newError(http.StatusForbidden, "forbidden", "this token lacks the chat scope")
		}
	case accessUserCookie:
		if token {
			return errCookieOnly
		}
	case accessRead:
		if token && !a.HasScope("read") {
			return newError(http.StatusForbidden, "forbidden", "this token lacks the read scope")
		}
	case accessAdmin:
		if !a.Role.IsAdmin() || !a.HasScope("admin") {
			return newError(http.StatusForbidden, "forbidden", "admin only")
		}
	case accessV1:
		if !a.HasScope("chat") && !a.HasScope("llm") {
			return newError(http.StatusForbidden, "forbidden", "this token lacks the chat and llm scopes")
		}
	}
	return nil
}

// filterFor is the SSE and banner ACL: the actor's principals, plus admin
// broadcasts for owners and admins. Admins never see other principals'
// content (admin_can_read_chats is locked off).
func filterFor(a core.Actor) core.Filter {
	return core.Filter{Principals: a.Viewer.Principals, Admin: a.Role.IsAdmin()}
}
