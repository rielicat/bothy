package wire

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// MaxEventBytes bounds one SSE event (or NDJSON line) so a hostile or broken
// server cannot make a client buffer without limit.
const MaxEventBytes = 8 << 20

// ErrEventTooLarge is returned when an event exceeds MaxEventBytes.
var ErrEventTooLarge = errors.New("stream event exceeds 8 MiB")

// Event is one server-sent event.
type Event struct {
	Name string // "event:" field; "" when absent
	Data []byte // "data:" lines joined by '\n'
}

// SSE reads server-sent events (WHATWG EventSource framing): CRLF and LF line
// ends, multi-line data, comments, and a final event without its blank line
// (some vendor fixtures end that way).
type SSE struct {
	r    *bufio.Reader
	name string
	data []byte
	has  bool
	done bool
}

func NewSSE(r io.Reader) *SSE { return &SSE{r: bufio.NewReaderSize(r, 32<<10)} }

// Next returns the next event, or io.EOF after the last one.
func (s *SSE) Next() (Event, error) {
	for !s.done {
		line, err := ReadLine(s.r)
		if err != nil && !errors.Is(err, io.EOF) {
			return Event{}, err
		}
		eof := errors.Is(err, io.EOF)
		if eof && line == nil {
			s.done = true
			break
		}
		if len(line) == 0 {
			if ev, ok := s.dispatch(); ok {
				return ev, nil
			}
		} else {
			if err := s.field(line); err != nil {
				return Event{}, err
			}
		}
		if eof {
			s.done = true
		}
	}
	if ev, ok := s.dispatch(); ok {
		return ev, nil
	}
	return Event{}, io.EOF
}

func (s *SSE) field(line []byte) error {
	if line[0] == ':' {
		return nil
	}
	name, value, _ := bytes.Cut(line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(name) {
	case "event":
		s.name = string(value)
		s.has = true
	case "data":
		if len(s.data)+len(value)+1 > MaxEventBytes {
			return ErrEventTooLarge
		}
		if s.data != nil {
			s.data = append(s.data, '\n')
		} else {
			s.data = make([]byte, 0, len(value))
		}
		s.data = append(s.data, value...)
		s.has = true
	}
	return nil
}

func (s *SSE) dispatch() (Event, bool) {
	if !s.has {
		return Event{}, false
	}
	ev := Event{Name: s.name, Data: s.data}
	s.name, s.data, s.has = "", nil, false
	return ev, true
}

// ReadLine returns one line without its "\n" or "\r\n" terminator, capped at
// MaxEventBytes. At the end of input it returns the trailing partial line
// (nil when there is none) with io.EOF.
func ReadLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > MaxEventBytes {
			return nil, ErrEventTooLarge
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return trimEOL(buf), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return trimEOL(buf), io.EOF
		default:
			return nil, fmt.Errorf("reading stream: %w", err)
		}
	}
}

func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}
