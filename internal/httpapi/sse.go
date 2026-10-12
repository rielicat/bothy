package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	ssePing     = 15 * time.Second
	sseBuffer   = 256
	sseDeadline = 10 * time.Second
)

// sseTracker caps concurrent streams per user; the oldest is closed first.
type sseTracker struct {
	mu    sync.Mutex
	conns map[core.UserID][]context.CancelFunc
	ids   map[core.UserID][]uint64
	next  uint64
}

func newSSETracker() *sseTracker {
	return &sseTracker{conns: map[core.UserID][]context.CancelFunc{}, ids: map[core.UserID][]uint64{}}
}

func (t *sseTracker) add(u core.UserID, cancel context.CancelFunc) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	id := t.next
	t.conns[u] = append(t.conns[u], cancel)
	t.ids[u] = append(t.ids[u], id)
	for len(t.conns[u]) > maxUserSSE {
		t.conns[u][0]()
		t.conns[u], t.ids[u] = t.conns[u][1:], t.ids[u][1:]
	}
	return id
}

// closeUser ends every stream of a user whose access was just revoked.
func (t *sseTracker) closeUser(u core.UserID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, cancel := range t.conns[u] {
		cancel()
	}
}

func (t *sseTracker) remove(u core.UserID, id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, x := range t.ids[u] {
		if x == id {
			t.conns[u] = append(t.conns[u][:i:i], t.conns[u][i+1:]...)
			t.ids[u] = append(t.ids[u][:i:i], t.ids[u][i+1:]...)
			break
		}
	}
	if len(t.ids[u]) == 0 {
		delete(t.conns, u)
		delete(t.ids, u)
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
	connID := s.sse.add(a.User, cancel)
	defer s.sse.remove(a.User, connID)

	f := filterFor(a)
	sub := s.d.Bus.Subscribe(f, sseBuffer)
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w, rc: http.NewResponseController(w), red: s.redactBytes}
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
	// hello only tells it where the stream stood.
	helloSeq := last
	if !resync && len(replay) == 0 && last == 0 {
		if evs, _ := s.d.Bus.Since(0, f); len(evs) > 0 {
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
	ping := time.NewTicker(ssePing)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.shutdown:
			return nil
		case <-ping.C:
			if sub.Dropped() > 0 {
				_ = sw.frame("", "resync", []byte("{}"))
				return nil
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
	for _, e := range s.d.Bus.Banners(f) {
		if b, ok := e.Data.(core.BannerData); ok {
			hello.Banners = append(hello.Banners, b)
		}
	}
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
