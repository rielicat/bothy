package config

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// signInErrs validates c and returns the errors under auth.sign_in.
func signInErrs(c *Config) []verr {
	v := &vctx{layerOf: func(string) int { return 0 }}
	validate(c, v)
	return slices.DeleteFunc(v.errs, func(e verr) bool { return !strings.HasPrefix(e.path, "auth.sign_in.") })
}

func readyGoogle(c *Config) {
	c.Server.PublicURL = "https://bothy.acme.com"
	c.Auth.SignIn.Allow = []string{"acme.com"}
	c.Auth.SignIn.Google = GoogleSignIn{Enabled: true, ClientID: "1234.apps.googleusercontent.com",
		ClientSecret: "secret:google_sign_in_client_secret"}
}

func readyGitHub(c *Config) {
	c.Server.PublicURL = "https://bothy.acme.com"
	c.Auth.SignIn.GitHub = GitHubSignIn{Enabled: true, ClientID: "Ov23liabc", ClientSecret: "secret:github_sign_in_client_secret",
		Orgs: []string{"acme-inc"}}
}

func allow(entries ...string) func(*Config) {
	return func(c *Config) { readyGoogle(c); c.Auth.SignIn.Allow = entries }
}

func TestSignInValidation(t *testing.T) {
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = "p" + strconv.Itoa(i) + "@acme.com"
	}
	for _, tc := range []struct {
		name string
		mut  func(*Config)
		path string
		msg  string
	}{
		{"client id with a space", func(c *Config) { readyGoogle(c); c.Auth.SignIn.Google.ClientID = "a b" }, "auth.sign_in.google.client_id", "printable"},
		{"upper-case domain", allow("Acme.com"), "auth.sign_in.allow", "lower case"},
		{"upper-case address", allow("Pat@acme.com"), "auth.sign_in.allow", "lower case"},
		{"single-label domain", allow("acme"), "auth.sign_in.allow", "neither an email address nor a domain"},
		{"domain with a space", allow("acme .com"), "auth.sign_in.allow", "neither"},
		{"gmail as a domain", allow("gmail.com"), "auth.sign_in.allow", "anyone can get an address at gmail.com; list each person's address instead"},
		{"proton as a domain", allow("proton.me"), "auth.sign_in.allow", "anyone can get an address at proton.me"},
		{"qq as a domain", allow("qq.com"), "auth.sign_in.allow", "anyone can get an address at qq.com"},
		{"GitHub noreply as a domain", allow("users.noreply.github.com"), "auth.sign_in.allow", "anyone can get an address at users.noreply.github.com"},
		{"a relay as a domain", allow("privaterelay.appleid.com"), "auth.sign_in.allow", "anyone can get an address at privaterelay.appleid.com"},
		{"regional free mail as a domain", allow("yahoo.co.jp"), "auth.sign_in.allow", "anyone can get an address at yahoo.co.jp"},
		{"two @", allow("pat@x@acme.com"), "auth.sign_in.allow", "not an email address"},
		{"empty local part", allow("@acme.com"), "auth.sign_in.allow", "not an email address"},
		{"space in local part", allow("pat smith@acme.com"), "auth.sign_in.allow", "not an email address"},
		{"long local part", allow(strings.Repeat("p", 65) + "@acme.com"), "auth.sign_in.allow", "not an email address"},
		{"bad address domain", allow("pat@acme"), "auth.sign_in.allow", "not an email address"},
		{"duplicate entry", allow("acme.com", "acme.com"), "auth.sign_in.allow", "listed twice"},
		{"too many entries", allow(tooMany...), "auth.sign_in.allow", "at most 200"},
		{"bad org", func(c *Config) { readyGitHub(c); c.Auth.SignIn.GitHub.Orgs = []string{"-acme"} }, "auth.sign_in.github.orgs", "organization"},
		{"duplicate org in another case", func(c *Config) { readyGitHub(c); c.Auth.SignIn.GitHub.Orgs = []string{"Acme", "acme"} }, "auth.sign_in.github.orgs", "duplicate"},
		{"api_base not loopback", func(c *Config) { c.Auth.SignIn.Google.APIBase = "https://accounts.example.com" }, "auth.sign_in.google.api_base", "loopback"},
		{"api_base with a query", func(c *Config) { c.Auth.SignIn.GitHub.APIBase = "http://127.0.0.1:9000/?x=1" }, "auth.sign_in.github.api_base", "query"},
		{"api_base with a user", func(c *Config) { c.Auth.SignIn.GitHub.APIBase = "http://u:p@127.0.0.1:9000" }, "auth.sign_in.github.api_base", "credentials"},
		{"enabled without client id", func(c *Config) { readyGoogle(c); c.Auth.SignIn.Google.ClientID = "" }, "auth.sign_in.google.enabled", "client_id"},
		{"enabled without secret", func(c *Config) { readyGitHub(c); c.Auth.SignIn.GitHub.ClientSecret = "" }, "auth.sign_in.github.enabled", "client_secret"},
		{"Google without an allow list", func(c *Config) { readyGoogle(c); c.Auth.SignIn.Allow = nil }, "auth.sign_in.google.enabled", "at least one address or domain"},
		{"Google with only orgs", func(c *Config) {
			readyGoogle(c)
			c.Auth.SignIn.Allow = nil
			c.Auth.SignIn.GitHub.Orgs = []string{"acme-inc"}
		}, "auth.sign_in.google.enabled", "auth.sign_in.allow"},
		{"GitHub without a list or orgs", func(c *Config) { readyGitHub(c); c.Auth.SignIn.GitHub.Orgs = nil }, "auth.sign_in.github.enabled", "or an organization"},
		{"GitHub with only domains", func(c *Config) {
			readyGitHub(c)
			c.Auth.SignIn.GitHub.Orgs = nil
			c.Auth.SignIn.Allow = []string{"acme.com"}
		}, "auth.sign_in.github.enabled", "domains apply to Google only"},
		{"enabled without public_url", func(c *Config) { readyGoogle(c); c.Server.PublicURL = "" }, "auth.sign_in.google.enabled", "server.public_url: Google sends people back"},
		{"enabled with plain http", func(c *Config) { readyGitHub(c); c.Server.PublicURL = "http://bothy.acme.com" }, "auth.sign_in.github.enabled", "https://"},
	} {
		c := Default()
		tc.mut(c)
		errs := signInErrs(c)
		if len(errs) != 1 || errs[0].path != tc.path || !strings.Contains(errs[0].msg, tc.msg) {
			t.Errorf("%s: errors %+v, want one on %s mentioning %q", tc.name, errs, tc.path, tc.msg)
		}
	}

	ok := []func(*Config){
		readyGoogle,
		readyGitHub,
		allow("acme.com", "pat@gmail.com", "sam@outlook.com", "o'brien+bothy@acme.co.uk", "eng.acme.io", "583231+octo@users.noreply.github.com", "kim@duck.com"),
		// GitHub alone may rely on the allow list instead of organizations.
		func(c *Config) {
			readyGitHub(c)
			c.Auth.SignIn.GitHub.Orgs = nil
			c.Auth.SignIn.Allow = []string{"pat@gmail.com"}
		},
		func(c *Config) { readyGoogle(c); c.Server.PublicURL = "http://127.0.0.1:8080" },
		func(c *Config) { readyGoogle(c); c.Auth.SignIn.Google.APIBase = "http://127.0.0.1:9000" },
		func(c *Config) { readyGoogle(c); c.Auth.SignIn.Google.APIBase = "https://localhost:9000" },
		func(c *Config) { readyGitHub(c); c.Auth.SignIn.GitHub.APIBase = "http://[::1]:9000" },
		// allow_new is not tied to the mode by validation; it takes effect only in team mode.
		func(c *Config) { readyGitHub(c); c.Mode = "single"; c.Auth.SignIn.AllowNew = true },
		// Off, nothing is required.
		func(c *Config) { c.Auth.SignIn.Google.ClientID = "x" },
	}
	for i, mut := range ok {
		c := Default()
		mut(c)
		if errs := signInErrs(c); len(errs) != 0 {
			t.Errorf("valid case %d: %+v", i, errs)
		}
	}
}

func TestSignInAddress(t *testing.T) {
	for _, tc := range []struct{ url, origin, problem string }{
		{"", "", "missing"},
		{"https://bothy.acme.com/", "https://bothy.acme.com", ""},
		{"https://bothy.acme.com:8443/some/path", "https://bothy.acme.com:8443", ""},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", ""},
		{"http://localhost:8080", "http://localhost:8080", ""},
		{"http://bothy.acme.com", "", "not_https"},
		{"http://10.0.0.5:8080", "", "not_https"},
	} {
		origin, problem := SignInAddress(tc.url)
		if origin != tc.origin || problem != tc.problem {
			t.Errorf("SignInAddress(%q) = %q, %q; want %q, %q", tc.url, origin, problem, tc.origin, tc.problem)
		}
	}
	if got := SignInRedirect("https://bothy.acme.com/chat", "google"); got != "https://bothy.acme.com/api/auth/oauth/google/callback" {
		t.Errorf("redirect = %q", got)
	}
	if got := SignInRedirect("http://bothy.acme.com", "github"); got != "" {
		t.Errorf("redirect for plain http = %q", got)
	}
}

// A broken combination falls back to "off" at the next lower layer, and the
// UI cannot switch a provider on half-configured or move public_url to plain
// http while one is on.
func TestSignInLayers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "auth:\n  sign_in:\n    google:\n      enabled: true\n      client_id: abc\n")
	m := load(t, dir)
	if m.Current().Auth.SignIn.Google.Enabled {
		t.Fatal("an incomplete provider is on")
	}
	found := false
	for _, e := range m.Effective().Errors {
		found = found || e.Path == "auth.sign_in.google.enabled"
	}
	if !found {
		t.Fatalf("no error on auth.sign_in.google.enabled: %+v", m.Effective().Errors)
	}

	dir = t.TempDir()
	m = load(t, dir)
	_, err := set(t, m, map[string]string{"auth.sign_in.github.enabled": "true"})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "client_id") {
		t.Fatalf("enabling while incomplete: %v", fe)
	}
	mustSet(t, m, map[string]string{
		"server.public_url":                 `"https://bothy.acme.com"`,
		"auth.sign_in.github.client_id":     `"Ov23liabc"`,
		"auth.sign_in.github.client_secret": `"secret:github_sign_in_client_secret"`,
		"auth.sign_in.allow":                `["acme.com", "pat@gmail.com"]`,
	})
	mustSet(t, m, map[string]string{"auth.sign_in.github.enabled": "true"})
	if !m.Current().Auth.SignIn.GitHub.Enabled {
		t.Fatal("complete provider is off")
	}
	_, err = set(t, m, map[string]string{"auth.sign_in.allow": `["gmail.com"]`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "anyone can get an address at gmail.com") {
		t.Fatalf("public mail domain: %v", fe)
	}
	_, err = set(t, m, map[string]string{"auth.sign_in.allow": `[]`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "auth.sign_in.github.enabled") {
		t.Fatalf("emptying the list while GitHub relies on it: %v", fe)
	}
	_, err = set(t, m, map[string]string{"auth.sign_in.allow": `["acme.com"]`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "auth.sign_in.github.enabled") {
		t.Fatalf("leaving only domains while GitHub relies on the list: %v", fe)
	}
	_, err = set(t, m, map[string]string{"server.public_url": `"http://bothy.acme.com"`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "https://") {
		t.Fatalf("plain http public_url while on: %v", fe)
	}
	mustSet(t, m, map[string]string{"server.public_url": `"http://127.0.0.1:8080"`})
	_, err = set(t, m, map[string]string{"auth.sign_in.github.client_secret": `"hunter2"`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "secret:github_sign_in_client_secret") {
		t.Fatalf("plaintext secret: %v", fe)
	}
	_, err = set(t, m, map[string]string{"auth.sign_in.github.api_base": `"http://127.0.0.1:9"`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "locked") {
		t.Fatalf("api_base from the UI: %v", fe)
	}

	var paths []string
	for _, sf := range m.Current().SecretFields() {
		paths = append(paths, sf.Path)
	}
	if !slices.Contains(paths, "auth.sign_in.github.client_secret") {
		t.Fatalf("SecretFields = %v", paths)
	}
}
