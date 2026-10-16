package config

import "testing"

func TestBudgetKeysAndChatShare(t *testing.T) {
	if d := Default().Budgets; d.ChatSharePct != 60 || d.MemberDailyUSD != 2 {
		t.Fatalf("budget defaults = %+v", d)
	}
	for _, c := range []struct {
		share int
		keys  map[string]float64
		bad   []string
	}{
		{share: 60, keys: map[string]float64{"alice": 3, "telegram:5001": 4, "slack:U01SAM": 1, "discord:42": 0}},
		{share: 100},
		{share: 1},
		{share: 0, bad: []string{"budgets.chat_share_pct"}},
		{share: 101, bad: []string{"budgets.chat_share_pct"}},
		{share: 60, keys: map[string]float64{"matrix:@a:b": 1, "slack:": 1}, bad: []string{
			"budgets.per_user_daily_usd.matrix:@a:b", "budgets.per_user_daily_usd.slack:"}},
	} {
		cfg := Default()
		cfg.Budgets.ChatSharePct, cfg.Budgets.PerUserDailyUSD = c.share, c.keys
		v := &vctx{layerOf: func(string) int { return 0 }}
		validate(cfg, v)
		got := map[string]bool{}
		for _, e := range v.errs {
			got[e.path] = true
		}
		if len(got) != len(c.bad) {
			t.Errorf("share %d, keys %v: errors %+v, want %v", c.share, c.keys, v.errs, c.bad)
		}
		for _, p := range c.bad {
			if !got[p] {
				t.Errorf("share %d, keys %v: no error at %s (%+v)", c.share, c.keys, p, v.errs)
			}
		}
	}
}
