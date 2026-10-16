package core

import (
	"context"
	"time"
)

type RunLimits struct {
	MaxSteps  int           `json:"max_steps"`
	MaxTokens int           `json:"max_tokens"` // cumulative input+output across the run
	MaxTime   time.Duration `json:"max_time"`   // wall clock, approval waits excluded
	MaxCost   Micros        `json:"max_cost_micros"`
}

type BudgetKind string

const (
	BudgetSteps     BudgetKind = "steps"
	BudgetTokens    BudgetKind = "tokens"
	BudgetTime      BudgetKind = "time"
	BudgetRunCost   BudgetKind = "run_cost"
	BudgetDailyCost BudgetKind = "daily_cost"
	BudgetRate      BudgetKind = "rate"
)

type BudgetError struct {
	Kind  BudgetKind
	Limit int64
	Used  int64
	// Scope of a daily_cost error: which cap ran out.
	Scope string // personal | chat | global
}

// BudgetError scopes of the daily caps.
const (
	BudgetPersonal = "personal" // the account's or teammate's own cap
	BudgetChat     = "chat"     // the share of the daily limit chat apps may use together
	BudgetGlobal   = "global"
)

func (e *BudgetError) Error() string { return "budget exceeded: " + string(e.Kind) }

type LoopVerdict uint8

const (
	LoopOK LoopVerdict = iota
	LoopWarn
	LoopAbort
)

type RunTotals struct {
	Steps     int           `json:"steps"`
	ToolCalls int           `json:"tool_calls"`
	Usage     Usage         `json:"usage"`
	Cost      Micros        `json:"cost_micros"`
	Active    time.Duration `json:"active"`
}

type RunMeter interface {
	Step() error // *BudgetError
	// BeforeCall projects estInput + m.MaxOutput against run tokens, run cost and
	// the principal's, the chat share's and the global daily caps. Unknown
	// prices skip $ checks only.
	BeforeCall(ctx context.Context, estInput int, m ModelInfo) error
	// AfterCall records spend_daily and run totals; emits budget.warning at 80%.
	AfterCall(ctx context.Context, c CallInfo) error
	ObserveTool(tool string, argsHash, resultHash uint64) LoopVerdict
	ObserveText(hash uint64) LoopVerdict // identical assistant text 3 steps in a row => abort
	Pause()                              // approval wait: wall clock stops
	Resume()
	Deadline() time.Time
	Totals() RunTotals
	Remaining() RunLimits
}

// Budget bills by principal: a user principal's id is its user's, and a
// team-owned principal (channel or teammate) pays its own line.
type Budget interface {
	StartRun(run RunID, p PrincipalID, l RunLimits) RunMeter
	// CheckDaily fails with *BudgetError(daily_cost) when the principal's
	// own cap, the chat share (team-owned principals) or the global cap is
	// exhausted; used before accepting a run.
	CheckDaily(ctx context.Context, p PrincipalID) error
	// RecordSpend is for calls outside a run (passthrough, probes, notes): it
	// writes the llm_calls row (run_id NULL) and spend_daily.
	RecordSpend(ctx context.Context, p PrincipalID, c CallInfo) error
	AllowSender(key string) (ok bool, retryAfter time.Duration) // token bucket, LRU 10k keys
	Daily(ctx context.Context, p PrincipalID) (spent, limit Micros, err error)
	DefaultLimits(t Trigger) RunLimits
}
