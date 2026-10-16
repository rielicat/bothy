//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/auth/oauth/oauthtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// signInVia starts a provider round trip at path as u, lets the fake answer
// as who and returns where the callback sends the browser.
func signInVia(t *testing.T, u *pltUser, fake *oauthtest.Server, path string, body any, who oauthtest.Identity) (string, url.Values) {
	t.Helper()
	fake.SignInAs(who)
	var start dto.SignInStartResp
	if code := u.JSON(t, http.MethodPost, path, body, &start); code != http.StatusOK {
		t.Fatalf("POST %s = %d", path, code)
	}
	loc, err := oauthtest.Follow(context.Background(), nil, start.RedirectURL)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	resp := u.Do(t, http.MethodGet, cb.RequestURI(), nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback = %s %s", resp.Status, resp.Body)
	}
	land, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || land.Host != "" {
		t.Fatalf("callback Location %q", resp.Header.Get("Location"))
	}
	return land.Path, land.Query()
}

func TestSignInGoogle(t *testing.T) {
	g := oauthtest.NewGoogle(t, "g-client", "g-secret")
	gw := Start(t, Options{PublicURL: true, Env: map[string]string{"BOTHY_SECRET_GOOGLE_SIGN_IN_CLIENT_SECRET": "g-secret"},
		Config: fmt.Sprintf(`mode: multi
auth:
  sign_in:
    allow: [acme.com, pat@gmail.com]
    google:
      enabled: true
      client_id: g-client
      client_secret: secret:google_sign_in_client_secret
      api_base: %q
`, g.URL)})
	owner := pltOwner(t, gw)
	pat := oauthtest.Identity{Subject: "g-pat", Email: "pat@gmail.com", EmailVerified: true, Name: "Pat"}

	path, q := signInVia(t, owner, g, "/api/me/sign-ins/google", nil, pat)
	if path != "/settings/account" || q.Get("connected") != "google" {
		t.Fatalf("connect landed on %s?%s", path, q.Encode())
	}
	var mine dto.MySignIns
	if code := owner.JSON(t, http.MethodGet, "/api/me/sign-ins", nil, &mine); code != http.StatusOK || len(mine.Linked) != 1 || mine.Linked[0].Email != "pat@gmail.com" {
		t.Fatalf("my sign-ins = %d %+v", code, mine)
	}

	browser := pltNewClient(gw)
	var methods dto.SignInMethods
	if code := browser.JSON(t, http.MethodGet, "/api/auth/methods", nil, &methods); code != http.StatusOK || len(methods.Providers) != 1 ||
		methods.Providers[0].Provider != core.SignInGoogle {
		t.Fatalf("methods = %d %+v", code, methods)
	}
	path, _ = signInVia(t, browser, g, "/api/auth/oauth/google/start", dto.SignInStartReq{Next: "/chat"}, pat)
	if path != "/chat" {
		t.Fatalf("signed in to %s", path)
	}
	if code := browser.JSON(t, http.MethodGet, "/api/auth/me", nil, &browser.me); code != http.StatusOK || browser.me.User.ID != owner.ID() {
		t.Fatalf("me = %d %+v", code, browser.me.User)
	}
	login := owner.WaitAudit(t, "auth.login", func(e core.AuditEntry) bool { return strings.Contains(string(e.Detail), `"method":"google"`) })
	if login.Target != "user:"+string(owner.ID()) {
		t.Fatalf("auth.login %+v", login)
	}
	all, _ := json.Marshal(owner.Audit(t, ""))
	for _, v := range g.Issued() {
		if strings.Contains(string(all), v) {
			t.Fatal("the audit log carries a code or token the provider issued")
		}
	}

	if _, q = signInVia(t, pltNewClient(gw), g, "/api/auth/oauth/google/start", dto.SignInStartReq{},
		oauthtest.Identity{Subject: "g-lee", Email: "lee@other.com", HD: "other.com", EmailVerified: true}); q.Get("signin_error") != "not_allowed" {
		t.Fatalf("another Workspace: %v", q)
	}
	if _, q = signInVia(t, pltNewClient(gw), g, "/api/auth/oauth/google/start", dto.SignInStartReq{},
		oauthtest.Identity{Subject: "g-maya", Email: "maya@acme.com", HD: "acme.com", EmailVerified: true}); q.Get("signin_error") != "no_account" {
		t.Fatalf("on the list without an account: %v", q)
	}

	// Taking Pat off the list stops the linked account at the next sign-in.
	var adm dto.SignInAdmin
	if code := owner.JSON(t, http.MethodPatch, "/api/sign-in", dto.SignInListPatch{Allow: &[]string{"acme.com"}}, &adm); code != http.StatusOK ||
		len(adm.Allow) != 1 || !adm.Providers[0].Ready {
		t.Fatalf("PATCH /api/sign-in = %d %+v", code, adm)
	}
	if _, q = signInVia(t, pltNewClient(gw), g, "/api/auth/oauth/google/start", dto.SignInStartReq{}, pat); q.Get("signin_error") != "not_allowed" ||
		q.Get("who") != "pat@gmail.com" {
		t.Fatalf("taken off the list: %v", q)
	}
	resp := owner.Do(t, http.MethodPatch, "/api/sign-in", dto.SignInListPatch{Allow: &[]string{"gmail.com"}}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(resp.Body), "anyone can get an address at gmail.com") {
		t.Fatalf("public mail domain = %s %s", resp.Status, resp.Body)
	}
}

func TestSignInGitHubNewMember(t *testing.T) {
	gh := oauthtest.NewGitHub(t, "h-client", "h-secret")
	gw := Start(t, Options{PublicURL: true, Env: map[string]string{"BOTHY_SECRET_GITHUB_SIGN_IN_CLIENT_SECRET": "h-secret"},
		Config: fmt.Sprintf(`mode: multi
auth:
  sign_in:
    allow: [acme.com]
    allow_new: true
    github:
      enabled: true
      client_id: h-client
      client_secret: secret:github_sign_in_client_secret
      orgs: [acme-inc]
      api_base: %q
`, gh.URL)})
	owner := pltOwner(t, gw)

	octo := oauthtest.Identity{ID: 583231, Login: "octo-cat", Name: "Octo Cat", Orgs: map[string]string{"acme-inc": "active"}}
	browser := pltNewClient(gw)
	if path, q := signInVia(t, browser, gh, "/api/auth/oauth/github/start", dto.SignInStartReq{Next: "/chat"}, octo); path != "/chat" {
		t.Fatalf("new member landed on %s?%s", path, q.Encode())
	}
	if code := browser.JSON(t, http.MethodGet, "/api/auth/me", nil, &browser.me); code != http.StatusOK ||
		browser.me.User.Username != "octo-cat" || browser.me.User.Role != core.RoleMember || browser.me.User.DisplayName != "Octo Cat" {
		t.Fatalf("new member = %d %+v", code, browser.me.User)
	}
	if got := gh.WaitRevoked(1); len(got) != 1 {
		t.Fatal("the GitHub token was not revoked")
	}
	owner.WaitAudit(t, "user.create", func(e core.AuditEntry) bool { return strings.Contains(string(e.Detail), `"via":"github"`) })

	byMail := oauthtest.Identity{ID: 7, Login: "sam-acme", Emails: []oauthtest.Email{{Email: "sam@acme.com", Primary: true, Verified: true}}}
	if path, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{}, byMail); path != "/chat" {
		t.Fatalf("verified address at a listed domain: %s %v", path, q)
	}
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{},
		oauthtest.Identity{ID: 8, Login: "new-hire", Orgs: map[string]string{"acme-inc": "pending"}}); q.Get("signin_error") != "not_allowed" {
		t.Fatalf("pending invitation: %v", q)
	}
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{},
		oauthtest.Identity{ID: 9, Login: "sso", Orgs: map[string]string{"acme-inc": "restricted"}}); q.Get("signin_error") != "org_access" {
		t.Fatalf("restricted organization: %v", q)
	}

	off := true
	if code := owner.JSON(t, http.MethodPatch, "/api/users/"+string(browser.me.User.ID), dto.UserPatchReq{Disabled: &off}, nil); code != http.StatusOK {
		t.Fatalf("turn off = %d", code)
	}
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{}, octo); q.Get("signin_error") != "disabled" {
		t.Fatalf("turned off: %v", q)
	}
}
