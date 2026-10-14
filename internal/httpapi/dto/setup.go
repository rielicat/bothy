package dto

import "github.com/rielicat/bothy/internal/core"

// ---- people and models setup (v0.2 UI) ----

// InstallKind is how this server runs (SetupState.Install): compose, docker,
// binary, or unknown where none of their commands applies (Kubernetes, ECS).
type InstallKind string

// UserCreateResp is POST /api/users: the new account and, when the request
// left the password empty, the one-time password to hand to its owner
// (shown once; they choose their own at the first sign-in).
type UserCreateResp struct {
	core.User
	OneTimePassword string `json:"one_time_password,omitempty"`
}

// PasswordResetResp is POST /api/users/{id}/password: the account's new
// one-time password, shown once.
type PasswordResetResp struct {
	OneTimePassword string `json:"one_time_password"`
}

// ProviderConnectReq is POST /api/providers/connect: one call that saves the
// key, writes providers[id] and sets the model slots in one config change.
// Give at most one of APIKey (pasted; saved in the global vault as
// "<id>_api_key"), SecretRef (a saved key, "secret:<name>", from a detection,
// or "aws-sm:<secret>" in AWS Secrets Manager) or EnvVar (a detected
// well-known key variable, copied into the vault).
// Bedrock takes none of them.
type ProviderConnectReq struct {
	ID           string            `json:"id,omitempty"` // providers[].id; "" = from kind and address (anthropic, openrouter, ollama, lmstudio, custom, …)
	Kind         core.ProviderKind `json:"kind"`
	BaseURL      string            `json:"base_url,omitempty"`
	APIKey       string            `json:"api_key,omitempty"` // write-only
	SecretRef    string            `json:"secret_ref,omitempty"`
	EnvVar       string            `json:"env_var,omitempty"`
	AllowPrivate bool              `json:"allow_private"`         // a detected local server, or an address the person allowed
	API          string            `json:"api,omitempty"`         // openai, openai_compat: auto | responses | chat
	Region       string            `json:"region,omitempty"`      // bedrock
	Profile      string            `json:"profile,omitempty"`     // bedrock
	Model        string            `json:"model,omitempty"`       // main model id; "" leaves both slots as they are
	QuickModel   string            `json:"quick_model,omitempty"` // quick tasks model id; "" = Model
}

// ProviderConnectResp says what POST /api/providers/connect wrote.
type ProviderConnectResp struct {
	Provider        string           `json:"provider"`       // the providers[].id written
	Main            string           `json:"main,omitempty"` // "<provider>/<model>", now first in models.main
	Fast            string           `json:"fast,omitempty"` // now first in models.fast
	Secret          *core.SecretMeta `json:"secret,omitempty"`
	RestartRequired []string         `json:"restart_required"`
}
