package githubtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"html"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The browser pages of GitHub's App Manifest flow
// (docs.github.com "Registering a GitHub App from a manifest"): the
// settings page the manifest is posted to, which creates the app and sends
// the browser to its redirect_url with a one-hour code, and the install
// page, which sends it to the app's setup_url. The fake serves them on its
// API origin, so the web origin a client derives from a loopback api_base
// is the fake itself.

// knownPermissions are the permission names GitHub accepts in a manifest's
// default_permissions.
var knownPermissions = []string{
	"actions", "administration", "checks", "codespaces", "contents", "dependabot_secrets", "deployments", "environments",
	"issues", "merge_queues", "metadata", "packages", "pages", "pull_requests", "repository_custom_properties",
	"repository_hooks", "repository_projects", "secret_scanning_alerts", "secrets", "security_events", "single_file",
	"statuses", "vulnerability_alerts", "workflows", "members", "organization_administration", "organization_hooks",
	"organization_plan", "organization_projects", "organization_secrets", "organization_self_hosted_runners",
	"organization_user_blocking", "team_discussions", "email_addresses", "followers", "gpg_keys", "keys", "starring",
}

const manifestCodeTTL = time.Hour

// web serves the browser pages; false leaves the request to the API.
func (c *reqCtx) web() bool {
	seg, m := c.seg, c.r.Method
	switch {
	case m == http.MethodPost && len(seg) == 3 && seg[0] == "settings" && seg[1] == "apps" && seg[2] == "new":
		c.auth = "web"
		c.s.record(c)
		c.newApp("", "User")
	case m == http.MethodPost && len(seg) == 5 && seg[0] == "organizations" && seg[2] == "settings" && seg[3] == "apps" && seg[4] == "new":
		c.auth = "web"
		c.s.record(c)
		c.newApp(seg[1], "Organization")
	case m == http.MethodGet && len(seg) == 4 && seg[0] == "apps" && seg[2] == "installations" && seg[3] == "new":
		c.auth = "web"
		c.s.record(c)
		c.installPage(seg[1])
	default:
		return false
	}
	return true
}

func (c *reqCtx) page(status int, title, text string) {
	c.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.w.WriteHeader(status)
	_, _ = io.WriteString(c.w, "<!doctype html><title>"+html.EscapeString(title)+"</title><p>"+html.EscapeString(text)+"</p>\n")
}

type manifestDoc struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	RedirectURL        string            `json:"redirect_url"`
	SetupURL           string            `json:"setup_url"`
	HookAttributes     map[string]any    `json:"hook_attributes"`
	DefaultEvents      []string          `json:"default_events"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

// newApp is the "Create GitHub App" page with the manifest already
// confirmed: it creates the app at once.
func (c *reqCtx) newApp(org, ownerType string) {
	form, err := url.ParseQuery(string(c.body))
	raw := form.Get("manifest")
	var doc manifestDoc
	var generic map[string]any
	if err != nil || raw == "" || json.Unmarshal([]byte(raw), &doc) != nil || json.Unmarshal([]byte(raw), &generic) != nil {
		c.page(http.StatusUnprocessableEntity, "Invalid manifest", "The manifest is missing or is not JSON.")
		return
	}
	switch {
	case doc.URL == "" || doc.RedirectURL == "":
		c.page(http.StatusUnprocessableEntity, "Invalid manifest", "url and redirect_url are required.")
		return
	case doc.Name == "" || utf8.RuneCountInString(doc.Name) > 34:
		c.page(http.StatusUnprocessableEntity, "Invalid manifest", "Name is too long (maximum is 34 characters).")
		return
	case len(doc.DefaultEvents) > 0 && doc.HookAttributes == nil:
		c.page(http.StatusUnprocessableEntity, "Invalid manifest", "Default events need a webhook.")
		return
	}
	for k, v := range doc.DefaultPermissions {
		if !slices.Contains(knownPermissions, k) || v != "read" && v != "write" {
			c.page(http.StatusUnprocessableEntity, "Invalid manifest", "Unknown permission "+k+": "+v+".")
			return
		}
	}
	owner := org
	if owner == "" {
		owner = c.s.User
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		c.page(http.StatusInternalServerError, "Error", err.Error())
		return
	}
	c.s.nextID++
	a := &App{ID: c.s.nextID, Slug: c.s.uniqueSlug(doc.Name), Key: &key.PublicKey, Name: doc.Name, Owner: owner, OwnerType: ownerType,
		ClientID: "Iv23li" + randString(10), ClientSecret: randHex(20),
		PEM:         pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		Permissions: doc.DefaultPermissions, RedirectURL: doc.RedirectURL, SetupURL: doc.SetupURL}
	if doc.HookAttributes != nil {
		a.WebhookSecret = randHex(20)
	}
	c.s.made = append(c.s.made, a)
	state := c.r.URL.Query().Get("state")
	c.s.manifests = append(c.s.manifests, Manifest{Owner: owner, OwnerType: ownerType, State: state, Raw: raw, JSON: generic})
	code := randHex(20)
	c.s.codes[code] = &manifestCode{app: a, expires: time.Now().Add(manifestCodeTTL)}
	c.redirect(doc.RedirectURL, url.Values{"code": {code}, "state": {state}})
}

func (c *reqCtx) redirect(to string, q url.Values) {
	u, err := url.Parse(to)
	if err != nil {
		c.page(http.StatusUnprocessableEntity, "Invalid URL", to)
		return
	}
	vals := u.Query()
	for k, vs := range q {
		if len(vs) > 0 && vs[0] != "" {
			vals[k] = vs
		}
	}
	u.RawQuery = vals.Encode()
	c.w.Header().Set("Location", u.String())
	c.w.WriteHeader(http.StatusFound)
}

func (s *Server) uniqueSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.TrimRight(b.String(), "-")
	if base == "" {
		base = "app"
	}
	slug := base
	for n := 2; slices.ContainsFunc(s.made, func(a *App) bool { return a.Slug == slug }) || s.App != nil && s.App.Slug == slug; n++ {
		slug = base + "-" + strconv.Itoa(n)
	}
	return slug
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// convert is POST /app-manifests/{code}/conversions: no authentication,
// each code redeemable once within an hour.
func (c *reqCtx) convert(code string) {
	mc := c.s.codes[code]
	if mc == nil || mc.used || time.Now().After(mc.expires) {
		c.error(http.StatusNotFound, "Not Found")
		return
	}
	mc.used = true
	c.s.conversions++
	a := mc.app
	j := obj(fixture("integration"))
	j["id"], j["slug"], j["name"], j["client_id"] = a.ID, a.Slug, a.Name, a.ClientID
	j["html_url"] = c.s.URL + "/apps/" + a.Slug
	j["external_url"] = ""
	j["description"] = ""
	ow := obj(j["owner"])
	ow["login"], ow["type"] = a.Owner, a.OwnerType
	perms := map[string]string{}
	for k, v := range a.Permissions {
		perms[k] = v
	}
	j["permissions"], j["events"] = perms, []string{}
	j["client_secret"], j["pem"] = a.ClientSecret, string(a.PEM)
	j["webhook_secret"] = nil
	if a.WebhookSecret != "" {
		j["webhook_secret"] = a.WebhookSecret
	}
	c.json(http.StatusCreated, j)
}

// Install adds an installation to App or an app made from a manifest, as
// installing it from GitHub's own pages does; a zero ID gets the next one.
func (s *Server) Install(appID int64, in Installation) Installation {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.appByID(appID)
	if a == nil {
		panic("githubtest: no app " + strconv.FormatInt(appID, 10))
	}
	if in.ID == 0 {
		s.nextID++
		in.ID = s.nextID
	}
	a.Installations = append(a.Installations, in)
	return in
}

// installPage installs an app made from a manifest on its owner, or asks
// for approval, and sends the browser to the app's setup URL with the state
// it was given.
func (c *reqCtx) installPage(slug string) {
	i := slices.IndexFunc(c.s.made, func(a *App) bool { return a.Slug == slug })
	if i < 0 {
		c.page(http.StatusNotFound, "Not Found", "No app called "+slug+".")
		return
	}
	a := c.s.made[i]
	state := c.r.URL.Query().Get("state")
	if c.s.InstallAction == "request" {
		if a.SetupURL == "" {
			c.page(http.StatusOK, "Requested", "An owner needs to approve "+a.Name+".")
			return
		}
		c.redirect(a.SetupURL, url.Values{"setup_action": {"request"}, "state": {state}})
		return
	}
	c.s.nextID++
	in := Installation{ID: c.s.nextID, Account: a.Owner, Type: a.OwnerType, Repos: slices.Clone(c.s.InstallRepos), Permissions: a.Permissions}
	a.Installations = append(a.Installations, in)
	if a.SetupURL == "" {
		c.page(http.StatusOK, "Installed", a.Name+" is installed.")
		return
	}
	c.redirect(a.SetupURL, url.Values{"installation_id": {strconv.FormatInt(in.ID, 10)}, "setup_action": {"install"}, "state": {state}})
}
