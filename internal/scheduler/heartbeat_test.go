package scheduler

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func heartbeatOn(c *config.Config) {
	c.Scheduler.Heartbeat.Enabled = true
	c.Scheduler.Heartbeat.Every = config.Duration(30 * time.Minute)
	c.Scheduler.Heartbeat.ActiveHours = "08:00-22:00"
}

func (h *harness) heartbeat() *core.Schedule {
	h.t.Helper()
	all, err := h.store.Schedules().List(context.Background(), core.SystemViewer())
	if err != nil {
		h.t.Fatal(err)
	}
	var out *core.Schedule
	for _, sc := range all {
		if sc.Kind == core.SchedHeartbeat && sc.CreatedBy == systemActor {
			if out != nil {
				h.t.Fatal("more than one system heartbeat")
			}
			out = sc
		}
	}
	return out
}

func delta(text string) core.Event {
	return core.Event{Type: core.EvRunDelta, Data: core.DeltaData{Text: text}}
}

var finished = core.Event{Type: core.EvRunFinished, Data: core.RunFinishedData{Status: core.RunDone}}

func TestHeartbeatRunAndSuppression(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, heartbeatOn)
		h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{h.p: {core.FileHeartbeat: "- Check the inbox\n"}}
		sleepUntil(utc(2026, 10, 12, 9, 0)) // 10:00 BST
		stop := h.start()
		defer stop()
		synctest.Wait()

		hb := h.heartbeat()
		if hb == nil || !hb.Enabled || hb.Principal != h.p || hb.Name != "heartbeat" || hb.Slot != core.SlotFast ||
			hb.Spec != "30m0s" || hb.TZ != "Europe/London" || hb.ActiveHours != "08:00-22:00" || hb.Deliver != nil ||
			!hb.NextRunAt.Equal(utc(2026, 10, 12, 9, 30)) {
			t.Fatalf("synced heartbeat = %+v", hb)
		}

		sleepUntil(utc(2026, 10, 12, 9, 30))
		synctest.Wait()
		fs := h.runner.all()
		if len(fs) != 1 {
			t.Fatalf("heartbeat runs = %d", len(fs))
		}
		req := fs[0].req
		// The instruction is turn context, so the chat shows the person's
		// own list; the model gets both.
		wantInput := []core.Part{{Type: core.PartContext, Text: heartbeatInstruction}, {Type: core.PartText, Text: "- Check the inbox\n"}}
		if req.Trigger != core.TrigHeartbeat || req.Slot != core.SlotFast || req.Provenance != core.ProvUser ||
			!slices.EqualFunc(req.Input, wantInput, func(a, b core.Part) bool { return a.Type == b.Type && a.Text == b.Text }) ||
			req.Schedule != hb.ID || req.User != h.owner {
			t.Fatalf("heartbeat request = %+v", req)
		}
		ses, err := h.store.Sessions().Get(context.Background(), core.SystemViewer(), req.Session)
		if err != nil || !ses.Hidden || ses.Title != "Check-in · 12 Oct" || ses.ScheduleID != hb.ID || ses.Origin != "schedule" {
			t.Fatalf("check-in chat = %+v, %v; want hidden, titled Check-in, under its schedule", ses, err)
		}
		// An account hears on the web only: no chat app gets the check-in.
		if req.Sink != nil || len(h.hub.StreamModes) != 0 {
			t.Fatalf("a check-in reached a chat app: %v", h.hub.StreamModes)
		}
		h.finish(fs[0].run, core.RunDone)
		sleepUntil(utc(2026, 10, 12, 10, 0))
		synctest.Wait()
		if fs = h.runner.all(); len(fs) != 2 {
			t.Fatalf("heartbeat runs = %d", len(fs))
		}

		// Outside the active hours nothing fires; the next run is at opening.
		sleepUntil(utc(2026, 10, 12, 20, 59))
		synctest.Wait()
		if next := h.heartbeat().NextRunAt; !next.Equal(utc(2026, 10, 13, 7, 0)) {
			t.Fatalf("next heartbeat after 22:00 BST = %v, want 08:00 BST", next)
		}
	})
}

// A check-in's chat is listed once there is something to see: the run
// parked on an approval, ended other than done, or answered anything but
// HEARTBEAT_OK. A done all-clear stays hidden and announces nothing.
func TestCheckInListedOnlyWithSomethingToSay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, heartbeatOn)
		ctx := context.Background()
		h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{h.p: {core.FileHeartbeat: "- Tell me if a deploy failed\n"}}
		sleepUntil(utc(2026, 10, 12, 9, 0))
		stop := h.start()
		defer stop()
		synctest.Wait()
		sub := h.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvSessionUpdated}}, 16)
		defer sub.Close()
		parked := func(run core.RunID) {
			r, err := h.store.Runs().Get(ctx, core.SystemViewer(), run)
			if err != nil {
				t.Fatal(err)
			}
			r.Status = core.RunParked
			if err := h.store.Runs().Update(ctx, r); err != nil {
				t.Fatal(err)
			}
			h.bus.Publish(core.Event{Type: core.EvRunParked, Principal: r.Principal, Session: r.Session, Run: run,
				Data: core.RunParkedData{Approval: "apr_x"}})
		}
		for i, c := range []struct {
			replies []string
			end     func(core.RunID)
			listed  bool
		}{
			{[]string{"HEARTBEAT_OK"}, func(r core.RunID) { h.finish(r, core.RunDone) }, false},
			{[]string{"Checking the deploys first.", "`HEARTBEAT_OK`"}, func(r core.RunID) { h.finish(r, core.RunDone) }, false},
			{[]string{"HEARTBEAT_OK", "The deploy to staging failed an hour ago."}, func(r core.RunID) { h.finish(r, core.RunDone) }, true},
			{[]string{"HEARTBEAT_OK"}, func(r core.RunID) { h.finish(r, core.RunFailed) }, true},
			{nil, parked, true},
		} {
			time.Sleep(30 * time.Minute)
			synctest.Wait()
			fs := h.runner.all()
			if len(fs) != i+1 {
				t.Fatalf("check-ins = %d, want %d", len(fs), i+1)
			}
			f := fs[i]
			for _, text := range c.replies {
				m := &core.Message{Session: f.req.Session, Run: f.run, Role: core.MsgAssistant, Kind: core.KindNormal,
					Provenance: core.ProvSystem, Parts: []core.Part{{Type: core.PartText, Text: text}}}
				if err := h.store.Messages().Append(ctx, m); err != nil {
					t.Fatal(err)
				}
			}
			c.end(f.run)
			synctest.Wait()
			ses, err := h.store.Sessions().Get(ctx, core.SystemViewer(), f.req.Session)
			if err != nil {
				t.Fatal(err)
			}
			var updated int
			for len(sub.C()) > 0 {
				if e := <-sub.C(); e.Session == ses.ID {
					updated++
				}
			}
			if ses.Hidden == c.listed || (updated == 1) != c.listed {
				t.Errorf("check-in %d (%q): hidden %v, %d session.updated; want listed %v", i, c.replies, ses.Hidden, updated, c.listed)
			}
		}
	})
}

// The notice that a check-in was turned off lands in the chat its last tick
// made, which is hidden while nothing was said there: the notice lists it.
func TestCheckInTurnedOffNoticeIsListed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, heartbeatOn)
		ctx := context.Background()
		h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{h.p: {core.FileHeartbeat: "- x\n"}}
		h.runner.fail(errors.New("no model is set up"))
		sleepUntil(utc(2026, 10, 12, 9, 0))
		stop := h.start()
		defer stop()
		for range maxFailures {
			time.Sleep(30 * time.Minute)
			synctest.Wait()
		}
		if hb := h.heartbeat(); hb.Enabled {
			t.Fatalf("check-in still on: %+v", hb)
		}
		chats, err := h.store.Sessions().List(ctx, h.viewer(), core.SessionFilter{NonEmpty: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(chats) != 1 || chats[0].Hidden || chats[0].Title != "Check-in · 12 Oct, 11:30" {
			t.Fatalf("listed chats = %+v, want the check-in's chat with the notice", chats)
		}
		all, err := h.store.Sessions().List(ctx, h.viewer(), core.SessionFilter{IncludeHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != maxFailures {
			t.Fatalf("chats = %d, want one per tick", len(all))
		}
	})
}

func TestQuietSink(t *testing.T) {
	run := func(evs ...core.Event) *fakes.Sink {
		out := &fakes.Sink{}
		q := &quietSink{next: out}
		for _, e := range evs {
			q.OnEvent(e)
		}
		return out
	}
	toolCall := core.Event{Type: core.EvToolCall}
	toolResult := core.Event{Type: core.EvToolResult}
	if out := run(delta("Let me check the calendar."), toolCall, toolResult, delta(" HEARTBEAT_OK"), finished); len(out.All()) != 0 {
		t.Error("text before a tool call is not the final answer")
	}
	if out := run(delta("**HEARTBEAT_OK**"), finished); len(out.All()) != 0 {
		t.Error("emphasis around the token should still suppress")
	}
	if out := run(delta("HEARTBEAT_OK"), toolCall, toolResult, delta("Actually, your flight is delayed."), finished); len(out.All()) != 5 {
		t.Errorf("a real final answer must be delivered: %v", out.Types())
	}
	if out := run(delta("All quiet. HEARTBEAT_OK"), finished); len(out.All()) != 2 {
		t.Error("only a reply that starts with HEARTBEAT_OK is suppressed")
	}
	out := run(delta("I need to send an email"), toolCall, core.Event{Type: core.EvRunParked})
	if got := out.Types(); len(got) != 3 || got[2] != core.EvRunParked {
		t.Errorf("a parked run is flushed at once: %v", got)
	}
	evs := make([]core.Event, maxQuietEvents+10)
	for i := range evs {
		evs[i] = delta("x")
	}
	if out := run(evs...); len(out.All()) != len(evs) {
		t.Errorf("a very long run is passed through: %d of %d", len(out.All()), len(evs))
	}
}

func TestHeartbeatSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, heartbeatOn)
		ctx := context.Background()
		h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{h.p: {core.FileHeartbeat: " \n\t\n"}}
		sleepUntil(utc(2026, 10, 12, 9, 0))
		stop := h.start()
		defer stop()
		sleepUntil(utc(2026, 10, 12, 10, 5))
		synctest.Wait()
		hb := h.heartbeat()
		if n := len(h.runner.all()); n != 0 {
			t.Fatalf("an empty HEARTBEAT.md made %d runs", n)
		}
		if hb.LastStatus != "" || !hb.NextRunAt.Equal(utc(2026, 10, 12, 10, 30)) {
			t.Fatalf("skipped ticks must record nothing and move on: %+v", hb)
		}
		if _, err := h.svc.RunNow(ctx, h.viewer(), hb.ID, h.owner); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("RunNow with an empty HEARTBEAT.md: %v", err)
		}

		// Daily cap: today's spend of this schedule's runs reaches $0.25.
		if _, err := h.memory.Write(ctx, core.SystemViewer(), h.p, core.FileHeartbeat, "- Check the inbox\n", "", h.owner); err != nil {
			t.Fatal(err)
		}
		sleepUntil(utc(2026, 10, 12, 10, 35))
		synctest.Wait()
		fs := h.runner.all()
		if len(fs) != 1 {
			t.Fatalf("runs = %d", len(fs))
		}
		if err := h.store.Runs().AddLLMCall(ctx, fs[0].run, h.p, h.owner, core.CallInfo{Purpose: "heartbeat", Cost: core.MicrosFromUSD(0.26), PriceKnown: true}); err != nil {
			t.Fatal(err)
		}
		sleepUntil(utc(2026, 10, 12, 20, 0))
		synctest.Wait()
		if n := len(h.runner.all()); n != 1 {
			t.Fatalf("the heartbeat ran %d times past its daily cap", n)
		}
		var be *core.BudgetError
		if _, err := h.svc.RunNow(ctx, h.viewer(), hb.ID, h.owner); !errors.As(err, &be) || be.Kind != core.BudgetDailyCost {
			t.Fatalf("RunNow over the heartbeat cap: %v", err)
		}
		// The cap resets at local midnight.
		sleepUntil(utc(2026, 10, 13, 7, 5))
		synctest.Wait()
		if n := len(h.runner.all()); n != 2 {
			t.Fatalf("no heartbeat the next morning (%d runs)", n)
		}
	})
}

func TestHeartbeatSyncHotReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 9, 0))
		stop := h.start()
		defer stop()
		synctest.Wait()
		if hb := h.heartbeat(); hb != nil {
			t.Fatalf("heartbeat created while off: %+v", hb)
		}

		h.cfg.Set(heartbeatOn)
		synctest.Wait()
		hb := h.heartbeat()
		if hb == nil || !hb.Enabled {
			t.Fatalf("enable: %+v", hb)
		}
		id := hb.ID

		h.cfg.Set(func(c *config.Config) {
			c.Scheduler.Heartbeat.Every = config.Duration(2 * time.Hour)
			c.Scheduler.Heartbeat.Deliver = "telegram"
			c.Scheduler.Heartbeat.Slot = "main"
		})
		synctest.Wait()
		hb = h.heartbeat()
		// The owner is an account: a chat app named there posts nowhere, so the schedule doesn't claim it.
		if hb.ID != id || hb.Spec != "2h0m0s" || hb.Slot != core.SlotMain || hb.Deliver != nil ||
			!hb.NextRunAt.Equal(utc(2026, 10, 12, 11, 0)) {
			t.Fatalf("update: %+v", hb)
		}

		// Unrelated config changes leave the schedule alone.
		before := *hb
		time.Sleep(time.Minute)
		h.cfg.Set(func(c *config.Config) { c.Budgets.DailyUSD = 9 })
		synctest.Wait()
		if after := h.heartbeat(); after.UpdatedAt != before.UpdatedAt || !after.NextRunAt.Equal(before.NextRunAt) {
			t.Fatalf("heartbeat rewritten by an unrelated change: %+v", after)
		}

		h.cfg.Set(func(c *config.Config) { c.Scheduler.Heartbeat.Enabled = false })
		synctest.Wait()
		if hb = h.heartbeat(); hb.ID != id || hb.Enabled || !hb.NextRunAt.IsZero() {
			t.Fatalf("disable: %+v", hb)
		}
		if !slices.Contains(h.audit.Actions(), "schedule.create") {
			t.Fatalf("heartbeat creation not audited: %v", h.audit.Actions())
		}
	})
}

// The check-in keeps the owner's hours where they are: it follows their own
// time zone, and a change to it, not only a config change, moves it.
func TestHeartbeatFollowsTheOwnersZone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, heartbeatOn)
		h.setZone("America/New_York")
		sleepUntil(utc(2026, 10, 12, 9, 0))
		stop := h.start()
		defer stop()
		synctest.Wait()
		if hb := h.heartbeat(); hb == nil || hb.TZ != "America/New_York" || !hb.NextRunAt.Equal(utc(2026, 10, 12, 12, 0)) {
			t.Fatalf("heartbeat = %+v; want the owner's zone, not the configured Europe/London", hb)
		}

		h.setZone("Asia/Tokyo")
		h.svc.ResyncHeartbeat()
		synctest.Wait()
		if hb := h.heartbeat(); hb.TZ != "Asia/Tokyo" || !hb.NextRunAt.Equal(utc(2026, 10, 12, 9, 30)) {
			t.Fatalf("after the owner moved = %+v", hb)
		}
	})
}

// scheduler.heartbeat.deliver naming a chat app still keeps the owner's
// check-in on the web: accounts have no chat.
func TestHeartbeatStaysOnTheWeb(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, heartbeatOn, func(c *config.Config) { c.Scheduler.Heartbeat.Deliver = "telegram" })
		h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{h.p: {core.FileHeartbeat: "- x\n"}}
		sleepUntil(utc(2026, 10, 12, 9, 0))
		stop := h.start()
		defer stop()
		sleepUntil(utc(2026, 10, 12, 9, 31))
		synctest.Wait()
		if fs := h.runner.all(); len(fs) != 1 || fs[0].req.Sink != nil {
			t.Fatalf("runs = %+v, want one delivered on the web only", fs)
		}
		if len(h.hub.StreamModes) != 0 {
			t.Fatalf("a check-in reached a chat app: %v", h.hub.StreamModes)
		}
	})
}
