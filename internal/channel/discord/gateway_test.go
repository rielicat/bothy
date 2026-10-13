//go:build !race

package discord

import (
	"context"
	"encoding/json"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// These tests need disgo's real gateway, which the race detector cannot run
// (see gateway_double_test.go).

func TestReconnectAndResumeAfterForcedClose(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	dm, user := f.srv.ConformanceIDs()
	f.srv.InjectText(dm, user, "before", false)
	if in := f.sink.message(t); in.Text != "before" {
		t.Fatalf("got %+v", in)
	}

	f.srv.ForceClose(4000)
	f.srv.InjectText(dm, user, "while away", false) // replayed on resume
	if in := f.sink.message(t); in.Text != "while away" {
		t.Fatalf("got %+v, want the event missed while disconnected", in)
	}
	waitFor(t, 5*time.Second, func() bool { return f.a.Status().State == stateConnected }, "connected again")
	if got := f.srv.Resumes(); !slices.Equal(got, []int{2}) {
		t.Fatalf("RESUME seqs = %v, want [2] (READY, then one message)", got)
	}
	if n := f.srv.Identifies(); n != 1 {
		t.Fatalf("%d IDENTIFYs, want 1: the session must be resumed, not replaced", n)
	}
	if got := f.srv.GatewayPaths(); !slices.Equal(got, []string{"/gateway", "/resume"}) {
		t.Fatalf("gateway connections %v, want the resume URL from READY", got)
	}
	f.srv.InjectText(dm, user, "after", false)
	if in := f.sink.message(t); in.Text != "after" {
		t.Fatalf("got %+v", in)
	}
}

func TestDisallowedIntentReportsAuthFailed(t *testing.T) {
	f := newFixture(t)
	f.srv.DisallowedIntents = 1 << 15 // MESSAGE_CONTENT
	f.start(t, stateAuthFailed)
	if st := f.a.Status(); !strings.Contains(st.Detail, "Message Content intent") {
		t.Fatalf("status = %+v, want the privileged-intent hint", st)
	}
}

func TestIdentifyIntentsAndEncoding(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const want = 1<<9 | 1<<12 | 1<<15 // GUILD_MESSAGES | DIRECT_MESSAGES | MESSAGE_CONTENT
	if got := f.srv.Intents(); !slices.Equal(got, []int64{want}) {
		t.Fatalf("IDENTIFY intents = %v, want [%d]", got, want)
	}
	q := f.srv.GatewayQueries()[0]
	if q.Get("v") != "10" || q.Get("encoding") != "json" || q.Has("compress") {
		t.Fatalf("gateway query = %v, want v=10, json, uncompressed", q)
	}
}

func TestGatewayURLFromREST(t *testing.T) {
	f := newFixture(t, func(c *config.Discord, _ *core.AdapterDeps) { c.GatewayURL = "" })
	f.start(t, stateConnected)
	if !slices.Contains(f.srv.Requests(), "GET /api/v10/gateway/bot") {
		t.Fatalf("requests %q, want GET /gateway/bot when gateway_url is empty", f.srv.Requests())
	}
}

func TestResumeAcrossRestart(t *testing.T) {
	f := newFixture(t)
	stop := f.start(t, stateConnected)
	dm, user := f.srv.ConformanceIDs()
	f.srv.InjectText(dm, user, "one", false)
	f.sink.message(t)
	stop()

	raw, err := f.meta.Get(context.Background(), metaResume)
	if err != nil {
		t.Fatalf("no resume cursor stored on shutdown: %v", err)
	}
	var rs resumeState
	if err := json.Unmarshal([]byte(raw), &rs); err != nil || rs.SessionID == "" || rs.Seq != 2 || !strings.HasSuffix(rs.URL, "/resume") {
		t.Fatalf("stored cursor %s, want session, seq 2 and the resume URL", raw)
	}
	f.srv.InjectText(dm, user, "missed", false)

	// A new adapter on the same Meta stands in for a restarted process.
	a, err := newAdapter(f.a.cfg, f.a.deps)
	if err != nil {
		t.Fatal(err)
	}
	f.a = a
	f.start(t, stateConnected)
	if in := f.sink.message(t); in.Text != "missed" {
		t.Fatalf("got %+v, want the event sent while stopped", in)
	}
	if got := f.srv.Resumes(); !slices.Equal(got, []int{2}) || f.srv.Identifies() != 1 {
		t.Fatalf("resumes %v, identifies %d; want the restart to resume the stored session", got, f.srv.Identifies())
	}
	if _, err := f.meta.Get(context.Background(), metaResume); err == nil {
		t.Fatal("the stored cursor was not consumed")
	}
}

// idleBudget is the SPEC §12.2 ceiling for one connected, idle adapter.
const idleBudget = 3 << 20

func TestIdleFootprint(t *testing.T) {
	const n = 4 // several adapters, so leftover garbage from earlier tests does not swamp the signal
	fixtures := make([]*fixture, n)
	for i := range fixtures {
		fixtures[i] = newFixture(t)
	}
	before := settledMem()
	for _, f := range fixtures {
		f.start(t, stateConnected)
	}
	time.Sleep(200 * time.Millisecond)
	// The fakes' side of each connection is counted too, so this overstates
	// the adapter's own share.
	per := (int64(settledMem()) - int64(before)) / n
	if per > idleBudget {
		t.Fatalf("each idle adapter grew the heap and stacks by %d bytes, budget %d", per, idleBudget)
	}
	t.Logf("idle footprint per adapter: %d KiB", per/1024)
}

func settledMem() uint64 {
	var ms runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc + ms.StackInuse
}
