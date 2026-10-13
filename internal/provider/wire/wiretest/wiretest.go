// Package wiretest serves recorded vendor responses to the provider clients
// in tests and records what the clients sent.
package wiretest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
)

// Key is the API key test clients send; tests assert it never leaks.
const Key = "test-key-CANARY-7f3a9c"

// Response is one canned reply.
type Response struct {
	Status      int
	ContentType string
	Header      http.Header
	Body        []byte
}

// Recorded is one request the server received.
type Recorded struct {
	Method string
	URL    string // path and query
	Header http.Header
	Body   []byte
}

// Server replies with Responses in order (the last one repeats).
type Server struct {
	*httptest.Server
	mu        sync.Mutex
	responses []Response
	n         int
	reqs      []Recorded
}

func NewServer(t testing.TB, rs ...Response) *Server {
	t.Helper()
	s := &Server{responses: rs}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.reqs = append(s.reqs, Recorded{Method: r.Method, URL: r.URL.RequestURI(), Header: r.Header.Clone(), Body: body})
	resp := Response{Status: http.StatusNotFound}
	if len(s.responses) > 0 {
		resp = s.responses[min(s.n, len(s.responses)-1)]
	}
	s.n++
	s.mu.Unlock()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// Requests returns what the server received so far.
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recorded(nil), s.reqs...)
}

// File reads a testdata file.
func File(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// SSE is a 200 text/event-stream reply with the given body.
func SSE(body []byte) Response {
	return Response{Status: http.StatusOK, ContentType: "text/event-stream", Body: body}
}

// JSON is a reply with an application/json body.
func JSON(status int, body []byte) Response {
	return Response{Status: status, ContentType: "application/json", Body: body}
}

// Options returns client options pointed at s with the test key.
func Options(s *Server, id string) wire.Options {
	return wire.Options{
		ID:      id,
		BaseURL: s.URL,
		HTTP:    s.Client(),
		Key: func(context.Context) (core.Secret, error) {
			return core.NewSecret([]byte(Key)), nil
		},
	}
}

// Events collects stream events.
type Events struct {
	mu  sync.Mutex
	All []core.StreamEvent
}

func (e *Events) On(ev core.StreamEvent) error {
	e.mu.Lock()
	e.All = append(e.All, ev)
	e.mu.Unlock()
	return nil
}

// Text joins the text deltas.
func (e *Events) Text() string {
	var s string
	for _, ev := range e.All {
		if ev.Type == core.StreamTextDelta {
			s += ev.Text
		}
	}
	return s
}

// Thinking joins the thinking deltas.
func (e *Events) Thinking() string {
	var s string
	for _, ev := range e.All {
		if ev.Type == core.StreamThinkingDelta {
			s += ev.Text
		}
	}
	return s
}

// Types lists the event types in order.
func (e *Events) Types() []core.StreamEventType {
	out := make([]core.StreamEventType, len(e.All))
	for i, ev := range e.All {
		out[i] = ev.Type
	}
	return out
}

// Done returns the StreamToolCallDone calls in order.
func (e *Events) Done() []core.ToolCall {
	var out []core.ToolCall
	for _, ev := range e.All {
		if ev.Type == core.StreamToolCallDone {
			out = append(out, *ev.Call)
		}
	}
	return out
}
