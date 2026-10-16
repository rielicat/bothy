package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// onboardingRoutes are the v0.2 setup, models and people endpoints the
// redesigned UI uses (docs/design/ui.md §17 S6, S9).
func (s *Server) onboardingRoutes() []route {
	return []route{
		{pattern: "POST /api/providers/connect", access: accessAdmin, h: s.connectProvider},
		// Cookie-only: a one-time password hands over the account, which an
		// API token must not be able to do.
		{pattern: "POST /api/users/{id}/password", access: accessAdminCookie, h: s.resetUserPassword},
	}
}

// ownerNamer, oneTimePasswords and passwordResetter are auth.Service extras
// outside core.Auth.
type ownerNamer interface {
	CreateOwnerNamed(ctx context.Context, grantCookie, username, displayName, password, timezone string) (string, core.Actor, error)
}

type oneTimePasswords interface {
	CreateUserOneTime(ctx context.Context, by core.Actor, username, displayName string, role core.Role) (*core.User, string, error)
}

type passwordResetter interface {
	ResetUserPassword(ctx context.Context, by core.Actor, u core.UserID) (string, error)
}

// passwordChangeRoutes are all an account with a one-time password may use
// until it has chosen its own.
var passwordChangeRoutes = map[string]bool{
	"GET /api/auth/me":        true,
	"POST /api/auth/password": true,
	"POST /api/auth/logout":   true,
}

var errPasswordChange = newError(http.StatusForbidden, "password_change_required", "choose a new password first").withFix("/password")

// installKind says how this server runs, so setup shows the command that
// prints a new setup code: compose, docker, binary, or unknown on
// orchestrators where none of those commands applies.
func (s *Server) installKind() dto.InstallKind {
	switch {
	case s.getenv("KUBERNETES_SERVICE_HOST") != "", s.getenv("ECS_CONTAINER_METADATA_URI_V4") != "",
		s.getenv("ECS_CONTAINER_METADATA_URI") != "":
		return "unknown"
	case s.getenv("BOTHY_DEPLOYMENT") != "container":
		return "binary"
	// Only the compose file pairs the image with a sandboxd service.
	case s.cfg().Sandbox.Backend == "sandboxd":
		return "compose"
	}
	return "docker"
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	pr, has := s.d.Auth.(passwordResetter)
	if !has {
		return core.ErrNotImplemented
	}
	ctx := r.Context()
	u, err := s.d.Store.Users().Get(ctx, core.UserID(r.PathValue("id")))
	if err != nil {
		return err
	}
	pw, err := pr.ResetUserPassword(ctx, a, u.ID)
	if err != nil {
		return err
	}
	// Every session of the account just ended; so do its streams.
	s.sse.closeUser(u.ID)
	return ok(w, dto.PasswordResetResp{OneTimePassword: pw})
}

// checkProbeInput validates the provider fields POST /api/providers/test and
// /connect share.
func checkProbeInput(kind core.ProviderKind, api, region, profile string, hasKey bool) error {
	switch kind {
	case core.KindAnthropic, core.KindOpenAI, core.KindGemini, core.KindOllama, core.KindOpenAICompat, core.KindBedrock:
	default:
		return badRequest("unknown provider kind %q", kind)
	}
	switch api {
	case "", "auto", "responses", "chat":
	default:
		return badRequest("api must be auto, responses or chat")
	}
	switch {
	case kind != core.KindBedrock && (region != "" || profile != ""):
		return badRequest("region and profile apply to kind bedrock only")
	case kind == core.KindBedrock && hasKey:
		return badRequest("bedrock uses the server's AWS credentials; send no api_key or secret_ref")
	case region != "" && !config.ValidAWSRegion(region):
		return badRequest("region %q is not an AWS region name", region)
	case profile != "" && !config.ValidAWSProfile(profile):
		return badRequest("invalid AWS profile name")
	}
	return nil
}

var providerIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// cloudCompat are the OpenAI-compatible services with their own provider id
// and a catalog entry, so their models need no discovery.
var cloudCompat = map[string]string{
	"openrouter.ai":    "openrouter",
	"api.groq.com":     "groq",
	"api.x.ai":         "xai",
	"api.deepseek.com": "deepseek",
}

// defaultProviderID names a provider the way the setup tiles do.
func defaultProviderID(kind core.ProviderKind, base string) string {
	if kind != core.KindOpenAICompat {
		return string(kind)
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "custom"
	}
	if id, ok := cloudCompat[strings.ToLower(u.Hostname())]; ok {
		return id
	}
	switch u.Port() {
	case "1234":
		return "lmstudio"
	case "8000":
		return "vllm"
	}
	return "custom"
}

// freeProviderID is id unless another provider already has it: connecting
// a second server gets "custom-2" instead of rewriting the first, whose
// models the slots may still name. The same provider (a vendor of the same
// kind, a server at the same address) keeps its id, so connecting it again
// replaces its key or region.
func freeProviderID(have []config.Provider, id string, kind core.ProviderKind, base string) string {
	same := func(p config.Provider) bool {
		if p.Type != string(kind) {
			return false
		}
		if kind != core.KindOllama && kind != core.KindOpenAICompat {
			return true
		}
		return strings.TrimRight(p.BaseURL, "/") == strings.TrimRight(base, "/")
	}
	for n := 1; ; n++ {
		cand := id
		if n > 1 {
			cand = fmt.Sprintf("%s-%d", id[:min(len(id), 60)], n)
		}
		i := slices.IndexFunc(have, func(p config.Provider) bool { return p.ID == cand })
		if i < 0 || same(have[i]) {
			return cand
		}
	}
}

// discoverByDefault: local servers, Bedrock (which models the account may
// invoke) and unknown compatible servers list their models; the vendors'
// come from the catalog.
func discoverByDefault(kind core.ProviderKind, base string) bool {
	switch kind {
	case core.KindOllama, core.KindBedrock:
		return true
	case core.KindOpenAICompat:
		u, err := url.Parse(strings.TrimSpace(base))
		if err != nil {
			return true
		}
		_, cloud := cloudCompat[strings.ToLower(u.Hostname())]
		return !cloud
	}
	return false
}

// connectProvider is setup's single save: it stores or picks the key, writes
// the provider and puts the chosen models first in both slots. The config
// change is one atomic write; a key it saved is taken back if that write is
// refused.
func (s *Server) connectProvider(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req dto.ProviderConnectReq
	if err := decode(r, &req); err != nil {
		return err
	}
	keys := 0
	for _, set := range []bool{req.APIKey != "", req.SecretRef != "", req.EnvVar != ""} {
		if set {
			keys++
		}
	}
	base := strings.TrimSpace(req.BaseURL)
	switch err := checkProbeInput(req.Kind, req.API, req.Region, req.Profile, keys > 0); {
	case err != nil:
		return err
	case keys > 1:
		return badRequest("give one of api_key, secret_ref or env_var")
	case req.Kind == core.KindBedrock && req.Region == "":
		return badRequest("region is required for bedrock")
	case req.Kind == core.KindOpenAICompat && base == "":
		return badRequest("base_url is required for openai_compat")
	case len(req.APIKey) > maxSecretValue:
		return badRequest("api_key must be at most 64 KB")
	case req.QuickModel != "" && req.Model == "":
		return badRequest("quick_model needs model")
	case strings.ContainsFunc(req.Model+req.QuickModel, func(c rune) bool { return c < ' ' || c == 0x7f }):
		return badRequest("model ids must not contain control characters")
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = freeProviderID(s.cfg().Providers, defaultProviderID(req.Kind, base), req.Kind, base)
	}
	if !providerIDRE.MatchString(id) {
		return badRequest("id must be 1-64 of a-z, 0-9, '_' or '-', starting with a letter or digit")
	}
	ctx := r.Context()
	p := map[string]any{"id": id, "type": string(req.Kind), "base_url": base, "allow_private": req.AllowPrivate,
		"discover": discoverByDefault(req.Kind, base)}
	if req.API != "" {
		p["api"] = req.API
	}
	if req.Kind == core.KindBedrock {
		p["region"] = req.Region
		if req.Profile != "" {
			p["profile"] = req.Profile
		}
	}
	resp := dto.ProviderConnectResp{Provider: id}
	var undo func()
	switch {
	case strings.HasPrefix(req.SecretRef, config.AWSSecretPrefix):
		ref, err := config.ParseAWSSecretRef(req.SecretRef)
		if err != nil {
			return badRequest("secret_ref: %s", err.Error())
		}
		// Bothy reads it with the server's AWS credentials when the provider
		// first needs it; the config write below checks the region.
		resp.Secret, p["api_key"] = &core.SecretMeta{Scope: core.GlobalScope, Name: ref.String(), Source: "aws-sm"}, ref.String()
	case req.SecretRef != "":
		name, found := strings.CutPrefix(req.SecretRef, "secret:")
		if !found || !secretNameRE.MatchString(name) {
			return badRequest("secret_ref must be secret:<name> or aws-sm:<secret>")
		}
		m, err := s.savedSecret(ctx, name)
		if err != nil {
			return err
		}
		resp.Secret, p["api_key"] = m, req.SecretRef
	case keys == 1:
		val, source := req.APIKey, ""
		if req.EnvVar != "" {
			keyVars := slices.DeleteFunc(slices.Clone(wellKnownEnv), func(v string) bool { return !strings.HasSuffix(v, "_API_KEY") })
			if !slices.Contains(keyVars, req.EnvVar) {
				return badRequest("env_var must be one of %s", strings.Join(keyVars, ", "))
			}
			if val = s.getenv(req.EnvVar); val == "" {
				return newError(http.StatusNotFound, "not_found", req.EnvVar+" is not set in the gateway's environment")
			}
			source = "env:" + req.EnvVar
		}
		m, u, err := s.saveGlobalKey(r, a, id[:min(len(id), 50)]+"_api_key", []byte(val), source, "provider:connect")
		if err != nil {
			return err
		}
		resp.Secret, undo, p["api_key"] = m, u, "secret:"+m.Name
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	changes := map[string]json.RawMessage{"providers." + id: raw}
	if req.Model != "" {
		cfg := s.cfg()
		resp.Main, resp.Fast = id+"/"+req.Model, id+"/"+cmp.Or(req.QuickModel, req.Model)
		for path, list := range map[string][]string{"models.main": front(resp.Main, cfg.Models.Main), "models.fast": front(resp.Fast, cfg.Models.Fast)} {
			if changes[path], err = json.Marshal(list); err != nil {
				return err
			}
		}
	}
	restart, err := s.d.Config.Set(ctx, changes, byString(a))
	if err != nil {
		if undo != nil {
			undo()
		}
		return err
	}
	resp.RestartRequired = nonNil(restart)
	s.audit(r, a, "provider.connect", "provider:"+id, "ok", map[string]any{"kind": req.Kind, "main": resp.Main, "fast": resp.Fast})
	return ok(w, resp)
}

// front puts ref first in a model chain and keeps the rest as backups.
func front(ref string, chain []string) []string {
	out := []string{ref}
	for _, c := range chain {
		if c != ref {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) savedSecret(ctx context.Context, name string) (*core.SecretMeta, error) {
	metas, err := s.d.Vault.List(ctx, core.GlobalScope)
	if err != nil {
		return nil, err
	}
	for i := range metas {
		if metas[i].Name == name {
			return &metas[i], nil
		}
	}
	return nil, badRequest("there is no saved key named %s", name)
}

// saveGlobalKey stores val as base ("<id>_api_key",
// "google_sign_in_client_secret") in the global vault and returns how to
// take the write back. A name already set by a BOTHY_SECRET_ variable would
// shadow the new value, so the next free "<base>_<n>" is used instead.
// purpose labels the read of the old value that undo restores.
func (s *Server) saveGlobalKey(r *http.Request, a core.Actor, base string, val []byte, source, purpose string) (*core.SecretMeta, func(), error) {
	var detail map[string]any
	if source != "" {
		detail = map[string]any{"source": source}
	}
	ctx := withOrigin(r, a, detail)
	metas, err := s.d.Vault.List(ctx, core.GlobalScope)
	if err != nil {
		return nil, nil, err
	}
	find := func(name string) *core.SecretMeta {
		for i := range metas {
			if metas[i].Name == name {
				return &metas[i]
			}
		}
		return nil
	}
	name := base
	prev := find(name)
	for n := 2; prev != nil && prev.Source == "env"; n++ {
		if n > 99 {
			return nil, nil, newError(http.StatusConflict, "conflict", "no free name to save this key under")
		}
		name = fmt.Sprintf("%s_%d", base, n)
		prev = find(name)
	}
	var old core.Secret
	if prev != nil {
		if old, err = s.d.Vault.Get(ctx, core.GlobalScope, name, purpose); err != nil && !errors.Is(err, core.ErrNotFound) {
			return nil, nil, err
		}
	}
	meta, err := s.d.Vault.Put(ctx, core.GlobalScope, name, val, a.User)
	if err != nil {
		return nil, nil, err
	}
	undo := func() {
		ctx := context.WithoutCancel(ctx)
		var err error
		if !old.IsZero() {
			_, err = s.d.Vault.Put(ctx, core.GlobalScope, name, old.Bytes(), a.User)
		} else {
			err = s.d.Vault.Delete(ctx, core.GlobalScope, name, a.User)
		}
		if err != nil {
			s.log.Error("take back a saved key after a refused config change", "name", name, "err", err)
		}
	}
	return &meta, undo, nil
}

// channelTestError turns a failed channel self-test into a 502 whose code
// says why (core.FailureCode) and whose message is the raw reason.
func (s *Server) channelTestError(err error) error {
	if errors.Is(err, core.ErrSealed) || errors.Is(err, core.ErrNotImplemented) {
		return err
	}
	code := core.CodeOf(err)
	if code == "" {
		msg := strings.ToLower(err.Error())
		switch {
		case containsAny(msg, "invalid_auth", "not_authed", "token_revoked", "account_inactive", "unauthorized", "rejected"):
			code = core.FailKeyRejected
		case containsAny(msg, "ratelimited", "rate limit", "too many requests"):
			code = core.FailRateLimited
		default:
			code = core.FailUnknown
		}
	}
	return newError(http.StatusBadGateway, string(code), err.Error())
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
