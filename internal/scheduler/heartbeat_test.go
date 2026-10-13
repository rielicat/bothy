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

func evs(es ...core.Event) []core.Event { return es }

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
		wantPrompt := heartbeatInstruction + "\n\n- Check the inbox\n"
		if req.Trigger != core.TrigHeartbeat || req.Slot != core.SlotFast || req.Provenance != core.ProvUser ||
			len(req.Input) != 1 || req.Input[0].Text != wantPrompt || req.Schedule != hb.ID || req.User != h.owner {
			t.Fatalf("heartbeat request = %+v", req)
		}
		if mode := h.hub.StreamModes[h.dm.Key()]; mode != "final" {
			t.Fatalf("delivery mode = %q, want final", mode)
		}
		out, _ := h.hub.Stream(h.dm)

		// HEARTBEAT_OK: nothing reaches the chat.
		for _, e := range evs(core.Event{Type: core.EvRunStarted}, delta("HEARTBEAT"), delta("_OK"), finished) {
			req.Sink.OnEvent(e)
		}
		if n := len(out.All()); n != 0 {
			t.Fatalf("HEARTBEAT_OK was delivered (%d events)", n)
		}
		h.finish(fs[0].run, core.RunDone)

		// Something to say: the whole run is delivered.
		sleepUntil(utc(2026, 10, 12, 10, 0))
		synctest.Wait()
		fs = h.runner.all()
		if len(fs) != 2 {
			t.Fatalf("heartbeat runs = %d", len(fs))
		}
		for _, e := range evs(core.Event{Type: core.EvRunStarted}, delta("Your 3pm moved to 4pm."), finished) {
			fs[1].req.Sink.OnEvent(e)
		}
		if got := out.Types(); !slices.Equal(got, []core.EventType{core.EvRunStarted, core.EvRunDelta, core.EvRunFinished}) || out.Text() != "Your 3pm moved to 4pm." {
			t.Fatalf("delivered %v %q", got, out.Text())
		}

		// Outside the active hours nothing fires; the next run is at opening.
		sleepUntil(utc(2026, 10, 12, 20, 59))
		synctest.Wait()
		if next := h.heartbeat().NextRunAt; !next.Equal(utc(2026, 10, 13, 7, 0)) {
			t.Fatalf("next heartbeat after 22:00 BST = %v, want 08:00 BST", next)
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
		if hb.ID != id || hb.Spec != "2h0m0s" || hb.Slot != core.SlotMain || hb.Deliver == nil || *hb.Deliver != (core.ChatRef{Channel: "telegram"}) ||
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

func TestHeartbeatDeliveryToChannelDM(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, heartbeatOn, func(c *config.Config) { c.Scheduler.Heartbeat.Deliver = "telegram" })
		h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{h.p: {core.FileHeartbeat: "- x\n"}}
		h.hub.PreferredChats = nil // the DM comes from the linked telegram identity
		sleepUntil(utc(2026, 10, 12, 9, 0))
		stop := h.start()
		defer stop()
		sleepUntil(utc(2026, 10, 12, 9, 31))
		synctest.Wait()
		if fs := h.runner.all(); len(fs) != 1 || fs[0].req.Sink == nil {
			t.Fatalf("runs = %+v", fs)
		}
		if mode := h.hub.StreamModes[h.dm.Key()]; mode != "final" {
			t.Fatalf("not delivered to the owner's telegram DM: %v", h.hub.StreamModes)
		}
	})
}
