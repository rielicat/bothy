package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	helloTimeout  = 10 * time.Second
	maxHelloBytes = 64 << 10
)

type spliceResult struct {
	up, down int64
	denied   bool
	reason   string
}

// budget is one direction of a box's traffic: its byte total against the
// session cap (0 = unlimited) and the box's activity flag.
type budget struct {
	used   *atomic.Int64
	limit  int64
	active *atomic.Bool
}

// take charges n bytes and reports whether the total stays within the cap.
// Activity is flagged before the bytes are passed on.
func (g budget) take(n int) bool {
	total := g.used.Add(int64(n))
	if g.limit > 0 && total > g.limit {
		return false
	}
	g.active.Store(true)
	return true
}

func (s *Service) budgets(b *box) (up, down budget) {
	upCap, downCap := s.caps()
	return budget{used: &b.use.up, limit: upCap, active: &b.activity},
		budget{used: &b.use.down, limit: downCap, active: &b.activity}
}

func (s *Service) tunnel(b *box, c net.Conn, cr io.Reader, up net.Conn, t target) spliceResult {
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return spliceResult{reason: "client went away"}
	}
	_ = c.SetWriteDeadline(time.Time{})
	sni, hello, err := peekSNI(cr, c)
	if err != nil {
		return spliceResult{denied: true, reason: "no TLS ClientHello"}
	}
	if !sniMatches(sni, t.host) {
		return spliceResult{denied: true, reason: fmt.Sprintf("SNI %q does not match CONNECT host", truncate(sni, 64))}
	}
	upB, downB := s.budgets(b)
	if !upB.take(len(hello)) {
		return spliceResult{reason: "upload cap exceeded"}
	}
	n, err := up.Write(hello)
	if err != nil {
		return spliceResult{up: int64(n), reason: "upstream write failed"}
	}
	r := splice(c, cr, up, upB, downB)
	r.up += int64(n)
	return r
}

type dirResult struct {
	n      int64
	capped bool
}

// idle tracks the last byte moved in either direction, so a long download
// is not cut off because the client side has nothing to say.
type idle struct{ last atomic.Int64 }

func newIdle() *idle {
	i := &idle{}
	i.touch()
	return i
}

func (i *idle) touch() { i.last.Store(time.Now().UnixNano()) }

func (i *idle) expired() bool { return time.Since(time.Unix(0, i.last.Load())) >= idleTimeout }

// splice copies both directions under the box's byte caps. A client EOF is
// passed on as a half-close; the upstream closing ends the tunnel.
func splice(c net.Conn, cr io.Reader, up net.Conn, upB, downB budget) spliceResult {
	act := newIdle()
	upc, downc := make(chan dirResult, 1), make(chan dirResult, 1)
	go func() {
		n, capped, err := pipe(up, cr, c, upB, act)
		if capped || err != nil {
			_ = c.Close()
			_ = up.Close()
		} else {
			closeWrite(up)
		}
		upc <- dirResult{n, capped}
	}()
	go func() {
		n, capped, _ := pipe(c, up, up, downB, act)
		_ = c.Close()
		_ = up.Close()
		downc <- dirResult{n, capped}
	}()
	u, d := <-upc, <-downc
	res := spliceResult{up: u.n, down: d.n}
	switch {
	case u.capped:
		res.reason = "upload cap exceeded"
	case d.capped:
		res.reason = "download cap exceeded"
	}
	return res
}

// pipe copies src to dst until EOF, an error, the cap or idleTimeout without
// traffic in either direction. readConn is the connection src reads from.
func pipe(dst net.Conn, src io.Reader, readConn net.Conn, g budget, act *idle) (n int64, capped bool, err error) {
	buf := make([]byte, 32<<10)
	for {
		_ = readConn.SetReadDeadline(time.Now().Add(idleTimeout))
		nr, rerr := src.Read(buf)
		if nr > 0 {
			act.touch()
			if !g.take(nr) {
				return n, true, nil
			}
			_ = dst.SetWriteDeadline(time.Now().Add(idleTimeout))
			nw, werr := dst.Write(buf[:nr])
			n += int64(nw)
			if werr != nil {
				return n, false, werr
			}
		}
		if rerr != nil {
			var ne net.Error
			switch {
			case errors.Is(rerr, io.EOF):
				return n, false, nil
			case errors.As(rerr, &ne) && ne.Timeout() && !act.expired():
				continue
			}
			return n, false, rerr
		}
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// chargedWriter counts what the client sends upstream against the up cap.
type chargedWriter struct {
	w      net.Conn
	g      budget
	n      int64
	capped bool
}

func (cw *chargedWriter) Write(p []byte) (int, error) {
	if !cw.g.take(len(p)) {
		cw.capped = true
		return 0, errors.New("upload cap exceeded")
	}
	_ = cw.w.SetWriteDeadline(time.Now().Add(idleTimeout))
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// forward relays one absolute-form plain-HTTP request. The Host header is
// forced to the target so a shared IP cannot be asked for another virtual
// host, and the connection closes after one response.
func (s *Service) forward(b *box, c net.Conn, cr io.Reader, up net.Conn, req *http.Request, t target) spliceResult {
	for _, f := range req.Header.Values("Connection") {
		for _, tok := range strings.Split(f, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				req.Header.Del(tok)
			}
		}
	}
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	req.Host = t.host.urlHost()
	if t.port != 80 {
		req.Host += ":" + strconv.Itoa(t.port)
	}
	req.Close = true

	upB, downB := s.budgets(b)
	downc := make(chan dirResult, 1)
	go func() {
		n, capped, _ := pipe(c, up, up, downB, newIdle())
		_ = c.Close()
		_ = up.Close()
		downc <- dirResult{n, capped}
	}()
	_ = c.SetReadDeadline(time.Now().Add(idleTimeout))
	cw := &chargedWriter{w: up, g: upB}
	if err := req.Write(cw); err != nil {
		_ = up.Close()
		_ = c.Close()
	}
	d := <-downc
	res := spliceResult{up: cw.n, down: d.n}
	switch {
	case cw.capped:
		res.reason = "upload cap exceeded"
	case d.capped:
		res.reason = "download cap exceeded"
	}
	return res
}

func sniMatches(sni string, h hostname) bool {
	if h.isIP() {
		return sni == ""
	}
	return strings.TrimSuffix(strings.ToLower(sni), ".") == h.name
}

var errHelloCaptured = errors.New("client hello captured")

// helloConn feeds a tls.Server from r while recording every byte consumed,
// and swallows the alert the aborted handshake writes back.
type helloConn struct {
	r   io.Reader
	buf bytes.Buffer
}

func (h *helloConn) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.buf.Write(p[:n])
	return n, err
}

func (h *helloConn) Write(p []byte) (int, error)      { return len(p), nil }
func (h *helloConn) Close() error                     { return nil }
func (h *helloConn) LocalAddr() net.Addr              { return &net.UnixAddr{Net: "unix"} }
func (h *helloConn) RemoteAddr() net.Addr             { return &net.UnixAddr{Net: "unix"} }
func (h *helloConn) SetDeadline(time.Time) error      { return nil }
func (h *helloConn) SetReadDeadline(time.Time) error  { return nil }
func (h *helloConn) SetWriteDeadline(time.Time) error { return nil }

// peekSNI parses the client's TLS ClientHello with crypto/tls itself and
// returns its server name plus the exact bytes read, to be replayed upstream.
func peekSNI(r io.Reader, c net.Conn) (sni string, consumed []byte, err error) {
	_ = c.SetReadDeadline(time.Now().Add(helloTimeout))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	hc := &helloConn{r: io.LimitReader(r, maxHelloBytes)}
	got := false
	cfg := &tls.Config{GetConfigForClient: func(ch *tls.ClientHelloInfo) (*tls.Config, error) {
		sni, got = ch.ServerName, true
		return nil, errHelloCaptured
	}}
	ctx, cancel := context.WithTimeout(context.Background(), helloTimeout)
	defer cancel()
	herr := tls.Server(hc, cfg).HandshakeContext(ctx)
	if !got {
		return "", nil, fmt.Errorf("reading TLS ClientHello: %w", herr)
	}
	return sni, hc.buf.Bytes(), nil
}
