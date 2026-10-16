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
	"regexp"
	"strings"

	"github.com/rielicat/bothy/internal/core"
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
	return core.SystemDest{Name: "channel:" + name, Hosts: []string{hostport}, AllowPrivate: a.conf().privateHosts[strings.ToLower(hostport)]}
}

// egressClient returns the egress client for hostport. A nil one would make
// disgo panic or, elsewhere, fall back to the default transport; refusing
// every request keeps a wiring mistake closed.
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
// client's *http.Transport. A client without one, or one that would route
// this destination through a proxy (which a raw WebSocket dial cannot
// honour), fails closed instead of dialing around egress.
func systemDialer(deps core.AdapterDeps, dest core.SystemDest, secure bool) (dialFunc, error) {
	c := deps.HTTP(dest)
	var tr *http.Transport
	if c != nil {
		tr, _ = c.Transport.(*http.Transport)
	}
	if tr == nil || tr.DialContext == nil || len(dest.Hosts) != 1 {
		return nil, fmt.Errorf("egress client for %v exposes no dialer: %w", dest.Hosts, core.ErrForbidden)
	}
	if tr.Proxy != nil {
		scheme := "http"
		if secure {
			scheme = "https"
		}
		if p, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: scheme, Host: dest.Hosts[0]}}); err != nil || p != nil {
			return nil, fmt.Errorf("egress client for %v routes through a proxy: %w", dest.Hosts, core.ErrForbidden)
		}
	}
	if !secure {
		return tr.DialContext, nil
	}
	if tr.DialTLSContext != nil {
		return tr.DialTLSContext, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if tr.TLSClientConfig != nil {
		tc = tr.TLSClientConfig.Clone()
		// http.Transport adds "h2" to NextProtos; the WebSocket handshake is
		// HTTP/1.1 only.
		tc.NextProtos = nil
		tc.MinVersion = max(tc.MinVersion, tls.VersionTLS12)
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
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
	}, nil
}

// quietHandler drops debug records: disgo logs whole gateway and REST
// payloads at debug level, message content and the IDENTIFY token included.
// It also masks interaction tokens, which disgo's warnings and errors carry
// inside endpoint URLs.
type quietHandler struct{ slog.Handler }

func (h quietHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l > slog.LevelDebug && h.Handler.Enabled(ctx, l)
}

func (h quietHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, maskTokens(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(maskAttr(a))
		return true
	})
	return h.Handler.Handle(ctx, out)
}

func (h quietHandler) WithAttrs(as []slog.Attr) slog.Handler {
	masked := make([]slog.Attr, len(as))
	for i, a := range as {
		masked[i] = maskAttr(a)
	}
	return quietHandler{h.Handler.WithAttrs(masked)}
}

func (h quietHandler) WithGroup(g string) slog.Handler { return quietHandler{h.Handler.WithGroup(g)} }

// tokenPathRe matches the token segment of interaction callback and
// webhook (followup) paths.
var tokenPathRe = regexp.MustCompile(`(/(?:interactions|webhooks)/\d+/)[^/?#\s"]+`)

func maskTokens(s string) string {
	if !strings.Contains(s, "/interactions/") && !strings.Contains(s, "/webhooks/") {
		return s
	}
	return tokenPathRe.ReplaceAllString(s, "${1}<token>")
}

func maskAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, maskTokens(v.String()))
	case slog.KindGroup:
		g := v.Group()
		masked := make([]slog.Attr, len(g))
		for i, ga := range g {
			masked[i] = maskAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(masked...)}
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, maskTokens(err.Error()))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

// hideURL keeps the request URL out of a failed interaction call's error:
// the URL path carries the interaction token, and errors end up in logs.
func hideURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s interaction endpoint: %w", ue.Op, ue.Err)
	}
	return err
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
