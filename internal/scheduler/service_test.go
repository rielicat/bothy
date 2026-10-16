package scheduler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

func TestNewRequiresDeps(t *testing.T) {
	st, cfg, r := memstore.New(), config.Static(config.Default()), &runner{}
	for name, d := range map[string]Deps{
		"store":  {Config: cfg, Runner: r},
		"config": {Store: st, Runner: r},
		"runner": {Store: st, Config: cfg},
	} {
		if _, err := New(d); err == nil {
			t.Errorf("%s: New accepted a missing dependency", name)
		}
	}
}

func TestUpsertNormalizesAndManagesState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 10, 7))
		sc := h.upsert(&core.Schedule{
			Name: "  Morning brief ", Kind: core.SchedCron, Spec: " 0  8 * * 1-5", Prompt: "Summarize my inbox", Enabled: true,
			Failures: 9, LastStatus: core.RunDone, CreatedBy: "user:forged", NextRunAt: utc(2000, 1, 1, 0, 0),
		})
		got := h.get(sc.ID)
		if got.Name != "Morning brief" || got.Spec != "0 8 * * 1-5" || got.TZ != "Europe/London" || got.Slot != core.SlotMain ||
			got.MissedRun != "skip" || got.Failures != 0 || got.LastStatus != "" || got.CreatedBy != "user:"+string(h.owner) ||
			!got.NextRunAt.Equal(utc(2026, 10, 13, 7, 0)) {
			t.Fatalf("stored = %+v", got)
		}
		if sc.ID == "" || sc.Name != got.Name || !sc.NextRunAt.Equal(got.NextRunAt) {
			t.Fatalf("Upsert must update the caller's schedule: %+v", sc)
		}

		// Updates keep run state and created_by; disabling clears next_run_at.
		_ = h.store.Schedules().SetRun(context.Background(), sc.ID, utc(2026, 10, 12, 7, 0), got.NextRunAt, core.RunFailed, 2, true)
		upd := *got
		upd.Enabled, upd.Prompt, upd.CreatedBy = false, "Summarize my inbox briefly", "system"
		h.upsert(&upd)
		got = h.get(sc.ID)
		if got.Enabled || !got.NextRunAt.IsZero() || got.Failures != 2 || got.LastStatus != core.RunFailed ||
			got.CreatedBy != "user:"+string(h.owner) || got.Prompt != "Summarize my inbox briefly" {
			t.Fatalf("after update = %+v", got)
		}
		acts := h.audit.Actions()
		if !slices.Equal(acts, []string{"schedule.create", "schedule.update"}) {
			t.Fatalf("audit = %v", acts)
		}
		for _, e := range h.audit.All() {
			if strings.Contains(string(e.Detail), "inbox") {
				t.Fatalf("audit detail leaks the prompt: %s", e.Detail)
			}
		}

		hb := h.upsert(&core.Schedule{Name: "mine", Kind: core.SchedHeartbeat, Spec: "45m", Enabled: true})
		if hb.Prompt != heartbeatInstruction || hb.Slot != core.SlotFast || !hb.NextRunAt.Equal(utc(2026, 10, 12, 10, 52)) {
			t.Fatalf("member heartbeat defaults = %+v", hb)
		}
	})
}

// A schedule saved without a time zone, from the API or the form, runs on
// its maker's clock, as one made in chat does; the configured zone is only
// for callers who are nobody in particular.
func TestUpsertDefaultsToTheViewersZone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.setZone("America/Santiago")
		sleepUntil(utc(2026, 10, 12, 10, 7))
		sc := h.upsert(&core.Schedule{Name: "brief", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "Brief me", Enabled: true})
		if got := h.get(sc.ID); got.TZ != "America/Santiago" || !got.NextRunAt.Equal(utc(2026, 10, 12, 11, 0)) {
			t.Fatalf("stored = %+v; want the owner's zone", got)
		}
		named := h.upsert(&core.Schedule{Name: "tokyo", Kind: core.SchedCron, Spec: "0 8 * * *", TZ: "Asia/Tokyo", Prompt: "Brief me", Enabled: true})
		if got := h.get(named.ID); got.TZ != "Asia/Tokyo" {
			t.Fatalf("a named zone was replaced: %+v", got)
		}
		sys := &core.Schedule{Principal: h.p, Name: "system", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "Brief me", Enabled: true}
		if err := h.svc.Upsert(context.Background(), core.SystemViewer(), sys, "system"); err != nil {
			t.Fatal(err)
		}
		if got := h.get(sys.ID); got.TZ != "Europe/London" {
			t.Fatalf("system schedule zone = %q; want the configured one", got.TZ)
		}
	})
}

func TestUpsertValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other := &core.User{Username: "bob", Role: core.RoleMember, CreatedAt: time.Now()}
	if err := h.store.Users().Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	base := func() *core.Schedule {
		return &core.Schedule{Principal: h.p, Name: "x", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Enabled: true}
	}
	for _, c := range []struct {
		name string
		mut  func(*core.Schedule)
		want string
	}{
		{"no name", func(s *core.Schedule) { s.Name = " " }, "name is required"},
		{"long name", func(s *core.Schedule) { s.Name = strings.Repeat("n", 101) }, "longer than 100"},
		{"control char", func(s *core.Schedule) { s.Name = "a\x07b" }, "control"},
		{"bad kind", func(s *core.Schedule) { s.Kind = "weekly" }, "kind must be"},
		{"bad cron", func(s *core.Schedule) { s.Spec = "every day" }, "5 fields"},
		{"never", func(s *core.Schedule) { s.Spec = "0 0 31 2 *" }, "never fires"},
		{"bad tz", func(s *core.Schedule) { s.TZ = "Mars/Olympus" }, "unknown time zone"},
		{"bad active", func(s *core.Schedule) { s.ActiveHours = "9-5" }, "active hours"},
		{"no prompt", func(s *core.Schedule) { s.Prompt = "  " }, "prompt is required"},
		{"long prompt", func(s *core.Schedule) { s.Prompt = strings.Repeat("p", 8001) }, "longer than 8000"},
		{"bad missed", func(s *core.Schedule) { s.MissedRun = "all" }, "missed_run"},
		{"bad slot", func(s *core.Schedule) { s.Slot = "turbo" }, "slot"},
		{"negative limits", func(s *core.Schedule) { s.Limits.MaxSteps = -1 }, "negative"},
		{"past once", func(s *core.Schedule) { s.Kind, s.Spec = core.SchedOnce, "2020-01-01T00:00:00Z" }, "in the past"},
		{"once with active hours", func(s *core.Schedule) { s.Kind, s.Spec, s.ActiveHours = core.SchedOnce, future, "08:00-09:00" }, "active hours"},
		{"tiny interval", func(s *core.Schedule) { s.Kind, s.Spec = core.SchedHeartbeat, "10s" }, "between 1m"},
		{"foreign chat", func(s *core.Schedule) { s.Deliver = &core.ChatRef{Channel: "telegram", ChatID: "999"} }, "is not this principal's own chat"},
		{"bad channel", func(s *core.Schedule) { s.Deliver = &core.ChatRef{Channel: "Tele gram"} }, "unknown channel"},
		{"foreign session", func(s *core.Schedule) { s.Session = "ses_00000000000000000000000000" }, "not a session"},
	} {
		sc := base()
		c.mut(sc)
		err := h.svc.Upsert(ctx, h.viewer(), sc, "user:x")
		if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalid containing %q", c.name, err, c.want)
		}
	}

	// Viewer scoping: another principal's schedules do not exist for you.
	bobV := core.Viewer{User: other.ID, Principals: []core.PrincipalID{core.UserPrincipal(other.ID)}}
	if err := h.svc.Upsert(ctx, bobV, base(), "user:"+string(other.ID)); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Upsert for a foreign principal: %v", err)
	}
	mine := h.upsert(base())
	for name, err := range map[string]error{
		"update": h.svc.Upsert(ctx, bobV, &core.Schedule{ID: mine.ID, Name: "x", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p"}, "user:b"),
		"delete": h.svc.Delete(ctx, bobV, mine.ID, other.ID),
		"run":    func() error { _, err := h.svc.RunNow(ctx, bobV, mine.ID, other.ID); return err }(),
	} {
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("%s across principals: %v", name, err)
		}
	}
	if err := h.svc.Upsert(ctx, h.viewer(), &core.Schedule{ID: "sch_00000000000000000000000000", Name: "x", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p"}, "user:x"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Upsert with an unknown id: %v", err)
	}
	moved := *mine
	moved.Principal = core.UserPrincipal(other.ID)
	if err := h.svc.Upsert(ctx, core.SystemViewer(), &moved, "system"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("moving a schedule to another principal: %v", err)
	}

	// Empty principal defaults to the viewer's own.
	sc := base()
	sc.Principal = ""
	if err := h.svc.Upsert(ctx, h.viewer(), sc, "user:x"); err != nil || sc.Principal != h.p {
		t.Fatalf("default principal: %q %v", sc.Principal, err)
	}

	if err := h.svc.Delete(ctx, h.viewer(), mine.ID, h.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Schedules().Get(ctx, core.SystemViewer(), mine.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("deleted schedule still there: %v", err)
	}
	if acts := h.audit.Actions(); acts[len(acts)-1] != "schedule.delete" {
		t.Fatalf("delete not audited: %v", acts)
	}
}

func TestUpsertDeliverTargets(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "telegram", ChatID: "-100", Kind: core.ChatGroup}, Enabled: true, CreatedAt: time.Now()}
	if err := h.store.Spaces().Create(ctx, sp, &core.Principal{ToolProfile: "restricted", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	other := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "telegram", ChatID: "-200", Kind: core.ChatGroup}, Enabled: true, CreatedAt: time.Now()}
	if err := h.store.Spaces().Create(ctx, other, &core.Principal{ToolProfile: "restricted", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []core.PrincipalID{sp.Principal, h.tp} {
		hb := &core.Schedule{Principal: p, Name: "hb", Kind: core.SchedHeartbeat, Spec: "30m", Enabled: true}
		if err := h.svc.Upsert(ctx, core.SystemViewer(), hb, "user:x"); !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "a check-in needs an account on the web") {
			t.Fatalf("heartbeat for %s: %v", p, err)
		}
	}
	for _, c := range []struct {
		in, want *core.ChatRef
	}{
		{nil, nil},
		{&core.ChatRef{Channel: "web", ChatID: "ignored"}, &core.ChatRef{Channel: "web"}},
	} {
		sc := h.upsert(&core.Schedule{Name: "d", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Deliver: c.in})
		if (sc.Deliver == nil) != (c.want == nil) || (c.want != nil && *sc.Deliver != *c.want) {
			t.Errorf("deliver %+v stored as %+v, want %+v", c.in, sc.Deliver, c.want)
		}
	}
	// An account has no chat: its schedule carries its private context, so
	// never into a chat app.
	for _, d := range []*core.ChatRef{{Channel: "telegram", ChatID: "-100"}, {Channel: "telegram", ChatID: "42"}} {
		userToChat := &core.Schedule{Principal: h.p, Name: "d", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Deliver: d}
		if err := h.svc.Upsert(ctx, h.viewer(), userToChat, "user:x"); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("account schedule delivered to %+v: %v", d, err)
		}
	}
	// A teammate's schedule goes to their own DM; never the web.
	for _, c := range []struct {
		in   *core.ChatRef
		want *core.ChatRef
		bad  string
	}{
		{in: &core.ChatRef{Channel: "telegram", ChatID: "42"}, want: &h.dm},
		{in: &core.ChatRef{Channel: "telegram"}, want: &core.ChatRef{Channel: "telegram"}},
		{in: &core.ChatRef{Channel: "web"}, bad: "web is only for accounts on the web"},
		{in: &core.ChatRef{Channel: "telegram", ChatID: "-100"}, bad: "is not this principal's own chat"},
	} {
		sc := &core.Schedule{Principal: h.tp, Name: "t", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Deliver: c.in}
		err := h.svc.Upsert(ctx, core.Viewer{Principals: []core.PrincipalID{h.tp}}, sc, "user:x")
		switch {
		case c.bad != "" && (!errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("teammate schedule to %+v: err = %v, want %q", c.in, err, c.bad)
		case c.bad == "" && (err != nil || *sc.Deliver != *c.want):
			t.Errorf("teammate schedule to %+v stored as %+v (%v), want %+v", c.in, sc.Deliver, err, c.want)
		}
	}
	// A channel's schedule goes to its own chat, not another channel or a DM.
	teamV := core.Viewer{Principals: []core.PrincipalID{sp.Principal, other.Principal}}
	for _, c := range []struct {
		in   *core.ChatRef
		want *core.ChatRef
	}{
		{&core.ChatRef{Channel: "telegram", ChatID: "-100", ThreadID: "7"}, &core.ChatRef{Channel: "telegram", ChatID: "-100", ThreadID: "7", Kind: core.ChatGroup}},
		{&core.ChatRef{Channel: "telegram", ChatID: "42"}, nil},
		{&core.ChatRef{Channel: "telegram", ChatID: "-200"}, nil},
	} {
		sc := &core.Schedule{Principal: sp.Principal, Name: "s", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Deliver: c.in}
		err := h.svc.Upsert(ctx, teamV, sc, "user:x")
		switch {
		case c.want == nil && !errors.Is(err, core.ErrInvalid):
			t.Errorf("channel schedule to %+v: err = %v, want ErrInvalid", c.in, err)
		case c.want != nil && (err != nil || *sc.Deliver != *c.want):
			t.Errorf("channel schedule to %+v stored as %+v (%v), want %+v", c.in, sc.Deliver, err, c.want)
		}
	}
}

func TestRunNowAndSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		sleepUntil(utc(2026, 10, 12, 22, 30)) // 23:30 BST
		sc := h.upsert(&core.Schedule{Name: "Digest", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "Make a digest",
			Limits: core.RunLimits{MaxSteps: 5}, Deliver: &core.ChatRef{Channel: "web"}, Enabled: true})
		next := sc.NextRunAt
		run1, err := h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner)
		if err != nil || run1 == "" {
			t.Fatalf("RunNow = %q, %v", run1, err)
		}
		time.Sleep(time.Hour)
		run2, err := h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner)
		if err != nil {
			t.Fatal(err)
		}
		got := h.get(sc.ID)
		if !got.NextRunAt.Equal(next) || got.LastStatus != core.RunQueued || !got.LastRunAt.Equal(utc(2026, 10, 12, 23, 30)) {
			t.Fatalf("RunNow must not move the next run: %+v", got)
		}
		fs := h.runner.all()
		if len(fs) != 2 || fs[0].run != run1 || fs[1].run != run2 {
			t.Fatalf("fires = %+v", fs)
		}
		r := fs[0].req
		if r.Trigger != core.TrigSchedule || r.Provenance != core.ProvUser || r.Sender != "schedule:"+string(sc.ID) ||
			r.Input[0].Text != "Make a digest" || r.Limits == nil || r.Limits.MaxSteps != 5 || r.Sink != nil || r.Slot != core.SlotMain {
			t.Fatalf("request = %+v", r)
		}
		// A fresh session per run, titled with the schedule name and local date.
		s1, err1 := h.store.Sessions().Get(ctx, core.SystemViewer(), fs[0].req.Session)
		s2, err2 := h.store.Sessions().Get(ctx, core.SystemViewer(), fs[1].req.Session)
		if err1 != nil || err2 != nil || s1.ID == s2.ID || s1.Origin != "schedule" ||
			s1.Title != "Digest · 2026-10-12" || s2.Title != "Digest · 2026-10-13" {
			t.Fatalf("sessions = %+v %+v (%v %v)", s1, s2, err1, err2)
		}
		// The run is the owner's; the audit says who pressed Run now.
		started := func() []core.AuditEntry {
			var out []core.AuditEntry
			for _, e := range h.audit.All() {
				if e.Action == "schedule.run" {
					out = append(out, e)
				}
			}
			return out
		}
		if a := started(); len(a) != 2 || a[0].Actor != "user:"+string(h.owner) || a[0].Target != "schedule:"+string(sc.ID) ||
			a[0].Run != run1 || a[1].Run != run2 {
			t.Fatalf("Run now audit = %+v", a)
		}

		h.runner.fail(errors.New("boom"))
		if _, err := h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner); err == nil {
			t.Fatal("RunNow must report a failed submit")
		}
		if got := h.get(sc.ID); got.Failures != 1 || got.LastStatus != core.RunFailed {
			t.Fatalf("failed manual run not recorded: %+v", got)
		}
		h.runner.fail(&core.BudgetError{Kind: core.BudgetDailyCost})
		if _, err := h.svc.RunNow(ctx, h.viewer(), sc.ID, h.owner); err == nil {
			t.Fatal("RunNow must report the budget refusal")
		}
		if got := h.get(sc.ID); got.Failures != 1 || got.LastStatus != core.RunBudgetExceeded {
			t.Fatalf("a daily-cap refusal is not the schedule's failure: %+v", got)
		}
		if n := len(started()); n != 2 {
			t.Fatalf("a Run now that started nothing was audited (%d entries)", n)
		}
	})
}

func TestDeliveryDefaults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	prin := func(id core.PrincipalID) *core.Principal {
		p, err := h.store.Principals().Get(ctx, core.SystemViewer(), id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	user, mate := prin(h.p), prin(h.tp)
	for _, c := range []struct {
		p       *core.Principal
		deliver *core.ChatRef
		want    string // chat key or "" for web only
	}{
		{mate, nil, h.dm.Key()},
		{mate, &core.ChatRef{Channel: "telegram"}, h.dm.Key()},
		{mate, &core.ChatRef{Channel: "discord"}, ""}, // they aren't on discord
		{user, nil, ""},
		{user, &core.ChatRef{Channel: "web"}, ""},
		{user, &core.ChatRef{Channel: "telegram"}, ""},
	} {
		sc := &core.Schedule{Principal: c.p.ID, Kind: core.SchedCron, Deliver: c.deliver}
		chat, ok := h.svc.deliveryChat(ctx, sc, c.p)
		if got := map[bool]string{true: chat.Key(), false: ""}[ok]; got != c.want {
			t.Errorf("%s deliver %+v -> %q, want %q", c.p.Kind, c.deliver, got, c.want)
		}
	}
	h.hub.PrincipalChats = nil
	if _, ok := h.svc.deliveryChat(ctx, &core.Schedule{}, mate); ok {
		t.Error("a teammate the hub can't reach hears on no chat")
	}
}

// A team schedule fires only while its teammate or channel may run: a skip,
// never a failure, otherwise.
func TestTeamScheduleSkippedWhenInactive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 10, 0))
		sc := h.teamUpsert(&core.Schedule{Name: "digest", Kind: core.SchedCron, Spec: "*/10 * * * *", TZ: "UTC", Prompt: "x", Enabled: true})
		h.hub.TeamActiveErr = map[core.PrincipalID]error{h.tp: errors.New("no longer in the team")}
		stop := h.start()
		defer stop()
		sleepUntil(utc(2026, 10, 12, 10, 25))
		synctest.Wait()
		if n := len(h.runner.all()); n != 0 {
			t.Fatalf("an inactive teammate's schedule fired %d runs", n)
		}
		if got := h.get(sc.ID); got.Failures != 0 || !got.Enabled {
			t.Fatalf("skips counted as failures: %+v", got)
		}
		h.hub.SetTeamActiveErr(nil)
		sleepUntil(utc(2026, 10, 12, 10, 31))
		synctest.Wait()
		fs := h.runner.all()
		if len(fs) != 1 || fs[0].req.User != "" || fs[0].req.Principal != h.tp || fs[0].req.Sink == nil {
			t.Fatalf("runs once active again = %+v, want one for the teammate, billed to no account, delivered", fs)
		}
	})
}

func TestPreviewAndDescribe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		sleepUntil(utc(2026, 10, 12, 10, 7))
		got, err := h.svc.Preview(core.SchedCron, "0 8 * * 1-5", "", 0)
		if err != nil || len(got) != 5 || !got[0].Equal(utc(2026, 10, 13, 7, 0)) || !got[4].Equal(utc(2026, 10, 19, 7, 0)) ||
			got[0].Location().String() != "Europe/London" {
			t.Fatalf("Preview = %v, %v", got, err)
		}
		if got, _ := h.svc.Preview(core.SchedHeartbeat, "30m", "UTC", 3); len(got) != 3 || !got[2].Equal(utc(2026, 10, 12, 11, 37)) {
			t.Fatalf("heartbeat preview = %v", got)
		}
		if got, _ := h.svc.Preview(core.SchedOnce, "2026-10-13T09:00", "UTC", 5); len(got) != 1 {
			t.Fatalf("once preview = %v", got)
		}
		if got, _ := h.svc.Preview(core.SchedCron, "* * * * *", "UTC", 500); len(got) != maxPreview {
			t.Fatalf("preview cap: %d", len(got))
		}
		if _, err := h.svc.Preview(core.SchedCron, "nope", "UTC", 5); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("bad spec: %v", err)
		}
		if s, err := h.svc.Describe(core.SchedCron, "0 8 * * 1-5", "Europe/London"); err != nil || s != "Weekdays at 08:00 (Europe/London)" {
			t.Fatalf("Describe = %q, %v", s, err)
		}
		if _, err := h.svc.Describe(core.SchedCron, "0 8 * * 1-5", "Nowhere/Land"); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("Describe bad tz: %v", err)
		}
	})
}
