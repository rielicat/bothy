package events

import (
	"slices"
	"sync/atomic"

	"github.com/bothyhq/bothy/internal/core"
)

var _ core.Bus = (*Bus)(nil)

// Publish assigns Seq and At, records the event in the ring and offers it to
// every matching subscriber without blocking. Fan-out happens under the bus
// lock so each subscriber sees events in Seq order.
func (b *Bus) Publish(e core.Event) core.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Seq = b.seq
	e.At = b.clock.Now()
	b.ring[b.head] = e
	b.head = (b.head + 1) % len(b.ring)
	if b.count < len(b.ring) {
		b.count++
	}
	if e.Type == core.EvBanner {
		b.trackBanner(e)
	}
	for s := range b.subs {
		if matches(s.f, e) {
			s.offer(e)
		}
	}
	return e
}

// Subscribe registers a subscriber with a buffered channel of buf events
// (default 256).
func (b *Bus) Subscribe(f core.Filter, buf int) core.Subscription {
	if buf <= 0 {
		buf = defaultSubBuffer
	}
	s := &subscription{bus: b, f: cloneFilter(f), ch: make(chan core.Event, buf)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Since returns the ring events with Seq > after that match f. complete is
// false when events after `after` were already evicted from the ring, or when
// after is beyond the newest Seq (a cursor from another boot).
func (b *Bus) Since(after uint64, f core.Filter) ([]core.Event, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if after > b.seq {
		return nil, false
	}
	oldest := b.seq - uint64(b.count) + 1
	complete := after+1 >= oldest
	skip := 0
	if complete {
		skip = int(after + 1 - oldest)
	}
	var evs []core.Event
	n := len(b.ring)
	for i := skip; i < b.count; i++ {
		e := b.ring[(b.head-b.count+i+n)%n]
		if matches(f, e) {
			evs = append(evs, e)
		}
	}
	return evs, complete
}

func (b *Bus) BootID() string { return b.boot }

// Banners returns the active banners matching f in Seq order.
func (b *Bus) Banners(f core.Filter) []core.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var evs []core.Event
	for _, e := range b.banners {
		if matches(f, e) {
			evs = append(evs, e)
		}
	}
	slices.SortFunc(evs, func(x, y core.Event) int {
		switch {
		case x.Seq < y.Seq:
			return -1
		case x.Seq > y.Seq:
			return 1
		}
		return 0
	})
	return evs
}

func (b *Bus) trackBanner(e core.Event) {
	var d core.BannerData
	switch x := e.Data.(type) {
	case core.BannerData:
		d = x
	case *core.BannerData:
		if x == nil {
			return
		}
		d = *x
	default:
		return
	}
	k := bannerKey{e.Principal, d.Code}
	if d.Clear {
		delete(b.banners, k)
		return
	}
	b.banners[k] = e
}

// matches applies the principal scope (bypassed by System) and then the
// optional Session and Types narrowing, which also apply to System filters.
func matches(f core.Filter, e core.Event) bool {
	if !f.System {
		if e.Principal == "" {
			if !f.Admin {
				return false
			}
		} else if !slices.Contains(f.Principals, e.Principal) {
			return false
		}
	}
	if f.Session != "" && e.Session != f.Session {
		return false
	}
	if len(f.Types) > 0 && !slices.Contains(f.Types, e.Type) {
		return false
	}
	return true
}

// cloneFilter keeps a subscriber's filter immune to later mutation of the
// caller's slices, which Publish reads under the bus lock.
func cloneFilter(f core.Filter) core.Filter {
	f.Principals = slices.Clone(f.Principals)
	f.Types = slices.Clone(f.Types)
	return f
}

type subscription struct {
	bus     *Bus
	f       core.Filter
	ch      chan core.Event
	dropped atomic.Uint64
	closed  bool // guarded by bus.mu
}

// offer runs under bus.mu. Once an event is dropped the subscriber stays
// stalled (every further event counts as dropped) until it reads Dropped, so
// it never sees a stream with a silent gap in it.
func (s *subscription) offer(e core.Event) {
	if s.dropped.Load() > 0 {
		s.dropped.Add(1)
		return
	}
	select {
	case s.ch <- e:
	default:
		s.dropped.Add(1)
	}
}

func (s *subscription) C() <-chan core.Event { return s.ch }

// Dropped returns the number of events dropped since the previous call and
// resumes delivery; a non-zero result means the consumer must resync.
func (s *subscription) Dropped() uint64 { return s.dropped.Swap(0) }

func (s *subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	delete(s.bus.subs, s)
	close(s.ch)
}
