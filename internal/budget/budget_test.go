package budget

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

type env struct {
	svc   *Service
	store core.Store
	bus   *events.Bus
	clock *fakes.Clock
	cfg   *config.Config
	owner core.UserID
	alice core.UserID // member
}

func newEnv(t *testing.T, mut func(c *config.Config)) *env {
	t.Helper()
	ctx := context.Background()
	cfg := config.Default()
	cfg.Mode = "multi"
	if mut != nil {
		mut(cfg)
	}
	st := memstore.New()
	clock := fakes.NewClock(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC))
	owner := &core.User{Username: "root", Role: core.RoleOwner, PWHash: "x", CreatedAt: clock.Now()}
	alice := &core.User{Username: "Alice", Role: core.RoleMember, PWHash: "x", CreatedAt: clock.Now()}
	for _, u := range []*core.User{owner, alice} {
		if err := st.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	bus := events.New(events.Options{Clock: clock})
	svc, err := New(Deps{Store: st, Config: config.Static(cfg), Bus: bus, Audit: &fakes.Auditor{}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, store: st, bus: bus, clock: clock, cfg: cfg, owner: owner.ID, alice: alice.ID}
}

func (e *env) spend(t *testing.T, u core.UserID, usd float64) {
	t.Helper()
	if err := e.svc.RecordSpend(context.Background(), u, core.CallInfo{Purpose: "probe", Cost: core.MicrosFromUSD(usd), PriceKnown: true}); err != nil {
		t.Fatal(err)
	}
}

func budgetKind(err error) core.BudgetKind {
	var be *core.BudgetError
	if errors.As(err, &be) {
		return be.Kind
	}
	return ""
}

func TestNewRequiresStoreAndConfig(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New without deps succeeded")
	}
}

func TestDefaultLimits(t *testing.T) {
	e := newEnv(t, nil)
	got := e.svc.DefaultLimits(core.TrigMessage)
	want := core.RunLimits{MaxSteps: 25, MaxTokens: 400_000, MaxTime: 10 * time.Minute}
	if got != want {
		t.Errorf("interactive = %+v, want %+v", got, want)
	}
	if got := e.svc.DefaultLimits(core.TrigAPI); got != want {
		t.Errorf("api = %+v, want interactive defaults", got)
	}
	want = core.RunLimits{MaxSteps: 15, MaxTokens: 150_000, MaxTime: 15 * time.Minute, MaxCost: 250_000}
	for _, tr := range []core.Trigger{core.TrigSchedule, core.TrigHeartbeat} {
		if got := e.svc.DefaultLimits(tr); got != want {
			t.Errorf("%s = %+v, want %+v", tr, got, want)
		}
	}
}

func TestCheckDailyCaps(t *testing.T) {
	ctx := context.Background()
	t.Run("member cap in multi mode", func(t *testing.T) {
		e := newEnv(t, nil) // member_daily_usd 2.00, daily_usd 5.00
		e.spend(t, e.alice, 1.99)
		if err := e.svc.CheckDaily(ctx, e.alice); err != nil {
			t.Fatalf("under cap: %v", err)
		}
		e.spend(t, e.alice, 0.01)
		err := e.svc.CheckDaily(ctx, e.alice)
		var be *core.BudgetError
		if !errors.As(err, &be) || be.Kind != core.BudgetDailyCost || be.Limit != 2_000_000 || be.Used != 2_000_000 {
			t.Fatalf("at cap: %v (%+v)", err, be)
		}
		if err := e.svc.CheckDaily(ctx, e.owner); err != nil {
			t.Fatalf("owner has no per-user cap: %v", err)
		}
	})
	t.Run("single mode has no member cap", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Mode = "single" })
		e.spend(t, e.alice, 3)
		if err := e.svc.CheckDaily(ctx, e.alice); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("global cap sums every user", func(t *testing.T) {
		e := newEnv(t, nil)
		e.spend(t, e.alice, 1.5)
		e.spend(t, e.owner, 3.5)
		if k := budgetKind(e.svc.CheckDaily(ctx, e.owner)); k != core.BudgetDailyCost {
			t.Fatalf("owner over global cap: kind %q", k)
		}
	})
	t.Run("per-user override matches username case-insensitively", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Budgets.PerUserDailyUSD = map[string]float64{"alice": 0.5, "ROOT": 1} })
		e.spend(t, e.alice, 0.6)
		if k := budgetKind(e.svc.CheckDaily(ctx, e.alice)); k != core.BudgetDailyCost {
			t.Fatalf("alice override: kind %q", k)
		}
		e.spend(t, e.owner, 1)
		if k := budgetKind(e.svc.CheckDaily(ctx, e.owner)); k != core.BudgetDailyCost {
			t.Fatalf("owner override: kind %q", k)
		}
	})
	t.Run("zero cap blocks, zero global is unlimited", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) {
			c.Budgets.DailyUSD = 0
			c.Budgets.PerUserDailyUSD = map[string]float64{"alice": 0}
		})
		if k := budgetKind(e.svc.CheckDaily(ctx, e.alice)); k != core.BudgetDailyCost {
			t.Fatalf("zero per-user cap: kind %q", k)
		}
		e.spend(t, e.owner, 1000)
		if err := e.svc.CheckDaily(ctx, e.owner); err != nil {
			t.Fatalf("unlimited global: %v", err)
		}
	})
	t.Run("new day resets spend", func(t *testing.T) {
		e := newEnv(t, nil)
		e.spend(t, e.alice, 2)
		e.clock.Advance(14 * time.Hour)
		if err := e.svc.CheckDaily(ctx, e.alice); err != nil {
			t.Fatalf("next day: %v", err)
		}
	})
	t.Run("spend day follows the configured timezone", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Timezone = "Pacific/Auckland" })
		// 10:00 UTC on 12 Oct is 23:00 in Auckland (NZDT, +13).
		e.spend(t, e.alice, 2)
		e.clock.Advance(90 * time.Minute) // 00:30 on 13 Oct in Auckland
		if err := e.svc.CheckDaily(ctx, e.alice); err != nil {
			t.Fatalf("local midnight passed: %v", err)
		}
	})
}

func TestDailyReportsTheBindingCap(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	e.spend(t, e.alice, 0.5)
	e.spend(t, e.owner, 1)
	spent, limit, err := e.svc.Daily(ctx, e.alice)
	if err != nil || spent != 500_000 || limit != 2_000_000 {
		t.Fatalf("alice = %v/%v %v, want her own cap", spent, limit, err)
	}
	e.spend(t, e.owner, 2.8) // global 4.3 of 5: 0.7 left < alice's 1.5
	if spent, limit, _ = e.svc.Daily(ctx, e.alice); spent != 4_300_000 || limit != 5_000_000 {
		t.Fatalf("alice near global cap = %v/%v, want the global cap", spent, limit)
	}
	e2 := newEnv(t, func(c *config.Config) { c.Budgets.DailyUSD = 0 })
	e2.spend(t, e2.owner, 1)
	if spent, limit, _ = e2.svc.Daily(ctx, e2.owner); spent != 1_000_000 || limit != 0 {
		t.Fatalf("no caps = %v/%v", spent, limit)
	}
}

func TestRecordSpendWritesCallAndSpend(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	if err := e.svc.RecordSpend(ctx, e.owner, core.CallInfo{Cost: 1234, Usage: core.Usage{Input: 10, Output: 5}}); err != nil {
		t.Fatal(err)
	}
	got, err := e.store.Usage().Spend(ctx, e.owner, "2026-10-12")
	if err != nil || got != 1234 {
		t.Fatalf("spend = %v %v", got, err)
	}
	rows, err := e.store.Usage().Report(ctx, core.UsageQuery{GroupBy: "user"})
	if err != nil || len(rows) != 1 || rows[0].Calls != 1 || rows[0].Key != string(e.owner) {
		t.Fatalf("llm_calls report = %+v %v", rows, err)
	}
}

func TestWarningAtRatioOncePerDay(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	sub := e.bus.Subscribe(core.Filter{System: true}, 64)
	defer sub.Close()
	e.spend(t, e.alice, 1.5)
	e.spend(t, e.alice, 0.1) // 1.6 of 2.0 = 80%
	e.spend(t, e.alice, 0.1)
	var warnings, banners int
	drain := func() {
		for {
			select {
			case ev := <-sub.C():
				switch d := ev.Data.(type) {
				case core.BudgetWarningData:
					warnings++
					if d.User != e.alice || d.Limit != 2_000_000 || ev.Principal != core.UserPrincipal(e.alice) {
						t.Errorf("warning = %+v to %q", d, ev.Principal)
					}
				case core.BannerData:
					if d.Code != "budget_80" {
						t.Errorf("banner code %q", d.Code)
					}
					if !d.Clear {
						banners++
					}
				}
			default:
				return
			}
		}
	}
	drain()
	if warnings != 1 || banners != 1 {
		t.Fatalf("warnings=%d banners=%d, want 1 each", warnings, banners)
	}
	if active := e.bus.Banners(core.Filter{Principals: []core.PrincipalID{core.UserPrincipal(e.alice)}}); len(active) != 1 {
		t.Fatalf("active banners = %d", len(active))
	}
	e.clock.Advance(24 * time.Hour)
	if err := e.svc.CheckDaily(ctx, e.alice); err != nil {
		t.Fatal(err)
	}
	if active := e.bus.Banners(core.Filter{Principals: []core.PrincipalID{core.UserPrincipal(e.alice)}}); len(active) != 0 {
		t.Fatalf("banner survived the day change: %d", len(active))
	}
	e.spend(t, e.alice, 1.7)
	drain()
	if warnings != 2 {
		t.Fatalf("second day warnings = %d, want 2", warnings)
	}
}

func TestMeterSteps(t *testing.T) {
	e := newEnv(t, nil)
	m := e.svc.StartRun("run_x", e.owner, core.RunLimits{MaxSteps: 2})
	for i := range 2 {
		if err := m.Step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	err := m.Step()
	var be *core.BudgetError
	if !errors.As(err, &be) || be.Kind != core.BudgetSteps || be.Limit != 2 || be.Used != 2 {
		t.Fatalf("third step: %v", err)
	}
	if r := m.Remaining(); r.MaxSteps != 0 {
		t.Fatalf("remaining steps = %d", r.MaxSteps)
	}
}

func TestMeterTimeExcludesPauses(t *testing.T) {
	e := newEnv(t, nil)
	m := e.svc.StartRun("run_x", e.owner, core.RunLimits{MaxTime: time.Minute})
	start := e.clock.Now()
	if got := m.Deadline(); !got.Equal(start.Add(time.Minute)) {
		t.Fatalf("deadline = %v", got)
	}
	e.clock.Advance(40 * time.Second)
	m.Pause()
	e.clock.Advance(time.Hour) // approval wait
	m.Resume()
	if err := m.Step(); err != nil {
		t.Fatalf("after pause: %v", err)
	}
	if got := m.Deadline(); !got.Equal(e.clock.Now().Add(20 * time.Second)) {
		t.Fatalf("deadline after pause = %v, want now+20s", got)
	}
	if tot := m.Totals(); tot.Active != 40*time.Second {
		t.Fatalf("active = %v", tot.Active)
	}
	e.clock.Advance(20 * time.Second)
	if k := budgetKind(m.Step()); k != core.BudgetTime {
		t.Fatalf("over time: kind %q", k)
	}
	if got := e.svc.StartRun("run_y", e.owner, core.RunLimits{}).Deadline(); !got.IsZero() {
		t.Fatalf("unlimited deadline = %v", got)
	}
}

func TestMeterBeforeCall(t *testing.T) {
	ctx := context.Background()
	priced := core.ModelInfo{MaxOutput: 1000, Price: core.Price{InPerM: 3_000_000, OutPerM: 15_000_000, Known: true}}
	unknown := core.ModelInfo{MaxOutput: 1000}
	t.Run("tokens", func(t *testing.T) {
		e := newEnv(t, nil)
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{MaxTokens: 10_000})
		if err := m.BeforeCall(ctx, 8_999, unknown); err != nil {
			t.Fatalf("fits: %v", err)
		}
		if err := m.AfterCall(ctx, core.CallInfo{Usage: core.Usage{Input: 4000, CacheRead: 1000, Output: 500}}); err != nil {
			t.Fatal(err)
		}
		if k := budgetKind(m.BeforeCall(ctx, 3600, unknown)); k != core.BudgetTokens {
			t.Fatalf("projection over tokens (unknown price still checked): kind %q", k)
		}
		if r := m.Remaining(); r.MaxTokens != 4500 {
			t.Fatalf("remaining tokens = %d", r.MaxTokens)
		}
	})
	t.Run("run cost", func(t *testing.T) {
		e := newEnv(t, nil)
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{MaxCost: 20_000})
		// 1000 in @ $3/M + 1000 out @ $15/M = $0.018
		if err := m.BeforeCall(ctx, 1000, priced); err != nil {
			t.Fatalf("fits: %v", err)
		}
		if err := m.AfterCall(ctx, core.CallInfo{Cost: 5000}); err != nil {
			t.Fatal(err)
		}
		if k := budgetKind(m.BeforeCall(ctx, 1000, priced)); k != core.BudgetRunCost {
			t.Fatalf("over run cost: kind %q", k)
		}
		if err := m.BeforeCall(ctx, 1_000_000, unknown); err != nil {
			t.Fatalf("unknown price skips $ checks: %v", err)
		}
	})
	t.Run("daily projection", func(t *testing.T) {
		e := newEnv(t, nil)
		e.spend(t, e.alice, 1.99)
		m := e.svc.StartRun("run_x", e.alice, core.RunLimits{})
		if k := budgetKind(m.BeforeCall(ctx, 1000, priced)); k != core.BudgetDailyCost {
			t.Fatalf("daily projection: kind %q", k)
		}
		if err := m.BeforeCall(ctx, 1000, unknown); err != nil {
			t.Fatalf("unknown price: %v", err)
		}
		local := core.ModelInfo{MaxOutput: 1000, Price: core.Price{Known: true}}
		if err := m.BeforeCall(ctx, 1000, local); err != nil {
			t.Fatalf("free local model: %v", err)
		}
	})
}

func TestMeterAfterCallTotalsAndSpend(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	m := e.svc.StartRun("run_x", e.owner, core.RunLimits{MaxCost: 1_000_000})
	for range 2 {
		if err := m.AfterCall(ctx, core.CallInfo{Cost: 100, Usage: core.Usage{Input: 1, Output: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	tot := m.Totals()
	if tot.Cost != 200 || tot.Usage.Input != 2 || tot.Usage.Output != 4 {
		t.Fatalf("totals = %+v", tot)
	}
	if r := m.Remaining(); r.MaxCost != 999_800 {
		t.Fatalf("remaining cost = %v", r.MaxCost)
	}
	if got, _ := e.store.Usage().Spend(ctx, e.owner, "2026-10-12"); got != 200 {
		t.Fatalf("spend_daily = %v", got)
	}
}

func TestLoopDetection(t *testing.T) {
	e := newEnv(t, nil)
	t.Run("warn at 3 then abort before the 5th identical call", func(t *testing.T) {
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{})
		for i := 1; i <= 4; i++ {
			if v := m.ObserveTool("shell", 7, 0); v != core.LoopOK {
				t.Fatalf("pre-check %d = %v", i, v)
			}
			want := core.LoopOK
			if i >= 3 {
				want = core.LoopWarn
			}
			if v := m.ObserveTool("shell", 7, uint64(100+i)); v != want {
				t.Fatalf("call %d = %v, want %v", i, v, want)
			}
		}
		if v := m.ObserveTool("shell", 7, 0); v != core.LoopAbort {
			t.Fatalf("5th pre-check = %v, want abort", v)
		}
		if tot := m.Totals(); tot.ToolCalls != 4 {
			t.Fatalf("tool calls = %d", tot.ToolCalls)
		}
	})
	t.Run("identical result three times aborts", func(t *testing.T) {
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{})
		m.ObserveTool("fs_read", 1, 9)
		m.ObserveTool("fs_read", 1, 9)
		if v := m.ObserveTool("fs_read", 1, 9); v != core.LoopAbort {
			t.Fatalf("got %v", v)
		}
	})
	t.Run("A-B-A-B over 8 entries aborts", func(t *testing.T) {
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{})
		var v core.LoopVerdict
		for i := range 8 {
			args := uint64(1 + i%2)
			v = m.ObserveTool("t", args, uint64(1000+i))
			if i < 7 && v == core.LoopAbort {
				t.Fatalf("aborted early at %d", i)
			}
		}
		if v != core.LoopAbort {
			t.Fatalf("got %v", v)
		}
	})
	t.Run("varied calls never trip", func(t *testing.T) {
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{})
		for i := range 40 {
			if v := m.ObserveTool("t", uint64(i), uint64(i+1)); v != core.LoopOK {
				t.Fatalf("call %d = %v", i, v)
			}
		}
	})
	t.Run("config thresholds apply", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Budgets.Loop = config.Loop{WarnAfter: 2, AbortAfter: 3} })
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{})
		m.ObserveTool("t", 1, 1)
		if v := m.ObserveTool("t", 1, 2); v != core.LoopWarn {
			t.Fatalf("warn = %v", v)
		}
		if v := m.ObserveTool("t", 1, 0); v != core.LoopAbort {
			t.Fatalf("abort pre-check = %v", v)
		}
	})
	t.Run("identical text three steps in a row", func(t *testing.T) {
		m := e.svc.StartRun("run_x", e.owner, core.RunLimits{})
		if m.ObserveText(5) != core.LoopOK || m.ObserveText(5) != core.LoopOK {
			t.Fatal("tripped early")
		}
		if v := m.ObserveText(5); v != core.LoopAbort {
			t.Fatalf("third = %v", v)
		}
		m2 := e.svc.StartRun("run_y", e.owner, core.RunLimits{})
		for _, h := range []uint64{5, 5, 0, 5, 6, 5} {
			if v := m2.ObserveText(h); v != core.LoopOK {
				t.Fatalf("broken streak tripped on %d", h)
			}
		}
	})
}

func TestAllowSender(t *testing.T) {
	e := newEnv(t, nil) // 10/min, burst 5
	for i := range 5 {
		if ok, _ := e.svc.AllowSender("telegram:1"); !ok {
			t.Fatalf("burst message %d refused", i)
		}
	}
	ok, retry := e.svc.AllowSender("telegram:1")
	if ok || retry <= 0 || retry > 6*time.Second {
		t.Fatalf("over burst = %v retry %v", ok, retry)
	}
	if ok, _ := e.svc.AllowSender("telegram:2"); !ok {
		t.Fatal("other sender limited")
	}
	e.clock.Advance(6 * time.Second)
	if ok, _ := e.svc.AllowSender("telegram:1"); !ok {
		t.Fatal("token not refilled after 6s")
	}
	if ok, _ := e.svc.AllowSender("telegram:1"); ok {
		t.Fatal("refill exceeded the rate")
	}
	e2 := newEnv(t, func(c *config.Config) { c.Budgets.SenderRate.PerMinute = 0 })
	for range 100 {
		if ok, _ := e2.svc.AllowSender("x"); !ok {
			t.Fatal("disabled limiter refused")
		}
	}
}

func TestSenderLimiterEvictsLRU(t *testing.T) {
	l := newSenderLimiter(3)
	now := time.Unix(0, 0)
	l.allow(now, "a", 1, 1)
	l.allow(now, "b", 1, 1)
	l.allow(now, "c", 1, 1)
	if ok, _ := l.allow(now, "a", 1, 1); ok {
		t.Fatal("a should be exhausted")
	}
	l.allow(now, "d", 1, 1) // evicts b, the least recently used
	if len(l.keys) != 3 {
		t.Fatalf("keys = %d", len(l.keys))
	}
	if _, ok := l.keys["b"]; ok {
		t.Fatal("b not evicted")
	}
	if ok, _ := l.allow(now, "b", 1, 1); !ok {
		t.Fatal("evicted sender should start with a full bucket")
	}
}
