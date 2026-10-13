package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	sseBuffer   = 256
	sseDeadline = 10 * time.Second
	// sseRetry is the reconnection delay every stream starts with, which
	// also undoes an earlier sseEvictedRetry in the same EventSource.
	sseRetry = 3 * time.Second
	// sseEvictedRetry is the delay a stream closed for the per-user cap is
	// told to wait. With the browser's default of about 3 s, five tabs would
	// evict each other in a loop, each reconnect closing another stream.
	sseEvictedRetry = time.Minute
)

// sseTracker caps concurrent streams per user; the oldest is closed first.
type sseTracker struct {
	mu    sync.Mutex
	conns map[core.UserID][]*sseStream
	next  uint64
}

type sseStream struct {
	id      uint64
	cancel  context.CancelFunc
	evicted atomic.Bool // closed for the per-user cap, not by the client
}

func newSSETracker() *sseTracker {
	return &sseTracker{conns: map[core.UserID][]*sseStream{}}
}

func (t *sseTracker) add(u core.UserID, cancel context.CancelFunc) *sseStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	st := &sseStream{id: t.next, cancel: cancel}
	t.conns[u] = append(t.conns[u], st)
	for len(t.conns[u]) > maxUserSSE {
		old := t.conns[u][0]
		old.evicted.Store(true)
		old.cancel()
		t.conns[u] = t.conns[u][1:]
	}
	return st
}

// closeUser ends every stream of a user whose access was just revoked.
func (t *sseTracker) closeUser(u core.UserID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, st := range t.conns[u] {
		st.cancel()
	}
}

func (t *sseTracker) remove(u core.UserID, st *sseStream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conns[u] = slices.DeleteFunc(t.conns[u], func(x *sseStream) bool { return x == st })
	if len(t.conns[u]) == 0 {
		delete(t.conns, u)
	}
}

type sseWriter struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	red func([]byte) []byte
}

func (sw *sseWriter) flush() error {
	_ = sw.rc.SetWriteDeadline(time.Now().Add(sseDeadline))
	return sw.rc.Flush()
}

func (sw *sseWriter) frame(id, event string, data []byte) error {
	var b strings.Builder
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("event: " + event + "\ndata: ")
	b.Write(sw.red(data))
	b.WriteString("\n\n")
	_ = sw.rc.SetWriteDeadline(time.Now().Add(sseDeadline))
	if _, err := sw.w.Write([]byte(b.String())); err != nil {
		return err
	}
	return sw.flush()
}

func (sw *sseWriter) comment(text string) error {
	_ = sw.rc.SetWriteDeadline(time.Now().Add(sseDeadline))
	if _, err := fmt.Fprintf(sw.w, ": %s\n\n", text); err != nil {
		return err
	}
	return sw.flush()
}

// retry sets the browser's reconnection delay for this EventSource.
func (sw *sseWriter) retry(d time.Duration, why string) error {
	_ = sw.rc.SetWriteDeadline(time.Now().Add(sseDeadline))
	if _, err := fmt.Fprintf(sw.w, ": %s\nretry: %d\n\n", why, d.Milliseconds()); err != nil {
		return err
	}
	return sw.flush()
}

func parseLastEventID(v string) (boot string, seq uint64, ok bool) {
	boot, n, found := strings.Cut(strings.TrimSpace(v), "-")
	if !found || boot == "" {
		return "", 0, false
	}
	seq, err := strconv.ParseUint(n, 10, 64)
	return boot, seq, err == nil
}

// events is GET /api/events (SPEC §8.2). The subscription is opened before
// the replay is read, so nothing published in between is lost; events seen
// in both are skipped by sequence number.
func (s *Server) events(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	if s.d.Bus == nil {
		return core.ErrNotImplemented
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream := s.sse.add(a.User, cancel)
	defer s.sse.remove(a.User, stream)

	f := filterFor(a)
	sub := s.d.Bus.Subscribe(f, sseBuffer)
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w, rc: http.NewResponseController(w), red: s.redactBytes}
	if err := sw.retry(sseRetry, "connected"); err != nil {
		return nil
	}
	boot := s.d.Bus.BootID()

	var replay []core.Event
	resync := false
	var last uint64
	if lb, seq, ok := parseLastEventID(r.Header.Get("Last-Event-ID")); ok && lb == boot {
		evs, complete := s.d.Bus.Since(seq, f)
		replay, resync, last = evs, !complete, seq
	} else if r.Header.Get("Last-Event-ID") != "" {
		resync = true
	}
	// A fresh client loads state over REST, so it gets every live event; the
	// hello only tells it where the stream stood. Events between its last
	// match and the bus head cannot match f, so the head serves as well and
	// needs no copy of the ring under the bus lock.
	helloSeq := last
	if !resync && len(replay) == 0 && last == 0 {
		if b, ok := s.d.Bus.(interface{ Seq() uint64 }); ok {
			helloSeq = b.Seq()
		} else if evs, _ := s.d.Bus.Since(0, f); len(evs) > 0 {
			helloSeq = evs[len(evs)-1].Seq
		}
	}
	if err := s.sendHello(ctx, sw, a, f, boot, helloSeq, replay); err != nil {
		return nil
	}
	if resync {
		if err := sw.frame("", "resync", []byte("{}")); err != nil {
			return nil
		}
	}
	for _, e := range replay {
		if err := s.sendEvent(sw, boot, e); err != nil {
			return nil
		}
		last = e.Seq
	}
	ping := time.NewTicker(s.ssePing)
	defer ping.Stop()
	checked := time.Now()
	for {
		select {
		case <-ctx.Done():
			if stream.evicted.Load() && r.Context().Err() == nil {
				_ = sw.retry(sseEvictedRetry, "closed: this user has too many open streams")
			}
			return nil
		case <-s.shutdown:
			return nil
		case <-ping.C:
			if sub.Dropped() > 0 {
				_ = sw.frame("", "resync", []byte("{}"))
				return nil
			}
			if time.Since(checked) >= s.sseRecheck {
				checked = time.Now()
				if !s.stillAuthorized(r, a) {
					return nil
				}
			}
			if err := sw.comment("ping"); err != nil {
				return nil
			}
		case e, open := <-sub.C():
			if !open {
				return nil
			}
			if sub.Dropped() > 0 {
				_ = sw.frame("", "resync", []byte("{}"))
				return nil
			}
			if e.Seq <= last {
				continue
			}
			if err := s.sendEvent(sw, boot, e); err != nil {
				return nil
			}
			last = e.Seq
		}
	}
}

// stillAuthorized re-runs authentication for an open stream, whose filter
// was fixed at connect time: a revoked token, an ended session, a disabled
// account, a role change or a changed set of principals (a new guest space)
// closes it, and the client reconnects with whatever access it has now.
func (s *Server) stillAuthorized(r *http.Request, a core.Actor) bool {
	rt := route{access: accessRead}
	cur, err := s.authenticate(r, rt)
	if err != nil {
		return false
	}
	return cur.User == a.User && cur.Role == a.Role && authorize(r, rt, cur) == nil &&
		samePrincipals(cur.Viewer.Principals, a.Viewer.Principals)
}

func samePrincipals(x, y []core.PrincipalID) bool {
	if len(x) != len(y) {
		return false
	}
	x, y = slices.Clone(x), slices.Clone(y)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func (s *Server) sendHello(ctx context.Context, sw *sseWriter, a core.Actor, f core.Filter, boot string, seq uint64, replay []core.Event) error {
	hello := struct {
		Boot    string            `json:"boot"`
		Seq     uint64            `json:"seq"`
		Paused  bool              `json:"paused"`
		Sandbox bool              `json:"sandbox"`
		Banners []core.BannerData `json:"banners"`
	}{Boot: boot, Seq: seq, Sandbox: s.sandboxAvailable(ctx), Banners: []core.BannerData{}}
	if len(replay) > 0 {
		hello.Seq = replay[len(replay)-1].Seq
	}
	if s.d.Runner != nil {
		hello.Paused = s.d.Runner.Paused()
	}
	hello.Banners = banners(s.d.Bus, f)
	b, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	return sw.frame("", "hello", b)
}

func (s *Server) sendEvent(sw *sseWriter, boot string, e core.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		s.log.Error("encode event", "type", e.Type, "err", err)
		return nil
	}
	return sw.frame(boot+"-"+strconv.FormatUint(e.Seq, 10), string(e.Type), b)
}

// redactBytes is defence in depth: payloads are redacted before publishing,
// but SSE is a documented redaction point (SPEC §10 #3).
func (s *Server) redactBytes(b []byte) []byte {
	if s.d.Vault == nil {
		return b
	}
	return s.d.Vault.Redactor().RedactBytes(b)
}
