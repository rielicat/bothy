package httpapi

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// githubAppRoutes are Bothy's own GitHub App, made from a manifest (SPEC
// §14.3): admin JSON routes, and the browser's round trip through GitHub,
// which answers with an HTML page or redirects, never JSON.
func (s *Server) githubAppRoutes() []route {
	return []route{
		{pattern: "GET /api/github/apps", access: accessAdmin, h: s.githubApps},
		{pattern: "POST /api/github/apps", access: accessAdminCookie, h: s.githubAppStart},
		{pattern: "POST /api/github/apps/{id}/check", access: accessAdmin, h: s.githubAppCheck},
		{pattern: "POST /api/github/apps/{id}/retire", access: accessAdmin, h: s.githubAppRetire},
		{pattern: "DELETE /api/github/apps/{id}", access: accessAdmin, h: s.githubAppDelete},
		{pattern: "GET /api/github/apps/handoff", access: accessNone, safe: true, h: s.githubAppHandoff},
		{pattern: "GET /api/github/apps/created", access: accessNone, safe: true, h: s.githubAppCreated},
		{pattern: "GET /api/github/apps/installed", access: accessNone, safe: true, h: s.githubAppInstalled},
	}
}

func (s *Server) ghApps() (core.GitHubAppMaker, error) {
	if s.d.GitHub == nil {
		return nil, errNoGitHub
	}
	mk, ok := s.d.GitHub.(core.GitHubAppMaker)
	if !ok {
		return nil, core.ErrNotImplemented
	}
	return mk, nil
}

func ghAppID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !core.ValidID(core.PrefixGitHubApp, id) {
		return "", errNotFound
	}
	return id, nil
}

// sessionResolver is the auth.Service extra that resumes a session from the
// sha256 of its cookie, the auth store's key (SPEC §8.1).
type sessionResolver interface {
	FromSessionHash(ctx context.Context, h []byte) (core.Actor, error)
}

// resumeAdmin finds the live session that started f: GitHub's redirects
// arrive without the Strict session cookie, so the flow kept its hash. It
// must still be the same active admin's.
func (s *Server) resumeAdmin(ctx context.Context, f ghAppFlow) (core.Actor, bool) {
	sr, ok := s.d.Auth.(sessionResolver)
	if !ok {
		return core.Actor{}, false
	}
	a, err := sr.FromSessionHash(ctx, f.session[:])
	if err != nil || a.User != f.user || !a.Role.IsAdmin() || a.MustChangePassword {
		return core.Actor{}, false
	}
	return a, true
}

func (s *Server) githubApps(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	mk, err := s.ghApps()
	if err != nil {
		return err
	}
	apps, err := mk.Apps(r.Context())
	if err != nil {
		return err
	}
	return ok(w, nonNil(apps))
}

func (s *Server) githubAppStart(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	mk, err := s.ghApps()
	if err != nil {
		return err
	}
	var req dto.GitHubAppStartReq
	if err := decode(r, &req); err != nil {
		return err
	}
	t := core.GitHubAppTarget{Name: strings.TrimSpace(req.Name)}
	switch req.Account {
	case "organization":
		if t.Org = strings.TrimSpace(req.Org); t.Org == "" {
			return badRequest("org is required for an organization")
		}
	case "personal":
	default:
		return badRequest("account must be organization or personal")
	}
	if s.d.Vault != nil && s.d.Vault.Status().Sealed {
		return core.ErrSealed
	}
	// The flow cookie is host-only and GitHub sends the browser back to
	// public_url: starting anywhere else would lose it.
	origin, same := s.publicOrigin(r)
	if !same {
		return newError(http.StatusConflict, "wrong_address", "open Bothy at "+origin+" to create the GitHub App").withFix(origin)
	}
	ctx := r.Context()
	if req.Replaces != "" {
		gh, err := s.gh()
		if err != nil {
			return err
		}
		if _, _, err := gh.Connection(ctx, req.Replaces); errors.Is(err, core.ErrNotFound) {
			return badRequest("that GitHub connection no longer exists")
		} else if err != nil {
			return err
		}
	}
	apps, err := mk.Apps(ctx)
	if err != nil {
		return err
	}
	for _, ap := range apps {
		if ap.State == "pending" {
			return newError(http.StatusConflict, "conflict", "finish installing "+ap.Name+" on GitHub, or start over")
		}
	}
	if u, err := s.d.Store.Users().Get(ctx, a.User); err == nil {
		t.Fallback = u.Username
	}
	h, err := mk.AppManifest(ctx, t, origin)
	if err != nil {
		return err
	}
	state, binding := randomToken(), randomToken()
	s.ghFlows.put(binding, &ghAppFlow{user: a.User, session: sha32(cookieValue(r, s.cookieName(r))), stateHash: sha32(state), state: state,
		phase: ghStarted, target: t, replaces: req.Replaces, handoff: h})
	s.setGHAppCookie(w, r, binding, int(ghFlowTTL.Seconds()))
	return ok(w, dto.GitHubAppStartResp{HandoffURL: "/api/github/apps/handoff"})
}

// land sends the browser back to the GitHub page with an outcome the web
// app turns into copy. The address never carries a code, state, id or
// error text; apps names whose GitHub Apps list to link, when known.
func land(w http.ResponseWriter, r *http.Request, outcome string, f *ghAppFlow) error {
	q := url.Values{"github_app": {outcome}}
	if f != nil && (outcome == "not_finished" || outcome == "exchange_failed" || outcome == "sealed") {
		q.Set("apps", cmp.Or(f.target.Org, "me"))
	}
	http.Redirect(w, r, "/settings/connections/github?"+q.Encode(), http.StatusSeeOther)
	return nil
}

var handoffPage = template.Must(template.New("handoff").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bothy: continue on GitHub</title>
<style nonce="{{.Nonce}}">:root{color-scheme:light dark;font:16px/1.5 system-ui,sans-serif}body{margin:0;padding:48px 16px;background:#fff;color:#1a1a1a}main{max-width:480px;margin:0 auto}h1{font-size:20px;font-weight:600;margin:0 0 8px}p{margin:0 0 16px}button{font:inherit;font-weight:500;min-height:40px;padding:0 16px;border:0;border-radius:6px;background:#1f7a4d;color:#fff;cursor:pointer}a{color:inherit}@media (prefers-color-scheme:dark){body{background:#161616;color:#ececec}}</style>
</head><body><main>
<h1>Continue on GitHub</h1>
<p>Bothy is taking you to GitHub to create {{.Name}} for {{.Where}}. Check the details there, then choose Create GitHub App.</p>
<form id="f" method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><button type="submit">Continue on GitHub</button></form>
<p><a href="/settings/connections/github">Back to Bothy</a></p>
</main>
<script nonce="{{.Nonce}}">var n=performance.getEntriesByType("navigation")[0];if(!n||n.type==="navigate")document.getElementById("f").submit();</script>
</body></html>
`))

// githubAppHandoff is the page that posts the manifest to GitHub. The SPA's
// policy allows forms only to Bothy itself, so this one page gets its own:
// scripts and styles by nonce, forms only to GitHub's web origin, and a
// Referrer-Policy that keeps the post's Origin header real (GitHub already
// has that origin in the manifest).
func (s *Server) githubAppHandoff(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	if _, err := s.ghApps(); err != nil || s.safeMode() {
		return land(w, r, "unavailable", nil)
	}
	f, found := s.ghFlows.get(cookieValue(r, s.ghAppCookieName(r)))
	cookie := cookieValue(r, s.cookieName(r))
	if !found || f.phase != ghStarted || cookie == "" || f.handoff == nil {
		return land(w, r, "expired", nil)
	}
	a, err := s.d.Auth.FromCookie(r.Context(), cookie)
	sh := sha32(cookie)
	if err != nil || a.User != f.user || subtle.ConstantTimeCompare(sh[:], f.session[:]) != 1 || !a.Role.IsAdmin() || a.MustChangePassword {
		return land(w, r, "expired", nil)
	}
	au, err := url.Parse(f.handoff.Action)
	if err != nil || au.Host == "" {
		return land(w, r, "unavailable", nil)
	}
	var nb [16]byte
	_, _ = rand.Read(nb[:])
	nonce := base64.RawURLEncoding.EncodeToString(nb[:])
	where := cmp.Or(f.handoff.Org, "your GitHub account")
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; form-action "+
		au.Scheme+"://"+au.Host+"; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "strict-origin")
	w.WriteHeader(http.StatusOK)
	return handoffPage.Execute(w, map[string]string{"Nonce": nonce, "Name": f.handoff.Name, "Where": where,
		"Action": f.handoff.Action + "?state=" + url.QueryEscape(f.state), "Manifest": f.handoff.Manifest})
}

// githubAppCreated is GitHub's redirect after it made the app. The code it
// carries redeems the app's private key without any authentication, so it
// is redeemed here, on the first request, and only for the browser and
// admin session that started the flow; it never reaches a page.
func (s *Server) githubAppCreated(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	mk, err := s.ghApps()
	if err != nil || s.safeMode() {
		return land(w, r, "unavailable", nil)
	}
	q := r.URL.Query()
	code, state := q.Get("code"), q.Get("state")
	binding := cookieValue(r, s.ghAppCookieName(r))
	if f, found := s.ghFlows.get(binding); found && f.phase == ghInstalling && code != "" {
		ch, sh := sha32(code), sha32(state)
		// Back from GitHub's install page to this redirect: send the browser
		// to the install page again rather than spending anything.
		if subtle.ConstantTimeCompare(ch[:], f.codeHash[:]) == 1 && subtle.ConstantTimeCompare(sh[:], f.prevState[:]) == 1 && f.installURL != "" {
			http.Redirect(w, r, f.installURL+"?state="+url.QueryEscape(f.state), http.StatusSeeOther)
			return nil
		}
	}
	var f ghAppFlow
	found := false
	if code != "" {
		f, found = s.ghFlows.advance(binding, ghStarted, ghExchanging, state, func(g *ghAppFlow) { g.codeHash = sha32(code) })
	}
	if !found {
		g, live := s.ghFlows.get(binding)
		switch {
		case !live:
			s.clearGHAppCookie(w, r)
		case (g.phase == ghExchanging || g.phase == ghInstalling) && g.gaveState(state):
			// This redirect again (a reload) while the first one redeems the
			// code, or since: the app is being made, so the page checks it.
			return land(w, r, "check", nil)
		}
		// A stale or forged address, or a flow another tab started since:
		// nothing is redeemed, and a flow still waiting keeps its cookie.
		return land(w, r, "not_finished", nil)
	}
	a, signedIn := s.resumeAdmin(r.Context(), f)
	if !signedIn {
		s.ghFlows.drop(binding)
		s.clearGHAppCookie(w, r)
		return land(w, r, "not_finished", &f)
	}
	app, err := mk.CreateApp(withOrigin(r, a, nil), code, f.target, f.replaces, a.User)
	if err != nil {
		s.ghFlows.drop(binding)
		s.clearGHAppCookie(w, r)
		if errors.Is(err, core.ErrSealed) {
			return land(w, r, "sealed", &f)
		}
		// CreateApp's errors never carry the code.
		s.log.Warn("github: creating the app failed", "request_id", infoOf(r).id, "err", s.redact(err.Error()))
		return land(w, r, "exchange_failed", &f)
	}
	next := randomToken()
	if _, found := s.ghFlows.advance(binding, ghExchanging, ghInstalling, state, func(g *ghAppFlow) {
		g.prevState, g.stateHash, g.state = g.stateHash, sha32(next), next
		g.app, g.installURL, g.expires = app.ID, app.InstallURL, s.ghFlows.fresh()
	}); !found {
		// Another start replaced the flow meanwhile; the app waits, pending.
		return land(w, r, "check", nil)
	}
	s.setGHAppCookie(w, r, binding, int(ghFlowTTL.Seconds()))
	http.Redirect(w, r, app.InstallURL+"?state="+url.QueryEscape(next), http.StatusSeeOther)
	return nil
}

// githubAppInstalled is GitHub's redirect after an installation, or after
// its repository access changed. installation_id is whatever the address
// says, so it is used only for the flow that made the app, and the service
// verifies it with the app's own JWT. Without such a flow it only lands:
// the GitHub page then checks the app with the admin's own session.
func (s *Server) githubAppInstalled(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	mk, err := s.ghApps()
	if err != nil || s.safeMode() {
		return land(w, r, "unavailable", nil)
	}
	q := r.URL.Query()
	action := q.Get("setup_action")
	binding := cookieValue(r, s.ghAppCookieName(r))
	f, found := s.ghFlows.advance(binding, ghInstalling, ghDone, q.Get("state"), nil)
	if !found {
		switch action {
		case "update":
			return land(w, r, "updated", nil)
		case "request":
			return land(w, r, "requested", nil)
		}
		return land(w, r, "check", nil)
	}
	s.ghFlows.drop(binding)
	s.clearGHAppCookie(w, r)
	a, signedIn := s.resumeAdmin(r.Context(), f)
	switch {
	case !signedIn:
		return land(w, r, "check", nil)
	case action == "request":
		return land(w, r, "requested", nil)
	}
	id, err := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	if err != nil || id <= 0 {
		return land(w, r, "check", nil)
	}
	if _, err := mk.InstallApp(withOrigin(r, a, nil), f.app, id, a.User); err != nil {
		s.log.Warn("github: connecting the app's installation failed", "request_id", infoOf(r).id, "err", s.redact(err.Error()))
		return land(w, r, "install_failed", nil)
	}
	return land(w, r, "installed", nil)
}

func (s *Server) githubAppCheck(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	mk, err := s.ghApps()
	if err != nil {
		return err
	}
	id, err := ghAppID(r)
	if err != nil {
		return err
	}
	conn, err := mk.CheckApp(withOrigin(r, a, nil), id, a.User)
	if err != nil {
		return err
	}
	return ok(w, dto.GitHubAppCheck{Installed: conn != nil, Connection: conn})
}

func (s *Server) githubAppRetire(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	mk, err := s.ghApps()
	if err != nil {
		return err
	}
	id, err := ghAppID(r)
	if err != nil {
		return err
	}
	app, err := mk.RetireApp(withOrigin(r, a, nil), id, a.User)
	if err != nil {
		return err
	}
	return ok(w, app)
}

func (s *Server) githubAppDelete(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	mk, err := s.ghApps()
	if err != nil {
		return err
	}
	id, err := ghAppID(r)
	if err != nil {
		return err
	}
	if err := mk.DeleteApp(r.Context(), id, a.User); err != nil {
		return err
	}
	return noContent(w)
}
