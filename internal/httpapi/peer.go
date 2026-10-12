package httpapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/bothyhq/bothy/internal/config"
)

type peerClass uint8

const (
	classPublic peerClass = iota
	classLAN
	classLocal
)

// reqInfo is computed once per request by the outer middleware.
type reqInfo struct {
	id     string
	ip     netip.Addr
	class  peerClass
	secure bool // TLS, or X-Forwarded-Proto https from a trusted proxy
}

type ctxKey struct{}

func withInfo(ctx context.Context, ri *reqInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, ri)
}

func infoOf(r *http.Request) *reqInfo {
	if ri, ok := r.Context().Value(ctxKey{}).(*reqInfo); ok {
		return ri
	}
	return &reqInfo{}
}

func clientIP(r *http.Request) string {
	if ip := infoOf(r).ip; ip.IsValid() {
		return ip.String()
	}
	return ""
}

// netCache holds the parsed network settings of the config snapshot it was
// built from; trusted_proxies and local_cidrs are restart-only, allowed_hosts
// and public_url can change at runtime.
type netCache struct {
	mu      sync.Mutex
	src     *config.Config
	trusted []netip.Prefix
	local   []netip.Prefix
	hosts   []hostEntry
}

type hostEntry struct{ host, port string }

func parsePrefixes(list []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return out
}

func (s *Server) netState() *netCache {
	c := s.cfg()
	nc := &s.net
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if nc.src != c {
		nc.src = c
		nc.trusted = parsePrefixes(c.Server.TrustedProxies)
		nc.local = parsePrefixes(c.Server.LocalCIDRs)
		nc.hosts = nc.hosts[:0]
		for _, h := range c.Server.AllowedHosts {
			if h = strings.TrimSpace(h); h != "" {
				nc.hosts = append(nc.hosts, splitHost(h))
			}
		}
		if u, err := url.Parse(c.Server.PublicURL); err == nil && u.Host != "" {
			nc.hosts = append(nc.hosts, splitHost(u.Host))
		}
	}
	return nc
}

func contains(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

// peer returns the client address and whether the request arrived over TLS.
// X-Forwarded-For and -Proto are honoured only when the TCP peer is a trusted
// proxy; the client is then the right-most hop that is not itself trusted.
func (s *Server) peer(r *http.Request) (netip.Addr, bool) {
	ip, _ := parseAddr(r.RemoteAddr)
	secure := r.TLS != nil
	nc := s.netState()
	if !ip.IsValid() || !contains(nc.trusted, ip) {
		return ip, secure
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := ip
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseAddr(hops[i])
		if !ok {
			break
		}
		client = a
		if !contains(nc.trusted, a) {
			break
		}
	}
	if protos := r.Header.Values("X-Forwarded-Proto"); len(protos) > 0 {
		parts := strings.Split(protos[len(protos)-1], ",")
		secure = secure || strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "https")
	}
	return client, secure
}

// classify: local = loopback, the container's default gateway (where
// Docker's published-port traffic arrives from) and local_cidrs; lan =
// RFC1918/ULA; public = everything else. The Host header plays no part.
func (s *Server) classify(ip netip.Addr) peerClass {
	switch {
	case !ip.IsValid():
		return classPublic
	case ip.IsLoopback(), contains(s.netState().local, ip), slices.Contains(s.gateways(), ip):
		return classLocal
	case ip.IsPrivate():
		return classLAN
	}
	return classPublic
}

func splitHost(hostport string) hostEntry {
	hostport = strings.ToLower(strings.TrimSpace(hostport))
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}
	return hostEntry{host: strings.TrimSuffix(strings.Trim(host, "[]"), "."), port: port}
}

// hostAllowed is the DNS-rebinding defence: Host must name this server.
// Entries without a port match any port.
func (s *Server) hostAllowed(host string) bool {
	h := splitHost(host)
	switch h.host {
	case "":
		return false
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	for _, e := range s.netState().hosts {
		if e.host == h.host && (e.port == "" || e.port == h.port) {
			return true
		}
	}
	return false
}

var (
	gatewayOnce sync.Once
	gatewayList []netip.Addr
)

// defaultGateways reads the default routes once (Linux only; elsewhere the
// files are absent and the list is empty).
func defaultGateways() []netip.Addr {
	gatewayOnce.Do(func() {
		gatewayList = append(readRoute4("/proc/net/route"), readRoute6("/proc/net/ipv6_route")...)
	})
	return gatewayList
}

func readRoute4(path string) []netip.Addr {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseRoute4(bufio.NewScanner(f))
}

func parseRoute4(sc *bufio.Scanner) []netip.Addr {
	var out []netip.Addr
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 4 || fs[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fs[3], 16, 32)
		if err != nil || flags&0x2 == 0 {
			continue
		}
		raw, err := hex.DecodeString(fs[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], binary.BigEndian.Uint32(raw))
		out = append(out, netip.AddrFrom4(b))
	}
	return out
}

func readRoute6(path string) []netip.Addr {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseRoute6(bufio.NewScanner(f))
}

func parseRoute6(sc *bufio.Scanner) []netip.Addr {
	var out []netip.Addr
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 5 || fs[0] != strings.Repeat("0", 32) || fs[1] != "00" {
			continue
		}
		raw, err := hex.DecodeString(fs[4])
		if err != nil || len(raw) != 16 {
			continue
		}
		a := netip.AddrFrom16([16]byte(raw))
		if !a.IsUnspecified() {
			out = append(out, a)
		}
	}
	return out
}
