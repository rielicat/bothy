package httpapi

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
)

type countingBus struct {
	*events.Bus
	since atomic.Int32
}

func (b *countingBus) Since(after uint64, f core.Filter) ([]core.Event, bool) {
	b.since.Add(1)
	return b.Bus.Since(after, f)
}

// A fresh stream needs only where the bus stands for its hello: copying the
// matching part of the 4096-event ring under the bus lock, which stalls
// every Publish, just to read the last seq is waste.
func TestSSEHelloForFreshClientDoesNotCopyTheRing(t *testing.T) {
	h := newHarness(t).setup()
	mp := core.UserPrincipal(h.member.user)
	for range 50 {
		h.bus.Publish(delta(mp, "x"))
	}
	last := h.bus.Publish(delta(core.UserPrincipal(h.owner.user), "not yours"))
	cb := &countingBus{Bus: h.bus}
	h.srv.d.Bus = cb
	c := h.openSSE(t, h.server(t), h.member, "")
	f := c.next(t)
	var hello struct {
		Seq uint64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(f.data), &hello); f.event != "hello" || err != nil {
		t.Fatalf("hello = %+v (%v)", f, err)
	}
	if n := cb.since.Load(); n != 0 {
		t.Fatalf("Since called %d times for a fresh client", n)
	}
	if hello.Seq != last.Seq {
		t.Fatalf("hello seq = %d, want the bus head %d", hello.Seq, last.Seq)
	}
	h.bus.Publish(delta(mp, "live"))
	if f := c.next(t); f.event != "run.delta" {
		t.Fatalf("live = %+v", f)
	}
}
