package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
)

// flakySchedules fails SetRun while failures remain.
type flakySchedules struct {
	core.ScheduleRepo
	failures *atomic.Int32
}

func (r flakySchedules) SetRun(ctx context.Context, id core.ScheduleID, last, next time.Time, status core.RunStatus, failures int, enabled bool) error {
	if r.failures.Add(-1) >= 0 {
		return errors.New("database is locked")
	}
	return r.ScheduleRepo.SetRun(ctx, id, last, next, status, failures, enabled)
}

type flakyStore struct {
	core.Store
	failures *atomic.Int32
}

func (s flakyStore) Schedules() core.ScheduleRepo {
	return flakySchedules{s.Store.Schedules(), s.failures}
}

func TestLoopUnsavedTickIsNotFiredAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 10, 0))
		sc := h.upsert(&core.Schedule{Name: "tick", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		fail := &atomic.Int32{}
		fail.Store(3)
		h.svc.store = flakyStore{h.store, fail}
		stop := h.start()
		defer stop()

		sleepUntil(utc(2026, 10, 12, 10, 12))
		synctest.Wait()
		if n := len(h.runner.all()); n != 1 {
			t.Fatalf("a tick whose row update failed fired %d runs, want 1", n)
		}
		got := h.get(sc.ID)
		if !got.NextRunAt.Equal(utc(2026, 10, 12, 10, 20)) || got.LastStatus != core.RunQueued || !got.LastRunAt.Equal(utc(2026, 10, 12, 10, 10)) {
			t.Fatalf("row after the store recovered = %+v", got)
		}
		sleepUntil(utc(2026, 10, 12, 10, 21))
		synctest.Wait()
		if n := len(h.runner.all()); n != 2 {
			t.Fatalf("next tick: %d runs, want 2", n)
		}
	})
}

func TestDeliveryRecheckedWhenFiring(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sc := h.upsert(&core.Schedule{Name: "d", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Deliver: &core.ChatRef{Channel: "telegram", ChatID: "42"}, Enabled: true})
	if _, err := h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner); err != nil {
		t.Fatal(err)
	}
	if fs := h.runner.all(); fs[0].req.Sink == nil {
		t.Fatal("a linked DM must be delivered to")
	}

	// The Telegram account is re-linked to someone else after the schedule was saved.
	bob := &core.User{Username: "bob", Role: core.RoleMember, CreatedAt: time.Now()}
	if err := h.store.Users().Create(ctx, bob); err != nil {
		t.Fatal(err)
	}
	id, err := h.store.Identities().Get(ctx, "telegram", "42")
	if err != nil {
		t.Fatal(err)
	}
	id.User = bob.ID
	if err := h.store.Identities().Update(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner); err != nil {
		t.Fatal(err)
	}
	if fs := h.runner.all(); len(fs) != 2 || fs[1].req.Sink != nil {
		t.Fatal("output went to a chat that no longer belongs to the schedule's owner")
	}
}

func TestDisabledUserAndSpaceDoNotFire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		sleepUntil(utc(2026, 10, 12, 10, 0))
		mine := h.upsert(&core.Schedule{Name: "mine", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		sp := &core.Space{Kind: "group", Chat: core.ChatRef{Channel: "telegram", ChatID: "-100", Kind: core.ChatGroup}, Owner: h.owner, CreatedAt: time.Now()}
		if err := h.store.Spaces().Create(ctx, sp, &core.Principal{Kind: core.PrincipalSpace, User: h.owner, ToolProfile: "restricted", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		group := &core.Schedule{Principal: sp.Principal, Name: "group", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true}
		if err := h.svc.Upsert(ctx, core.SystemViewer(), group, "user:"+string(h.owner)); err != nil {
			t.Fatal(err)
		}
		stop := h.start()
		defer stop()

		sleepUntil(utc(2026, 10, 12, 10, 15))
		synctest.Wait()
		if fs := h.runner.all(); len(fs) != 1 || fs[0].req.Schedule != mine.ID {
			t.Fatalf("a space that is not enabled fired: %+v", fs)
		}
		if _, err := h.svc.RunNow(ctx, core.SystemViewer(), group.ID, h.owner); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("RunNow for a disabled space: %v", err)
		}

		u, _ := h.store.Users().Get(ctx, h.owner)
		u.Disabled = true
		if err := h.store.Users().Update(ctx, u); err != nil {
			t.Fatal(err)
		}
		sleepUntil(utc(2026, 10, 12, 11, 5))
		synctest.Wait()
		if n := len(h.runner.all()); n != 1 {
			t.Fatalf("a disabled user's schedule fired %d more runs", n-1)
		}
		if got := h.get(mine.ID); got.Failures != 0 || !got.Enabled || !got.NextRunAt.Equal(utc(2026, 10, 12, 11, 10)) {
			t.Fatalf("skipped ticks must move on without counting as failures: %+v", got)
		}
		if _, err := h.svc.RunNow(ctx, h.viewer(), mine.ID, h.owner); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("RunNow for a disabled user: %v", err)
		}
	})
}

func TestScheduleLimitsDefaultFromConfig(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d := h.cfg.Current().Budgets.Scheduled
	partial := h.upsert(&core.Schedule{Name: "p", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "x", Limits: core.RunLimits{MaxSteps: 3}, Enabled: true})
	unset := h.upsert(&core.Schedule{Name: "u", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "x", Enabled: true})
	for _, id := range []core.ScheduleID{partial.ID, unset.ID} {
		if _, err := h.svc.RunNow(ctx, h.viewer(), id, h.owner); err != nil {
			t.Fatal(err)
		}
	}
	fs := h.runner.all()
	want := core.RunLimits{MaxSteps: 3, MaxTokens: d.MaxTokens, MaxTime: d.MaxTime.D(), MaxCost: core.MicrosFromUSD(d.MaxUSD)}
	if fs[0].req.Limits == nil || *fs[0].req.Limits != want {
		t.Fatalf("partial limits = %+v, want %+v", fs[0].req.Limits, want)
	}
	want.MaxSteps = d.MaxSteps
	if fs[1].req.Limits == nil || *fs[1].req.Limits != want {
		t.Fatalf("default limits = %+v, want %+v", fs[1].req.Limits, want)
	}
}

func TestDeleteToolAuditsTheAgent(t *testing.T) {
	h := newHarness(t)
	sc := h.upsert(&core.Schedule{Name: "x", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Enabled: true})
	env := h.env(&h.dm)
	run(t, h.tool("schedule_delete"), env, `{"id":"`+string(sc.ID)+`"}`)
	all := h.audit.All()
	if last := all[len(all)-1]; last.Action != "schedule.delete" || last.Actor != "agent:"+string(env.Run) || last.Run != env.Run {
		t.Fatalf("agent delete audited as %+v", last)
	}
}

// lockProbeHub records whether the scheduler lock was free while a notice was sent.
type lockProbeHub struct {
	*fakes.Hub
	svc      *Service
	unlocked atomic.Bool
}

func (h *lockProbeHub) Send(ctx context.Context, chat core.ChatRef, markdown string) (core.MsgRef, error) {
	if h.svc.mu.TryLock() {
		h.unlocked.Store(true)
		h.svc.mu.Unlock()
	}
	return h.Hub.Send(ctx, chat, markdown)
}

func TestDisableNoticeSentOutsideLock(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	probe := &lockProbeHub{Hub: h.hub, svc: h.svc}
	h.svc.hub = probe
	sc := h.upsert(&core.Schedule{Name: "x", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Enabled: true})
	h.runner.fail(errors.New("boom"))
	for range maxFailures {
		_, _ = h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner)
	}
	if got := h.get(sc.ID); got.Enabled {
		t.Fatalf("three failed submits must disable: %+v", got)
	}
	msgs := h.hub.Messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "was turned off") {
		t.Fatalf("notices = %q", msgs)
	}
	if !probe.unlocked.Load() {
		t.Fatal("the owner notice was sent while holding the scheduler lock")
	}
}
