package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Frame is one SSE event (SPEC §8.2): id "<boot>-<seq>", the event type, and
// the JSON data (a core.Event, or the hello/resync payloads).
type Frame struct {
	ID    string
	Event string
	Data  json.RawMessage
}

// EventStream reads GET /api/events until closed.
type EventStream struct {
	Status int
	frames chan Frame
	errc   chan error
	cancel context.CancelFunc
}

// Events opens the SSE stream with the gateway's cookies (or hdr, e.g. a
// bearer token). lastEventID resumes from a previous Frame.ID when non-empty.
func (g *Gateway) Events(t testing.TB, lastEventID string, hdr http.Header) *EventStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.BaseURL+"/api/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// The shared client has a whole-request timeout, which would cut the stream.
	client := &http.Client{Jar: g.Client.Jar}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET /api/events: %v", err)
	}
	s := &EventStream{Status: resp.StatusCode, frames: make(chan Frame, 256), errc: make(chan error, 1), cancel: cancel}
	t.Cleanup(s.Close)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.errc <- fmt.Errorf("GET /api/events: %s %s", resp.Status, body)
		return s
	}
	go s.read(ctx, resp.Body)
	return s
}

func (s *EventStream) read(ctx context.Context, body io.ReadCloser) {
	defer body.Close()
	r := bufio.NewReader(body)
	var f Frame
	var data []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			s.errc <- err // buffered; a stream sends at most one error
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if f.Event != "" || len(data) > 0 {
				if f.Event == "" {
					f.Event = "message"
				}
				f.Data = json.RawMessage(strings.Join(data, "\n"))
				select {
				case s.frames <- f:
				case <-ctx.Done():
					return
				}
			}
			f, data = Frame{}, nil
		case strings.HasPrefix(line, ":"): // comment (": ping")
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "id":
				f.ID = value
			case "event":
				f.Event = value
			case "data":
				data = append(data, value)
			}
		}
	}
}

// Next returns the next frame, or an error on timeout or stream end.
func (s *EventStream) Next(timeout time.Duration) (Frame, error) {
	select {
	case f := <-s.frames:
		return f, nil
	case err := <-s.errc:
		return Frame{}, err
	case <-time.After(timeout):
		return Frame{}, errors.New("sse: timeout")
	}
}

// WaitFor skips frames until one of type event arrives.
func (s *EventStream) WaitFor(t testing.TB, event string, timeout time.Duration) Frame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			t.Fatalf("sse: no %q event within %s", event, timeout)
		}
		f, err := s.Next(left)
		if err != nil {
			t.Fatalf("sse: waiting for %q: %v", event, err)
		}
		if f.Event == event {
			return f
		}
	}
}

func (s *EventStream) Close() { s.cancel() }
