package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// The AWS instance metadata service and the ECS container credential
// endpoint. Every other dialer refuses them (metadataAddrs).
var (
	imdsEndpoints = []netip.AddrPort{
		netip.MustParseAddrPort("169.254.169.254:80"),
		netip.MustParseAddrPort("[fd00:ec2::254]:80"),
	}
	ecsEndpoint = netip.MustParseAddrPort("169.254.170.2:80")
	// containerAddrs are the addresses AWS_CONTAINER_CREDENTIALS_FULL_URI may
	// name over plain http besides loopback (the SDK's own allowlist): the
	// ECS agent and the EKS Pod Identity agent.
	containerAddrs = []netip.Addr{
		netip.MustParseAddr("169.254.170.2"),
		netip.MustParseAddr("169.254.170.23"),
		netip.MustParseAddr("fd00:ec2::23"),
	}
)

const (
	credsDest        = "credentials:aws"
	credsDialTimeout = 2 * time.Second
	credsMaxBody     = 1 << 20

	imdsTokenPath   = "/latest/api/token"
	imdsRolesPath   = "/latest/meta-data/iam/security-credentials/"
	imdsRegionPath  = "/latest/meta-data/placement/region"
	imdsTokenHeader = "X-Aws-Ec2-Metadata-Token"
	imdsTTLHeader   = "X-Aws-Ec2-Metadata-Token-Ttl-Seconds"
)

// imdsRoleRe is an IAM role name under security-credentials/.
var imdsRoleRe = regexp.MustCompile(`^/latest/meta-data/iam/security-credentials/[A-Za-z0-9+=,.@_-]{1,128}$`)

// credTarget is one endpoint CredentialsClient may reach.
type credTarget struct {
	scheme   string
	hostport string         // normalized "host:port"
	fixed    netip.AddrPort // dialed directly; invalid for a full-URI host name
	imds     bool
	path     string // exact escaped path and query of a container endpoint
}

// CredentialsClient returns the client the bedrock provider's AWS credential
// chain uses for instance and container credentials, and nothing else may
// use: it reaches the EC2 instance metadata service for IMDSv2 token and
// role-credential requests (a GET without a session token, i.e. IMDSv1, is
// refused) and the container credential endpoint at exactly relativeURI
// (AWS_CONTAINER_CREDENTIALS_RELATIVE_URI, on 169.254.170.2) or fullURI
// (AWS_CONTAINER_CREDENTIALS_FULL_URI); either may be "". Connections are
// logged as system connections to "credentials:aws".
func (s *Service) CredentialsClient(relativeURI, fullURI string) *http.Client {
	targets := map[string]credTarget{}
	for _, ap := range imdsEndpoints {
		targets["http://"+ap.String()] = credTarget{scheme: "http", hostport: ap.String(), fixed: ap, imds: true}
	}
	if rel, err := url.Parse(relativeURI); strings.HasPrefix(relativeURI, "/") && err == nil && rel.Scheme == "" && rel.Host == "" && rel.User == nil {
		targets["http://"+ecsEndpoint.String()] = credTarget{scheme: "http", hostport: ecsEndpoint.String(), fixed: ecsEndpoint, path: requestPath(rel)}
	} else if relativeURI != "" {
		s.log.Warn("egress: ignoring an invalid container credentials relative URI")
	}
	if fullURI != "" {
		if k, t, err := fullURITarget(fullURI); err != nil {
			s.log.Warn("egress: ignoring the container credentials full URI", "err", err)
		} else if _, taken := targets[k]; !taken {
			targets[k] = t
		}
	}
	ct := &credsTransport{s: s, targets: targets}
	ct.rt = &http.Transport{
		Proxy:                  nil,
		DialContext:            ct.dial,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport:     ct,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// fullURITarget turns AWS_CONTAINER_CREDENTIALS_FULL_URI into its target key.
func fullURITarget(raw string) (string, credTarget, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "", credTarget{}, fmt.Errorf("unparseable URL: %w", err)
	case u.Scheme != "http" && u.Scheme != "https":
		return "", credTarget{}, fmt.Errorf("scheme %q", u.Scheme)
	case u.User != nil || u.Host == "":
		return "", credTarget{}, fmt.Errorf("URL needs a host and no credentials")
	}
	hp, err := urlHostPort(u)
	if err != nil {
		return "", credTarget{}, err
	}
	t := credTarget{scheme: u.Scheme, hostport: hp, path: requestPath(u)}
	if ap, err := netip.ParseAddrPort(hp); err == nil {
		a := ap.Addr().Unmap()
		for _, m := range imdsEndpoints {
			if a == m.Addr() {
				return "", credTarget{}, fmt.Errorf("the instance metadata address is not a container endpoint")
			}
		}
		if a.IsLoopback() || containsAddr(containerAddrs, a) {
			t.fixed = netip.AddrPortFrom(a, ap.Port())
		}
	}
	return u.Scheme + "://" + hp, t, nil
}

func containsAddr(list []netip.Addr, a netip.Addr) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

// requestPath is the escaped path and query a request for u carries.
func requestPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// urlHostPort normalizes a URL's authority to the "host:port" key the
// targets use (IPv6 in brackets, the scheme's default port filled in).
func urlHostPort(u *url.URL) (string, error) {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	h, err := parseHost(u.Hostname())
	if err != nil {
		return "", err
	}
	n, err := parsePort(port)
	if err != nil {
		return "", err
	}
	return h.hostport(n), nil
}

type credsTransport struct {
	s       *Service
	targets map[string]credTarget
	rt      *http.Transport
}

func (t *credsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	refuse := func(why string) (*http.Response, error) {
		if req.Body != nil {
			req.Body.Close()
		}
		rec := core.EgressRecord{At: t.s.clock.Now(), Dialer: "system", Dest: credsDest, Decision: "deny", Reason: why}
		if h, err := parseHost(req.URL.Hostname()); err == nil {
			rec.Host = h.name
		} else {
			rec.Host = req.URL.Hostname()
		}
		rec.Port, _ = strconv.Atoi(req.URL.Port())
		if rec.Port == 0 && req.URL.Scheme == "https" {
			rec.Port = 443
		} else if rec.Port == 0 {
			rec.Port = 80
		}
		t.s.record(rec)
		t.s.log.Warn("egress: credentials request refused", "host", req.URL.Host, "reason", why)
		return nil, fmt.Errorf("egress: %w: credentials client: %s", errDenied, why)
	}
	hp, err := urlHostPort(req.URL)
	if err != nil {
		return refuse(err.Error())
	}
	tg, ok := t.targets[req.URL.Scheme+"://"+hp]
	if !ok {
		return refuse(hp + " is not a credential endpoint")
	}
	if why := tg.check(req); why != "" {
		return refuse(why)
	}
	r := req.Clone(req.Context())
	r.Header.Del("Cookie")
	r.Header.Del("Proxy-Authorization")
	resp, err := t.rt.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedReadCloser{r: io.LimitReader(resp.Body, credsMaxBody), c: resp.Body}
	return resp, nil
}

// check returns why req is not a request the AWS SDK's credential providers
// make to this endpoint, or "".
func (tg credTarget) check(req *http.Request) string {
	method := req.Method
	if method == "" {
		method = http.MethodGet // net/http's meaning of an empty method; the AWS SDK sends it
	}
	if !tg.imds {
		if method != http.MethodGet {
			return "container credentials allow GET only"
		}
		if requestPath(req.URL) != tg.path {
			return "not the configured container credentials path"
		}
		return ""
	}
	if req.URL.RawQuery != "" {
		return "instance metadata requests take no query"
	}
	p := req.URL.EscapedPath()
	switch method {
	case http.MethodPut:
		if p != imdsTokenPath || req.Header.Get(imdsTTLHeader) == "" {
			return "only the IMDSv2 token request may PUT"
		}
		return ""
	case http.MethodGet:
		if req.Header.Get(imdsTokenHeader) == "" {
			return "IMDSv1 is refused: a session token is required"
		}
		if p == imdsRolesPath || p == imdsRegionPath || imdsRoleRe.MatchString(p) {
			return ""
		}
		return "instance metadata path " + p + " is not a credential path"
	}
	return "method " + method + " is not allowed"
}

func (t *credsTransport) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	h, port, err := splitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	var tg credTarget
	found := false
	for _, x := range t.targets {
		if x.hostport == h.hostport(port) {
			tg, found = x, true
			break
		}
	}
	rec := core.EgressRecord{Dialer: "system", Dest: credsDest, Host: h.name, Port: port}
	if !found {
		rec.At, rec.Decision, rec.Reason = t.s.clock.Now(), "deny", "not a credential endpoint"
		t.s.record(rec)
		return nil, fmt.Errorf("egress: %w: %s is not a credential endpoint", errDenied, h.hostport(port))
	}
	if !tg.fixed.IsValid() {
		// A full-URI host name: an ordinary pinned dial, so it still cannot
		// resolve to the metadata addresses.
		c, _, err := t.s.guardedDial(ctx, h, port, true, rec)
		return c, err
	}
	return t.s.fixedDial(ctx, tg.fixed, rec)
}

// fixedDial connects to one credential endpoint address. egress.deny still
// applies; the blocklist does not, which is the point of this dialer, so the
// socket's Control accepts exactly ap.
func (s *Service) fixedDial(ctx context.Context, ap netip.AddrPort, rec core.EgressRecord) (net.Conn, error) {
	rec.At, rec.IP = s.clock.Now(), ap.Addr().String()
	start := time.Now()
	lit := hostname{name: ap.Addr().String(), ip: ap.Addr()}
	if e, ok := matchAny(s.cfg().Egress.Deny, lit, int(ap.Port()), true, true); ok {
		rec.Decision, rec.Reason = "deny", "denylisted ("+e+")"
		s.record(rec)
		return nil, fmt.Errorf("egress: %w: %s is denylisted", errDenied, ap)
	}
	dial := s.dialCreds
	if dial == nil {
		dial = credsDial
	}
	c, err := dial(ctx, ap)
	if err != nil {
		rec.Decision, rec.Reason, rec.Duration = "allow", "connect failed", time.Since(start)
		s.record(rec)
		return nil, fmt.Errorf("egress: %w", err)
	}
	rec.Decision = "allow"
	return &trackedConn{Conn: c, onClose: func(up, down int64) {
		rec.Up, rec.Down, rec.Duration = up, down, time.Since(start)
		s.record(rec)
	}}, nil
}

func credsDial(ctx context.Context, ap netip.AddrPort) (net.Conn, error) {
	d := &net.Dialer{Timeout: credsDialTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		got, err := netip.ParseAddrPort(address)
		if err != nil || got.Addr().Unmap() != ap.Addr() || got.Port() != ap.Port() {
			return fmt.Errorf("%w: credentials dialer reached %q, want %s", errBlocked, address, ap)
		}
		return nil
	}}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(ap.Addr().String(), strconv.Itoa(int(ap.Port()))))
}

type limitedReadCloser struct {
	r io.Reader
	c io.Closer
}

func (l *limitedReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedReadCloser) Close() error               { return l.c.Close() }
