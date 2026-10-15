package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func TestSetupStateInstall(t *testing.T) {
	for _, tc := range []struct {
		env     map[string]string
		backend string
		want    dto.InstallKind
	}{
		{map[string]string{}, "auto", "binary"},
		{map[string]string{"BOTHY_DEPLOYMENT": "container"}, "sandboxd", "compose"},
		{map[string]string{"BOTHY_DEPLOYMENT": "container"}, "auto", "docker"},
		{map[string]string{"BOTHY_DEPLOYMENT": "container", "KUBERNETES_SERVICE_HOST": "10.0.0.1"}, "sandboxd", "unknown"},
		{map[string]string{"BOTHY_DEPLOYMENT": "container", "ECS_CONTAINER_METADATA_URI_V4": "http://169.254.170.2/v4/x"}, "sandboxd", "unknown"},
	} {
		h := newHarness(t, func(c *config.Config) { c.Sandbox.Backend = tc.backend })
		h.env = tc.env
		if _, _, err := h.auth.EnsureSetupToken(context.Background()); err != nil {
			t.Fatal(err)
		}
		st := decodeAs[dto.SetupState](t, h.do("GET", "/api/setup/state", nil, nil))
		if st.Install != tc.want {
			t.Errorf("%v %s: install %q, want %q", tc.env, tc.backend, st.Install, tc.want)
		}
	}
}

// claimed runs setup up to a valid grant and returns a request option that
// carries it.
func claimed(t *testing.T, h *harness) (reqOpt, string) {
	t.Helper()
	ctx := context.Background()
	tok, _, err := h.auth.EnsureSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := h.do("POST", "/api/setup/claim", dto.SetupClaimReq{Token: tok}, nil)
	expect(t, rec, http.StatusOK)
	claim := decodeAs[dto.SetupClaimResp](t, rec)
	var grant string
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieSetup {
			grant = c.Value
		}
	}
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: cookieSetup, Value: grant})
		r.Header.Set("X-CSRF-Token", claim.CSRF)
	}, claim.CSRF
}

func ownerFrom(t *testing.T, rec interface {
	Result() *http.Response
}, me dto.Me) *client {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookiePlain {
			return &client{name: "owner", user: me.User.ID, cookie: c.Value, csrf: me.CSRF}
		}
	}
	t.Fatal("no session cookie")
	return nil
}

// The wizard's path: owner with a display name, one-call connect, then
// setup complete as Step 3 opens.
func TestSetupFlowWithConnect(t *testing.T) {
	h := newHarness(t)
	grant, _ := claimed(t, h)
	bad := dto.SetupOwnerReq{DisplayName: strings.Repeat("x", 65), Username: "maya", Password: testPassword, Timezone: "UTC"}
	expect(t, h.do("POST", "/api/setup/owner", bad, nil, grant), http.StatusBadRequest)
	if n, _ := h.store.Users().Count(context.Background()); n != 0 {
		t.Fatal("a rejected display name still created the owner")
	}
	rec := h.do("POST", "/api/setup/owner", dto.SetupOwnerReq{DisplayName: " Maya Chen ", Username: "maya", Password: testPassword, Timezone: "UTC"}, nil, grant)
	expect(t, rec, http.StatusOK)
	me := decodeAs[dto.Me](t, rec)
	if me.User.DisplayName != "Maya Chen" || me.User.MustChangePassword {
		t.Fatalf("owner = %+v", me.User)
	}
	owner := ownerFrom(t, rec, me)
	if p, _ := h.store.Principals().ForUser(context.Background(), me.User.ID); len(p) != 1 || p[0].DisplayName != "Maya Chen" {
		t.Fatalf("principal = %+v", p)
	}

	req := dto.ProviderConnectReq{Kind: core.KindAnthropic, APIKey: "sk-ant-api03-SETUP-KEY-0001", Model: "claude-sonnet-4-6", QuickModel: "claude-haiku-4-5"}
	rec = h.do("POST", "/api/providers/connect", req, owner)
	expect(t, rec, http.StatusOK)
	resp := decodeAs[dto.ProviderConnectResp](t, rec)
	if resp.Provider != "anthropic" || resp.Main != "anthropic/claude-sonnet-4-6" || resp.Fast != "anthropic/claude-haiku-4-5" ||
		resp.Secret == nil || resp.Secret.Name != "anthropic_api_key" || resp.RestartRequired == nil {
		t.Fatalf("connect = %+v", resp)
	}
	if strings.Contains(rec.Body.String(), "SETUP-KEY") {
		t.Fatal("the key came back in the response")
	}
	got, err := h.vault.Get(context.Background(), core.GlobalScope, "anthropic_api_key", "test")
	if err != nil || got.Reveal() != req.APIKey {
		t.Fatalf("vault = %v", err)
	}
	if acts := h.audit.Actions(); slices.Contains(acts, "vault.put") || !slices.Contains(acts, "provider.connect") {
		t.Fatalf("audit = %v, want provider.connect only: the vault audits its own write", acts)
	}
	sets := h.cfg.sets
	if len(sets) != 1 {
		t.Fatalf("config writes = %d, want one atomic change", len(sets))
	}
	var prov map[string]any
	_ = json.Unmarshal(sets[0]["providers.anthropic"], &prov)
	if prov["type"] != "anthropic" || prov["api_key"] != "secret:anthropic_api_key" || prov["allow_private"] != false || prov["discover"] != false {
		t.Fatalf("provider = %v", prov)
	}
	if string(sets[0]["models.main"]) != `["anthropic/claude-sonnet-4-6"]` || string(sets[0]["models.fast"]) != `["anthropic/claude-haiku-4-5"]` {
		t.Fatalf("slots = %s %s", sets[0]["models.main"], sets[0]["models.fast"])
	}
	expect(t, h.do("POST", "/api/setup/complete", nil, owner), http.StatusNoContent)
	expect(t, h.do("POST", "/api/setup/complete", nil, owner), http.StatusNotFound)
}

// failingConfig refuses every change, like a config validation error.
type failingConfig struct{ *testConfig }

func (failingConfig) Set(context.Context, map[string]json.RawMessage, string) ([]string, error) {
	return nil, config.FieldErrors{{Path: "providers.anthropic.base_url", Message: "invalid URL"}}
}

func TestConnectProvider(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	connect := func(req dto.ProviderConnectReq) *dto.ProviderConnectResp {
		t.Helper()
		rec := h.do("POST", "/api/providers/connect", req, h.admin)
		expect(t, rec, http.StatusOK)
		r := decodeAs[dto.ProviderConnectResp](t, rec)
		return &r
	}
	last := func() map[string]json.RawMessage { return h.cfg.sets[len(h.cfg.sets)-1] }

	// A detected local server, allowed and discovered; no key.
	r := connect(dto.ProviderConnectReq{Kind: core.KindOllama, BaseURL: "http://host.docker.internal:11434", AllowPrivate: true, Model: "qwen3:14b"})
	if r.Provider != "ollama" || r.Secret != nil || r.Main != "ollama/qwen3:14b" || r.Fast != "ollama/qwen3:14b" {
		t.Fatalf("ollama = %+v", r)
	}
	if !strings.Contains(string(last()["providers.ollama"]), `"allow_private":true`) || !strings.Contains(string(last()["providers.ollama"]), `"discover":true`) {
		t.Fatalf("ollama provider = %s", last()["providers.ollama"])
	}

	// A key saved earlier with `bothy vault set`.
	if _, err := h.vault.Put(ctx, core.GlobalScope, "openrouter_api_key", []byte("sk-or-v1-saved-0000abcd"), ""); err != nil {
		t.Fatal(err)
	}
	h.cfg.update(func(c *config.Config) { c.Models.Main = []string{"ollama/qwen3:14b"} })
	r = connect(dto.ProviderConnectReq{Kind: core.KindOpenAICompat, BaseURL: "https://openrouter.ai/api/v1", SecretRef: "secret:openrouter_api_key",
		Model: "anthropic/claude-sonnet-4.5"})
	if r.Provider != "openrouter" || r.Secret == nil || r.Secret.Hint != "abcd" || r.Main != "openrouter/anthropic/claude-sonnet-4.5" {
		t.Fatalf("openrouter = %+v", r)
	}
	// The previous main model stays as a backup.
	if string(last()["models.main"]) != `["openrouter/anthropic/claude-sonnet-4.5","ollama/qwen3:14b"]` ||
		!strings.Contains(string(last()["providers.openrouter"]), `"discover":false`) {
		t.Fatalf("change = %s", last())
	}
	expect(t, h.do("POST", "/api/providers/connect", dto.ProviderConnectReq{Kind: core.KindOpenAICompat, BaseURL: "https://openrouter.ai/api/v1",
		SecretRef: "secret:nothing_here"}, h.admin), http.StatusBadRequest)

	// A key the company keeps in AWS Secrets Manager: only the reference is
	// written; the vault is not asked.
	r = connect(dto.ProviderConnectReq{Kind: core.KindAnthropic, SecretRef: "aws-sm:prod/llm?region=us-east-1#anthropic"})
	if r.Provider != "anthropic" || r.Secret == nil || r.Secret.Source != "aws-sm" || r.Secret.Hint != "" || r.Secret.Name != "aws-sm:prod/llm#anthropic?region=us-east-1" {
		t.Fatalf("anthropic from aws-sm = %+v", r)
	}
	if !strings.Contains(string(last()["providers.anthropic"]), `"api_key":"aws-sm:prod/llm#anthropic?region=us-east-1"`) {
		t.Fatalf("provider = %s", last()["providers.anthropic"])
	}
	bad := h.do("POST", "/api/providers/connect", dto.ProviderConnectReq{Kind: core.KindAnthropic, SecretRef: "aws-sm:prod/llm#"}, h.admin)
	expect(t, bad, http.StatusBadRequest)
	if strings.Contains(bad.Body.String(), "prod/llm") {
		t.Errorf("error echoes the reference: %s", bad.Body.String())
	}

	// A detected environment key is copied into the vault.
	h.env["OPENAI_API_KEY"] = "sk-proj-from-the-environment-1234"
	r = connect(dto.ProviderConnectReq{Kind: core.KindOpenAI, EnvVar: "OPENAI_API_KEY"})
	if r.Provider != "openai" || r.Secret == nil || r.Secret.Name != "openai_api_key" || r.Main != "" {
		t.Fatalf("openai = %+v", r)
	}
	if _, set := last()["models.main"]; set {
		t.Fatal("connect without a model changed the slots")
	}
	expect(t, h.do("POST", "/api/providers/connect", dto.ProviderConnectReq{Kind: core.KindOpenAI, EnvVar: "PATH"}, h.admin), http.StatusBadRequest)
	h.env["TELEGRAM_BOT_TOKEN"] = "123:telegram"
	expect(t, h.do("POST", "/api/providers/connect", dto.ProviderConnectReq{Kind: core.KindOpenAI, EnvVar: "TELEGRAM_BOT_TOKEN"}, h.admin), http.StatusBadRequest)

	// A second server of a kind gets its own id instead of rewriting the
	// first; the same server again keeps its id.
	h.cfg.update(func(c *config.Config) {
		c.Providers = append(c.Providers, config.Provider{ID: "custom", Type: "openai_compat", BaseURL: "http://10.0.0.5:8080/v1"})
	})
	r = connect(dto.ProviderConnectReq{Kind: core.KindOpenAICompat, BaseURL: "http://10.0.0.6:8080/v1", AllowPrivate: true, Model: "m"})
	if r.Provider != "custom-2" || r.Main != "custom-2/m" {
		t.Fatalf("second server = %+v", r)
	}
	if r = connect(dto.ProviderConnectReq{Kind: core.KindOpenAICompat, BaseURL: "http://10.0.0.5:8080/v1/", AllowPrivate: true}); r.Provider != "custom" {
		t.Fatalf("same server = %+v", r)
	}

	// Bedrock: the server's AWS access, never a key.
	r = connect(dto.ProviderConnectReq{Kind: core.KindBedrock, Region: "eu-west-1", Model: "eu.anthropic.claude-sonnet-4-6"})
	if r.Provider != "bedrock" || !strings.Contains(string(last()["providers.bedrock"]), `"region":"eu-west-1"`) {
		t.Fatalf("bedrock = %+v %s", r, last()["providers.bedrock"])
	}
	for _, bad := range []dto.ProviderConnectReq{
		{Kind: core.KindBedrock, Region: "eu-west-1", APIKey: "x"},
		{Kind: core.KindBedrock},
		{Kind: core.KindOpenAICompat, Model: "m"},
		{Kind: core.KindAnthropic, APIKey: "a", SecretRef: "secret:b"},
		{Kind: core.KindAnthropic, ID: "Bad Id", APIKey: "a"},
		{Kind: core.KindAnthropic, QuickModel: "m"},
		{Kind: "bogus"},
	} {
		if rec := h.do("POST", "/api/providers/connect", bad, h.admin); rec.Code != http.StatusBadRequest {
			t.Errorf("%+v: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	expect(t, h.do("POST", "/api/providers/connect", dto.ProviderConnectReq{Kind: core.KindOllama}, h.member), http.StatusForbidden)

	// A refused config change takes the saved key back: a new one is
	// deleted, a replaced one restored.
	h.srv.d.Config = failingConfig{h.cfg}
	rec := h.do("POST", "/api/providers/connect", dto.ProviderConnectReq{Kind: core.KindAnthropic, APIKey: "sk-ant-new-0000", Model: "m"}, h.admin)
	expect(t, rec, http.StatusBadRequest)
	if _, err := h.vault.Get(ctx, core.GlobalScope, "anthropic_api_key", "test"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the new key was kept after the refused change: %v", err)
	}
	rec = h.do("POST", "/api/providers/connect", dto.ProviderConnectReq{Kind: core.KindOpenAI, APIKey: "sk-proj-replacement-9999"}, h.admin)
	expect(t, rec, http.StatusBadRequest)
	if s, err := h.vault.Get(ctx, core.GlobalScope, "openai_api_key", "test"); err != nil || s.Reveal() != "sk-proj-from-the-environment-1234" {
		t.Fatalf("the replaced key was not restored: %v", err)
	}
}

func TestOneTimePasswords(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("POST", "/api/users", dto.UserCreateReq{Username: "jordan", DisplayName: "Jordan Kim", Role: core.RoleMember}, h.admin)
	expect(t, rec, http.StatusOK)
	created := decodeAs[dto.UserCreateResp](t, rec)
	if created.OneTimePassword == "" || !created.MustChangePassword || created.DisplayName != "Jordan Kim" || created.ID == "" {
		t.Fatalf("created = %+v", created)
	}
	// A typed password keeps the v0.1 behaviour.
	typed := decodeAs[dto.UserCreateResp](t, h.do("POST", "/api/users", dto.UserCreateReq{Username: "pat", Password: testPassword, Role: core.RoleMember}, h.admin))
	if typed.OneTimePassword != "" || typed.MustChangePassword {
		t.Fatalf("typed = %+v", typed)
	}

	rec = h.do("POST", "/api/auth/login", dto.LoginReq{Username: "jordan", Password: created.OneTimePassword}, nil)
	expect(t, rec, http.StatusOK)
	me := decodeAs[dto.Me](t, rec)
	if !me.User.MustChangePassword {
		t.Fatalf("sign-in did not return the flag: %+v", me.User)
	}
	var cookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookiePlain {
			cookie = c.Value
		}
	}
	jordan := &client{name: "jordan", user: me.User.ID, cookie: cookie, csrf: me.CSRF}
	rec = h.do("GET", "/api/sessions", nil, jordan)
	expect(t, rec, http.StatusForbidden)
	if errCode(t, rec) != "password_change_required" {
		t.Fatalf("code %q", errCode(t, rec))
	}
	expect(t, h.do("GET", "/api/auth/me", nil, jordan), http.StatusOK)
	expect(t, h.do("POST", "/api/auth/password", dto.PasswordReq{New: "short"}, jordan), http.StatusBadRequest)
	rec = h.do("POST", "/api/auth/password", dto.PasswordReq{New: "harbour lantern quietly folds"}, jordan)
	expect(t, rec, http.StatusNoContent)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookiePlain {
			jordan.cookie = c.Value
		}
	}
	me = decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, jordan))
	jordan.csrf = me.CSRF
	if me.User.MustChangePassword {
		t.Fatal("the flag survived the change")
	}
	expect(t, h.do("GET", "/api/sessions", nil, jordan), http.StatusOK)
	expect(t, h.do("POST", "/api/auth/login", dto.LoginReq{Username: "jordan", Password: created.OneTimePassword}, nil), http.StatusUnauthorized)
	// Without the flag the current password is required again.
	expect(t, h.do("POST", "/api/auth/password", dto.PasswordReq{New: "another fine long phrase"}, jordan), http.StatusBadRequest)
}

func TestResetUserPassword(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("POST", "/api/users/"+string(h.member.user)+"/password", nil, h.admin)
	expect(t, rec, http.StatusOK)
	reset := decodeAs[dto.PasswordResetResp](t, rec)
	if len(reset.OneTimePassword) < 19 {
		t.Fatalf("reset = %+v", reset)
	}
	// The member's sessions ended and the next sign-in must change it.
	expect(t, h.do("GET", "/api/auth/me", nil, h.member), http.StatusUnauthorized)
	me := decodeAs[dto.Me](t, h.do("POST", "/api/auth/login", dto.LoginReq{Username: "member", Password: reset.OneTimePassword}, nil))
	if !me.User.MustChangePassword {
		t.Fatal("reset account need not change its password")
	}
	for _, tc := range []struct {
		by     *client
		target core.UserID
		want   int
	}{
		{h.admin, h.owner.user, http.StatusForbidden},
		{h.admin, h.admin.user, http.StatusForbidden},
		{h.newUser("member2", core.RoleMember), h.admin.user, http.StatusForbidden},
		{h.tokenFor(h.owner, "admin"), h.admin.user, http.StatusForbidden},
		{h.owner, "usr_00000000000000000000000000", http.StatusNotFound},
	} {
		if rec := h.do("POST", "/api/users/"+string(tc.target)+"/password", nil, tc.by); rec.Code != tc.want {
			t.Errorf("%s resetting %s: %d %s", tc.by.name, tc.target, rec.Code, rec.Body.String())
		}
	}
	expect(t, h.do("POST", "/api/users/"+string(h.admin.user)+"/password", nil, h.owner), http.StatusOK)
}

type failingChannel struct {
	core.Channel
	name string
	err  error
}

func (c failingChannel) Name() string                             { return c.name }
func (c failingChannel) Status() core.ChannelStatus               { return core.ChannelStatus{Name: c.name} }
func (c failingChannel) SelfTest(context.Context) (string, error) { return "", c.err }

type codedErr struct{ code core.FailureCode }

func (e codedErr) Error() string                 { return "Slack rejected the bot token (invalid_auth)" }
func (e codedErr) FailureCode() core.FailureCode { return e.code }

func TestChannelTestCodes(t *testing.T) {
	h := newHarness(t).setup()
	h.hub.Add(failingChannel{name: "slack", err: codedErr{core.FailKeyRejected}})
	h.hub.Add(failingChannel{name: "telegram", err: errors.New(`telegram getMe: Post "https://api.telegram.org/bot<token>/getMe": dial tcp: lookup api.telegram.org: no such host`)})
	h.hub.Add(failingChannel{name: "discord", err: errors.New("discord: get current user: something odd")})
	for name, want := range map[string]core.FailureCode{"slack": core.FailKeyRejected, "telegram": core.FailUnreachable, "discord": core.FailUnknown} {
		rec := h.do("POST", "/api/channels/"+name+"/test", nil, h.admin)
		expect(t, rec, http.StatusBadGateway)
		env := decodeAs[dto.Error](t, rec)
		if core.FailureCode(env.Error.Code) != want || env.Error.Message == "" {
			t.Errorf("%s: %+v", name, env)
		}
	}
	expect(t, h.do("POST", "/api/channels/matrix/test", nil, h.admin), http.StatusNotFound)
}
