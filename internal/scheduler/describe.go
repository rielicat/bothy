package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// describe renders a parsed plan in plain English, e.g. "Weekdays at 08:00
// (Europe/London)". Cron shapes it does not recognise fall back to the spec.
func describe(p plan) string {
	var s string
	switch p.kind {
	case core.SchedOnce:
		s = "Once, on " + p.once.In(p.loc).Format("Mon 2 Jan 2006 at 15:04")
	case core.SchedHeartbeat:
		s = "Every " + humanDuration(p.every) + " (±10%)"
	default:
		s = describeCron(p.cron.spec)
	}
	if p.active.set {
		s += fmt.Sprintf(", between %02d:%02d and %02d:%02d", p.active.from/60, p.active.from%60, p.active.to/60, p.active.to%60)
	}
	return s + " (" + p.loc.String() + ")"
}

func humanDuration(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	case d%time.Minute == 0:
		return plural(int(d/time.Minute), "minute")
	}
	return d.String()
}

func plural(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func describeCron(spec string) string {
	f := strings.Fields(spec)
	minute, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]
	fallback := "Cron " + spec
	if minute == "*" && hour == "*" && dom == "*" && mon == "*" && dow == "*" {
		return "Every minute"
	}
	if n, ok := stepOf(minute); ok && hour == "*" && dom == "*" && mon == "*" && dow == "*" {
		return "Every " + plural(n, "minute")
	}
	m, okMin := atoi(minute, 59)
	if !okMin {
		return fallback
	}
	if hour == "*" && dom == "*" && mon == "*" && dow == "*" {
		return fmt.Sprintf("Every hour at :%02d", m)
	}
	if n, ok := stepOf(hour); ok && dom == "*" && mon == "*" && dow == "*" {
		return fmt.Sprintf("Every %s at :%02d", plural(n, "hour"), m)
	}
	var times []string
	for _, h := range strings.Split(hour, ",") {
		hv, ok := atoi(h, 23)
		if !ok {
			return fallback
		}
		times = append(times, fmt.Sprintf("%02d:%02d", hv, m))
	}
	at := " at " + joinAnd(times)
	switch {
	case dom == "*" && mon == "*":
		days, ok := describeDays(dow)
		if !ok {
			return fallback
		}
		return days + at
	case dow == "*" && mon == "*":
		d, ok := atoi(dom, 31)
		if !ok {
			return fallback
		}
		return "Monthly on day " + strconv.Itoa(d) + at
	case dow == "*":
		d, okD := atoi(dom, 31)
		mo, okM := atoi(mon, 12)
		if !okD || !okM || mo == 0 {
			return fallback
		}
		return fmt.Sprintf("Every year on %d %s%s", d, time.Month(mo), at)
	}
	return fallback
}

func describeDays(dow string) (string, bool) {
	switch dow {
	case "*":
		return "Every day", true
	case "1-5", "MON-FRI", "mon-fri":
		return "Weekdays", true
	case "0,6", "6,0", "6,7", "SAT,SUN", "sat,sun":
		return "Weekends", true
	}
	var names []string
	for _, d := range strings.Split(dow, ",") {
		v, ok := atoi(d, 7)
		if !ok {
			return "", false
		}
		names = append(names, weekdayNames[v%7]+"s")
	}
	return joinAnd(names), true
}

func stepOf(s string) (int, bool) {
	rest, ok := strings.CutPrefix(s, "*/")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

func atoi(s string, maxV int) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && n <= maxV
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
