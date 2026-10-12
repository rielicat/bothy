//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEventStreamParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Last-Event-ID"); got != "b1-7" {
			t.Errorf("Last-Event-ID = %q, want b1-7", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: hello\ndata: {\"boot\":\"b1\",\"seq\":7}\n\n")
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, "id: b1-8\r\nevent: run.delta\r\ndata: {\"a\":\r\ndata: 1}\r\n\r\n")
		fmt.Fprint(w, "event: resync\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close) // after the stream's own cleanup, which ends the handler

	g := &Gateway{BaseURL: srv.URL, Client: srv.Client()}
	s := g.Events(t, "b1-7", nil)
	if s.Status != http.StatusOK {
		t.Fatalf("status %d", s.Status)
	}
	hello := s.WaitFor(t, "hello", 5*time.Second)
	if string(hello.Data) != `{"boot":"b1","seq":7}` {
		t.Errorf("hello data %s", hello.Data)
	}
	delta, err := s.Next(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if delta.ID != "b1-8" || delta.Event != "run.delta" || string(delta.Data) != "{\"a\":\n1}" {
		t.Errorf("delta = %+v (%s)", delta, delta.Data)
	}
	s.WaitFor(t, "resync", 5*time.Second)
	if _, err := s.Next(50 * time.Millisecond); err == nil {
		t.Error("expected a timeout after the last frame")
	}
}
