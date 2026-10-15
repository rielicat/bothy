package scheduler

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// The owner notice says how the last task ended in words, never as a status.
func TestLastFailure(t *testing.T) {
	for st, want := range map[core.RunStatus]string{
		core.RunFailed:         "",
		core.RunBudgetExceeded: " and the last one hit the spending limit",
		core.RunLoopDetected:   " and in the last one Bothy kept repeating the same step",
	} {
		if got := lastFailure(st); got != want {
			t.Errorf("lastFailure(%s) = %q, want %q", st, got, want)
		}
	}
}

func fireTimes(fs []fired) []time.Time {
	out := make([]time.Time, len(fs))
	for i, f := range fs {
		out[i] = f.at.UTC()
	}
	return out
}

func TestLoopCronAcrossDST(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		london(t)
		sleepUntil(utc(2026, 3, 27, 12, 0))
		daily := h.upsert(&core.Schedule{Name: "early", Kind: core.SchedCron, Spec: "30 1 * * *", TZ: "Europe/London", Prompt: "x", Enabled: true})
		stop := h.start()
		defer stop()

		sleepUntil(utc(2026, 3, 30, 12, 0))
		synctest.Wait()
		wantTimes(t, "spring forward", fireTimes(h.runner.all()),
			utc(2026, 3, 28, 1, 30), utc(2026, 3, 29, 1, 0), utc(2026, 3, 30, 0, 30))

		sleepUntil(utc(2026, 10, 23, 12, 0))
		synctest.Wait()
		before := len(h.runner.all())
		hourly := h.upsert(&core.Schedule{Name: "hourly", Kind: core.SchedCron, Spec: "0 * * * *", TZ: "Europe/London", Prompt: "x", Enabled: true})
		sleepUntil(utc(2026, 10, 26, 12, 0))
		synctest.Wait()
		var dailyAt []time.Time
		perDay := map[int]int{}
		for _, f := range h.runner.all()[before:] {
			switch f.req.Schedule {
			case daily.ID:
				dailyAt = append(dailyAt, f.at.UTC())
			case hourly.ID:
				perDay[f.at.In(london(t)).Day()]++
			}
		}
		wantTimes(t, "fall back", dailyAt, utc(2026, 10, 24, 0, 30), utc(2026, 10, 25, 0, 30), utc(2026, 10, 26, 1, 30))
		if perDay[24] != 24 || perDay[25] != 25 {
			t.Fatalf("hourly runs per local day = %v, want 24 on the 24th and 25 on the 25th", perDay)
		}
		if next := h.get(daily.ID).NextRunAt; !next.Equal(utc(2026, 10, 27, 1, 30)) {
			t.Fatalf("next run = %v", next)
		}
	})
}

func TestLoopPausedFiresNothingAndResumes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		sleepUntil(utc(2026, 10, 12, 10, 0))
		sc := h.upsert(&core.Schedule{Name: "tick", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		stop := h.start()
		defer stop()

		h.svc.SetPaused(true)
		time.Sleep(time.Hour + time.Second)
		synctest.Wait()
		if n := len(h.runner.all()); n != 0 {
			t.Fatalf("paused scheduler fired %d runs", n)
		}
		if got := h.get(sc.ID); !got.NextRunAt.Equal(utc(2026, 10, 12, 11, 10)) || !got.LastRunAt.IsZero() || got.LastStatus != "" {
			t.Fatalf("paused ticks must be skipped without a record: %+v", got)
		}
		if _, err := h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner); err == nil {
			t.Fatal("RunNow while paused should fail")
		}

		h.svc.SetPaused(false)
		sleepUntil(utc(2026, 10, 12, 11, 25))
		synctest.Wait()
		wantTimes(t, "after resume", fireTimes(h.runner.all()), utc(2026, 10, 12, 11, 10), utc(2026, 10, 12, 11, 20))

		// The kill switch (Runner.Paused) and scheduler.enabled stop it too.
		_ = h.runner.SetPaused(ctx, true, h.owner)
		sleepUntil(utc(2026, 10, 12, 12, 5))
		synctest.Wait()
		_ = h.runner.SetPaused(ctx, false, h.owner)
		h.cfg.Set(func(c *config.Config) { c.Scheduler.Enabled = false })
		sleepUntil(utc(2026, 10, 12, 12, 35))
		synctest.Wait()
		if n := len(h.runner.all()); n != 2 {
			t.Fatalf("fired %d runs while the kill switch or scheduler.enabled=false was on", n)
		}
		h.cfg.Set(func(c *config.Config) { c.Scheduler.Enabled = true })
		sleepUntil(utc(2026, 10, 12, 12, 45))
		synctest.Wait()
		if got := fireTimes(h.runner.all()); len(got) != 3 || !got[2].Equal(utc(2026, 10, 12, 12, 40)) {
			t.Fatalf("after re-enabling: %v", got)
		}
	})
}

func TestLoopMissedRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Scheduler.MissedRun = "once" })
		sleepUntil(utc(2026, 10, 12, 9, 0))
		once := h.upsert(&core.Schedule{Name: "once", Kind: core.SchedOnce, Spec: "2026-10-12T11:00:00Z", TZ: "UTC", Prompt: "x", Enabled: true})
		cron := h.upsert(&core.Schedule{Name: "cron", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		skipped := h.upsert(&core.Schedule{Name: "skip", Kind: core.SchedOnce, Spec: "2026-10-12T11:00:00Z", TZ: "UTC", Prompt: "x", MissedRun: "skip", Enabled: true})
		stale := h.upsert(&core.Schedule{Name: "stale", Kind: core.SchedOnce, Spec: "2026-10-12T09:30:00Z", TZ: "UTC", Prompt: "x", Enabled: true})
		if once.MissedRun != "once" || skipped.MissedRun != "skip" {
			t.Fatalf("missed_run defaults: %q %q", once.MissedRun, skipped.MissedRun)
		}

		// The gateway is down from 09:00 to 11:40.
		sleepUntil(utc(2026, 10, 12, 11, 40))
		stop := h.start()
		defer stop()
		synctest.Wait()
		count := func() map[core.ScheduleID]int {
			m := map[core.ScheduleID]int{}
			for _, f := range h.runner.all() {
				m[f.req.Schedule]++
			}
			return m
		}
		if c := count(); c[once.ID] != 1 || c[cron.ID] != 1 || c[skipped.ID] != 0 || c[stale.ID] != 0 {
			t.Fatalf("catch-up runs at boot = %v", c)
		}
		for _, id := range []core.ScheduleID{once.ID, skipped.ID, stale.ID} {
			if sc := h.get(id); sc.Enabled || !sc.NextRunAt.IsZero() {
				t.Fatalf("one-off %s still scheduled: %+v", sc.Name, sc)
			}
		}
		if got := h.get(once.ID); got.LastStatus != core.RunQueued || !got.LastRunAt.Equal(utc(2026, 10, 12, 11, 40)) {
			t.Fatalf("catch-up not recorded: %+v", got)
		}
		if got := h.get(skipped.ID); got.LastStatus != "" || !got.LastRunAt.IsZero() {
			t.Fatalf("skip policy must record nothing: %+v", got)
		}

		sleepUntil(utc(2026, 10, 13, 12, 5))
		synctest.Wait()
		c := count()
		if c[once.ID] != 1 {
			t.Fatalf("the missed one-off fired %d times, want exactly once", c[once.ID])
		}
		if c[cron.ID] != 1+1+6*24+1 { // 11:40 catch-up, 11:50, then 12:00 to 12:00 next day
			t.Fatalf("cron fired %d times, want one catch-up plus every tick after", c[cron.ID])
		}
	})
}

func TestLoopMissedSkipDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 9, 0))
		cron := h.upsert(&core.Schedule{Name: "cron", Kind: core.SchedCron, Spec: "0 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		sleepUntil(utc(2026, 10, 12, 10, 30))
		stop := h.start()
		defer stop()
		synctest.Wait()
		if n := len(h.runner.all()); n != 0 {
			t.Fatalf("missed_run skip fired %d catch-up runs", n)
		}
		if next := h.get(cron.ID).NextRunAt; !next.Equal(utc(2026, 10, 12, 11, 0)) {
			t.Fatalf("next = %v", next)
		}
		// A tick that fires within the grace period is not "missed".
		sleepUntil(utc(2026, 10, 12, 11, 30))
		synctest.Wait()
		if n := len(h.runner.all()); n != 1 {
			t.Fatalf("on-time tick: %d runs", n)
		}
	})
}

func TestLoopThreeFailuresDisableAndNotify(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 10, 0))
		sc := h.upsert(&core.Schedule{Name: "report [*link*](x)", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		sub := h.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{h.p}, Types: []core.EventType{core.EvScheduleRun}}, 64)
		defer sub.Close()
		stop := h.start()
		defer stop()

		tick := func(status core.RunStatus) {
			t.Helper()
			time.Sleep(10 * time.Minute)
			synctest.Wait()
			fs := h.runner.all()
			h.finish(fs[len(fs)-1].run, status)
			synctest.Wait()
		}
		tick(core.RunFailed)
		tick(core.RunLoopDetected)
		if got := h.get(sc.ID); got.Failures != 2 || !got.Enabled {
			t.Fatalf("after two failures: %+v", got)
		}
		tick(core.RunDone)
		if got := h.get(sc.ID); got.Failures != 0 || got.LastStatus != core.RunDone {
			t.Fatalf("a success must reset the count: %+v", got)
		}
		tick(core.RunStopped) // neither a failure nor a success
		tick(core.RunFailed)
		tick(core.RunBudgetExceeded)
		if got := h.get(sc.ID); got.Failures != 2 || !got.Enabled {
			t.Fatalf("stopped must not count: %+v", got)
		}
		if len(h.hub.Messages()) != 0 {
			t.Fatal("notified too early")
		}
		tick(core.RunFailed)
		got := h.get(sc.ID)
		if got.Enabled || got.Failures != 3 || !got.NextRunAt.IsZero() || got.LastStatus != core.RunFailed {
			t.Fatalf("third failure must disable: %+v", got)
		}
		msgs := h.hub.Messages()
		if len(msgs) != 1 || msgs[0] != h.dm.Key()+"|The schedule “report linkx” was turned off because its last 3 tasks failed. Turn it back on under Schedules once it's fixed." {
			t.Fatalf("owner notice = %q", msgs)
		}
		if !slices.ContainsFunc(h.audit.All(), func(e core.AuditEntry) bool {
			return e.Action == "schedule.update" && e.Actor == "system" && strings.Contains(string(e.Detail), "consecutive_failures")
		}) {
			t.Fatalf("auto-disable not audited: %v", h.audit.Actions())
		}
		n := len(h.runner.all())
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		if len(h.runner.all()) != n {
			t.Fatal("a disabled schedule kept firing")
		}
		var statuses []core.RunStatus
		for len(sub.C()) > 0 {
			e := <-sub.C()
			statuses = append(statuses, e.Data.(core.ScheduleRunData).Status)
		}
		if len(statuses) != 14 || statuses[0] != core.RunQueued || statuses[1] != core.RunFailed {
			t.Fatalf("schedule.run events = %v", statuses)
		}

		// Re-enabling resets the count.
		got.Enabled = true
		h.upsert(got)
		if got := h.get(sc.ID); got.Failures != 0 || got.NextRunAt.IsZero() {
			t.Fatalf("re-enabled: %+v", got)
		}
	})
}

func TestLoopBusyRecordsSkipped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		sleepUntil(utc(2026, 10, 12, 10, 0))
		target, err := h.runner.NewSession(ctx, h.p, "telegram", &h.dm)
		if err != nil {
			t.Fatal(err)
		}
		sc := h.upsert(&core.Schedule{Name: "standup", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Session: target.ID, Enabled: true})
		h.runner.fail(core.ErrBusy)
		stop := h.start()
		defer stop()
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		got := h.get(sc.ID)
		if got.LastStatus != core.RunSkipped || got.Failures != 0 || !got.Enabled {
			t.Fatalf("busy target: %+v", got)
		}
		runs, err := h.store.Runs().List(ctx, core.SystemViewer(), core.RunFilter{Schedule: sc.ID})
		if err != nil || len(runs) != 1 || runs[0].Status != core.RunSkipped || runs[0].Session != target.ID || runs[0].Trigger != core.TrigSchedule {
			t.Fatalf("skipped run row = %+v, %v", runs, err)
		}

		h.runner.fail(nil)
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		fs := h.runner.all()
		if len(fs) != 1 || fs[0].req.Session != target.ID {
			t.Fatalf("schedule with a target session must reuse it: %+v", fs)
		}
	})
}
