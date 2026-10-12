package discord

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"github.com/bothyhq/bothy/internal/core"
)

func hostPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return net.JoinHostPort(u.Hostname(), p)
	}
	switch u.Scheme {
	case "http", "ws":
		return net.JoinHostPort(u.Hostname(), "80")
	default:
		return net.JoinHostPort(u.Hostname(), "443")
	}
}

func (a *adapter) dest(hostport string) core.SystemDest {
	return core.SystemDest{Name: "channel:" + name, Hosts: []string{hostport}, AllowPrivate: a.allowPrivate}
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// systemDialer returns the egress system dialer for exactly dest. AdapterDeps
// hands out only *http.Client values, so the dialer is borrowed from the
// client's *http.Transport; a client without one fails closed instead of
// dialing around egress (ICR core.AdapterDeps.Dial would remove this).
func systemDialer(deps core.AdapterDeps, dest core.SystemDest) (plain, secure dialFunc, err error) {
	c := deps.HTTP(dest)
	var tr *http.Transport
	if c != nil {
		tr, _ = c.Transport.(*http.Transport)
	}
	if tr == nil || tr.DialContext == nil {
		return nil, nil, fmt.Errorf("egress client for %v exposes no dialer: %w", dest.Hosts, core.ErrForbidden)
	}
	if tr.DialTLSContext != nil {
		return tr.DialContext, tr.DialTLSContext, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if tr.TLSClientConfig != nil {
		tc = tr.TLSClientConfig.Clone()
		// http.Transport adds "h2" to NextProtos; the WebSocket handshake is
		// HTTP/1.1 only.
		tc.NextProtos = nil
		tc.MinVersion = max(tc.MinVersion, tls.VersionTLS12)
	}
	secure = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := tr.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		cfg := tc.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName, _, _ = net.SplitHostPort(addr)
		}
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	return tr.DialContext, secure, nil
}

// newOptionArg allocates the value an option function takes a pointer to.
// The gateway's dialer type lives in gorilla/websocket, which SPEC §11.1 does
// not approve as a direct import; reaching it through disgo's own option
// keeps the import graph within the approved set while the WebSocket still
// goes through egress.
func newOptionArg[T, O any](func(*T) O) *T { return new(T) }

// quietHandler drops debug records. disgo logs whole gateway and REST
// payloads at debug level, message content and the IDENTIFY token included.
type quietHandler struct{ slog.Handler }

func (h quietHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l > slog.LevelDebug && h.Handler.Enabled(ctx, l)
}

func (h quietHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return quietHandler{h.Handler.WithAttrs(as)}
}

func (h quietHandler) WithGroup(g string) slog.Handler { return quietHandler{h.Handler.WithGroup(g)} }

var errTooLarge = errors.New("attachment exceeds the size limit")

// cappedBody fails the read that would take the body past its limit.
type cappedBody struct {
	io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One more byte tells a body of exactly the limit from a longer one.
		var one [1]byte
		n, err := b.ReadCloser.Read(one[:])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}
