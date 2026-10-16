package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/auth/oauth"
	"github.com/rielicat/bothy/internal/auth/oauth/oauthtest"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

const signInIP = "203.0.113.9"

type signInEnv struct {
	*env
	google, github *oauthtest.Server
	vault          *fakes.Vault
	owner          core.Actor
}

var (
	maya = oauthtest.Identity{Subject: "g-maya", Email: "maya@acme.com", HD: "acme.com", EmailVerified: true, Name: "Maya Chen"}
	pat  = oauthtest.Identity{Subject: "g-pat", Email: "pat@gmail.com", EmailVerified: true, Name: "Pat Doe"}
	lee  = oauthtest.Identity{ID: 4242, Login: "lee-gh", Name: "Lee", Orgs: map[string]string{"acme-inc": "active"},
		Emails: []oauthtest.Email{{Email: "lee@personal.dev", Primary: true, Verified: true}}}
)

// newSignInEnv has both providers on against fakes, in team mode, with
// allow_new off and the owner set up.
func newSignInEnv(t *testing.T, mut ...func(*config.Config)) *signInEnv {
	t.Helper()
	g := oauthtest.NewGoogle(t, "g-client", "g-secret")
	h := oauthtest.NewGitHub(t, "h-client", "h-secret")
	e := newEnv(t, append([]func(*config.Config){func(c *config.Config) {
		c.Mode = "multi"
		c.Server.PublicURL = "http://127.0.0.1:8080"
		c.Auth.SignIn = config.SignInCfg{
			Allow:  []string{"acme.com", "pat@gmail.com"},
			Google: config.GoogleSignIn{Enabled: true, ClientID: "g-client", ClientSecret: "secret:google_sign_in_client_secret", APIBase: g.URL},
			GitHub: config.GitHubSignIn{Enabled: true, ClientID: "h-client", ClientSecret: "secret:github_sign_in_client_secret",
				Orgs: []string{"acme-inc"}, APIBase: h.URL},
		}
	}}, mut...)...)
	// The fake signs id tokens with the wall clock.
	e.clock.Advance(time.Since(e.clock.Now()))
	v := fakes.NewVault(map[string]string{"google_sign_in_client_secret": "g-secret", "github_sign_in_client_secret": "h-secret"})
	e.s.SetSignIn(SignInDeps{Vault: v, HTTP: func(core.SystemDest) *http.Client { return &http.Client{Timeout: 5 * time.Second} }})
	se := &signInEnv{env: e, google: g, github: h, vault: v}
	se.owner = e.owner(t)
	if err := e.s.CompleteSetup(ctx, se.owner); err != nil {
		t.Fatal(err)
	}
	return se
}

func (e *signInEnv) fake(p core.SignInProvider) *oauthtest.Server {
	if p == core.SignInGoogle {
		return e.google
	}
	return e.github
}

// back follows the provider's redirect and returns what the callback gets.
func (e *signInEnv) back(t *testing.T, st SignInStart) SignInCallback {
	t.Helper()
	loc, err := oauthtest.Follow(ctx, nil, st.URL)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	q := u.Query()
	return SignInCallback{CookieState: st.State, State: q.Get("state"), Code: q.Get("code"), Error: q.Get("error"), IP: signInIP}
}

func (e *signInEnv) signIn(t *testing.T, p core.SignInProvider, who oauthtest.Identity) (*SignInResult, error) {
	t.Helper()
	e.fake(p).SignInAs(who)
	st, err := e.s.StartSignIn(ctx, p, "/chat/ses_1?q=a")
	if err != nil {
		t.Fatal(err)
	}
	return e.s.FinishSignIn(ctx, p, e.back(t, st))
}

// session signs u in with a password and returns its cookie and actor.
func (e *signInEnv) session(t *testing.T, username string) (string, core.Actor) {
	t.Helper()
	cookie, a, err := e.s.Login(ctx, username, goodPassword, signInIP)
	if err != nil {
		t.Fatal(err)
	}
	return cookie, a
}

func (e *signInEnv) link(t *testing.T, cookie string, a core.Actor, p core.SignInProvider, who oauthtest.Identity) (*SignInResult, error) {
	t.Helper()
	e.fake(p).SignInAs(who)
	st, err := e.s.StartLink(ctx, a, cookie, p)
	if err != nil {
		t.Fatal(err)
	}
	return e.s.FinishSignIn(ctx, p, e.back(t, st))
}

func toOAuth(id oauthtest.Identity) oauth.Identity {
	if id.Login != "" {
		return oauth.Identity{Subject: strconv.FormatInt(id.ID, 10), Login: id.Login, Name: id.Name}
	}
	return oauth.Identity{Subject: id.Subject, Email: id.Email, Name: id.Name}
}

func signInCode(err error) string {
	var se *SignInError
	if errors.As(err, &se) {
		return se.Code
	}
	return fmt.Sprintf("not a SignInError: %v", err)
}

func (e *signInEnv) repo(t *testing.T) core.SignInRepo {
	t.Helper()
	r, err := e.s.signInRepo()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSignInOptions(t *testing.T) {
	e := newSignInEnv(t)
	names := func() []string {
		opts, err := e.s.SignInOptions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, o := range opts {
			out = append(out, o.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Google", "GitHub"}) {
		t.Fatalf("options = %v", got)
	}
	e.cfg.Auth.SignIn.GitHub.Enabled = false
	e.cfg.Auth.SignIn.Google.ClientID = ""
	if got := names(); len(got) != 0 {
		t.Fatalf("off and incomplete = %v", got)
	}
	e.cfg.Auth.SignIn.GitHub.Enabled, e.cfg.Auth.SignIn.Google.ClientID = true, "g-client"
	e.cfg.Server.PublicURL = "http://bothy.acme.com"
	if got := names(); len(got) != 0 {
		t.Fatalf("plain http address = %v", got)
	}
	e.cfg.Server.PublicURL = "http://127.0.0.1:8080"
	e.cfg.Auth.SignIn.Allow = nil
	if got := names(); !slices.Equal(got, []string{"GitHub"}) {
		t.Fatalf("no allow list, GitHub orgs only = %v", got)
	}
	e.cfg.Auth.SignIn.Allow, e.cfg.Auth.SignIn.GitHub.Orgs = []string{"acme.com"}, nil
	if got := names(); !slices.Equal(got, []string{"Google"}) {
		t.Fatalf("only domains listed, no organizations = %v", got)
	}
	e.cfg.Auth.SignIn.GitHub.Orgs = []string{"acme-inc"}
	e.vault.Seal()
	if got := names(); len(got) != 0 {
		t.Fatalf("sealed = %v", got)
	}
	_ = e.vault.Unseal(ctx, nil)
	_ = e.vault.Delete(ctx, core.GlobalScope, "github_sign_in_client_secret", "")
	if got := names(); !slices.Equal(got, []string{"Google"}) {
		t.Fatalf("missing secret = %v", got)
	}
	if uses := e.vault.Uses; len(uses) != 0 {
		t.Fatalf("listing options read secrets: %v", uses)
	}
	st := e.s.SignInStatus(ctx)
	if st[1].Problem != "no_secret" || st[1].Ready || !st[1].Enabled || st[0].SecretSource != "vault" || !st[0].SecretSaved ||
		st[0].RedirectURI != "http://127.0.0.1:8080/api/auth/oauth/google/callback" || st[0].Orgs == nil || st[1].Orgs[0] != "acme-inc" {
		t.Fatalf("status %+v", st)
	}
	fs := e.s.checkSignIn(ctx)
	if len(fs) != 1 || fs[0].ID != "auth.sign_in" || !strings.HasPrefix(fs[0].Title, "GitHub") || fs[0].Fix != "/settings/security/github-sign-in" {
		t.Fatalf("findings %+v", fs)
	}

	fresh := newEnv(t)
	fresh.s.SetSignIn(SignInDeps{Vault: e.vault})
	if opts, _ := fresh.s.SignInOptions(ctx); len(opts) != 0 {
		t.Fatalf("before setup: %v", opts)
	}
	if _, err := fresh.s.StartSignIn(ctx, core.SignInGoogle, ""); signInCode(err) != SignInUnavailable {
		t.Fatalf("start before setup: %v", err)
	}
	unwired := newEnv(t, func(c *config.Config) { c.Auth.SignIn = e.cfg.Auth.SignIn })
	if st := unwired.s.SignInStatus(ctx); st[0].Problem != "unavailable" || st[0].Ready {
		t.Fatalf("without SetSignIn: %+v", st[0])
	}
	if fs := unwired.s.checkSignIn(ctx); len(fs) != 0 {
		t.Fatalf("findings without SetSignIn: %+v", fs)
	}
}

func TestStartSignIn(t *testing.T) {
	e := newSignInEnv(t)
	st, err := e.s.StartSignIn(ctx, core.SignInGoogle, "/chat")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	q := u.Query()
	if q.Get("state") != st.State || len(st.State) != 43 || q.Get("nonce") == "" || q.Get("code_challenge_method") != "S256" ||
		len(q.Get("code_challenge")) != 43 || q.Get("redirect_uri") != "http://127.0.0.1:8080/api/auth/oauth/google/callback" ||
		q.Has("hd") || st.MaxAge != 10*time.Minute {
		t.Fatalf("start %+v", st)
	}
	e.s.flows.mu.Lock()
	for k := range e.s.flows.m {
		if strings.Contains(k, st.State) {
			t.Error("the flow is kept under the plain state")
		}
	}
	e.s.flows.mu.Unlock()

	e.cfg.Auth.SignIn.Allow = []string{"acme.com"}
	st, _ = e.s.StartSignIn(ctx, core.SignInGoogle, "")
	if u, _ := url.Parse(st.URL); u.Query().Get("hd") != "acme.com" {
		t.Fatalf("a single domain is not hinted: %s", st.URL)
	}
	e.cfg.Auth.SignIn.Google.Enabled = false
	if _, err := e.s.StartSignIn(ctx, core.SignInGoogle, ""); signInCode(err) != SignInUnavailable {
		t.Fatalf("start while off: %v", err)
	}
	if _, err := e.s.StartSignIn(ctx, "okta", ""); signInCode(err) != SignInUnavailable {
		t.Fatalf("start of an unknown provider: %v", err)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "/chat",
		"/chat/ses_1?q=a":              "/chat/ses_1?q=a",
		"/settings/account":            "/settings/account",
		"//evil.example":               "/chat",
		"/\\evil.example":              "/chat",
		"https://evil.example/x":       "/chat",
		"chat":                         "/chat",
		"/api/auth/me":                 "/chat",
		"/api":                         "/chat",
		"/v1/models":                   "/chat",
		"/login":                       "/chat",
		"/login?next=/x":               "/chat",
		"/setup/step":                  "/chat",
		"/password?x=1":                "/chat",
		"/chat\r\nSet-Cookie: x":       "/chat",
		"/chat\tx":                     "/chat",
		"/" + strings.Repeat("a", 600): "/chat",
		"/loginx":                      "/loginx",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignInLinked(t *testing.T) {
	e := newSignInEnv(t)
	cookie, a := e.session(t, "owner")
	res, err := e.link(t, cookie, a, core.SignInGoogle, maya)
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent != IntentLink || res.Link.User != a.User || res.Link.Email != "maya@acme.com" {
		t.Fatalf("link %+v", res)
	}
	e.clock.Advance(time.Minute)
	res, err = e.signIn(t, core.SignInGoogle, maya)
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent != IntentSignIn || res.Next != "/chat/ses_1?q=a" || res.Created || res.Actor.User != a.User {
		t.Fatalf("sign-in %+v", res)
	}
	got, err := e.s.FromCookie(ctx, res.Cookie)
	if err != nil || got.User != a.User || got.Via != "cookie" {
		t.Fatalf("session = %+v, %v", got, err)
	}
	u, _ := e.store.Users().Get(ctx, a.User)
	if !u.LastLoginAt.Equal(e.clock.Now().Truncate(time.Millisecond)) {
		t.Errorf("last_login_at = %v", u.LastLoginAt)
	}
	l, _ := e.repo(t).Get(ctx, core.SignInGoogle, "g-maya")
	if !l.LastUsed.Equal(e.clock.Now().Truncate(time.Millisecond)) {
		t.Errorf("link last used = %v", l.LastUsed)
	}
	var login *core.AuditEntry
	for _, en := range e.audit.All() {
		if en.Action == "auth.login" && strings.Contains(string(en.Detail), `"google"`) {
			login = &en
		}
	}
	if login == nil || login.IP != signInIP || login.Target != userTarget(a.User) {
		t.Fatalf("auth.login rows: %+v", e.audit.All())
	}
	all, _ := json.Marshal(e.audit.All())
	for _, v := range e.google.Issued() {
		if strings.Contains(string(all), v) {
			t.Fatalf("the audit log carries an issued code or token")
		}
	}
	for _, f := range e.google.Authorizations() {
		if strings.Contains(string(all), f.State) || strings.Contains(string(all), f.Nonce) {
			t.Fatalf("the audit log carries a state or nonce")
		}
	}
	if !slices.Contains(e.vault.Uses, "google_sign_in_client_secret|sign-in:google") {
		t.Fatalf("secret uses %v", e.vault.Uses)
	}
}

func TestSignInFlowChecks(t *testing.T) {
	e := newSignInEnv(t)
	cookie, a := e.session(t, "owner")
	if _, err := e.link(t, cookie, a, core.SignInGoogle, maya); err != nil {
		t.Fatal(err)
	}
	e.google.SignInAs(maya)
	start := func() (SignInStart, SignInCallback) {
		st, err := e.s.StartSignIn(ctx, core.SignInGoogle, "/settings")
		if err != nil {
			t.Fatal(err)
		}
		return st, e.back(t, st)
	}

	_, cb := start()
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); err != nil {
		t.Fatal(err)
	}
	before := len(e.audit.All())
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInExpired {
		t.Errorf("replay: %v", err)
	}
	if len(e.audit.All()) != before {
		t.Error("an unknown state was audited")
	}

	_, cb = start()
	cb.CookieState = "someone-else"
	_, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb)
	if signInCode(err) != SignInExpired {
		t.Errorf("cookie mismatch: %v", err)
	}
	var se *SignInError
	if errors.As(err, &se) && se.Next != "/settings" {
		t.Errorf("failure keeps next: %+v", se)
	}
	if last := e.audit.All()[len(e.audit.All())-1]; last.Action != "auth.login_failed" || !strings.Contains(string(last.Detail), `"reason":"expired"`) {
		t.Errorf("cookie mismatch audit %+v", last)
	}

	_, cb = start()
	cb.CookieState = ""
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInExpired {
		t.Errorf("missing cookie: %v", err)
	}

	_, cb = start()
	e.clock.Advance(11 * time.Minute)
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInExpired {
		t.Errorf("after 11 minutes: %v", err)
	}

	_, cb = start()
	if _, err := e.s.FinishSignIn(ctx, core.SignInGitHub, cb); signInCode(err) != SignInExpired {
		t.Errorf("wrong provider path: %v", err)
	}

	e.google.Deny()
	_, cb = start()
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInCancelled {
		t.Errorf("access_denied: %v", err)
	}
	_, cb = start()
	cb.Code, cb.Error = "", "server_error"
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInUnavailable {
		t.Errorf("provider error: %v", err)
	}
	_, cb = start()
	cb.Code = strings.Repeat("c", 2049)
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInUnavailable {
		t.Errorf("oversize code: %v", err)
	}
	_, cb = start()
	cb.Code = "forged"
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInExpired {
		t.Errorf("a code the provider never issued: %v", err)
	}

	_, cb = start()
	e.cfg.Auth.SignIn.Google.Enabled = false
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInUnavailable {
		t.Errorf("turned off mid-flow: %v", err)
	}
	e.cfg.Auth.SignIn.Google.Enabled = true
	_, cb = start()
	e.cfg.Auth.SignIn.Google.ClientID = "another-client"
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInUnavailable {
		t.Errorf("client changed mid-flow: %v", err)
	}
	e.cfg.Auth.SignIn.Google.ClientID = "g-client"
	_, cb = start()
	e.cfg.Server.PublicURL = "http://localhost:8080"
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInUnavailable {
		t.Errorf("redirect address changed mid-flow: %v", err)
	}
}

func TestSignInGoogleList(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Config) { c.Auth.SignIn.AllowNew = true })
	for _, tc := range []struct {
		name string
		who  oauthtest.Identity
		code string
	}{
		{"listed address, verified", pat, ""},
		{"listed address, not verified", oauthtest.Identity{Subject: "g-pat2", Email: "pat@gmail.com", Name: "Pat"}, SignInNotAllowed},
		{"Workspace account of a listed domain", maya, ""},
		{"consumer account with a listed domain's address", oauthtest.Identity{Subject: "g-sam", Email: "sam@acme.com", EmailVerified: true}, SignInNotAllowed},
		{"unlisted", oauthtest.Identity{Subject: "g-kim", Email: "kim@gmail.com", EmailVerified: true}, SignInNotAllowed},
	} {
		res, err := e.signIn(t, core.SignInGoogle, tc.who)
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.code == "" && !res.Created:
			t.Errorf("%s: no account was made", tc.name)
		case tc.code != "" && signInCode(err) != tc.code:
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		case tc.code == SignInNotAllowed:
			var se *SignInError
			errors.As(err, &se)
			if se.Who != tc.who.Email {
				t.Errorf("%s: who = %q", tc.name, se.Who)
			}
		}
	}
	last := e.audit.All()[len(e.audit.All())-1]
	if last.Action != "auth.login_failed" || last.Target != "google:kim@gmail.com" || last.IP != signInIP {
		t.Errorf("refusal audit %+v", last)
	}
}

func TestSignInRemovedFromList(t *testing.T) {
	e := newSignInEnv(t)
	cookie, a := e.session(t, "owner")
	if _, err := e.link(t, cookie, a, core.SignInGoogle, pat); err != nil {
		t.Fatal(err)
	}
	if _, err := e.signIn(t, core.SignInGoogle, pat); err != nil {
		t.Fatal(err)
	}
	e.cfg.Auth.SignIn.Allow = []string{"acme.com"}
	if _, err := e.signIn(t, core.SignInGoogle, pat); signInCode(err) != SignInNotAllowed {
		t.Fatalf("taken off the list: %v", err)
	}
	if last := e.audit.All()[len(e.audit.All())-1]; last.Target != userTarget(a.User) {
		t.Errorf("a linked refusal names the account: %+v", last)
	}
	// Linking is checked too.
	e.cfg.Auth.SignIn.Allow = []string{"acme.com", "pat@gmail.com"}
	e.cfg.Auth.SignIn.GitHub.Orgs = nil
	if _, err := e.link(t, cookie, a, core.SignInGitHub, oauthtest.Identity{ID: 9, Login: "stranger"}); signInCode(err) != SignInNotAllowed {
		t.Fatalf("linking someone off the list: %v", err)
	}
}

func TestSignInGitHubList(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Config) { c.Auth.SignIn.AllowNew = true })
	res, err := e.signIn(t, core.SignInGitHub, oauthtest.Identity{ID: 1, Login: "patgh", Emails: []oauthtest.Email{
		{Email: "pat@personal.dev", Primary: true, Verified: true}, {Email: "pat@gmail.com", Verified: true}}})
	if err != nil || !res.Created {
		t.Fatalf("verified secondary address: %+v, %v", res, err)
	}
	l, _ := e.repo(t).Get(ctx, core.SignInGitHub, "1")
	if l.Email != "pat@gmail.com" || l.Login != "patgh" {
		t.Fatalf("stored link %+v, want the address that let them in", l)
	}
	if _, err := e.signIn(t, core.SignInGitHub, oauthtest.Identity{ID: 2, Login: "faker", Emails: []oauthtest.Email{
		{Email: "faker@personal.dev", Primary: true, Verified: true}, {Email: "boss@acme.com"}}}); signInCode(err) != SignInNotAllowed {
		t.Fatalf("unverified address: %v", err)
	}
	if _, err := e.signIn(t, core.SignInGitHub, oauthtest.Identity{ID: 6, Login: "former", Emails: []oauthtest.Email{
		{Email: "former@acme.com", Primary: true, Verified: true}}}); signInCode(err) != SignInNotAllowed {
		t.Fatalf("verified address at a listed domain: %v", err)
	}
	if res, err := e.signIn(t, core.SignInGitHub, lee); err != nil || !res.Created {
		t.Fatalf("organization member: %+v, %v", res, err)
	}
	l, _ = e.repo(t).Get(ctx, core.SignInGitHub, "4242")
	if l.Email != "lee@personal.dev" {
		t.Fatalf("member link %+v, want the primary address", l)
	}
	_, err = e.signIn(t, core.SignInGitHub, oauthtest.Identity{ID: 3, Login: "nobody"})
	var se *SignInError
	if !errors.As(err, &se) || se.Code != SignInNotAllowed || se.Who != "nobody" {
		t.Fatalf("neither: %v", err)
	}
	if _, err := e.signIn(t, core.SignInGitHub, oauthtest.Identity{ID: 5, Login: "sso", Orgs: map[string]string{"acme-inc": "restricted"}}); signInCode(err) != SignInOrgAccess {
		t.Fatalf("restricted organization: %v", err)
	}
	if got := e.github.WaitRevoked(6); len(got) != 6 {
		t.Fatalf("revoked %d tokens", len(got))
	}
}

func TestSignInAccounts(t *testing.T) {
	e := newSignInEnv(t)
	if _, err := e.signIn(t, core.SignInGoogle, maya); signInCode(err) != SignInNoAccount {
		t.Fatalf("unlinked with allow_new off: %v", err)
	}
	e.cfg.Auth.SignIn.AllowNew = true
	e.cfg.Mode = "single"
	if _, err := e.signIn(t, core.SignInGoogle, maya); signInCode(err) != SignInNoAccount {
		t.Fatalf("allow_new outside team mode: %v", err)
	}
	e.cfg.Mode = "multi"
	res, err := e.signIn(t, core.SignInGoogle, maya)
	if err != nil || !res.Created || res.Actor.Role != core.RoleMember {
		t.Fatalf("new member: %+v, %v", res, err)
	}
	u, _ := e.store.Users().Get(ctx, res.Actor.User)
	if u.PWHash != noPassword || u.Username != "maya" || u.DisplayName != "Maya Chen" || u.Role != core.RoleMember || u.Timezone == "" {
		t.Fatalf("new account %+v", u)
	}
	acts := e.audit.Actions()
	if !slices.Contains(acts, "user.create") {
		t.Fatalf("audit %v", acts)
	}
	if res2, err := e.signIn(t, core.SignInGoogle, maya); err != nil || res2.Created || res2.Actor.User != u.ID {
		t.Fatalf("second sign-in: %+v, %v", res2, err)
	}

	// Password sign-in for an account without one fails like an unknown
	// username and counts.
	if _, _, err := e.s.Login(ctx, "maya", "!", signInIP); !errors.Is(err, errCredentials) {
		t.Fatalf("password for a provider-only account: %v", err)
	}
	if u, _ := e.store.Users().Get(ctx, u.ID); u.FailedLogins != 1 {
		t.Fatalf("failed logins = %d", u.FailedLogins)
	}
	pw, err := e.s.ResetUserPassword(ctx, e.owner, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.Login(ctx, "maya", pw, signInIP); err != nil {
		t.Fatalf("after an admin gave a password: %v", err)
	}

	// Disabled accounts are refused.
	up, _ := e.store.Users().Get(ctx, u.ID)
	up.Disabled = true
	if err := e.s.UpdateUser(ctx, e.owner, up); err != nil {
		t.Fatal(err)
	}
	if _, err := e.signIn(t, core.SignInGoogle, maya); signInCode(err) != SignInDisabled {
		t.Fatalf("disabled: %v", err)
	}

	// Names: the local part, deduplicated, and a profile name somebody else
	// goes by falls back to the username.
	if _, err := e.signIn(t, core.SignInGoogle, oauthtest.Identity{Subject: "g-maya2", Email: "maya@acme.com", HD: "acme.com",
		EmailVerified: true, Name: "Maya Chen"}); err != nil {
		t.Fatal(err)
	}
	u2 := e.byLink(t, core.SignInGoogle, "g-maya2")
	if u2.Username != "maya-2" || u2.DisplayName != "maya-2" {
		t.Fatalf("second maya %+v", u2)
	}
	if _, err := e.signIn(t, core.SignInGoogle, oauthtest.Identity{Subject: "g-odd", Email: "..O'Brien+Bothy@acme.com", HD: "acme.com",
		EmailVerified: true, Name: "Owner"}); err != nil {
		t.Fatal(err)
	}
	if u3 := e.byLink(t, core.SignInGoogle, "g-odd"); u3.Username != "o-brien-bothy" || u3.DisplayName != "o-brien-bothy" {
		t.Fatalf("odd local part %+v", u3)
	}
}

func (e *signInEnv) byLink(t *testing.T, p core.SignInProvider, subject string) *core.User {
	t.Helper()
	l, err := e.repo(t).Get(ctx, p, subject)
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.store.Users().Get(ctx, l.User)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsernameBase(t *testing.T) {
	for _, tc := range []struct {
		p    core.SignInProvider
		id   oauthtest.Identity
		want string
	}{
		{core.SignInGoogle, oauthtest.Identity{Email: "alice@acme.com"}, "alice"},
		{core.SignInGoogle, oauthtest.Identity{Email: "Al.Ice_X@acme.com"}, "al.ice_x"},
		{core.SignInGoogle, oauthtest.Identity{Email: "@acme.com"}, "user"},
		{core.SignInGoogle, oauthtest.Identity{Email: "--++--@acme.com"}, "user"},
		{core.SignInGitHub, oauthtest.Identity{Login: "Octo-Cat"}, "octo-cat"},
		{core.SignInGitHub, oauthtest.Identity{Login: strings.Repeat("a", 39)}, strings.Repeat("a", 27)},
		{core.SignInGoogle, oauthtest.Identity{Email: "jöse@acme.com"}, "j-se"},
	} {
		got := usernameBase(tc.p, toOAuth(tc.id))
		if got != tc.want {
			t.Errorf("usernameBase(%+v) = %q, want %q", tc.id, got, tc.want)
		}
		if validUsername(got) != nil {
			t.Errorf("%q is not a valid username", got)
		}
	}
	users := []*core.User{{Username: "alice"}}
	for n := 2; n <= 99; n++ {
		users = append(users, &core.User{Username: fmt.Sprintf("alice-%d", n)})
	}
	if got := freeUsername(users, "alice"); !strings.HasPrefix(got, "alice-") || len(got) != len("alice-xxxx") || validUsername(got) != nil {
		t.Fatalf("after alice-99: %q", got)
	}
	long := strings.Repeat("a", 27)
	if got := freeUsername([]*core.User{{Username: long}}, long); validUsername(got) != nil {
		t.Fatalf("long base: %q", got)
	}
}

// Two callbacks for the same new person racing make one account.
func TestSignInCreateRace(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Config) { c.Auth.SignIn.AllowNew = true })
	ids := make([]core.UserID, 2)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			u, _, err := e.s.createFromSignIn(ctx, core.SignInGoogle, toOAuth(maya))
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = u.ID
		})
	}
	wg.Wait()
	if ids[0] != ids[1] || ids[0] == "" {
		t.Fatalf("two accounts: %v", ids)
	}
	us, _ := e.store.Users().List(ctx)
	if len(us) != 2 {
		t.Fatalf("%d users, want the owner and one more", len(us))
	}
}

func TestLinkAndUnlink(t *testing.T) {
	e := newSignInEnv(t)
	member := e.user(t, e.owner, "sam", core.RoleMember)
	cookie, a := e.session(t, "owner")
	mcookie, ma := e.session(t, "sam")

	res, err := e.link(t, cookie, a, core.SignInGitHub, lee)
	if err != nil {
		t.Fatal(err)
	}
	if res.Link.Login != "lee-gh" || res.Link.User != a.User {
		t.Fatalf("link %+v", res.Link)
	}
	last := e.audit.All()[len(e.audit.All())-1]
	if last.Action != "signin.link" || last.Outcome != "ok" || !strings.Contains(string(last.Detail), `"login":"lee-gh"`) {
		t.Fatalf("link audit %+v", last)
	}
	if _, err := e.s.StartLink(ctx, a, cookie, core.SignInGitHub); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second GitHub link start: %v", err)
	}
	if _, err := e.link(t, mcookie, ma, core.SignInGitHub, lee); signInCode(err) != SignInTaken {
		t.Fatalf("someone else's GitHub account: %v", err)
	}
	if last := e.audit.All()[len(e.audit.All())-1]; last.Action != "signin.link" || last.Outcome != "denied" || last.Target != userTarget(member.ID) {
		t.Fatalf("taken audit %+v", last)
	}

	// The account already has a Google link when the round trip ends.
	e.google.SignInAs(maya)
	st, err := e.s.StartLink(ctx, a, cookie, core.SignInGoogle)
	if err != nil {
		t.Fatal(err)
	}
	cb := e.back(t, st)
	if err := e.repo(t).Create(ctx, &core.SignIn{Provider: core.SignInGoogle, Subject: "g-other", User: a.User, CreatedAt: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInAlreadyLinked {
		t.Fatalf("already linked: %v", err)
	}
	if err := e.repo(t).Delete(ctx, a.User, core.SignInGoogle); err != nil {
		t.Fatal(err)
	}

	// The session that started it ended.
	st, _ = e.s.StartLink(ctx, ma, mcookie, core.SignInGoogle)
	cb = e.back(t, st)
	if err := e.s.Logout(ctx, mcookie); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb); signInCode(err) != SignInSignedOut {
		t.Fatalf("signed out meanwhile: %v", err)
	}
	if _, err := e.s.StartLink(ctx, core.Actor{User: a.User, Via: "token"}, "", core.SignInGoogle); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("token start: %v", err)
	}

	if err := e.s.Unlink(ctx, a, core.SignInGitHub); err != nil {
		t.Fatal(err)
	}
	if last := e.audit.All()[len(e.audit.All())-1]; last.Action != "signin.unlink" {
		t.Fatalf("unlink audit %+v", last)
	}
	if err := e.s.Unlink(ctx, a, core.SignInGitHub); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unlink twice: %v", err)
	}

	// An account without a password keeps its last way in.
	e.cfg.Auth.SignIn.AllowNew = true
	res, err = e.signIn(t, core.SignInGoogle, maya)
	if err != nil || !res.Created {
		t.Fatalf("new member: %+v, %v", res, err)
	}
	if err := e.s.Unlink(ctx, res.Actor, core.SignInGoogle); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("unlinking the only way in: %v", err)
	}
	if _, err := e.link(t, res.Cookie, res.Actor, core.SignInGitHub, oauthtest.Identity{ID: 77, Login: "maya-gh", Orgs: map[string]string{"acme-inc": "active"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Unlink(ctx, res.Actor, core.SignInGoogle); err != nil {
		t.Fatalf("unlinking one of two: %v", err)
	}
	opts, links, hasPassword, err := e.s.MySignIns(ctx, res.Actor.User)
	if err != nil || len(opts) != 2 || len(links) != 1 || links[0].Provider != core.SignInGitHub || hasPassword {
		t.Fatalf("MySignIns = %v, %v, %v, %v", opts, links, hasPassword, err)
	}
	if _, _, hasPassword, _ := e.s.MySignIns(ctx, a.User); !hasPassword {
		t.Fatal("the owner has a password")
	}
}

func TestLinkSameAccountTwice(t *testing.T) {
	e := newSignInEnv(t)
	cookie, a := e.session(t, "owner")
	e.google.SignInAs(maya)
	st1, _ := e.s.StartLink(ctx, a, cookie, core.SignInGoogle)
	st2, _ := e.s.StartLink(ctx, a, cookie, core.SignInGoogle)
	cb1, cb2 := e.back(t, st1), e.back(t, st2)
	if _, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb1); err != nil {
		t.Fatal(err)
	}
	if res, err := e.s.FinishSignIn(ctx, core.SignInGoogle, cb2); err != nil || res.Link.User != a.User {
		t.Fatalf("the same account again: %+v, %v", res, err)
	}
}

func TestFlowStoreBound(t *testing.T) {
	c := fakes.NewClock(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	fs := newFlowStore(c)
	fs.put("first", &flow{})
	c.Advance(time.Second)
	for i := range maxFlows {
		fs.put(fmt.Sprint("s", i), &flow{})
	}
	if fs.len() != maxFlows || fs.take("first") != nil || fs.take("s1") == nil {
		t.Fatalf("len %d; the oldest flow must go first", fs.len())
	}
	fs.put("refill", &flow{})
	c.Advance(flowTTL)
	fs.put("fresh", &flow{})
	if fs.len() != 1 || fs.take("fresh") == nil {
		t.Fatalf("expired flows not pruned at the bound: %d", fs.len())
	}
	if fs.take("") != nil || fs.take(strings.Repeat("x", 300)) != nil {
		t.Fatal("odd states")
	}
}

func TestCheckSignIn(t *testing.T) {
	e := newSignInEnv(t)
	if err := e.s.CheckSignIn(ctx, core.SignInGoogle, "", core.Secret{}); err != nil {
		t.Fatalf("saved Google client: %v", err)
	}
	if err := e.s.CheckSignIn(ctx, core.SignInGitHub, "", core.NewSecret([]byte("wrong"))); core.CodeOf(err) != core.FailKeyRejected {
		t.Fatalf("wrong GitHub secret: %v", err)
	}
	if err := e.s.CheckSignIn(ctx, core.SignInGitHub, "h-client", core.NewSecret([]byte("h-secret"))); err != nil {
		t.Fatalf("typed GitHub client: %v", err)
	}
	if !slices.Contains(e.vault.Uses, "google_sign_in_client_secret|sign-in:google:check") {
		t.Fatalf("uses %v", e.vault.Uses)
	}
	e.cfg.Auth.SignIn.Google.ClientSecret = ""
	if err := e.s.CheckSignIn(ctx, core.SignInGoogle, "", core.Secret{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("nothing saved: %v", err)
	}
}
