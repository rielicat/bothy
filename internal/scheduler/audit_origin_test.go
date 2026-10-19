package scheduler

import (
	"context"
	"encoding/json"
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
