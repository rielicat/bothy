package budget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// caps are the daily limits that apply to one principal: its own (an
// account's or a teammate's), the chat share every team-owned principal
// spends together, and the global one. A cap that is not set is unlimited;
// a cap set to zero allows no spend at all.
type caps struct {
	own, chat, global          core.Micros
	ownSet, chatSet, globalSet bool
}

// spend is today's spend under each cap.
type spend struct {
	own, chat, global core.Micros
}

// payer is what billing needs to know about a team-owned principal.
type payer struct {
	kind core.PrincipalKind
	key  string // a teammate's "<app>:<external id>", their per_user_daily_usd key
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

// payerOf looks a team-owned principal up once; ok is false when it is gone.
func (s *Service) payerOf(ctx context.Context, p core.PrincipalID) (payer, bool, error) {
	if v, ok := s.payers.Load(p); ok {
		return v.(payer), true, nil
	}
	pr, err := s.store.Principals().Get(ctx, core.SystemViewer(), p)
	if errors.Is(err, core.ErrNotFound) {
		return payer{}, false, nil
	}
	if err != nil {
		return payer{}, false, fmt.Errorf("budget: principal %s: %w", p, err)
	}
	py := payer{kind: pr.Kind}
	if pr.Kind == core.PrincipalPerson {
		id, err := s.store.Identities().ByPrincipal(ctx, p)
		switch {
		case err == nil:
			py.key = id.Key()
		case !errors.Is(err, core.ErrNotFound):
			return payer{}, false, fmt.Errorf("budget: identity of %s: %w", p, err)
		}
	}
	s.payers.Store(p, py)
	return py, true, nil
}

func (s *Service) capsFor(ctx context.Context, c *config.Config, p core.PrincipalID) (caps, error) {
	var out caps
	b := c.Budgets
	if b.DailyUSD > 0 {
		out.global, out.globalSet = core.MicrosFromUSD(b.DailyUSD), true
	}
	if p == "" {
		return out, nil
	}
	if core.ValidID(core.PrefixUser, string(p)) {
		return s.userCaps(ctx, c, core.UserID(p), out)
	}
	py, ok, err := s.payerOf(ctx, p)
	if err != nil || !ok {
		return out, err
	}
	if usd, ok := b.ChatShareUSD(); ok {
		out.chat, out.chatSet = core.MicrosFromUSD(usd), true
	}
	if py.kind == core.PrincipalPerson {
		out.own, out.ownSet = core.MicrosFromUSD(max(b.MemberDailyUSD, 0)), true
		if usd, ok := lookupCap(b.PerUserDailyUSD, py.key); ok && py.key != "" {
			out.own = core.MicrosFromUSD(max(usd, 0))
		}
	}
	return out, nil
}

// userCaps adds an account's own cap: its username's per_user_daily_usd
// entry, else member_daily_usd for members in multi mode.
func (s *Service) userCaps(ctx context.Context, c *config.Config, u core.UserID, out caps) (caps, error) {
	usr, err := s.store.Users().Get(ctx, u)
	if errors.Is(err, core.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("budget: user %s: %w", u, err)
	}
	if usd, ok := lookupCap(c.Budgets.PerUserDailyUSD, usr.Username); ok {
		out.own, out.ownSet = core.MicrosFromUSD(max(usd, 0)), true
		return out, nil
	}
	if c.Mode == "multi" && usr.Role == core.RoleMember {
		out.own, out.ownSet = core.MicrosFromUSD(max(c.Budgets.MemberDailyUSD, 0)), true
	}
	return out, nil
}

// lookupCap finds a per_user_daily_usd key regardless of case.
func lookupCap(m map[string]float64, key string) (float64, bool) {
	for name, usd := range m {
		if strings.EqualFold(name, key) {
			return usd, true
		}
	}
	return 0, false
}

func (s *Service) spendFor(ctx context.Context, day string, p core.PrincipalID, cp caps) (spend, error) {
	var out spend
	var err error
	if cp.ownSet {
		if out.own, err = s.store.Usage().Spend(ctx, p, day); err != nil {
			return out, fmt.Errorf("budget: spend of %s: %w", p, err)
		}
	}
	if cp.chatSet {
		if out.chat, err = s.store.Usage().TeamSpend(ctx, day); err != nil {
			return out, fmt.Errorf("budget: chat apps' spend: %w", err)
		}
	}
	if cp.globalSet {
		if out.global, err = s.store.Usage().Spend(ctx, "", day); err != nil {
			return out, fmt.Errorf("budget: global spend: %w", err)
		}
	}
	return out, nil
}

// exceeded reports the first cap that extra more micros would overrun: the
// principal's own, then the chat share, then the global one. With extra 0
// a cap is exhausted once spend reaches it.
func exceeded(cp caps, sp spend, extra core.Micros) error {
	over := func(spent, limit core.Micros) bool {
		if extra == 0 {
			return spent >= limit
		}
		return spent+extra > limit
	}
	daily := func(limit, used core.Micros, scope string) error {
		return &core.BudgetError{Kind: core.BudgetDailyCost, Limit: int64(limit), Used: int64(used), Scope: scope}
	}
	switch {
	case cp.ownSet && over(sp.own, cp.own):
		return daily(cp.own, sp.own, core.BudgetPersonal)
	case cp.chatSet && over(sp.chat, cp.chat):
		return daily(cp.chat, sp.chat, core.BudgetChat)
	case cp.globalSet && over(sp.global, cp.global):
		return daily(cp.global, sp.global, core.BudgetGlobal)
	}
	return nil
}

func (s *Service) CheckDaily(ctx context.Context, p core.PrincipalID) error {
	c := s.cfg.Current()
	day := s.day(c)
	s.rollover(day)
	cp, err := s.capsFor(ctx, c, p)
	if err != nil {
		return err
	}
	sp, err := s.spendFor(ctx, day, p, cp)
	if err != nil {
		return err
	}
	return exceeded(cp, sp, 0)
}

// checkProjected is BeforeCall's daily part: projected is the cost of the
// call about to be made.
func (s *Service) checkProjected(ctx context.Context, p core.PrincipalID, projected core.Micros) error {
	c := s.cfg.Current()
	cp, err := s.capsFor(ctx, c, p)
	if err != nil {
		return err
	}
	sp, err := s.spendFor(ctx, s.day(c), p, cp)
	if err != nil {
		return err
	}
	return exceeded(cp, sp, max(projected, 0))
}

// Daily reports the cap closest to running out, with the spend under it:
// the principal's own, the chat share or the global cap. limit is 0 when
// none is set; spent is then the principal's own spend.
func (s *Service) Daily(ctx context.Context, p core.PrincipalID) (spent, limit core.Micros, err error) {
	c := s.cfg.Current()
	day := s.day(c)
	cp, err := s.capsFor(ctx, c, p)
	if err != nil {
		return 0, 0, err
	}
	all := cp
	all.ownSet = true // the principal's own spend is reported even without a cap
	sp, err := s.spendFor(ctx, day, p, all)
	if err != nil {
		return 0, 0, err
	}
	spent, limit = sp.own, 0
	found := false
	for _, x := range []struct {
		set         bool
		spent, have core.Micros
	}{{cp.ownSet, sp.own, cp.own}, {cp.chatSet, sp.chat, cp.chat}, {cp.globalSet, sp.global, cp.global}} {
		if x.set && (!found || x.have-x.spent < limit-spent) {
			spent, limit, found = x.spent, x.have, true
		}
	}
	return spent, limit, nil
}

// userOf is the account that pays for p: its user for a user principal,
// nobody for a team-owned one.
func userOf(p core.PrincipalID) core.UserID {
	if core.ValidID(core.PrefixUser, string(p)) {
		return core.UserID(p)
	}
	return ""
}

func (s *Service) RecordSpend(ctx context.Context, p core.PrincipalID, c core.CallInfo) error {
	if c.Purpose == "" {
		c.Purpose = "passthrough"
	}
	if err := s.store.Runs().AddLLMCall(ctx, "", p, userOf(p), c); err != nil {
		return fmt.Errorf("budget: record llm call: %w", err)
	}
	return s.addSpend(ctx, p, c)
}

func (s *Service) addSpend(ctx context.Context, p core.PrincipalID, c core.CallInfo) error {
	cfg := s.cfg.Current()
	day := s.day(cfg)
	if err := s.store.Usage().AddSpend(ctx, p, day, c); err != nil {
		return fmt.Errorf("budget: add spend: %w", err)
	}
	if c.Cost > 0 {
		s.warn(ctx, cfg, day, p)
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
	payer core.PrincipalID // "" with global or chat
	scope string           // personal | chat | global
}

// warn publishes budget.warning and a budget_80 banner the first time a cap
// crosses budgets.warn_ratio on a given day; for the chat share it raises
// the admins' chat_budget_80 banner instead.
func (s *Service) warn(ctx context.Context, c *config.Config, day string, p core.PrincipalID) {
	if s.bus == nil {
		return
	}
	ratio := c.Budgets.WarnRatio
	if ratio <= 0 || ratio > 1 {
		ratio = 0.8
	}
	cp, err := s.capsFor(ctx, c, p)
	if err != nil {
		s.log.Warn("budget: warning check", "err", err)
		return
	}
	sp, err := s.spendFor(ctx, day, p, cp)
	if err != nil {
		s.log.Warn("budget: warning check", "err", err)
		return
	}
	crossed := func(spent, limit core.Micros) bool {
		return limit > 0 && float64(spent) >= ratio*float64(limit)
	}
	if cp.ownSet && crossed(sp.own, cp.own) {
		s.publishWarning(warnKey{payer: p, scope: core.BudgetPersonal}, day, p, userOf(p), sp.own, cp.own)
	}
	if cp.chatSet && crossed(sp.chat, cp.chat) && s.firstToday(warnKey{scope: core.BudgetChat}, day) {
		s.bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{
			Level:  "warn",
			Code:   "chat_budget_80",
			Text:   fmt.Sprintf("Chat apps have used %d%% of today's %s limit.", sp.chat*100/cp.chat, money(cp.chat)),
			Action: "/activity/spending",
		}})
	}
	if cp.globalSet && crossed(sp.global, cp.global) {
		s.publishWarning(warnKey{scope: core.BudgetGlobal}, day, "", "", sp.global, cp.global)
	}
}

// firstToday reports whether k has not warned today, marking it.
func (s *Service) firstToday(k warnKey, day string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.warned[k] == day {
		return false
	}
	s.warned[k] = day
	return true
}

func (s *Service) publishWarning(k warnKey, day string, p core.PrincipalID, u core.UserID, spent, limit core.Micros) {
	if !s.firstToday(k, day) {
		return
	}
	s.bus.Publish(core.Event{Type: core.EvBudgetWarning, Principal: p, Data: core.BudgetWarningData{User: u, Spent: spent, Limit: limit}})
	who := "your"
	if k.scope == core.BudgetGlobal {
		who = "the instance's"
	}
	s.bus.Publish(core.Event{Type: core.EvBanner, Principal: p, Data: core.BannerData{
		Level:  "warn",
		Code:   "budget_80",
		Text:   fmt.Sprintf("%d%% of %s daily budget is used (%s of %s).", spent*100/limit, who, spent, limit),
		Action: "/settings/budgets",
	}})
}

// money is a whole-cent dollar amount for banner copy (docs/design/ui.md §14.2).
func money(m core.Micros) string { return fmt.Sprintf("$%.2f", m.USD()) }

// rollover clears the budget banners of previous days.
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
		code := "budget_80"
		if k.scope == core.BudgetChat {
			code = "chat_budget_80"
		}
		s.bus.Publish(core.Event{Type: core.EvBanner, Principal: k.payer, Data: core.BannerData{Code: code, Clear: true}})
	}
}
