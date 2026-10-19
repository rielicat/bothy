package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const maxSystemClients = 256

// PublicClient is the session-scoped client for gateway-side tools
// (web_fetch). Keep-alives are off so every connection, and its egress_log
// row, belongs to exactly one session.
func (s *Service) PublicClient(sess core.SessionID, p core.PrincipalID) *http.Client {
	wf := s.cfg().WebFetch
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return s.publicDial(ctx, addr, sess, p)
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  wf.Timeout.D(),
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{Transport: publicTransport{tr}, CheckRedirect: redirectPolicy(wf.MaxRedirects)}
}

// publicTransport allows only credential-free GETs to http(s) URLs.
type publicTransport struct{ rt http.RoundTripper }

func (t publicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkPublicURL(req); err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	if req.Method != http.MethodGet {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, fmt.Errorf("egress: %w: public client allows GET only, not %s", errDenied, req.Method)
	}
	r := req.Clone(req.Context())
	for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		r.Header.Del(h)
	}
	return t.rt.RoundTrip(r)
}

func checkPublicURL(req *http.Request) error {
	u := req.URL
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("egress: %w: scheme %q", errDenied, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("egress: %w: credentials in URL", errDenied)
	}
	return nil
}

func redirectPolicy(max int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > max {
			return fmt.Errorf("egress: stopped after %d redirects", max)
		}
		if err := checkPublicURL(req); err != nil {
			return err
		}
		h, err := parseHost(req.URL.Hostname())
		if err != nil {
			return fmt.Errorf("egress: redirect: %w", err)
		}
		if h.isIP() {
			if r := addrProblem(h.ip, false); r != "" {
				return fmt.Errorf("egress: redirect: %w: %s", errBlocked, r)
			}
		}
		return nil
	}
}

func (s *Service) publicDial(ctx context.Context, addr string, sess core.SessionID, p core.PrincipalID) (net.Conn, error) {
	h, port, err := splitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	rec := core.EgressRecord{Dialer: "public", Session: sess, Principal: p, Host: h.name, Port: port}
	c, reason, err := s.guardedDial(ctx, h, port, false, false, rec)
	if reason != "" {
		s.publishBlocked(p, sess, "", h.name, port, reason)
	}
	return c, err
}

// guardedDial dials through dialPinned and logs the connection under rec. A
// non-empty reason means the destination was refused.
func (s *Service) guardedDial(ctx context.Context, h hostname, port int, allowPrivate, privateOnly bool, rec core.EgressRecord) (net.Conn, string, error) {
	rec.At = s.clock.Now()
	start := time.Now()
	pd := s.dialPinned(ctx, h, port, allowPrivate, privateOnly)
	if pd.ip.IsValid() {
		rec.IP = pd.ip.String()
	}
	if pd.reason != "" {
		rec.Decision, rec.Reason, rec.Duration = "deny", pd.reason, time.Since(start)
		s.record(rec)
		return nil, pd.reason, fmt.Errorf("egress: %w", pd.err)
	}
	if pd.err != nil {
		rec.Decision, rec.Reason, rec.Duration = "allow", "connect failed", time.Since(start)
		s.record(rec)
		return nil, "", fmt.Errorf("egress: %w", pd.err)
	}
	rec.Decision = "allow"
	return &trackedConn{Conn: pd.conn, onClose: func(up, down int64) {
		rec.Up, rec.Down, rec.Duration = up, down, time.Since(start)
		s.record(rec)
	}}, "", nil
}

// SystemClient is for providers, channels, search APIs and HTTP MCP servers.
// Clients are cached per destination so callers share keep-alive pools.
func (s *Service) SystemClient(d core.SystemDest) *http.Client {
	allowed := map[string]bool{}
	var hosts []string
	for _, hp := range d.Hosts {
		h, port, err := splitHostPort(hp)
		if err != nil {
			s.log.Warn("egress: ignoring invalid system destination host", "dest", d.Name, "host", hp, "err", err)
			continue
		}
		k := h.hostport(port)
		if !allowed[k] {
			allowed[k] = true
			hosts = append(hosts, k)
		}
	}
	slices.Sort(hosts)
	key := d.Name + "|" + strings.Join(hosts, ",") + "|" + strconv.FormatBool(d.AllowPrivate) + "|" + strconv.FormatBool(d.PrivateOnly)

	s.sysMu.Lock()
	defer s.sysMu.Unlock()
	if c, ok := s.sysClients[key]; ok {
		return c
	}
	if len(s.sysClients) >= maxSystemClients {
		for k, c := range s.sysClients {
			c.CloseIdleConnections()
			delete(s.sysClients, k)
			break
		}
	}
	name, allowPrivate, privateOnly := d.Name, d.AllowPrivate, d.PrivateOnly
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return s.systemDial(ctx, addr, name, allowed, allowPrivate, privateOnly)
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 1 << 20,
	}
	c := &http.Client{Transport: tr}
	s.sysClients[key] = c
	return c
}

func (s *Service) systemDial(ctx context.Context, addr, name string, allowed map[string]bool, allowPrivate, privateOnly bool) (net.Conn, error) {
	h, port, err := splitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	rec := core.EgressRecord{Dialer: "system", Dest: name, Host: h.name, Port: port}
	if !allowed[h.hostport(port)] {
		rec.At, rec.Decision, rec.Reason = s.clock.Now(), "deny", "not a configured destination"
		s.record(rec)
		s.log.Warn("egress: system destination refused", "dest", name, "host", h.hostport(port))
		return nil, fmt.Errorf("egress: %w: %s is not a configured host for %s", errDenied, h.hostport(port), name)
	}
	c, reason, err := s.guardedDial(ctx, h, port, allowPrivate, privateOnly, rec)
	if reason != "" {
		s.log.Warn("egress: system destination refused", "dest", name, "host", h.hostport(port), "reason", reason)
	}
	return c, err
}
