package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/auth/oauth/oauthtest"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

type signInHarness struct {
	*harness
	google, github *oauthtest.Server
}

var (
	gMaya = oauthtest.Identity{Subject: "g-maya", Email: "maya@acme.com", HD: "acme.com", EmailVerified: true, Name: "Maya Chen"}
	gKim  = oauthtest.Identity{Subject: "g-kim", Email: "kim@gmail.com", EmailVerified: true}
)

func newSignInHarness(t *testing.T, mut ...func(*config.Config)) *signInHarness {
	t.Helper()
	g := oauthtest.NewGoogle(t, "g-client", "g-secret")
	gh := oauthtest.NewGitHub(t, "h-client", "h-secret")
	h := newHarness(t, append([]func(*config.Config){func(c *config.Config) {
		c.Server.PublicURL = "http://127.0.0.1:8080"
		c.Auth.SignIn = config.SignInCfg{
			Allow:  []string{"acme.com"},
			Google: config.GoogleSignIn{Enabled: true, ClientID: "g-client", ClientSecret: "secret:google_sign_in_client_secret", APIBase: g.URL},
			GitHub: config.GitHubSignIn{Enabled: true, ClientID: "h-client", ClientSecret: "secret:github_sign_in_client_secret",
				Orgs: []string{"acme-inc"}, APIBase: gh.URL},
		}
	}}, mut...)...)
	ctx := context.Background()
	_, _ = h.vault.Put(ctx, core.GlobalScope, "google_sign_in_client_secret", []byte("g-secret"), "")
	_, _ = h.vault.Put(ctx, core.GlobalScope, "github_sign_in_client_secret", []byte("h-secret"), "")
	h.auth.SetSignIn(auth.SignInDeps{Vault: h.vault, HTTP: func(core.SystemDest) *http.Client { return &http.Client{Timeout: 5 * time.Second} }})
	return &signInHarness{harness: h.setup(), google: g, github: gh}
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// roundTrip starts at path (sign in or connect), lets the fake provider
// answer as who, and returns the callback's answer.
func (h *signInHarness) roundTrip(path string, body any, c *client, who oauthtest.Identity, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	fake := h.google
	if strings.Contains(path, "github") {
		fake = h.github
	}
	fake.SignInAs(who)
	rec := h.do("POST", path, body, c, opts...)
	expect(h.t, rec, http.StatusOK)
	start := decodeAs[dto.SignInStartResp](h.t, rec)
	sc := cookieNamed(rec, cookieSignInPlain)
	if sc == nil {
		h.t.Fatalf("no sign-in cookie: %v", rec.Result().Cookies())
	}
	loc, err := oauthtest.Follow(context.Background(), nil, start.RedirectURL)
	if err != nil {
		h.t.Fatal(err)
	}
	cb := h.request("GET", oauthtest.Path(loc), nil, c)
	cb.AddCookie(&http.Cookie{Name: cookieSignInPlain, Value: sc.Value})
	out := httptest.NewRecorder()
	h.h.ServeHTTP(out, cb)
	return out
}

func location(t *testing.T, rec *httptest.ResponseRecorder) (string, url.Values) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d %s, want 303", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || u.Host != "" || u.Scheme != "" {
		t.Fatalf("Location %q is not a local path", rec.Header().Get("Location"))
	}
	return u.Path, u.Query()
}

func TestAuthMethods(t *testing.T) {
	plain := newHarness(t).setup()
	if m := decodeAs[dto.SignInMethods](t, plain.do("GET", "/api/auth/methods", nil, nil)); m.Providers == nil || len(m.Providers) != 0 {
		t.Fatalf("nothing configured: %+v", m)
	}
	h := newSignInHarness(t)
	m := decodeAs[dto.SignInMethods](t, h.do("GET", "/api/auth/methods", nil, nil))
	if len(m.Providers) != 2 || m.Providers[0].Provider != core.SignInGoogle || m.Providers[0].Name != "Google" {
		t.Fatalf("methods %+v", m)
	}
	h.cfg.eff.SafeMode = true
	if m := decodeAs[dto.SignInMethods](t, h.do("GET", "/api/auth/methods", nil, nil)); len(m.Providers) != 0 {
		t.Fatalf("safe mode: %+v", m)
	}
	expect(t, h.do("POST", "/api/auth/oauth/google/start", dto.SignInStartReq{}, nil), http.StatusServiceUnavailable)
	_, q := location(t, h.do("GET", "/api/auth/oauth/google/callback?code=x&state=y", nil, nil))
	if q.Get("signin_error") != "unavailable" {
		t.Fatalf("safe-mode callback %v", q)
	}
}

func TestSignInStartHTTP(t *testing.T) {
	h := newSignInHarness(t)
	rec := h.do("POST", "/api/auth/oauth/google/start", dto.SignInStartReq{Next: "/chat"}, nil)
	expect(t, rec, http.StatusOK)
	start := decodeAs[dto.SignInStartResp](t, rec)
	c := cookieNamed(rec, cookieSignInPlain)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 600 || c.Secure {
		t.Fatalf("sign-in cookie %+v", c)
	}
	u, _ := url.Parse(start.RedirectURL)
	if u.Query().Get("state") != c.Value || len(u.Query().Get("code_challenge")) != 43 || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("redirect %s", start.RedirectURL)
	}
	if !strings.HasPrefix(start.RedirectURL, h.google.URL+"/o/oauth2/v2/auth?") {
		t.Fatalf("redirect %s", start.RedirectURL)
	}

	expect(t, h.do("POST", "/api/auth/oauth/google/start", dto.SignInStartReq{}, nil, withHeader("Sec-Fetch-Site", "cross-site")), http.StatusForbidden)
	rec = h.do("POST", "/api/auth/oauth/google/start", dto.SignInStartReq{}, nil, withHost("localhost:8080"))
	expect(t, rec, http.StatusConflict)
	if errCode(t, rec) != "wrong_address" || !strings.Contains(rec.Body.String(), `"fix":"http://127.0.0.1:8080"`) {
		t.Fatalf("wrong address %s", rec.Body.String())
	}
	expect(t, h.do("POST", "/api/auth/oauth/okta/start", dto.SignInStartReq{}, nil), http.StatusNotFound)
	h.cfg.update(func(c *config.Config) { c.Auth.SignIn.GitHub.Enabled = false })
	rec = h.do("POST", "/api/auth/oauth/github/start", dto.SignInStartReq{}, nil)
	if rec.Code != http.StatusConflict || errCode(t, rec) != "sign_in_unavailable" {
		t.Fatalf("turned off: %d %s", rec.Code, rec.Body.String())
	}

	// Behind HTTPS the cookie is __Host- and Secure.
	h.cfg.update(func(c *config.Config) { c.Server.PublicURL = "https://127.0.0.1:8080" })
	rec = h.do("POST", "/api/auth/oauth/google/start", dto.SignInStartReq{}, nil, overTLS())
	expect(t, rec, http.StatusOK)
	if c := cookieNamed(rec, cookieSignInSecure); c == nil || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("TLS sign-in cookie %v", rec.Result().Cookies())
	}
	expect(t, h.do("POST", "/api/auth/oauth/google/start", dto.SignInStartReq{}, nil), http.StatusConflict)

	h.cfg.update(func(c *config.Config) { c.Server.PublicURL = "http://127.0.0.1:8080" })
	for i := 0; ; i++ {
		rec := h.do("POST", "/api/auth/oauth/google/start", dto.SignInStartReq{}, nil, fromAddr("127.0.0.9:1"))
		if rec.Code == http.StatusTooManyRequests {
			if i != signInsPerMin || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("limited after %d starts", i)
			}
			break
		}
		expect(t, rec, http.StatusOK)
	}
}

func TestSignInCallbackHTTP(t *testing.T) {
	h := newSignInHarness(t)
	rec := h.roundTrip("/api/me/sign-ins/google", nil, h.owner, gMaya)
	path, q := location(t, rec)
	if path != "/settings/account" || q.Get("connected") != "google" {
		t.Fatalf("connect landed on %s?%s", path, q.Encode())
	}
	my := decodeAs[dto.MySignIns](t, h.do("GET", "/api/me/sign-ins", nil, h.owner))
	if len(my.Linked) != 1 || my.Linked[0].Email != "maya@acme.com" || !my.HasPassword || len(my.Providers) != 2 {
		t.Fatalf("my sign-ins %+v", my)
	}

	rec = h.roundTrip("/api/auth/oauth/google/start", dto.SignInStartReq{Next: "/chat/ses_1"}, nil, gMaya)
	path, _ = location(t, rec)
	if path != "/chat/ses_1" {
		t.Fatalf("signed in to %s", path)
	}
	loc := rec.Header().Get("Location")
	for _, v := range h.google.Issued() {
		if strings.Contains(loc, v) {
			t.Fatal("the callback reflects a code")
		}
	}
	for _, a := range h.google.Authorizations() {
		if strings.Contains(loc, a.State) {
			t.Fatal("the callback reflects the state")
		}
	}
	sess := cookieNamed(rec, cookiePlain)
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie %+v", sess)
	}
	if c := cookieNamed(rec, cookieSignInPlain); c == nil || c.MaxAge >= 0 {
		t.Fatalf("sign-in cookie not cleared: %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" ||
		rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("callback headers %v", rec.Header())
	}
	me := decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, &client{cookie: sess.Value}))
	if me.User.ID != h.owner.user {
		t.Fatalf("signed in as %+v", me.User)
	}

	// Failures land on the sign-in page with a code, the provider and next.
	rec = h.roundTrip("/api/auth/oauth/google/start", dto.SignInStartReq{Next: "/settings"}, nil, gKim)
	path, q = location(t, rec)
	if path != "/login" || q.Get("signin_error") != "not_allowed" || q.Get("provider") != "google" || q.Get("next") != "/settings" ||
		q.Get("who") != "kim@gmail.com" || cookieNamed(rec, cookiePlain) != nil {
		t.Fatalf("refused: %s?%s", path, q.Encode())
	}
	rec = h.roundTrip("/api/auth/oauth/google/start", dto.SignInStartReq{}, nil,
		oauthtest.Identity{Subject: "g-sam", Email: "sam@acme.com", HD: "acme.com", EmailVerified: true})
	if _, q = location(t, rec); q.Get("signin_error") != "no_account" || q.Has("next") || q.Has("who") {
		t.Fatalf("no account: %v", q)
	}
	_, q = location(t, h.do("GET", "/api/auth/oauth/google/callback?state=nope&code=x", nil, nil))
	if q.Get("signin_error") != "expired" || q.Get("provider") != "google" {
		t.Fatalf("unknown state: %v", q)
	}
	path, q = location(t, h.do("GET", "/api/auth/oauth/okta/callback?state=nope&code=x", nil, nil))
	if path != "/login" || q.Get("signin_error") != "unavailable" || q.Has("provider") {
		t.Fatalf("unknown provider: %s %v", path, q)
	}
	_, q = location(t, h.do("GET", "/api/auth/oauth/github/callback?error=access_denied&error_description=x&state=nope", nil, nil))
	if q.Get("signin_error") != "expired" {
		t.Fatalf("stray error: %v", q)
	}
}

func TestSignInLinkHTTP(t *testing.T) {
	h := newSignInHarness(t, func(c *config.Config) { c.Auth.SignIn.AllowNew = true })
	expect(t, h.do("POST", "/api/me/sign-ins/google", nil, h.owner, noCSRF()), http.StatusForbidden)
	rec := h.do("POST", "/api/me/sign-ins/google", nil, h.tokenFor(h.owner, "chat", "admin"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "browser session") {
		t.Fatalf("bearer connect: %d %s", rec.Code, rec.Body.String())
	}
	expect(t, h.do("GET", "/api/me/sign-ins", nil, h.tokenFor(h.member, "read")), http.StatusForbidden)

	lee := oauthtest.Identity{ID: 42, Login: "lee", Orgs: map[string]string{"acme-inc": "active"}}
	path, q := location(t, h.roundTrip("/api/me/sign-ins/github", nil, h.member, lee))
	if path != "/settings/account" || q.Get("connected") != "github" {
		t.Fatalf("connect: %s %v", path, q)
	}
	expect(t, h.do("POST", "/api/me/sign-ins/github", nil, h.member), http.StatusConflict)
	path, q = location(t, h.roundTrip("/api/me/sign-ins/github", nil, h.admin, lee))
	if path != "/settings/account" || q.Get("signin_error") != "taken" || q.Get("provider") != "github" {
		t.Fatalf("someone else's account: %s %v", path, q)
	}
	expect(t, h.do("DELETE", "/api/me/sign-ins/github", nil, h.member), http.StatusNoContent)
	expect(t, h.do("DELETE", "/api/me/sign-ins/github", nil, h.member), http.StatusNotFound)
	expect(t, h.do("DELETE", "/api/me/sign-ins/okta", nil, h.member), http.StatusNotFound)

	// An account made by sign-in keeps its only way in.
	rec = h.roundTrip("/api/auth/oauth/google/start", dto.SignInStartReq{}, nil, gMaya)
	sess := cookieNamed(rec, cookiePlain)
	if sess == nil {
		t.Fatalf("new member not signed in: %s", rec.Header().Get("Location"))
	}
	me := decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, &client{cookie: sess.Value}))
	maya := &client{name: "maya", user: me.User.ID, cookie: sess.Value, csrf: me.CSRF}
	if me.User.Role != core.RoleMember || me.User.Username != "maya" {
		t.Fatalf("new member %+v", me.User)
	}
	my := decodeAs[dto.MySignIns](t, h.do("GET", "/api/me/sign-ins", nil, maya))
	if my.HasPassword || len(my.Linked) != 1 {
		t.Fatalf("my sign-ins %+v", my)
	}
	rec = h.do("DELETE", "/api/me/sign-ins/google", nil, maya)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ask an admin for a password") {
		t.Fatalf("last way in: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSignInAccessHTTP(t *testing.T) {
	h := newSignInHarness(t)
	for _, c := range []*client{h.member, h.tokenFor(h.admin, "chat")} {
		expect(t, h.do("GET", "/api/sign-in", nil, c), http.StatusForbidden)
		expect(t, h.do("PATCH", "/api/sign-in", dto.SignInListPatch{}, c), http.StatusForbidden)
		expect(t, h.do("PATCH", "/api/sign-in/google", dto.SignInPatch{}, c), http.StatusForbidden)
		expect(t, h.do("POST", "/api/sign-in/google/check", dto.SignInCheckReq{}, c), http.StatusForbidden)
	}
	ctx := context.Background()
	oa, _ := h.auth.FromCookie(ctx, h.owner.cookie)
	_, pw, err := h.auth.CreateUserOneTime(ctx, oa, "jordan", "", core.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	cookie, a, err := h.auth.Login(ctx, "jordan", pw, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	jordan := &client{cookie: cookie, csrf: a.CSRF}
	for _, rec := range []*httptest.ResponseRecorder{h.do("GET", "/api/me/sign-ins", nil, jordan), h.do("POST", "/api/me/sign-ins/google", nil, jordan)} {
		if rec.Code != http.StatusForbidden || errCode(t, rec) != "password_change_required" {
			t.Fatalf("one-time password: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestSignInAdminHTTP(t *testing.T) {
	h := newSignInHarness(t, func(c *config.Config) { c.Auth.SignIn.Google.ClientSecret = "" })
	adm := decodeAs[dto.SignInAdmin](t, h.do("GET", "/api/sign-in", nil, h.admin))
	g, gh := adm.Providers[0], adm.Providers[1]
	if adm.Address != "ok" || !adm.TeamMode || adm.PublicURL != "http://127.0.0.1:8080" || !slices.Equal(adm.Allow, []string{"acme.com"}) ||
		g.Problem != "incomplete" || g.SecretSaved || g.RedirectURI != "http://127.0.0.1:8080/api/auth/oauth/google/callback" ||
		!gh.Ready || !gh.SecretSaved || gh.SecretSource != "vault" || !slices.Equal(gh.Orgs, []string{"acme-inc"}) || g.Orgs == nil {
		t.Fatalf("admin view %+v", adm)
	}
	if strings.Contains(h.do("GET", "/api/sign-in", nil, h.admin).Body.String(), "h-secret") {
		t.Fatal("the admin view carries a secret")
	}

	_ = h.vault.Delete(context.Background(), core.GlobalScope, "google_sign_in_client_secret", "")
	rec := h.do("PATCH", "/api/sign-in/google", map[string]any{"client_id": "  g-client  ", "client_secret": " new-secret\n"}, h.admin)
	expect(t, rec, http.StatusOK)
	set := h.cfg.sets[len(h.cfg.sets)-1]
	if string(set["auth.sign_in.google.client_secret"]) != `"secret:google_sign_in_client_secret"` || string(set["auth.sign_in.google.client_id"]) != `"g-client"` {
		t.Fatalf("config change %s", set)
	}
	if v, err := h.vault.Get(context.Background(), core.GlobalScope, "google_sign_in_client_secret", "t"); err != nil || string(v.Bytes()) != "new-secret" {
		t.Fatalf("vault = %v", err)
	}
	expect(t, h.do("PATCH", "/api/sign-in/google", map[string]any{"orgs": []string{"x"}}, h.admin), http.StatusBadRequest)
	expect(t, h.do("PATCH", "/api/sign-in/google", map[string]any{}, h.admin), http.StatusBadRequest)
	expect(t, h.do("PATCH", "/api/sign-in/okta", map[string]any{"enabled": true}, h.admin), http.StatusNotFound)
	expect(t, h.do("PATCH", "/api/sign-in/github", map[string]any{"client_secret": "a\x00b"}, h.admin), http.StatusBadRequest)

	expect(t, h.do("PATCH", "/api/sign-in/github", map[string]any{"orgs": []string{" Acme-Inc ", "acme-inc", "", "labs"}, "enabled": true}, h.admin), http.StatusOK)
	set = h.cfg.sets[len(h.cfg.sets)-1]
	if string(set["auth.sign_in.github.orgs"]) != `["Acme-Inc","labs"]` || string(set["auth.sign_in.github.enabled"]) != "true" {
		t.Fatalf("orgs change %s", set)
	}

	// A refused config change takes the new secret back.
	h.cfg.refuse = config.FieldErrors{{Path: "auth.sign_in.github.enabled", Message: "GitHub sign-in needs server.public_url"}}
	rec = h.do("PATCH", "/api/sign-in/github", map[string]any{"client_secret": "replacement", "enabled": true}, h.admin)
	expect(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), `"fields"`) {
		t.Fatalf("refusal %s", rec.Body.String())
	}
	if v, _ := h.vault.Get(context.Background(), core.GlobalScope, "github_sign_in_client_secret", "t"); string(v.Bytes()) != "h-secret" {
		t.Fatalf("the old secret was not restored: %q", v.Bytes())
	}

	rec = h.do("PATCH", "/api/sign-in", map[string]any{"allow": []string{" Acme.com", "@eng.acme.com", "Pat@Gmail.com", "acme.com", ""}, "allow_new": true}, h.admin)
	expect(t, rec, http.StatusOK)
	set = h.cfg.sets[len(h.cfg.sets)-1]
	if string(set["auth.sign_in.allow"]) != `["acme.com","eng.acme.com","pat@gmail.com"]` || string(set["auth.sign_in.allow_new"]) != "true" {
		t.Fatalf("list change %s", set)
	}
	expect(t, h.do("PATCH", "/api/sign-in", map[string]any{}, h.admin), http.StatusBadRequest)

	check := decodeAs[dto.SignInCheck](t, h.do("POST", "/api/sign-in/github/check", dto.SignInCheckReq{}, h.admin))
	if !check.OK {
		t.Fatalf("saved GitHub client: %+v", check)
	}
	check = decodeAs[dto.SignInCheck](t, h.do("POST", "/api/sign-in/github/check", dto.SignInCheckReq{ClientSecret: "wrong"}, h.admin))
	if check.OK || check.Code != core.FailKeyRejected || strings.Contains(check.Error, "wrong") {
		t.Fatalf("wrong secret: %+v", check)
	}
}

func TestSignInAuditSummaries(t *testing.T) {
	h := newSignInHarness(t)
	ctx := context.Background()
	detail := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	member := "user:" + string(h.member.user)
	entries := []core.AuditEntry{
		{Actor: member, Action: "auth.login", Target: member, Outcome: "ok", Detail: detail(map[string]any{"method": "google"})},
		{Actor: member, Action: "auth.login", Target: member, Outcome: "ok", Detail: detail(map[string]any{"method": "password"})},
		{Actor: "system", Action: "auth.login_failed", Target: "github:octo", Outcome: "denied", Detail: detail(map[string]any{"method": "github", "reason": "not_allowed"})},
		{Actor: "system", Action: "auth.login_failed", Target: "google", Outcome: "denied", Detail: detail(map[string]any{"method": "google", "reason": "expired"})},
		{Actor: "system", Action: "auth.login_failed", Target: "user:" + string(h.admin.user), Outcome: "denied", Detail: detail(map[string]any{"method": "google"})},
		{Actor: member, Action: "signin.link", Target: member, Outcome: "ok", Detail: detail(map[string]any{"provider": "github", "login": "octo"})},
		{Actor: member, Action: "signin.link", Target: "user:x1", Outcome: "denied", Detail: detail(map[string]any{"provider": "google", "reason": "taken"})},
		{Actor: member, Action: "signin.unlink", Target: "user:x2", Outcome: "ok", Detail: detail(map[string]any{"provider": "google"})},
		{Actor: "system", Action: "user.create", Target: "user:x3", Outcome: "ok", Detail: detail(map[string]any{"username": "maya", "via": "google"})},
	}
	for i := range entries {
		e := entries[i]
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
	for _, want := range []string{"Signed in with Google", "Signed in", "Someone couldn't sign in with GitHub as octo",
		"Someone couldn't sign in with Google", "Someone couldn't sign in with Google as admin", "Connected GitHub",
		"Couldn't connect a Google account someone else uses", "Disconnected Google", "maya joined with Google"} {
		if !slices.Contains(got, want) {
			t.Errorf("no row %q in %q", want, got)
		}
	}
}
