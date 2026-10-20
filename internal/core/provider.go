package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Slot string

const (
	SlotMain Slot = "main" // agent turns
	SlotFast Slot = "fast" // compaction, titles, heartbeat triage
)

type ProviderKind string

const (
	KindAnthropic    ProviderKind = "anthropic"
	KindOpenAI       ProviderKind = "openai"
	KindGemini       ProviderKind = "gemini"
	KindOllama       ProviderKind = "ollama"        // native /api/chat (num_ctx honoured)
	KindOpenAICompat ProviderKind = "openai_compat" // LM Studio, vLLM, OpenRouter, Groq, xAI, DeepSeek…
	KindBedrock      ProviderKind = "bedrock"       // Amazon Bedrock ConverseStream; AWS default credential chain, no API key
)

// ModelRef is "<provider id>/<model>". The model part may itself contain "/".
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (m ModelRef) String() string { return m.Provider + "/" + m.Model }

func ParseModelRef(s string) (ModelRef, error) {
	p, m, ok := strings.Cut(s, "/")
	if !ok || p == "" || m == "" {
		return ModelRef{}, errors.New("model ref must be <provider>/<model>")
	}
	return ModelRef{Provider: p, Model: m}, nil
}

// Price is USD micros per 1M tokens. Tiers reprice the whole request once input
// passes a threshold (xAI bills 2x above 200K).
type Price struct {
	InPerM         Micros      `json:"in_per_m"`
	OutPerM        Micros      `json:"out_per_m"`
	CacheReadPerM  Micros      `json:"cache_read_per_m"`
	CacheWritePerM Micros      `json:"cache_write_per_m"`
	Tiers          []PriceTier `json:"tiers,omitempty"`
	Known          bool        `json:"known"`
}

// PriceTier multipliers are permille (2000 = 2x). Permille applies to every
// component whose own multiplier is 0 (Gemini Pro and OpenAI bill 2x input
// but 1.5x output above their thresholds).
type PriceTier struct {
	AboveInput         int `json:"above_input"`
	Permille           int `json:"permille"`
	OutPermille        int `json:"out_permille,omitempty"`
	CacheReadPermille  int `json:"cache_read_permille,omitempty"`
	CacheWritePermille int `json:"cache_write_permille,omitempty"`
}

// Cost is exact integer math, rounding each component half up. Input excludes
// cache reads/writes (providers report them separately; adapters normalise).
// Each component takes the largest of 1000 and its tier multiplier over the
// tiers whose AboveInput the prompt (input + cache read + cache write)
// exceeds, so a discount tier never lowers a price.
func (p Price) Cost(u Usage) Micros {
	in, out, cr, cw := int64(1000), int64(1000), int64(1000), int64(1000)
	total := u.Input + u.CacheRead + u.CacheWrite
	for _, t := range p.Tiers {
		if total > t.AboveInput {
			in = max(in, int64(t.Permille))
			out = max(out, int64(cmp.Or(t.OutPermille, t.Permille)))
			cr = max(cr, int64(cmp.Or(t.CacheReadPermille, t.Permille)))
			cw = max(cw, int64(cmp.Or(t.CacheWritePermille, t.Permille)))
		}
	}
	part := func(tokens int, perM Micros, mul int64) int64 {
		return (int64(tokens)*int64(perM)*mul + 500_000_000) / 1_000_000_000
	}
	return Micros(part(u.Input, p.InPerM, in) + part(u.Output, p.OutPerM, out) +
		part(u.CacheRead, p.CacheReadPerM, cr) + part(u.CacheWrite, p.CacheWritePerM, cw))
}

type CacheMode uint8

const (
	CacheNone     CacheMode = iota
	CacheExplicit           // Anthropic cache_control breakpoints
	CacheImplicit           // OpenAI/Gemini/DeepSeek automatic prefix caching
)

type ModelInfo struct {
	Ref       ModelRef  `json:"ref"`
	Display   string    `json:"display"`
	Context   int       `json:"context"` // effective window (Ollama: the num_ctx we send: the configured context, else min(trained, max(16384, served context from /api/ps)))
	MaxOutput int       `json:"max_output"`
	Tools     bool      `json:"tools"`
	Vision    bool      `json:"vision"`
	Thinking  bool      `json:"thinking"`
	Cache     CacheMode `json:"cache"`
	Local     bool      `json:"local"`
	Price     Price     `json:"price"`
	Source    string    `json:"source"` // catalog | config | discovery

	// Plain names for the UI (v0.2): DisplayName "Claude Sonnet 4.5",
	// ShortName "Sonnet 4.5" for narrow screens, ProviderName "Anthropic".
	// Recommended marks the provider's suggested main model and
	// QuickTasksDefault its suggested quick tasks model, among the models
	// listed together.
	DisplayName       string `json:"display_name,omitempty"`
	ShortName         string `json:"short_name,omitempty"`
	ProviderName      string `json:"provider_name,omitempty"`
	Recommended       bool   `json:"recommended,omitempty"`
	QuickTasksDefault bool   `json:"quick_tasks_default,omitempty"`
	// Slot is "main" or "fast" when GET /api/models finds the model first
	// in that slot (main when both), so members, who can't read the
	// config, see which model a chat uses; "" otherwise and elsewhere.
	Slot Slot `json:"slot,omitempty"`
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"` // JSON Schema object
}

type SystemBlock struct {
	Text       string
	CacheBreak bool // Anthropic cache_control breakpoint after this block
}

type Request struct {
	Model       string
	System      []SystemBlock
	Tools       []ToolDef
	ToolsCache  bool // breakpoint after the last tool (Anthropic)
	Messages    []Message
	CacheTail   bool // rolling breakpoint on the last message (Anthropic)
	MaxTokens   int
	Temperature *float64
	ToolChoice  string // "auto" | "none" | "required" (force some tool call)
	Stop        []string
	NumCtx      int // Ollama native only
	// CacheKey is stable for every call of one session and opaque (a hash,
	// never an id); the OpenAI Responses client sends it as
	// prompt_cache_key. "" sends none.
	CacheKey string
}

type StreamEventType uint8

const (
	StreamTextDelta StreamEventType = iota + 1
	StreamThinkingDelta
	StreamToolCallStart // Call has ID and Name
	StreamToolCallDone  // Call complete with repaired Args
	StreamUsage
)

type StreamEvent struct {
	Type  StreamEventType
	Text  string
	Call  *ToolCall
	Usage *Usage
}

// Usage: Input excludes cache reads and writes.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Reasoning  int `json:"reasoning"`
}

func (u Usage) Add(o Usage) Usage {
	return Usage{u.Input + o.Input, u.Output + o.Output, u.CacheRead + o.CacheRead,
		u.CacheWrite + o.CacheWrite, u.Reasoning + o.Reasoning}
}

func (u Usage) TotalInput() int { return u.Input + u.CacheRead + u.CacheWrite }

type StopReason string

const (
	StopEnd       StopReason = "end_turn"
	StopTool      StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
)

type Response struct {
	Message        Message // assistant role, parts assembled (text, thinking+sig, tool calls)
	Usage          Usage
	UsageEstimated bool // provider reported none; estimator filled it
	Stop           StopReason
	Model          string
}

// Provider is one wire protocol plus credentials. It never retries, never reads
// env vars, and uses only the http.Client given by Egress.SystemClient. One
// exception: a bedrock provider's AWS default credential chain reads the
// AWS_* variables and ~/.aws by design, and fetches instance or container
// credentials only through Egress's credentials-only client.
type Provider interface {
	ID() string
	Kind() ProviderKind
	Models(ctx context.Context) ([]ModelInfo, error) // config + discovery
	Stream(ctx context.Context, req *Request, on func(StreamEvent) error) (*Response, error)
}

type ErrorKind string

const (
	ErrKindAuth           ErrorKind = "auth"
	ErrKindRateLimit      ErrorKind = "rate_limit"
	ErrKindOverloaded     ErrorKind = "overloaded"
	ErrKindContextLength  ErrorKind = "context_length"
	ErrKindInvalidRequest ErrorKind = "invalid_request"
	ErrKindContentFilter  ErrorKind = "content_filter"
	ErrKindNetwork        ErrorKind = "network"
	ErrKindServer         ErrorKind = "server"
)

type ProviderError struct {
	Provider   string
	Kind       ErrorKind
	Status     int
	RetryAfter time.Duration
	Msg        string // redacted provider message
	Err        error
}

// ErrModelUnsupported, in a ProviderError's chain, says the model cannot
// serve the request as built (function tools over an API that cannot call
// them for that model). The error stays invalid_request for callers of one
// model; the facade tries the next model of the chain instead, as it does
// for a model without tool support.
var ErrModelUnsupported = errors.New("model cannot serve this request")

func (e *ProviderError) Error() string { return e.Provider + ": " + string(e.Kind) + ": " + e.Msg }
func (e *ProviderError) Unwrap() error { return e.Err }
func (e *ProviderError) Retryable() bool {
	switch e.Kind {
	case ErrKindRateLimit, ErrKindOverloaded, ErrKindNetwork, ErrKindServer:
		return true
	}
	return false
}

type Attempt struct {
	Ref     ModelRef      `json:"ref"`
	Err     string        `json:"err,omitempty"`
	Kind    ErrorKind     `json:"kind,omitempty"`
	Latency time.Duration `json:"latency"`
}

// Breakdown is estimated tokens per prompt region, scaled so the sum equals the
// provider-reported total input.
type Breakdown struct {
	Constitution int `json:"constitution"`
	Persona      int `json:"persona"`
	Memory       int `json:"memory"`
	Skills       int `json:"skills"`
	Pins         int `json:"pins"`
	Tools        int `json:"tools"`
	Summary      int `json:"summary"`
	History      int `json:"history"`
	ToolResults  int `json:"tool_results"`
	TurnContext  int `json:"turn_context"`
	Total        int `json:"total"`
	Window       int `json:"window"`
	Reserve      int `json:"reserve"`
	Cacheable    int `json:"cacheable"` // tokens in the frozen prefix (tools + system)
}

type CallInfo struct {
	ID             string        `json:"id"` // llm_<26>
	Ref            ModelRef      `json:"ref"`
	Slot           Slot          `json:"slot"`
	Purpose        string        `json:"purpose"` // agent|compaction|title|heartbeat|probe|passthrough|notes
	Step           int           `json:"step"`
	Attempts       []Attempt     `json:"attempts"`
	Usage          Usage         `json:"usage"`
	UsageEstimated bool          `json:"usage_estimated"`
	Cost           Micros        `json:"cost_micros"`
	PriceKnown     bool          `json:"price_known"`
	TTFT           time.Duration `json:"ttft"`
	Latency        time.Duration `json:"latency"`
	Breakdown      Breakdown     `json:"breakdown"`
	PrefixHash     string        `json:"prefix_hash"` // sha256(tools bytes || system bytes), hex[:16]
	Stop           StopReason    `json:"stop"`
	Error          string        `json:"error,omitempty"`
}

// PrefixHash is sha256(tools bytes || system bytes) as hex[:16]: equal hashes
// mean the cacheable prompt prefix did not change. The agent and the provider
// facade both use it, so their hashes of one request agree.
func PrefixHash(tools []ToolDef, system []SystemBlock) string {
	h := sha256.New()
	if len(tools) > 0 {
		b, _ := json.Marshal(tools)
		h.Write(b)
	}
	for _, s := range system {
		h.Write([]byte(s.Text))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// BuildFunc assembles the request for the concrete model picked. The facade calls
// it again on fallback so context is re-fitted to that model's window.
type BuildFunc func(m ModelInfo) (*Request, error)

type ProbeConfig struct {
	Kind         ProviderKind `json:"kind"`
	BaseURL      string       `json:"base_url"`
	APIKey       Secret       `json:"-"`          // transient; never persisted by Probe
	SecretRef    string       `json:"secret_ref"` // alternative to APIKey: "secret:<name>" or "aws-sm:<secret>"
	Model        string       `json:"model"`
	AllowPrivate bool         `json:"allow_private"`
	API          string       `json:"api,omitempty"`     // openai, openai_compat: auto | responses | chat ("" = auto), as providers[].api
	Region       string       `json:"region,omitempty"`  // bedrock: AWS region; "" = the AWS config's default
	Profile      string       `json:"profile,omitempty"` // bedrock: shared-config profile; "" = AWS_PROFILE or default
}

type ProbeResult struct {
	OK            bool          `json:"ok"`
	Latency       time.Duration `json:"latency"`
	ToolCalling   bool          `json:"tool_calling"` // a forced one-tool call round-tripped
	UsageReported bool          `json:"usage_reported"`
	Context       int           `json:"context"` // effective window
	Models        []string      `json:"models"`
	Cost          Micros        `json:"cost_micros"`
	API           string        `json:"api,omitempty"` // openai, openai_compat: the API tested, "responses" or "chat"
	Detail        string        `json:"detail"`        // plain-language failure or hint (e.g. OLLAMA_CONTEXT_LENGTH)

	Choices     []ModelChoice `json:"choices,omitempty"`     // Models with display names, for a model picker
	Credentials string        `json:"credentials,omitempty"` // bedrock: where the AWS credentials came from, never their values

	Model string      `json:"model,omitempty"` // the model the probe exercised
	Code  FailureCode `json:"code,omitempty"`  // why the check failed (OK false)
	Error string      `json:"error,omitempty"` // the raw, redacted message behind Code
}

// ModelChoice is one entry of a model picker, with the plain names and
// suggestions of ModelInfo.
type ModelChoice struct {
	ID                string `json:"id"`
	Display           string `json:"display"`
	DisplayName       string `json:"display_name,omitempty"`
	ShortName         string `json:"short_name,omitempty"`
	ProviderName      string `json:"provider_name,omitempty"`
	Recommended       bool   `json:"recommended,omitempty"`
	QuickTasksDefault bool   `json:"quick_tasks_default,omitempty"`
	// Slot is "main" or "fast" when GET /api/models finds the model first
	// in that slot (main when both), so members, who can't read the
	// config, see which model a chat uses; "" otherwise and elsewhere.
	Slot Slot `json:"slot,omitempty"`
}

type Detection struct {
	Kind    ProviderKind `json:"kind"`
	BaseURL string       `json:"base_url"`
	EnvVar  string       `json:"env_var,omitempty"` // e.g. ANTHROPIC_API_KEY; the value never leaves the process
	Models  []string     `json:"models,omitempty"`
	Note    string       `json:"note,omitempty"`
	Region  string       `json:"region,omitempty"`  // bedrock: the AWS config's region, "" when it names none
	Profile string       `json:"profile,omitempty"` // bedrock: the AWS_PROFILE in use, "" for the default chain
	Source  string       `json:"source,omitempty"`  // bedrock: where the AWS credentials came from in plain words ("instance role bothy-prod"), never their values
	Problem string       `json:"problem,omitempty"` // bedrock: why AWS settings found give no access yet: "signin_expired" (an SSO sign-in ended) or "no_credentials"

	Choices    []ModelChoice `json:"choices,omitempty"`     // Models with plain names and suggestions
	SecretRef  string        `json:"secret_ref,omitempty"`  // a provider key already saved in the global vault: "secret:<name>"
	SecretHint string        `json:"secret_hint,omitempty"` // its last four characters ("" for short values)
}

type ProviderHealth struct {
	ID        string       `json:"id"`
	Kind      ProviderKind `json:"kind"`
	State     string       `json:"state"` // ok | degraded | unreachable (Bothy couldn't reach it or where its key is kept) | down | auth_failed
	LastError string       `json:"last_error,omitempty"`
	Since     time.Time    `json:"since"`
	// Source says in plain words where the credentials come from ("Saved
	// key", "AWS Secrets Manager", "This server's AWS access (SSO profile
	// demo)"), never what they are; "" when the provider needs none.
	Source string `json:"source,omitempty"`
}

// LLM is the fallback-aware facade the agent uses. It owns retries, circuit
// breakers and the fallback chain; it never switches model after the first
// streamed event.
type LLM interface {
	Complete(ctx context.Context, slot Slot, override *ModelRef, build BuildFunc, on func(StreamEvent) error) (*Response, CallInfo, error)
	// Passthrough calls exactly one model, no fallback, for /v1 passthrough mode.
	Passthrough(ctx context.Context, ref ModelRef, req *Request, on func(StreamEvent) error) (*Response, CallInfo, error)
	Chain(slot Slot, override *ModelRef) []ModelInfo // resolved chain; [0] drives context sizing
	Model(ref ModelRef) (ModelInfo, bool)
	Models(ctx context.Context) []ModelInfo
	Estimate(text string, ref ModelRef) int // bytes/3.6 times the per-model EWMA correction
	Calibrate(ref ModelRef, estimated, actual int)
	Probe(ctx context.Context, cfg ProbeConfig) (ProbeResult, error)
	Detect(ctx context.Context) []Detection
	Health() []ProviderHealth
}
