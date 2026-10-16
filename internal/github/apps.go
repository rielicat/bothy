package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// Bothy's own GitHub App, created from a manifest (SPEC §14.3): GitHub's
// App Manifest flow hands over the new app's private key once, Bothy keeps
// it in the vault, and the app's installation becomes an ordinary App
// connection whose key Bothy owns.

var _ core.GitHubAppMaker = (*Service)(nil)

// appPermissions is the manifest's default_permissions: what the GitHub
// tools and the release dispatcher call, and nothing more.
//
//	metadata read       required with any repository permission; repository lookups
//	contents write      commits and tarballs (repo_checkout); Git Data API blobs, trees, commits and refs (repo_open_pr)
//	pull_requests write github_pr; repo_open_pr creates the pull request; comments on a pull request
//	issues write        github_issues; github_comment on an issue
//	actions write       workflows and runs (github_runs, releases); workflow_dispatch for release deploys
//
// Never administration, secrets, workflows (a pull request that edits
// .github/workflows fails, by design) or any organization permission.
var appPermissions = map[string]string{
	"metadata":      "read",
	"contents":      "write",
	"pull_requests": "write",
	"issues":        "write",
	"actions":       "write",
}

var (
	manifestCodeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	appSlugRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
	clientIDRE     = regexp.MustCompile(`^Iv[0-9A-Za-z.]{1,40}$`)
)

// maxAppName is GitHub's limit on an app's name.
const maxAppName = 34

const appCreateTimeout = 30 * time.Second

// defaultAppName is "Bothy (<x>)", cutting x so the name fits GitHub's 34
// characters. The web app proposes the same name.
func defaultAppName(x string) string {
	if x == "" {
		return "Bothy"
	}
	if n := maxAppName - len("Bothy ()"); utf8.RuneCountInString(x) > n {
		x = string([]rune(x)[:n])
	}
	return "Bothy (" + x + ")"
}

func appURL(web, slug string) string { return web + "/apps/" + slug }

func appInstallURL(web, slug string) string { return appURL(web, slug) + "/installations/new" }

func appSettingsURL(web, owner, ownerType, slug string) string {
	if ownerType == "Organization" {
		return web + "/organizations/" + url.PathEscape(owner) + "/settings/apps/" + slug
	}
	return web + "/settings/apps/" + slug
}

// fill sets the fields derived from the app's state and github.api_base.
func (s *Service) fill(a *core.GitHubOwnApp) *core.GitHubOwnApp {
	switch {
	case !a.RetiredAt.IsZero():
		a.State = "retired"
	case a.Connection != "":
		a.State = "connected"
	default:
		a.State = "pending"
	}
	if ep, err := s.endpoint(); err == nil {
		web := ep.web()
		a.URL, a.InstallURL, a.SettingsURL = appURL(web, a.Slug), appInstallURL(web, a.Slug), appSettingsURL(web, a.Owner, a.OwnerType, a.Slug)
	}
	return a
}

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{core.ErrInvalid}, a...)...)
}

// appManifest is the JSON form field GitHub's new-app page reads, in a fixed
// field order. No hook_attributes: Bothy takes no webhooks.
type appManifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	Description        string            `json:"description"`
	RedirectURL        string            `json:"redirect_url"`
	SetupURL           string            `json:"setup_url"`
	SetupOnUpdate      bool              `json:"setup_on_update"`
	Public             bool              `json:"public"`
	DefaultEvents      []string          `json:"default_events"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

func (s *Service) AppManifest(t core.GitHubAppTarget, origin string) (*core.GitHubAppHandoff, error) {
	o, err := url.Parse(origin)
	if err != nil || o.Scheme != "http" && o.Scheme != "https" || o.Host == "" || o.User != nil || o.Opaque != "" ||
		o.RawQuery != "" || o.Fragment != "" || o.Path != "" && o.Path != "/" {
		return nil, invalidf("Bothy's address must be http(s)://host[:port], got %q", origin)
	}
	origin = o.Scheme + "://" + o.Host
	if t.Org != "" && !ownerRE.MatchString(t.Org) {
		return nil, invalidf("%q isn't an organization name on GitHub", t.Org)
	}
	name := strings.TrimSpace(t.Name)
	if name == "" {
		name = defaultAppName(cmp.Or(t.Org, t.Fallback))
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > maxAppName || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, invalidf("the app name must be 1 to %d characters", maxAppName)
	}
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	web := ep.web()
	action := web + "/settings/apps/new"
	if t.Org != "" {
		action = web + "/organizations/" + url.PathEscape(t.Org) + "/settings/apps/new"
	}
	m, err := json.Marshal(appManifest{
		Name: name, URL: origin,
		Description:   "Lets Bothy at " + origin + " read code, open pull requests and start deploys in the repositories you choose.",
		RedirectURL:   origin + "/api/github/apps/created",
		SetupURL:      origin + "/api/github/apps/installed",
		SetupOnUpdate: true, DefaultEvents: []string{}, DefaultPermissions: maps.Clone(appPermissions),
	})
	if err != nil {
		return nil, err
	}
	au, err := url.Parse(action)
	if err != nil {
		return nil, err
	}
	return &core.GitHubAppHandoff{Action: action, WebOrigin: au.Scheme + "://" + au.Host, Manifest: string(m), Name: name, Org: t.Org}, nil
}

type ghManifestConversion struct {
	ID           int64             `json:"id"`
	Slug         string            `json:"slug"`
	Name         string            `json:"name"`
	Owner        ghUser            `json:"owner"`
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret"`
	PEM          string            `json:"pem"`
	Permissions  map[string]string `json:"permissions"`
	// webhook_secret is deliberately not decoded: Bothy takes no webhooks.
}

// scrubCode drops the manifest code from an error: until it is spent it
// redeems the app's private key, and GitHub's errors name the path.
func scrubCode(err error, code string) error {
	msg := strings.ReplaceAll(err.Error(), code, "{code}")
	if st := statusOf(err); st != 0 {
		return &apiError{status: st, msg: msg}
	}
	return errors.New(msg)
}

func (s *Service) CreateApp(ctx context.Context, code string, t core.GitHubAppTarget, replaces string, by core.UserID) (*core.GitHubOwnApp, error) {
	if !manifestCodeRE.MatchString(code) {
		return nil, invalidf("the code GitHub sent back is malformed")
	}
	// The code redeems the key once: never spend it while the key can't be kept.
	if s.vault.Status().Sealed {
		return nil, core.ErrSealed
	}
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	// Once GitHub has handed the key over, the browser leaving must not stop
	// Bothy keeping it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), appCreateTimeout)
	defer cancel()
	owner := cmp.Or(t.Org, "personal")
	fail := func(err error) (*core.GitHubOwnApp, error) {
		s.record(ctx, userActor(by), "github.app_create", "", "error", map[string]any{"owner": owner, "error": s.vault.Redactor().Redact(err.Error())})
		return nil, err
	}
	cl := &apiClient{ep: ep, http: s.httpClient(ep), now: s.now}
	var conv ghManifestConversion
	if err := cl.call(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", nil, &conv); err != nil {
		return fail(fmt.Errorf("github: redeeming the new app's code: %w", scrubCode(err, code)))
	}
	name := oneLine(conv.Name, 100)
	switch _, kerr := parsePrivateKey([]byte(conv.PEM)); {
	case conv.ID <= 0, !appSlugRE.MatchString(conv.Slug), !ownerRE.MatchString(conv.Owner.Login),
		conv.Owner.Type != "Organization" && conv.Owner.Type != "User", name == "" || len(name) > 100,
		kerr != nil, conv.ClientID != "" && !clientIDRE.MatchString(conv.ClientID):
		return fail(errors.New("github: GitHub's answer about the new app was unexpected"))
	}
	if t.Org != "" && !strings.EqualFold(conv.Owner.Login, t.Org) {
		s.log.Warn("github: the new app belongs to another account than the one chosen", "chosen", t.Org, "owner", conv.Owner.Login)
	}
	id := core.NewID(core.PrefixGitHubApp)
	a := &core.GitHubOwnApp{ID: id, AppID: conv.ID, Slug: conv.Slug, Name: name, Owner: conv.Owner.Login, OwnerType: conv.Owner.Type,
		ClientID: conv.ClientID, Replaces: replaces, CreatedBy: by}
	keyName, secretName := "github_"+id+"_key", "github_"+id+"_client_secret"
	if _, err := s.vault.Put(ctx, core.GlobalScope, keyName, []byte(conv.PEM), by); err != nil {
		return fail(fmt.Errorf("github: storing the new app's private key: %w", err))
	}
	a.KeyRef = "secret:" + keyName
	if conv.ClientSecret != "" {
		if _, err := s.vault.Put(ctx, core.GlobalScope, secretName, []byte(conv.ClientSecret), by); err != nil {
			s.deleteSecret(ctx, keyName, by)
			return fail(fmt.Errorf("github: storing the new app's client secret: %w", err))
		}
		a.ClientSecretRef = "secret:" + secretName
	}
	if err := s.store.PutApp(ctx, a); err != nil {
		s.deleteSecret(ctx, keyName, by)
		s.deleteSecret(ctx, secretName, by)
		return fail(err)
	}
	detail := map[string]any{"app_id": a.AppID, "slug": a.Slug, "name": a.Name, "owner": a.Owner, "owner_type": a.OwnerType,
		"permissions": conv.Permissions}
	if extra := extraPermissions(conv.Permissions); len(extra) > 0 {
		detail["extra_permissions"] = extra
		s.log.Warn("github: GitHub granted the new app more than Bothy asked for", "app", a.Slug, "extra", extra)
	}
	if replaces != "" {
		detail["replaces"] = replaces
	}
	s.record(ctx, userActor(by), "github.app_create", a.ID, "ok", detail)
	return s.fill(a), nil
}

// extraPermissions lists the granted permissions beyond appPermissions, as
// "name:level".
func extraPermissions(granted map[string]string) []string {
	var out []string
	for k, v := range granted {
		want := appPermissions[k]
		if want == "" || want == "read" && v != "read" {
			out = append(out, k+":"+v)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Service) deleteSecret(ctx context.Context, name string, by core.UserID) {
	if err := s.vault.Delete(context.WithoutCancel(ctx), core.GlobalScope, name, by); err != nil && !errors.Is(err, core.ErrNotFound) {
		s.log.Warn("github: deleting a vault secret", "name", name, "err", err)
	}
}

// appSrc is the cached App source of a Bothy-made app, without an
// installation, so automatic checks read its key at most every secretTTL.
func (s *Service) appSrc(a *core.GitHubOwnApp, ep endpoint) *appSource {
	key := "app:" + a.ID
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.sources[key]; ok && cs.base == ep.base.String() {
		return cs.src.(*appSource)
	}
	if old, ok := s.sources[key]; ok {
		forget(old.src)
	}
	src := s.newSource(ep, core.GitHubApp, s.vaultSecret(a.KeyRef, key), strconv.FormatInt(a.AppID, 10), 0, key).(*appSource)
	s.sources[key] = &connSource{base: ep.base.String(), src: src}
	return src
}

func (s *Service) InstallApp(ctx context.Context, id string, installationID int64, by core.UserID) (*core.GitHubConnection, error) {
	if installationID <= 0 {
		return nil, invalidf("installation_id must be positive")
	}
	unlock := s.locks.lock("app:" + id)
	defer unlock()
	a, err := s.store.App(ctx, id)
	if err != nil {
		return nil, err
	}
	if !a.RetiredAt.IsZero() {
		return nil, invalidf("this app is no longer used")
	}
	if a.Connection != "" {
		conn, err := s.store.Connection(ctx, a.Connection)
		switch {
		case err == nil && conn.InstallationID == installationID:
			return conn, nil
		case err == nil:
			return s.reinstall(ctx, a, conn, installationID, by)
		case !errors.Is(err, core.ErrNotFound):
			return nil, err
		}
	}
	creds := core.GitHubCredentials{Auth: core.GitHubApp, AppID: strconv.FormatInt(a.AppID, 10), InstallationID: installationID, SecretRef: a.KeyRef}
	// The probe asks GitHub for the installation with the app's JWT: one of
	// another app is not found, so an id from a URL can't attach anything else.
	conn, p, err := s.connect(ctx, creds, nil, by, connectOpts{method: "app_manifest", ownsRef: true, app: a})
	if err != nil {
		return nil, err
	}
	a.Connection = conn.ID
	if err := s.store.PutApp(ctx, a); err != nil {
		// Not Disconnect: that would delete the key the app still needs.
		_ = s.store.DeleteConnection(context.WithoutCancel(ctx), conn.ID)
		s.dropSource(conn.ID)
		return nil, err
	}
	if a.Replaces != "" && a.Replaces != conn.ID {
		if err := s.swap(ctx, a.Replaces, conn, p, by); err != nil {
			s.log.Warn("github: moving repositories to the new app", "app", a.ID, "err", err)
		}
	}
	if a.Replaces != "" {
		a.Replaces = ""
		if err := s.store.PutApp(ctx, a); err != nil {
			s.log.Warn("github: recording the finished swap", "app", a.ID, "err", err)
		}
	}
	return conn, nil
}

// reinstall moves the app's connection to a new installation of it (the
// app was uninstalled and installed again), once GitHub confirms the
// installation is the app's.
func (s *Service) reinstall(ctx context.Context, a *core.GitHubOwnApp, conn *core.GitHubConnection, installationID int64, by core.UserID) (*core.GitHubConnection, error) {
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	var in ghInstallation
	if _, err := s.appSrc(a, ep).appClient().getJSON(ctx, "/app/installations/"+strconv.FormatInt(installationID, 10), nil, &in); err != nil {
		return nil, appCheckError(err)
	}
	conn.InstallationID, conn.Status, conn.Error, conn.CheckedAt = installationID, "ok", "", s.now()
	if in.Account.Login != "" {
		conn.Account, conn.AccountType = in.Account.Login, in.Account.Type
	}
	if err := s.store.PutConnection(ctx, conn); err != nil {
		return nil, err
	}
	s.dropSource(conn.ID)
	s.record(ctx, userActor(by), "github.connect", conn.ID, "ok", map[string]any{"auth": string(conn.Auth), "account": conn.Account,
		"installation_id": installationID, "method": "app_manifest", "app": a.Slug, "app_name": a.Name, "reinstalled": true})
	return conn, nil
}

// swap moves the repositories the replaced connection allows to the new
// one, where its installation reaches them, then disconnects the old one.
func (s *Service) swap(ctx context.Context, oldID string, conn *core.GitHubConnection, p *core.GitHubProbe, by core.UserID) error {
	_, oldRepos, err := s.Connection(ctx, oldID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var held []string
	for _, r := range oldRepos {
		if r.Allowed {
			held = append(held, r.FullName())
		}
	}
	ep, err := s.endpoint()
	if err != nil {
		return err
	}
	if err := s.lookupUnlisted(ctx, s.apiClient(ep, s.source(conn, ep)), p, held, conn.ID); err != nil {
		return err
	}
	var carry []core.GitHubRepoAccess
	for _, r := range p.Repos {
		if slices.ContainsFunc(held, func(h string) bool { return strings.EqualFold(h, r.Owner+"/"+r.Name) }) {
			carry = append(carry, r)
		}
	}
	if err := s.Disconnect(ctx, oldID, by); err != nil {
		return err
	}
	var added []string
	for _, r := range carry {
		if err := s.store.PutRepo(ctx, accessRepo(conn.ID, r, true)); err != nil {
			s.log.Warn("github: moving a repository to the new app", "repo", r.Owner+"/"+r.Name, "err", err)
			continue
		}
		added = append(added, r.Owner+"/"+r.Name)
	}
	if err := s.refresh(ctx); err != nil {
		s.log.Warn("github: refreshing the tool gate", "err", err)
	}
	s.record(ctx, userActor(by), "github.repos", conn.ID, "ok", map[string]any{"added": nonNilStrings(added), "removed": []string{}})
	return nil
}

// appCheckError keeps a locked vault as itself and words any other failure
// to reach the app as GitHub's reason.
func appCheckError(err error) error {
	if errors.Is(err, core.ErrSealed) {
		return err
	}
	return fmt.Errorf("%w: %s", core.ErrInvalid, probeError(err))
}

func (s *Service) CheckApp(ctx context.Context, id string, by core.UserID) (*core.GitHubConnection, error) {
	a, err := s.store.App(ctx, id)
	if err != nil {
		return nil, err
	}
	if !a.RetiredAt.IsZero() {
		return nil, fmt.Errorf("%w: this app is no longer used", core.ErrConflict)
	}
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	insts, _, err := paged(ctx, s.appSrc(a, ep).appClient(), "/app/installations", nil, 100, jsonArray[ghInstallation])
	if err != nil {
		return nil, appCheckError(err)
	}
	i := slices.IndexFunc(insts, func(in ghInstallation) bool { return strings.EqualFold(in.Account.Login, a.Owner) })
	if i < 0 && len(insts) == 1 {
		i = 0
	}
	if i < 0 {
		return nil, nil
	}
	return s.InstallApp(ctx, id, insts[i].ID, by)
}

func (s *Service) Apps(ctx context.Context) ([]*core.GitHubOwnApp, error) {
	apps, err := s.store.Apps(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		s.fill(a)
	}
	return apps, nil
}

func (s *Service) ConnectionApp(ctx context.Context, connID string) (*core.GitHubOwnApp, error) {
	a, err := s.store.AppByConnection(ctx, connID)
	if err != nil {
		return nil, err
	}
	return s.fill(a), nil
}

func (s *Service) RetireApp(ctx context.Context, id string, by core.UserID) (*core.GitHubOwnApp, error) {
	unlock := s.locks.lock("app:" + id)
	defer unlock()
	a, err := s.store.App(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case a.Connection != "":
		return nil, fmt.Errorf("%w: disconnect GitHub first", core.ErrConflict)
	case !a.RetiredAt.IsZero():
		return s.fill(a), nil
	}
	if err := s.retire(ctx, a, by); err != nil {
		return nil, err
	}
	s.record(ctx, userActor(by), "github.app_retire", a.ID, "ok", map[string]any{"slug": a.Slug, "name": a.Name, "owner": a.Owner})
	return s.fill(a), nil
}

// retire deletes the app's keys and keeps its row as retired. The app stays
// on GitHub: Bothy never deletes apps there.
func (s *Service) retire(ctx context.Context, a *core.GitHubOwnApp, by core.UserID) error {
	for _, ref := range []string{a.KeyRef, a.ClientSecretRef} {
		if name, ok := strings.CutPrefix(ref, "secret:"); ok {
			s.deleteSecret(ctx, name, by)
		}
	}
	a.KeyRef, a.ClientSecretRef, a.Connection, a.Replaces, a.RetiredAt = "", "", "", "", s.now()
	s.dropSource("app:" + a.ID)
	return s.store.PutApp(context.WithoutCancel(ctx), a)
}

func (s *Service) DeleteApp(ctx context.Context, id string, _ core.UserID) error {
	a, err := s.store.App(ctx, id)
	if err != nil {
		return err
	}
	if a.RetiredAt.IsZero() {
		return fmt.Errorf("%w: only an app Bothy no longer uses can be dismissed", core.ErrConflict)
	}
	return s.store.DeleteApp(ctx, id)
}
