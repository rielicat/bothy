package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	dialTimeout    = 10 * time.Second
	maxDNSTTL      = 60 * time.Second
	maxDNSCacheLen = 1024
)

var (
	// errBlocked means the destination address is never dialable.
	errBlocked = errors.New("destination address blocked")
	// errDenied means the destination is not on the dialer's allowlist.
	errDenied = errors.New("destination not allowed")
)

// resolver is satisfied by *net.Resolver.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type dnsEntry struct {
	addrs []netip.Addr
	exp   time.Time
}

type dnsCache struct {
	mu      sync.Mutex
	entries map[string]dnsEntry
	key     string // servers the cached resolver was built for
	res     *net.Resolver
}

// systemResolver returns a pure-Go resolver for the configured servers ("" =
// the host's resolv.conf). It is rebuilt only when the server list changes.
func (c *dnsCache) systemResolver(servers []string) *net.Resolver {
	key := strings.Join(servers, ",")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.res != nil && c.key == key {
		return c.res
	}
	r := &net.Resolver{PreferGo: true}
	if len(servers) > 0 {
		addrs := make([]string, len(servers))
		for i, s := range servers {
			if a, err := netip.ParseAddr(s); err == nil {
				addrs[i] = netip.AddrPortFrom(a, 53).String()
			} else {
				addrs[i] = s
			}
		}
		var d net.Dialer
		r.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			var errs []error
			for _, a := range addrs {
				c, err := d.DialContext(ctx, network, a)
				if err == nil {
					return c, nil
				}
				errs = append(errs, err)
			}
			return nil, errors.Join(errs...)
		}
	}
	c.key, c.res = key, r
	c.entries = nil
	return r
}

func (c *dnsCache) get(host string, now time.Time) ([]netip.Addr, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[host]
	if !ok || !now.Before(e.exp) {
		return nil, false
	}
	return e.addrs, true
}

func (c *dnsCache) put(host string, addrs []netip.Addr, now, exp time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]dnsEntry{}
	}
	if len(c.entries) >= maxDNSCacheLen {
		for k, e := range c.entries {
			if !now.Before(e.exp) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < maxDNSCacheLen/2 {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[host] = dnsEntry{addrs: addrs, exp: exp}
}

// resolve looks host up with the gateway resolver. Go's resolver does not
// expose record TTLs, so answers are cached for egress.dns.max_ttl (≤ 60s).
func (s *Service) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	dns := s.cfg().Egress.DNS
	ttl := min(dns.MaxTTL.D(), maxDNSTTL)
	now := s.clock.Now()
	if ttl > 0 {
		if addrs, ok := s.dns.get(host, now); ok {
			return addrs, nil
		}
	}
	r := s.resolver
	if r == nil {
		r = s.dns.systemResolver(dns.Servers)
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	if ttl > 0 {
		s.dns.put(host, addrs, now, now.Add(ttl))
	}
	return addrs, nil
}

// pinned is the outcome of dialPinned. A non-empty reason means the
// destination was refused by policy; err alone is a network failure.
type pinned struct {
	conn   net.Conn
	ip     netip.Addr
	reason string
	err    error
}

// dialPinned refuses a denylisted host, resolves h once, refuses the
// destination if any answer is blocked or denylisted, and dials only address
// literals, so a second DNS answer can never redirect the connection
// (rebinding). egress.deny applies to every dialer.
func (s *Service) dialPinned(ctx context.Context, h hostname, port int, allowPrivate bool) pinned {
	deny := s.cfg().Egress.Deny
	if e, ok := matchAny(deny, h, port, true, true); ok {
		return pinned{ip: h.ip, reason: "denylisted (" + e + ")", err: fmt.Errorf("%w: %s is denylisted", errDenied, h.hostport(port))}
	}
	addrs := []netip.Addr{h.ip}
	if !h.isIP() {
		var err error
		if addrs, err = s.resolve(ctx, h.name); err != nil {
			return pinned{err: fmt.Errorf("resolve %s: %w", h.name, err)}
		}
	}
	for _, a := range addrs {
		if r := addrProblem(a, allowPrivate); r != "" {
			return pinned{ip: a.Unmap(), reason: r, err: fmt.Errorf("%w: %s resolves to %s (%s)", errBlocked, h.name, a.Unmap(), r)}
		}
		lit := hostname{name: a.Unmap().String(), ip: a.Unmap()}
		if e, ok := matchAny(deny, lit, port, true, true); ok {
			return pinned{ip: lit.ip, reason: "denylisted (" + e + ")", err: fmt.Errorf("%w: %s is denylisted", errDenied, lit.name)}
		}
	}
	var errs []error
	for _, a := range addrs {
		a = a.Unmap()
		c, err := s.dialAddr(ctx, "tcp", netip.AddrPortFrom(a, uint16(port)).String(), allowPrivate)
		if err == nil {
			return pinned{conn: c, ip: a}
		}
		if errors.Is(err, errBlocked) {
			return pinned{ip: a, reason: "blocked at connect", err: err}
		}
		errs = append(errs, err)
	}
	return pinned{ip: addrs[0].Unmap(), err: errors.Join(errs...)}
}

// netDial is the production dialer. Control re-checks the final socket
// address, so nothing reaches a blocked range even if a caller slipped one in.
func netDial(ctx context.Context, network, addr string, allowPrivate bool) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second, Control: controlFor(allowPrivate)}
	return d.DialContext(ctx, network, addr)
}

func controlFor(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: unparseable dial address %q", errBlocked, address)
		}
		if r := addrProblem(ap.Addr(), allowPrivate); r != "" {
			return fmt.Errorf("%w: %s", errBlocked, r)
		}
		return nil
	}
}

// trackedConn counts bytes and reports them once on Close. up is what we
// sent to the destination, down what we received from it.
type trackedConn struct {
	net.Conn
	rmu, wmu sync.Mutex
	up, down int64
	once     sync.Once
	onClose  func(up, down int64)
}

func (c *trackedConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	n, err := c.Conn.Read(p)
	c.down += int64(n)
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	n, err := c.Conn.Write(p)
	c.up += int64(n)
	return n, err
}

// Close reports only after in-flight Read/Write calls have added their bytes:
// the transport's write goroutine can still be between Write returning and
// counting when the response has already been read and the conn closed.
// Closing the underlying conn first unblocks them.
func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.rmu.Lock()
		c.wmu.Lock()
		up, down := c.up, c.down
		c.wmu.Unlock()
		c.rmu.Unlock()
		c.onClose(up, down)
	})
	return err
}
