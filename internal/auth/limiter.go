package auth

import (
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/config"
)

// maxTracked bounds the in-memory failure and attempt maps so a flood of
// distinct usernames or addresses cannot grow them without limit.
const maxTracked = 10000

// lockouts tracks failed logins per key ("ip:<addr>", "name:<username>")
// with exponential lockout: max_failures failures lock for base, each further
// failure doubles it, capped at max. Known users are also tracked in the
// users table (failed_logins, locked_until) so lockouts survive restarts.
type lockouts struct {
	mu sync.Mutex
	m  map[string]*failState
}

type failState struct {
	n     int
	last  time.Time
	until time.Time
}

func newLockouts() *lockouts { return &lockouts{m: map[string]*failState{}} }

func lockFor(n int, l config.Lockout) time.Duration {
	maxF := l.MaxFailures
	if maxF <= 0 {
		maxF = 5
	}
	if n < maxF {
		return 0
	}
	base, ceiling := l.Base.D(), l.Max.D()
	if base <= 0 {
		base = time.Minute
	}
	if ceiling < base {
		ceiling = base
	}
	d := base
	for i := maxF; i < n && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}

// locked returns the remaining lockout for key.
func (l *lockouts) locked(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, ok := l.m[key]; ok && now.Before(st.until) {
		return st.until.Sub(now)
	}
	return 0
}

// fail records a failure and returns the lockout it starts (0 for none).
func (l *lockouts) fail(key string, now time.Time, cfg config.Lockout) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.m[key]
	if !ok {
		l.pruneLocked(now)
		st = &failState{}
		l.m[key] = st
	}
	// A quiet hour forgives earlier failures that never reached a lockout.
	if now.Sub(st.last) > max(cfg.Max.D(), time.Hour) && !now.Before(st.until) {
		st.n = 0
	}
	st.n++
	st.last = now
	d := lockFor(st.n, cfg)
	if d > 0 {
		st.until = now.Add(d)
	}
	return d
}

func (l *lockouts) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

func (l *lockouts) pruneLocked(now time.Time) {
	if len(l.m) < maxTracked {
		return
	}
	for k, st := range l.m {
		if !now.Before(st.until) && now.Sub(st.last) > time.Hour {
			delete(l.m, k)
		}
	}
	for k := range l.m {
		if len(l.m) < maxTracked {
			break
		}
		delete(l.m, k)
	}
}

// attempts is a sliding-window counter (pairing redemptions per identity).
type attempts struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	m      map[string][]time.Time
}

func newAttempts(limit int, window time.Duration) *attempts {
	return &attempts{limit: limit, window: window, m: map[string][]time.Time{}}
}

// take records an attempt for key, or returns how long until one is allowed.
func (a *attempts) take(key string, now time.Time) (retry time.Duration, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ts := a.m[key][:0]
	for _, t := range a.m[key] {
		if now.Sub(t) < a.window {
			ts = append(ts, t)
		}
	}
	if len(ts) >= a.limit {
		a.m[key] = ts
		return ts[0].Add(a.window).Sub(now), false
	}
	if len(ts) == 0 && len(a.m) >= maxTracked {
		for k, v := range a.m {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= a.window {
				delete(a.m, k)
			}
		}
	}
	a.m[key] = append(ts, now)
	return 0, true
}
