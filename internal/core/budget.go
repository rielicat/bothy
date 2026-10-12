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
}

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
	// the user's and global daily caps. Unknown prices skip $ checks only.
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

type Budget interface {
	StartRun(run RunID, u UserID, l RunLimits) RunMeter
	// CheckDaily fails with *BudgetError(daily_cost) when the user's or global cap
	// is exhausted; used before accepting a run.
	CheckDaily(ctx context.Context, u UserID) error
	// RecordSpend is for calls outside a run (passthrough, probes): it writes the
	// llm_calls row (run_id NULL, principal = the user's own) and spend_daily.
	RecordSpend(ctx context.Context, u UserID, c CallInfo) error
	AllowSender(key string) (ok bool, retryAfter time.Duration) // token bucket, LRU 10k keys
	Daily(ctx context.Context, u UserID) (spent, limit Micros, err error)
	DefaultLimits(t Trigger) RunLimits
}
