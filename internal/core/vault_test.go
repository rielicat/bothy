package core

import "testing"

func TestIsSystemSecret(t *testing.T) {
	const conn, app = "ghc_01m4wa97e4s0p3qgcng95bcyhf", "gha_01m4wa97e4s0p3qgcng95bcyhf"
	for _, name := range []string{
		"telegram_bot_token", "discord_bot_token", "slack_app_token", "slack_bot_token",
		"google_sign_in_client_secret", "github_sign_in_client_secret", "github_sign_in_client_secret_2",
		GitHubConnSecretName(conn), GitHubAppKeyName(app), GitHubAppClientSecretName(app),
		SignInSecretName(SignInGoogle),
		// Environment secrets are listed lower-cased, but a reference may not be.
		"TELEGRAM_BOT_TOKEN",
	} {
		if !IsSystemSecret(name) {
			t.Errorf("IsSystemSecret(%q) = false", name)
		}
	}
	for _, name := range []string{
		"", "linear_key", "mcp_docs_header", "anthropic_api_key", "brave_api_key", "github_token",
		"telegram_bot_token_2", "my_telegram_bot_token", "google_sign_in_client_secret_x",
		"github_ghc_", "github_gha_" + "01m4_key_extra", "github_gha__key", "github_" + conn + "_key",
	} {
		if IsSystemSecret(name) {
			t.Errorf("IsSystemSecret(%q) = true", name)
		}
	}
}
