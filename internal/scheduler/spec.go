package scheduler

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/adhocore/gronx"

	"github.com/rielicat/bothy/internal/core"
)

const (
	// dstSlack bounds how far a DST transition can move local time; ISC cron
	// uses the same 3 hours to tell a DST change from a clock adjustment.
	dstSlack = 3 * time.Hour

	minInterval = time.Minute // matches config validation of scheduler.heartbeat.every
	maxInterval = 31 * 24 * time.Hour

	// cronSearchLimit caps the gronx steps per next-run search; a 5-field
	// expression never needs more than a few hundred near a DST change.
	cronSearchLimit = 4000
	// activeSearchLimit caps how many cron ticks are skipped looking for one
	// inside the active hours.
	activeSearchLimit = 2000
)

var errNever = errors.New("never fires")

var locCache sync.Map // name -> *time.Location

func loadLocation(name string) (*time.Location, error) {
	if l, ok := locCache.Load(name); ok {
		return l.(*time.Location), nil
	}
	l, err := time.LoadLocation(name)
	if err != nil || name == "" || strings.EqualFold(name, "local") {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	locCache.Store(name, l)
	return l, nil
}

// cronExpr is a validated 5-field cron expression. fixed reports that neither
// the minute nor the hour field has a wildcard: such a job runs at "a
// particular time" in ISC cron's terms and gets its DST treatment (run once
// when the clocks change if the time was skipped; never twice when it repeats).
// Every other job follows the wall clock.
type cronExpr struct {
	spec  string
	fixed bool
}

func parseCron(spec string) (cronExpr, error) {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return cronExpr{}, fmt.Errorf("cron spec %q needs 5 fields: minute hour day-of-month month day-of-week", spec)
	}
	norm := strings.Join(f, " ")
	if !gronx.IsValid(norm) {
		return cronExpr{}, fmt.Errorf("cron spec %q is not valid", norm)
	}
	return cronExpr{spec: norm, fixed: !strings.ContainsAny(f[0], "*?") && !strings.ContainsAny(f[1], "*?")}, nil
}

// wall re-labels t's local wall-clock reading as UTC. gronx matches fields of
// whatever location it is given, and UTC has no DST, so searching in wall
// space is exact; instants maps the result back.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// instants returns the real instants whose local reading in loc is the wall
// time w: one normally, two (ascending) in a repeated hour, none in a skipped
// one. For a skipped time it also returns the instant the clocks jumped.
func instants(w time.Time, loc *time.Location) (ts []time.Time, jump time.Time) {
	var offsets []int
	for _, d := range []time.Duration{-14 * time.Hour, 0, 14 * time.Hour} {
		if _, off := w.Add(d).In(loc).Zone(); !slices.Contains(offsets, off) {
			offsets = append(offsets, off)
		}
	}
	var after time.Time
	for _, off := range offsets {
		t := w.Add(-time.Duration(off) * time.Second).In(loc)
		switch lw := wall(t); {
		case lw.Equal(w):
			if !slices.ContainsFunc(ts, t.Equal) {
				ts = append(ts, t)
			}
		case lw.After(w):
			after = t
		}
	}
	slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
	if len(ts) == 0 && !after.IsZero() {
		jump, _ = after.ZoneBounds()
	}
	return ts, jump
}

func nearTransition(t time.Time, loc *time.Location) bool {
	start, end := t.In(loc).ZoneBounds()
	return (!start.IsZero() && t.Sub(start) < dstSlack) || (!end.IsZero() && end.Sub(t) < dstSlack)
}

// next returns the first fire time strictly after `after`.
func (c cronExpr) next(after time.Time, loc *time.Location) (time.Time, error) {
	w := wall(after.In(loc))
	if nearTransition(after, loc) {
		// Wall times just before after's reading can still lie ahead in real
		// time when the clocks went back.
		w = w.Add(-dstSlack)
	}
	var best, bestWall time.Time
	for range cronSearchLimit {
		nw, err := gronx.NextTickAfter(c.spec, w, false)
		if err != nil || !nw.After(w) {
			break
		}
		if !best.IsZero() && nw.Sub(bestWall) > dstSlack {
			return best, nil
		}
		w = nw
		ts, jump := instants(nw, loc)
		switch {
		case len(ts) == 0 && c.fixed && !jump.IsZero():
			ts = []time.Time{jump}
		case len(ts) == 2 && c.fixed:
			ts = ts[:1]
		}
		for _, t := range ts {
			if t.After(after) && (best.IsZero() || t.Before(best)) {
				best, bestWall = t, nw
			}
		}
		if !best.IsZero() && !nearTransition(best, loc) {
			return best, nil
		}
	}
	if best.IsZero() {
		return time.Time{}, fmt.Errorf("cron spec %q: %w", c.spec, errNever)
	}
	return best, nil
}

// activeHours is a daily window "HH:MM-HH:MM" in the schedule's zone; the end
// is exclusive, may be 24:00, and a window whose end precedes its start wraps
// past midnight.
type activeHours struct {
	set      bool
	from, to int // minutes since midnight
}

func parseActiveHours(s string) (activeHours, error) {
	if s == "" {
		return activeHours{}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	from, err1 := clockMinutes(a, false)
	to, err2 := clockMinutes(b, true)
	if !ok || err1 != nil || err2 != nil {
		return activeHours{}, fmt.Errorf("active hours %q: want HH:MM-HH:MM", s)
	}
	return activeHours{set: true, from: from, to: to}, nil
}

func clockMinutes(s string, end bool) (int, error) {
	if end && s == "24:00" {
		return 24 * 60, nil
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (a activeHours) contains(t time.Time) bool {
	if !a.set || a.from == a.to%(24*60) {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if a.from < a.to {
		return m >= a.from && m < a.to
	}
	return m >= a.from || m < a.to
}

// nextStart returns the next opening of the window after t (t is outside it).
func (a activeHours) nextStart(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	s := time.Date(l.Year(), l.Month(), l.Day(), a.from/60, a.from%60, 0, 0, loc)
	if !s.After(t) {
		s = time.Date(l.Year(), l.Month(), l.Day()+1, a.from/60, a.from%60, 0, 0, loc)
	}
	return s
}

func (a activeHours) String() string {
	if !a.set {
		return ""
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", a.from/60, a.from%60, a.to/60, a.to%60)
}

func parseInterval(spec string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(spec))
	if err != nil {
		return 0, fmt.Errorf("interval %q: want a duration such as 30m or 2h", spec)
	}
	if d < minInterval || d > maxInterval {
		return 0, fmt.Errorf("interval %q: must be between 1m and 744h", spec)
	}
	return d, nil
}

var onceLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"}

// parseOnce accepts RFC 3339, or a local date-time without offset in loc.
func parseOnce(spec string, loc *time.Location) (time.Time, error) {
	spec = strings.TrimSpace(spec)
	if t, err := time.Parse(time.RFC3339, spec); err == nil {
		return t, nil
	}
	for _, layout := range onceLayouts {
		if t, err := time.ParseInLocation(layout, spec, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("date-time %q: want RFC 3339 (2026-10-12T09:00:00+01:00) or YYYY-MM-DDTHH:MM", spec)
}

// plan is a parsed schedule: everything needed to compute fire times.
type plan struct {
	kind   core.ScheduleKind
	loc    *time.Location
	cron   cronExpr
	every  time.Duration
	once   time.Time
	active activeHours
	spec   string // normalized
}

func parsePlan(kind core.ScheduleKind, spec, tz, active string) (plan, error) {
	loc, err := loadLocation(tz)
	if err != nil {
		return plan{}, err
	}
	p := plan{kind: kind, loc: loc}
	if p.active, err = parseActiveHours(strings.TrimSpace(active)); err != nil {
		return plan{}, err
	}
	switch kind {
	case core.SchedCron:
		if p.cron, err = parseCron(spec); err != nil {
			return plan{}, err
		}
		p.spec = p.cron.spec
	case core.SchedHeartbeat:
		if p.every, err = parseInterval(spec); err != nil {
			return plan{}, err
		}
		p.spec = strings.TrimSpace(spec)
	case core.SchedOnce:
		if p.active.set {
			return plan{}, errors.New("active hours do not apply to a one-off schedule")
		}
		if p.once, err = parseOnce(spec, loc); err != nil {
			return plan{}, err
		}
		p.spec = p.once.In(loc).Format(time.RFC3339)
	default:
		return plan{}, fmt.Errorf("kind must be cron, heartbeat or once, got %q", kind)
	}
	return p, nil
}

// next returns the first fire time after `after`, or zero when a one-off time
// has passed. jitter in [-1, 1) spreads heartbeats by up to ±10%.
func (p plan) next(after time.Time, jitter float64) (time.Time, error) {
	switch p.kind {
	case core.SchedOnce:
		if p.once.After(after) {
			return p.once, nil
		}
		return time.Time{}, nil
	case core.SchedHeartbeat:
		t := after.Add(p.every + time.Duration(float64(p.every)*0.1*jitter))
		if !p.active.contains(t.In(p.loc)) {
			t = p.active.nextStart(t, p.loc)
		}
		return t, nil
	}
	t := after
	for range activeSearchLimit {
		n, err := p.cron.next(t, p.loc)
		if err != nil {
			return time.Time{}, err
		}
		if p.active.contains(n.In(p.loc)) {
			return n, nil
		}
		t = n
	}
	return time.Time{}, fmt.Errorf("cron spec %q never fires within active hours %s: %w", p.spec, p.active, errNever)
}
