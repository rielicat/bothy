package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// An admin token can't change what decides who gets in: sign-in settings,
// team mode while signing in makes accounts, or a chat app turned on or
// given another token. Like letting in one person, those need an admin
// signed in on the web; every other setting stays open to the token.
func TestConfigThatLetsPeopleInNeedsASignedInAdmin(t *testing.T) {
	h := newHarness(t).setup()
	m, err := config.Load(t.Context(), config.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.mu.Lock()
	h.cfg.mgr = m
	h.cfg.mu.Unlock()
	tok := h.tokenFor(h.admin, "chat", "admin")
	patch := func(c *client, set map[string]any) *httptest.ResponseRecorder {
		return h.do("PATCH", "/api/config", map[string]any{"set": set}, c)
	}
	refused := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signed-in browser session") {
			t.Errorf("%s by a token = %d %s", what, rec.Code, rec.Body.String())
		}
	}

	for _, set := range []map[string]any{
		{"auth.sign_in.allow": []string{"evil.example"}},
		{"auth.sign_in.allow_new": true},
		{"auth": map[string]any{"sign_in": map[string]any{"github": map[string]any{"orgs": []string{"evil-org"}}}}},
		{"auth.lockout.max_failures": 1000},
		{"channels.slack.enabled": true},
		{"channels": map[string]any{"telegram": map[string]any{"enabled": true}}},
		{"channels.discord": map[string]any{"enabled": true}},
	} {
		refused("PATCH /api/config", patch(tok, set))
	}
	cur := m.Current()
	if len(cur.Auth.SignIn.Allow) != 0 || cur.Auth.SignIn.AllowNew || cur.Auth.Lockout.MaxFailures != 5 || cur.Channels.Slack.Enabled || cur.Channels.Telegram.Enabled || cur.Channels.Discord.Enabled {
		t.Fatalf("a refused change was saved: %+v %+v", cur.Auth, cur.Channels)
	}
	expect(t, patch(tok, map[string]any{"budgets.daily_usd": 3, "channels.slack.groups.mention_only": false, "channels.unknown_senders": "ignore"}), http.StatusOK)

	// A signed-in admin makes the same changes.
	expect(t, patch(h.admin, map[string]any{"auth.sign_in.allow": []string{"acme.com"}, "auth.sign_in.allow_new": true, "channels.slack.enabled": true}), http.StatusOK)
	// With Slack on, the token may not swap its workspace's token or switch
	// to team mode (accounts on sign-in), nor reset the sign-in list.
	refused("another Slack token", patch(tok, map[string]any{"channels.slack.bot_token": "secret:other_workspace"}))
	refused("team mode", patch(tok, map[string]any{"mode": "multi"}))
	refused("DELETE /api/config/auth", h.do("DELETE", "/api/config/auth", nil, tok))
	// Turning Slack off lets nobody in.
	expect(t, patch(tok, map[string]any{"channels.slack.enabled": false}), http.StatusOK)
	// Off, Slack's token still can't point at a secret the token saved: an
	// admin's later switch would turn that workspace on.
	expect(t, h.do("PUT", "/api/vault/global/other_workspace", dto.VaultPutReq{Value: "xoxb-other"}, tok), http.StatusOK)
	refused("another Slack token while it is off", patch(tok, map[string]any{"channels.slack.bot_token": "secret:other_workspace"}))
	if cur := m.Current(); cur.Mode != "single" || len(cur.Auth.SignIn.Allow) != 1 || cur.Channels.Slack.Enabled || cur.Channels.Slack.BotToken != "secret:slack_bot_token" {
		t.Fatalf("after token calls: mode %s allow %v slack %v %s", cur.Mode, cur.Auth.SignIn.Allow, cur.Channels.Slack.Enabled, cur.Channels.Slack.BotToken)
	}
	expect(t, patch(h.admin, map[string]any{"mode": "multi"}), http.StatusOK)

	// The sign-in screens' own saves, and the secret a chat app's token
	// setting names (another Slack token is another workspace).
	on := true
	refused("PATCH /api/sign-in", h.do("PATCH", "/api/sign-in", dto.SignInListPatch{Allow: &[]string{"evil.example"}}, tok))
	refused("PATCH /api/sign-in/google", h.do("PATCH", "/api/sign-in/google", dto.SignInPatch{Enabled: &on}, tok))
	refused("PUT the Slack token", h.do("PUT", "/api/vault/global/slack_bot_token", dto.VaultPutReq{Value: "xoxb-other"}, tok))
	h.env["TELEGRAM_BOT_TOKEN"] = "123:other"
	refused("importing the Telegram token", h.do("POST", "/api/vault/import", dto.VaultImportReq{EnvVar: "TELEGRAM_BOT_TOKEN", Name: "telegram_bot_token"}, tok))
	expect(t, h.do("PUT", "/api/vault/global/brave_api_key", dto.VaultPutReq{Value: "brave-key"}, tok), http.StatusOK)
	expect(t, h.do("PUT", "/api/vault/global/slack_bot_token", dto.VaultPutReq{Value: "xoxb-ours"}, h.admin), http.StatusOK)
}
