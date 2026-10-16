// Package oauthtest fakes Google's and GitHub's sign-in endpoints on a
// loopback server, for the auth, httpapi and e2e tests and for local stubs
// (StartGoogle, StartGitHub). It checks what a real
// provider checks (client credentials, single-use codes, the exact redirect
// address, PKCE S256) and records what it was asked and what it issued, so
// tests can prove no token or code leaks.
package oauthtest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Identity is who consents at the next authorize.
type Identity struct {
	// Google
	Subject, Email, Name, HD string
	EmailVerified            bool
	// GitHub
	ID     int64
	Login  string
	Emails []Email
	Orgs   map[string]string // org → "active" | "pending" | "restricted" (answers 403); missing answers 404
}

type Email struct {
	Email             string
	Primary, Verified bool
}

// AuthRecord is one authorize request as the browser sent it.
type AuthRecord struct {
	ClientID, RedirectURI, State, Nonce, Challenge, Method, Scope string
	Query                                                         url.Values
}

type grant struct {
	id        Identity
	redirect  string
	challenge string
	nonce     string
	clientID  string
	issued    time.Time
	used      bool
}

// Server is one fake provider.
type Server struct {
	URL, ClientID, ClientSecret string

	google bool
	srv    *httptest.Server

	mu       sync.Mutex
	who      Identity
	deny     bool
	fail     map[string]int
	codes    map[string]*grant
	tokens   map[string]Identity
	revoked  []string
	issued   []string
	orgs     map[string]int64
	auths    []AuthRecord
	mutate   func(map[string]any)
	tokenErr string
}

// NewGoogle starts a fake Google answering /o/oauth2/v2/auth and /token,
// stopped when the test ends.
func NewGoogle(t testing.TB, clientID, secret string) *Server {
	return mustStart(t, true, clientID, secret)
}

// NewGitHub starts a fake GitHub answering the github.com OAuth paths and the
// api.github.com paths sign-in uses, stopped when the test ends.
func NewGitHub(t testing.TB, clientID, secret string) *Server {
	return mustStart(t, false, clientID, secret)
}

func mustStart(t testing.TB, google bool, clientID, secret string) *Server {
	t.Helper()
	s, err := start(google, clientID, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// Option changes how StartGoogle and StartGitHub serve.
type Option func(*options)

type options struct{ listen string }

// Listen serves on addr ("127.0.0.1:19405") instead of a random loopback
// port, for a local stub that Bothy's config points at.
func Listen(addr string) Option { return func(o *options) { o.listen = addr } }

// StartGoogle is NewGoogle outside a test: the caller stops it with Close.
func StartGoogle(clientID, secret string, opts ...Option) (*Server, error) {
	return start(true, clientID, secret, opts)
}

// StartGitHub is NewGitHub outside a test: the caller stops it with Close.
func StartGitHub(clientID, secret string, opts ...Option) (*Server, error) {
	return start(false, clientID, secret, opts)
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Handler is the fake's HTTP handler, for mounting on a server of the
// caller's.
func (s *Server) Handler() http.Handler { return s.srv.Config.Handler }

func start(google bool, clientID, secret string, opts []Option) (*Server, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	s := &Server{ClientID: clientID, ClientSecret: secret, google: google, fail: map[string]int{}, codes: map[string]*grant{},
		tokens: map[string]Identity{}, orgs: map[string]int64{}}
	mux := http.NewServeMux()
	if google {
		mux.HandleFunc("GET /o/oauth2/v2/auth", s.authorize)
		mux.HandleFunc("POST /token", s.googleToken)
	} else {
		mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
		mux.HandleFunc("POST /login/oauth/access_token", s.githubToken)
		mux.HandleFunc("GET /user", s.githubUser)
		mux.HandleFunc("GET /user/emails", s.githubEmails)
		mux.HandleFunc("GET /user/memberships/orgs/{org}", s.githubMembership)
		mux.HandleFunc("GET /orgs/{org}", s.githubOrg)
		mux.HandleFunc("DELETE /applications/{client}/token", s.githubRevoke)
	}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		status, ok := s.fail[r.URL.Path]
		delete(s.fail, r.URL.Path)
		s.mu.Unlock()
		if ok {
			writeJSON(w, status, map[string]string{"error": "server_error", "message": "fake failure"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	if o.listen != "" {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", o.listen)
		if err != nil {
			return nil, fmt.Errorf("oauthtest: %w", err)
		}
		_ = s.srv.Listener.Close()
		s.srv.Listener = ln
	}
	s.srv.Start()
	s.URL = s.srv.URL
	return s, nil
}

// SignInAs sets who consents at the following authorizes.
func (s *Server) SignInAs(id Identity) {
	s.mu.Lock()
	s.who, s.deny = id, false
	s.mu.Unlock()
}

// SetOrg makes GET /orgs/{login} answer with id, and memberships of login
// carry it as organization.id. Calling it again with another id is a
// renamed organization's name taken by another.
func (s *Server) SetOrg(login string, id int64) {
	s.mu.Lock()
	s.orgs[strings.ToLower(login)] = id
	s.mu.Unlock()
}

func (s *Server) orgID(login string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orgs[strings.ToLower(login)]
}

// Deny makes the next authorize redirect back with error=access_denied.
func (s *Server) Deny() {
	s.mu.Lock()
	s.deny = true
	s.mu.Unlock()
}

// FailNext makes the next request to path answer status.
func (s *Server) FailNext(path string, status int) {
	s.mu.Lock()
	s.fail[path] = status
	s.mu.Unlock()
}

// TokenError makes the token endpoint answer with this error code until
// cleared with "" (GitHub in a 200 body, Google with 400 or 401).
func (s *Server) TokenError(code string) {
	s.mu.Lock()
	s.tokenErr = code
	s.mu.Unlock()
}

// MutateClaims edits the claims of the id tokens Google issues from now on.
func (s *Server) MutateClaims(fn func(map[string]any)) {
	s.mu.Lock()
	s.mutate = fn
	s.mu.Unlock()
}

// Revoked lists the GitHub tokens revoked through DELETE /applications/{id}/token.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.revoked)
}

// WaitRevoked waits up to 5 s for n revocations (they run in the background).
func (s *Server) WaitRevoked(n int) []string {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if r := s.Revoked(); len(r) >= n {
			return r
		}
	}
	return s.Revoked()
}

// Issued lists every code, access token and id token the fake handed out.
func (s *Server) Issued() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.issued)
}

// Authorizations lists the authorize requests received.
func (s *Server) Authorizations() []AuthRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.auths)
}

func random() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rec := AuthRecord{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"), Nonce: q.Get("nonce"),
		Challenge: q.Get("code_challenge"), Method: q.Get("code_challenge_method"), Scope: q.Get("scope"), Query: q}
	redirect, err := url.Parse(rec.RedirectURI)
	if err != nil || redirect.Scheme == "" || rec.ClientID != s.ClientID || rec.Method != "S256" || rec.Challenge == "" ||
		(s.google && q.Get("response_type") != "code") {
		http.Error(w, "fake provider: bad authorize request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.auths = append(s.auths, rec)
	deny, who := s.deny, s.who
	s.deny = false
	back := redirect.Query()
	back.Set("state", rec.State)
	if deny {
		back.Set("error", "access_denied")
	} else {
		code := random()
		s.codes[code] = &grant{id: who, redirect: rec.RedirectURI, challenge: rec.Challenge, nonce: rec.Nonce, clientID: rec.ClientID, issued: time.Now()}
		s.issued = append(s.issued, code)
		back.Set("code", code)
		if s.google {
			back.Set("scope", googleScope)
			back.Set("authuser", "0")
		}
	}
	s.mu.Unlock()
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

const googleScope = "openid email profile"

// redeem checks a token request the way the providers do and consumes the
// code. It runs with s.mu held.
func (s *Server) redeem(form url.Values) (*grant, string) {
	g := s.codes[form.Get("code")]
	switch {
	case g == nil, g.used, time.Since(g.issued) > time.Minute:
		return nil, "bad_code"
	case form.Get("redirect_uri") != g.redirect:
		return nil, "redirect_uri_mismatch"
	case challenge(form.Get("code_verifier")) != g.challenge:
		return nil, "bad_code"
	}
	g.used = true
	return g, ""
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) googleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	f := r.PostForm
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokenErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tokenErr})
		return
	}
	if f.Get("client_id") != s.ClientID || f.Get("client_secret") != s.ClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client", "error_description": "The OAuth client was not found."})
		return
	}
	if f.Get("grant_type") != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	g, problem := s.redeem(f)
	if problem != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": problem})
		return
	}
	now := time.Now()
	claims := map[string]any{"iss": "https://accounts.google.com", "aud": s.ClientID, "azp": s.ClientID, "sub": g.id.Subject,
		"email": g.id.Email, "email_verified": g.id.EmailVerified, "name": g.id.Name, "nonce": g.nonce,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	if g.id.HD != "" {
		claims["hd"] = g.id.HD
	}
	if s.mutate != nil {
		s.mutate(claims)
	}
	payload, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	idToken := enc([]byte(`{"alg":"RS256","kid":"fake","typ":"JWT"}`)) + "." + enc(payload) + "." + enc([]byte("not-a-signature"))
	access := "ya29.fake-" + random()
	s.issued = append(s.issued, access, idToken)
	writeJSON(w, http.StatusOK, map[string]any{"access_token": access, "id_token": idToken, "expires_in": 3599, "token_type": "Bearer", "scope": googleScope})
}

func (s *Server) githubToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Error(w, "fake github: want a form and Accept: application/json", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	s.mu.Lock()
	defer s.mu.Unlock()
	refuse := func(code string) {
		writeJSON(w, http.StatusOK, map[string]string{"error": code, "error_description": code})
	}
	if s.tokenErr != "" {
		refuse(s.tokenErr)
		return
	}
	if f.Get("client_id") != s.ClientID || f.Get("client_secret") != s.ClientSecret {
		refuse("incorrect_client_credentials")
		return
	}
	g, problem := s.redeem(f)
	switch problem {
	case "":
	case "redirect_uri_mismatch":
		refuse("redirect_uri_mismatch")
		return
	default:
		refuse("bad_verification_code")
		return
	}
	tok := "gho_fake" + random()
	s.tokens[tok] = g.id
	s.issued = append(s.issued, tok)
	writeJSON(w, http.StatusOK, map[string]string{"access_token": tok, "token_type": "bearer", "scope": "read:org,user:email"})
}

// bearer resolves the request's GitHub token; revoked tokens stop working.
func (s *Server) bearer(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	id, known := s.tokens[tok]
	s.mu.Unlock()
	if !ok || !known || r.Header.Get("X-GitHub-Api-Version") == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return Identity{}, false
	}
	return id, true
}

func (s *Server) githubUser(w http.ResponseWriter, r *http.Request) {
	id, ok := s.bearer(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id.ID, "login": id.Login, "name": id.Name})
}

func (s *Server) githubEmails(w http.ResponseWriter, r *http.Request) {
	id, ok := s.bearer(w, r)
	if !ok {
		return
	}
	out := []map[string]any{}
	for _, e := range id.Emails {
		out = append(out, map[string]any{"email": e.Email, "primary": e.Primary, "verified": e.Verified, "visibility": nil})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) githubMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := s.bearer(w, r)
	if !ok {
		return
	}
	org := r.PathValue("org")
	var state string
	for k, v := range id.Orgs {
		if strings.EqualFold(k, org) {
			state = v
		}
	}
	switch state {
	case "active", "pending":
		writeJSON(w, http.StatusOK, map[string]any{"state": state, "role": "member", "organization": map[string]any{"login": org, "id": s.orgID(org)}})
	case "restricted":
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Although you appear to have the correct authorization credentials, the organization has enabled OAuth App access restrictions."})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

// githubOrg answers GET /orgs/{org}, which needs no token, for the
// organizations SetOrg made.
func (s *Server) githubOrg(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("org")
	id := s.orgID(org)
	if id == 0 || r.Header.Get("X-GitHub-Api-Version") == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": org, "id": id, "type": "Organization"})
}

func (s *Server) githubRevoke(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != s.ClientID || pass != s.ClientSecret || r.PathValue("client") != s.ClientID {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err := json.Unmarshal(b, &body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request"})
		return
	}
	s.mu.Lock()
	_, known := s.tokens[body.AccessToken]
	delete(s.tokens, body.AccessToken)
	if known {
		s.revoked = append(s.revoked, body.AccessToken)
	}
	s.mu.Unlock()
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Follow requests the authorize address as a browser would, without
// following the redirect, and returns where the provider sends the browser
// (the gateway's callback with code and state).
func Follow(ctx context.Context, c *http.Client, authURL string) (string, error) {
	cp := http.Client{Timeout: 10 * time.Second}
	if c != nil {
		cp = *c
	}
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := cp.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("oauthtest: authorize answered %d: %s", resp.StatusCode, b)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("oauthtest: authorize answered without a Location")
	}
	return loc, nil
}

// Path returns loc's path and query, for driving an in-process handler.
func Path(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	return u.RequestURI()
}
