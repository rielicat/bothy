package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// v1SmallBody is how much of a /v1 body is read without a large-body
	// slot. Bigger bodies carry images, and parsing one holds the body, its
	// decoded JSON and the decoded images at once, several times its size.
	v1SmallBody = 1 << 20
	// v1LargeSlots bounds how many such bodies are in memory at once: one
	// maximum-size request already outweighs the gateway's GOMEMLIMIT (SPEC
	// §11.3), so concurrent ones must queue rather than add up.
	v1LargeSlots = 1
	// v1LargeWait is how long a large request waits for a slot before it
	// is turned away with 429 and a Retry-After of the same length.
	v1LargeWait = 10 * time.Second
)

var errV1Busy = &v1Err{status: http.StatusTooManyRequests, code: "rate_limited",
	msg: "too many requests with large bodies (images) are being processed; retry shortly"}

// readV1Body reads a /v1 body (already capped at v1BodyMax by the route).
// One over v1SmallBody first takes a large-body slot; release gives it back
// and is safe to call more than once.
func (s *Server) readV1Body(r *http.Request) (body []byte, release func(), err error) {
	release = func() {}
	var buf bytes.Buffer
	if r.ContentLength > v1SmallBody {
		if release, err = s.largeSlot(r.Context()); err != nil {
			return nil, func() {}, err
		}
		buf.Grow(int(min(r.ContentLength, v1BodyMax)))
	} else {
		if _, err := buf.ReadFrom(io.LimitReader(r.Body, v1SmallBody+1)); err != nil {
			return nil, release, err
		}
		if buf.Len() <= v1SmallBody {
			return buf.Bytes(), release, nil
		}
		// A chunked or mislabelled body turned out large.
		if release, err = s.largeSlot(r.Context()); err != nil {
			return nil, func() {}, err
		}
	}
	if _, err := buf.ReadFrom(r.Body); err != nil {
		release()
		return nil, func() {}, err
	}
	return buf.Bytes(), release, nil
}

func (s *Server) largeSlot(ctx context.Context) (func(), error) {
	t := time.NewTimer(s.v1Wait)
	defer t.Stop()
	select {
	case s.v1Large <- struct{}{}:
		return sync.OnceFunc(func() { <-s.v1Large }), nil
	case <-t.C:
		return nil, errV1Busy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// v1Keys maps each top-level key of a /v1 request to its value's literal for
// scalars ("null", "true", "4") or its first byte (`"`, "{", "[").
type v1Keys map[string]string

// set reports a key that is present and not null.
func (k v1Keys) set(name string) bool {
	v, ok := k[name]
	return ok && v != "null"
}

func (k v1Keys) ignored(honoured []string) []string {
	var out []string
	for name := range k {
		if !slices.Contains(honoured, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

var errNotObject = errors.New("the request must be a JSON object")

// topLevelKeys walks the top level of body, which json.Unmarshal has already
// validated, without decoding or copying any value: the request itself is
// decoded once, into oaRequest.
func topLevelKeys(body []byte) (v1Keys, error) {
	keys := v1Keys{}
	i := skipSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil, errNotObject
	}
	i++
	for {
		i = skipSpace(body, i)
		if i < len(body) && body[i] == ',' {
			i = skipSpace(body, i+1)
		}
		if i >= len(body) {
			return nil, errNotObject
		}
		if body[i] == '}' {
			return keys, nil
		}
		end := valueEnd(body, i)
		if end < 0 {
			return nil, errNotObject
		}
		var name string
		if err := json.Unmarshal(body[i:end], &name); err != nil {
			return nil, err
		}
		i = skipSpace(body, end)
		if i >= len(body) || body[i] != ':' {
			return nil, errNotObject
		}
		i = skipSpace(body, i+1)
		end = valueEnd(body, i)
		if end < 0 {
			return nil, errNotObject
		}
		switch body[i] {
		case '"', '{', '[':
			keys[name] = string(body[i])
		default:
			keys[name] = string(body[i:end])
		}
		i = end
	}
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// valueEnd returns the index just past the JSON value starting at b[i], or
// -1 when b ends first.
func valueEnd(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		for j := i + 1; j < len(b); j++ {
			switch b[j] {
			case '\\':
				j++
			case '"':
				return j + 1
			}
		}
		return -1
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end := valueEnd(b, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	}
	j := i
	for j < len(b) && strings.IndexByte(",}] \t\r\n", b[j]) < 0 {
		j++
	}
	return j
}
