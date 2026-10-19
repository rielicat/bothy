package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// A change made over HTTP is audited once, here, with the request's actor,
// address and detail: the handler no longer writes a second row.
func TestChangesCarryTheirRequestOrigin(t *testing.T) {
	h := newHarness(t)
	ctx := core.WithAuditOrigin(context.Background(), core.AuditOrigin{Actor: "token:tok_x", IP: "10.0.0.7", Detail: map[string]any{"grants": 2}})
	sc := &core.Schedule{Principal: h.p, Name: "Weekly digest", Kind: core.SchedCron, Spec: "0 8 * * 1", Prompt: "Summarize", Enabled: true}
	if err := h.svc.Upsert(ctx, h.viewer(), sc, "user:"+string(h.owner)); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Delete(ctx, h.viewer(), sc.ID, h.owner); err != nil {
		t.Fatal(err)
	}
	all := h.audit.All()
	if len(all) != 2 {
		t.Fatalf("audit = %v", h.audit.Actions())
	}
	for _, e := range all {
		var d map[string]any
		_ = json.Unmarshal(e.Detail, &d)
		if e.Actor != "token:tok_x" || e.IP != "10.0.0.7" || d["grants"] != float64(2) || d["name"] != "Weekly digest" {
			t.Fatalf("%s = %+v %s", e.Action, e, e.Detail)
		}
	}
}

// Trying a schedule out with Run now never turns it off, however it ends.
func TestRunNowFailuresDontTurnAScheduleOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 10, 0))
		sc := h.upsert(&core.Schedule{Name: "QA failing", Kind: core.SchedCron, Spec: "0 6 * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		stop := h.start()
		defer stop()
		for range maxFailures + 1 {
			run, err := h.svc.RunNow(context.Background(), h.viewer(), sc.ID, h.owner)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			h.finish(run, core.RunFailed)
			synctest.Wait()
		}
		if got := h.get(sc.ID); !got.Enabled || got.Failures != 0 || got.LastStatus != core.RunFailed {
			t.Fatalf("after failed Run now = %+v", got)
		}
	})
}

// An account has no chat app: the notice that its schedule was turned off
// lands in the chat of the last failed task.
func TestAccountHearsItsScheduleWasTurnedOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 10, 0))
		sc := h.upsert(&core.Schedule{Name: "Morning brief", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		stop := h.start()
		defer stop()
		var last core.RunID
		for range maxFailures {
			time.Sleep(10 * time.Minute)
			synctest.Wait()
			fs := h.runner.all()
			last = fs[len(fs)-1].run
			h.finish(last, core.RunFailed)
			synctest.Wait()
		}
		if got := h.get(sc.ID); got.Enabled {
			t.Fatalf("not turned off: %+v", got)
		}
		r, err := h.store.Runs().Get(context.Background(), core.SystemViewer(), last)
		if err != nil {
			t.Fatal(err)
		}
		msgs, err := h.store.Messages().Range(context.Background(), core.SystemViewer(), r.Session, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var notices []string
		for _, m := range msgs {
			if m.Kind == core.KindNotice {
				notices = append(notices, m.PlainText())
			}
		}
		if len(notices) != 1 || notices[0] != "The schedule “Morning brief” was turned off because its last 3 tasks failed. Turn it back on under Schedules once it's fixed." {
			t.Fatalf("notices = %q", notices)
		}
	})
}

// A tick that fails before its task starts leaves no task chat to hear in:
// the notice goes to the chat that tick made for it, or to a new one when the
// tick failed before making one.
func TestAccountHearsItsScheduleWasTurnedOffWithoutATask(t *testing.T) {
	for name, fault := range map[string]func(*runner){
		"the task never started": func(r *runner) { r.fail(errors.New("no model is set up")) },
		"no chat was made":       func(r *runner) { r.sessionFails = maxFailures },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t)
				ctx := context.Background()
				fault(h.runner)
				sleepUntil(utc(2026, 10, 12, 10, 0))
				sc := h.upsert(&core.Schedule{Name: "Morning brief", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
				sub := h.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{h.p}, Types: []core.EventType{core.EvMessageCreated}}, 8)
				defer sub.Close()
				stop := h.start()
				defer stop()
				for range maxFailures {
					time.Sleep(10 * time.Minute)
					synctest.Wait()
				}
				if got := h.get(sc.ID); got.Enabled || got.Failures != maxFailures || len(h.runner.all()) != 0 {
					t.Fatalf("schedule = %+v, tasks = %+v", got, h.runner.all())
				}
				chats, err := h.store.Sessions().List(ctx, h.viewer(), core.SessionFilter{NonEmpty: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(chats) != 1 || chats[0].Title != "Morning brief · 2026-10-12" || chats[0].LastMessageAt.IsZero() {
					t.Fatalf("chats = %+v", chats)
				}
				msgs, err := h.store.Messages().Range(ctx, h.viewer(), chats[0].ID, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				if len(msgs) != 1 || msgs[0].Kind != core.KindNotice ||
					msgs[0].PlainText() != "The schedule “Morning brief” was turned off because its last 3 tasks failed. Turn it back on under Schedules once it's fixed." {
					t.Fatalf("messages = %+v", msgs)
				}
				if len(sub.C()) != 1 {
					t.Fatalf("message.created events = %d, want 1", len(sub.C()))
				}
			})
		})
	}
}

// Deleting the failed task's chat archives it, where nobody reads: the notice
// goes to a new chat instead.
func TestAccountHearsItsScheduleWasTurnedOffAfterDeletingTheChat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		sleepUntil(utc(2026, 10, 12, 10, 0))
		sc := h.upsert(&core.Schedule{Name: "Morning brief", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		stop := h.start()
		defer stop()
		var gone core.SessionID
		for range maxFailures {
			time.Sleep(10 * time.Minute)
			synctest.Wait()
			fs := h.runner.all()
			last := fs[len(fs)-1]
			gone = last.req.Session
			if err := h.store.Sessions().Archive(ctx, h.viewer(), gone); err != nil {
				t.Fatal(err)
			}
			h.finish(last.run, core.RunFailed)
			synctest.Wait()
		}
		if got := h.get(sc.ID); got.Enabled {
			t.Fatalf("not turned off: %+v", got)
		}
		if msgs, err := h.store.Messages().Range(ctx, core.SystemViewer(), gone, 0, 0); err != nil || len(msgs) != 0 {
			t.Fatalf("deleted chat got %+v (%v)", msgs, err)
		}
		chats, err := h.store.Sessions().List(ctx, h.viewer(), core.SessionFilter{NonEmpty: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(chats) != 1 || chats[0].ID == gone || chats[0].Title != "Morning brief · 2026-10-12" {
			t.Fatalf("chats = %+v", chats)
		}
		msgs, err := h.store.Messages().Range(ctx, h.viewer(), chats[0].ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Kind != core.KindNotice || msgs[0].Run != "" ||
			msgs[0].PlainText() != "The schedule “Morning brief” was turned off because its last 3 tasks failed. Turn it back on under Schedules once it's fixed." {
			t.Fatalf("messages = %+v", msgs)
		}
	})
}
