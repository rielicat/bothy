//go:build e2e

package e2e

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/github/githubtest"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

var (
	e2eFormAction   = regexp.MustCompile(`<form id="f" method="post" action="([^"]*)">`)
	e2eFormManifest = regexp.MustCompile(`<input type="hidden" name="manifest" value="([^"]*)">`)
)

func ghLocation(t *testing.T, resp *Response, status int) *url.URL {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %s, want %d: %s", resp.Status, status, resp.Body)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestGitHubAppManifest is the "Bothy creates its own GitHub App" scenario
// against the fake GitHub: the admin starts it, the handoff page's form goes
// to GitHub's new-app page, GitHub's redirect back redeems the code, the
// install page sends the browser to the setup URL, and the installation
// becomes an App connection. The agent then opens a pull request as the
// app; disconnecting retires the app and deletes its keys. The key, client
// secret and code reach neither the logs nor the audit log.
func TestGitHubAppManifest(t *testing.T) {
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	gh.AddRepo("acme", "web", true, map[string]string{"README.md": "# Web\n"})
	gh.AddRepo("acme", "api", true, map[string]string{"main.go": "package main\n"})

	const readme = "# Web\n\nThe customer web app.\n"
	llm := pltLLM(t,
		mockllm.Rule{MatchSystem: `^You keep short project notes`, Text: []string{"## Summary\nThe web app.\n"}},
		mockllm.Rule{MatchUser: `APP-PR`, MatchToolResult: `Opened pull request`, Text: []string{"The pull request is open."}},
		mockllm.Rule{MatchUser: `APP-PR`, MatchToolResult: `Wrote \d+ bytes`, ToolCalls: []core.ToolCall{pltCall("call_pr", "use_tool",
			`{"name":"repo_open_pr","args":{"repo":"acme/web","title":"Describe the app"}}`)}},
		mockllm.Rule{MatchUser: `APP-PR`, MatchToolResult: `Checked out`, ToolCalls: []core.ToolCall{pltCall("call_w", "fs_write",
			`{"path":"repos/acme/web/README.md","content":"`+strings.ReplaceAll(readme, "\n", `\n`)+`"}`)}},
		mockllm.Rule{MatchUser: `APP-PR`, ToolCalls: []core.ToolCall{pltCall("call_co", "use_tool", `{"name":"repo_checkout","args":{"repo":"acme/web"}}`)}},
	)
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, "github:\n  api_base: "+gh.URL+"\n")})
	owner := pltOwner(t, g)
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var start dto.GitHubAppStartResp
	if code := owner.JSON(t, http.MethodPost, "/api/github/apps", dto.GitHubAppStartReq{Account: "organization", Org: "acme"}, &start); code != http.StatusOK ||
		start.HandoffURL != "/api/github/apps/handoff" {
		t.Fatalf("start = %d %+v", code, start)
	}
	page := owner.Do(t, http.MethodGet, start.HandoffURL, nil, nil)
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Header.Get("Content-Security-Policy"), "form-action "+gh.URL+";") {
		t.Fatalf("handoff = %s %v", page.Status, page.Header)
	}
	action := e2eFormAction.FindSubmatch(page.Body)
	field := e2eFormManifest.FindSubmatch(page.Body)
	if action == nil || field == nil {
		t.Fatalf("handoff page:\n%s", page.Body)
	}
	var manifest struct {
		Name        string            `json:"name"`
		RedirectURL string            `json:"redirect_url"`
		SetupURL    string            `json:"setup_url"`
		Permissions map[string]string `json:"default_permissions"`
	}
	raw := html.UnescapeString(string(field[1]))
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RedirectURL != g.BaseURL+"/api/github/apps/created" || manifest.SetupURL != g.BaseURL+"/api/github/apps/installed" ||
		manifest.Name != "Bothy (acme)" || len(manifest.Permissions) != 5 || manifest.Permissions["contents"] != "write" || manifest.Permissions["metadata"] != "read" {
		t.Fatalf("manifest = %+v", manifest)
	}

	// The browser posts the form to GitHub, which creates the app.
	resp, err := plain.PostForm(html.UnescapeString(string(action[1])), url.Values{"manifest": {raw}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	created, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || !strings.HasPrefix(created.String(), g.BaseURL+"/api/github/apps/created?") {
		t.Fatalf("GitHub's new-app page = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	code := created.Query().Get("code")

	// The same redirect from another browser is refused, and the code stays
	// unspent.
	if land := ghLocation(t, pltNewClient(g).Do(t, http.MethodGet, created.RequestURI(), nil, nil), http.StatusSeeOther); land.Query().Get("github_app") != "not_finished" ||
		gh.Conversions() != 0 {
		t.Fatalf("another browser = %q, %d conversions", land, gh.Conversions())
	}

	install := ghLocation(t, owner.Do(t, http.MethodGet, created.RequestURI(), nil, nil), http.StatusSeeOther)
	if gh.Conversions() != 1 || !strings.HasPrefix(install.String(), gh.URL+"/apps/bothy-acme/installations/new?state=") || strings.Contains(install.String(), code) {
		t.Fatalf("created sent the browser to %q (%d conversions)", install, gh.Conversions())
	}
	resp, err = plain.Get(install.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	setup, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(setup.String(), g.BaseURL+"/api/github/apps/installed?") {
		t.Fatalf("GitHub's install page = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if land := ghLocation(t, owner.Do(t, http.MethodGet, setup.RequestURI(), nil, nil), http.StatusSeeOther); land.Path != "/settings/connections/github" ||
		land.Query().Get("github_app") != "installed" {
		t.Fatalf("installed landed on %q", land)
	}

	var conns []core.GitHubConnection
	if code := owner.JSON(t, http.MethodGet, "/api/github/connections", nil, &conns); code != http.StatusOK || len(conns) != 1 ||
		conns[0].Auth != core.GitHubApp || conns[0].AppSlug != "bothy-acme" || conns[0].Account != "acme" || !conns[0].OwnsSecret {
		t.Fatalf("connections = %d %+v", code, conns)
	}
	conn := conns[0]
	var detail dto.GitHubConnectionDetail
	if code := owner.JSON(t, http.MethodGet, "/api/github/connections/"+conn.ID, nil, &detail); code != http.StatusOK || detail.App == nil ||
		detail.App.State != "connected" || detail.App.Slug != "bothy-acme" {
		t.Fatalf("detail = %d %+v", code, detail.App)
	}
	app := *detail.App
	if code := owner.JSON(t, http.MethodPut, "/api/github/connections/"+conn.ID+"/repos", dto.GitHubReposReq{Repos: []string{"acme/web"}}, nil); code != http.StatusOK {
		t.Fatalf("set repos = %d", code)
	}

	// The agent works on GitHub as the app.
	sid := owner.NewSession(t).ID
	run := owner.Send(t, sid, "APP-PR describe acme/web in its README")
	ap := owner.WaitApproval(t, run, core.ApprovalPending)
	if ap.Action == nil || ap.Action.Tool != "repo_open_pr" {
		t.Fatalf("approval = %+v", ap.Action)
	}
	owner.Resolve(t, ap.ID, core.AllowOnce)
	if d := owner.Finished(t, run); d.Run.Status != core.RunDone {
		t.Fatalf("run = %+v %+v", d.Run, d.ToolCalls)
	}
	if pulls := gh.Repo("acme/web").Pulls(); len(pulls) != 1 || pulls[0].Title != "Describe the app" {
		t.Fatalf("pulls = %+v", pulls)
	}
	for _, r := range gh.Requests() {
		if strings.HasPrefix(r.Path, "/repos/acme/web/") && r.Auth != "installation" {
			t.Errorf("%s %s sent as %s", r.Method, r.Path, r.Auth)
		}
	}

	var secrets []core.SecretMeta
	if code := owner.JSON(t, http.MethodGet, "/api/vault?scope=global", nil, &secrets); code != http.StatusOK {
		t.Fatalf("vault = %d", code)
	}
	var names []string
	for _, s := range secrets {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"github_" + app.ID + "_client_secret", "github_" + app.ID + "_key"}) {
		t.Fatalf("vault = %v", names)
	}
	made := gh.Apps()[0]
	audit, _ := json.Marshal(owner.Audit(t, "github."))
	for name, secret := range map[string]string{"code": code, "key": strings.Split(string(made.PEM), "\n")[1], "client secret": made.ClientSecret} {
		if strings.Contains(g.Logs(), secret) {
			t.Errorf("the %s is in the gateway logs", name)
		}
		if strings.Contains(string(audit), secret) {
			t.Errorf("the %s is in the audit log", name)
		}
	}
	owner.WaitAudit(t, "github.app_create", func(e core.AuditEntry) bool { return e.Target == app.ID && e.Outcome == "ok" })
	owner.WaitAudit(t, "github.connect", func(e core.AuditEntry) bool {
		return e.Target == conn.ID && strings.Contains(string(e.Detail), `"method":"app_manifest"`)
	})

	if code := owner.JSON(t, http.MethodDelete, "/api/github/connections/"+conn.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("disconnect = %d", code)
	}
	var apps []core.GitHubOwnApp
	if code := owner.JSON(t, http.MethodGet, "/api/github/apps", nil, &apps); code != http.StatusOK || len(apps) != 1 || apps[0].State != "retired" ||
		apps[0].SettingsURL != gh.URL+"/organizations/acme/settings/apps/bothy-acme" {
		t.Fatalf("apps after disconnect = %d %+v", code, apps)
	}
	secrets = nil
	if code := owner.JSON(t, http.MethodGet, "/api/vault?scope=global", nil, &secrets); code != http.StatusOK || len(secrets) != 0 {
		t.Errorf("vault after disconnect = %d %+v", code, secrets)
	}
}
