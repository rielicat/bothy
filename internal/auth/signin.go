package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/auth/oauth"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// noPassword is users.pw_hash of an account made by provider sign-in. No
// PHC string parses as it, and checkLogin treats it like an unknown user.
const noPassword = "!"

// defaultNext is where sign-in lands without a usable next path, as the
// web app's safeNext.
const defaultNext = "/chat"

const finishTimeout = 30 * time.Second

// SignInDeps is what provider sign-in needs beyond Deps. cmd/bothy sets it
// once the vault and egress exist (like vault.SetAWS); until then, and in
// offline CLI commands, providers report "unavailable".
type SignInDeps struct {
	Vault core.Vault                         // resolves client secrets (audited secret.use), lists saved keys
	HTTP  func(core.SystemDest) *http.Client // egress.SystemClient
}

func (s *Service) SetSignIn(d SignInDeps) { s.signIn.Store(&d) }

// SignInStart is where to send the browser, and the state its sign-in
// cookie must carry back.
type SignInStart struct {
	URL    string
	State  string
	MaxAge time.Duration
}

// SignInCallback is what the provider's redirect brought back.
type SignInCallback struct {
	CookieState string // the sign-in cookie
	State       string // the state query parameter
	Code        string
	Error       string // the provider's error query parameter
	IP          string
}

type SignInResult struct {
	Intent   SignInIntent
	Provider core.SignInProvider
	Cookie   string     // signin: the new session cookie
	Actor    core.Actor // signin
	Next     string     // signin: validated path to land on
	Created  bool       // signin: a member account was made
	Link     *core.SignIn
}

// SignInError codes, which the web app turns into copy.
const (
	SignInCancelled     = "cancelled"      // the person said no at the provider
	SignInExpired       = "expired"        // missing, replayed or expired flow, a cookie mismatch, a stale code
	SignInNotAllowed    = "not_allowed"    // not on the sign-in list (or Google hasn't verified the address)
	SignInOrgAccess     = "org_access"     // a GitHub organization restricts the app
	SignInNoAccount     = "no_account"     // on the list, nothing linked, and allow_new off
	SignInDisabled      = "disabled"       // the account is turned off
	SignInTaken         = "taken"          // link: the provider account is someone else's
	SignInAlreadyLinked = "already_linked" // link: the person has another account of that provider
	SignInSignedOut     = "signed_out"     // link: the session that started it ended
	SignInUnavailable   = "unavailable"    // turned off, misconfigured, or the provider failed
)

// SignInError is a provider round trip that ended without signing in or
// linking. Next is the flow's next path, so the failure page can keep it;
// Who is the account's email (Google) or login (GitHub) when known.
type SignInError struct {
	Code     string
	Intent   SignInIntent
	Provider core.SignInProvider
	Next     string
	Who      string
	Err      error
}

func (e *SignInError) Error() string {
	msg := "sign-in with " + e.Provider.Name() + ": " + e.Code
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *SignInError) Unwrap() error { return e.Err }

func validProvider(p core.SignInProvider) bool { return slices.Contains(core.SignInProviders, p) }

// providerConf is one provider's settings and whether it can work now.
type providerConf struct {
	p         core.SignInProvider
	enabled   bool
	clientID  string
	secretRef string
	secret    *core.SecretMeta // what secretRef names, when found
	rules     oauth.Rules
	allowNew  bool // auth.sign_in.allow_new, in team mode only
	ep        oauth.Endpoints
	epErr     error
	redirect  string
	// problem is "" (usable), unavailable (SetSignIn never called),
	// no_address, incomplete, no_secret or sealed.
	problem string
}

func (pc providerConf) ready() bool { return pc.enabled && pc.problem == "" }

func (s *Service) providerConf(ctx context.Context, p core.SignInProvider) providerConf {
	cfg := s.conf()
	si := cfg.Auth.SignIn
	pc := providerConf{p: p, rules: oauth.Rules{Allow: si.Allow}, allowNew: si.AllowNew && cfg.Mode == "multi",
		redirect: config.SignInRedirect(cfg.Server.PublicURL, string(p))}
	rules := len(si.Allow) > 0
	switch p {
	case core.SignInGoogle:
		g := si.Google
		pc.enabled, pc.clientID, pc.secretRef = g.Enabled, g.ClientID, g.ClientSecret
		pc.ep, pc.epErr = oauth.GoogleEndpoints(g.APIBase)
	case core.SignInGitHub:
		h := si.GitHub
		pc.enabled, pc.clientID, pc.secretRef = h.Enabled, h.ClientID, h.ClientSecret
		pc.ep, pc.epErr = oauth.GitHubEndpoints(h.APIBase)
		pc.rules.Orgs = h.Orgs
		rules = config.SignInListsAddress(si.Allow) || len(h.Orgs) > 0
	}
	deps := s.signIn.Load()
	secretProblem := ""
	if deps != nil && pc.secretRef != "" {
		pc.secret, secretProblem = s.secretState(ctx, deps.Vault, pc.secretRef)
	}
	switch {
	case deps == nil:
		pc.problem = "unavailable"
	case pc.redirect == "":
		pc.problem = "no_address"
	case pc.clientID == "" || pc.secretRef == "" || !rules || pc.epErr != nil:
		pc.problem = "incomplete"
	default:
		pc.problem = secretProblem
	}
	return pc
}

// secretState finds the saved secret ref names without reading it, so
// listing what the sign-in page offers writes no secret.use rows.
func (s *Service) secretState(ctx context.Context, v core.Vault, ref string) (*core.SecretMeta, string) {
	if strings.HasPrefix(ref, config.AWSSecretPrefix) {
		return &core.SecretMeta{Scope: core.GlobalScope, Name: ref, Source: "aws-sm"}, ""
	}
	name, ok := strings.CutPrefix(ref, "secret:")
	if !ok || v == nil {
		return nil, "incomplete"
	}
	metas, err := v.List(ctx, core.GlobalScope)
	if err != nil {
		s.log.Warn("sign-in: list saved keys", "err", err)
		return nil, "unavailable"
	}
	for i := range metas {
		if metas[i].Name != name {
			continue
		}
		if metas[i].Source == "vault" && v.Status().Sealed {
			return &metas[i], "sealed"
		}
		return &metas[i], ""
	}
	return nil, "no_secret"
}

func (s *Service) signInRepo() (core.SignInRepo, error) {
	ss, ok := s.store.(core.SignInStore)
	if !ok {
		return nil, fmt.Errorf("sign-in links: %w", core.ErrNotImplemented)
	}
	return ss.SignIns(), nil
}

// safeNext keeps next only when it is a path on this site the web app can
// land on after signing in, as the web app's own safeNext.
func safeNext(next string) string {
	if next == "" || len(next) > 512 || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return defaultNext
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return defaultNext
	}
	for _, p := range []string{"/api", "/v1", "/login", "/setup", "/password"} {
		if u.Path == p || strings.HasPrefix(u.Path, p+"/") {
			return defaultNext
		}
	}
	return next
}

// SignInOptions lists the providers the sign-in page offers: those turned
// on and usable now. Nothing is offered before setup.
func (s *Service) SignInOptions(ctx context.Context) ([]core.SignInOption, error) {
	out := []core.SignInOption{}
	if need, err := s.NeedsSetup(ctx); err != nil || need {
		return out, err
	}
	for _, p := range core.SignInProviders {
		if s.providerConf(ctx, p).ready() {
			out = append(out, core.SignInOption{Provider: p, Name: p.Name()})
		}
	}
	return out, nil
}

// StartSignIn begins signing in with p and returns the provider's address.
func (s *Service) StartSignIn(ctx context.Context, p core.SignInProvider, next string) (SignInStart, error) {
	unavailable := &SignInError{Code: SignInUnavailable, Intent: IntentSignIn, Provider: p}
	if !validProvider(p) {
		return SignInStart{}, unavailable
	}
	if need, err := s.NeedsSetup(ctx); err != nil {
		return SignInStart{}, err
	} else if need {
		return SignInStart{}, unavailable
	}
	pc := s.providerConf(ctx, p)
	if !pc.ready() {
		return SignInStart{}, unavailable
	}
	return s.start(pc, &flow{intent: IntentSignIn, next: safeNext(next)}), nil
}

// StartLink begins connecting p to the signed-in account of by. The flow
// is bound to the session cookie it starts from, by hash.
func (s *Service) StartLink(ctx context.Context, by core.Actor, sessionCookie string, p core.SignInProvider) (SignInStart, error) {
	if by.Via != "cookie" || sessionCookie == "" {
		return SignInStart{}, fmt.Errorf("%w: connecting a sign-in method needs a browser session", core.ErrForbidden)
	}
	if !validProvider(p) {
		return SignInStart{}, &SignInError{Code: SignInUnavailable, Intent: IntentLink, Provider: p}
	}
	pc := s.providerConf(ctx, p)
	if !pc.ready() {
		return SignInStart{}, &SignInError{Code: SignInUnavailable, Intent: IntentLink, Provider: p}
	}
	repo, err := s.signInRepo()
	if err != nil {
		return SignInStart{}, err
	}
	links, err := repo.ForUser(ctx, by.User)
	if err != nil {
		return SignInStart{}, err
	}
	if slices.ContainsFunc(links, func(l *core.SignIn) bool { return l.Provider == p }) {
		return SignInStart{}, fmt.Errorf("%w: you already connected %s; disconnect it first", core.ErrConflict, p.Name())
	}
	return s.start(pc, &flow{intent: IntentLink, user: by.User, sessionHash: sha(sessionCookie)}), nil
}

func (s *Service) start(pc providerConf, f *flow) SignInStart {
	state, nonce := randomToken(32), randomToken(16)
	verifier, challenge := oauth.NewPKCE()
	f.provider, f.verifier, f.nonce, f.redirect, f.clientID = pc.p, verifier, nonce, pc.redirect, pc.clientID
	s.flows.put(state, f)
	r := oauth.AuthRequest{ClientID: pc.clientID, RedirectURI: pc.redirect, State: state, Nonce: nonce, Challenge: challenge}
	var u string
	if pc.p == core.SignInGoogle {
		r.DomainHint = oauth.DomainHint(pc.rules.Allow)
		u = oauth.GoogleAuthURL(pc.ep, r)
	} else {
		u = oauth.GitHubAuthURL(pc.ep, r)
	}
	return SignInStart{URL: u, State: state, MaxAge: flowTTL}
}

// FinishSignIn completes the round trip the provider's redirect ends. The
// sign-in list is checked again here every time, so someone taken off it
// can no longer sign in even with a linked account.
func (s *Service) FinishSignIn(ctx context.Context, p core.SignInProvider, in SignInCallback) (*SignInResult, error) {
	ctx, cancel := context.WithTimeout(ctx, finishTimeout)
	defer cancel()
	f := s.flows.take(in.State)
	if f == nil {
		// Not audited: anyone can hit the callback, and the log is append-only.
		return nil, &SignInError{Code: SignInExpired, Intent: IntentSignIn, Provider: p}
	}
	if f.provider != p || in.CookieState == "" || subtle.ConstantTimeCompare([]byte(in.CookieState), []byte(in.State)) != 1 {
		return nil, s.refuse(ctx, f, p, in.IP, SignInExpired, nil, nil)
	}
	if in.Error != "" {
		code := SignInUnavailable
		if in.Error == "access_denied" {
			code = SignInCancelled
		}
		return nil, s.refuse(ctx, f, p, in.IP, code, nil, nil)
	}
	if in.Code == "" || len(in.Code) > 2048 {
		return nil, s.refuse(ctx, f, p, in.IP, SignInUnavailable, nil, nil)
	}
	pc := s.providerConf(ctx, p)
	deps := s.signIn.Load()
	if !pc.ready() || deps == nil || pc.clientID != f.clientID || pc.redirect != f.redirect {
		return nil, s.refuse(ctx, f, p, in.IP, SignInUnavailable, nil, errors.New("sign-in settings changed or stopped working"))
	}
	secret, err := deps.Vault.Resolve(ctx, pc.secretRef, "", "sign-in:"+string(p))
	if err != nil {
		s.log.Warn("sign-in: client secret unavailable", "provider", p, "err", err)
		return nil, s.refuse(ctx, f, p, in.IP, SignInUnavailable, nil, err)
	}
	c := oauth.Client{HTTP: deps.HTTP, Now: s.now, Log: s.log}
	x := oauth.Exchange{Code: in.Code, Verifier: f.verifier, RedirectURI: f.redirect, ClientID: f.clientID, Secret: secret, Nonce: f.nonce}
	var id oauth.Identity
	if p == core.SignInGoogle {
		id, err = c.Google(ctx, pc.ep, x, pc.rules)
	} else {
		id, err = c.GitHub(ctx, pc.ep, x, pc.rules)
	}
	switch {
	case errors.Is(err, oauth.ErrNotAllowed):
		return nil, s.refuse(ctx, f, p, in.IP, SignInNotAllowed, &id, err)
	case errors.Is(err, oauth.ErrOrgAccess):
		return nil, s.refuse(ctx, f, p, in.IP, SignInOrgAccess, &id, err)
	case errors.Is(err, oauth.ErrRejected):
		return nil, s.refuse(ctx, f, p, in.IP, SignInExpired, nil, err)
	case err != nil:
		s.log.Warn("sign-in: provider check failed", "provider", p, "code", core.CodeOf(err), "err", err)
		return nil, s.refuse(ctx, f, p, in.IP, SignInUnavailable, nil, err)
	}
	if f.intent == IntentLink {
		return s.finishLink(ctx, f, p, in.IP, id)
	}
	return s.finishSignIn(ctx, f, pc, in.IP, id)
}

func (s *Service) finishSignIn(ctx context.Context, f *flow, pc providerConf, ip string, id oauth.Identity) (*SignInResult, error) {
	p := pc.p
	repo, err := s.signInRepo()
	if err != nil {
		return nil, err
	}
	link, err := repo.Get(ctx, p, id.Subject)
	var u *core.User
	created := false
	switch {
	case err == nil:
		if u, err = s.store.Users().Get(ctx, link.User); err != nil {
			return nil, err
		}
		if u.Disabled {
			return nil, s.refuse(ctx, f, p, ip, SignInDisabled, &id, nil)
		}
		if err := repo.Touch(ctx, p, id.Subject, s.now(), id.Email, id.Login); err != nil {
			return nil, err
		}
	case !errors.Is(err, core.ErrNotFound):
		return nil, err
	case !pc.allowNew:
		return nil, s.refuse(ctx, f, p, ip, SignInNoAccount, &id, nil)
	default:
		if u, created, err = s.createFromSignIn(ctx, p, id); err != nil {
			return nil, err
		}
		if u.Disabled {
			return nil, s.refuse(ctx, f, p, ip, SignInDisabled, &id, nil)
		}
	}
	cookie, a, err := s.startSession(ctx, u, ip)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"ip": ip, "method": string(p)}
	if created {
		detail["new"] = true
	}
	s.record(ctx, userTarget(u.ID), "auth.login", userTarget(u.ID), "ok", detail)
	return &SignInResult{Intent: IntentSignIn, Provider: p, Cookie: cookie, Actor: a, Next: f.next, Created: created}, nil
}

func (s *Service) finishLink(ctx context.Context, f *flow, p core.SignInProvider, ip string, id oauth.Identity) (*SignInResult, error) {
	now := s.now()
	sess, err := s.store.AuthSessions().Get(ctx, f.sessionHash)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil, s.refuse(ctx, f, p, ip, SignInSignedOut, &id, nil)
	case err != nil:
		return nil, err
	case sess.User != f.user || !now.Before(sess.IdleExpires) || !now.Before(sess.Expires):
		return nil, s.refuse(ctx, f, p, ip, SignInSignedOut, &id, nil)
	}
	u, err := s.store.Users().Get(ctx, f.user)
	if err != nil || u.Disabled {
		return nil, s.refuse(ctx, f, p, ip, SignInSignedOut, &id, err)
	}
	repo, err := s.signInRepo()
	if err != nil {
		return nil, err
	}
	existing, err := repo.Get(ctx, p, id.Subject)
	switch {
	case err == nil && existing.User == f.user:
		if err := repo.Touch(ctx, p, id.Subject, now, id.Email, id.Login); err != nil {
			return nil, err
		}
		existing.LastUsed, existing.Email, existing.Login = now, id.Email, id.Login
		return &SignInResult{Intent: IntentLink, Provider: p, Link: existing}, nil
	case err == nil:
		return nil, s.refuse(ctx, f, p, ip, SignInTaken, &id, nil)
	case !errors.Is(err, core.ErrNotFound):
		return nil, err
	}
	link := &core.SignIn{Provider: p, Subject: id.Subject, User: f.user, Email: id.Email, Login: id.Login, CreatedAt: now, LastUsed: now}
	if err := repo.Create(ctx, link); err != nil {
		if !errors.Is(err, core.ErrConflict) {
			return nil, err
		}
		// Either the person connected another account of p meanwhile, or
		// someone else took this one.
		code := SignInAlreadyLinked
		if other, gerr := repo.Get(ctx, p, id.Subject); gerr == nil && other.User != f.user {
			code = SignInTaken
		}
		return nil, s.refuse(ctx, f, p, ip, code, &id, err)
	}
	s.record(ctx, userTarget(f.user), "signin.link", userTarget(f.user), "ok", s.linkDetail(ip, p, id, ""))
	return &SignInResult{Intent: IntentLink, Provider: p, Link: link}, nil
}

func (s *Service) linkDetail(ip string, p core.SignInProvider, id oauth.Identity, reason string) map[string]any {
	d := map[string]any{"ip": ip, "provider": string(p)}
	if id.Email != "" {
		d["email"] = id.Email
	}
	if id.Login != "" {
		d["login"] = id.Login
	}
	if reason != "" {
		d["reason"] = reason
	}
	return d
}

// refuse audits a callback that ended without signing in or linking, and
// returns its SignInError. Audit rows name the provider account, never a
// code, state or token.
func (s *Service) refuse(ctx context.Context, f *flow, p core.SignInProvider, ip, code string, id *oauth.Identity, err error) error {
	se := &SignInError{Code: code, Intent: f.intent, Provider: p, Next: f.next, Err: err}
	if f.next == defaultNext {
		se.Next = ""
	}
	var who oauth.Identity
	if id != nil {
		who = *id
		se.Who = who.Email
		if p == core.SignInGitHub {
			se.Who = who.Login
		}
	}
	if f.intent == IntentLink {
		s.record(ctx, userTarget(f.user), "signin.link", userTarget(f.user), "denied", s.linkDetail(ip, p, who, code))
		return se
	}
	target := string(p)
	if se.Who != "" {
		target = string(p) + ":" + se.Who
	}
	if u := s.linkedUser(ctx, p, who.Subject); u != "" {
		target = userTarget(u)
	}
	s.record(ctx, "system", "auth.login_failed", target, "denied", map[string]any{"ip": ip, "method": string(p), "reason": code})
	return se
}

func (s *Service) linkedUser(ctx context.Context, p core.SignInProvider, subject string) core.UserID {
	repo, err := s.signInRepo()
	if err != nil || subject == "" {
		return ""
	}
	l, err := repo.Get(ctx, p, subject)
	if err != nil {
		return ""
	}
	return l.User
}

// Unlink disconnects p from by's account. The last way into an account
// without a password stays: an admin gives it a password first.
func (s *Service) Unlink(ctx context.Context, by core.Actor, p core.SignInProvider) error {
	if by.Via != "cookie" {
		return fmt.Errorf("%w: disconnecting a sign-in method needs a browser session", core.ErrForbidden)
	}
	repo, err := s.signInRepo()
	if err != nil {
		return err
	}
	u, err := s.store.Users().Get(ctx, by.User)
	if err != nil {
		return err
	}
	links, err := repo.ForUser(ctx, by.User)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(links, func(l *core.SignIn) bool { return l.Provider == p }) {
		return fmt.Errorf("%w: you haven't connected %s", core.ErrNotFound, p.Name())
	}
	if u.PWHash == noPassword && len(links) == 1 {
		return fmt.Errorf("%w: this is the only way you sign in; ask an admin for a password first", core.ErrConflict)
	}
	if err := repo.Delete(ctx, by.User, p); err != nil {
		return err
	}
	s.record(ctx, actorOf(by), "signin.unlink", userTarget(by.User), "ok", map[string]any{"provider": string(p)})
	return nil
}

// MySignIns is what Account → Sign-in methods shows: the providers u may
// connect now, u's links (including providers since turned off), and
// whether u has a password at all.
func (s *Service) MySignIns(ctx context.Context, u core.UserID) ([]core.SignInOption, []*core.SignIn, bool, error) {
	opts, err := s.SignInOptions(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	user, err := s.store.Users().Get(ctx, u)
	if err != nil {
		return nil, nil, false, err
	}
	links := []*core.SignIn{}
	if repo, err := s.signInRepo(); err == nil {
		if links, err = repo.ForUser(ctx, u); err != nil {
			return nil, nil, false, err
		}
	}
	return opts, links, user.PWHash != noPassword, nil
}

// SignInStatus is the admin view of each provider.
func (s *Service) SignInStatus(ctx context.Context) []core.SignInProviderStatus {
	out := make([]core.SignInProviderStatus, 0, len(core.SignInProviders))
	for _, p := range core.SignInProviders {
		pc := s.providerConf(ctx, p)
		st := core.SignInProviderStatus{Provider: p, Name: p.Name(), Enabled: pc.enabled, Ready: pc.ready(), Problem: pc.problem,
			RedirectURI: pc.redirect, ClientID: pc.clientID, Orgs: slices.Clone(pc.rules.Orgs)}
		if st.Orgs == nil {
			st.Orgs = []string{}
		}
		if pc.secret != nil {
			st.SecretSaved, st.SecretSource = true, pc.secret.Source
		}
		out = append(out, st)
	}
	return out
}

// CheckSignIn asks p whether it accepts a client id and secret, by trading
// a made-up code. An empty clientID or zero secret means the saved one.
func (s *Service) CheckSignIn(ctx context.Context, p core.SignInProvider, clientID string, secret core.Secret) error {
	if !validProvider(p) {
		return fmt.Errorf("%w: unknown sign-in provider", core.ErrNotFound)
	}
	deps := s.signIn.Load()
	if deps == nil {
		return fmt.Errorf("provider sign-in: %w", core.ErrNotImplemented)
	}
	pc := s.providerConf(ctx, p)
	if pc.epErr != nil {
		return pc.epErr
	}
	if clientID == "" {
		clientID = pc.clientID
	}
	if secret.IsZero() {
		if pc.secretRef == "" {
			return fmt.Errorf("%w: save a client secret first", core.ErrInvalid)
		}
		var err error
		if secret, err = deps.Vault.Resolve(ctx, pc.secretRef, "", "sign-in:"+string(p)+":check"); err != nil {
			return err
		}
	}
	if clientID == "" {
		return fmt.Errorf("%w: save a client ID first", core.ErrInvalid)
	}
	c := oauth.Client{HTTP: deps.HTTP, Now: s.now, Log: s.log}
	if p == core.SignInGoogle {
		return c.CheckGoogle(ctx, pc.ep, clientID, pc.redirect, secret)
	}
	return c.CheckGitHub(ctx, pc.ep, clientID, pc.redirect, secret)
}
