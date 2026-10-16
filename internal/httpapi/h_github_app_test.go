package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

const (
	ghAppTestID   = "gha_01m4wa97e4s0p3qgcng95bcyhf"
	ghTestInstall = "https://github.com/apps/bothy-acme/installations/new"
	ghTestCode    = "c0ffee0123456789c0ffee0123456789c0ffee01"
)

type ghCreate struct {
	code     string
	target   core.GitHubAppTarget
	replaces string
	by       core.UserID
}

type ghInstall struct {
	app  string
	id   int64
	by   core.UserID
	from string // the audit origin's actor
}

// fakeMaker is fakeGitHub plus core.GitHubAppMaker, recording what the
// handlers pass.
type fakeMaker struct {
	*fakeGitHub
	mu                                 sync.Mutex
	apps                               []*core.GitHubOwnApp
	manifestErr, createErr, installErr error
	checkErr, retireErr, deleteErr     error
	checkConn                          *core.GitHubConnection
	connApp                            *core.GitHubOwnApp
	targets                            []core.GitHubAppTarget
	origins                            []string
	creates                            []ghCreate
	installs                           []ghInstall
	checked, retired, deleted          []string
}

func newFakeMaker() *fakeMaker { return &fakeMaker{fakeGitHub: &fakeGitHub{}} }

func (m *fakeMaker) AppManifest(t core.GitHubAppTarget, origin string) (*core.GitHubAppHandoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets, m.origins = append(m.targets, t), append(m.origins, origin)
	if m.manifestErr != nil {
		return nil, m.manifestErr
	}
	action := "https://github.com/settings/apps/new"
	if t.Org != "" {
		action = "https://github.com/organizations/" + t.Org + "/settings/apps/new"
	}
	return &core.GitHubAppHandoff{Action: action, WebOrigin: "https://github.com", Name: "Bothy (acme)", Org: t.Org,
		Manifest: `{"name":"Bothy (acme)","url":"` + origin + `","description":"a <b> & 'c'"}`}, nil
}

func (m *fakeMaker) CreateApp(_ context.Context, code string, t core.GitHubAppTarget, replaces string, by core.UserID) (*core.GitHubOwnApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creates = append(m.creates, ghCreate{code, t, replaces, by})
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &core.GitHubOwnApp{ID: ghAppTestID, Slug: "bothy-acme", Name: "Bothy (acme)", State: "pending", InstallURL: ghTestInstall}, nil
}

func (m *fakeMaker) InstallApp(ctx context.Context, id string, inst int64, by core.UserID) (*core.GitHubConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, _ := core.AuditOriginFrom(ctx)
	m.installs = append(m.installs, ghInstall{id, inst, by, o.Actor})
	if m.installErr != nil {
		return nil, m.installErr
	}
	return m.conn(), nil
}

func (m *fakeMaker) CheckApp(_ context.Context, id string, _ core.UserID) (*core.GitHubConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checked = append(m.checked, id)
	return m.checkConn, m.checkErr
}

func (m *fakeMaker) Apps(context.Context) ([]*core.GitHubOwnApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.apps), nil
}

func (m *fakeMaker) ConnectionApp(_ context.Context, connID string) (*core.GitHubOwnApp, error) {
	if m.connApp == nil || connID != ghConnID {
		return nil, core.ErrNotFound
	}
	return m.connApp, nil
}

func (m *fakeMaker) RetireApp(_ context.Context, id string, _ core.UserID) (*core.GitHubOwnApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retired = append(m.retired, id)
	if m.retireErr != nil {
		return nil, m.retireErr
	}
	return &core.GitHubOwnApp{ID: id, State: "retired"}, nil
}

func (m *fakeMaker) DeleteApp(_ context.Context, id string, _ core.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, id)
	return m.deleteErr
}

func (m *fakeMaker) createCalls() []ghCreate {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.creates)
}

func (m *fakeMaker) installCalls() []ghInstall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.installs)
}

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }

// anon is a request with no session at all, as a cross-site redirect from
// GitHub arrives (the session cookie is SameSite=Strict).
func (h *harness) browser(path string, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodGet, path, nil, nil, opts...)
}

func flowCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieGHAppPlain || c.Name == cookieGHAppSecure {
			return c
		}
	}
	t.Fatalf("no flow cookie in %v", rec.Header().Values("Set-Cookie"))
	return nil
}

func landed(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || u.Path != "/settings/connections/github" || u.Host != "" {
		t.Fatalf("landed at %q", rec.Header().Get("Location"))
	}
	return u.Query()
}

var (
	actionRE   = regexp.MustCompile(`<form id="f" method="post" action="([^"]*)">`)
	manifestRE = regexp.MustCompile(`<input type="hidden" name="manifest" value="([^"]*)">`)
	nonceRE    = regexp.MustCompile(`nonce="([^"]+)"`)
)

// startFlow starts an organization app as c and opens the handoff page,
// returning the flow cookie and the state the page sends GitHub.
func (h *harness) startFlow(c *client) (*http.Cookie, string) {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/github/apps", dto.GitHubAppStartReq{Account: "organization", Org: "acme"}, c)
	expect(h.t, rec, http.StatusOK)
	if got := decodeAs[dto.GitHubAppStartResp](h.t, rec); got.HandoffURL != "/api/github/apps/handoff" {
		h.t.Fatalf("start = %+v", got)
	}
	fc := flowCookieOf(h.t, rec)
	rec = h.do(http.MethodGet, "/api/github/apps/handoff", nil, c, withCookie(fc))
	expect(h.t, rec, http.StatusOK)
	m := actionRE.FindStringSubmatch(rec.Body.String())
	if m == nil {
		h.t.Fatalf("no form in %s", rec.Body)
	}
	u, err := url.Parse(html.UnescapeString(m[1]))
	if err != nil || u.Query().Get("state") == "" {
		h.t.Fatalf("form action %q", m[1])
	}
	return fc, u.Query().Get("state")
}

// created runs GitHub's redirect after it made the app and returns the
// state it put on the install page's address.
func (h *harness) created(fc *http.Cookie, state string) string {
	h.t.Helper()
	rec := h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(state), withCookie(fc))
	if rec.Code != http.StatusSeeOther {
		h.t.Fatalf("created = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || u.Scheme+"://"+u.Host+u.Path != ghTestInstall || u.Query().Get("state") == "" {
		h.t.Fatalf("created sent the browser to %q", rec.Header().Get("Location"))
	}
	return u.Query().Get("state")
}

func TestGitHubAppStart(t *testing.T) {
	h := newHarness(t).setup()
	mk := newFakeMaker()
	h.srv.d.GitHub = mk
	start := dto.GitHubAppStartReq{Account: "organization", Org: "acme"}

	expect(t, h.do(http.MethodPost, "/api/github/apps", start, h.member), http.StatusForbidden)
	expect(t, h.do(http.MethodPost, "/api/github/apps", start, h.tokenFor(h.admin, "admin")), http.StatusForbidden)
	expect(t, h.do(http.MethodPost, "/api/github/apps", start, h.admin, noCSRF()), http.StatusForbidden)
	expect(t, h.do(http.MethodPost, "/api/github/apps", dto.GitHubAppStartReq{Account: "team"}, h.admin), http.StatusBadRequest)
	expect(t, h.do(http.MethodPost, "/api/github/apps", dto.GitHubAppStartReq{Account: "organization"}, h.admin), http.StatusBadRequest)
	mk.manifestErr = fmt.Errorf("%w: the app name must be 1 to 34 characters", core.ErrInvalid)
	expect(t, h.do(http.MethodPost, "/api/github/apps", start, h.admin), http.StatusBadRequest)
	mk.manifestErr = nil
	if rec := h.do(http.MethodPost, "/api/github/apps", dto.GitHubAppStartReq{Account: "organization", Org: "acme", Replaces: "ghc_gone"}, h.admin); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown replaces = %d %s", rec.Code, rec.Body)
	}

	h.vault.Seal()
	if rec := h.do(http.MethodPost, "/api/github/apps", start, h.admin); rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "sealed" {
		t.Errorf("sealed = %d %s", rec.Code, rec.Body)
	}
	_ = h.vault.Unseal(context.Background(), nil)

	mk.apps = []*core.GitHubOwnApp{{ID: ghAppTestID, Name: "Bothy (acme)", State: "pending"}}
	if rec := h.do(http.MethodPost, "/api/github/apps", start, h.admin); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "finish installing Bothy (acme)") {
		t.Errorf("with a pending app = %d %s", rec.Code, rec.Body)
	}
	mk.apps = []*core.GitHubOwnApp{{ID: ghAppTestID, Name: "Bothy (acme)", State: "retired"}}

	h.cfg.update(func(c *config.Config) { c.Server.PublicURL = "https://bothy.example.com" })
	rec := h.do(http.MethodPost, "/api/github/apps", start, h.admin)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "wrong_address" || !strings.Contains(rec.Body.String(), `"fix":"https://bothy.example.com"`) {
		t.Errorf("another address = %d %s", rec.Code, rec.Body)
	}
	h.cfg.update(func(c *config.Config) { c.Server.PublicURL = "http://127.0.0.1:8080" })

	rec = h.do(http.MethodPost, "/api/github/apps", dto.GitHubAppStartReq{Account: "personal", Org: "ignored", Name: " Our bot ", Replaces: ghConnID}, h.admin)
	expect(t, rec, http.StatusOK)
	sc := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(sc, "bothy_ghapp=") || !strings.Contains(sc, "Path=/") || !strings.Contains(sc, "Max-Age=3600") ||
		!strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Lax") || strings.Contains(sc, "Secure") {
		t.Errorf("flow cookie = %q", sc)
	}
	if got := mk.targets[len(mk.targets)-1]; got != (core.GitHubAppTarget{Name: "Our bot", Fallback: "admin"}) || mk.origins[len(mk.origins)-1] != "http://127.0.0.1:8080" {
		t.Errorf("manifest asked for %+v at %v", got, mk.origins)
	}
	if strings.Contains(rec.Body.String(), "state") {
		t.Errorf("the state is in the response: %s", rec.Body)
	}

	tls := newHarness(t, func(c *config.Config) { c.Server.TrustedProxies = []string{"10.0.0.2/32"} }).setup()
	tls.srv.d.GitHub = newFakeMaker()
	rec = tls.do(http.MethodPost, "/api/github/apps", start, tls.admin, fromAddr("10.0.0.2:1"), withHeader("X-Forwarded-For", "203.0.113.4"),
		withHeader("X-Forwarded-Proto", "https"))
	expect(t, rec, http.StatusOK)
	if c := flowCookieOf(t, rec); c.Name != "__Host-bothy_ghapp" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("TLS flow cookie = %+v", c)
	}
}

func TestGitHubAppHandoff(t *testing.T) {
	h := newHarness(t).setup()
	mk := newFakeMaker()
	h.srv.d.GitHub = mk
	other := h.newUser("admin2", core.RoleAdmin)

	rec := h.do(http.MethodPost, "/api/github/apps", dto.GitHubAppStartReq{Account: "organization", Org: "acme"}, h.admin)
	expect(t, rec, http.StatusOK)
	fc := flowCookieOf(t, rec)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no flow cookie":    h.do(http.MethodGet, "/api/github/apps/handoff", nil, h.admin),
		"another admin":     h.do(http.MethodGet, "/api/github/apps/handoff", nil, other, withCookie(fc)),
		"no session":        h.browser("/api/github/apps/handoff", withCookie(fc)),
		"unknown binding":   h.do(http.MethodGet, "/api/github/apps/handoff", nil, h.admin, withCookie(&http.Cookie{Name: cookieGHAppPlain, Value: "x"})),
		"the owner instead": h.do(http.MethodGet, "/api/github/apps/handoff", nil, h.owner, withCookie(fc)),
	} {
		if q := landed(t, rec); q.Get("github_app") != "expired" || q.Has("apps") {
			t.Errorf("%s: landed %v", name, q)
		}
	}

	var nonces []string
	for range 2 {
		rec := h.do(http.MethodGet, "/api/github/apps/handoff", nil, h.admin, withCookie(fc))
		expect(t, rec, http.StatusOK)
		body := rec.Body.String()
		ns := nonceRE.FindAllStringSubmatch(body, -1)
		if len(ns) != 2 || ns[0][1] != ns[1][1] {
			t.Fatalf("nonces = %v", ns)
		}
		n := ns[0][1]
		nonces = append(nonces, n)
		want := "default-src 'none'; script-src 'nonce-" + n + "'; style-src 'nonce-" + n + "'; form-action https://github.com; base-uri 'none'; frame-ancestors 'none'"
		if got := rec.Header().Get("Content-Security-Policy"); got != want {
			t.Errorf("CSP = %q\nwant  %q", got, want)
		}
		if rec.Header().Get("Referrer-Policy") != "strict-origin" || rec.Header().Get("Cache-Control") != "no-store" ||
			rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("headers = %v", rec.Header())
		}
		m := actionRE.FindStringSubmatch(body)
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil || u.Scheme+"://"+u.Host+u.Path != "https://github.com/organizations/acme/settings/apps/new" || len(u.Query().Get("state")) < 40 {
			t.Errorf("form action = %q", m[1])
		}
		mf := manifestRE.FindStringSubmatch(body)
		if mf == nil || html.UnescapeString(mf[1]) != `{"name":"Bothy (acme)","url":"http://127.0.0.1:8080","description":"a <b> & 'c'"}` {
			t.Errorf("manifest field = %v", mf)
		}
		if !strings.Contains(body, `<button type="submit">Continue on GitHub</button>`) || !strings.Contains(body, `document.getElementById("f").submit()`) ||
			!strings.Contains(body, "to create Bothy (acme) for acme.") {
			t.Errorf("page:\n%s", body)
		}
	}
	if nonces[0] == nonces[1] {
		t.Error("the nonce is the same on every load")
	}
	// Every other page keeps the global policy.
	if rec := h.do(http.MethodGet, "/api/github/apps", nil, h.admin); rec.Header().Get("Content-Security-Policy") != csp || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("global headers = %v", rec.Header())
	}
}

func TestGitHubAppCreated(t *testing.T) {
	h := newHarness(t).setup()
	mk := newFakeMaker()
	h.srv.d.GitHub = mk
	clock := fakes.NewClock(time.Now())
	h.srv.ghFlows = newGHAppFlows(clock)
	notFinished := func(name string, rec *httptest.ResponseRecorder, apps string) {
		t.Helper()
		q := landed(t, rec)
		if q.Get("github_app") != "not_finished" || q.Get("apps") != apps {
			t.Errorf("%s: landed %v", name, q)
		}
		if strings.Contains(rec.Header().Get("Location"), ghTestCode) {
			t.Errorf("%s: the code is in the address", name)
		}
	}

	fc, _ := h.startFlow(h.admin)
	wrong := h.browser("/api/github/apps/created?code="+ghTestCode+"&state=nope", withCookie(fc))
	notFinished("wrong state", wrong, "")
	if sc := wrong.Header().Get("Set-Cookie"); sc != "" {
		t.Errorf("a wrong state touched the flow cookie: %q", sc)
	}
	notFinished("no code", h.browser("/api/github/apps/created?state=nope", withCookie(fc)), "")

	_, state := h.startFlow(h.admin)
	notFinished("no flow cookie", h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(state)), "")

	fc, state = h.startFlow(h.admin)
	clock.Advance(61 * time.Minute)
	notFinished("expired", h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(state), withCookie(fc)), "")

	gone := h.newUser("admin3", core.RoleAdmin)
	fc, state = h.startFlow(gone)
	expect(t, h.do(http.MethodPost, "/api/auth/logout", nil, gone), http.StatusNoContent)
	notFinished("signed out", h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(state), withCookie(fc)), "acme")

	demoted := h.newUser("admin4", core.RoleAdmin)
	fc, state = h.startFlow(demoted)
	role := core.RoleMember
	expect(t, h.do(http.MethodPatch, "/api/users/"+string(demoted.user), dto.UserPatchReq{Role: &role}, h.owner), http.StatusOK)
	notFinished("demoted", h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(state), withCookie(fc)), "acme")
	if calls := mk.createCalls(); len(calls) != 0 {
		t.Fatalf("CreateApp ran for a flow it can't tie to the admin: %+v", calls)
	}

	fc, state = h.startFlow(h.admin)
	path := "/api/github/apps/created?code=" + ghTestCode + "&state=" + url.QueryEscape(state)
	rec := h.browser(path, withCookie(fc))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("created = %d %s", rec.Code, rec.Body)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	next := loc.Query().Get("state")
	if loc.Scheme+"://"+loc.Host+loc.Path != ghTestInstall || next == "" || next == state || strings.Contains(loc.String(), ghTestCode) {
		t.Fatalf("created sent the browser to %q", loc)
	}
	calls := mk.createCalls()
	if len(calls) != 1 || calls[0].code != ghTestCode || calls[0].target.Org != "acme" || calls[0].by != h.admin.user {
		t.Fatalf("CreateApp calls = %+v", calls)
	}
	if c := flowCookieOf(t, rec); c.Value != fc.Value || c.MaxAge != 3600 {
		t.Errorf("flow cookie after create = %+v", c)
	}
	// Back from GitHub's install page replays this address: the browser is
	// sent to the install page again and nothing is redeemed twice.
	rec = h.browser(path, withCookie(fc))
	if loc2, _ := url.Parse(rec.Header().Get("Location")); rec.Code != http.StatusSeeOther || loc2.String() != loc.String() {
		t.Errorf("replay = %d %q, want %q", rec.Code, rec.Header().Get("Location"), loc)
	}
	if len(mk.createCalls()) != 1 {
		t.Error("the code was redeemed twice")
	}

	// Two tabs: the later start owns the cookie. The earlier tab's return is
	// refused without ending the later flow, which still completes.
	_, early := h.startFlow(h.admin)
	late, lateState := h.startFlow(h.admin)
	notFinished("the earlier tab", h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(early), withCookie(late)), "")
	h.created(late, lateState)

	mk.createErr = core.ErrSealed
	fc, state = h.startFlow(h.admin)
	if q := landed(t, h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(state), withCookie(fc))); q.Get("github_app") != "sealed" || q.Get("apps") != "acme" {
		t.Errorf("sealed = %v", q)
	}
	mk.createErr = errors.New("github: redeeming the new app's code: GitHub found nothing at POST /app-manifests/{code}/conversions")
	fc, state = h.startFlow(h.admin)
	rec = h.browser("/api/github/apps/created?code="+ghTestCode+"&state="+url.QueryEscape(state), withCookie(fc))
	if q := landed(t, rec); q.Get("github_app") != "exchange_failed" || q.Get("apps") != "acme" {
		t.Errorf("exchange failed = %v", q)
	}
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "Max-Age=0") {
		t.Errorf("flow cookie not cleared: %q", sc)
	}
}

func TestGitHubAppInstalled(t *testing.T) {
	h := newHarness(t).setup()
	mk := newFakeMaker()
	h.srv.d.GitHub = mk

	fc, state := h.startFlow(h.admin)
	next := h.created(fc, state)
	if q := landed(t, h.browser("/api/github/apps/installed?installation_id=42&setup_action=install&state=wrong", withCookie(fc))); q.Get("github_app") != "check" {
		t.Errorf("bad state = %v", q)
	}
	if len(mk.installCalls()) != 0 {
		t.Fatal("InstallApp ran for a bad state")
	}
	rec := h.browser("/api/github/apps/installed?installation_id=42&setup_action=install&state="+url.QueryEscape(next), withCookie(fc))
	if q := landed(t, rec); q.Get("github_app") != "installed" {
		t.Errorf("installed = %v", q)
	}
	if calls := mk.installCalls(); len(calls) != 1 || calls[0] != (ghInstall{ghAppTestID, 42, h.admin.user, "user:" + string(h.admin.user)}) {
		t.Errorf("InstallApp calls = %+v", calls)
	}
	if sc := rec.Header().Get("Set-Cookie"); !strings.HasPrefix(sc, "bothy_ghapp=;") || !strings.Contains(sc, "Max-Age=0") {
		t.Errorf("flow cookie not cleared: %q", sc)
	}
	// The flow is spent.
	if q := landed(t, h.browser("/api/github/apps/installed?installation_id=42&setup_action=install&state="+url.QueryEscape(next), withCookie(fc))); q.Get("github_app") != "check" ||
		len(mk.installCalls()) != 1 {
		t.Errorf("replay = %v, %d installs", q, len(mk.installCalls()))
	}

	for _, tc := range []struct{ query, want string }{
		{"installation_id=43&setup_action=update", "updated"},
		{"setup_action=request", "requested"},
		{"installation_id=43&setup_action=install", "check"},
	} {
		if q := landed(t, h.browser("/api/github/apps/installed?"+tc.query)); q.Get("github_app") != tc.want {
			t.Errorf("without a flow, %s = %v", tc.query, q)
		}
	}

	fc, state = h.startFlow(h.admin)
	next = h.created(fc, state)
	if q := landed(t, h.browser("/api/github/apps/installed?setup_action=request&state="+url.QueryEscape(next), withCookie(fc))); q.Get("github_app") != "requested" {
		t.Errorf("request = %v", q)
	}
	fc, state = h.startFlow(h.admin)
	next = h.created(fc, state)
	if q := landed(t, h.browser("/api/github/apps/installed?installation_id=4x2&setup_action=install&state="+url.QueryEscape(next), withCookie(fc))); q.Get("github_app") != "check" {
		t.Errorf("non-numeric id = %v", q)
	}
	mk.installErr = fmt.Errorf("%w: GitHub found nothing", core.ErrInvalid)
	fc, state = h.startFlow(h.admin)
	next = h.created(fc, state)
	if q := landed(t, h.browser("/api/github/apps/installed?installation_id=44&setup_action=install&state="+url.QueryEscape(next), withCookie(fc))); q.Get("github_app") != "install_failed" {
		t.Errorf("install failed = %v", q)
	}
	if n := len(mk.installCalls()); n != 2 {
		t.Errorf("%d InstallApp calls, want 2", n)
	}

	h.cfg.eff.SafeMode = true
	for _, p := range []string{"/api/github/apps/handoff", "/api/github/apps/created?code=x&state=y", "/api/github/apps/installed?installation_id=1&state=y"} {
		if q := landed(t, h.browser(p)); q.Get("github_app") != "unavailable" {
			t.Errorf("safe mode %s = %v", p, q)
		}
	}
	h.cfg.eff.SafeMode = false
	h.srv.d.GitHub = nil
	if q := landed(t, h.browser("/api/github/apps/created?code=x&state=y")); q.Get("github_app") != "unavailable" {
		t.Errorf("without GitHub = %v", q)
	}
}

func TestGitHubAppJSONRoutes(t *testing.T) {
	h := newHarness(t).setup()
	mk := newFakeMaker()
	h.srv.d.GitHub = mk

	if rec := h.do(http.MethodGet, "/api/github/apps", nil, h.admin); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("no apps = %d %s", rec.Code, rec.Body)
	}
	rec := h.do(http.MethodPost, "/api/github/apps/"+ghAppTestID+"/check", nil, h.admin)
	if got := decodeAs[dto.GitHubAppCheck](t, rec); rec.Code != http.StatusOK || got.Installed || got.Connection != nil {
		t.Errorf("not installed = %d %+v", rec.Code, got)
	}
	mk.checkConn = mk.conn()
	rec = h.do(http.MethodPost, "/api/github/apps/"+ghAppTestID+"/check", nil, h.admin)
	if got := decodeAs[dto.GitHubAppCheck](t, rec); !got.Installed || got.Connection == nil || got.Connection.ID != ghConnID {
		t.Errorf("installed = %+v", got)
	}
	mk.checkConn = nil
	for err, want := range map[error]int{
		fmt.Errorf("%w: this app is no longer used", core.ErrConflict):     http.StatusConflict,
		fmt.Errorf("%w: GitHub rejected the credentials", core.ErrInvalid): http.StatusBadRequest,
		core.ErrSealed: http.StatusServiceUnavailable,
	} {
		mk.checkErr = err
		if rec := h.do(http.MethodPost, "/api/github/apps/"+ghAppTestID+"/check", nil, h.admin); rec.Code != want {
			t.Errorf("check with %v = %d", err, rec.Code)
		}
	}
	mk.checkErr = nil
	expect(t, h.do(http.MethodPost, "/api/github/apps/ghc_nope/check", nil, h.admin), http.StatusNotFound)

	rec = h.do(http.MethodPost, "/api/github/apps/"+ghAppTestID+"/retire", nil, h.admin)
	if got := decodeAs[core.GitHubOwnApp](t, rec); rec.Code != http.StatusOK || got.State != "retired" {
		t.Errorf("retire = %d %+v", rec.Code, got)
	}
	mk.retireErr = fmt.Errorf("%w: disconnect GitHub first", core.ErrConflict)
	expect(t, h.do(http.MethodPost, "/api/github/apps/"+ghAppTestID+"/retire", nil, h.admin), http.StatusConflict)
	expect(t, h.do(http.MethodDelete, "/api/github/apps/"+ghAppTestID, nil, h.admin), http.StatusNoContent)
	mk.deleteErr = core.ErrConflict
	expect(t, h.do(http.MethodDelete, "/api/github/apps/"+ghAppTestID, nil, h.admin), http.StatusConflict)

	for _, rt := range h.srv.githubAppRoutes() {
		if rt.access != accessAdmin && rt.access != accessAdminCookie {
			continue
		}
		method, path := concretePath(strings.Replace(rt.pattern, "{id}", ghAppTestID, 1))
		if rec := h.do(method, path, nil, h.member); rec.Code != http.StatusForbidden {
			t.Errorf("member %s = %d", rt.pattern, rec.Code)
		}
	}

	mk.connApp = &core.GitHubOwnApp{ID: ghAppTestID, Slug: "bothy-acme", Name: "Bothy (acme)", State: "connected"}
	detail := decodeAs[dto.GitHubConnectionDetail](t, h.do(http.MethodGet, "/api/github/connections/"+ghConnID, nil, h.admin))
	if detail.App == nil || detail.App.Slug != "bothy-acme" {
		t.Errorf("detail = %+v", detail)
	}
	// The query-credential guard still covers the browser routes.
	expect(t, h.browser("/api/github/apps/created?code=x&state=y&token=z"), http.StatusBadRequest)
}

func TestGitHubAppAuditSummaries(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	detail := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	admin := "user:" + string(h.admin.user)
	for i, e := range []core.AuditEntry{
		{Actor: admin, Action: "github.app_create", Target: ghAppTestID, Outcome: "ok", Detail: detail(map[string]any{"name": "Bothy (acme)", "slug": "bothy-acme"})},
		{Actor: admin, Action: "github.app_create", Outcome: "error", Detail: detail(map[string]any{"owner": "acme"})},
		{Actor: admin, Action: "github.app_retire", Target: ghAppTestID, Outcome: "ok", Detail: detail(map[string]any{"name": "Bothy (labs)"})},
		{Actor: admin, Action: "github.connect", Target: ghConnID, Outcome: "ok", Detail: detail(map[string]any{"method": "app_manifest", "app_name": "Bothy (acme)"})},
		{Actor: admin, Action: "github.connect", Target: ghConnID, Outcome: "ok", Detail: detail(map[string]any{"method": "token"})},
	} {
		e.At = time.Now().Add(time.Duration(i) * time.Second)
		if _, err := h.store.Audit().Append(ctx, &e, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	rows := decodeAs[dto.List[dto.AuditRow]](t, h.do("GET", "/api/audit?limit=50", nil, h.admin))
	var got []string
	for _, r := range rows.Items {
		got = append(got, r.Summary)
	}
	for _, want := range []string{"Created the GitHub App Bothy (acme)", "Couldn't create a GitHub App", "Stopped using the GitHub App Bothy (labs)",
		"Connected GitHub as Bothy (acme)", "Connected GitHub"} {
		if !slices.Contains(got, want) {
			t.Errorf("no row %q in %q", want, got)
		}
	}
}
