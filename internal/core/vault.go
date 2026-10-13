package core

import "context"

// Scope is "global" or "user:<id>". Spaces have no vault scope.
type Scope string

const GlobalScope Scope = "global"

func UserScope(u UserID) Scope { return Scope("user:" + string(u)) }

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
}

// Redactor masks exact secret values (and their base64, base64url, hex and
// percent-encoded forms, for values >= 8 bytes) plus token patterns: sk-…,
// sk-ant-…, xox[abpr]-…, xapp-…, ghp_/gho_/github_pat_…, AIza…, JWTs,
// bothy_pat_…. Applied to logs, audit detail, tool results, channel output,
// SSE payloads and model input. Redact and RedactBytes return their input
// unchanged when nothing is masked; RedactBytes may return its argument, so
// callers that keep or modify the result copy it first.
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
	// principals). A ref embedded in a header value ("Bearer secret:x") resolves
	// to the whole header with the ref substituted. Plain strings are rejected
	// for secret-typed fields.
	Resolve(ctx context.Context, ref string, u UserID, purpose string) (Secret, error)
	Delete(ctx context.Context, sc Scope, name string, by UserID) error
	List(ctx context.Context, sc Scope) ([]SecretMeta, error) // names, source, hint; never values
	Unseal(ctx context.Context, passphrase []byte) error
	RotateKEK(ctx context.Context, by UserID) error
	Status() VaultStatus
	Redactor() Redactor
}
