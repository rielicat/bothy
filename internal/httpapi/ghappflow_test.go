package httpapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// Flows are single use per step, expire, and a flood of starts can neither
// grow memory nor push out other people's flows.
func TestGHAppFlows(t *testing.T) {
	clock := fakes.NewClock(time.Now())
	fs := newGHAppFlows(clock)
	fs.put("b1", &ghAppFlow{user: "usr_a", stateHash: sha32("s1"), phase: ghStarted})
	if _, ok := fs.advance("b1", ghStarted, ghExchanging, "s2", nil); ok {
		t.Fatal("advanced with the wrong state")
	}
	if _, ok := fs.advance("b2", ghStarted, ghExchanging, "s1", nil); ok {
		t.Fatal("advanced another binding's flow")
	}
	if f, ok := fs.advance("b1", ghStarted, ghExchanging, "s1", func(f *ghAppFlow) { f.app = "gha_x" }); !ok || f.app != "gha_x" || f.phase != ghExchanging {
		t.Fatalf("advance = %+v %v", f, ok)
	}
	if _, ok := fs.advance("b1", ghStarted, ghExchanging, "s1", nil); ok {
		t.Fatal("a step ran twice")
	}
	clock.Advance(ghFlowTTL)
	if _, ok := fs.get("b1"); ok {
		t.Fatal("an expired flow is still there")
	}

	for i := range maxGHFlowsPerUser + 2 {
		fs.put(fmt.Sprintf("a%d", i), &ghAppFlow{user: "usr_a"})
		clock.Advance(time.Second)
	}
	if _, ok := fs.get("a0"); ok {
		t.Error("a user kept more than their share of flows")
	}
	if _, ok := fs.get(fmt.Sprintf("a%d", maxGHFlowsPerUser+1)); !ok {
		t.Error("the newest flow is missing")
	}
	for i := range maxGHFlows + 5 {
		fs.put(fmt.Sprintf("u%d", i), &ghAppFlow{user: core.UserID(fmt.Sprintf("usr_%d", i))})
	}
	fs.mu.Lock()
	n := len(fs.m)
	fs.mu.Unlock()
	if n > maxGHFlows {
		t.Errorf("%d flows kept, at most %d", n, maxGHFlows)
	}
}
