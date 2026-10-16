package githubtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func postForm(t *testing.T, u string, form url.Values) *http.Response {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.PostForm(u, form)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

const testManifest = `{"name":"Bothy (acme)","url":"http://127.0.0.1:8080","redirect_url":"http://127.0.0.1:8080/api/github/apps/created",` +
	`"setup_url":"http://127.0.0.1:8080/api/github/apps/installed","public":false,"default_events":[],` +
	`"default_permissions":{"contents":"write","metadata":"read"}}`

// The App Manifest flow's pages: the settings page creates the app and
// redirects with a single-use code, the conversion hands over the key once,
// and the install page sends the browser to the setup URL.
func TestFakeManifestFlow(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddRepo("acme", "web", true, map[string]string{"f": "x"})

	resp := postForm(t, s.URL+"/organizations/acme/settings/apps/new?state=s1", url.Values{"manifest": {testManifest}})
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || loc.Path != "/api/github/apps/created" || loc.Query().Get("state") != "s1" || loc.Query().Get("code") == "" {
		t.Fatalf("manifest page = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	ms := s.Manifests()
	if len(ms) != 1 || ms[0].Owner != "acme" || ms[0].OwnerType != "Organization" || ms[0].State != "s1" || ms[0].JSON["name"] != "Bothy (acme)" {
		t.Fatalf("manifests = %+v", ms)
	}
	code := loc.Query().Get("code")
	resp = postForm(t, s.URL+"/app-manifests/"+code+"/conversions", nil)
	var conv map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&conv); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("conversion = %d %v", resp.StatusCode, err)
	}
	owner, _ := conv["owner"].(map[string]any)
	pemText, _ := conv["pem"].(string)
	clientID, _ := conv["client_id"].(string)
	if conv["slug"] != "bothy-acme" || owner["login"] != "acme" || owner["type"] != "Organization" || !strings.HasPrefix(pemText, "-----BEGIN RSA PRIVATE KEY-----") ||
		conv["client_secret"] == "" || !strings.HasPrefix(clientID, "Iv23li") || conv["webhook_secret"] != nil {
		t.Fatalf("conversion body = %v", conv)
	}
	if perms, _ := conv["permissions"].(map[string]any); perms["contents"] != "write" || len(perms) != 2 {
		t.Errorf("permissions = %v", conv["permissions"])
	}
	if resp := postForm(t, s.URL+"/app-manifests/"+code+"/conversions", nil); resp.StatusCode != http.StatusNotFound || s.Conversions() != 1 {
		t.Fatalf("second conversion = %d, %d conversions", resp.StatusCode, s.Conversions())
	}

	resp = get(t, s, "/apps/bothy-acme/installations/new?state=s2", "")
	loc, _ = url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Path != "/api/github/apps/installed" || loc.Query().Get("setup_action") != "install" ||
		loc.Query().Get("state") != "s2" || loc.Query().Get("installation_id") == "" {
		t.Fatalf("install page = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	a := s.Apps()[0]
	if len(a.Installations) != 1 || a.Installations[0].Account != "acme" || a.Installations[0].Permissions["contents"] != "write" {
		t.Fatalf("installations = %+v", a.Installations)
	}
	s.InstallAction = "request"
	resp = get(t, s, "/apps/bothy-acme/installations/new?state=s3", "")
	loc, _ = url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("setup_action") != "request" || loc.Query().Has("installation_id") || loc.Query().Get("state") != "s3" {
		t.Fatalf("request = %q", resp.Header.Get("Location"))
	}

	// A personal account's app belongs to the PAT's user; slugs are unique.
	resp = postForm(t, s.URL+"/settings/apps/new?state=p", url.Values{"manifest": {testManifest}})
	if resp.StatusCode != http.StatusFound || s.Apps()[1].Owner != "octocat" || s.Apps()[1].OwnerType != "User" || s.Apps()[1].Slug != "bothy-acme-2" {
		t.Fatalf("personal app = %d %+v", resp.StatusCode, s.Apps()[1])
	}
	for _, bad := range []string{
		`{"name":"Bothy (acme)","url":"http://x","default_permissions":{"contents":"write"}}`,
		strings.Replace(testManifest, `"contents":"write"`, `"contents":"admin"`, 1),
		strings.Replace(testManifest, `"contents":"write"`, `"root":"write"`, 1),
		strings.Replace(testManifest, `"default_events":[]`, `"default_events":["push"]`, 1),
		strings.Replace(testManifest, `Bothy (acme)`, strings.Repeat("x", 35), 1),
	} {
		if resp := postForm(t, s.URL+"/settings/apps/new", url.Values{"manifest": {bad}}); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("manifest %s = %d", bad, resp.StatusCode)
		}
	}
	for _, r := range s.Requests() {
		if strings.Contains(r.Path, "settings/apps/new") && r.Auth != "web" {
			t.Errorf("web page recorded as %s", r.Auth)
		}
	}
}

// Installation tokens carry their installation's permissions; JWTs of any
// app the fake knows authenticate as that app.
func TestFakeInstallationPermissions(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddRepo("acme", "web", true, map[string]string{"f": "x"})
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s.App = &App{ID: 7, Slug: "app", Key: &key.PublicKey}
	in := s.Install(7, Installation{Account: "acme", Type: "Organization", Permissions: map[string]string{"metadata": "read", "issues": "read"}})
	tok := mintFor(t, s, key, 7, in.ID)
	if resp := get(t, s, "/repos/acme/web/issues", tok); resp.StatusCode != http.StatusOK {
		t.Errorf("issues with issues:read = %d", resp.StatusCode)
	}
	resp := get(t, s, "/repos/acme/web/pulls", tok)
	var e map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != http.StatusForbidden || e["message"] != "Resource not accessible by integration" {
		t.Errorf("pulls without pull_requests = %d %v", resp.StatusCode, e)
	}
	if resp := get(t, s, "/repos/acme/web/commits/main", tok); resp.StatusCode != http.StatusForbidden {
		t.Errorf("commits without contents = %d", resp.StatusCode)
	}

	// An app made from a manifest signs in with its own key.
	resp = postForm(t, s.URL+"/organizations/acme/settings/apps/new", url.Values{"manifest": {testManifest}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("manifest page = %d", resp.StatusCode)
	}
	made := s.Apps()[0]
	if made.ID == 7 || mintFor(t, s, mustKey(t, made.PEM), made.ID, s.Install(made.ID, Installation{Account: "acme", Type: "Organization"}).ID) == "" {
		t.Fatal("no token for the made app")
	}
}

func mustKey(t *testing.T, pemBytes []byte) *rsa.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("not PEM")
	}
	k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// mintFor signs an App JWT and mints an installation token with it.
func mintFor(t *testing.T, s *Server, key *rsa.PrivateKey, appID, install int64) string {
	t.Helper()
	now := time.Now()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"iat":%d,"exp":%d,"iss":%d}`, now.Add(-time.Minute).Unix(), now.Add(5*time.Minute).Unix(), appID))
	sum := sha256.Sum256([]byte(hdr + "." + claims))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, s.URL+"/app/installations/"+strconv.FormatInt(install, 10)+"/access_tokens", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+hdr+"."+claims+"."+base64.RawURLEncoding.EncodeToString(sig))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var it struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&it); err != nil || it.Token == "" {
		t.Fatalf("mint = %d %v", resp.StatusCode, err)
	}
	return it.Token
}
