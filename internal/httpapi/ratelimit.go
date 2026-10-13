package httpapi

import (
	"math"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

// limiter is a set of token buckets keyed by token id or client address.
type limiter struct {
	clock core.Clock
	mu    sync.Mutex
	m     map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

const maxBuckets = 10000

func newLimiter(c core.Clock) *limiter { return &limiter{clock: c, m: map[string]*bucket{}} }

// allow takes one token from key's bucket (perMin refill, burst capacity),
// or reports how long until one is available.
func (l *limiter) allow(key string, perMin, burst int) (bool, time.Duration) {
	if perMin <= 0 {
		return true, 0
	}
	burst = max(burst, 1)
	rate := float64(perMin) / 60
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.m[key]
	if !ok {
		if len(l.m) >= maxBuckets {
			l.pruneLocked(now, rate, burst)
		}
		b = &bucket{tokens: float64(burst), last: now}
		l.m[key] = b
	}
	b.tokens = math.Min(float64(burst), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / rate * float64(time.Second))
}

// pruneLocked drops buckets that have refilled completely; they carry no
// state worth keeping.
func (l *limiter) pruneLocked(now time.Time, rate float64, burst int) {
	for k, b := range l.m {
		if b.tokens+now.Sub(b.last).Seconds()*rate >= float64(burst) {
			delete(l.m, k)
		}
	}
	for k := range l.m {
		if len(l.m) < maxBuckets {
			return
		}
		delete(l.m, k)
	}
}
