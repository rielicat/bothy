package auth

import (
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
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
