package core

import (
	"cmp"
	"context"
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
// env vars, and uses only the http.Client given by Egress.SystemClient.
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
	Purpose        string        `json:"purpose"` // agent|compaction|title|heartbeat|probe|passthrough
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

// BuildFunc assembles the request for the concrete model picked. The facade calls
// it again on fallback so context is re-fitted to that model's window.
type BuildFunc func(m ModelInfo) (*Request, error)

type ProbeConfig struct {
	Kind         ProviderKind `json:"kind"`
	BaseURL      string       `json:"base_url"`
	APIKey       Secret       `json:"-"`          // transient; never persisted by Probe
	SecretRef    string       `json:"secret_ref"` // alternative to APIKey: "secret:<name>"
	Model        string       `json:"model"`
	AllowPrivate bool         `json:"allow_private"`
}

type ProbeResult struct {
	OK            bool          `json:"ok"`
	Latency       time.Duration `json:"latency"`
	ToolCalling   bool          `json:"tool_calling"` // a forced one-tool call round-tripped
	UsageReported bool          `json:"usage_reported"`
	Context       int           `json:"context"` // effective window
	Models        []string      `json:"models"`
	Cost          Micros        `json:"cost_micros"`
	Detail        string        `json:"detail"` // plain-language failure or hint (e.g. OLLAMA_CONTEXT_LENGTH)
}

type Detection struct {
	Kind    ProviderKind `json:"kind"`
	BaseURL string       `json:"base_url"`
	EnvVar  string       `json:"env_var,omitempty"` // e.g. ANTHROPIC_API_KEY; the value never leaves the process
	Models  []string     `json:"models,omitempty"`
	Note    string       `json:"note,omitempty"`
}

type ProviderHealth struct {
	ID        string       `json:"id"`
	Kind      ProviderKind `json:"kind"`
	State     string       `json:"state"` // ok | degraded | down | auth_failed
	LastError string       `json:"last_error,omitempty"`
	Since     time.Time    `json:"since"`
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
