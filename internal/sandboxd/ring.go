package sandboxd

import "sync"

const stderrRing = 64 << 10

// ring keeps the last stderrRing bytes written to it.
type ring struct {
	mu   sync.Mutex
	buf  []byte
	full bool
	pos  int
}

func newRing() *ring { return &ring{buf: make([]byte, stderrRing)} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	if len(p) >= len(r.buf) {
		copy(r.buf, p[len(p)-len(r.buf):])
		r.pos, r.full = 0, true
		return n, nil
	}
	c := copy(r.buf[r.pos:], p)
	if c < len(p) {
		copy(r.buf, p[c:])
		r.full = true
	}
	r.pos = (r.pos + len(p)) % len(r.buf)
	if r.pos == 0 && len(p) > 0 {
		r.full = true
	}
	return n, nil
}

func (r *ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.pos:]...)
	return append(out, r.buf[:r.pos]...)
}
