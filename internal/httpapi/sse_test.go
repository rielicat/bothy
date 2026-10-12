package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

type sseFrame struct {
	id, event, data string
}

type sseConn struct {
	frames chan sseFrame
	resp   *http.Response
	cancel context.CancelFunc
}

func (h *harness) openSSE(t *testing.T, srv *httptest.Server, c *client, lastID string) *sseConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else {
		req.AddCookie(&http.Cookie{Name: cookiePlain, Value: c.cookie})
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("SSE status %d", resp.StatusCode)
	}
	sc := &sseConn{frames: make(chan sseFrame, 1024), resp: resp, cancel: cancel}
	go func() {
		defer close(sc.frames)
		br := bufio.NewReader(resp.Body)
		var f sseFrame
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case line == "":
				if f.event != "" {
					sc.frames <- f
				}
				f = sseFrame{}
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			}
		}
	}()
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return sc
}

// server starts a real HTTP server; streams end before it closes.
func (h *harness) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h.h)
	t.Cleanup(func() {
		h.srv.stopStreams()
		srv.Close()
	})
	return srv
}

func (sc *sseConn) next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case f, ok := <-sc.frames:
		if !ok {
			t.Fatal("stream closed")
		}
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an SSE frame")
	}
	return sseFrame{}
}

// quiet asserts that no frame other than pings arrives for a short while.
func (sc *sseConn) quiet(t *testing.T) {
	t.Helper()
	select {
	case f, ok := <-sc.frames:
		if ok {
			t.Fatalf("unexpected frame %+v", f)
		}
	case <-time.After(150 * time.Millisecond):
	}
}

func (sc *sseConn) closed(t *testing.T) bool {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-sc.frames:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func delta(p core.PrincipalID, text string) core.Event {
	return core.Event{Type: core.EvRunDelta, Principal: p, Session: "ses_x", Run: "run_x", Data: core.DeltaData{Text: text}}
}

func TestSSEHelloAndACL(t *testing.T) {
	h := newHarness(t).setup()
	srv := h.server(t)
	h.bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: "error", Code: "no_sandbox", Text: "No sandbox"}})
	op, mp := core.UserPrincipal(h.owner.user), core.UserPrincipal(h.member.user)
	h.bus.Publish(core.Event{Type: core.EvBanner, Principal: mp, Data: core.BannerData{Level: "warn", Code: "budget_80", Text: "80%"}})

	owner := h.openSSE(t, srv, h.owner, "")
	member := h.openSSE(t, srv, h.member, "")
	admin := h.openSSE(t, srv, h.tokenFor(h.admin, "read"), "")
	for name, c := range map[string]*sseConn{"owner": owner, "member": member, "admin": admin} {
		f := c.next(t)
		if f.event != "hello" {
			t.Fatalf("%s first frame = %+v", name, f)
		}
		var hello struct {
			Boot    string            `json:"boot"`
			Banners []core.BannerData `json:"banners"`
		}
		if err := json.Unmarshal([]byte(f.data), &hello); err != nil || hello.Boot != h.bus.BootID() {
			t.Fatalf("%s hello = %s (%v)", name, f.data, err)
		}
		codes := []string{}
		for _, b := range hello.Banners {
			codes = append(codes, b.Code)
		}
		want := map[string]string{"owner": "no_sandbox", "admin": "no_sandbox", "member": "budget_80"}[name]
		if strings.Join(codes, ",") != want {
			t.Fatalf("%s banners = %v, want %s", name, codes, want)
		}
	}
	if owner.resp.Header.Get("Content-Type") != "text/event-stream" || owner.resp.Header.Get("X-Accel-Buffering") != "no" ||
		owner.resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", owner.resp.Header)
	}

	h.bus.Publish(delta(mp, "member secret"))
	if f := member.next(t); f.event != "run.delta" || !strings.Contains(f.data, "member secret") {
		t.Fatalf("member frame = %+v", f)
	}
	h.bus.Publish(delta(op, "owner secret"))
	if f := owner.next(t); f.event != "run.delta" || !strings.Contains(f.data, "owner secret") {
		t.Fatalf("owner frame = %+v", f)
	}
	h.bus.Publish(core.Event{Type: core.EvChannelStatus, Data: core.ChannelStatusData{Status: core.ChannelStatus{Name: "telegram", State: "connected"}}})
	if f := owner.next(t); f.event != "channel.status" {
		t.Fatalf("owner broadcast = %+v", f)
	}
	// The admin gets admin broadcasts but never another principal's deltas,
	// and the member gets neither the owner's deltas nor broadcasts.
	if f := admin.next(t); f.event != "channel.status" {
		t.Fatalf("admin first frame after hello = %+v (must not be a run.delta)", f)
	}
	member.quiet(t)
	admin.quiet(t)
}

func TestSSEResumeWithoutGaps(t *testing.T) {
	h := newHarness(t).setup()
	srv := h.server(t)
	mp := core.UserPrincipal(h.member.user)
	c := h.openSSE(t, srv, h.member, "")
	c.next(t) // hello
	var lastID string
	for i := 0; i < 3; i++ {
		h.bus.Publish(delta(mp, "a"+strconv.Itoa(i)))
		lastID = c.next(t).id
	}
	c.cancel()
	for i := 0; i < 5; i++ {
		h.bus.Publish(delta(mp, "b"+strconv.Itoa(i)))
	}
	h.bus.Publish(delta(core.UserPrincipal(h.owner.user), "not yours"))
	c2 := h.openSSE(t, srv, h.member, lastID)
	if f := c2.next(t); f.event != "hello" {
		t.Fatalf("first = %+v", f)
	}
	prev := lastID
	for i := 0; i < 5; i++ {
		f := c2.next(t)
		if f.event != "run.delta" || !strings.Contains(f.data, `"b`+strconv.Itoa(i)+`"`) {
			t.Fatalf("replay %d = %+v", i, f)
		}
		if seqOf(t, f.id) <= seqOf(t, prev) {
			t.Fatalf("ids not increasing: %s after %s", f.id, prev)
		}
		prev = f.id
	}
	h.bus.Publish(delta(mp, "live"))
	if f := c2.next(t); !strings.Contains(f.data, "live") {
		t.Fatalf("live after replay = %+v", f)
	}
	c2.quiet(t)
}

func seqOf(t *testing.T, id string) uint64 {
	t.Helper()
	_, n, ok := parseLastEventID(id)
	if !ok {
		t.Fatalf("bad id %q", id)
	}
	return n
}

func TestSSEResyncAcrossBoots(t *testing.T) {
	h := newHarness(t).setup()
	srv := h.server(t)
	c := h.openSSE(t, srv, h.member, "0123456789abcdef-42")
	if f := c.next(t); f.event != "hello" {
		t.Fatalf("first = %+v", f)
	}
	if f := c.next(t); f.event != "resync" || f.data != "{}" {
		t.Fatalf("second = %+v", f)
	}
	// Live events still flow after a resync.
	h.bus.Publish(delta(core.UserPrincipal(h.member.user), "after"))
	if f := c.next(t); f.event != "run.delta" {
		t.Fatalf("after resync = %+v", f)
	}
	// A cursor beyond the newest sequence (another boot with the same id
	// prefix cannot exist, but a stale ring can) also resyncs.
	c2 := h.openSSE(t, srv, h.member, h.bus.BootID()+"-999999")
	c2.next(t)
	if f := c2.next(t); f.event != "resync" {
		t.Fatalf("future cursor = %+v", f)
	}
}

func TestSSEConnectionCap(t *testing.T) {
	h := newHarness(t).setup()
	srv := h.server(t)
	var conns []*sseConn
	for i := 0; i < maxUserSSE+1; i++ {
		c := h.openSSE(t, srv, h.member, "")
		c.next(t)
		conns = append(conns, c)
	}
	if !conns[0].closed(t) {
		t.Fatal("oldest stream was not closed")
	}
	h.bus.Publish(delta(core.UserPrincipal(h.member.user), "still here"))
	for _, c := range conns[1:] {
		if f := c.next(t); f.event != "run.delta" {
			t.Fatalf("frame = %+v", f)
		}
	}
}

// blockingWriter stalls the handler on its first event write, so the bus
// subscription overflows.
type blockingWriter struct {
	mu      sync.Mutex
	h       http.Header
	buf     strings.Builder
	release chan struct{}
	writes  int
}

func (b *blockingWriter) Header() http.Header { return b.h }
func (b *blockingWriter) WriteHeader(int)     {}
func (b *blockingWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.writes++
	n := b.writes
	b.mu.Unlock()
	if n == 2 {
		<-b.release
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *blockingWriter) Flush() {}
func (b *blockingWriter) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSSEOverflowResyncsAndCloses(t *testing.T) {
	h := newHarness(t).setup()
	mp := core.UserPrincipal(h.member.user)
	bw := &blockingWriter{h: http.Header{}, release: make(chan struct{})}
	req := h.request("GET", "/api/events", nil, h.member)
	done := make(chan struct{})
	go func() {
		h.h.ServeHTTP(bw, req)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(bw.String(), "event: hello") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < sseBuffer+50; i++ {
		h.bus.Publish(delta(mp, "x"))
	}
	close(bw.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not close after overflow")
	}
	if !strings.Contains(bw.String(), "event: resync") {
		t.Fatalf("no resync frame in %q", bw.String()[:min(len(bw.String()), 400)])
	}
}
