package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/auth/oauth/oauthtest"
	"github.com/rielicat/bothy/internal/core"
)

const (
	clientID = "client-123"
	secret   = "s3cret-value"
	redirect = "http://127.0.0.1:8080/api/auth/oauth/x/callback"
)

var ctx = context.Background()

// recorder hands out plain clients and remembers the egress destinations.
type recorder struct {
	mu    sync.Mutex
	dests []core.SystemDest
}

func (r *recorder) client(d core.SystemDest) *http.Client {
	r.mu.Lock()
	r.dests = append(r.dests, d)
	r.mu.Unlock()
	return &http.Client{Timeout: 5 * time.Second}
}

func newClient(rec *recorder) *Client {
	if rec == nil {
		rec = &recorder{}
	}
	return &Client{HTTP: rec.client}
}

func TestPKCE(t *testing.T) {
	// RFC 7636 Appendix B.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("Challenge = %q", got)
	}
	v, c := NewPKCE()
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(v) || c != Challenge(v) {
		t.Fatalf("NewPKCE = %q, %q", v, c)
	}
	if v2, _ := NewPKCE(); v2 == v {
		t.Fatal("two verifiers are equal")
	}
}

func TestAuthURLs(t *testing.T) {
	gep, _ := GoogleEndpoints("")
	r := AuthRequest{ClientID: clientID, RedirectURI: redirect, State: "st&ate", Nonce: "n1", Challenge: "ch"}
	u, _ := url.Parse(GoogleAuthURL(gep, r))
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != "https://accounts.google.com/o/oauth2/v2/auth" || q.Get("state") != "st&ate" ||
		q.Get("nonce") != "n1" || q.Get("code_challenge") != "ch" || q.Get("code_challenge_method") != "S256" ||
		q.Get("response_type") != "code" || q.Get("scope") != "openid email profile" || q.Get("redirect_uri") != redirect ||
		q.Get("client_id") != clientID || q.Get("prompt") != "select_account" || q.Has("hd") {
		t.Fatalf("Google URL %s", u)
	}
	r.DomainHint = "acme.com"
	u, _ = url.Parse(GoogleAuthURL(gep, r))
	if u.Query().Get("hd") != "acme.com" {
		t.Fatalf("Google URL with a hint %s", u)
	}

	hep, _ := GitHubEndpoints("")
	u, _ = url.Parse(GitHubAuthURL(hep, r))
	q = u.Query()
	if u.Host != "github.com" || q.Get("scope") != "read:org user:email" || q.Get("allow_signup") != "false" ||
		q.Get("code_challenge_method") != "S256" || q.Get("state") != "st&ate" || q.Has("nonce") || q.Has("hd") {
		t.Fatalf("GitHub URL %s", u)
	}

	for _, tc := range []struct {
		allow []string
		want  string
	}{
		{[]string{"acme.com"}, "acme.com"},
		{[]string{"acme.com", "eng.acme.com"}, ""},
		{[]string{"pat@gmail.com"}, ""},
		{[]string{"acme.com", "pat@gmail.com"}, ""},
		{nil, ""},
	} {
		if got := DomainHint(tc.allow); got != tc.want {
			t.Errorf("DomainHint(%v) = %q, want %q", tc.allow, got, tc.want)
		}
	}
}

func TestEndpoints(t *testing.T) {
	g, _ := GoogleEndpoints("")
	if g.TokenURL != "https://oauth2.googleapis.com/token" || len(g.Hosts) != 1 || g.Hosts[0] != "oauth2.googleapis.com:443" || g.Private {
		t.Fatalf("Google production %+v", g)
	}
	h, _ := GitHubEndpoints("")
	if h.APIBase != "https://api.github.com" || strings.Join(h.Hosts, ",") != "github.com:443,api.github.com:443" || h.Private {
		t.Fatalf("GitHub production %+v", h)
	}
	for _, ok := range []string{"http://127.0.0.1:9000", "https://localhost:9000/", "http://[::1]:9000"} {
		ep, err := GitHubEndpoints(ok)
		if err != nil || !ep.Private || !strings.HasSuffix(ep.TokenURL, "/login/oauth/access_token") || strings.Contains(ep.APIBase, "//login") {
			t.Errorf("override %q = %+v, %v", ok, ep, err)
		}
	}
	if ep, _ := GoogleEndpoints("http://127.0.0.1"); ep.Hosts[0] != "127.0.0.1:80" {
		t.Errorf("default port %v", ep.Hosts)
	}
	for _, bad := range []string{"http://10.0.0.1", "https://example.com", "http://u:p@127.0.0.1", "http://127.0.0.1/?q=1", "http://127.0.0.1/#f", "ftp://127.0.0.1"} {
		if _, err := GoogleEndpoints(bad); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("override %q accepted: %v", bad, err)
		}
	}
}

// googleCode runs the browser half against the fake and returns what the
// callback would receive.
func googleCode(t *testing.T, fake *oauthtest.Server, ep Endpoints, who oauthtest.Identity) Exchange {
	t.Helper()
	fake.SignInAs(who)
	verifier, challenge := NewPKCE()
	loc, err := oauthtest.Follow(ctx, nil, GoogleAuthURL(ep, AuthRequest{ClientID: clientID, RedirectURI: redirect, State: "s", Nonce: "nonce-1", Challenge: challenge}))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	return Exchange{Code: u.Query().Get("code"), Verifier: verifier, RedirectURI: redirect, ClientID: clientID,
		Secret: core.NewSecret([]byte(secret)), Nonce: "nonce-1"}
}

func newGoogle(t *testing.T) (*oauthtest.Server, Endpoints) {
	fake := oauthtest.NewGoogle(t, clientID, secret)
	ep, err := GoogleEndpoints(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	return fake, ep
}

func TestGoogleAllowList(t *testing.T) {
	fake, ep := newGoogle(t)
	rec := &recorder{}
	c := newClient(rec)
	rules := Rules{Allow: []string{"acme.com", "pat@gmail.com", "kate@acme.com", "kite.dev"}}
	for _, tc := range []struct {
		name string
		who  oauthtest.Identity
		ok   bool
	}{
		{"listed address, verified", oauthtest.Identity{Subject: "1", Email: "Pat@Gmail.com", EmailVerified: true}, true},
		{"listed address, not verified", oauthtest.Identity{Subject: "2", Email: "pat@gmail.com", EmailVerified: false}, false},
		{"Workspace account of a listed domain", oauthtest.Identity{Subject: "3", Email: "maya@acme.com", HD: "acme.com", EmailVerified: true}, true},
		{"Workspace secondary-domain address", oauthtest.Identity{Subject: "4", Email: "maya@acme.io", HD: "ACME.com", EmailVerified: true}, true},
		{"consumer account with a work address", oauthtest.Identity{Subject: "5", Email: "sam@acme.com", EmailVerified: true}, false},
		{"Workspace account of another domain", oauthtest.Identity{Subject: "6", Email: "lee@other.com", HD: "other.com", EmailVerified: true}, false},
		{"unlisted consumer account", oauthtest.Identity{Subject: "7", Email: "kim@gmail.com", EmailVerified: true}, false},
		{"listed address in another case", oauthtest.Identity{Subject: "8", Email: "Kate@acme.com", EmailVerified: true}, true},
		{"address that only folds to a listed one", oauthtest.Identity{Subject: "9", Email: "\u212Aate@acme.com", EmailVerified: true}, false},
		{"Workspace domain that only folds to a listed one", oauthtest.Identity{Subject: "10", Email: "kim@kite.dev", HD: "\u212Aite.dev", EmailVerified: true}, false},
	} {
		id, err := c.Google(ctx, ep, googleCode(t, fake, ep, tc.who), rules)
		want := ErrNotAllowed
		if !tc.who.EmailVerified {
			want = ErrUnverified
		}
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case !tc.ok && !errors.Is(err, want):
			t.Errorf("%s: err = %v, want %v", tc.name, err, want)
		case id.Subject != tc.who.Subject || id.Email != tc.who.Email:
			t.Errorf("%s: identity %+v", tc.name, id)
		}
	}
	if d := rec.dests[0]; d.Name != "signin:google" || !d.AllowPrivate || len(d.Hosts) != 1 {
		t.Errorf("egress destination %+v", d)
	}
}

func TestListComparesASCII(t *testing.T) {
	allow := []string{"kate@acme.com", "sam@acme.com", "kite.dev"}
	for _, tc := range []struct {
		email string
		ok    bool
	}{
		{"kate@acme.com", true},
		{"Kate@ACME.com", true},
		{"\u212Aate@acme.com", false}, // KELVIN SIGN folds to k
		{"\u017Fam@acme.com", false},  // long s folds to s
		{"kate@acme.com\x00", false},
		{"kate @acme.com", false},
		{"kite.dev", false},
		{"", false},
	} {
		if got := listedAddress(allow, tc.email); got != tc.ok {
			t.Errorf("listedAddress(%q) = %v", tc.email, got)
		}
	}
	for _, tc := range []struct {
		hd string
		ok bool
	}{
		{"kite.dev", true},
		{"KITE.dev", true},
		{"\u212Aite.dev", false},
		{"kate@acme.com", false},
		{"", false},
	} {
		if got := listedDomain(allow, tc.hd); got != tc.ok {
			t.Errorf("listedDomain(%q) = %v", tc.hd, got)
		}
	}
}

func TestGoogleClaims(t *testing.T) {
	fake, ep := newGoogle(t)
	c := newClient(nil)
	who := oauthtest.Identity{Subject: "1", Email: "maya@acme.com", HD: "acme.com", EmailVerified: true}
	rules := Rules{Allow: []string{"acme.com"}}
	for _, tc := range []struct {
		name    string
		mutate  func(map[string]any)
		allowed bool // false: ErrNotAllowed rather than a provider error
		ok      bool
	}{
		{"audience as an array", func(m map[string]any) { m["aud"] = []string{"other", clientID} }, true, true},
		{"no azp", func(m map[string]any) { delete(m, "azp") }, true, true},
		{"email_verified as a string", func(m map[string]any) { m["email_verified"] = "true" }, true, true},
		{"short issuer", func(m map[string]any) { m["iss"] = "accounts.google.com" }, true, true},
		{"expired within the leeway", func(m map[string]any) { m["exp"] = time.Now().Add(-30 * time.Second).Unix() }, true, true},
		{"wrong issuer", func(m map[string]any) { m["iss"] = "https://evil.example" }, true, false},
		{"wrong audience", func(m map[string]any) { m["aud"] = "other" }, true, false},
		{"azp of another client", func(m map[string]any) { m["azp"] = "other" }, true, false},
		{"expired", func(m map[string]any) { m["exp"] = time.Now().Add(-2 * time.Minute).Unix() }, true, false},
		{"issued in the future", func(m map[string]any) { m["iat"] = time.Now().Add(10 * time.Minute).Unix() }, true, false},
		{"wrong nonce", func(m map[string]any) { m["nonce"] = "other" }, true, false},
		{"no nonce", func(m map[string]any) { delete(m, "nonce") }, true, false},
		{"no subject", func(m map[string]any) { m["sub"] = "" }, true, false},
		{"no email", func(m map[string]any) { m["email"] = "" }, true, false},
		{"email_verified false", func(m map[string]any) { m["email_verified"] = false }, false, false},
		{"email_verified missing", func(m map[string]any) { delete(m, "email_verified") }, false, false},
		{"no hd", func(m map[string]any) { delete(m, "hd") }, false, false},
	} {
		fake.MutateClaims(tc.mutate)
		_, err := c.Google(ctx, ep, googleCode(t, fake, ep, who), rules)
		var pe *ProviderError
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.ok:
		case tc.allowed && !errors.As(err, &pe):
			t.Errorf("%s: err = %v, want a ProviderError", tc.name, err)
		case !tc.allowed && strings.HasPrefix(tc.name, "email_verified") && !errors.Is(err, ErrUnverified):
			t.Errorf("%s: err = %v, want ErrUnverified", tc.name, err)
		case !tc.allowed && !strings.HasPrefix(tc.name, "email_verified") && !errors.Is(err, ErrNotAllowed):
			t.Errorf("%s: err = %v, want ErrNotAllowed", tc.name, err)
		}
	}
}

func TestDecodeIDToken(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	for name, tok := range map[string]string{
		"empty":       "",
		"two parts":   "a.b",
		"bad base64":  "a.!!!.c",
		"not JSON":    "a." + enc([]byte("nope")) + ".c",
		"too large":   "a." + strings.Repeat("A", 17<<10) + ".c",
		"four parts":  "a.b.c.d",
		"array claim": "a." + enc([]byte(`[1]`)) + ".c",
	} {
		if _, err := decodeIDToken(tok); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	cl, err := decodeIDToken("a." + enc([]byte(`{"sub":"1","aud":["x","y"]}`)) + ".sig")
	if err != nil || cl.Sub != "1" || len(cl.Aud) != 2 {
		t.Fatalf("decode = %+v, %v", cl, err)
	}
}

func TestGoogleTokenErrors(t *testing.T) {
	fake, ep := newGoogle(t)
	c := newClient(nil)
	who := oauthtest.Identity{Subject: "1", Email: "pat@gmail.com", EmailVerified: true}
	rules := Rules{Allow: []string{"pat@gmail.com"}}

	x := googleCode(t, fake, ep, who)
	x.Secret = core.NewSecret([]byte("wrong"))
	_, err := c.Google(ctx, ep, x, rules)
	if core.CodeOf(err) != core.FailKeyRejected {
		t.Errorf("wrong secret: %v (%s)", err, core.CodeOf(err))
	}
	x = googleCode(t, fake, ep, who)
	if _, err := c.Google(ctx, ep, x, rules); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Google(ctx, ep, x, rules); !errors.Is(err, ErrRejected) {
		t.Errorf("replayed code: %v", err)
	}
	x = googleCode(t, fake, ep, who)
	x.Verifier, _ = NewPKCE()
	if _, err := c.Google(ctx, ep, x, rules); !errors.Is(err, ErrRejected) {
		t.Errorf("wrong verifier: %v", err)
	}
	x = googleCode(t, fake, ep, who)
	x.RedirectURI = "http://127.0.0.1:8080/elsewhere"
	if _, err := c.Google(ctx, ep, x, rules); !errors.Is(err, ErrRejected) {
		t.Errorf("another redirect address: %v", err)
	}
	x = googleCode(t, fake, ep, who)
	fake.FailNext("/token", http.StatusServiceUnavailable)
	if _, err := c.Google(ctx, ep, x, rules); core.CodeOf(err) != core.FailProviderError {
		t.Errorf("503: %v", err)
	}
}

func TestNoRedirectsFollowed(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("User-Agent") != "Bothy" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers %v", r.Header)
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	ep, _ := GoogleEndpoints(srv.URL)
	_, err := newClient(nil).Google(ctx, ep, Exchange{Code: "c", Verifier: "v", ClientID: clientID, Secret: core.NewSecret([]byte(secret)), Nonce: "n"}, Rules{})
	if err == nil || hits != 1 {
		t.Fatalf("err %v after %d requests", err, hits)
	}
}

func newGitHub(t *testing.T) (*oauthtest.Server, Endpoints) {
	fake := oauthtest.NewGitHub(t, clientID, secret)
	ep, err := GitHubEndpoints(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	return fake, ep
}

func githubCode(t *testing.T, fake *oauthtest.Server, ep Endpoints, who oauthtest.Identity) Exchange {
	t.Helper()
	fake.SignInAs(who)
	verifier, challenge := NewPKCE()
	loc, err := oauthtest.Follow(ctx, nil, GitHubAuthURL(ep, AuthRequest{ClientID: clientID, RedirectURI: redirect, State: "s", Challenge: challenge}))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	return Exchange{Code: u.Query().Get("code"), Verifier: verifier, RedirectURI: redirect, ClientID: clientID, Secret: core.NewSecret([]byte(secret))}
}

func TestGitHubAllowList(t *testing.T) {
	fake, ep := newGitHub(t)
	c := newClient(nil)
	rules := Rules{Allow: []string{"acme.com", "pat@gmail.com", "kate@acme.com", "sam@acme.com"}, Orgs: []string{"acme-inc", "acme-labs"}}
	emails := func(es ...oauthtest.Email) []oauthtest.Email { return es }
	for _, tc := range []struct {
		name  string
		who   oauthtest.Identity
		err   error
		email string
	}{
		{"verified secondary address on the list", oauthtest.Identity{ID: 1, Login: "pat", Emails: emails(
			oauthtest.Email{Email: "pat@personal.dev", Primary: true, Verified: true},
			oauthtest.Email{Email: "Pat@Gmail.com", Verified: true})}, nil, "Pat@Gmail.com"},
		// GitHub never re-checks an address it confirmed once, so a domain
		// entry would let in former staff and anyone reading a shared inbox.
		{"verified address at a listed domain", oauthtest.Identity{ID: 2, Login: "maya", Emails: emails(
			oauthtest.Email{Email: "maya@acme.com", Primary: true, Verified: true})}, ErrNotAllowed, "maya@acme.com"},
		{"verified address listed exactly", oauthtest.Identity{ID: 3, Login: "kate", Emails: emails(
			oauthtest.Email{Email: "kate@acme.com", Primary: true, Verified: true})}, nil, "kate@acme.com"},
		{"unverified address on the list", oauthtest.Identity{ID: 4, Login: "fake", Emails: emails(
			oauthtest.Email{Email: "fake@personal.dev", Primary: true, Verified: true},
			oauthtest.Email{Email: "boss@acme.com", Verified: false})}, ErrNotAllowed, "fake@personal.dev"},
		{"active member of a listed organization", oauthtest.Identity{ID: 5, Login: "lee", Emails: emails(
			oauthtest.Email{Email: "lee@personal.dev", Primary: true, Verified: true}),
			Orgs: map[string]string{"acme-labs": "active"}}, nil, "lee@personal.dev"},
		{"member without a verified address", oauthtest.Identity{ID: 6, Login: "noemail", Orgs: map[string]string{"ACME-INC": "active"}}, nil, ""},
		{"pending membership", oauthtest.Identity{ID: 7, Login: "new", Orgs: map[string]string{"acme-inc": "pending"}}, ErrNotAllowed, ""},
		{"organization restricts the app", oauthtest.Identity{ID: 8, Login: "sso", Orgs: map[string]string{"acme-inc": "restricted"}}, ErrOrgAccess, ""},
		{"restricted, then active", oauthtest.Identity{ID: 9, Login: "two", Orgs: map[string]string{"acme-inc": "restricted", "acme-labs": "active"}}, nil, ""},
		{"neither", oauthtest.Identity{ID: 10, Login: "stranger", Emails: emails(
			oauthtest.Email{Email: "stranger@other.com", Primary: true, Verified: true})}, ErrNotAllowed, "stranger@other.com"},
		{"listed address in another case", oauthtest.Identity{ID: 11, Login: "kate2", Emails: emails(
			oauthtest.Email{Email: "Kate@acme.com", Primary: true, Verified: true})}, nil, "Kate@acme.com"},
		{"address that only folds to a listed one", oauthtest.Identity{ID: 12, Login: "kelvin", Emails: emails(
			oauthtest.Email{Email: "\u212Aate@acme.com", Primary: true, Verified: true})}, ErrNotAllowed, "\u212Aate@acme.com"},
		{"address with a long s that only folds to a listed one", oauthtest.Identity{ID: 13, Login: "longs", Emails: emails(
			oauthtest.Email{Email: "\u017Fam@acme.com", Primary: true, Verified: true})}, ErrNotAllowed, "\u017Fam@acme.com"},
	} {
		id, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, tc.who), rules)
		switch {
		case tc.err == nil && err != nil, tc.err != nil && !errors.Is(err, tc.err):
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.err)
		case id.Login != tc.who.Login || id.Email != tc.email || id.Subject == "":
			t.Errorf("%s: identity %+v, want email %q", tc.name, id, tc.email)
		}
	}
	if got := fake.WaitRevoked(13); len(got) != 13 {
		t.Fatalf("revoked %d tokens after 13 sign-ins, refusals included", len(got))
	}
}

func TestGitHubFailures(t *testing.T) {
	fake, ep := newGitHub(t)
	c := newClient(nil)
	who := oauthtest.Identity{ID: 1, Login: "pat", Emails: []oauthtest.Email{{Email: "pat@acme.com", Primary: true, Verified: true}},
		Orgs: map[string]string{"acme-inc": "active"}}

	fake.FailNext("/user/emails", http.StatusInternalServerError)
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Allow: []string{"pat@acme.com"}}); core.CodeOf(err) != core.FailProviderError {
		t.Errorf("unreadable addresses with an address listed: %v", err)
	}
	fake.FailNext("/user/emails", http.StatusInternalServerError)
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Allow: []string{"acme.com"}}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("unreadable addresses with only domains listed: %v", err)
	}
	fake.FailNext("/user/emails", http.StatusInternalServerError)
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Allow: []string{"acme.com"}, Orgs: []string{"acme-inc"}}); err != nil {
		t.Errorf("unreadable addresses, member anyway: %v", err)
	}
	fake.FailNext("/user/memberships/orgs/acme-inc", http.StatusInternalServerError)
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Orgs: []string{"acme-inc"}}); core.CodeOf(err) != core.FailProviderError {
		t.Errorf("membership 500: %v", err)
	}
	fake.FailNext("/user", http.StatusBadGateway)
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Orgs: []string{"acme-inc"}}); core.CodeOf(err) != core.FailProviderError {
		t.Errorf("user 502: %v", err)
	}

	x := githubCode(t, fake, ep, who)
	x.Secret = core.NewSecret([]byte("wrong"))
	if _, err := c.GitHub(ctx, ep, x, Rules{Orgs: []string{"acme-inc"}}); core.CodeOf(err) != core.FailKeyRejected {
		t.Errorf("wrong secret: %v", err)
	}
	x = githubCode(t, fake, ep, who)
	x.Verifier, _ = NewPKCE()
	if _, err := c.GitHub(ctx, ep, x, Rules{Orgs: []string{"acme-inc"}}); !errors.Is(err, ErrRejected) {
		t.Errorf("wrong verifier: %v", err)
	}
	x = githubCode(t, fake, ep, who)
	x.RedirectURI = "http://127.0.0.1:8080/elsewhere"
	if _, err := c.GitHub(ctx, ep, x, Rules{Orgs: []string{"acme-inc"}}); err == nil || !strings.Contains(err.Error(), "redirect_uri_mismatch") {
		t.Errorf("another redirect address: %v", err)
	}
}

// A pinned organization must be the one a membership belongs to: whoever
// takes a renamed organization's old name can't let their members in.
func TestGitHubPinnedOrgs(t *testing.T) {
	fake, ep := newGitHub(t)
	c := newClient(nil)
	fake.SetOrg("acme-inc", 100)
	who := oauthtest.Identity{ID: 1, Login: "lee", Orgs: map[string]string{"acme-inc": "active"}}
	pinned := Rules{Orgs: []string{"Acme-Inc"}, OrgIDs: map[string]int64{"acme-inc": 100}}
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), pinned); err != nil {
		t.Fatalf("member of the pinned organization: %v", err)
	}
	fake.SetOrg("acme-inc", 200)
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), pinned); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("the name now belongs to another organization: %v", err)
	}
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Orgs: []string{"acme-inc"}}); err != nil {
		t.Fatalf("not pinned, matched by name: %v", err)
	}
	// A pin is found by name, never by position: one for another name
	// doesn't apply.
	renamed := Rules{Orgs: []string{"labs", "acme-inc"}, OrgIDs: map[string]int64{"acme-two": 100}}
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), renamed); err != nil {
		t.Fatalf("a pin under another name: %v", err)
	}
	if _, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Orgs: []string{"acme-inc"}, OrgIDs: map[string]int64{"acme-inc": 0}}); err != nil {
		t.Fatalf("a 0 id is not pinned: %v", err)
	}
}

// GitHub limiting requests is neither a refusal nor an organization's
// restriction.
func TestGitHubRateLimited(t *testing.T) {
	fake, ep := newGitHub(t)
	c := newClient(nil)
	who := oauthtest.Identity{ID: 1, Login: "lee", Orgs: map[string]string{"acme-inc": "active"}}
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		fake.RateLimitNext("/user/memberships/orgs/acme-inc", status)
		_, err := c.GitHub(ctx, ep, githubCode(t, fake, ep, who), Rules{Orgs: []string{"acme-inc"}})
		if core.CodeOf(err) != core.FailRateLimited || errors.Is(err, ErrOrgAccess) || errors.Is(err, ErrNotAllowed) {
			t.Errorf("membership limited with %d: %v", status, err)
		}
	}
}

func TestGitHubOrgID(t *testing.T) {
	fake, ep := newGitHub(t)
	rec := &recorder{}
	c := newClient(rec)
	fake.SetOrg("Acme-Inc", 4242)
	if id, err := c.GitHubOrgID(ctx, ep, "acme-inc"); err != nil || id != 4242 {
		t.Fatalf("known organization: %d, %v", id, err)
	}
	if _, err := c.GitHubOrgID(ctx, ep, "nope"); !errors.Is(err, ErrUnknownOrg) {
		t.Fatalf("unknown organization: %v", err)
	}
	fake.FailNext("/orgs/acme-inc", http.StatusInternalServerError)
	if _, err := c.GitHubOrgID(ctx, ep, "acme-inc"); core.CodeOf(err) != core.FailProviderError {
		t.Fatalf("500: %v", err)
	}
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		fake.RateLimitNext("/orgs/acme-inc", status)
		if _, err := c.GitHubOrgID(ctx, ep, "acme-inc"); core.CodeOf(err) != core.FailRateLimited {
			t.Fatalf("limited with %d: %v", status, err)
		}
	}
	// A 403 with requests left is not a limit.
	fake.FailNext("/orgs/acme-inc", http.StatusForbidden)
	if _, err := c.GitHubOrgID(ctx, ep, "acme-inc"); core.CodeOf(err) != core.FailProviderError {
		t.Fatalf("403: %v", err)
	}
	if d := rec.dests[0]; d.Name != "signin:github" || !d.AllowPrivate {
		t.Fatalf("egress destination %+v", d)
	}
}

// No code or token the provider issued ever appears in an error.
func TestErrorsCarryNoTokens(t *testing.T) {
	gfake, gep := newGoogle(t)
	hfake, hep := newGitHub(t)
	c := newClient(nil)
	var errs []error
	collect := func(_ Identity, err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	gwho := oauthtest.Identity{Subject: "1", Email: "kim@gmail.com", EmailVerified: true}
	collect(c.Google(ctx, gep, googleCode(t, gfake, gep, gwho), Rules{Allow: []string{"acme.com"}}))
	gfake.MutateClaims(func(m map[string]any) { m["nonce"] = "x" })
	collect(c.Google(ctx, gep, googleCode(t, gfake, gep, gwho), Rules{Allow: []string{"kim@gmail.com"}}))
	hwho := oauthtest.Identity{ID: 1, Login: "kim", Orgs: map[string]string{"acme-inc": "restricted"}}
	collect(c.GitHub(ctx, hep, githubCode(t, hfake, hep, hwho), Rules{Orgs: []string{"acme-inc"}}))
	hfake.FailNext("/user/memberships/orgs/acme-inc", http.StatusInternalServerError)
	collect(c.GitHub(ctx, hep, githubCode(t, hfake, hep, hwho), Rules{Orgs: []string{"acme-inc"}}))
	if len(errs) != 4 {
		t.Fatalf("%d errors, want 4", len(errs))
	}
	issued := append(gfake.Issued(), hfake.Issued()...)
	for _, err := range errs {
		for _, tok := range issued {
			if strings.Contains(err.Error(), tok) {
				t.Fatalf("error %q carries an issued value", err)
			}
		}
	}
}

func TestChecks(t *testing.T) {
	gfake, gep := newGoogle(t)
	hfake, hep := newGitHub(t)
	c := newClient(nil)
	good, bad := core.NewSecret([]byte(secret)), core.NewSecret([]byte("wrong"))
	if err := c.CheckGoogle(ctx, gep, clientID, "", good); err != nil {
		t.Errorf("Google accepted client: %v", err)
	}
	if err := c.CheckGoogle(ctx, gep, clientID, redirect, bad); core.CodeOf(err) != core.FailKeyRejected {
		t.Errorf("Google wrong secret: %v", err)
	}
	if err := c.CheckGitHub(ctx, hep, clientID, redirect, good); err != nil {
		t.Errorf("GitHub accepted client: %v", err)
	}
	if err := c.CheckGitHub(ctx, hep, "other", redirect, good); core.CodeOf(err) != core.FailKeyRejected {
		t.Errorf("GitHub wrong client: %v", err)
	}
	hfake.TokenError("redirect_uri_mismatch")
	if err := c.CheckGitHub(ctx, hep, clientID, redirect, good); core.CodeOf(err) != core.FailProviderError || !strings.Contains(err.Error(), "redirect_uri_mismatch") {
		t.Errorf("GitHub redirect mismatch: %v", err)
	}
	gfake.FailNext("/token", http.StatusInternalServerError)
	if err := c.CheckGoogle(ctx, gep, clientID, "", good); core.CodeOf(err) != core.FailProviderError {
		t.Errorf("Google 500: %v", err)
	}
	dead, _ := GoogleEndpoints("http://127.0.0.1:1")
	if err := c.CheckGoogle(ctx, dead, clientID, "", good); core.CodeOf(err) != core.FailUnreachable {
		t.Errorf("nothing listening: %v (%s)", err, core.CodeOf(err))
	}
}

// The fakes also run outside a test, on a fixed address, for local stubs.
func TestStartOnAddress(t *testing.T) {
	s, err := oauthtest.StartGitHub(clientID, secret, oauthtest.Listen("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, err := http.Get(s.URL + "/user") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /user = %d", resp.StatusCode)
	}
	if s.Handler() == nil {
		t.Fatal("no handler")
	}
}
