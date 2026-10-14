package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const (
	loginsPerMin = 10
	claimsPerMin = 5
)

func (s *Server) sandboxAvailable(ctx context.Context) bool {
	return s.d.Sandbox != nil && s.d.Sandbox.Status(ctx).Available
}

func (s *Server) me(ctx context.Context, a core.Actor) (*dto.Me, error) {
	u, err := s.d.Store.Users().Get(ctx, a.User)
	if err != nil {
		return nil, err
	}
	ps, err := s.d.Store.Principals().ForUser(ctx, a.User)
	if err != nil {
		return nil, err
	}
	m := &dto.Me{User: *u, Mode: s.cfg().Mode, Principals: nonNil(ps), Sandbox: s.sandboxAvailable(ctx), Banners: []core.BannerData{}}
	if a.Via == "cookie" {
		m.CSRF = a.CSRF
	}
	if ri, ok := ctx.Value(ctxKey{}).(*reqInfo); ok {
		m.Access = dto.Access{Peer: ri.class.String(), TLS: ri.secure}
	}
	if m.Mode == "" {
		m.Mode = "single"
	}
	if s.d.Runner != nil {
		m.Paused = s.d.Runner.Paused()
	}
	if s.d.Bus != nil {
		m.Banners = banners(s.d.Bus, filterFor(a))
	}
	return m, nil
}

// banners lists the active banners under f. The bus tracks both value and
// pointer payloads, so both are accepted here.
func banners(bus core.Bus, f core.Filter) []core.BannerData {
	out := []core.BannerData{}
	for _, e := range bus.Banners(f) {
		switch b := e.Data.(type) {
		case core.BannerData:
			out = append(out, b)
		case *core.BannerData:
			if b != nil {
				out = append(out, *b)
			}
		}
	}
	return out
}

func (s *Server) setupState(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	ctx := r.Context()
	need, err := s.d.Auth.NeedsSetup(ctx)
	if err != nil {
		return err
	}
	st := dto.SetupState{NeedsSetup: need, Sandbox: s.sandboxAvailable(ctx), Install: s.installKind()}
	if sg, has := s.d.Auth.(setupGrants); has {
		// A reload between claim and owner creation must not strand the
		// wizard: the grant cookie's holder gets its CSRF token back.
		if csrf, err := sg.SetupGrant(ctx, cookieValue(r, cookieSetup)); err == nil {
			st.Claimed, st.CSRF = true, csrf
		}
	}
	n, err := s.d.Store.Users().Count(ctx)
	if err != nil {
		return err
	}
	st.OwnerSet = n > 0
	return ok(w, st)
}

func (s *Server) setupClaim(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	if allowed, retry := s.claims.allow("claim", claimsPerMin, claimsPerMin); !allowed {
		e := newError(http.StatusTooManyRequests, "rate_limited", "too many setup claims")
		e.retry = retry
		return e
	}
	var req dto.SetupClaimReq
	if err := decode(r, &req); err != nil {
		return err
	}
	grant, err := s.d.Auth.ClaimSetup(r.Context(), req.Token)
	if err != nil {
		return err
	}
	sg, has := s.d.Auth.(setupGrants)
	if !has {
		return core.ErrNotImplemented
	}
	csrf, err := sg.SetupGrant(r.Context(), grant)
	if err != nil {
		return err
	}
	s.setSetupCookie(w, r, grant, 30*60)
	return ok(w, dto.SetupClaimResp{CSRF: csrf})
}

func (s *Server) setupOwner(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	var req dto.SetupOwnerReq
	if err := decode(r, &req); err != nil {
		return err
	}
	grant := cookieValue(r, cookieSetup)
	var cookie string
	var a core.Actor
	var err error
	if on, has := s.d.Auth.(ownerNamer); has {
		cookie, a, err = on.CreateOwnerNamed(r.Context(), grant, req.Username, req.DisplayName, req.Password, req.Timezone)
	} else {
		cookie, a, err = s.d.Auth.CreateOwner(r.Context(), grant, req.Username, req.Password, req.Timezone)
	}
	if err != nil {
		return err
	}
	s.setSessionCookie(w, r, cookie)
	s.setSetupCookie(w, r, "", -1)
	m, err := s.me(r.Context(), a)
	if err != nil {
		return err
	}
	return ok(w, m)
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if err := s.d.Auth.CompleteSetup(r.Context(), a); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	ip := clientIP(r)
	if allowed, retry := s.logins.allow(ip, loginsPerMin, loginsPerMin); !allowed {
		e := newError(http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts")
		e.retry = retry
		return e
	}
	var req dto.LoginReq
	if err := decode(r, &req); err != nil {
		return err
	}
	cookie, a, err := s.d.Auth.Login(r.Context(), req.Username, req.Password, ip)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, r, cookie)
	m, err := s.me(r.Context(), a)
	if err != nil {
		return err
	}
	return ok(w, m)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if a.Via == "cookie" {
		if err := s.d.Auth.Logout(r.Context(), cookieValue(r, s.cookieName(r))); err != nil {
			return err
		}
		s.clearSessionCookie(w, r)
	}
	return noContent(w)
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	m, err := s.me(r.Context(), a)
	if err != nil {
		return err
	}
	return ok(w, m)
}

// changePassword rotates the session: the auth service revokes every
// session of the user, then the new password signs this browser in again.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.PasswordReq
	if err := decode(r, &req); err != nil {
		return err
	}
	u, err := s.d.Store.Users().Get(r.Context(), a.User)
	if err != nil {
		return err
	}
	if err := s.d.Auth.SetPassword(r.Context(), a.User, req.Old, req.New); err != nil {
		return err
	}
	// Every session just ended; so do streams opened with them.
	s.sse.closeUser(a.User)
	cookie, _, err := s.d.Auth.Login(r.Context(), u.Username, req.New, clientIP(r))
	if err != nil {
		s.clearSessionCookie(w, r)
		return err
	}
	s.setSessionCookie(w, r, cookie)
	return noContent(w)
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ts, err := s.d.Store.Tokens().List(r.Context(), a.User)
	if err != nil {
		return err
	}
	return ok(w, nonNil(ts))
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var spec core.TokenSpec
	if err := decode(r, &spec); err != nil {
		return err
	}
	plain, rec, err := s.d.Auth.CreateToken(r.Context(), a, spec)
	if err != nil {
		return err
	}
	return ok(w, dto.TokenCreateResp{Token: plain, Meta: rec})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if err := s.d.Auth.RevokeToken(r.Context(), a, r.PathValue("id")); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, _ core.Actor) error {
	us, err := s.d.Store.Users().List(r.Context())
	if err != nil {
		return err
	}
	return ok(w, nonNil(us))
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if s.cfg().Mode != "multi" {
		return newError(http.StatusForbidden, "forbidden", "adding people needs team mode").
			withFix("set mode: multi in Settings → Advanced")
	}
	var req dto.UserCreateReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Password == "" {
		ot, has := s.d.Auth.(oneTimePasswords)
		if !has {
			return badRequest("password is required")
		}
		u, pw, err := ot.CreateUserOneTime(r.Context(), a, req.Username, req.DisplayName, req.Role)
		if err != nil {
			return err
		}
		return ok(w, dto.UserCreateResp{User: *u, OneTimePassword: pw})
	}
	u, err := s.d.Auth.CreateUser(r.Context(), a, req.Username, req.Password, req.DisplayName, req.Role)
	if err != nil {
		return err
	}
	return ok(w, dto.UserCreateResp{User: *u})
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.UserPatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	u, err := s.d.Store.Users().Get(ctx, core.UserID(r.PathValue("id")))
	if err != nil {
		return err
	}
	prevRole := u.Role
	if req.DisplayName != nil {
		u.DisplayName = *req.DisplayName
	}
	if req.Role != nil {
		u.Role = *req.Role
	}
	if req.Disabled != nil {
		u.Disabled = *req.Disabled
	}
	if err := s.d.Auth.UpdateUser(ctx, a, u); err != nil {
		return err
	}
	u, err = s.d.Store.Users().Get(ctx, u.ID)
	if err != nil {
		return err
	}
	// Open streams carry the filter of the old role (admin broadcasts).
	if u.Disabled || u.Role != prevRole {
		s.sse.closeUser(u.ID)
	}
	return ok(w, u)
}

// deleteUser disables the account (which revokes sessions and tokens) and,
// with ?purge=true, wipes the user's data; audit rows are kept.
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	purge, err := boolParam(r, "purge")
	if err != nil {
		return err
	}
	ctx := r.Context()
	u, err := s.d.Store.Users().Get(ctx, core.UserID(r.PathValue("id")))
	if err != nil {
		return err
	}
	switch {
	case u.Role == core.RoleOwner:
		return newError(http.StatusForbidden, "forbidden", "the owner cannot be deleted")
	case u.ID == a.User:
		return newError(http.StatusForbidden, "forbidden", "you cannot delete your own account")
	}
	if !u.Disabled {
		next := *u
		next.Disabled = true
		if err := s.d.Auth.UpdateUser(ctx, a, &next); err != nil {
			return err
		}
	}
	s.sse.closeUser(u.ID)
	if purge {
		if err := s.purgeUser(r, a, u); err != nil {
			return err
		}
	}
	return noContent(w)
}

func (s *Server) newPairing(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	pc, err := s.d.Auth.NewPairingCode(r.Context(), a)
	if err != nil {
		return err
	}
	if pc.DeepLinks == nil {
		pc.DeepLinks = map[string]string{}
	}
	if s.d.Hub != nil {
		raw := strings.ReplaceAll(pc.Code, "-", "")
		for _, st := range s.d.Hub.Status() {
			if st.State != "connected" {
				continue
			}
			if link := s.d.Hub.DeepLink(st.Name, raw); link != "" {
				pc.DeepLinks[st.Name] = link
			}
		}
	}
	return ok(w, pc)
}

func (s *Server) approvePairing(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.PairingApproveReq
	if err := decode(r, &req); err != nil {
		return err
	}
	id, err := s.d.Auth.ApproveSender(r.Context(), a, req.Code, req.As, req.UserID)
	if err != nil {
		return err
	}
	return ok(w, id)
}

func (s *Server) listIdentities(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	status := core.IdentityStatus(r.URL.Query().Get("status"))
	if a.Role.IsAdmin() && a.HasScope("admin") {
		ids, err := s.d.Store.Identities().List(ctx, status)
		if err != nil {
			return err
		}
		return ok(w, nonNil(ids))
	}
	ids, err := s.d.Store.Identities().ForUser(ctx, a.User)
	if err != nil {
		return err
	}
	if status != "" {
		ids = slices.DeleteFunc(ids, func(id *core.ChannelIdentity) bool { return id.Status != status })
	}
	return ok(w, nonNil(ids))
}

type identityEditor interface {
	SetIdentityStatus(ctx context.Context, by core.Actor, channel, externalID string, status core.IdentityStatus, u core.UserID) (*core.ChannelIdentity, error)
	UnlinkIdentity(ctx context.Context, by core.Actor, channel, externalID string) error
}

func (s *Server) patchIdentity(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ed, has := s.d.Auth.(identityEditor)
	if !has {
		return core.ErrNotImplemented
	}
	var req dto.IdentityPatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	id, err := ed.SetIdentityStatus(r.Context(), a, r.PathValue("channel"), r.PathValue("external"), req.Status, req.UserID)
	if err != nil {
		return err
	}
	return ok(w, id)
}

func (s *Server) deleteIdentity(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ed, has := s.d.Auth.(identityEditor)
	if !has {
		return core.ErrNotImplemented
	}
	if !a.HasScope("admin") {
		a.Role = core.RoleMember // an admin's chat token unlinks only its own identities
	}
	if err := ed.UnlinkIdentity(r.Context(), a, r.PathValue("channel"), r.PathValue("external")); err != nil {
		return err
	}
	return noContent(w)
}

// adminViewer covers every principal. It is used only for space and grant
// administration (metadata, never message content) by owners and admins.
func (s *Server) adminViewer(ctx context.Context, a core.Actor) (core.Viewer, error) {
	users, err := s.d.Store.Users().List(ctx)
	if err != nil {
		return core.Viewer{}, err
	}
	v := core.Viewer{User: a.User, Admin: true}
	for _, u := range users {
		ps, err := s.d.Store.Principals().ForUser(ctx, u.ID)
		if err != nil {
			return core.Viewer{}, err
		}
		for _, p := range ps {
			v.Principals = append(v.Principals, p.ID)
		}
	}
	return v, nil
}

func isAdmin(a core.Actor) bool { return a.Role.IsAdmin() && a.HasScope("admin") }

func (s *Server) listSpaces(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	v := a.Viewer
	if isAdmin(a) {
		av, err := s.adminViewer(r.Context(), a)
		if err != nil {
			return err
		}
		v = av
	}
	sps, err := s.d.Store.Spaces().List(r.Context(), v)
	if err != nil {
		return err
	}
	return ok(w, nonNil(sps))
}

func (s *Server) patchSpace(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.SpacePatchReq
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	v := a.Viewer
	if isAdmin(a) {
		av, err := s.adminViewer(ctx, a)
		if err != nil {
			return err
		}
		v = av
	}
	id := core.PrincipalID(r.PathValue("id"))
	sp, err := s.d.Store.Spaces().Get(ctx, v, id)
	if err != nil {
		return err
	}
	changed := map[string]any{}
	if req.Title != nil {
		t := strings.TrimSpace(*req.Title)
		if utf8.RuneCountInString(t) > 128 {
			return badRequest("title must be at most 128 characters")
		}
		sp.Title, changed["title"] = t, t
	}
	if req.Enabled != nil {
		sp.Enabled, changed["enabled"] = *req.Enabled, *req.Enabled
	}
	if req.MentionOnly != nil {
		sp.MentionOnly, changed["mention_only"] = *req.MentionOnly, *req.MentionOnly
	}
	if req.ToolProfile != nil {
		if !isAdmin(a) {
			return newError(http.StatusForbidden, "forbidden", "only admins can change a space's tool profile")
		}
		if *req.ToolProfile != "restricted" && *req.ToolProfile != "standard" {
			return badRequest("tool_profile must be restricted or standard")
		}
		p, err := s.d.Store.Principals().Get(ctx, v, id)
		if err != nil {
			return err
		}
		p.ToolProfile = *req.ToolProfile
		if err := s.d.Store.Principals().Update(ctx, p); err != nil {
			return err
		}
		changed["tool_profile"] = p.ToolProfile
	}
	sp.UpdatedAt = s.now()
	if err := s.d.Store.Spaces().Update(ctx, sp); err != nil {
		return err
	}
	s.audit(r, a, "space.update", "space:"+string(id), "ok", changed)
	sp, err = s.d.Store.Spaces().Get(ctx, v, id)
	if err != nil {
		return err
	}
	return ok(w, sp)
}
