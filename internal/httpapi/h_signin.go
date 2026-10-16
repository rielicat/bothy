package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// signInsPerMin is per client (an IPv4 address or an IPv6 /64) and separate
// from the password limiter: a start costs a provider round trip, not a guess.
const signInsPerMin = 20

// signInRoutes are sign in with Google and GitHub (SPEC §15).
func (s *Server) signInRoutes() []route {
	return []route{
		{pattern: "GET /api/auth/methods", access: accessNone, safe: true, h: s.authMethods},
		{pattern: "POST /api/auth/oauth/{provider}/start", access: accessNone, h: s.signInStart},
		{pattern: "GET /api/auth/oauth/{provider}/callback", access: accessNone, safe: true, h: s.signInCallback},
		{pattern: "GET /api/me/sign-ins", access: accessUserCookie, h: s.mySignIns},
		{pattern: "POST /api/me/sign-ins/{provider}", access: accessUserCookie, h: s.signInLink},
		{pattern: "DELETE /api/me/sign-ins/{provider}", access: accessUserCookie, h: s.unlinkSignIn},
		{pattern: "GET /api/sign-in", access: accessAdmin, h: s.signInAdmin},
		{pattern: "PATCH /api/sign-in", access: accessAdmin, h: s.patchSignInList},
		{pattern: "PATCH /api/sign-in/{provider}", access: accessAdmin, h: s.patchSignIn},
		{pattern: "POST /api/sign-in/{provider}/check", access: accessAdmin, h: s.checkSignIn},
	}
}

// signInAuth is the auth.Service extra behind these routes.
type signInAuth interface {
	SignInOptions(ctx context.Context) ([]core.SignInOption, error)
	StartSignIn(ctx context.Context, p core.SignInProvider, next, client string) (auth.SignInStart, error)
	StartLink(ctx context.Context, by core.Actor, sessionCookie string, p core.SignInProvider, client string) (auth.SignInStart, error)
	FinishSignIn(ctx context.Context, p core.SignInProvider, in auth.SignInCallback) (*auth.SignInResult, error)
	Unlink(ctx context.Context, by core.Actor, p core.SignInProvider) error
	MySignIns(ctx context.Context, u core.UserID) ([]core.SignInOption, []*core.SignIn, bool, error)
	SignInStatus(ctx context.Context) []core.SignInProviderStatus
	CheckSignIn(ctx context.Context, p core.SignInProvider, clientID string, secret core.Secret) error
	PinGitHubOrgs(ctx context.Context, orgs []string) (map[string]int64, error)
}

func (s *Server) signInSvc() (signInAuth, error) {
	svc, ok := s.d.Auth.(signInAuth)
	if !ok {
		return nil, core.ErrNotImplemented
	}
	return svc, nil
}

func parseProvider(raw string) (core.SignInProvider, bool) {
	for _, p := range core.SignInProviders {
		if raw == string(p) {
			return p, true
		}
	}
	return "", false
}

func (s *Server) authMethods(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	out := dto.SignInMethods{Providers: []core.SignInOption{}}
	svc, err := s.signInSvc()
	if err != nil || s.safeMode() {
		return ok(w, out)
	}
	opts, err := svc.SignInOptions(r.Context())
	if err != nil {
		return err
	}
	out.Providers = nonNil(opts)
	return ok(w, out)
}

// sameAddress refuses a start from an address other than server.public_url:
// the provider sends the browser back to public_url, where the sign-in
// cookie set here would be missing.
func (s *Server) sameAddress(r *http.Request, p core.SignInProvider) error {
	origin, problem := config.SignInAddress(s.cfg().Server.PublicURL)
	if problem != "" {
		return nil // the start itself reports the provider unavailable
	}
	norm := func(hostport string, secure bool) hostEntry {
		h := splitHost(hostport)
		if secure && h.port == "443" || !secure && h.port == "80" {
			h.port = ""
		}
		return h
	}
	u, err := url.Parse(origin)
	secure := err == nil && u.Scheme == "https"
	if err == nil && norm(u.Host, secure) == norm(r.Host, infoOf(r).secure) && infoOf(r).secure == secure {
		return nil
	}
	return newError(http.StatusConflict, "wrong_address", "open Bothy at "+origin+" to sign in with "+p.Name()).withFix(origin)
}

func (s *Server) signInLimited(r *http.Request) error {
	if allowed, retry := s.starts.allow(clientKey(r), signInsPerMin, signInsPerMin); !allowed {
		e := newError(http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts")
		e.retry = retry
		return e
	}
	return nil
}

func signInUnavailable(p core.SignInProvider) error {
	return newError(http.StatusConflict, "sign_in_unavailable", p.Name()+" sign-in isn't available right now")
}

func (s *Server) signInStart(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	p, found := parseProvider(r.PathValue("provider"))
	if !found {
		return errNotFound
	}
	svc, err := s.signInSvc()
	if err != nil {
		return err
	}
	if err := s.signInLimited(r); err != nil {
		return err
	}
	if err := s.sameAddress(r, p); err != nil {
		return err
	}
	var req dto.SignInStartReq
	if err := decode(r, &req); err != nil {
		return err
	}
	st, err := svc.StartSignIn(r.Context(), p, req.Next, clientKey(r))
	var se *auth.SignInError
	if errors.As(err, &se) {
		return signInUnavailable(p)
	}
	if err != nil {
		return err
	}
	s.setSignInCookie(w, r, st.State, int(st.MaxAge.Seconds()))
	return ok(w, dto.SignInStartResp{RedirectURL: st.URL})
}

// signInCallback always answers 303 to a fixed path of the web app, so a
// provider's redirect never shows a JSON error and the code and state never
// reach a page or the history. The sign-in cookie is cleared either way. A
// refused account is named in the refusal cookie, never in the address.
func (s *Server) signInCallback(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	raw := r.PathValue("provider")
	q := r.URL.Query()
	errCode := q.Get("error")
	if len(errCode) > 64 {
		errCode = errCode[:64]
	}
	in := auth.SignInCallback{CookieState: cookieValue(r, s.signInCookieName(r)), State: q.Get("state"), Code: q.Get("code"),
		Error: errCode, IP: clientIP(r)}
	s.clearSignInCookie(w, r)
	target := "/login?" + url.Values{"signin_error": {auth.SignInUnavailable}}.Encode()
	refused := ""
	p, found := parseProvider(raw)
	svc, err := s.signInSvc()
	if found && err == nil && !s.safeMode() {
		target = "/login?" + url.Values{"signin_error": {auth.SignInUnavailable}, "provider": {string(p)}}.Encode()
		res, err := svc.FinishSignIn(r.Context(), p, in)
		var se *auth.SignInError
		switch {
		case errors.As(err, &se):
			v := url.Values{"signin_error": {se.Code}, "provider": {string(p)}}
			if se.Code == auth.SignInNotAllowed {
				refused = se.Who
			}
			if se.Intent == auth.IntentLink {
				target = "/settings/account?" + v.Encode()
				break
			}
			if se.Next != "" {
				v.Set("next", se.Next)
			}
			target = "/login?" + v.Encode()
		case err != nil:
			// FinishSignIn errors never carry a code or token.
			s.log.Error("provider sign-in failed", "provider", p, "request_id", infoOf(r).id, "err", err)
		case res.Intent == auth.IntentLink:
			target = "/settings/account?" + url.Values{"connected": {string(p)}}.Encode()
		default:
			s.setSessionCookie(w, r, res.Cookie)
			target = res.Next
		}
	}
	switch {
	case refused != "":
		s.setRefusedCookie(w, r, refused, refusedMaxAge)
	case cookieValue(r, s.refusedCookieName(r)) != "":
		s.setRefusedCookie(w, r, "", -1)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
	return nil
}

func (s *Server) mySignIns(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	svc, err := s.signInSvc()
	if err != nil {
		return err
	}
	opts, links, hasPassword, err := svc.MySignIns(r.Context(), a.User)
	if err != nil {
		return err
	}
	return ok(w, dto.MySignIns{Providers: nonNil(opts), Linked: nonNil(links), HasPassword: hasPassword})
}

func (s *Server) signInLink(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, found := parseProvider(r.PathValue("provider"))
	if !found {
		return errNotFound
	}
	svc, err := s.signInSvc()
	if err != nil {
		return err
	}
	if err := s.signInLimited(r); err != nil {
		return err
	}
	if err := s.sameAddress(r, p); err != nil {
		return err
	}
	st, err := svc.StartLink(r.Context(), a, cookieValue(r, s.cookieName(r)), p, clientKey(r))
	var se *auth.SignInError
	if errors.As(err, &se) {
		return signInUnavailable(p)
	}
	if err != nil {
		return err
	}
	s.setSignInCookie(w, r, st.State, int(st.MaxAge.Seconds()))
	return ok(w, dto.SignInStartResp{RedirectURL: st.URL})
}

func (s *Server) unlinkSignIn(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, found := parseProvider(r.PathValue("provider"))
	if !found {
		return errNotFound
	}
	svc, err := s.signInSvc()
	if err != nil {
		return err
	}
	if err := svc.Unlink(r.Context(), a, p); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) signInAdmin(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	svc, err := s.signInSvc()
	if err != nil {
		return err
	}
	cfg := s.cfg()
	_, problem := config.SignInAddress(cfg.Server.PublicURL)
	return ok(w, dto.SignInAdmin{PublicURL: cfg.Server.PublicURL, Address: cmpOr(problem, "ok"), TeamMode: cfg.Mode == "multi",
		Allow: nonNil(cfg.Auth.SignIn.Allow), AllowNew: cfg.Auth.SignIn.AllowNew, Providers: svc.SignInStatus(r.Context())})
}

// normalizeAllow trims and lower-cases sign-in list entries, reads "@acme.com"
// as the domain, and drops empties and repeats. Validation is the config's.
func normalizeAllow(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if rest, ok := strings.CutPrefix(e, "@"); ok && !strings.Contains(rest, "@") {
			e = rest
		}
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

func normalizeOrgs(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, o := range in {
		o = strings.TrimSpace(o)
		if k := strings.ToLower(o); o != "" && !seen[k] {
			seen[k] = true
			out = append(out, o)
		}
	}
	return out
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (s *Server) patchSignInList(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SignInListPatch
	if err := decode(r, &req); err != nil {
		return err
	}
	changes := map[string]json.RawMessage{}
	if req.Allow != nil {
		changes["auth.sign_in.allow"] = rawJSON(normalizeAllow(*req.Allow))
	}
	if req.AllowNew != nil {
		changes["auth.sign_in.allow_new"] = rawJSON(*req.AllowNew)
	}
	if len(changes) == 0 {
		return badRequest("nothing to change")
	}
	if _, err := s.d.Config.Set(withOrigin(r, a, nil), changes, byString(a)); err != nil {
		return err
	}
	return s.signInAdmin(w, r, a)
}

func (s *Server) patchSignIn(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, found := parseProvider(r.PathValue("provider"))
	if !found {
		return errNotFound
	}
	var req dto.SignInPatch
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Orgs != nil && p != core.SignInGitHub {
		return badRequest("organizations apply to GitHub sign-in only")
	}
	base := "auth.sign_in." + string(p)
	changes := map[string]json.RawMessage{}
	if req.Enabled != nil {
		changes[base+".enabled"] = rawJSON(*req.Enabled)
	}
	if req.ClientID != nil {
		changes[base+".client_id"] = rawJSON(strings.TrimSpace(*req.ClientID))
	}
	if req.Orgs != nil {
		orgs := normalizeOrgs(*req.Orgs)
		ids, err := s.pinOrgs(r.Context(), orgs)
		if err != nil {
			return err
		}
		changes[base+".orgs"] = rawJSON(orgs)
		changes[base+".org_ids"] = rawJSON(ids)
	}
	var undo func()
	if secret := strings.TrimSpace(req.ClientSecret); secret != "" {
		if len(secret) > 4096 || strings.ContainsFunc(secret, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return badRequest("client_secret must be at most 4096 printable characters")
		}
		meta, u, err := s.saveGlobalKey(r, a, string(p)+"_sign_in_client_secret", []byte(secret), "", "sign-in:"+string(p))
		if err != nil {
			return err
		}
		undo = u
		changes[base+".client_secret"] = rawJSON("secret:" + meta.Name)
	}
	if len(changes) == 0 {
		return badRequest("nothing to change")
	}
	// ServerWrite lets this save write org_ids, which the generic config
	// API refuses.
	if _, err := s.d.Config.Set(config.ServerWrite(withOrigin(r, a, nil)), changes, byString(a)); err != nil {
		if undo != nil {
			undo()
		}
		return err
	}
	return s.signInAdmin(w, r, a)
}

// pinOrgs returns the GitHub ids of the organizations being saved: the
// ones already pinned, and the others looked up. Names that can't be
// organizations skip the lookup: config validation refuses them with its
// own message.
func (s *Server) pinOrgs(ctx context.Context, orgs []string) (map[string]int64, error) {
	if len(orgs) > config.MaxSignInOrgs || slices.ContainsFunc(orgs, func(o string) bool { return !config.ValidGitHubOrg(o) }) {
		return map[string]int64{}, nil
	}
	svc, err := s.signInSvc()
	if err != nil {
		return nil, err
	}
	ids, err := svc.PinGitHubOrgs(ctx, orgs)
	var fe config.FieldErrors
	switch {
	case err == nil:
		return ids, nil
	case errors.As(err, &fe), errors.Is(err, core.ErrNotImplemented):
		return nil, err
	}
	s.log.Warn("sign-in: looking up GitHub organizations failed", "err", err)
	return nil, newError(http.StatusBadGateway, "provider_error", "Bothy couldn't reach GitHub to check the organizations; try again")
}

func (s *Server) checkSignIn(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	p, found := parseProvider(r.PathValue("provider"))
	if !found {
		return errNotFound
	}
	svc, err := s.signInSvc()
	if err != nil {
		return err
	}
	var req dto.SignInCheckReq
	if err := decode(r, &req); err != nil {
		return err
	}
	var secret core.Secret
	if v := strings.TrimSpace(req.ClientSecret); v != "" {
		secret = core.NewSecret([]byte(v))
	}
	err = svc.CheckSignIn(r.Context(), p, strings.TrimSpace(req.ClientID), secret)
	switch {
	case err == nil:
		return ok(w, dto.SignInCheck{OK: true})
	case errors.Is(err, core.ErrNotFound):
		return badRequest("the saved client secret is missing; enter it again and save")
	case errors.Is(err, core.ErrInvalid), errors.Is(err, core.ErrSealed), errors.Is(err, core.ErrNotImplemented):
		return err
	}
	return ok(w, dto.SignInCheck{OK: false, Code: cmp.Or(core.CodeOf(err), core.FailUnknown), Error: s.redact(err.Error())})
}
