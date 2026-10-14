package egress

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

type blockedPrefix struct {
	p netip.Prefix
	// private prefixes are admitted for system destinations with AllowPrivate.
	private bool
}

var blocklist = func() []blockedPrefix {
	always := []string{
		"0.0.0.0/8", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "ff00::/8", "100::/64", "2001:db8::/32", "fec0::/10", "3fff::/20",
	}
	private := []string{
		"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.168.0.0/16", "::1/128", "fc00::/7", "fe80::/10",
	}
	var out []blockedPrefix
	for _, s := range always {
		out = append(out, blockedPrefix{p: netip.MustParsePrefix(s)})
	}
	for _, s := range private {
		out = append(out, blockedPrefix{p: netip.MustParsePrefix(s), private: true})
	}
	return out
}()

// metadataAddrs stay blocked even with AllowPrivate. The ECS and EKS
// container credential endpoints are listed too: they hand out the task's
// role credentials to anything that can reach them, and only
// CredentialsClient may.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("fd00:ec2::254"),
	netip.MustParseAddr("100.100.100.200"),
	netip.MustParseAddr("169.254.170.2"),
	netip.MustParseAddr("169.254.170.23"),
	netip.MustParseAddr("fd00:ec2::23"),
}

var (
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour  = netip.MustParsePrefix("2002::/16")
	teredo     = netip.MustParsePrefix("2001::/32")
	v4Compat   = netip.MustParsePrefix("::/96")
	v4Trans    = netip.MustParsePrefix("::ffff:0:0:0/96")
)

// addrProblem returns why a may not be dialed, or "" when it may.
func addrProblem(a netip.Addr, allowPrivate bool) string {
	if !a.IsValid() {
		return "invalid address"
	}
	if a.Zone() != "" {
		return "scoped address"
	}
	a = a.Unmap()
	for _, m := range metadataAddrs {
		if a == m {
			return "cloud metadata address"
		}
	}
	for _, v4 := range embeddedV4(a) {
		if r := addrProblem(v4, allowPrivate); r != "" {
			return "embeds " + v4.String() + ": " + r
		}
	}
	for _, b := range blocklist {
		if b.p.Contains(a) && (!allowPrivate || !b.private) {
			return "address in blocked range " + b.p.String()
		}
	}
	return ""
}

// embeddedV4 extracts IPv4 addresses that IPv6 transition mechanisms would
// route to, so a NAT64, 6to4 or Teredo form cannot smuggle a blocked target.
func embeddedV4(a netip.Addr) []netip.Addr {
	if !a.Is6() {
		return nil
	}
	b := a.As16()
	v4 := func(x [4]byte) netip.Addr { return netip.AddrFrom4(x) }
	switch {
	case nat64.Contains(a), nat64Local.Contains(a), v4Trans.Contains(a):
		return []netip.Addr{v4([4]byte{b[12], b[13], b[14], b[15]})}
	case sixToFour.Contains(a):
		return []netip.Addr{v4([4]byte{b[2], b[3], b[4], b[5]})}
	case teredo.Contains(a):
		return []netip.Addr{
			v4([4]byte{b[4], b[5], b[6], b[7]}),
			v4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}),
		}
	case v4Compat.Contains(a) && a != netip.IPv6Unspecified() && a != netip.IPv6Loopback():
		return []netip.Addr{v4([4]byte{b[12], b[13], b[14], b[15]})}
	}
	return nil
}

var errInvalidHost = errors.New("invalid host")

// hostname is a normalized destination host: lowercase punycode for names,
// the unmapped address for IP literals.
type hostname struct {
	name string
	ip   netip.Addr
}

func (h hostname) isIP() bool { return h.ip.IsValid() }

func (h hostname) hostport(port int) string { return net.JoinHostPort(h.name, strconv.Itoa(port)) }

// urlHost is the host as written in a URL authority (IPv6 in brackets).
func (h hostname) urlHost() string {
	if h.ip.Is6() {
		return "[" + h.name + "]"
	}
	return h.name
}

// parseHost normalizes s (no port). Numeric forms that inet_aton-style
// resolvers accept ("2130706433", "0x7f000001", "017700000001", "127.1") are
// treated as the IPv4 literals they denote, never as names.
func parseHost(s string) (hostname, error) {
	if inner, ok := strings.CutPrefix(s, "["); ok {
		inner, ok = strings.CutSuffix(inner, "]")
		a, err := netip.ParseAddr(inner)
		if !ok || err != nil || !a.Is6() {
			return hostname{}, fmt.Errorf("%w %q", errInvalidHost, s)
		}
		s = inner
	}
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Zone() != "" {
			return hostname{}, fmt.Errorf("%w %q: scoped addresses are not allowed", errInvalidHost, s)
		}
		a = a.Unmap()
		return hostname{name: a.String(), ip: a}, nil
	}
	if s == "" || len(s) > 253 {
		return hostname{}, fmt.Errorf("%w %q", errInvalidHost, s)
	}
	ascii, err := idna.Lookup.ToASCII(s)
	if err != nil {
		return hostname{}, fmt.Errorf("%w %q: %w", errInvalidHost, s, err)
	}
	ascii = strings.TrimSuffix(strings.ToLower(ascii), ".")
	if a, numeric, err := parseLooseIPv4(ascii); numeric {
		if err != nil {
			return hostname{}, fmt.Errorf("%w %q: %w", errInvalidHost, s, err)
		}
		return hostname{name: a.String(), ip: a}, nil
	}
	if !validDNSName(ascii) {
		return hostname{}, fmt.Errorf("%w %q", errInvalidHost, s)
	}
	return hostname{name: ascii}, nil
}

func validDNSName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// parseLooseIPv4 follows the WHATWG URL host parser: a host whose last label
// is numeric is an IPv4 address in one to four decimal, octal (leading 0) or
// hex (0x) parts, or it is invalid. numeric reports whether that rule applied.
func parseLooseIPv4(s string) (a netip.Addr, numeric bool, err error) {
	parts := strings.Split(s, ".")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if !numericLabel(parts[len(parts)-1]) {
		return netip.Addr{}, false, nil
	}
	if len(parts) > 4 {
		return netip.Addr{}, true, errors.New("too many IPv4 parts")
	}
	vals := make([]uint64, len(parts))
	for i, p := range parts {
		v, err := parseIPv4Part(p)
		if err != nil {
			return netip.Addr{}, true, err
		}
		vals[i] = v
	}
	for _, v := range vals[:len(vals)-1] {
		if v > 255 {
			return netip.Addr{}, true, errors.New("IPv4 part out of range")
		}
	}
	last := vals[len(vals)-1]
	if last >= 1<<(8*(5-len(vals))) {
		return netip.Addr{}, true, errors.New("IPv4 part out of range")
	}
	n := last
	for i, v := range vals[:len(vals)-1] {
		n |= v << (8 * (3 - i))
	}
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), true, nil
}

func numericLabel(l string) bool {
	if l == "" {
		return false
	}
	if rest, ok := cutHexPrefix(l); ok {
		for i := 0; i < len(rest); i++ {
			if !isHex(rest[i]) {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(l); i++ {
		if l[i] < '0' || l[i] > '9' {
			return false
		}
	}
	return true
}

func parseIPv4Part(p string) (uint64, error) {
	if p == "" {
		return 0, errors.New("empty IPv4 part")
	}
	base, digits := 10, p
	if rest, ok := cutHexPrefix(p); ok {
		base, digits = 16, rest
		if digits == "" {
			return 0, nil
		}
	} else if len(p) > 1 && p[0] == '0' {
		base, digits = 8, p[1:]
	}
	v, err := strconv.ParseUint(digits, base, 32)
	if err != nil {
		return 0, fmt.Errorf("bad IPv4 part %q", p)
	}
	return v, nil
}

func cutHexPrefix(s string) (string, bool) {
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		return s[2:], true
	}
	return s, false
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// parsePort accepts only plain decimal 1..65535.
func parsePort(s string) (int, error) {
	if s == "" || len(s) > 5 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("invalid port %q", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return n, nil
}

// splitHostPort parses "host:port" (IPv6 in brackets) into a normalized host.
func splitHostPort(s string) (hostname, int, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return hostname{}, 0, fmt.Errorf("%w %q: %w", errInvalidHost, s, err)
	}
	port, err := parsePort(p)
	if err != nil {
		return hostname{}, 0, err
	}
	host, err := parseHost(h)
	return host, port, err
}
