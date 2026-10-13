package budget

import (
	"context"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func (s *Service) StartRun(run core.RunID, u core.UserID, l core.RunLimits) core.RunMeter {
	return &meter{s: s, run: run, user: u, limits: l, start: s.clock.Now()}
}

// meter enforces one run's limits. Wall time is measured with the service
// clock and excludes paused intervals (approval waits).
type meter struct {
	s      *Service
	run    core.RunID
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
	loop     loopDetector
}

func (m *meter) activeLocked(now time.Time) time.Duration {
	p := m.paused
	if !m.pausedAt.IsZero() {
		p += now.Sub(m.pausedAt)
	}
	return now.Sub(m.start) - p
}

func (m *meter) Step() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.limits.MaxTime > 0 {
		if active := m.activeLocked(m.s.clock.Now()); active >= m.limits.MaxTime {
			return &core.BudgetError{Kind: core.BudgetTime, Limit: int64(m.limits.MaxTime), Used: int64(active)}
		}
	}
	if m.limits.MaxSteps > 0 && m.steps >= m.limits.MaxSteps {
		return &core.BudgetError{Kind: core.BudgetSteps, Limit: int64(m.limits.MaxSteps), Used: int64(m.steps)}
	}
	m.steps++
	return nil
}

func (m *meter) BeforeCall(ctx context.Context, estInput int, info core.ModelInfo) error {
	out := max(info.MaxOutput, 0)
	m.mu.Lock()
	used := m.usage.TotalInput() + m.usage.Output
	cost := m.cost
	m.mu.Unlock()
	if m.limits.MaxTokens > 0 && used+estInput+out > m.limits.MaxTokens {
		return &core.BudgetError{Kind: core.BudgetTokens, Limit: int64(m.limits.MaxTokens), Used: int64(used)}
	}
	if !info.Price.Known {
		return nil
	}
	projected := info.Price.Cost(core.Usage{Input: estInput, Output: out})
	if m.limits.MaxCost > 0 && cost+projected > m.limits.MaxCost {
		return &core.BudgetError{Kind: core.BudgetRunCost, Limit: int64(m.limits.MaxCost), Used: int64(cost)}
	}
	return m.s.checkProjected(ctx, m.user, projected)
}

func (m *meter) AfterCall(ctx context.Context, c core.CallInfo) error {
	m.mu.Lock()
	m.usage = m.usage.Add(c.Usage)
	m.cost += c.Cost
	m.mu.Unlock()
	return m.s.addSpend(ctx, m.user, c)
}

func (m *meter) ObserveTool(tool string, argsHash, resultHash uint64) core.LoopVerdict {
	l := m.s.cfg.Current().Budgets.Loop
	m.mu.Lock()
	defer m.mu.Unlock()
	if resultHash != 0 {
		m.tools++
	}
	return m.loop.observeTool(tool, argsHash, resultHash, l.WarnAfter, l.AbortAfter)
}

func (m *meter) ObserveText(hash uint64) core.LoopVerdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loop.observeText(hash)
}

func (m *meter) Pause() {
	m.mu.Lock()
	if m.pausedAt.IsZero() {
		m.pausedAt = m.s.clock.Now()
	}
	m.mu.Unlock()
}

func (m *meter) Resume() {
	m.mu.Lock()
	if !m.pausedAt.IsZero() {
		m.paused += m.s.clock.Now().Sub(m.pausedAt)
		m.pausedAt = time.Time{}
	}
	m.mu.Unlock()
}

// Deadline is zero without a time limit. While paused it keeps moving, so
// callers re-read it after Resume.
func (m *meter) Deadline() time.Time {
	if m.limits.MaxTime <= 0 {
		return time.Time{}
	}
	now := m.s.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	return now.Add(m.limits.MaxTime - m.activeLocked(now))
}

func (m *meter) Totals() core.RunTotals {
	now := m.s.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	return core.RunTotals{Steps: m.steps, ToolCalls: m.tools, Usage: m.usage, Cost: m.cost, Active: m.activeLocked(now)}
}

// Remaining reports zero for unlimited fields.
func (m *meter) Remaining() core.RunLimits {
	now := m.s.clock.Now()
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
		r.MaxTime = max(0, m.limits.MaxTime-m.activeLocked(now))
	}
	if m.limits.MaxCost > 0 {
		r.MaxCost = max(0, m.limits.MaxCost-m.cost)
	}
	return r
}
