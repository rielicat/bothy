package fakes

import (
	"context"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.Budget = (*Budget)(nil)

// Budget allows everything unless Deny is set; StartRun returns a meter that
// enforces only the given RunLimits steps and records AfterCall infos.
//
// A Deny of kind rate fails AllowSender (retry after a minute); any other kind
// fails CheckDaily and RunMeter.BeforeCall. RecordSpend is recorded in Calls
// like AfterCall. Loop checks always return LoopOK. DefaultLimits returns the
// SPEC §5.12 defaults.
type Budget struct {
	core.Budget
	Deny  *core.BudgetError
	Calls []core.CallInfo

	mu      sync.Mutex
	Senders []string // AllowSender keys, in order
	spent   map[core.UserID]core.Micros
}

func (b *Budget) denial(rate bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Deny == nil || (b.Deny.Kind == core.BudgetRate) != rate {
		return nil
	}
	return b.Deny
}

func (b *Budget) record(u core.UserID, c core.CallInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Calls = append(b.Calls, c)
	if b.spent == nil {
		b.spent = map[core.UserID]core.Micros{}
	}
	b.spent[u] += c.Cost
}

func (b *Budget) StartRun(_ core.RunID, u core.UserID, l core.RunLimits) core.RunMeter {
	return &meter{b: b, user: u, limits: l, start: time.Now()}
}

func (b *Budget) CheckDaily(context.Context, core.UserID) error { return b.denial(false) }

func (b *Budget) RecordSpend(_ context.Context, u core.UserID, c core.CallInfo) error {
	b.record(u, c)
	return nil
}

func (b *Budget) AllowSender(key string) (bool, time.Duration) {
	b.mu.Lock()
	b.Senders = append(b.Senders, key)
	b.mu.Unlock()
	if b.denial(true) != nil {
		return false, time.Minute
	}
	return true, 0
}

// Daily reports the user's recorded spend and no limit.
func (b *Budget) Daily(_ context.Context, u core.UserID) (core.Micros, core.Micros, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent[u], 0, nil
}

func (b *Budget) DefaultLimits(t core.Trigger) core.RunLimits {
	if t.Scheduled() {
		return core.RunLimits{MaxSteps: 15, MaxTokens: 150_000, MaxTime: 15 * time.Minute, MaxCost: core.MicrosFromUSD(0.25)}
	}
	return core.RunLimits{MaxSteps: 25, MaxTokens: 400_000, MaxTime: 10 * time.Minute}
}

type meter struct {
	b      *Budget
	user   core.UserID
	limits core.RunLimits
	start  time.Time

	mu       sync.Mutex
	steps    int
	tools    int
	usage    core.Usage
	cost     core.Micros
	pausedAt time.Time
	paused   time.Duration
}

func (m *meter) Step() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.limits.MaxSteps > 0 && m.steps >= m.limits.MaxSteps {
		return &core.BudgetError{Kind: core.BudgetSteps, Limit: int64(m.limits.MaxSteps), Used: int64(m.steps)}
	}
	m.steps++
	return nil
}

func (m *meter) BeforeCall(context.Context, int, core.ModelInfo) error { return m.b.denial(false) }

func (m *meter) AfterCall(_ context.Context, c core.CallInfo) error {
	m.b.record(m.user, c)
	m.mu.Lock()
	m.usage, m.cost = m.usage.Add(c.Usage), m.cost+c.Cost
	m.mu.Unlock()
	return nil
}

func (m *meter) ObserveTool(string, uint64, uint64) core.LoopVerdict {
	m.mu.Lock()
	m.tools++
	m.mu.Unlock()
	return core.LoopOK
}

func (m *meter) ObserveText(uint64) core.LoopVerdict { return core.LoopOK }

func (m *meter) Pause() {
	m.mu.Lock()
	if m.pausedAt.IsZero() {
		m.pausedAt = time.Now()
	}
	m.mu.Unlock()
}

func (m *meter) Resume() {
	m.mu.Lock()
	if !m.pausedAt.IsZero() {
		m.paused += time.Since(m.pausedAt)
		m.pausedAt = time.Time{}
	}
	m.mu.Unlock()
}

func (m *meter) pausedLocked() time.Duration {
	if m.pausedAt.IsZero() {
		return m.paused
	}
	return m.paused + time.Since(m.pausedAt)
}

// Deadline is zero without MaxTime; paused time extends it.
func (m *meter) Deadline() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.limits.MaxTime <= 0 {
		return time.Time{}
	}
	return m.start.Add(m.limits.MaxTime + m.pausedLocked())
}

func (m *meter) Totals() core.RunTotals {
	m.mu.Lock()
	defer m.mu.Unlock()
	return core.RunTotals{Steps: m.steps, ToolCalls: m.tools, Usage: m.usage, Cost: m.cost, Active: time.Since(m.start) - m.pausedLocked()}
}

// Remaining reports zero for unlimited fields.
func (m *meter) Remaining() core.RunLimits {
	m.mu.Lock()
	defer m.mu.Unlock()
	var r core.RunLimits
	if m.limits.MaxSteps > 0 {
		r.MaxSteps = max(0, m.limits.MaxSteps-m.steps)
	}
	if m.limits.MaxTokens > 0 {
		r.MaxTokens = max(0, m.limits.MaxTokens-m.usage.TotalInput()-m.usage.Output)
	}
	if m.limits.MaxTime > 0 {
		r.MaxTime = max(0, m.limits.MaxTime-(time.Since(m.start)-m.pausedLocked()))
	}
	if m.limits.MaxCost > 0 {
		r.MaxCost = max(0, m.limits.MaxCost-m.cost)
	}
	return r
}
