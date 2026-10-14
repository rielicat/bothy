// Package githubtest is a fake GitHub REST API for tests: personal access
// tokens, a GitHub App with installations and minted tokens, repositories
// with git objects, archive downloads through a codeload redirect, the Git
// Data API, pull requests, issues, comments and workflow runs, with GitHub's
// pagination, rate-limit headers and error bodies. Response bodies start from
// the examples in GitHub's REST API description (fixtures/README.md).
package githubtest

import (
	"cmp"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed fixtures/*.json
var fixtures embed.FS

// Server is the fake. Configure the exported fields before the first
// request; use the methods afterwards.
type Server struct {
	URL string // API base, e.g. http://127.0.0.1:41234 (also serves the archive downloads)

	PAT          string    // the personal access token it accepts
	User         string    // login of the PAT's user (default octocat)
	TokenExpires time.Time // sent as GitHub-Authentication-Token-Expiration when set
	App          *App      // nil: App endpoints answer 401
	// TarballLocation, when set, replaces the codeload URL the tarball
	// endpoint redirects to.
	TarballLocation string
	// DispatchNoDetails answers workflow_dispatch with 204 and no run id, as
	// servers without return_run_details do.
	DispatchNoDetails bool

	srv *httptest.Server

	mu        sync.Mutex
	repos     map[string]*Repo
	nextID    int64
	requests  []Request
	faults    []*fault
	tokens    map[string]minted
	downloads map[string]string // download token -> "owner/repo@sha"
	dispatch  []Dispatch
	rateLimit int
	remaining int
	reset     time.Time
	before    func(*http.Request)
}

// App is the fake's GitHub App.
type App struct {
	ID            int64
	Slug          string
	Key           *rsa.PublicKey
	Installations []Installation
	TokenTTL      time.Duration // default one hour
}

// Installation is one install of the App.
type Installation struct {
	ID      int64
	Account string
	Type    string   // Organization | User
	Repos   []string // full names it reaches; empty reaches every repository
}

type minted struct {
	inst    Installation
	expires time.Time
}

// Dispatch is a workflow_dispatch the fake accepted.
type Dispatch struct {
	Repo, Workflow, Ref string
	Inputs              map[string]string
	ReturnRunDetails    bool
	RunID               int64
	Auth                string // as in Request
}

// Dispatches returns the accepted workflow_dispatch calls, oldest first.
func (s *Server) Dispatches() []Dispatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dispatch)
}

// Request is what the fake recorded about one call.
type Request struct {
	Method string
	Path   string
	Query  string
	Auth   string // pat | jwt | installation | none | bad
	Body   string
}

type fault struct {
	method string
	path   *regexp.Regexp
	status int
	header http.Header
	body   string
	left   int
}

// New starts a fake on 127.0.0.1 with a random PAT.
func New() *Server {
	s := &Server{
		PAT:       "github_pat_" + randString(40),
		User:      "octocat",
		repos:     map[string]*Repo{},
		tokens:    map[string]minted{},
		downloads: map[string]string{},
		nextID:    1000,
		rateLimit: 5000,
		remaining: 5000,
		reset:     time.Now().Add(time.Hour).Truncate(time.Second),
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	return s
}

func (s *Server) Close() { s.srv.Close() }

// OnRequest runs fn before each later request is handled, outside the
// fake's lock, so fn may hold a request in flight.
func (s *Server) OnRequest(fn func(*http.Request)) {
	s.mu.Lock()
	s.before = fn
	s.mu.Unlock()
}

func randString(n int) string {
	const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = abc[int(b[i])%len(abc)]
	}
	return string(b)
}

// AddRepo creates a repository whose default branch main holds files
// (path -> content; a trailing * marks an executable).
func (s *Server) AddRepo(owner, name string, private bool, files map[string]string) *Repo {
	s.mu.Lock()
	s.nextID++
	r := &Repo{srv: s, ID: s.nextID, Owner: owner, Name: name, DefaultBranch: "main", Private: private,
		Description: "This your first repo!", blobs: map[string][]byte{}, trees: map[string]map[string]entry{},
		commits: map[string]*commit{}, refs: map[string]string{}, comments: map[int][]Comment{},
		tarballs: map[string][]byte{}, symlinks: map[string]string{}}
	s.repos[strings.ToLower(owner+"/"+name)] = r
	s.mu.Unlock()
	r.Commit(files, "Initial commit")
	return r
}

// Repo returns a repository by full name.
func (s *Server) Repo(full string) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repos[strings.ToLower(full)]
}

// Fail makes the next times calls of method on paths matching pattern (a
// regexp over the path) answer status with header and body. A body of ""
// gets GitHub's error shape.
func (s *Server) Fail(method, pattern string, status int, header http.Header, body string, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &fault{method: method, path: regexp.MustCompile(pattern), status: status, header: header, body: body, left: times})
}

// SetRateLimit sets the remaining requests of the hour and its reset time.
func (s *Server) SetRateLimit(remaining int, reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remaining, s.reset = remaining, reset.Truncate(time.Second)
}

// Requests returns the recorded calls, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Count returns how many recorded calls match method and path prefix.
func (s *Server) Count(method, prefix string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// MintedTokens returns how many installation tokens were minted.
func (s *Server) MintedTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens)
}

// RevokeInstallationTokens makes every minted token answer 401 from now on,
// as an uninstall or a key rotation does.
func (s *Server) RevokeInstallationTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, m := range s.tokens {
		m.expires = time.Time{}
		s.tokens[k] = m
	}
}

// InstallationTokens returns the minted tokens, so tests can check that
// they never show up where they must not.
func (s *Server) InstallationTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tokens))
	for k := range s.tokens {
		out = append(out, k)
	}
	return out
}

// ---- plumbing ----

type reqCtx struct {
	s    *Server
	w    http.ResponseWriter
	r    *http.Request
	auth string
	inst *Installation
	seg  []string
	body []byte
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	before := s.before
	s.mu.Unlock()
	if before != nil {
		before(r)
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	c := &reqCtx{s: s, w: w, r: r, body: body}
	c.seg = strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	for i, p := range c.seg {
		if u, err := url.PathUnescape(p); err == nil {
			c.seg[i] = u
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(c.seg) > 0 && c.seg[0] == "_codeload" {
		c.auth = "none"
		if r.Header.Get("Authorization") != "" {
			c.auth = "bearer-on-codeload"
		}
		s.record(c)
		c.codeload()
		return
	}
	c.authenticate()
	s.record(c)
	for i, f := range s.faults {
		if f.left > 0 && f.method == r.Method && f.path.MatchString(r.URL.Path) {
			f.left--
			if f.left == 0 {
				s.faults = slices.Delete(s.faults, i, i+1)
			}
			c.rateHeaders()
			for k, vs := range f.header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			if f.body == "" {
				c.error(f.status, http.StatusText(f.status))
			} else {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(f.status)
				_, _ = io.WriteString(w, f.body)
			}
			return
		}
	}
	if c.auth == "bad" {
		c.rateHeaders()
		c.error(http.StatusUnauthorized, "Bad credentials")
		return
	}
	if c.auth != "none" {
		if s.remaining <= 0 {
			c.rateHeaders()
			c.error(http.StatusForbidden, "API rate limit exceeded for user ID 1. If you reach out to GitHub Support for help, please include the request ID.")
			return
		}
		s.remaining--
	}
	c.rateHeaders()
	c.route()
}

func (s *Server) record(c *reqCtx) {
	s.requests = append(s.requests, Request{Method: c.r.Method, Path: c.r.URL.Path, Query: c.r.URL.RawQuery, Auth: c.auth, Body: string(c.body)})
}

func (c *reqCtx) rateHeaders() {
	h := c.w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(c.s.rateLimit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(max(c.s.remaining, 0)))
	h.Set("X-RateLimit-Used", strconv.Itoa(c.s.rateLimit-max(c.s.remaining, 0)))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(c.s.reset.Unix(), 10))
	h.Set("X-RateLimit-Resource", "core")
}

func (c *reqCtx) authenticate() {
	h := c.r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		if tok, ok = strings.CutPrefix(h, "token "); !ok {
			c.auth = "none"
			return
		}
	}
	switch {
	case tok == c.s.PAT:
		c.auth = "pat"
	case strings.Count(tok, ".") == 2 && c.s.App != nil && c.s.verifyJWT(tok):
		c.auth = "jwt"
	default:
		if m, ok := c.s.tokens[tok]; ok && time.Now().Before(m.expires) {
			c.auth, c.inst = "installation", &m.inst
			return
		}
		c.auth = "bad"
	}
}

// verifyJWT checks an App JWT as GitHub does: RS256 by the App's key, iss
// the app id, iat not in the future (60 s grace) and exp at most 10 minutes
// after iat and not passed.
func (s *Server) verifyJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	hdr, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	claims, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	var h struct{ Alg string }
	if json.Unmarshal(hdr, &h) != nil || h.Alg != "RS256" {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(s.App.Key, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	var cl struct {
		Iat, Exp int64
		Iss      json.Number
	}
	if json.Unmarshal(claims, &cl) != nil || cl.Iss.String() != strconv.FormatInt(s.App.ID, 10) {
		return false
	}
	now := time.Now().Unix()
	return cl.Iat <= now+60 && cl.Exp > now && cl.Exp-cl.Iat <= 600+60
}

func (c *reqCtx) json(status int, v any) {
	c.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.w.WriteHeader(status)
	_ = json.NewEncoder(c.w).Encode(v)
}

// error writes GitHub's error body.
func (c *reqCtx) error(status int, msg string, errs ...map[string]string) {
	body := map[string]any{"message": msg, "documentation_url": "https://docs.github.com/rest", "status": strconv.Itoa(status)}
	if len(errs) > 0 {
		body["errors"] = errs
	}
	c.json(status, body)
}

func fixture(name string) any {
	b, err := fixtures.ReadFile("fixtures/" + name + ".json")
	if err != nil {
		panic(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		panic(err)
	}
	return v
}

func obj(v any) map[string]any { return v.(map[string]any) }

func first(name string) map[string]any { return obj(fixture(name).([]any)[0]) }

// paginate slices items by ?page and ?per_page (default 30, max 100) and
// sets the Link header the way GitHub does.
func (c *reqCtx) paginate(n int) (lo, hi int) {
	q := c.r.URL.Query()
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 {
		per = 30
	}
	per = min(per, 100)
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	last := max((n+per-1)/per, 1)
	link := func(p int, rel string) string {
		u := *c.r.URL
		u.Scheme, u.Host = "http", c.r.Host
		qq := u.Query()
		qq.Set("page", strconv.Itoa(p))
		qq.Set("per_page", strconv.Itoa(per))
		u.RawQuery = qq.Encode()
		return "<" + u.String() + `>; rel="` + rel + `"`
	}
	var links []string
	if page > 1 {
		links = append(links, link(page-1, "prev"), link(1, "first"))
	}
	if page < last {
		links = append(links, link(page+1, "next"), link(last, "last"))
	}
	if len(links) > 0 {
		c.w.Header().Set("Link", strings.Join(links, ", "))
	}
	lo = min((page-1)*per, n)
	return lo, min(lo+per, n)
}

func (c *reqCtx) need(kinds ...string) bool {
	if slices.Contains(kinds, c.auth) {
		return true
	}
	if c.auth == "none" {
		c.error(http.StatusUnauthorized, "Requires authentication")
	} else {
		c.error(http.StatusUnauthorized, "A JSON web token could not be decoded")
	}
	return false
}

func (c *reqCtx) visible(r *Repo) bool {
	switch c.auth {
	case "pat":
		return true
	case "installation":
		return len(c.inst.Repos) == 0 || slices.ContainsFunc(c.inst.Repos, func(n string) bool { return strings.EqualFold(n, r.FullName()) })
	}
	return false
}

func (c *reqCtx) route() {
	seg, m := c.seg, c.r.Method
	switch {
	case m == http.MethodGet && len(seg) == 1 && seg[0] == "user":
		c.getUser()
	case m == http.MethodGet && len(seg) == 2 && seg[0] == "user" && seg[1] == "repos":
		c.listRepos(false)
	case m == http.MethodGet && len(seg) == 2 && seg[0] == "installation" && seg[1] == "repositories":
		c.listRepos(true)
	case len(seg) >= 1 && seg[0] == "app":
		c.app()
	case len(seg) >= 3 && seg[0] == "repos":
		r := c.s.repos[strings.ToLower(seg[1]+"/"+seg[2])]
		if r == nil || !c.need("pat", "installation") {
			if r == nil {
				c.error(http.StatusNotFound, "Not Found")
			}
			return
		}
		if !c.visible(r) {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		c.repo(r, seg[3:])
	default:
		c.error(http.StatusNotFound, "Not Found")
	}
}

func (c *reqCtx) getUser() {
	if !c.need("pat") {
		return
	}
	u := obj(fixture("private-user-response-with-public-and-private-profile-information"))
	u["login"] = c.s.User
	if !c.s.TokenExpires.IsZero() {
		c.w.Header().Set("GitHub-Authentication-Token-Expiration", c.s.TokenExpires.UTC().Format("2006-01-02 15:04:05 MST"))
	}
	c.json(http.StatusOK, u)
}

func (c *reqCtx) repoJSON(r *Repo) map[string]any {
	j := first("repository-items-default-response")
	j["id"], j["name"], j["full_name"] = r.ID, r.Name, r.FullName()
	obj(j["owner"])["login"] = r.Owner
	j["private"], j["archived"], j["default_branch"], j["description"] = r.Private, r.Archived, r.DefaultBranch, r.Description
	j["html_url"] = "https://github.com/" + r.FullName()
	return j
}

func (c *reqCtx) sortedRepos() []*Repo {
	var out []*Repo
	for _, r := range c.s.repos {
		if c.visible(r) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *Repo) int {
		return strings.Compare(strings.ToLower(a.FullName()), strings.ToLower(b.FullName()))
	})
	return out
}

func (c *reqCtx) listRepos(installation bool) {
	if installation && !c.need("installation") || !installation && !c.need("pat") {
		return
	}
	all := c.sortedRepos()
	lo, hi := c.paginate(len(all))
	items := []any{}
	for _, r := range all[lo:hi] {
		items = append(items, c.repoJSON(r))
	}
	if !installation {
		c.json(http.StatusOK, items)
		return
	}
	page := obj(fixture("repository-paginated-2"))
	page["total_count"], page["repositories"] = len(all), items
	c.json(http.StatusOK, page)
}

func (c *reqCtx) installationJSON(in Installation) map[string]any {
	j := first("base-installation-items")
	j["id"], j["app_id"], j["repository_selection"] = in.ID, c.s.App.ID, "all"
	if len(in.Repos) > 0 {
		j["repository_selection"] = "selected"
	}
	acc := obj(j["account"])
	acc["login"], acc["type"] = in.Account, in.Type
	return j
}

func (c *reqCtx) app() {
	if c.s.App == nil || !c.need("jwt") {
		if c.s.App == nil {
			c.error(http.StatusUnauthorized, "A JSON web token could not be decoded")
		}
		return
	}
	seg, a := c.seg, c.s.App
	switch {
	case c.r.Method == http.MethodGet && len(seg) == 1:
		j := obj(fixture("integration"))
		j["id"], j["slug"] = a.ID, a.Slug
		c.json(http.StatusOK, j)
	case c.r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "installations":
		lo, hi := c.paginate(len(a.Installations))
		items := []any{}
		for _, in := range a.Installations[lo:hi] {
			items = append(items, c.installationJSON(in))
		}
		c.json(http.StatusOK, items)
	case len(seg) >= 3 && seg[1] == "installations":
		id, _ := strconv.ParseInt(seg[2], 10, 64)
		i := slices.IndexFunc(a.Installations, func(in Installation) bool { return in.ID == id })
		if i < 0 {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		in := a.Installations[i]
		switch {
		case c.r.Method == http.MethodGet && len(seg) == 3:
			c.json(http.StatusOK, c.installationJSON(in))
		case c.r.Method == http.MethodPost && len(seg) == 4 && seg[3] == "access_tokens":
			ttl := a.TokenTTL
			if ttl == 0 {
				ttl = time.Hour
			}
			tok := "ghs_" + randString(36)
			exp := time.Now().Add(ttl).UTC().Truncate(time.Second)
			c.s.tokens[tok] = minted{inst: in, expires: exp}
			j := obj(fixture("installation-token"))
			j["token"], j["expires_at"] = tok, exp.Format(time.RFC3339)
			c.json(http.StatusCreated, j)
		default:
			c.error(http.StatusNotFound, "Not Found")
		}
	default:
		c.error(http.StatusNotFound, "Not Found")
	}
}

// codeload serves archives the tarball endpoint redirected to; GitHub
// authorises them by the token in the query, not a header.
func (c *reqCtx) codeload() {
	key, ok := c.s.downloads[c.r.URL.Query().Get("token")]
	if !ok {
		c.error(http.StatusNotFound, "Not Found")
		return
	}
	full, sha, _ := strings.Cut(key, "@")
	r := c.s.repos[full]
	b, err := r.archive(sha)
	if err != nil {
		c.error(http.StatusNotFound, "Not Found")
		return
	}
	c.w.Header().Set("Content-Type", "application/x-gzip")
	c.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-%s-%s.tar.gz", r.Owner, r.Name, sha[:7]))
	_, _ = c.w.Write(b)
}

func gitRefURL(c *reqCtx, r *Repo, ref string) string {
	return "http://" + c.r.Host + "/repos/" + r.FullName() + "/git/" + ref
}

func (c *reqCtx) repo(r *Repo, seg []string) {
	m := c.r.Method
	switch {
	case m == http.MethodGet && len(seg) == 0:
		c.json(http.StatusOK, c.repoJSON(r))
	case m == http.MethodGet && len(seg) >= 2 && seg[0] == "commits":
		ref := strings.Join(seg[1:], "/")
		sha := r.resolve(ref)
		if sha == "" {
			c.error(http.StatusUnprocessableEntity, "No commit found for SHA: "+ref)
			return
		}
		if c.r.Header.Get("Accept") == "application/vnd.github.sha" {
			c.w.Header().Set("Content-Type", "application/vnd.github.sha; charset=utf-8")
			_, _ = io.WriteString(c.w, sha)
			return
		}
		c.json(http.StatusOK, map[string]any{"sha": sha})
	case m == http.MethodGet && len(seg) >= 2 && seg[0] == "tarball":
		sha := r.resolve(strings.Join(seg[1:], "/"))
		if sha == "" {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		tok := randString(24)
		c.s.downloads[tok] = strings.ToLower(r.FullName()) + "@" + sha
		loc := c.s.URL + "/_codeload/" + r.FullName() + "/legacy.tar.gz/" + sha + "?token=" + tok
		if c.s.TarballLocation != "" {
			loc = c.s.TarballLocation
		}
		c.w.Header().Set("Location", loc)
		c.w.WriteHeader(http.StatusFound)
	case len(seg) >= 1 && seg[0] == "git":
		c.gitData(r, seg[1:])
	case len(seg) >= 1 && seg[0] == "pulls":
		c.pulls(r, seg[1:])
	case len(seg) >= 1 && seg[0] == "issues":
		c.issues(r, seg[1:])
	case len(seg) >= 2 && seg[0] == "actions":
		c.actions(r, seg[1:])
	default:
		c.error(http.StatusNotFound, "Not Found")
	}
}

func (c *reqCtx) decode(v any) bool {
	if err := json.Unmarshal(c.body, v); err != nil {
		c.error(http.StatusBadRequest, "Problems parsing JSON")
		return false
	}
	return true
}

func (c *reqCtx) gitData(r *Repo, seg []string) {
	m := c.r.Method
	switch {
	case m == http.MethodGet && len(seg) == 2 && seg[0] == "commits":
		cm := r.commits[seg[1]]
		if cm == nil {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		j := obj(fixture("git-commit-2"))
		j["sha"], j["message"] = cm.sha, cm.message
		obj(j["tree"])["sha"] = cm.tree
		var parents []any
		for _, p := range cm.parents {
			parents = append(parents, map[string]any{"sha": p})
		}
		j["parents"] = parents
		c.json(http.StatusOK, j)
	case m == http.MethodPost && len(seg) == 1 && seg[0] == "blobs":
		var in struct{ Content, Encoding string }
		if !c.decode(&in) {
			return
		}
		data := []byte(in.Content)
		if in.Encoding == "base64" {
			b, err := base64.StdEncoding.DecodeString(in.Content)
			if err != nil || in.Content == "" {
				c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "Blob", "field": "content", "code": "invalid"})
				return
			}
			data = b
		} else if in.Encoding != "utf-8" && in.Encoding != "" {
			c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "Blob", "field": "encoding", "code": "invalid"})
			return
		}
		j := obj(fixture("short-blob"))
		sha := r.putBlob(data)
		j["sha"], j["url"] = sha, gitRefURL(c, r, "blobs/"+sha)
		c.json(http.StatusCreated, j)
	case m == http.MethodPost && len(seg) == 1 && seg[0] == "trees":
		var in struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct {
				Path, Mode, Type string
				SHA              *string
			}
		}
		if !c.decode(&in) {
			return
		}
		t := map[string]entry{}
		if in.BaseTree != "" {
			base, ok := r.trees[in.BaseTree]
			if !ok {
				c.error(http.StatusUnprocessableEntity, "base_tree is not a valid tree")
				return
			}
			for k, v := range base {
				t[k] = v
			}
		}
		for _, e := range in.Tree {
			switch {
			case e.SHA == nil:
				delete(t, e.Path)
			case r.blobs[*e.SHA] == nil:
				c.error(http.StatusUnprocessableEntity, "tree.sha "+*e.SHA+" is not a valid blob")
				return
			case e.Type != "blob" || (e.Mode != "100644" && e.Mode != "100755"):
				c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "Tree", "field": "mode", "code": "invalid"})
				return
			default:
				t[e.Path] = entry{sha: *e.SHA, mode: e.Mode}
			}
		}
		sha := r.putTree(t)
		j := obj(fixture("git-tree"))
		j["sha"], j["truncated"], j["url"] = sha, false, gitRefURL(c, r, "trees/"+sha)
		c.json(http.StatusCreated, j)
	case m == http.MethodPost && len(seg) == 1 && seg[0] == "commits":
		var in struct {
			Message string
			Tree    string
			Parents []string
		}
		if !c.decode(&in) {
			return
		}
		if _, ok := r.trees[in.Tree]; !ok || in.Message == "" {
			c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "Commit", "field": "tree", "code": "invalid"})
			return
		}
		for _, p := range in.Parents {
			if r.commits[p] == nil {
				c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "Commit", "field": "parents", "code": "invalid"})
				return
			}
		}
		sha := r.putCommit(in.Tree, in.Message, in.Parents)
		j := obj(fixture("git-commit"))
		j["sha"], j["message"] = sha, in.Message
		obj(j["tree"])["sha"] = in.Tree
		c.json(http.StatusCreated, j)
	case m == http.MethodPost && len(seg) == 1 && seg[0] == "refs":
		var in struct{ Ref, SHA string }
		if !c.decode(&in) {
			return
		}
		name, ok := strings.CutPrefix(in.Ref, "refs/")
		if !ok || strings.Count(in.Ref, "/") < 2 {
			c.error(http.StatusUnprocessableEntity, "Reference name must start with 'refs/' and have at least two slashes.")
			return
		}
		if r.commits[in.SHA] == nil {
			c.error(http.StatusUnprocessableEntity, "Object does not exist")
			return
		}
		if _, exists := r.refs[name]; exists {
			c.error(http.StatusUnprocessableEntity, "Reference already exists")
			return
		}
		r.refs[name] = in.SHA
		c.json(http.StatusCreated, c.refJSON(r, name))
	case m == http.MethodGet && len(seg) >= 2 && seg[0] == "ref":
		name := strings.Join(seg[1:], "/")
		if _, ok := r.refs[name]; !ok {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		c.json(http.StatusOK, c.refJSON(r, name))
	case m == http.MethodDelete && len(seg) >= 2 && seg[0] == "refs":
		name := strings.Join(seg[1:], "/")
		if _, ok := r.refs[name]; !ok {
			c.error(http.StatusUnprocessableEntity, "Reference does not exist")
			return
		}
		delete(r.refs, name)
		c.w.WriteHeader(http.StatusNoContent)
	default:
		c.error(http.StatusNotFound, "Not Found")
	}
}

func (c *reqCtx) refJSON(r *Repo, name string) map[string]any {
	j := obj(fixture("git-ref"))
	j["ref"], j["url"] = "refs/"+name, gitRefURL(c, r, "refs/"+name)
	o := obj(j["object"])
	o["sha"], o["url"] = r.refs[name], gitRefURL(c, r, "commits/"+r.refs[name])
	return j
}

func (c *reqCtx) pullJSON(r *Repo, p *Pull, template map[string]any) map[string]any {
	j := template
	j["number"], j["title"], j["body"], j["state"], j["draft"] = p.Number, p.Title, p.Body, p.State, p.Draft
	j["html_url"] = "https://github.com/" + r.FullName() + "/pull/" + strconv.Itoa(p.Number)
	j["merged"], j["merged_at"] = false, nil
	h, b := obj(j["head"]), obj(j["base"])
	h["ref"], h["sha"], h["label"] = p.Head, p.HeadSHA, r.Owner+":"+p.Head
	b["ref"], b["sha"], b["label"] = p.Base, p.BaseSHA, r.Owner+":"+p.Base
	if _, ok := j["changed_files"]; ok {
		j["changed_files"] = len(r.changed(p.BaseSHA, p.HeadSHA))
	}
	return j
}

func (c *reqCtx) findPull(r *Repo, n string) *Pull {
	num, _ := strconv.Atoi(n)
	for _, p := range r.pulls {
		if p.Number == num {
			return p
		}
	}
	return nil
}

func (c *reqCtx) pulls(r *Repo, seg []string) {
	m := c.r.Method
	switch {
	case m == http.MethodPost && len(seg) == 0:
		var in struct {
			Title, Head, Base, Body string
			Draft                   bool
		}
		if !c.decode(&in) {
			return
		}
		head, base := r.refs["heads/"+in.Head], r.refs["heads/"+in.Base]
		switch {
		case in.Title == "":
			c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "PullRequest", "field": "title", "code": "missing_field"})
			return
		case head == "":
			c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "PullRequest", "field": "head", "code": "invalid"})
			return
		case base == "" || in.Head == in.Base:
			c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "PullRequest", "field": "base", "code": "invalid"})
			return
		}
		p := &Pull{Number: r.nextNumber(), Title: in.Title, Body: in.Body, Head: in.Head, Base: in.Base, HeadSHA: head, BaseSHA: base, Draft: in.Draft, State: "open"}
		r.pulls = append(r.pulls, p)
		c.json(http.StatusCreated, c.pullJSON(r, p, obj(fixture("pull-request"))))
	case m == http.MethodGet && len(seg) == 0:
		state := cmp.Or(c.r.URL.Query().Get("state"), "open")
		var ps []*Pull
		for i := len(r.pulls) - 1; i >= 0; i-- {
			if state == "all" || r.pulls[i].State == state {
				ps = append(ps, r.pulls[i])
			}
		}
		lo, hi := c.paginate(len(ps))
		items := []any{}
		for _, p := range ps[lo:hi] {
			items = append(items, c.pullJSON(r, p, first("pull-request-simple-items")))
		}
		c.json(http.StatusOK, items)
	case len(seg) >= 1:
		p := c.findPull(r, seg[0])
		if p == nil {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		switch {
		case m == http.MethodGet && len(seg) == 1 && c.r.Header.Get("Accept") == "application/vnd.github.diff":
			c.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			for _, f := range r.changed(p.BaseSHA, p.HeadSHA) {
				fmt.Fprintf(c.w, "diff --git a/%s b/%s\n", f, f)
			}
		case m == http.MethodGet && len(seg) == 1:
			c.json(http.StatusOK, c.pullJSON(r, p, obj(fixture("pull-request"))))
		case m == http.MethodGet && len(seg) == 2 && seg[1] == "files":
			files := r.changed(p.BaseSHA, p.HeadSHA)
			lo, hi := c.paginate(len(files))
			items := []any{}
			for _, f := range files[lo:hi] {
				j := first("diff-entry-items")
				j["filename"], j["status"] = f, "modified"
				items = append(items, j)
			}
			c.json(http.StatusOK, items)
		case m == http.MethodGet && len(seg) == 2 && seg[1] == "comments":
			c.json(http.StatusOK, fixture("pull-request-review-comment-items"))
		default:
			c.error(http.StatusNotFound, "Not Found")
		}
	default:
		c.error(http.StatusNotFound, "Not Found")
	}
}

func (c *reqCtx) issueJSON(r *Repo, is *Issue, template map[string]any) map[string]any {
	j := template
	j["number"], j["title"], j["body"], j["state"] = is.Number, is.Title, is.Body, is.State
	j["comments"] = len(r.comments[is.Number])
	j["html_url"] = "https://github.com/" + r.FullName() + "/issues/" + strconv.Itoa(is.Number)
	obj(j["user"])["login"] = is.User
	labels := []any{}
	for _, l := range is.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	j["labels"] = labels
	delete(j, "pull_request")
	if is.State == "open" {
		j["state_reason"], j["closed_at"] = nil, nil
	}
	return j
}

func (c *reqCtx) issues(r *Repo, seg []string) {
	m := c.r.Method
	switch {
	case m == http.MethodGet && len(seg) == 0:
		q := c.r.URL.Query()
		state := cmp.Or(q.Get("state"), "open")
		var want []string
		if l := q.Get("labels"); l != "" {
			want = strings.Split(l, ",")
		}
		var list []*Issue
		for i := len(r.issues) - 1; i >= 0; i-- {
			is := r.issues[i]
			if state != "all" && is.State != state {
				continue
			}
			if !slices.ContainsFunc(want, func(w string) bool { return !slices.Contains(is.Labels, w) }) {
				list = append(list, is)
			}
		}
		lo, hi := c.paginate(len(list))
		items := []any{}
		for _, is := range list[lo:hi] {
			items = append(items, c.issueJSON(r, is, first("issue-items")))
		}
		c.json(http.StatusOK, items)
	case len(seg) >= 1:
		num, _ := strconv.Atoi(seg[0])
		i := slices.IndexFunc(r.issues, func(is *Issue) bool { return is.Number == num })
		isPull := c.findPull(r, seg[0]) != nil
		if i < 0 && !isPull {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		switch {
		case m == http.MethodGet && len(seg) == 1 && i >= 0:
			c.json(http.StatusOK, c.issueJSON(r, r.issues[i], obj(fixture("issue"))))
		case m == http.MethodGet && len(seg) == 2 && seg[1] == "comments":
			cs := r.comments[num]
			lo, hi := c.paginate(len(cs))
			items := []any{}
			for _, cm := range cs[lo:hi] {
				j := first("issue-comment-items")
				j["id"], j["body"] = cm.ID, cm.Body
				obj(j["user"])["login"] = cm.User
				items = append(items, j)
			}
			c.json(http.StatusOK, items)
		case m == http.MethodPost && len(seg) == 2 && seg[1] == "comments":
			var in struct{ Body string }
			if !c.decode(&in) {
				return
			}
			if in.Body == "" {
				c.error(http.StatusUnprocessableEntity, "Validation Failed", map[string]string{"resource": "IssueComment", "field": "body", "code": "missing_field"})
				return
			}
			c.s.nextID++
			cm := Comment{ID: c.s.nextID, User: c.s.User, Body: in.Body}
			r.comments[num] = append(r.comments[num], cm)
			j := obj(fixture("issue-comment"))
			j["id"], j["body"] = cm.ID, cm.Body
			j["html_url"] = fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", r.FullName(), num, cm.ID)
			c.json(http.StatusCreated, j)
		default:
			c.error(http.StatusNotFound, "Not Found")
		}
	default:
		c.error(http.StatusNotFound, "Not Found")
	}
}

func (c *reqCtx) runJSON(r *Repo, run *Run, template map[string]any) map[string]any {
	j := template
	j["id"], j["name"], j["display_title"], j["head_branch"] = run.ID, run.Name, run.Title, run.Branch
	j["event"], j["status"] = cmp.Or(run.Event, "push"), run.Status
	if run.Conclusion == "" {
		j["conclusion"] = nil
	} else {
		j["conclusion"] = run.Conclusion
	}
	j["path"] = ".github/workflows/" + run.Workflow + "@" + run.Branch
	j["html_url"] = "https://github.com/" + r.FullName() + "/actions/runs/" + strconv.FormatInt(run.ID, 10)
	if run.HeadSHA != "" {
		j["head_sha"] = run.HeadSHA
	}
	if !run.CreatedAt.IsZero() {
		j["created_at"], j["updated_at"] = run.CreatedAt.UTC().Format(time.RFC3339), run.CreatedAt.UTC().Format(time.RFC3339)
	}
	return j
}

// dispatch starts a queued workflow_dispatch run of workflow on the ref.
func (c *reqCtx) dispatch(r *Repo, workflow string) {
	var in struct {
		Ref              string            `json:"ref"`
		Inputs           map[string]string `json:"inputs"`
		ReturnRunDetails bool              `json:"return_run_details"`
	}
	if !c.decode(&in) {
		return
	}
	if in.Ref == "" {
		c.error(http.StatusUnprocessableEntity, "Invalid request.\n\n\"ref\" wasn't supplied.")
		return
	}
	sha := r.resolve(in.Ref)
	if sha == "" {
		c.error(http.StatusUnprocessableEntity, "No ref found for: "+in.Ref)
		return
	}
	c.s.nextID++
	run := &Run{ID: c.s.nextID, Name: workflow, Workflow: workflow, Branch: in.Ref, Event: "workflow_dispatch",
		Status: "queued", HeadSHA: sha, CreatedAt: time.Now().UTC()}
	r.runs = append(r.runs, run)
	c.s.dispatch = append(c.s.dispatch, Dispatch{Repo: r.FullName(), Workflow: workflow, Ref: in.Ref, Inputs: in.Inputs,
		ReturnRunDetails: in.ReturnRunDetails, RunID: run.ID, Auth: c.auth})
	if in.ReturnRunDetails && !c.s.DispatchNoDetails {
		id := strconv.FormatInt(run.ID, 10)
		c.json(http.StatusOK, map[string]any{"workflow_run_id": run.ID,
			"run_url":  c.s.URL + "/repos/" + r.FullName() + "/actions/runs/" + id,
			"html_url": "https://github.com/" + r.FullName() + "/actions/runs/" + id})
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (c *reqCtx) actions(r *Repo, seg []string) {
	q := c.r.URL.Query()
	list := func(workflow string) {
		var runs []*Run
		for i := len(r.runs) - 1; i >= 0; i-- {
			run := r.runs[i]
			switch st := q.Get("status"); {
			case workflow != "" && run.Workflow != workflow,
				q.Get("branch") != "" && run.Branch != q.Get("branch"),
				q.Get("event") != "" && cmp.Or(run.Event, "push") != q.Get("event"),
				st != "" && run.Status != st && run.Conclusion != st:
				continue
			}
			runs = append(runs, run)
		}
		lo, hi := c.paginate(len(runs))
		items := []any{}
		for _, run := range runs[lo:hi] {
			items = append(items, c.runJSON(r, run, obj(fixture("workflow-run"))))
		}
		page := obj(fixture("workflow-run-paginated"))
		page["total_count"], page["workflow_runs"] = len(runs), items
		c.json(http.StatusOK, page)
	}
	switch {
	case c.r.Method == http.MethodPost && len(seg) == 3 && seg[0] == "workflows" && seg[2] == "dispatches":
		c.dispatch(r, seg[1])
	case c.r.Method != http.MethodGet:
		c.error(http.StatusNotFound, "Not Found")
	case len(seg) == 1 && seg[0] == "runs":
		list("")
	case len(seg) == 3 && seg[0] == "workflows" && seg[2] == "runs":
		list(seg[1])
	case len(seg) >= 2 && seg[0] == "runs":
		id, _ := strconv.ParseInt(seg[1], 10, 64)
		i := slices.IndexFunc(r.runs, func(run *Run) bool { return run.ID == id })
		if i < 0 {
			c.error(http.StatusNotFound, "Not Found")
			return
		}
		switch {
		case len(seg) == 2:
			c.json(http.StatusOK, c.runJSON(r, r.runs[i], obj(fixture("workflow-run"))))
		case len(seg) == 3 && seg[2] == "jobs":
			page := obj(fixture("job-paginated"))
			for _, j := range page["jobs"].([]any) {
				obj(j)["run_id"] = id
			}
			c.json(http.StatusOK, page)
		default:
			c.error(http.StatusNotFound, "Not Found")
		}
	default:
		c.error(http.StatusNotFound, "Not Found")
	}
}
