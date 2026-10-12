package slack

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/slack-go/slack/socketmode"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	handshakeTimeout = 20 * time.Second
	// writeBuffer keeps every Socket Mode response in a single frame: Slack
	// does not reassemble continuation frames (slack-go uses the same size).
	writeBuffer = 32 * 1024
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

// egressClient returns the egress client for hostport. A nil one would make
// slack-go panic or fall back to the default transport; refusing every
// request keeps a wiring mistake closed.
func (a *adapter) egressClient(hostport string) *http.Client {
	if c := a.deps.HTTP(a.dest(hostport)); c != nil {
		return c
	}
	return &http.Client{Transport: refuse{}}
}

type refuse struct{}

func (refuse) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("no egress client for %s: %w", r.URL.Host, core.ErrForbidden)
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
// Socket Mode's dialer type lives in gorilla/websocket, which SPEC §11.1 does
// not approve as a direct import; reaching it through slack-go's own option
// keeps the import graph within the approved set while the WebSocket still
// goes through egress.
func newOptionArg[T, O any](func(*T) O) *T { return new(T) }

// socketDialer connects the Socket Mode WebSocket, whose host comes from
// apps.connections.open, through the egress system dialer for that exact
// host:port. It sets no proxy: slack-go's default dialer would read
// HTTPS_PROXY from the environment.
func (a *adapter) socketDialer() socketmode.Option {
	d := newOptionArg(socketmode.OptionDialer)
	d.HandshakeTimeout = handshakeTimeout
	d.WriteBufferSize = writeBuffer
	dial := func(secure bool) dialFunc {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			plain, tlsDial, err := systemDialer(a.deps, a.dest(addr))
			if err != nil {
				return nil, err
			}
			if secure {
				return tlsDial(ctx, network, addr)
			}
			return plain(ctx, network, addr)
		}
	}
	d.NetDialContext = dial(false)
	d.NetDialTLSContext = dial(true)
	return socketmode.OptionDialer(d)
}

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
