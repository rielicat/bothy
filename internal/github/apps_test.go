package github

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/github/githubtest"
)

const bothyOrigin = "http://127.0.0.1:8080"

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// lockedBuffer collects log lines written from any goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// logTo rebuilds the service with a logger writing to the returned buffer.
func (f *fx) logTo() *lockedBuffer {
	f.t.Helper()
	var buf lockedBuffer
	svc, err := New(Deps{Config: config.Static(f.cfg), Store: f.store, Vault: f.vault, Egress: f.egress, Bus: f.bus,
		Audit: f.audit, DataDir: f.data, Clock: f.clock, Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		f.t.Fatal(err)
	}
	f.svc = svc
	return &buf
}

// postManifest posts a handoff's manifest to the fake's new-app page, as the
// handoff page's form does, and returns the code its redirect carries.
func (f *fx) postManifest(h *core.GitHubAppHandoff, state string) string {
	f.t.Helper()
	resp, err := noRedirect.PostForm(h.Action+"?state="+url.QueryEscape(state), url.Values{"manifest": {h.Manifest}})
	if err != nil {
		f.t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || loc.Query().Get("state") != state || loc.Query().Get("code") == "" {
		f.t.Fatalf("new-app page = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func (f *fx) makeApp(org string) *core.GitHubOwnApp { return f.makeAppReplacing(org, "") }

// makeAppReplacing runs the manifest flow against the fake: the handoff's
// form post, then CreateApp with the code GitHub's redirect brought back.
func (f *fx) makeAppReplacing(org, replaces string) *core.GitHubOwnApp {
	f.t.Helper()
	tgt := core.GitHubAppTarget{Org: org, Fallback: "octocat"}
	h, err := f.svc.AppManifest(tgt, bothyOrigin)
	if err != nil {
		f.t.Fatal(err)
	}
	a, err := f.svc.CreateApp(f.ctx, f.postManifest(h, "s1"), tgt, replaces, "usr_owner")
	if err != nil {
		f.t.Fatalf("CreateApp: %v", err)
	}
	return a
}

// installOnGitHub opens the app's install page and returns the installation
// id the redirect to the setup URL carries.
func (f *fx) installOnGitHub(a *core.GitHubOwnApp) int64 {
	f.t.Helper()
	resp, err := noRedirect.Get(a.InstallURL + "?state=s2")
	if err != nil {
		f.t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), bothyOrigin+"/api/github/apps/installed?") {
		f.t.Fatalf("install page = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	id, err := strconv.ParseInt(loc.Query().Get("installation_id"), 10, 64)
	if err != nil {
		f.t.Fatalf("installation_id in %q", loc)
	}
	return id
}

func (f *fx) secretNames() []string {
	f.t.Helper()
	list, err := f.vault.List(f.ctx, core.GlobalScope)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, m := range list {
		out = append(out, m.Name)
	}
	slices.Sort(out)
	return out
}

func (f *fx) audits(action string) []core.AuditEntry {
	var out []core.AuditEntry
	for _, e := range f.audit.All() {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func TestAppManifest(t *testing.T) {
	for _, tc := range []struct{ base, org, action, web string }{
		{"https://api.github.com", "acme", "https://github.com/organizations/acme/settings/apps/new", "https://github.com"},
		{"https://ghe.example/api/v3", "", "https://ghe.example/settings/apps/new", "https://ghe.example"},
	} {
		f := newFx(t, func(c *config.Config) { c.GitHub.APIBase = tc.base })
		h, err := f.svc.AppManifest(core.GitHubAppTarget{Org: tc.org, Fallback: "sam"}, "https://bothy.acme.dev")
		if err != nil || h.Action != tc.action || h.WebOrigin != tc.web || h.Org != tc.org {
			t.Errorf("%s: handoff = %+v %v", tc.base, h, err)
		}
	}

	f := newFx(t)
	h, err := f.svc.AppManifest(core.GitHubAppTarget{Org: "acme", Fallback: "sam"}, bothyOrigin+"/")
	if err != nil {
		t.Fatal(err)
	}
	if h.Action != f.gh.URL+"/organizations/acme/settings/apps/new" || h.WebOrigin != f.gh.URL || h.Name != "Bothy (acme)" {
		t.Errorf("handoff = %+v", h)
	}
	if !strings.HasPrefix(h.Manifest, `{"name":"Bothy (acme)","url":"http://127.0.0.1:8080","description":"Lets Bothy at http://127.0.0.1:8080 read code`) {
		t.Errorf("manifest order: %s", h.Manifest)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(h.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	})
	if !slices.Equal(keys, []string{"default_events", "default_permissions", "description", "name", "public", "redirect_url", "setup_on_update", "setup_url", "url"}) {
		t.Errorf("manifest keys = %v", keys)
	}
	if m["redirect_url"] != bothyOrigin+"/api/github/apps/created" || m["setup_url"] != bothyOrigin+"/api/github/apps/installed" ||
		m["public"] != false || m["setup_on_update"] != true || len(m["default_events"].([]any)) != 0 {
		t.Errorf("manifest = %v", m)
	}
	perms := map[string]string{}
	for k, v := range m["default_permissions"].(map[string]any) {
		perms[k] = v.(string)
	}
	want := map[string]string{"metadata": "read", "contents": "write", "pull_requests": "write", "issues": "write", "actions": "write"}
	if len(perms) != len(want) {
		t.Errorf("permissions = %v", perms)
	}
	for k, v := range want {
		if perms[k] != v {
			t.Errorf("permission %s = %q, want %q", k, perms[k], v)
		}
	}
	for _, never := range []string{"administration", "secrets", "workflows", "environments", "deployments", "checks", "statuses", "members"} {
		if _, ok := perms[never]; ok {
			t.Errorf("manifest asks for %s", never)
		}
	}

	h, err = f.svc.AppManifest(core.GitHubAppTarget{Fallback: "sam"}, bothyOrigin)
	if err != nil || h.Name != "Bothy (sam)" || h.Action != f.gh.URL+"/settings/apps/new" || h.Org != "" {
		t.Errorf("personal handoff = %+v %v", h, err)
	}
	h, err = f.svc.AppManifest(core.GitHubAppTarget{Org: "acme", Name: "  Acme deploys  "}, bothyOrigin)
	if err != nil || h.Name != "Acme deploys" {
		t.Errorf("named handoff = %+v %v", h, err)
	}
	long := strings.Repeat("a", 30)
	if got := defaultAppName(long); got != "Bothy ("+long[:26]+")" || len([]rune(got)) != 34 {
		t.Errorf("defaultAppName(30 chars) = %q", got)
	}
	if got := defaultAppName("acme"); got != "Bothy (acme)" {
		t.Errorf("defaultAppName = %q", got)
	}

	for _, bad := range []struct {
		t      core.GitHubAppTarget
		origin string
	}{
		{core.GitHubAppTarget{Org: "-acme"}, bothyOrigin},
		{core.GitHubAppTarget{Org: "acme/evil"}, bothyOrigin},
		{core.GitHubAppTarget{Org: "acme", Name: strings.Repeat("x", 35)}, bothyOrigin},
		{core.GitHubAppTarget{Org: "acme", Name: "Bothy\nbot"}, bothyOrigin},
		{core.GitHubAppTarget{Org: "acme"}, "ftp://bothy"},
		{core.GitHubAppTarget{Org: "acme"}, "http://"},
		{core.GitHubAppTarget{Org: "acme"}, "http://u:p@bothy"},
		{core.GitHubAppTarget{Org: "acme"}, "http://bothy/sub"},
		{core.GitHubAppTarget{Org: "acme"}, "http://bothy?x=1"},
		{core.GitHubAppTarget{Org: "acme"}, "http://bothy#f"},
	} {
		if _, err := f.svc.AppManifest(bad.t, bad.origin); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("AppManifest(%+v, %q) = %v, want ErrInvalid", bad.t, bad.origin, err)
		}
	}
}

func TestCreateApp(t *testing.T) {
	f := newFx(t)
	logs := f.logTo()
	tgt := core.GitHubAppTarget{Org: "acme"}
	h, err := f.svc.AppManifest(tgt, bothyOrigin)
	if err != nil {
		t.Fatal(err)
	}
	// Ask GitHub for a webhook too, so its answer carries a webhook secret
	// that must go nowhere.
	var m map[string]any
	_ = json.Unmarshal([]byte(h.Manifest), &m)
	m["hook_attributes"] = map[string]any{"url": bothyOrigin + "/hook"}
	b, _ := json.Marshal(m)
	h.Manifest = string(b)
	code := f.postManifest(h, "s1")

	a, err := f.svc.CreateApp(f.ctx, code, tgt, "", "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	made := f.gh.Apps()[0]
	if made.WebhookSecret == "" {
		t.Fatal("the fake sent no webhook secret")
	}
	if a.AppID != made.ID || a.Slug != "bothy-acme" || a.Name != "Bothy (acme)" || a.Owner != "acme" || a.OwnerID != githubtest.AccountID("acme") ||
		a.OwnerType != "Organization" ||
		a.ClientID != made.ClientID || a.KeyRef != "secret:github_"+a.ID+"_key" || a.ClientSecretRef != "secret:github_"+a.ID+"_client_secret" ||
		a.Connection != "" || a.CreatedBy != "usr_owner" || a.State != "pending" {
		t.Errorf("app = %+v", a)
	}
	if a.URL != f.gh.URL+"/apps/bothy-acme" || a.InstallURL != f.gh.URL+"/apps/bothy-acme/installations/new" ||
		a.SettingsURL != f.gh.URL+"/organizations/acme/settings/apps/bothy-acme" {
		t.Errorf("app links = %q %q %q", a.URL, a.InstallURL, a.SettingsURL)
	}
	if got := f.secretNames(); !slices.Equal(got, []string{"github_" + a.ID + "_client_secret", "github_" + a.ID + "_key"}) {
		t.Errorf("vault = %v", got)
	}
	if k, err := f.vault.Get(f.ctx, core.GlobalScope, "github_"+a.ID+"_key", "test"); err != nil || k.Reveal() != string(made.PEM) {
		t.Errorf("stored key = %v", err)
	}
	if s, err := f.vault.Get(f.ctx, core.GlobalScope, "github_"+a.ID+"_client_secret", "test"); err != nil || s.Reveal() != made.ClientSecret {
		t.Errorf("stored client secret = %v", err)
	}
	if stored, err := f.store.App(f.ctx, a.ID); err != nil || stored.AppID != a.AppID || stored.KeyRef != a.KeyRef || stored.URL != a.URL {
		t.Errorf("stored app = %+v %v", stored, err)
	}
	ok := f.audits("github.app_create")
	if len(ok) != 1 || ok[0].Outcome != "ok" || ok[0].Target != a.ID || ok[0].Actor != "user:usr_owner" ||
		!strings.Contains(string(ok[0].Detail), `"slug":"bothy-acme"`) || !strings.Contains(string(ok[0].Detail), `"contents":"write"`) {
		t.Errorf("app_create audit = %+v", ok)
	}

	// The code is spent: a replay fails and stores nothing more.
	if _, err := f.svc.CreateApp(f.ctx, code, tgt, "", "usr_owner"); err == nil {
		t.Fatal("a spent code created another app")
	}
	if apps, _ := f.store.Apps(f.ctx); len(apps) != 1 || len(f.secretNames()) != 2 {
		t.Errorf("after a replay: %d apps, vault %v", len(apps), f.secretNames())
	}
	failed := f.audits("github.app_create")
	if len(failed) != 2 || failed[1].Outcome != "error" || !strings.Contains(string(failed[1].Detail), `"owner":"acme"`) {
		t.Errorf("failed app_create audit = %+v", failed)
	}

	all, _ := json.Marshal(f.audit.All())
	for name, secret := range map[string]string{"code": code, "key": strings.Split(string(made.PEM), "\n")[1], "client secret": made.ClientSecret,
		"webhook secret": made.WebhookSecret} {
		if strings.Contains(string(all), secret) {
			t.Errorf("the %s is in the audit log", name)
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the %s is in the logs", name)
		}
	}
	for _, s := range f.secretNames() {
		v, _ := f.vault.Get(f.ctx, core.GlobalScope, s, "test")
		if strings.Contains(v.Reveal(), made.WebhookSecret) {
			t.Error("the webhook secret was stored")
		}
	}
}

// GitHub Enterprise Server serves app pages under /github-apps/: the links,
// and the install page the browser is sent to, follow the html_url GitHub
// gave.
func TestCreateAppEnterprise(t *testing.T) {
	f := newFx(t)
	f.gh.Enterprise = true
	a := f.makeApp("acme")
	if a.URL != f.gh.URL+"/github-apps/bothy-acme" || a.InstallURL != f.gh.URL+"/github-apps/bothy-acme/installations/new" ||
		a.SettingsURL != f.gh.URL+"/organizations/acme/settings/apps/bothy-acme" {
		t.Errorf("app links = %q %q %q", a.URL, a.InstallURL, a.SettingsURL)
	}
	if _, err := f.svc.InstallApp(f.ctx, a.ID, f.installOnGitHub(a), "usr_owner"); err != nil {
		t.Fatal(err)
	}
	apps, _ := f.svc.Apps(f.ctx)
	if len(apps) != 1 || apps[0].InstallURL != a.InstallURL || apps[0].State != "connected" {
		t.Errorf("apps = %+v", apps)
	}
}

// A locked vault can't keep the key, so the code is never spent.
func TestCreateAppSealed(t *testing.T) {
	f := newFx(t)
	tgt := core.GitHubAppTarget{Org: "acme"}
	h, _ := f.svc.AppManifest(tgt, bothyOrigin)
	code := f.postManifest(h, "s1")
	f.vault.Seal()
	if _, err := f.svc.CreateApp(f.ctx, code, tgt, "", "usr_owner"); !errors.Is(err, core.ErrSealed) {
		t.Fatalf("CreateApp with a sealed vault = %v", err)
	}
	if f.gh.Conversions() != 0 || f.gh.Count(http.MethodPost, "/app-manifests/") != 0 {
		t.Fatal("the code was spent while the vault was sealed")
	}
	if err := f.vault.Unseal(f.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateApp(f.ctx, code, tgt, "", "usr_owner"); err != nil {
		t.Fatalf("after unsealing: %v", err)
	}
}

func testPEM(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func TestCreateAppFailures(t *testing.T) {
	good := testPEM(t)
	answer := func(mut func(map[string]any)) string {
		j := map[string]any{"id": 4242, "slug": "bothy-acme", "name": "Bothy (acme)", "owner": map[string]any{"login": "acme", "id": 77, "type": "Organization"},
			"html_url":  "{web}/apps/bothy-acme",
			"client_id": "Iv23liAbc", "client_secret": "s3cr3t-s3cr3t-s3cr3t", "pem": good, "permissions": map[string]string{"metadata": "read"}}
		mut(j)
		b, _ := json.Marshal(j)
		return string(b)
	}
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"not found":   {http.StatusNotFound, ""},
		"server down": {http.StatusInternalServerError, ""},
		"bad key": {http.StatusCreated, answer(func(j map[string]any) {
			j["pem"] = "-----BEGIN RSA PRIVATE KEY-----\nnope\n-----END RSA PRIVATE KEY-----\n"
		})},
		"bad slug":                 {http.StatusCreated, answer(func(j map[string]any) { j["slug"] = "../evil" })},
		"enterprise":               {http.StatusCreated, answer(func(j map[string]any) { j["owner"] = map[string]any{"login": "acme", "id": 77, "type": "Enterprise"} })},
		"no owner id":              {http.StatusCreated, answer(func(j map[string]any) { j["owner"] = map[string]any{"login": "acme", "type": "Organization"} })},
		"no id":                    {http.StatusCreated, answer(func(j map[string]any) { delete(j, "id") })},
		"no html_url":              {http.StatusCreated, answer(func(j map[string]any) { delete(j, "html_url") })},
		"html_url on another host": {http.StatusCreated, answer(func(j map[string]any) { j["html_url"] = "https://evil.example/apps/bothy-acme" })},
		"html_url of another app":  {http.StatusCreated, answer(func(j map[string]any) { j["html_url"] = "{web}/apps/other" })},
		"html_url with a query":    {http.StatusCreated, answer(func(j map[string]any) { j["html_url"] = "{web}/apps/bothy-acme?next=evil" })},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			const code = "0123456789abcdef0123456789abcdef01234567"
			f.gh.Fail(http.MethodPost, "^/app-manifests/", tc.status, nil, strings.ReplaceAll(tc.body, "{web}", f.gh.URL), 1)
			if _, err := f.svc.CreateApp(f.ctx, code, core.GitHubAppTarget{Org: "acme"}, "", "usr_owner"); err == nil || strings.Contains(err.Error(), code) {
				t.Fatalf("CreateApp = %v", err)
			}
			if apps, _ := f.store.Apps(f.ctx); len(apps) != 0 || len(f.secretNames()) != 0 {
				t.Errorf("stored %d apps, vault %v", len(apps), f.secretNames())
			}
			rows := f.audits("github.app_create")
			if len(rows) != 1 || rows[0].Outcome != "error" || strings.Contains(string(rows[0].Detail), code) ||
				strings.Contains(string(rows[0].Detail), "s3cr3t") {
				t.Errorf("audit = %+v", rows)
			}
		})
	}
	f := newFx(t)
	if _, err := f.svc.CreateApp(f.ctx, "../../user", core.GitHubAppTarget{}, "", "usr_owner"); !errors.Is(err, core.ErrInvalid) || f.gh.Count(http.MethodPost, "/") != 0 {
		t.Errorf("malformed code = %v", err)
	}
}

// The name and client id drive nothing, so GitHub surprising Bothy with
// them never costs the key, which it hands over only once.
func TestCreateAppTidiesNameAndClientID(t *testing.T) {
	cjk := strings.Repeat("機", 34)
	long := strings.Repeat("n", 150)
	for name, tc := range map[string]struct{ name, clientID, wantName, wantClientID string }{
		"34 wide characters":     {cjk, "Iv23liAbc", cjk, "Iv23liAbc"},
		"a name over 100":        {long, "Iv23liAbc", long[:100], "Iv23liAbc"},
		"a name on two lines":    {"Bothy\n(acme)\t", "Iv23liAbc", "Bothy (acme)", "Iv23liAbc"},
		"no name":                {"", "Iv23liAbc", "bothy-acme", "Iv23liAbc"},
		"a new client id format": {"Bothy (acme)", "Ov42.x_y-z", "Bothy (acme)", "Ov42.x_y-z"},
		"a client id with space": {"Bothy (acme)", "nope nope", "Bothy (acme)", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			body, _ := json.Marshal(map[string]any{"id": 4242, "slug": "bothy-acme", "name": tc.name, "owner": map[string]any{"login": "acme", "id": 77, "type": "Organization"},
				"html_url": f.gh.URL + "/apps/bothy-acme", "client_id": tc.clientID, "client_secret": "s3cr3t", "pem": testPEM(t)})
			f.gh.Fail(http.MethodPost, "^/app-manifests/", http.StatusCreated, nil, string(body), 1)
			a, err := f.svc.CreateApp(f.ctx, "0123456789abcdef0123456789abcdef01234567", core.GitHubAppTarget{Org: "acme"}, "", "usr_owner")
			if err != nil {
				t.Fatal(err)
			}
			if a.Name != tc.wantName || a.ClientID != tc.wantClientID || len(f.secretNames()) != 2 {
				t.Errorf("app = %q %q, vault %v", a.Name, a.ClientID, f.secretNames())
			}
			if stored, err := f.store.App(f.ctx, a.ID); err != nil || stored.Name != tc.wantName {
				t.Errorf("stored = %+v %v", stored, err)
			}
		})
	}
}

func TestInstallApp(t *testing.T) {
	f := newFx(t)
	f.gh.AddRepo("acme", "web", true, map[string]string{"index.html": "<h1>hi</h1>\n"})
	a := f.makeApp("acme")

	// An installation of another app is not this app's, whatever the URL says.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.gh.App = &githubtest.App{ID: 1, Slug: "other", Key: &key.PublicKey}
	other := f.gh.Install(1, githubtest.Installation{Account: "acme", Type: "Organization"})
	if _, err := f.svc.InstallApp(f.ctx, a.ID, other.ID, "usr_owner"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("another app's installation = %v", err)
	}
	// Nor is one of this app on another account than its owner.
	foreign := f.gh.Install(a.AppID, githubtest.Installation{Account: "someone-else", Type: "User"})
	if _, err := f.svc.InstallApp(f.ctx, a.ID, foreign.ID, "usr_owner"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("an installation on another account = %v", err)
	}
	if conns, _ := f.store.Connections(f.ctx); len(conns) != 0 || len(f.secretNames()) != 2 {
		t.Fatalf("after a foreign installation: %d connections, vault %v", len(conns), f.secretNames())
	}

	id := f.installOnGitHub(a)
	conn, err := f.svc.InstallApp(f.ctx, a.ID, id, "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Auth != core.GitHubApp || conn.AppID != strconv.FormatInt(a.AppID, 10) || conn.InstallationID != id || conn.SecretRef != a.KeyRef ||
		!conn.OwnsSecret || conn.AppSlug != a.Slug || conn.Account != "acme" || conn.AccountType != "Organization" {
		t.Errorf("connection = %+v", conn)
	}
	if got, err := f.svc.ConnectionApp(f.ctx, conn.ID); err != nil || got.ID != a.ID || got.State != "connected" {
		t.Errorf("ConnectionApp = %+v %v", got, err)
	}
	if again, err := f.svc.InstallApp(f.ctx, a.ID, id, "usr_owner"); err != nil || again.ID != conn.ID {
		t.Errorf("second install = %+v %v", again, err)
	}
	var wg sync.WaitGroup
	got := make([]string, 10)
	for i := range got {
		wg.Go(func() {
			if c, err := f.svc.InstallApp(f.ctx, a.ID, id, "usr_owner"); err == nil {
				got[i] = c.ID
			}
		})
	}
	wg.Wait()
	if conns, _ := f.store.Connections(f.ctx); len(conns) != 1 || slices.ContainsFunc(got, func(s string) bool { return s != conn.ID }) {
		t.Errorf("concurrent installs: %d connections, ids %v", len(conns), got)
	}
	rows := f.audits("github.connect")
	if len(rows) != 1 || !strings.Contains(string(rows[0].Detail), `"method":"app_manifest"`) || !strings.Contains(string(rows[0].Detail), `"app":"bothy-acme"`) {
		t.Errorf("connect audit = %+v", rows)
	}

	// Installed again on GitHub: the connection moves to the new
	// installation, once GitHub confirms it is the app's.
	if _, err := f.svc.InstallApp(f.ctx, a.ID, other.ID, "usr_owner"); err == nil {
		t.Error("moved the connection to another app's installation")
	}
	if _, err := f.svc.InstallApp(f.ctx, a.ID, foreign.ID, "usr_owner"); err == nil {
		t.Error("moved the connection to an installation on another account")
	}
	if c, _ := f.store.Connection(f.ctx, conn.ID); c.InstallationID != id {
		t.Errorf("the connection moved to installation %d", c.InstallationID)
	}
	id2 := f.installOnGitHub(a)
	moved, err := f.svc.InstallApp(f.ctx, a.ID, id2, "usr_owner")
	if err != nil || moved.ID != conn.ID || moved.InstallationID != id2 {
		t.Fatalf("reinstall = %+v %v", moved, err)
	}
	if rows := f.audits("github.connect"); len(rows) != 2 || !strings.Contains(string(rows[1].Detail), `"reinstalled":true`) {
		t.Errorf("reinstall audit = %+v", rows)
	}
	if _, err := f.svc.SetRepos(f.ctx, conn.ID, []string{"acme/web"}, "usr_owner"); err != nil {
		t.Fatalf("repositories through the new installation: %v", err)
	}
}

// A new app replacing a connection takes over the repositories its
// installation reaches, then the old connection goes.
func TestAppReplacesConnection(t *testing.T) {
	f := newFx(t)
	f.gh.AddRepo("octocat", "private2", true, map[string]string{"x": "y"})
	pat := f.connect("octocat/Hello-World", "octocat/private2")
	f.gh.InstallRepos = []string{"octocat/Hello-World"}
	a := f.makeAppReplacing("", pat.ID)
	if a.Replaces != pat.ID || a.Owner != "octocat" || a.OwnerType != "User" || a.SettingsURL != f.gh.URL+"/settings/apps/"+a.Slug {
		t.Fatalf("app = %+v", a)
	}
	conn, err := f.svc.InstallApp(f.ctx, a.ID, f.installOnGitHub(a), "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Connection(f.ctx, pat.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("the replaced connection: %v", err)
	}
	if slices.Contains(f.secretNames(), "github_"+pat.ID) {
		t.Error("the replaced connection's token is still stored")
	}
	repos, _ := f.store.Repos(f.ctx, conn.ID)
	if len(repos) != 1 || repos[0].FullName() != "octocat/Hello-World" || !repos[0].Allowed {
		t.Errorf("repos moved = %+v", repos)
	}
	if got, _ := f.store.App(f.ctx, a.ID); got.Replaces != "" {
		t.Errorf("swap not recorded as done: %+v", got)
	}
	if d := f.audits("github.disconnect"); len(d) != 1 || d[0].Target != pat.ID {
		t.Errorf("disconnect audit = %+v", d)
	}
	if r := f.audits("github.repos"); len(r) != 1 || r[0].Target != conn.ID || !strings.Contains(string(r[0].Detail), `"added":["octocat/Hello-World"]`) {
		t.Errorf("repos audit = %+v", r)
	}

	// Replacing a Bothy-made connection retires its app.
	b := f.makeAppReplacing("", conn.ID)
	next, err := f.svc.InstallApp(f.ctx, b.ID, f.installOnGitHub(b), "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := f.store.App(f.ctx, a.ID)
	if old.RetiredAt.IsZero() || old.KeyRef != "" || old.Connection != "" {
		t.Errorf("the replaced app = %+v", old)
	}
	if got := f.secretNames(); !slices.Equal(got, []string{"github_" + b.ID + "_client_secret", "github_" + b.ID + "_key"}) {
		t.Errorf("vault = %v", got)
	}
	if repos, _ := f.store.Repos(f.ctx, next.ID); len(repos) != 1 || repos[0].Name != "Hello-World" {
		t.Errorf("repos after the second swap = %+v", repos)
	}
}

// flakyStore fails deleting one connection while failDelete names it.
type flakyStore struct {
	core.GitHubStore
	failDelete string
}

func (s *flakyStore) DeleteConnection(ctx context.Context, id string) error {
	if id == s.failDelete {
		return errors.New("store: database is locked")
	}
	return s.GitHubStore.DeleteConnection(ctx, id)
}

// A swap that fails leaves the replaced connection in use and the app
// still replacing it, so the next check finishes the swap.
func TestAppSwapRetries(t *testing.T) {
	f := newFx(t)
	pat := f.connect("octocat/Hello-World")
	f.gh.InstallRepos = []string{"octocat/Hello-World"}
	flaky := &flakyStore{GitHubStore: f.store, failDelete: pat.ID}
	f.store = flaky
	f.logTo()
	a := f.makeAppReplacing("", pat.ID)
	conn, err := f.svc.InstallApp(f.ctx, a.ID, f.installOnGitHub(a), "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Connection(f.ctx, pat.ID); err != nil {
		t.Fatalf("the replaced connection after a failed swap: %v", err)
	}
	if got, _ := f.store.App(f.ctx, a.ID); got.Replaces != pat.ID || got.Connection != conn.ID {
		t.Fatalf("app after a failed swap = %+v", got)
	}
	if rows := f.audits("github.app_replace"); len(rows) != 1 || rows[0].Outcome != "error" || rows[0].Target != a.ID ||
		!strings.Contains(string(rows[0].Detail), `"replaces":"`+pat.ID+`"`) {
		t.Errorf("app_replace audit = %+v", rows)
	}

	flaky.failDelete = ""
	if c, err := f.svc.CheckApp(f.ctx, a.ID, "usr_owner"); err != nil || c == nil || c.ID != conn.ID {
		t.Fatalf("check = %+v %v", c, err)
	}
	if _, err := f.store.Connection(f.ctx, pat.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("the replaced connection after the retry: %v", err)
	}
	if repos, _ := f.store.Repos(f.ctx, conn.ID); len(repos) != 1 || repos[0].FullName() != "octocat/Hello-World" || !repos[0].Allowed {
		t.Errorf("repos moved = %+v", repos)
	}
	if got, _ := f.store.App(f.ctx, a.ID); got.Replaces != "" {
		t.Errorf("swap not recorded as done: %+v", got)
	}
}

func TestCheckApp(t *testing.T) {
	f := newFx(t)
	a := f.makeApp("acme")
	if c, err := f.svc.CheckApp(f.ctx, a.ID, "usr_owner"); c != nil || err != nil {
		t.Fatalf("not installed = %+v %v", c, err)
	}
	// Installed from GitHub's own pages: one on another account doesn't
	// count, even alone; the owner's does.
	f.gh.Install(a.AppID, githubtest.Installation{Account: "someone-else", Type: "User"})
	if c, err := f.svc.CheckApp(f.ctx, a.ID, "usr_owner"); c != nil || err != nil {
		t.Fatalf("installed only on another account = %+v %v", c, err)
	}
	mine := f.gh.Install(a.AppID, githubtest.Installation{Account: "ACME", Type: "Organization"})
	c, err := f.svc.CheckApp(f.ctx, a.ID, "usr_owner")
	if err != nil || c == nil || c.InstallationID != mine.ID {
		t.Fatalf("installed = %+v %v", c, err)
	}

	// A renamed organization keeps its account id.
	b := f.makeApp("octo-org")
	renamed := f.gh.Install(b.AppID, githubtest.Installation{Account: "octo-org-renamed", AccountID: githubtest.AccountID("octo-org"), Type: "Organization"})
	if c, err := f.svc.CheckApp(f.ctx, b.ID, "usr_owner"); err != nil || c == nil || c.InstallationID != renamed.ID {
		t.Fatalf("the renamed owner's installation = %+v %v", c, err)
	}

	r := f.makeApp("acme")
	if _, err := f.svc.RetireApp(f.ctx, r.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CheckApp(f.ctx, r.ID, "usr_owner"); !errors.Is(err, core.ErrConflict) {
		t.Errorf("checking a retired app = %v", err)
	}
	if _, err := f.svc.InstallApp(f.ctx, r.ID, 99, "usr_owner"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("installing a retired app = %v", err)
	}
}

func TestRetireDeleteAndDisconnectApp(t *testing.T) {
	f := newFx(t)
	a := f.makeApp("acme")
	if err := f.svc.DeleteApp(f.ctx, a.ID, "usr_owner"); !errors.Is(err, core.ErrConflict) {
		t.Errorf("dismissing a pending app = %v", err)
	}
	conn, err := f.svc.InstallApp(f.ctx, a.ID, f.installOnGitHub(a), "usr_owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RetireApp(f.ctx, a.ID, "usr_owner"); !errors.Is(err, core.ErrConflict) {
		t.Errorf("retiring a connected app = %v", err)
	}
	if err := f.svc.Disconnect(f.ctx, conn.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.App(f.ctx, a.ID)
	if err != nil || got.RetiredAt.IsZero() || got.KeyRef != "" || got.ClientSecretRef != "" || got.Connection != "" {
		t.Fatalf("after disconnect = %+v %v", got, err)
	}
	if len(f.secretNames()) != 0 {
		t.Errorf("vault after disconnect = %v", f.secretNames())
	}
	if d := f.audits("github.disconnect"); len(d) != 1 || !strings.Contains(string(d[0].Detail), `"app":"bothy-acme"`) {
		t.Errorf("disconnect audit = %+v", d)
	}
	apps, _ := f.svc.Apps(f.ctx)
	if len(apps) != 1 || apps[0].State != "retired" || apps[0].SettingsURL != f.gh.URL+"/organizations/acme/settings/apps/bothy-acme" {
		t.Errorf("apps = %+v", apps)
	}
	if err := f.svc.DeleteApp(f.ctx, a.ID, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.App(f.ctx, a.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("dismissed app: %v", err)
	}

	b := f.makeApp("acme")
	r, err := f.svc.RetireApp(f.ctx, b.ID, "usr_owner")
	if err != nil || r.State != "retired" || r.KeyRef != "" || len(f.secretNames()) != 0 {
		t.Fatalf("retired = %+v %v, vault %v", r, err, f.secretNames())
	}
	if again, err := f.svc.RetireApp(f.ctx, b.ID, "usr_owner"); err != nil || again.State != "retired" {
		t.Errorf("retiring twice = %+v %v", again, err)
	}
	if rows := f.audits("github.app_retire"); len(rows) != 1 || rows[0].Target != b.ID {
		t.Errorf("retire audit = %+v", rows)
	}
	// Bothy never asks GitHub to delete an app.
	for _, req := range f.gh.Requests() {
		if req.Method == http.MethodDelete && strings.HasPrefix(req.Path, "/app") {
			t.Errorf("deleted on GitHub: %s", req.Path)
		}
	}
}
