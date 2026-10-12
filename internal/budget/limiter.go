package budget

import (
	"container/list"
	"math"
	"sync"
	"time"
)

const senderKeys = 10_000

// senderLimiter is a token bucket per sender key with LRU eviction, so a
// flood of distinct senders cannot grow memory without bound. An evicted
// sender comes back with a full bucket, which is the only effect of eviction.
type senderLimiter struct {
	mu   sync.Mutex
	max  int
	lru  *list.List // front = most recently used *bucket
	keys map[string]*list.Element
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
}

func newSenderLimiter(maxKeys int) *senderLimiter {
	return &senderLimiter{max: maxKeys, lru: list.New(), keys: map[string]*list.Element{}}
}

// allow takes one token for key. perMinute <= 0 disables the limit.
func (l *senderLimiter) allow(now time.Time, key string, perMinute, burst int) (bool, time.Duration) {
	if perMinute <= 0 {
		return true, 0
	}
	burst = max(burst, 1)
	rate := float64(perMinute) / 60 // tokens per second
	l.mu.Lock()
	defer l.mu.Unlock()
	var b *bucket
	if el, ok := l.keys[key]; ok {
		l.lru.MoveToFront(el)
		b = el.Value.(*bucket)
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens = math.Min(float64(burst), b.tokens+elapsed*rate)
		}
		b.tokens = math.Min(b.tokens, float64(burst))
	} else {
		b = &bucket{key: key, tokens: float64(burst)}
		l.keys[key] = l.lru.PushFront(b)
		for l.lru.Len() > l.max {
			old := l.lru.Back()
			l.lru.Remove(old)
			delete(l.keys, old.Value.(*bucket).key)
		}
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration(math.Ceil((1 - b.tokens) / rate * float64(time.Second)))
	return false, wait
}
