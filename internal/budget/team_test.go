package budget

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// teammate gives e a person in a chat app with their person space.
func (e *env) teammate(t *testing.T, channel, ext string) core.PrincipalID {
	t.Helper()
	ctx := context.Background()
	now := e.clock.Now()
	sp := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: channel, ChatID: ext, Kind: core.ChatDM}, Title: ext,
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := e.store.Spaces().Create(ctx, sp, &core.Principal{DisplayName: ext, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	id, err := e.store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: channel, ExternalID: ext, LastSeen: now})
	if err != nil {
		t.Fatal(err)
	}
	id.Status, id.Principal, id.Via = core.IdentityTeammate, sp.Principal, core.ViaAdmin
	if err := e.store.Identities().Update(ctx, id); err != nil {
		t.Fatal(err)
	}
	return sp.Principal
}

// channel gives e a group chat.
func (e *env) channel(t *testing.T, chatID string) core.PrincipalID {
	t.Helper()
	now := e.clock.Now()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "slack", ChatID: chatID, Kind: core.ChatGroup}, Title: "#" + chatID,
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := e.store.Spaces().Create(context.Background(), sp, &core.Principal{DisplayName: sp.Title, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return sp.Principal
}

func scopeOf(err error) string {
	var be *core.BudgetError
	if errors.As(err, &be) && be.Kind == core.BudgetDailyCost {
		return be.Scope
	}
	return ""
}

func TestTeammateOwnCap(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"single", "multi"} {
		e := newEnv(t, func(c *config.Config) { c.Mode = mode })
		maya := e.teammate(t, "slack", "U1")
		e.spend(t, maya, 1.99)
		if err := e.svc.CheckDaily(ctx, maya); err != nil {
			t.Fatalf("%s mode, under the member default: %v", mode, err)
		}
		e.spend(t, maya, 0.01)
		err := e.svc.CheckDaily(ctx, maya)
		var be *core.BudgetError
		if !errors.As(err, &be) || be.Scope != core.BudgetPersonal || be.Limit != 2_000_000 {
			t.Fatalf("%s mode, at the member default: %v (%+v)", mode, err, be)
		}
	}

	e := newEnv(t, func(c *config.Config) {
		c.Mode = "single"
		c.Budgets.PerUserDailyUSD = map[string]float64{"Telegram:5001": 4}
	})
	olivia := e.teammate(t, "telegram", "5001")
	e.spend(t, olivia, 2.5)
	if err := e.svc.CheckDaily(ctx, olivia); err != nil {
		t.Fatalf("an app-keyed cap of $4 refused $2.50: %v", err)
	}
	if spent, limit, _ := e.svc.Daily(ctx, olivia); spent != 2_500_000 || limit != 3_000_000 {
		t.Fatalf("Daily = %d of %d, want the chat share ($3 of $5) as the cap closest to running out", spent, limit)
	}
}

func TestChatShare(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, func(c *config.Config) { c.Budgets.MemberDailyUSD = 10 })
	maya, sam := e.teammate(t, "slack", "U1"), e.teammate(t, "slack", "U2")
	eng := e.channel(t, "C1")
	e.spend(t, maya, 1.5)
	e.spend(t, eng, 1.5) // $3 of the $5 daily limit: the 60% share
	for _, p := range []core.PrincipalID{maya, sam, eng} {
		if s := scopeOf(e.svc.CheckDaily(ctx, p)); s != core.BudgetChat {
			t.Fatalf("%s at the chat share: scope %q, want chat", p, s)
		}
	}
	if err := e.svc.CheckDaily(ctx, e.owner); err != nil {
		t.Fatalf("an account still runs under the chat share: %v", err)
	}
	e.spend(t, e.owner, 2)
	if s := scopeOf(e.svc.CheckDaily(ctx, e.owner)); s != core.BudgetGlobal {
		t.Fatalf("account at the global cap: scope %q", s)
	}

	whole := newEnv(t, func(c *config.Config) { c.Budgets.MemberDailyUSD = 10; c.Budgets.ChatSharePct = 100 })
	ch := whole.channel(t, "C1")
	whole.spend(t, ch, 4)
	if err := whole.svc.CheckDaily(ctx, ch); err != nil {
		t.Fatalf("chat_share_pct 100 has no separate share: %v", err)
	}
	whole.spend(t, ch, 1)
	if s := scopeOf(whole.svc.CheckDaily(ctx, ch)); s != core.BudgetGlobal {
		t.Fatalf("a channel at the global cap: scope %q", s)
	}
	if spent, limit, _ := whole.svc.Daily(ctx, ch); spent != 5_000_000 || limit != 5_000_000 {
		t.Fatalf("a channel's Daily = %d of %d, want the global cap", spent, limit)
	}
}

func TestTeamSpendBillsThePrincipal(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	maya := e.teammate(t, "slack", "U1")
	if err := e.svc.RecordSpend(ctx, maya, core.CallInfo{Purpose: "notes", Cost: 1234, PriceKnown: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Usage().Spend(ctx, maya, "2026-10-12"); got != 1234 {
		t.Fatalf("teammate's spend = %d", got)
	}
	rows, err := e.store.Usage().Report(ctx, core.UsageQuery{GroupBy: "user"})
	if err != nil || len(rows) != 1 || rows[0].Key != string(maya) {
		t.Fatalf("report = %+v, %v; want the call billed to no account and grouped under the teammate", rows, err)
	}
	if rows, _ := e.store.Usage().Report(ctx, core.UsageQuery{GroupBy: "user", User: core.UserID(e.owner)}); len(rows) != 0 {
		t.Fatalf("the owner pays for a teammate: %+v", rows)
	}
}

func TestTeamWarnings(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, func(c *config.Config) { c.Budgets.MemberDailyUSD = 1 })
	maya := e.teammate(t, "slack", "U1")
	sam := e.teammate(t, "slack", "U2")
	sub := e.bus.Subscribe(core.Filter{System: true}, 64)
	defer sub.Close()
	e.spend(t, maya, 0.85) // 85% of Maya's $1
	e.spend(t, sam, 0.9)
	e.spend(t, maya, 0.05)
	e.spend(t, sam, 0.5)
	e.spend(t, sam, 0.1) // the team reaches $2.40: 80% of the $3 share
	var personal, chat int
	for done := false; !done; {
		select {
		case ev := <-sub.C():
			if d, ok := ev.Data.(core.BannerData); ok && !d.Clear {
				switch d.Code {
				case "budget_80":
					personal++
					if ev.Principal != maya && ev.Principal != sam {
						t.Errorf("budget_80 to %q", ev.Principal)
					}
				case "chat_budget_80":
					chat++
					if ev.Principal != "" || d.Text != "Chat apps have used 80% of today's $3.00 limit." || d.Action != "/activity/spending" {
						t.Errorf("chat_budget_80 = %+v to %q", d, ev.Principal)
					}
				}
			}
		default:
			done = true
		}
	}
	if personal != 2 || chat != 1 {
		t.Fatalf("budget_80 banners %d (want one per teammate), chat_budget_80 %d (want 1)", personal, chat)
	}
	e.clock.Advance(24 * time.Hour)
	if err := e.svc.CheckDaily(ctx, maya); err != nil {
		t.Fatal(err)
	}
	if active := e.bus.Banners(core.Filter{Admin: true}); len(active) != 0 {
		t.Fatalf("admin banners survived the day change: %+v", active)
	}
}
