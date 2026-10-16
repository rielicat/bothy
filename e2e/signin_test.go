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
	refused := pltNewClient(gw)
	if _, q = signInVia(t, refused, g, "/api/auth/oauth/google/start", dto.SignInStartReq{}, pat); q.Get("signin_error") != "not_allowed" ||
		q.Has("who") {
		t.Fatalf("taken off the list: %v", q)
	}
	// The refused account rides in a short-lived cookie the sign-in page
	// reads, never in the address.
	base, _ := url.Parse(gw.BaseURL)
	var who string
	for _, c := range refused.client.Jar.Cookies(base) {
		if c.Name == "bothy_signin_who" {
			who = c.Value
		}
	}
	if who != "pat%40gmail.com" {
		t.Fatalf("refusal cookie = %q", who)
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
    allow: [acme.com, sam@acme.com]
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
		t.Fatalf("verified address listed exactly: %s %v", path, q)
	}
	// A domain lets in Google Workspace accounts only.
	former := oauthtest.Identity{ID: 10, Login: "former", Emails: []oauthtest.Email{{Email: "former@acme.com", Primary: true, Verified: true}}}
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{}, former); q.Get("signin_error") != "not_allowed" {
		t.Fatalf("verified address at a listed domain: %v", q)
	}
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{},
		oauthtest.Identity{ID: 8, Login: "new-hire", Orgs: map[string]string{"acme-inc": "pending"}}); q.Get("signin_error") != "not_allowed" {
		t.Fatalf("pending invitation: %v", q)
	}
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{},
		oauthtest.Identity{ID: 9, Login: "sso", Orgs: map[string]string{"acme-inc": "restricted"}}); q.Get("signin_error") != "org_access" {
		t.Fatalf("restricted organization: %v", q)
	}

	// Organizations from bothy.yaml are matched by name until saved once in
	// the settings, which pins them by GitHub id.
	unpinned := func() bool {
		var rep dto.DoctorReport
		if code := owner.JSON(t, http.MethodPost, "/api/security-audit", dto.SecurityAuditReq{}, &rep); code != http.StatusOK {
			t.Fatalf("security audit = %d", code)
		}
		for _, f := range rep.Findings {
			if f.ID == "auth.sign_in" && strings.Contains(f.Detail, "so Bothy can pin them") {
				return true
			}
		}
		return false
	}
	if !unpinned() {
		t.Fatal("no finding for organizations matched by name")
	}
	gh.SetOrg("acme-inc", 1001)
	var adm dto.SignInAdmin
	if code := owner.JSON(t, http.MethodPatch, "/api/sign-in/github", dto.SignInPatch{Orgs: &[]string{"acme-inc"}}, &adm); code != http.StatusOK {
		t.Fatalf("save organizations = %d", code)
	}
	if unpinned() {
		t.Fatal("the organizations are still not pinned after a save")
	}
	resp := owner.Do(t, http.MethodPatch, "/api/sign-in/github", dto.SignInPatch{Orgs: &[]string{"acme-inc", "no-such-org"}}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(resp.Body), "GitHub has no organization called no-such-org.") {
		t.Fatalf("unknown organization = %s %s", resp.Status, resp.Body)
	}
	member := oauthtest.Identity{ID: 11, Login: "member-two", Orgs: map[string]string{"acme-inc": "active"}}
	if path, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{}, member); path != "/chat" {
		t.Fatalf("member of the pinned organization: %s %v", path, q)
	}
	gh.SetOrg("acme-inc", 2002)
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{},
		oauthtest.Identity{ID: 12, Login: "squatter", Orgs: map[string]string{"acme-inc": "active"}}); q.Get("signin_error") != "not_allowed" {
		t.Fatalf("member of another organization under the pinned name: %v", q)
	}
	gh.SetOrg("acme-inc", 1001)

	off := true
	if code := owner.JSON(t, http.MethodPatch, "/api/users/"+string(browser.me.User.ID), dto.UserPatchReq{Disabled: &off}, nil); code != http.StatusOK {
		t.Fatalf("turn off = %d", code)
	}
	if _, q := signInVia(t, pltNewClient(gw), gh, "/api/auth/oauth/github/start", dto.SignInStartReq{}, octo); q.Get("signin_error") != "disabled" {
		t.Fatalf("turned off: %v", q)
	}
}
