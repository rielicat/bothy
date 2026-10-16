package channel

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

const maya = "U0000501"

func TestMemberDMBecomesTeammateAndIsGreetedOnce(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.mc.SetMember(maya, core.Membership{Status: core.MemberYes, Team: "Acme"})
		h.inject(core.Inbound{Chat: h.dmChat(maya), SenderID: maya, SenderName: "Maya Chen", Text: "hi", Mentioned: true})
		id := h.ident(maya)
		if id.Status != core.IdentityTeammate || id.Via != core.ViaMembership || id.Member != core.MemberYes || id.Principal == "" || id.Greet != "" {
			t.Fatalf("identity = %+v", id)
		}
		if v := h.visible(maya); len(v) != 1 || v[0] != helloText("Maya Chen") {
			t.Fatalf("DM = %q, want the hello", v)
		}
		subs := h.runner.Submitted()
		if len(subs) != 1 || subs[0].Principal != id.Principal || subs[0].Provenance != core.ProvUser {
			t.Fatalf("submits = %+v", subs)
		}
		h.inject(core.Inbound{Chat: h.dmChat(maya), SenderID: maya, SenderName: "Maya Chen", Text: "again", Mentioned: true})
		if n := len(h.visible(maya)); n != 1 || len(h.runner.Submitted()) != 2 {
			t.Fatalf("second message: %d replies, %d submits", n, len(h.runner.Submitted()))
		}
		if !slices.ContainsFunc(h.audit.All(), func(e core.AuditEntry) bool { return e.Action == "identity.teammate" }) {
			t.Fatal("no identity.teammate audit row")
		}
	})
}

func TestMovedTeammateGetsMovedGreetingOnce(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		id := h.ident(memberChat)
		id.Greet = core.GreetMoved
		if err := h.store.Identities().Update(h.ctx, id); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"hello?", "still there?"} {
			h.inject(core.Inbound{Chat: h.dmChat(memberChat), SenderID: memberChat, SenderName: "Mark", Text: text, Mentioned: true})
		}
		v := h.visible(memberChat)
		if len(v) != 1 || v[0] != "Hi Mark, this chat is no longer tied to your Bothy account, so your earlier chats and notes stay in Bothy on the web. "+
			"Ask me anything; when something needs approval, I'll ask an admin and tell you here." {
			t.Fatalf("DM = %q, want the moved greeting once", v)
		}
		if n := len(h.runner.Submitted()); n != 2 {
			t.Fatalf("submits = %d, want both messages", n)
		}
	})
}

func TestOutsiderDMGetsOneLineADayAndNoRun(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		sub := h.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvInboxChanged, core.EvBanner}}, 16)
		defer sub.Close()
		const guest = "U0000601"
		h.mc.SetMember(guest, core.Membership{Status: core.MemberOutsider, Team: "Acme"})
		for range 3 {
			h.dm(guest, "let me in")
		}
		if v := h.visible(guest); len(v) != 1 || v[0] != "Bothy works with members of this workspace." {
			t.Fatalf("outsider's DM = %q", v)
		}
		if len(h.runner.Submitted()) != 0 || len(h.visible(ownerChat)) != 0 {
			t.Fatal("an outsider reached the runner or an approver")
		}
		select {
		case e := <-sub.C():
			t.Fatalf("an outsider raised %+v", e)
		default:
		}
		time.Sleep(25 * time.Hour)
		h.dm(guest, "hello again")
		if n := len(h.visible(guest)); n != 2 {
			t.Fatalf("after a day: %d lines", n)
		}
	})
}

func TestOutsiderLineSentInIgnoreMode(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("discord")).start()
		h.cfg.Set(func(c *config.Config) { c.Channels.UnknownSenders = "ignore" })
		h.dm("1100000000000000601", "hi")
		if v := h.visible("1100000000000000601"); len(v) != 1 || v[0] != "Bothy works only with members of its team." {
			t.Fatalf("outsider's DM = %q", v)
		}
	})
}

// Guests, Slack Connect strangers and Slackbot get nothing in a channel
// Bothy is on in; teammates do.
func TestGuestStrangerAndSlackbotIgnoredInChannel(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		sp := h.space("C1", true, false)
		h.mc.SetMember("U0000602", core.Membership{Status: core.MemberOutsider})
		for _, who := range []string{"U0000602", "U0000603", "USLACKBOT"} {
			h.group("C1", who, "do something", true)
		}
		if len(h.visible("C1")) != 0 || len(h.runner.Submitted()) != 0 {
			t.Fatalf("outsiders reached Bothy in a channel: %q", h.visible("C1"))
		}
		for _, who := range []string{"U0000602", "U0000603", "USLACKBOT"} {
			if len(h.visible(who)) != 0 {
				t.Fatalf("%s got a DM for a channel message", who)
			}
		}
		h.group("C1", memberChat, "summarize", true)
		if subs := h.runner.Submitted(); len(subs) != 1 || subs[0].Principal != sp.Principal {
			t.Fatalf("teammate in channel: %+v", subs)
		}
	})
}

// TestMembershipRecheckedAfterAnHour: a verdict over an hour old is asked
// again; someone who left loses access: their runs stop, they can't approve,
// and their chats stay.
func TestMembershipRecheckedAfterAnHour(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.dm(memberChat, "first")
		calls := len(h.mc.MemberCalls())
		h.dm(memberChat, "within the hour")
		if n := len(h.mc.MemberCalls()); n != calls {
			t.Fatalf("a fresh verdict was asked again (%d lookups)", n-calls)
		}
		yes := true
		if _, err := h.auth.SetTeammate(h.ctx, h.admin(), "slack", memberChat, authPatch(nil, &yes)); err != nil {
			t.Fatal(err)
		}
		h.mc.SetMember(memberChat, core.Membership{Status: core.MemberOutsider})
		time.Sleep(core.MemberTTL + time.Minute)
		h.dm(memberChat, "after an hour")
		id := h.ident(memberChat)
		if id.Member != core.MemberOutsider || id.CanApprove || id.Status != core.IdentityTeammate {
			t.Fatalf("identity after leaving = %+v", id)
		}
		if got := h.runner.StoppedPrincipals(); !slices.Contains(got, id.Principal) {
			t.Fatalf("stopped principals = %v, want the departed teammate's", got)
		}
		if n := len(h.runner.Submitted()); n != 2 {
			t.Fatalf("submits = %d; the departed teammate's message reached the runner", n)
		}
		if got := h.lastText(memberChat); got != "Bothy works with members of this workspace." {
			t.Fatalf("DM = %q", got)
		}
		h.session(id.Principal, h.dmChat(memberChat))
	})
}

// TestLookupFailureUsesFourHourVerdictElseNotice: when Slack doesn't answer,
// a member verdict under four hours old still counts; an older one gets
// "can't check" in a DM at most once an hour, and silence in channels.
func TestLookupFailureUsesFourHourVerdictElseNotice(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.space("C1", true, false)
		h.mc.SetMemberErr(errors.New("slack is down"))
		time.Sleep(2 * time.Hour)
		h.dm(memberChat, "two hours on")
		if n := len(h.runner.Submitted()); n != 1 {
			t.Fatalf("a 2-hour verdict didn't count (%d submits)", n)
		}
		time.Sleep(3 * time.Hour)
		h.dm(memberChat, "five hours on")
		h.dm(memberChat, "and again")
		h.group("C1", memberChat, "in the channel", true)
		if v := h.visible(memberChat); len(v) != 1 || v[0] != cantCheck {
			t.Fatalf("DM = %q, want one can't-check line", v)
		}
		if len(h.visible("C1")) != 0 || len(h.runner.Submitted()) != 1 {
			t.Fatal("a stale verdict reached the runner or the channel")
		}
	})
}

func TestLookupsAfterSenderLimitAndThrottled(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.budget.Deny = &core.BudgetError{Kind: core.BudgetRate}
		h.dm("U0000701", "hi")
		if n := len(h.mc.MemberCalls()); n != 0 {
			t.Fatalf("a rate-limited sender made %d lookups", n)
		}
		h.budget.Deny = nil

		for i := range lookupsPerMin + 5 {
			h.dm("U00008"+strconv.Itoa(10+i), "hi")
		}
		if n := len(h.mc.MemberCalls()); n != lookupsPerMin {
			t.Fatalf("%d lookups in a minute, want %d", n, lookupsPerMin)
		}

		time.Sleep(time.Minute)
		h.mc.SetMemberErr(errors.New("slack is down"))
		before := len(h.mc.MemberCalls())
		h.dm("U0000901", "hi")
		h.dm("U0000901", "hi again")
		if n := len(h.mc.MemberCalls()) - before; n != 1 {
			t.Fatalf("%d lookups after a failure within a minute, want 1", n)
		}
	})
}

// TestMembershipCheckRunsOffTheDispatcher: a lookup in flight doesn't hold
// back another chat's /stop.
func TestMembershipCheckRunsOffTheDispatcher(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		gate := make(chan struct{})
		h.mc.Gate = gate
		h.mc.SetMember(maya, core.Membership{Status: core.MemberYes})
		h.dm(maya, "hi")
		h.dm(ownerChat, "/stop")
		if got := h.lastText(ownerChat); got != "Nothing to stop." {
			t.Fatalf("/stop behind a lookup = %q", got)
		}
		close(gate)
		synctest.Wait()
		if subs := h.runner.Submitted(); len(subs) != 1 || subs[0].Sender != "slack:"+maya {
			t.Fatalf("submits after the lookup = %+v", subs)
		}
	})
}

func TestScopeChangeResetsVerdicts(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.dm(memberChat, "first")
		calls := len(h.mc.MemberCalls())
		h.mc.SetScope("T2/")
		h.dm(memberChat, "another workspace's token")
		if n := len(h.mc.MemberCalls()) - calls; n != 1 {
			t.Fatalf("%d lookups after the workspace changed, want 1", n)
		}
		if v, _ := h.store.Meta().Get(h.ctx, "member_scope:slack"); v != "T2/|" {
			t.Fatalf("stored scope = %q", v)
		}
	})
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("discord"), func(h *harness) { h.servers = []core.TeamInfo{{ID: "G1", Name: "Acme"}} }).start()
		sp := h.space("C1", true, false)
		sp.Team = "G1"
		if err := h.store.Spaces().Update(h.ctx, sp); err != nil {
			t.Fatal(err)
		}
		h.hub.InvalidateTeams()
		h.dm(memberChat, "first")
		calls := len(h.mc.MemberCalls())
		sp.Enabled = false
		if err := h.store.Spaces().Update(h.ctx, sp); err != nil {
			t.Fatal(err)
		}
		h.hub.InvalidateTeams()
		h.dm(memberChat, "after the last channel in the server turned off")
		mcs := h.mc.MemberCalls()
		if n := len(mcs) - calls; n != 1 || len(mcs[len(mcs)-1].Teams) != 0 {
			t.Fatalf("lookups after the scope changed = %+v", mcs[calls:])
		}
	})
}

func TestSweepRefreshesApproversAndScheduled(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("slack")).start()
		h.letIn("U0000703", "Sam", false)
		now := time.Now()
		if err := h.store.Schedules().Upsert(h.ctx, &core.Schedule{Principal: h.pid(memberChat), Name: "digest", Kind: core.SchedCron,
			Spec: "0 9 * * *", TZ: "UTC", Prompt: "x", MissedRun: "skip", Enabled: true, CreatedBy: "run:x", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(sweepAge + sweepEvery)
		asked := map[string]int{}
		for _, c := range h.mc.MemberCalls() {
			asked[c.ExternalID]++
		}
		if asked[ownerChat] == 0 || asked[memberChat] == 0 || asked["U0000703"] != 0 {
			t.Fatalf("sweep asked about %v; want the approver and the scheduled teammate only", asked)
		}
	})
}

func TestDiscordDMChecksEnabledGuilds(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("discord")).start()
		for chat, s := range map[string]struct {
			team string
			on   bool
		}{"C1": {"G1", true}, "C2": {"G2", false}, "C3": {"G1", true}, "C4": {"G3", true}} {
			sp := h.space(chat, s.on, false)
			sp.Team = s.team
			if err := h.store.Spaces().Update(h.ctx, sp); err != nil {
				t.Fatal(err)
			}
		}
		h.hub.InvalidateTeams()
		h.dm("1100000000000000701", "hi")
		if calls := h.mc.MemberCalls(); len(calls) != 1 || len(calls[0].Teams) != 0 {
			t.Fatalf("a checker that lists no servers was asked with servers: %+v", calls)
		}
	})
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("discord"), func(h *harness) { h.servers = []core.TeamInfo{{ID: "G1"}, {ID: "G2"}, {ID: "G3"}} }).start()
		for chat, s := range map[string]struct {
			team string
			on   bool
		}{"C1": {"G1", true}, "C2": {"G2", false}, "C3": {"G1", true}, "C4": {"G3", true}} {
			sp := h.space(chat, s.on, false)
			sp.Team = s.team
			if err := h.store.Spaces().Update(h.ctx, sp); err != nil {
				t.Fatal(err)
			}
		}
		h.hub.InvalidateTeams()
		h.dm("1100000000000000701", "hi")
		calls := h.mc.MemberCalls()
		if len(calls) != 1 || !slices.Equal(calls[0].Teams, []string{"G1", "G3"}) {
			t.Fatalf("lookups = %+v, want the servers with a channel on", calls)
		}
		if st := h.hub.Status()[0]; st.Detail != "Bothy is in 1 server where it isn't on in any channel." || st.IdleServers != 1 {
			t.Fatalf("status = %+v, want 1 idle server", st)
		}
	})
}

// A teammate an admin let in where Discord can't tell (no server with a
// channel on, or no access) can still DM.
func TestDiscordNoGuildsOrNoAccessFallsBackToLetIn(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("discord")).start()
		const dee = "1100000000000000702"
		now := time.Now()
		if _, err := h.store.Identities().Touch(h.ctx, &core.ChannelIdentity{Channel: "discord", ExternalID: dee, DisplayName: "Dee",
			Member: core.MemberUnknown, FirstSeen: now, LastSeen: now}); err != nil {
			t.Fatal(err)
		}
		id := h.ident(dee)
		id.Member, id.MemberAt = core.MemberUnknown, now
		if err := h.store.Identities().Update(h.ctx, id); err != nil {
			t.Fatal(err)
		}
		teammate := core.IdentityTeammate
		if _, err := h.auth.SetTeammate(h.ctx, h.admin(), "discord", dee, authPatch(&teammate, nil)); err != nil {
			t.Fatal(err)
		}
		h.mc.SetMember(dee, core.Membership{Status: core.MemberUnknown})
		time.Sleep(core.MemberTTL + time.Minute)
		h.dm(dee, "hi")
		if subs := h.runner.Submitted(); len(subs) != 1 || subs[0].Principal != h.pid(dee) {
			t.Fatalf("submits = %+v", subs)
		}
	})
}

func TestDiscordTeamBackfilledOnConnect(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t, withChecker("discord"), func(h *harness) { h.servers = []core.TeamInfo{{ID: "G1", Name: "Acme"}} })
		sp := h.space("C1", true, false)
		h.ch.SetChat("C1", core.ChatInfo{TeamID: "G1", TeamName: "Acme", Title: "#general", Members: 42})
		h.start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		got, err := h.store.Spaces().ByChat(h.ctx, "discord", "C1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Team != "G1" || got.TeamName != "Acme" || got.Members != 42 || got.Title != sp.Title || !got.ReviewedAt.Equal(sp.ReviewedAt) {
			t.Fatalf("space after connect = %+v", got)
		}
	})
}

func TestGreetAndTellApprover(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		now := time.Now()
		if _, err := h.store.Identities().Touch(h.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "3003", DisplayName: "Sam",
			Member: core.MemberUnknown, FirstSeen: now, LastSeen: now}); err != nil {
			t.Fatal(err)
		}
		teammate := core.IdentityTeammate
		if _, err := h.auth.SetTeammate(h.ctx, h.admin(), "telegram", "3003", authPatch(&teammate, nil)); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := h.hub.Greet(h.ctx, "telegram", "3003"); err != nil {
				t.Fatal(err)
			}
		}
		if v := h.visible("3003"); len(v) != 1 || v[0] != helloText("Sam") {
			t.Fatalf("greetings = %q", v)
		}
		if id := h.ident("3003"); id.Greet != "" || id.DMChat != "3003" {
			t.Fatalf("identity after the greeting = %+v", id)
		}
		if err := h.hub.TellApprover(h.ctx, "telegram", "3003"); err != nil {
			t.Fatal(err)
		}
		if got := h.lastText("3003"); got != approverText {
			t.Fatalf("approver note = %q", got)
		}
		if err := h.hub.Greet(h.ctx, "slack", "3003"); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("Greet on an app that is off = %v", err)
		}
	})
}

// limitBudget reports a daily limit for every principal: the cap closest
// to running out, which needn't be the one that warned.
type limitBudget struct {
	*fakes.Budget
	limit core.Micros
}

func (b *limitBudget) Daily(context.Context, core.PrincipalID) (core.Micros, core.Micros, error) {
	return b.limit * 98 / 100, b.limit, nil
}

// TestBudgetWarningDMOncePerDay: a teammate whose own limit passed the
// warning ratio hears it once a day, in that limit's figures.
func TestBudgetWarningDMOncePerDay(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t)
		h.hub.d.Budget = &limitBudget{Budget: h.budget, limit: core.MicrosFromUSD(5)}
		h.start()
		warn := func(p core.PrincipalID) {
			h.bus.Publish(core.Event{Type: core.EvBudgetWarning, Principal: p,
				Data: core.BudgetWarningData{Spent: core.MicrosFromUSD(1.7), Limit: core.MicrosFromUSD(2)}})
			synctest.Wait()
		}
		warn(h.pid(memberChat))
		warn(h.pid(memberChat))
		if v := h.visible(memberChat); len(v) != 1 || v[0] != "You've used 85% of your $2.00 daily limit in Bothy." {
			t.Fatalf("DMs = %q", v)
		}
		h.bus.Publish(core.Event{Type: core.EvBudgetWarning, Data: core.BudgetWarningData{Spent: 4, Limit: 5}})
		synctest.Wait()
		warn(core.UserPrincipal(h.member.ID))
		sp := h.space("-500", true, false)
		warn(sp.Principal)
		if len(h.visible("-500")) != 0 {
			t.Fatal("a channel got a personal limit note")
		}
		time.Sleep(25 * time.Hour)
		warn(h.pid(memberChat))
		if n := len(h.visible(memberChat)); n != 2 {
			t.Fatalf("the next day: %d notes", n)
		}
	})
}
