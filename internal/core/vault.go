package core

import (
	"context"
	"strings"
)

// Scope is "global" or "user:<id>". Spaces have no vault scope.
type Scope string

const GlobalScope Scope = "global"

func UserScope(u UserID) Scope { return Scope("user:" + string(u)) }

// The names of the keys Bothy keeps in the global vault for itself: its chat
// apps' tokens, sign-in client secrets and its GitHub connections' and apps'
// keys. IsSystemSecret is the one list of them.

const (
	SecretTelegramBotToken = "telegram_bot_token"
	SecretDiscordBotToken  = "discord_bot_token"
	SecretSlackAppToken    = "slack_app_token"
	SecretSlackBotToken    = "slack_bot_token"
)

// SignInSecretName is where a sign-in provider's client secret is saved; a
// "_2" (and on) suffix when an environment variable holds that name.
func SignInSecretName(p SignInProvider) string { return string(p) + "_sign_in_client_secret" }

// GitHubConnSecretName is where a GitHub connection (ghc_…) keeps the token
// or app key Bothy stores for it.
func GitHubConnSecretName(connID string) string { return "github_" + connID }

// GitHubAppKeyName and GitHubAppClientSecretName are where an app Bothy made
// (gha_…) keeps its private key and client secret.
func GitHubAppKeyName(appID string) string          { return "github_" + appID + "_key" }
func GitHubAppClientSecretName(appID string) string { return "github_" + appID + "_client_secret" }

// IsSystemSecret reports whether name is one of Bothy's own keys. They are
// never offered for, or sent to, an MCP server: a key a person saves for a
// connected tool has any other name.
func IsSystemSecret(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case SecretTelegramBotToken, SecretDiscordBotToken, SecretSlackAppToken, SecretSlackBotToken:
		return true
	}
	for _, p := range SignInProviders {
		if rest, ok := strings.CutPrefix(n, SignInSecretName(p)); ok && (rest == "" || rest[0] == '_' && onlyOf(rest[1:], "0123456789")) {
			return true
		}
	}
	// Lenient about the id's shape: missing one of these costs more than
	// hiding a key someone happened to name like them.
	const idChars = "0123456789abcdefghijklmnopqrstuvwxyz"
	if id, ok := strings.CutPrefix(n, GitHubConnSecretName(PrefixGitHubConn+"_")); ok && onlyOf(id, idChars) {
		return true
	}
	if rest, ok := strings.CutPrefix(n, "github_"+PrefixGitHubApp+"_"); ok {
		for _, part := range []string{"_key", "_client_secret"} {
			if id, ok := strings.CutSuffix(rest, part); ok && onlyOf(id, idChars) {
				return true
			}
		}
	}
	return false
}

func onlyOf(s, chars string) bool { return s != "" && strings.Trim(s, chars) == "" }

// Secret never prints its value through fmt, JSON or slog.
type Secret struct{ v []byte }

func NewSecret(b []byte) Secret               { return Secret{v: b} }
func (s Secret) Reveal() string               { return string(s.v) }
func (s Secret) Bytes() []byte                { return s.v }
func (s Secret) IsZero() bool                 { return len(s.v) == 0 }
func (s Secret) String() string               { return "[redacted]" }
func (s Secret) GoString() string             { return "[redacted]" }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

type VaultStatus struct {
	Sealed    bool   `json:"sealed"`     // passphrase KEK not yet entered
	KEKSource string `json:"kek_source"` // docker_secret | systemd | file | passphrase | env | autogen
	KEKID     string `json:"kek_id"`     // sha256(kek)[:8] hex
	Colocated bool   `json:"colocated"`  // KEK file on the same volume as the DB
	Secrets   int    `json:"secrets"`
	// NewPassphrase: a sealed passphrase vault that has none yet, whose
	// first Unseal chooses it.
	NewPassphrase bool `json:"new_passphrase,omitempty"`
	// External counts the configured references to cloud secret stores by
	// source ("aws-sm"). Their values are fetched when used, kept in memory
	// for secrets.aws.cache_ttl and never stored.
	External map[string]int `json:"external,omitempty"`
}

// Redactor masks exact secret values (and their base64, base64url, hex and
// percent-encoded forms, for values >= 8 bytes) plus token patterns: sk-…,
// sk-ant-…, xox[abpr]-…, xapp-…, ghp_/gho_/github_pat_…, AIza…, JWTs,
// bothy_pat_…. Applied to logs, audit detail, tool results, stored messages,
// channel output, SSE payloads and model input. Redact and RedactBytes return
// their input unchanged when nothing is masked; RedactBytes may return its
// argument, so callers that keep or modify the result copy it first.
type Redactor interface {
	Redact(s string) string
	RedactBytes(b []byte) []byte
	Add(id string, value []byte) // vault plaintexts, setup token, session cookies in flight
	Remove(id string)
}

// Not part of Redactor: vault.Redactor (and testkit/fakes.Redactor) also
// implement PartialAt(s string) int, which returns where the earliest suffix
// of s starts that is a proper prefix of a registered value or of one of its
// encodings (len(s) when none). Streamers (agent run.delta, run.thinking and
// run.tool_output; the channel hub's live messages) find it by a type
// assertion on interface{ PartialAt(string) int } and hold that tail back, so
// redacting each published piece masks exactly what redacting the whole
// stream would, even for a secret that contains spaces.

type Vault interface {
	Put(ctx context.Context, sc Scope, name string, value []byte, by UserID) (SecretMeta, error)
	// Get resolves BOTHY_SECRET_<NAME> env first, then the given scope; with a user
	// scope it falls back to global. Every use is audited as secret.use(name, purpose).
	Get(ctx context.Context, sc Scope, name, purpose string) (Secret, error)
	// Resolve handles config references "secret:<name>" (user scope, then global;
	// u == "" => global only, used for providers, channels, search and space
	// principals) and "aws-sm:<secret id or ARN>[#<json key>]" (AWS Secrets
	// Manager, read with the server's AWS credentials; company-wide, so the
	// same for every u). A ref embedded in a header value ("Bearer secret:x")
	// resolves to the whole header with the ref substituted. Plain strings are
	// rejected for secret-typed fields. Every use is audited as secret.use.
	Resolve(ctx context.Context, ref string, u UserID, purpose string) (Secret, error)
	Delete(ctx context.Context, sc Scope, name string, by UserID) error
	// List returns names, source and hint, never values; the global scope also
	// lists the configuration's aws-sm references (source "aws-sm", no hint).
	List(ctx context.Context, sc Scope) ([]SecretMeta, error)
	Unseal(ctx context.Context, passphrase []byte) error
	RotateKEK(ctx context.Context, by UserID) error
	Status() VaultStatus
	Redactor() Redactor
}
