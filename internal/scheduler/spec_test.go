package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func london(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	return loc
}

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

// seq returns the next n fire times after from.
func seq(t *testing.T, spec string, from time.Time, n int, loc *time.Location) []time.Time {
	t.Helper()
	c, err := parseCron(spec)
	if err != nil {
		t.Fatal(err)
	}
	var out []time.Time
	for range n {
		nt, err := c.next(from, loc)
		if err != nil {
			t.Fatalf("%q next after %v: %v", spec, from, err)
		}
		out = append(out, nt)
		// The loop passes clock readings in whatever zone the process has.
		from = nt.UTC()
	}
	return out
}

func wantTimes(t *testing.T, name string, got []time.Time, want ...time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("%s[%d] = %v (%v UTC), want %v UTC", name, i, got[i], got[i].UTC(), want[i])
		}
	}
}

// Europe/London 2026: clocks go forward at 01:00 UTC on 29 March (01:00 GMT ->
// 02:00 BST) and back at 01:00 UTC on 25 October (02:00 BST -> 01:00 GMT).
func TestCronSpringForward(t *testing.T) {
	loc := london(t)
	from := utc(2026, 3, 27, 12, 0)
	// A fixed-time job in the skipped hour runs once, when the clocks change.
	wantTimes(t, "30 1 * * *", seq(t, "30 1 * * *", from, 3, loc),
		utc(2026, 3, 28, 1, 30), utc(2026, 3, 29, 1, 0), utc(2026, 3, 30, 0, 30))
	// Several skipped fixed times still give one run.
	wantTimes(t, "0,15,30,45 1 * * *", seq(t, "0,15,30,45 1 * * *", utc(2026, 3, 29, 0, 50), 2, loc),
		utc(2026, 3, 29, 1, 0), utc(2026, 3, 30, 0, 0))
	// A job just after the gap is unaffected.
	wantTimes(t, "30 2 * * *", seq(t, "30 2 * * *", from, 2, loc), utc(2026, 3, 28, 2, 30), utc(2026, 3, 29, 1, 30))
	// Daily 09:00 keeps local time: the interval across the change is 23h.
	wantTimes(t, "0 9 * * *", seq(t, "0 9 * * *", from, 2, loc), utc(2026, 3, 28, 9, 0), utc(2026, 3, 29, 8, 0))
	// Wildcard jobs follow the wall clock: no extra run for the skipped hour.
	wantTimes(t, "0 * * * *", seq(t, "0 * * * *", utc(2026, 3, 28, 23, 30), 3, loc),
		utc(2026, 3, 29, 0, 0), utc(2026, 3, 29, 1, 0), utc(2026, 3, 29, 2, 0))
	wantTimes(t, "*/30 1 * * *", seq(t, "*/30 1 * * *", utc(2026, 3, 28, 23, 0), 2, loc),
		utc(2026, 3, 30, 0, 0), utc(2026, 3, 30, 0, 30))
}

func TestCronFallBack(t *testing.T) {
	loc := london(t)
	// A fixed-time job in the repeated hour runs once, at the first 01:30.
	wantTimes(t, "30 1 * * *", seq(t, "30 1 * * *", utc(2026, 10, 23, 12, 0), 3, loc),
		utc(2026, 10, 24, 0, 30), utc(2026, 10, 25, 0, 30), utc(2026, 10, 26, 1, 30))
	// Daily 09:00 keeps local time: the interval across the change is 25h.
	wantTimes(t, "0 9 * * *", seq(t, "0 9 * * *", utc(2026, 10, 24, 0, 0), 2, loc), utc(2026, 10, 24, 8, 0), utc(2026, 10, 25, 9, 0))
	// Wildcard jobs run in both copies of the repeated hour.
	wantTimes(t, "0 * * * *", seq(t, "0 * * * *", utc(2026, 10, 24, 23, 30), 4, loc),
		utc(2026, 10, 25, 0, 0), utc(2026, 10, 25, 1, 0), utc(2026, 10, 25, 2, 0), utc(2026, 10, 25, 3, 0))
	wantTimes(t, "*/20 1 * * *", seq(t, "*/20 1 * * *", utc(2026, 10, 24, 23, 30), 6, loc),
		utc(2026, 10, 25, 0, 0), utc(2026, 10, 25, 0, 20), utc(2026, 10, 25, 0, 40),
		utc(2026, 10, 25, 1, 0), utc(2026, 10, 25, 1, 20), utc(2026, 10, 25, 1, 40))
	// Starting between the two 01:30s does not fire the second one.
	wantTimes(t, "from inside the repeated hour", seq(t, "30 1 * * *", utc(2026, 10, 25, 0, 45), 1, loc), utc(2026, 10, 26, 1, 30))
	// Hourly every local day: 25 runs on the long day.
	var n int
	for _, ts := range seq(t, "0 * * * *", utc(2026, 10, 24, 22, 59), 30, loc) {
		if l := ts.In(loc); l.Day() == 25 {
			n++
		}
	}
	if n != 25 {
		t.Fatalf("hourly job ran %d times on 25 October, want 25", n)
	}
}

func TestCronOrdinary(t *testing.T) {
	from := utc(2026, 10, 12, 10, 7) // a Monday
	wantTimes(t, "weekdays", seq(t, "0 8 * * 1-5", from, 5, time.UTC),
		utc(2026, 10, 13, 8, 0), utc(2026, 10, 14, 8, 0), utc(2026, 10, 15, 8, 0), utc(2026, 10, 16, 8, 0), utc(2026, 10, 19, 8, 0))
	wantTimes(t, "every 15m", seq(t, "*/15 * * * *", from, 2, time.UTC), utc(2026, 10, 12, 10, 15), utc(2026, 10, 12, 10, 30))
	wantTimes(t, "leap day", seq(t, "0 0 29 2 *", from, 1, time.UTC), utc(2028, 2, 29, 0, 0))
	if _, err := (cronExpr{spec: "0 0 31 2 *", fixed: true}).next(from, time.UTC); err == nil {
		t.Fatal("31 February should never fire")
	}
	for _, bad := range []string{"", "* * * *", "* * * * * *", "61 * * * *", "@daily", "x y z w v"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("parseCron(%q) accepted", bad)
		}
	}
	c, err := parseCron("  0   8 * *   1-5 ")
	if err != nil || c.spec != "0 8 * * 1-5" || !c.fixed {
		t.Fatalf("parseCron = %+v, %v", c, err)
	}
	if c, _ := parseCron("*/5 8 * * *"); c.fixed {
		t.Fatal("a minute wildcard is not a fixed-time job")
	}
}

func TestActiveHours(t *testing.T) {
	loc := london(t)
	a, err := parseActiveHours("08:00-22:00")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 12, h, m, 0, 0, loc) }
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(7, 59), false}, {at(8, 0), true}, {at(21, 59), true}, {at(22, 0), false}} {
		if got := a.contains(c.t); got != c.want {
			t.Errorf("08:00-22:00 contains %v = %v", c.t, got)
		}
	}
	if got := a.nextStart(at(23, 0), loc); !got.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, loc)) {
		t.Errorf("nextStart after closing = %v", got)
	}
	if got := a.nextStart(at(6, 0), loc); !got.Equal(at(8, 0)) {
		t.Errorf("nextStart before opening = %v", got)
	}
	night, _ := parseActiveHours("22:00-06:00")
	if !night.contains(at(23, 0)) || !night.contains(at(5, 59)) || night.contains(at(12, 0)) {
		t.Error("a window across midnight is wrong")
	}
	all, _ := parseActiveHours("00:00-24:00")
	if !all.contains(at(23, 59)) || all.String() != "00:00-24:00" {
		t.Error("00:00-24:00 must cover the whole day")
	}
	for _, bad := range []string{"8-22", "08:00", "08:00-25:00", "24:00-08:00"} {
		if _, err := parseActiveHours(bad); err == nil {
			t.Errorf("parseActiveHours(%q) accepted", bad)
		}
	}
}

func TestPlanNext(t *testing.T) {
	loc := london(t)
	now := time.Date(2026, 10, 12, 21, 50, 0, 0, loc)
	hb, err := parsePlan(core.SchedHeartbeat, "30m", "Europe/London", "08:00-22:00")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := hb.next(time.Date(2026, 10, 12, 12, 0, 0, 0, loc), 0); !got.Equal(time.Date(2026, 10, 12, 12, 30, 0, 0, loc)) {
		t.Errorf("heartbeat next = %v", got)
	}
	if got, _ := hb.next(time.Date(2026, 10, 12, 12, 0, 0, 0, loc), 0.999); got.Sub(time.Date(2026, 10, 12, 12, 0, 0, 0, loc)) > 33*time.Minute {
		t.Errorf("jitter above 10%%: %v", got)
	}
	if got, _ := hb.next(time.Date(2026, 10, 12, 12, 0, 0, 0, loc), -1); !got.Equal(time.Date(2026, 10, 12, 12, 27, 0, 0, loc)) {
		t.Errorf("heartbeat with -10%% jitter = %v", got)
	}
	if got, _ := hb.next(now, 0); !got.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, loc)) {
		t.Errorf("heartbeat past closing time = %v, want 08:00 next day", got)
	}

	cron, err := parsePlan(core.SchedCron, "0 * * * *", "Europe/London", "09:00-11:00")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := cron.next(now, 0); !got.Equal(time.Date(2026, 10, 13, 9, 0, 0, 0, loc)) {
		t.Errorf("cron within active hours = %v", got)
	}
	never, _ := parsePlan(core.SchedCron, "0 3 * * *", "UTC", "09:00-11:00")
	if _, err := never.next(now, 0); err == nil {
		t.Error("a cron that never falls inside its active hours must fail")
	}

	once, err := parsePlan(core.SchedOnce, "2026-10-13T09:00", "Europe/London", "")
	if err != nil || once.spec != "2026-10-13T09:00:00+01:00" {
		t.Fatalf("once = %+v, %v", once, err)
	}
	if got, _ := once.next(now, 0); !got.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("once next = %v", got)
	}
	if got, _ := once.next(time.Date(2026, 10, 14, 0, 0, 0, 0, loc), 0); !got.IsZero() {
		t.Errorf("past once = %v, want zero", got)
	}
	if p, err := parsePlan(core.SchedOnce, "2026-10-13T09:00:00Z", "Europe/London", ""); err != nil || p.spec != "2026-10-13T10:00:00+01:00" {
		t.Errorf("RFC 3339 once = %q, %v", p.spec, err)
	}

	for _, c := range []struct{ kind, spec, tz, active string }{
		{"cron", "0 8 * * *", "Mars/Olympus", ""},
		{"cron", "0 8 * * *", "", ""},
		{"cron", "0 8 * * *", "Local", ""},
		{"heartbeat", "30s", "UTC", ""},
		{"heartbeat", "800h", "UTC", ""},
		{"heartbeat", "soon", "UTC", ""},
		{"once", "tomorrow", "UTC", ""},
		{"once", "2026-10-13T09:00", "UTC", "08:00-09:00"},
		{"weekly", "x", "UTC", ""},
		{"cron", "0 8 * * *", "UTC", "8"},
	} {
		if _, err := parsePlan(core.ScheduleKind(c.kind), c.spec, c.tz, c.active); err == nil {
			t.Errorf("parsePlan(%+v) accepted", c)
		}
	}
}

func TestDescribe(t *testing.T) {
	for spec, want := range map[string]string{
		"* * * * *":         "Every minute",
		"*/15 * * * *":      "Every 15 minutes",
		"5 * * * *":         "Every hour at :05",
		"0 */2 * * *":       "Every 2 hours at :00",
		"0 8 * * *":         "Every day at 08:00",
		"0 8 * * 1-5":       "Weekdays at 08:00",
		"30 9 * * 0,6":      "Weekends at 09:30",
		"0 9,17 * * 1":      "Mondays at 09:00 and 17:00",
		"0 7 * * 1,3,5":     "Mondays, Wednesdays and Fridays at 07:00",
		"0 6 1 * *":         "Monthly on day 1 at 06:00",
		"0 6 5 3 *":         "Every year on 5 March at 06:00",
		"0 6 1-7 * 1":       "Repeat rule 0 6 1-7 * 1",
		"0-30/5 8-10 * * *": "Repeat rule 0-30/5 8-10 * * *",
	} {
		p, err := parsePlan(core.SchedCron, spec, "Europe/London", "")
		if err != nil {
			t.Fatal(err)
		}
		if got := describe(p); got != want+" (Europe/London)" {
			t.Errorf("describe(%q) = %q, want %q", spec, got, want)
		}
	}
	hb, _ := parsePlan(core.SchedHeartbeat, "30m0s", "UTC", "08:00-22:00")
	if got := describe(hb); got != "Every 30 minutes (±10%), between 08:00 and 22:00 (UTC)" {
		t.Errorf("heartbeat = %q", got)
	}
	once, _ := parsePlan(core.SchedOnce, "2026-10-13T09:00", "Europe/London", "")
	if got := describe(once); !strings.HasPrefix(got, "Once, on Tue 13 Oct 2026 at 09:00") {
		t.Errorf("once = %q", got)
	}
}
