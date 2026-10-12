package budget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// caps are the daily limits that apply to one user. A cap that is not set is
// unlimited; a cap set to zero allows no spend at all.
type caps struct {
	user, global       core.Micros
	userSet, globalSet bool
}

// spend is today's spend under each cap.
type spend struct {
	user, global core.Micros
}

func (s *Service) location(c *config.Config) *time.Location {
	name := c.Timezone
	if name == "" || name == "UTC" {
		return time.UTC
	}
	if l, ok := s.zones.Load(name); ok {
		return l.(*time.Location)
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		s.log.Warn("budget: unknown timezone, spend days use UTC", "timezone", name, "err", err)
		l = time.UTC
	}
	s.zones.Store(name, l)
	return l
}

// day is the spend_daily key: the local date in the configured timezone.
func (s *Service) day(c *config.Config) string {
	return s.clock.Now().In(s.location(c)).Format(time.DateOnly)
}

func (s *Service) capsFor(ctx context.Context, c *config.Config, u core.UserID) (caps, error) {
	var out caps
	if c.Budgets.DailyUSD > 0 {
		out.global, out.globalSet = core.MicrosFromUSD(c.Budgets.DailyUSD), true
	}
	if u == "" {
		return out, nil
	}
	usr, err := s.store.Users().Get(ctx, u)
	if errors.Is(err, core.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("budget: user %s: %w", u, err)
	}
	for name, usd := range c.Budgets.PerUserDailyUSD {
		if strings.EqualFold(name, usr.Username) {
			out.user, out.userSet = core.MicrosFromUSD(max(usd, 0)), true
			return out, nil
		}
	}
	if c.Mode == "multi" && usr.Role == core.RoleMember {
		out.user, out.userSet = core.MicrosFromUSD(max(c.Budgets.MemberDailyUSD, 0)), true
	}
	return out, nil
}

func (s *Service) spendFor(ctx context.Context, day string, u core.UserID, cp caps) (spend, error) {
	var out spend
	var err error
	if cp.userSet {
		if out.user, err = s.store.Usage().Spend(ctx, u, day); err != nil {
			return out, fmt.Errorf("budget: spend of %s: %w", u, err)
		}
	}
	if cp.globalSet {
		if out.global, err = s.store.Usage().Spend(ctx, "", day); err != nil {
			return out, fmt.Errorf("budget: global spend: %w", err)
		}
	}
	return out, nil
}

// exceeded reports the first cap that extra more micros would overrun. With
// extra 0 a cap is exhausted once spend reaches it.
func exceeded(cp caps, sp spend, extra core.Micros) error {
	over := func(spent, limit core.Micros) bool {
		if extra == 0 {
			return spent >= limit
		}
		return spent+extra > limit
	}
	if cp.userSet && over(sp.user, cp.user) {
		return &core.BudgetError{Kind: core.BudgetDailyCost, Limit: int64(cp.user), Used: int64(sp.user)}
	}
	if cp.globalSet && over(sp.global, cp.global) {
		return &core.BudgetError{Kind: core.BudgetDailyCost, Limit: int64(cp.global), Used: int64(sp.global)}
	}
	return nil
}

func (s *Service) CheckDaily(ctx context.Context, u core.UserID) error {
	c := s.cfg.Current()
	day := s.day(c)
	s.rollover(day)
	cp, err := s.capsFor(ctx, c, u)
	if err != nil {
		return err
	}
	sp, err := s.spendFor(ctx, day, u, cp)
	if err != nil {
		return err
	}
	return exceeded(cp, sp, 0)
}

// checkProjected is BeforeCall's daily part: projected is the cost of the
// call about to be made.
func (s *Service) checkProjected(ctx context.Context, u core.UserID, projected core.Micros) error {
	c := s.cfg.Current()
	cp, err := s.capsFor(ctx, c, u)
	if err != nil {
		return err
	}
	sp, err := s.spendFor(ctx, s.day(c), u, cp)
	if err != nil {
		return err
	}
	return exceeded(cp, sp, max(projected, 0))
}

// Daily reports the cap closest to running out: the user's own cap when that
// leaves less room than the global one, otherwise the global cap with the
// global spend. limit is 0 when neither cap is set.
func (s *Service) Daily(ctx context.Context, u core.UserID) (spent, limit core.Micros, err error) {
	c := s.cfg.Current()
	day := s.day(c)
	cp, err := s.capsFor(ctx, c, u)
	if err != nil {
		return 0, 0, err
	}
	all := cp
	all.userSet = true // the user's own spend is reported even without a cap
	sp, err := s.spendFor(ctx, day, u, all)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case cp.userSet && (!cp.globalSet || cp.user-sp.user <= cp.global-sp.global):
		return sp.user, cp.user, nil
	case cp.globalSet:
		return sp.global, cp.global, nil
	}
	return sp.user, 0, nil
}

func (s *Service) RecordSpend(ctx context.Context, u core.UserID, c core.CallInfo) error {
	if c.Purpose == "" {
		c.Purpose = "passthrough"
	}
	if err := s.store.Runs().AddLLMCall(ctx, "", core.UserPrincipal(u), u, c); err != nil {
		return fmt.Errorf("budget: record llm call: %w", err)
	}
	return s.addSpend(ctx, u, c)
}

func (s *Service) addSpend(ctx context.Context, u core.UserID, c core.CallInfo) error {
	cfg := s.cfg.Current()
	day := s.day(cfg)
	if err := s.store.Usage().AddSpend(ctx, u, day, c); err != nil {
		return fmt.Errorf("budget: add spend: %w", err)
	}
	if c.Cost > 0 {
		s.warn(ctx, cfg, day, u)
	}
	return nil
}

func (s *Service) DefaultLimits(t core.Trigger) core.RunLimits {
	b := s.cfg.Current().Budgets
	rb := b.Run
	if t.Scheduled() {
		rb = b.Scheduled
	}
	l := core.RunLimits{MaxSteps: rb.MaxSteps, MaxTokens: rb.MaxTokens, MaxTime: rb.MaxTime.D()}
	if rb.MaxUSD > 0 {
		l.MaxCost = core.MicrosFromUSD(rb.MaxUSD)
	}
	return l
}

func (s *Service) AllowSender(key string) (bool, time.Duration) {
	r := s.cfg.Current().Budgets.SenderRate
	return s.senders.allow(s.clock.Now(), key, r.PerMinute, r.Burst)
}

type warnKey struct {
	user   core.UserID // "" = the global cap
	global bool
}

// warn publishes budget.warning and a budget_80 banner the first time a cap
// crosses budgets.warn_ratio on a given day.
func (s *Service) warn(ctx context.Context, c *config.Config, day string, u core.UserID) {
	if s.bus == nil {
		return
	}
	ratio := c.Budgets.WarnRatio
	if ratio <= 0 || ratio > 1 {
		ratio = 0.8
	}
	cp, err := s.capsFor(ctx, c, u)
	if err != nil {
		s.log.Warn("budget: warning check", "err", err)
		return
	}
	sp, err := s.spendFor(ctx, day, u, cp)
	if err != nil {
		s.log.Warn("budget: warning check", "err", err)
		return
	}
	crossed := func(spent, limit core.Micros) bool {
		return limit > 0 && float64(spent) >= ratio*float64(limit)
	}
	if cp.userSet && crossed(sp.user, cp.user) {
		s.publishWarning(warnKey{user: u}, day, core.UserPrincipal(u), u, sp.user, cp.user)
	}
	if cp.globalSet && crossed(sp.global, cp.global) {
		s.publishWarning(warnKey{global: true}, day, "", "", sp.global, cp.global)
	}
}

func (s *Service) publishWarning(k warnKey, day string, p core.PrincipalID, u core.UserID, spent, limit core.Micros) {
	s.mu.Lock()
	if s.warned[k] == day {
		s.mu.Unlock()
		return
	}
	s.warned[k] = day
	s.mu.Unlock()
	s.bus.Publish(core.Event{Type: core.EvBudgetWarning, Principal: p, Data: core.BudgetWarningData{User: u, Spent: spent, Limit: limit}})
	who := "your"
	if k.global {
		who = "the instance's"
	}
	s.bus.Publish(core.Event{Type: core.EvBanner, Principal: p, Data: core.BannerData{
		Level:  "warn",
		Code:   "budget_80",
		Text:   fmt.Sprintf("%d%% of %s daily budget is used (%s of %s).", spent*100/limit, who, spent, limit),
		Action: "/settings/budgets",
	}})
}

// rollover clears the budget_80 banners of previous days.
func (s *Service) rollover(day string) {
	if s.bus == nil {
		return
	}
	s.mu.Lock()
	var stale []warnKey
	for k, d := range s.warned {
		if d != day {
			stale = append(stale, k)
			delete(s.warned, k)
		}
	}
	s.mu.Unlock()
	for _, k := range stale {
		p := core.UserPrincipal(k.user)
		if k.global {
			p = ""
		}
		s.bus.Publish(core.Event{Type: core.EvBanner, Principal: p, Data: core.BannerData{Code: "budget_80", Clear: true}})
	}
}
