package mcp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

// headerTransport adds vault-resolved headers to every request of one HTTP
// MCP session. Values stay core.Secret until the moment they are set on the
// outgoing request, so nothing that formats the transport can print them.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]core.Secret
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v.Reveal())
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	// The SDK reads a JSON response whole; event streams it bounds per event.
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); !strings.HasPrefix(ct, "text/event-stream") {
		resp.Body = &cappedBody{ReadCloser: resp.Body}
	}
	return resp, nil
}

// maxResponseBytes bounds one non-streaming response of an HTTP MCP server.
const maxResponseBytes = 16 << 20

var errResponseTooLarge = fmt.Errorf("mcp server response is larger than %d MB", maxResponseBytes>>20)

type cappedBody struct {
	io.ReadCloser
	n int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.n > maxResponseBytes {
		return 0, errResponseTooLarge
	}
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if b.n > maxResponseBytes {
		return n, errResponseTooLarge
	}
	return n, err
}

// onceCloser makes Close idempotent: the SDK closes an IOTransport's reader
// and writer separately, and both are the same sandbox stdio stream.
type onceCloser struct {
	io.ReadWriteCloser
	once sync.Once
	err  error
}

func (c *onceCloser) Close() error {
	c.once.Do(func() { c.err = c.ReadWriteCloser.Close() })
	return c.err
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	benchmark = netip.MustParsePrefix("198.18.0.0/15")
	metadata  = []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("fd00:ec2::254")}
)

var errPrivateHost = errors.New("private address")

// checkHost refuses a server URL whose host is a private, loopback or
// link-local IP literal (or a localhost name) unless allow_private is set, and
// cloud metadata addresses always. The egress system dialer enforces the same
// rules after DNS resolution; this check fails fast with a clear message for
// literals, before any request is attempted.
func checkHost(hostPort string, allowPrivate bool) error {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return err
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		if allowPrivate {
			return nil
		}
		return fmt.Errorf("%w: %s needs allow_private", errPrivateHost, host)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return checkAddr(addr.WithZone("").Unmap(), allowPrivate)
	}
	return nil // a DNS name: the egress dialer checks what it resolves to
}

func checkAddr(addr netip.Addr, allowPrivate bool) error {
	for _, m := range metadata {
		if addr == m {
			return fmt.Errorf("%w: %s is a cloud metadata address", errPrivateHost, addr)
		}
	}
	private := addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsUnspecified() || addr.IsMulticast() || cgnat.Contains(addr) || benchmark.Contains(addr)
	if private && !allowPrivate {
		return fmt.Errorf("%w: %s needs allow_private", errPrivateHost, addr)
	}
	return nil
}
