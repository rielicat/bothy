package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// maxProbeRepos bounds the repositories a probe lists (ten API pages);
// requested repositories past it are looked up one by one. A var for tests.
var maxProbeRepos = 1000

// adminTimeout bounds one connection call from the HTTP API: a probe pages
// through up to maxProbeRepos repositories, and nothing else stops a
// GitHub that accepts the connection and never answers.
const adminTimeout = 2 * time.Minute

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Owner         ghUser `json:"owner"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description"`
}

type ghApp struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Owner ghUser `json:"owner"`
}

type ghInstallation struct {
	ID                  int64  `json:"id"`
	Account             ghUser `json:"account"`
	RepositorySelection string `json:"repository_selection"`
}

// validateCreds checks the shape of a connect or test request.
func validateCreds(c core.GitHubCredentials) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{core.ErrInvalid}, a...)...)
	}
	given := 0
	for _, set := range []bool{!c.Token.IsZero(), !c.PrivateKey.IsZero(), c.SecretRef != ""} {
		if set {
			given++
		}
	}
	if given != 1 {
		return bad("give exactly one of token, private_key or secret_ref")
	}
	if c.SecretRef != "" && !validSecretRef(c.SecretRef) {
		return bad(`secret_ref must look like "secret:<name>" or "aws-sm:<secret>[#<json key>]"`)
	}
	switch c.Auth {
	case core.GitHubPAT:
		if !c.PrivateKey.IsZero() || c.AppID != "" || c.InstallationID != 0 {
			return bad("a personal access token connection takes only a token")
		}
	case core.GitHubApp:
		if !c.Token.IsZero() {
			return bad("a GitHub App connection takes the App's private key, not a token")
		}
		if !appIDRE.MatchString(c.AppID) {
			return bad("app_id must be the App's numeric id or its client id")
		}
		if c.InstallationID < 0 {
			return bad("installation_id must be positive")
		}
	default:
		return bad(`auth must be "pat" or "app"`)
	}
	return nil
}

func rawSecret(c core.GitHubCredentials) core.Secret {
	if !c.Token.IsZero() {
		return core.NewSecret([]byte(strings.TrimSpace(c.Token.Reveal())))
	}
	return c.PrivateKey
}

// credSecret yields the request's secret: its raw value or the vault ref.
func (s *Service) credSecret(c core.GitHubCredentials) secretFunc {
	if c.SecretRef != "" {
		return s.vaultSecret(c.SecretRef, "test")
	}
	sec := rawSecret(c)
	return func(context.Context) (core.Secret, error) { return sec, nil }
}

func (s *Service) Test(ctx context.Context, c core.GitHubCredentials) (*core.GitHubProbe, error) {
	if err := validateCreds(c); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	src := s.newSource(ep, c.Auth, s.credSecret(c), c.AppID, c.InstallationID, "")
	defer forget(src)
	p := s.probe(ctx, ep, src, c.Auth, c.AppID, c.InstallationID, "")
	return p, nil
}

// probe asks GitHub who the credentials are and what they reach. Failures
// land in the probe (OK false), never in an error.
func (s *Service) probe(ctx context.Context, ep endpoint, src tokenSource, auth core.GitHubAuth, appID string, install int64, connID string) *core.GitHubProbe {
	p := &core.GitHubProbe{Auth: auth, Repos: []core.GitHubRepoAccess{}}
	var last rateInfo
	observe := func(ri rateInfo) { last = ri }
	fail := func(err error) *core.GitHubProbe {
		p.OK, p.Error, p.Code = false, probeError(err), probeCode(err)
		p.RateLimit = core.GitHubRateLimit{Limit: last.limit, Remaining: last.remaining, Reset: last.reset, Resource: last.resource}
		return p
	}
	var repos []ghRepo
	var truncated bool
	switch auth {
	case core.GitHubApp:
		as, ok := src.(*appSource)
		if !ok {
			return fail(errors.New("github: no App credentials"))
		}
		app := as.appClient()
		app.observe = observe
		var a ghApp
		if _, err := app.getJSON(ctx, "/app", nil, &a); err != nil {
			return fail(err)
		}
		p.AppSlug = a.Slug
		if install == 0 {
			insts, _, err := paged(ctx, app, "/app/installations", nil, 100, jsonArray[ghInstallation])
			if err != nil {
				return fail(err)
			}
			for _, in := range insts {
				p.Installations = append(p.Installations, core.GitHubInstallation{ID: in.ID, Account: in.Account.Login,
					AccountType: in.Account.Type, RepositorySelection: in.RepositorySelection})
			}
			switch len(insts) {
			case 0:
				return fail(errors.New("the App is not installed on any account yet: install it on GitHub first"))
			case 1:
				install = insts[0].ID
				as.setInstallation(install)
			default:
				return fail(fmt.Errorf("the App has %d installations: choose one", len(insts)))
			}
		}
		var in ghInstallation
		if _, err := app.getJSON(ctx, "/app/installations/"+strconv.FormatInt(install, 10), nil, &in); err != nil {
			return fail(err)
		}
		p.InstallationID, p.Account, p.AccountType = install, in.Account.Login, in.Account.Type
		cl := &apiClient{ep: ep, http: as.http, auth: as.token, authFailed: as.invalidate, observe: observe, now: s.now}
		var err error
		repos, truncated, err = paged(ctx, cl, "/installation/repositories", nil, maxProbeRepos, func(b []byte) ([]ghRepo, error) {
			var page struct {
				Repositories []ghRepo `json:"repositories"`
			}
			err := json.Unmarshal(b, &page)
			return page.Repositories, err
		})
		if err != nil {
			return fail(err)
		}
	default:
		cl := &apiClient{ep: ep, http: s.httpClient(ep), auth: src.token, authFailed: src.invalidate, observe: observe, now: s.now}
		var u ghUser
		hdr, err := cl.getJSON(ctx, "/user", nil, &u)
		if err != nil {
			return fail(err)
		}
		p.Account, p.AccountType = u.Login, u.Type
		p.TokenExpires = tokenExpiry(hdr.Get("GitHub-Authentication-Token-Expiration"))
		repos, truncated, err = paged(ctx, cl, "/user/repos", nil, maxProbeRepos, jsonArray[ghRepo])
		if err != nil {
			return fail(err)
		}
	}
	held, err := s.store.Repos(ctx, "")
	if err != nil {
		return fail(err)
	}
	for _, r := range repos {
		if acc, ok := repoAccess(r, held, connID); ok {
			p.Repos = append(p.Repos, acc)
		}
	}
	slices.SortFunc(p.Repos, func(a, b core.GitHubRepoAccess) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Owner), strings.ToLower(b.Owner)),
			strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	p.OK, p.Truncated = true, truncated
	p.RateLimit = core.GitHubRateLimit{Limit: last.limit, Remaining: last.remaining, Reset: last.reset, Resource: last.resource}
	return p
}

// repoAccess describes a repository a probe found, with the allowlist
// state of its stored row if any. A row another connection no longer
// allows does not hold the repository: any connection may take it over.
func repoAccess(r ghRepo, held []*core.GitHubRepo, connID string) (core.GitHubRepoAccess, bool) {
	owner, name, ok := repoNames(r)
	if !ok {
		return core.GitHubRepoAccess{}, false
	}
	acc := core.GitHubRepoAccess{GitHubID: r.ID, Owner: owner, Name: name, Private: r.Private, Archived: r.Archived,
		DefaultBranch: r.DefaultBranch, Description: oneLine(r.Description, 300)}
	if i := slices.IndexFunc(held, func(h *core.GitHubRepo) bool {
		return strings.EqualFold(h.Owner, owner) && strings.EqualFold(h.Name, name)
	}); i >= 0 && (held[i].Allowed || held[i].Connection == connID) {
		acc.Connection = held[i].Connection
		acc.Allowed = held[i].Allowed && (connID == "" || held[i].Connection == connID)
	}
	return acc, true
}

// lookupUnlisted adds the requested repositories a truncated probe did not
// list, asking GitHub for each one; those it does not show stay out, and
// pick reports them as unreachable.
func (s *Service) lookupUnlisted(ctx context.Context, cl *apiClient, p *core.GitHubProbe, want []string, connID string) error {
	if !p.Truncated {
		return nil
	}
	var held []*core.GitHubRepo
	for _, w := range want {
		owner, name, err := splitRepo(w)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(p.Repos, func(r core.GitHubRepoAccess) bool {
			return strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Name, name)
		}) {
			continue
		}
		var r ghRepo
		if _, err := cl.getJSON(ctx, "/repos/"+owner+"/"+name, nil, &r); err != nil {
			if statusOf(err) == http.StatusNotFound {
				continue
			}
			return err
		}
		if held == nil {
			if held, err = s.store.Repos(ctx, ""); err != nil {
				return err
			}
		}
		if acc, ok := repoAccess(r, held, connID); ok && strings.EqualFold(acc.Owner, owner) && strings.EqualFold(acc.Name, name) {
			p.Repos = append(p.Repos, acc)
		}
	}
	return nil
}

// repoNames takes owner and name from a repository object, refusing ones
// that would not be safe path segments.
func repoNames(r ghRepo) (string, string, bool) {
	owner, name := r.Owner.Login, r.Name
	if owner == "" || name == "" {
		var ok bool
		if owner, name, ok = strings.Cut(r.FullName, "/"); !ok {
			return "", "", false
		}
	}
	if _, _, err := splitRepo(owner + "/" + name); err != nil {
		return "", "", false
	}
	return owner, name, true
}

func probeError(err error) string {
	var ae *apiError
	var re *rateLimitError
	switch {
	case errors.As(err, &re):
		return re.msg
	case errors.As(err, &ae):
		return ae.msg
	case errors.Is(err, core.ErrSealed):
		return "the vault is sealed: unseal it first"
	case errors.Is(err, core.ErrNotFound):
		return "the secret reference names no stored secret"
	}
	return err.Error()
}

// probeCode says why a probe failed, for the connect screen.
func probeCode(err error) core.FailureCode {
	var ae *apiError
	var re *rateLimitError
	switch {
	case errors.As(err, &re):
		return core.FailRateLimited
	case errors.As(err, &ae) && ae.status == http.StatusUnauthorized:
		return core.FailKeyRejected
	case errors.As(err, &ae) && ae.status >= 500:
		return core.FailProviderError
	case errors.As(err, &ae):
		return core.FailUnknown
	}
	if c := core.CodeOf(err); c != "" {
		return c
	}
	return core.FailUnknown
}

// tokenExpiry parses GitHub-Authentication-Token-Expiration, sent for
// tokens that expire ("2026-11-01 10:00:00 UTC").
func tokenExpiry(h string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, strings.TrimSpace(h)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// pick resolves requested "owner/name" strings against what a probe found.
func pick(p *core.GitHubProbe, want []string, connID string) ([]core.GitHubRepoAccess, error) {
	seen := map[string]bool{}
	var out []core.GitHubRepoAccess
	for _, w := range want {
		owner, name, err := splitRepo(w)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(owner + "/" + name)
		if seen[key] {
			continue
		}
		seen[key] = true
		i := slices.IndexFunc(p.Repos, func(r core.GitHubRepoAccess) bool {
			return strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Name, name)
		})
		if i < 0 {
			return nil, fmt.Errorf("%w: %s/%s is not reachable with these credentials", core.ErrInvalid, owner, name)
		}
		r := p.Repos[i]
		if r.Connection != "" && r.Connection != connID {
			return nil, fmt.Errorf("%w: %s/%s is already connected through another GitHub connection", core.ErrConflict, r.Owner, r.Name)
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) Connect(ctx context.Context, c core.GitHubCredentials, repos []string, by core.UserID) (*core.GitHubConnection, error) {
	conn, _, err := s.connect(ctx, c, repos, by, connectOpts{method: methodOf(c)})
	return conn, err
}

func methodOf(c core.GitHubCredentials) string {
	if c.Auth == core.GitHubApp {
		return "app"
	}
	return "token"
}

type connectOpts struct {
	method string // audit: "token" | "app" | "app_manifest"
	// ownsRef: c.SecretRef names a secret Bothy stored (the key of an app it
	// made), so Disconnect deletes it. A failed connect leaves it alone: it
	// still belongs to the app.
	ownsRef bool
	app     *core.GitHubOwnApp // the probe must find this app
}

func (s *Service) connect(ctx context.Context, c core.GitHubCredentials, repos []string, by core.UserID, o connectOpts) (*core.GitHubConnection, *core.GitHubProbe, error) {
	if err := validateCreds(c); err != nil {
		return nil, nil, err
	}
	ep, err := s.endpoint()
	if err != nil {
		return nil, nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	src := s.newSource(ep, c.Auth, s.credSecret(c), c.AppID, c.InstallationID, "")
	defer forget(src)
	p := s.probe(probeCtx, ep, src, c.Auth, c.AppID, c.InstallationID, "")
	if !p.OK {
		return nil, nil, fmt.Errorf("%w: %s", core.ErrInvalid, p.Error)
	}
	if o.app != nil && p.AppSlug != o.app.Slug {
		return nil, nil, fmt.Errorf("%w: GitHub names the app %q, not %q", core.ErrInvalid, p.AppSlug, o.app.Slug)
	}
	if err := s.lookupUnlisted(probeCtx, s.apiClient(ep, src), p, repos, ""); err != nil {
		return nil, nil, err
	}
	chosen, err := pick(p, repos, "")
	if err != nil {
		return nil, nil, err
	}
	conn := &core.GitHubConnection{
		ID: core.NewID(core.PrefixGitHubConn), Auth: c.Auth, Account: p.Account, AccountType: p.AccountType,
		AppSlug: p.AppSlug, InstallationID: p.InstallationID, Status: "ok", TokenExpires: p.TokenExpires,
		CheckedAt: s.now(), CreatedBy: by, SecretRef: c.SecretRef, OwnsSecret: o.ownsRef,
	}
	if c.Auth == core.GitHubApp {
		conn.AppID = c.AppID
	}
	storedHere := conn.SecretRef == ""
	if storedHere {
		name := "github_" + conn.ID
		if _, err := s.vault.Put(ctx, core.GlobalScope, name, rawSecret(c).Bytes(), by); err != nil {
			return nil, nil, fmt.Errorf("github: storing the secret in the vault: %w", err)
		}
		conn.SecretRef, conn.OwnsSecret = "secret:"+name, true
	}
	undo := func() {
		if storedHere {
			s.dropOwnedSecret(ctx, conn, by)
		}
	}
	if err := s.store.PutConnection(ctx, conn); err != nil {
		undo()
		return nil, nil, err
	}
	for _, r := range chosen {
		if err := s.store.PutRepo(ctx, accessRepo(conn.ID, r, true)); err != nil {
			_ = s.store.DeleteConnection(context.WithoutCancel(ctx), conn.ID)
			undo()
			return nil, nil, err
		}
	}
	if err := s.refresh(ctx); err != nil {
		s.log.Warn("github: refreshing the tool gate", "err", err)
	}
	detail := map[string]any{
		"auth": string(conn.Auth), "account": conn.Account, "installation_id": conn.InstallationID,
		"repos": repoNamesOf(chosen), "owns_secret": conn.OwnsSecret, "method": o.method,
	}
	if o.app != nil {
		detail["app"], detail["app_name"] = o.app.Slug, o.app.Name
	}
	s.record(ctx, userActor(by), "github.connect", conn.ID, "ok", detail)
	return conn, p, nil
}

func accessRepo(connID string, r core.GitHubRepoAccess, allowed bool) *core.GitHubRepo {
	return &core.GitHubRepo{Connection: connID, GitHubID: r.GitHubID, Owner: r.Owner, Name: r.Name, DefaultBranch: r.DefaultBranch,
		Private: r.Private, Allowed: allowed}
}

func repoNamesOf(rs []core.GitHubRepoAccess) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Owner + "/" + r.Name
	}
	return out
}

func (s *Service) dropOwnedSecret(ctx context.Context, c *core.GitHubConnection, by core.UserID) {
	if !c.OwnsSecret {
		return
	}
	name := strings.TrimPrefix(c.SecretRef, "secret:")
	if err := s.vault.Delete(context.WithoutCancel(ctx), core.GlobalScope, name, by); err != nil && !errors.Is(err, core.ErrNotFound) {
		s.log.Warn("github: deleting the connection's vault secret", "connection", c.ID, "err", err)
	}
}

func (s *Service) Connections(ctx context.Context) ([]*core.GitHubConnection, error) {
	return s.store.Connections(ctx)
}

func (s *Service) Connection(ctx context.Context, id string) (*core.GitHubConnection, []*core.GitHubRepo, error) {
	c, err := s.store.Connection(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	repos, err := s.store.Repos(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return c, repos, nil
}

func (s *Service) Probe(ctx context.Context, id string) (*core.GitHubProbe, error) {
	c, err := s.store.Connection(ctx, id)
	if err != nil {
		return nil, err
	}
	ep, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	p := s.probe(probeCtx, ep, s.source(c, ep), c.Auth, c.AppID, c.InstallationID, c.ID)
	// The probe can take minutes; writing back the row read before it would
	// bring back a connection disconnected meanwhile.
	if c, err = s.store.Connection(ctx, id); err != nil {
		return nil, err
	}
	c.CheckedAt = s.now()
	if p.OK {
		c.Status, c.Error, c.Account, c.AccountType, c.TokenExpires = "ok", "", p.Account, p.AccountType, p.TokenExpires
		if p.AppSlug != "" {
			c.AppSlug = p.AppSlug
		}
	} else {
		c.Status, c.Error = "error", p.Error
	}
	if err := s.store.PutConnection(ctx, c); err != nil {
		return nil, err
	}
	// Default branches and visibility change on GitHub; keep the stored rows
	// current for the repositories this connection holds.
	if p.OK {
		held, err := s.store.Repos(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, h := range held {
			if i := slices.IndexFunc(p.Repos, func(r core.GitHubRepoAccess) bool {
				return strings.EqualFold(r.Owner, h.Owner) && strings.EqualFold(r.Name, h.Name)
			}); i >= 0 {
				r := p.Repos[i]
				if r.DefaultBranch != h.DefaultBranch || r.Private != h.Private || r.Owner != h.Owner || r.Name != h.Name {
					if err := s.store.PutRepo(ctx, accessRepo(id, r, h.Allowed)); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return p, nil
}

func (s *Service) SetRepos(ctx context.Context, id string, repos []string, by core.UserID) ([]*core.GitHubRepo, error) {
	c, err := s.store.Connection(ctx, id)
	if err != nil {
		return nil, err
	}
	held, err := s.store.Repos(ctx, id)
	if err != nil {
		return nil, err
	}
	var chosen []core.GitHubRepoAccess
	if len(repos) > 0 {
		ep, err := s.endpoint()
		if err != nil {
			return nil, err
		}
		probeCtx, cancel := context.WithTimeout(ctx, adminTimeout)
		defer cancel()
		src := s.source(c, ep)
		p := s.probe(probeCtx, ep, src, c.Auth, c.AppID, c.InstallationID, c.ID)
		if !p.OK {
			return nil, fmt.Errorf("%w: %s", core.ErrInvalid, p.Error)
		}
		if err := s.lookupUnlisted(probeCtx, s.apiClient(ep, src), p, repos, id); err != nil {
			return nil, err
		}
		if chosen, err = pick(p, repos, id); err != nil {
			return nil, err
		}
	}
	var added, removed []string
	for _, r := range chosen {
		wasAllowed := slices.ContainsFunc(held, func(h *core.GitHubRepo) bool {
			return h.Allowed && strings.EqualFold(h.Owner, r.Owner) && strings.EqualFold(h.Name, r.Name)
		})
		if err := s.store.PutRepo(ctx, accessRepo(id, r, true)); err != nil {
			return nil, err
		}
		if !wasAllowed {
			added = append(added, r.Owner+"/"+r.Name)
		}
	}
	for _, h := range held {
		if !h.Allowed || slices.ContainsFunc(chosen, func(r core.GitHubRepoAccess) bool {
			return strings.EqualFold(h.Owner, r.Owner) && strings.EqualFold(h.Name, r.Name)
		}) {
			continue
		}
		h.Allowed = false
		if err := s.store.PutRepo(ctx, h); err != nil {
			return nil, err
		}
		removed = append(removed, h.FullName())
	}
	if err := s.refresh(ctx); err != nil {
		s.log.Warn("github: refreshing the tool gate", "err", err)
	}
	s.record(ctx, userActor(by), "github.repos", id, "ok", map[string]any{"added": nonNilStrings(added), "removed": nonNilStrings(removed)})
	return s.store.Repos(ctx, id)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Disconnect also retires the app Bothy made for the connection: its keys
// go, and the app stays on GitHub until an admin deletes it there.
func (s *Service) Disconnect(ctx context.Context, id string, by core.UserID) error {
	c, err := s.store.Connection(ctx, id)
	if err != nil {
		return err
	}
	app, err := s.store.AppByConnection(ctx, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		app = nil
	case err != nil:
		return err
	}
	if err := s.store.DeleteConnection(ctx, id); err != nil {
		return err
	}
	s.dropSource(id)
	s.dropOwnedSecret(ctx, c, by)
	detail := map[string]any{"account": c.Account, "owned_secret": c.OwnsSecret}
	if app != nil {
		// If this fails the app reads pending with its key gone; checking it
		// then reports the vault error and the admin can start over.
		if err := s.retire(ctx, app, by); err != nil {
			s.log.Warn("github: retiring the app of a disconnected connection", "app", app.ID, "err", err)
		}
		detail["app"] = app.Slug
	}
	if err := s.refresh(ctx); err != nil {
		s.log.Warn("github: refreshing the tool gate", "err", err)
	}
	s.record(ctx, userActor(by), "github.disconnect", id, "ok", detail)
	return nil
}

func (s *Service) Repos(ctx context.Context) ([]*core.GitHubRepo, error) {
	all, err := s.store.Repos(ctx, "")
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if r.Allowed {
			out = append(out, r)
		}
	}
	return out, nil
}

// allowedRepo resolves a tool's "owner/name" to an allowlisted repository
// and its connection.
func (s *Service) allowedRepo(ctx context.Context, repo string) (*core.GitHubRepo, error) {
	return s.connectedRepo(ctx, repo, true)
}

// connectedRepo resolves "owner/name" to its repository row; mustAllow
// also refuses a row its connection no longer allows.
func (s *Service) connectedRepo(ctx context.Context, repo string, mustAllow bool) (*core.GitHubRepo, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	r, err := s.store.Repo(ctx, owner, name)
	if errors.Is(err, core.ErrNotFound) || err == nil && mustAllow && !r.Allowed {
		return nil, fmt.Errorf("%w: %s/%s is not a connected repository (repo_list shows the connected ones)", core.ErrInvalid, owner, name)
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) connectionFor(ctx context.Context, r *core.GitHubRepo) (*core.GitHubConnection, error) {
	c, err := s.store.Connection(ctx, r.Connection)
	if err != nil {
		return nil, fmt.Errorf("github: the connection of %s: %w", r.FullName(), err)
	}
	return c, nil
}

// Checks reports connections whose last check failed and tokens that
// expire within a week.
func (s *Service) Checks() []core.Check { return []core.Check{connCheck{s}} }

type connCheck struct{ s *Service }

func (connCheck) ID() string { return "github.connections" }

func (c connCheck) Run(ctx context.Context) []core.Finding {
	conns, err := c.s.store.Connections(ctx)
	if err != nil {
		return []core.Finding{{ID: "github.unreadable", Area: "github", Title: "GitHub connections could not be read",
			Detail: err.Error(), Severity: core.SevMedium}}
	}
	var out []core.Finding
	now := c.s.now()
	for _, conn := range conns {
		label := conn.Account
		if label == "" {
			label = conn.ID
		}
		if conn.Status == "error" {
			out = append(out, core.Finding{ID: "github.connection_error", Area: "github",
				Title:  "GitHub connection " + label + " is failing",
				Detail: conn.Error, Fix: "/settings/connections", Severity: core.SevMedium})
		}
		if !conn.TokenExpires.IsZero() && conn.TokenExpires.Sub(now) < 7*24*time.Hour {
			out = append(out, core.Finding{ID: "github.token_expiring", Area: "github",
				Title:  "The GitHub token of " + label + " expires " + conn.TokenExpires.Format("2006-01-02"),
				Detail: "Create a new fine-grained token on GitHub and reconnect.", Fix: "/settings/connections", Severity: core.SevLow})
		}
	}
	return out
}

func (connCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }
