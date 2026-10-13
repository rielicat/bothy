package events

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func delta(p core.PrincipalID, s core.SessionID, text string) core.Event {
	return core.Event{Type: core.EvRunDelta, Principal: p, Session: s, Data: core.DeltaData{Text: text}}
}

func recv(t *testing.T, sub core.Subscription) core.Event {
	t.Helper()
	select {
	case e := <-sub.C():
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered")
		return core.Event{}
	}
}

func assertEmpty(t *testing.T, sub core.Subscription) {
	t.Helper()
	select {
	case e := <-sub.C():
		t.Fatalf("unexpected event %+v", e)
	default:
	}
}

func TestPublishAssignsSeqAndAt(t *testing.T) {
	b := New(Options{Clock: fixedClock{t0}})
	for i := uint64(1); i <= 3; i++ {
		e := b.Publish(core.Event{Type: core.EvRunDelta, Seq: 99, At: time.Unix(1, 0), Data: core.DeltaData{}})
		if e.Seq != i || !e.At.Equal(t0) {
			t.Fatalf("publish %d: seq=%d at=%v", i, e.Seq, e.At)
		}
	}
}

func TestFilterMatching(t *testing.T) {
	tests := []struct {
		name string
		f    core.Filter
		e    core.Event
		want bool
	}{
		{"own principal", core.Filter{Principals: []core.PrincipalID{"usr_a"}}, delta("usr_a", "", ""), true},
		{"owned space", core.Filter{Principals: []core.PrincipalID{"usr_a", "spc_g"}}, delta("spc_g", "", ""), true},
		{"foreign principal", core.Filter{Principals: []core.PrincipalID{"usr_a"}}, delta("usr_b", "", ""), false},
		{"admin broadcast to member", core.Filter{Principals: []core.PrincipalID{"usr_a"}}, delta("", "", ""), false},
		{"admin broadcast to admin", core.Filter{Principals: []core.PrincipalID{"usr_a"}, Admin: true}, delta("", "", ""), true},
		{"admin never sees other principals", core.Filter{Principals: []core.PrincipalID{"usr_a"}, Admin: true}, delta("usr_b", "", ""), false},
		{"session narrowing hit", core.Filter{Principals: []core.PrincipalID{"usr_a"}, Session: "ses_1"}, delta("usr_a", "ses_1", ""), true},
		{"session narrowing miss", core.Filter{Principals: []core.PrincipalID{"usr_a"}, Session: "ses_1"}, delta("usr_a", "ses_2", ""), false},
		{"type narrowing hit", core.Filter{Principals: []core.PrincipalID{"usr_a"}, Types: []core.EventType{core.EvRunDelta}}, delta("usr_a", "", ""), true},
		{"type narrowing miss", core.Filter{Principals: []core.PrincipalID{"usr_a"}, Types: []core.EventType{core.EvBanner}}, delta("usr_a", "", ""), false},
		{"system sees everything", core.Filter{System: true}, delta("usr_b", "ses_9", ""), true},
		{"system sees broadcasts", core.Filter{System: true}, delta("", "", ""), true},
		{"system still narrows by type", core.Filter{System: true, Types: []core.EventType{core.EvBanner}}, delta("usr_b", "", ""), false},
		{"zero filter", core.Filter{}, delta("usr_a", "", ""), false},
	}
	for _, tt := range tests {
		if got := matches(tt.f, tt.e); got != tt.want {
			t.Errorf("%s: matches = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSubscribeDeliversMatchingInOrder(t *testing.T) {
	b := New(Options{})
	principals := []core.PrincipalID{"usr_a"}
	sub := b.Subscribe(core.Filter{Principals: principals}, 16)
	defer sub.Close()
	principals[0] = "usr_b" // the bus must not alias the caller's slice
	b.Publish(delta("usr_a", "", "1"))
	b.Publish(delta("usr_b", "", "x"))
	b.Publish(delta("", "", "broadcast"))
	b.Publish(delta("usr_a", "", "2"))
	for _, want := range []string{"1", "2"} {
		if got := recv(t, sub).Data.(core.DeltaData).Text; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	assertEmpty(t, sub)
	if sub.Dropped() != 0 {
		t.Fatal("dropped events without overflow")
	}
}

func TestOverflowStallsUntilDroppedRead(t *testing.T) {
	b := New(Options{})
	sub := b.Subscribe(core.Filter{System: true}, 2)
	defer sub.Close()
	done := make(chan struct{})
	go func() {
		for range 5 {
			b.Publish(delta("usr_a", "", ""))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	if e := recv(t, sub); e.Seq != 1 {
		t.Fatalf("first seq = %d", e.Seq)
	}
	if e := recv(t, sub); e.Seq != 2 {
		t.Fatalf("second seq = %d", e.Seq)
	}
	b.Publish(delta("usr_a", "", "")) // seq 6: still stalled, buffer has room but no gap may be hidden
	assertEmpty(t, sub)
	if n := sub.Dropped(); n != 4 {
		t.Fatalf("Dropped = %d, want 4", n)
	}
	if n := sub.Dropped(); n != 0 {
		t.Fatalf("Dropped after read = %d, want 0", n)
	}
	b.Publish(delta("usr_a", "", ""))
	if e := recv(t, sub); e.Seq != 7 {
		t.Fatalf("delivery did not resume: seq %d", e.Seq)
	}
}

func TestPublishNeverBlocksWithIdleSubscribers(t *testing.T) {
	b := New(Options{Ring: 64})
	var subs []core.Subscription
	for range 8 {
		subs = append(subs, b.Subscribe(core.Filter{System: true}, 1))
	}
	done := make(chan struct{})
	go func() {
		for range 20000 {
			b.Publish(delta("usr_a", "", ""))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish blocked")
	}
	for _, s := range subs {
		if n := s.Dropped(); n != 19999 {
			t.Fatalf("Dropped = %d, want 19999", n)
		}
		s.Close()
	}
}

func TestSince(t *testing.T) {
	b := New(Options{Ring: 8})
	for i := range 20 {
		p := core.PrincipalID("usr_a")
		if i%2 == 1 {
			p = "usr_b"
		}
		b.Publish(delta(p, "", ""))
	}
	all := core.Filter{System: true}
	seqs := func(evs []core.Event) []uint64 {
		var out []uint64
		for _, e := range evs {
			out = append(out, e.Seq)
		}
		return out
	}
	tests := []struct {
		name     string
		after    uint64
		f        core.Filter
		want     []uint64
		complete bool
	}{
		{"gap outside ring", 5, all, []uint64{13, 14, 15, 16, 17, 18, 19, 20}, false},
		{"from zero after eviction", 0, all, []uint64{13, 14, 15, 16, 17, 18, 19, 20}, false},
		{"exactly at ring start", 12, all, []uint64{13, 14, 15, 16, 17, 18, 19, 20}, true},
		{"inside ring", 17, all, []uint64{18, 19, 20}, true},
		{"caught up", 20, all, nil, true},
		{"future cursor", 25, all, nil, false},
		{"filtered", 14, core.Filter{Principals: []core.PrincipalID{"usr_a"}}, []uint64{15, 17, 19}, true},
	}
	for _, tt := range tests {
		evs, complete := b.Since(tt.after, tt.f)
		if complete != tt.complete || !slices.Equal(seqs(evs), tt.want) {
			t.Errorf("%s: Since(%d) = %v, %v; want %v, %v", tt.name, tt.after, seqs(evs), complete, tt.want, tt.complete)
		}
	}
	empty := New(Options{})
	if evs, complete := empty.Since(0, all); len(evs) != 0 || !complete {
		t.Errorf("empty bus: Since(0) = %v, %v", evs, complete)
	}
}

func TestDefaultRingSize(t *testing.T) {
	b := New(Options{})
	for range defaultRing + 1 {
		b.Publish(delta("usr_a", "", ""))
	}
	if _, complete := b.Since(0, core.Filter{System: true}); complete {
		t.Fatal("seq 1 should have been evicted")
	}
	evs, complete := b.Since(1, core.Filter{System: true})
	if !complete || len(evs) != defaultRing || evs[0].Seq != 2 {
		t.Fatalf("Since(1): %d events, complete=%v", len(evs), complete)
	}
}

func TestBanners(t *testing.T) {
	b := New(Options{})
	banner := func(p core.PrincipalID, code, text string, clear bool) core.Event {
		return core.Event{Type: core.EvBanner, Principal: p, Data: core.BannerData{Level: "warn", Code: code, Text: text, Clear: clear}}
	}
	b.Publish(banner("", "no_sandbox", "v1", false))
	b.Publish(banner("usr_a", "budget_80", "a", false))
	b.Publish(banner("usr_b", "budget_80", "b", false))
	b.Publish(banner("", "no_sandbox", "v2", false))
	b.Publish(core.Event{Type: core.EvBanner, Principal: "usr_a", Data: &core.BannerData{Code: "paused", Text: "p"}})
	b.Publish(banner("", "config_error", "", true)) // clearing an absent banner is a no-op

	texts := func(evs []core.Event) []string {
		var out []string
		for _, e := range evs {
			switch d := e.Data.(type) {
			case core.BannerData:
				out = append(out, d.Text)
			case *core.BannerData:
				out = append(out, d.Text)
			}
		}
		return out
	}
	admin := core.Filter{Principals: []core.PrincipalID{"usr_a"}, Admin: true}
	if got := texts(b.Banners(admin)); !slices.Equal(got, []string{"a", "v2", "p"}) {
		t.Fatalf("admin banners = %v", got)
	}
	if got := texts(b.Banners(core.Filter{Principals: []core.PrincipalID{"usr_b"}})); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("member banners = %v", got)
	}
	b.Publish(banner("", "no_sandbox", "", true))
	b.Publish(banner("usr_a", "budget_80", "", true))
	if got := texts(b.Banners(core.Filter{System: true})); !slices.Equal(got, []string{"b", "p"}) {
		t.Fatalf("after clear = %v", got)
	}
}

func TestBootID(t *testing.T) {
	a, b := New(Options{}).BootID(), New(Options{}).BootID()
	if a == "" || a == b || strings.Contains(a, "-") {
		t.Fatalf("boot ids %q %q", a, b)
	}
}

func TestClose(t *testing.T) {
	b := New(Options{})
	sub := b.Subscribe(core.Filter{System: true}, 4)
	b.Publish(delta("usr_a", "", ""))
	sub.Close()
	sub.Close()
	b.Publish(delta("usr_a", "", ""))
	var n int
	for range sub.C() {
		n++
	}
	if n != 1 {
		t.Fatalf("received %d events, want the 1 buffered before Close", n)
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	const publishers, perPublisher, subscribers = 8, 500, 4
	b := New(Options{Ring: 128})
	var readers sync.WaitGroup
	results := make([][]uint64, subscribers)
	subs := make([]core.Subscription, subscribers)
	for i := range subscribers {
		subs[i] = b.Subscribe(core.Filter{System: true}, publishers*perPublisher)
		readers.Add(1)
		go func() {
			defer readers.Done()
			for e := range subs[i].C() {
				results[i] = append(results[i], e.Seq)
			}
		}()
	}
	stop := make(chan struct{})
	var noise sync.WaitGroup
	noise.Add(1)
	go func() {
		defer noise.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s := b.Subscribe(core.Filter{Principals: []core.PrincipalID{"usr_a"}}, 1)
			b.Since(0, core.Filter{System: true})
			b.Banners(core.Filter{Admin: true})
			s.Close()
		}
	}()
	var pubs sync.WaitGroup
	for p := range publishers {
		pubs.Add(1)
		go func() {
			defer pubs.Done()
			for i := range perPublisher {
				if i%50 == 0 {
					b.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Code: "c", Clear: p%2 == 0}})
					continue
				}
				b.Publish(delta("usr_a", "", ""))
			}
		}()
	}
	pubs.Wait()
	close(stop)
	noise.Wait()
	for _, s := range subs {
		if s.Dropped() != 0 {
			t.Fatal("dropped with a buffer large enough for every event")
		}
		s.Close()
	}
	readers.Wait()
	for i, seqs := range results {
		if len(seqs) != publishers*perPublisher {
			t.Fatalf("subscriber %d got %d events", i, len(seqs))
		}
		for j := 1; j < len(seqs); j++ {
			if seqs[j] != seqs[j-1]+1 {
				t.Fatalf("subscriber %d: seq %d after %d", i, seqs[j], seqs[j-1])
			}
		}
	}
}
