package egress

import (
	"net"
	"slices"
	"strings"

	"github.com/bothyhq/bothy/internal/config"
)

var (
	presetPython = []string{"pypi.org:443", "files.pythonhosted.org:443"}
	presetNPM    = []string{"registry.npmjs.org:443"}
)

func presetHosts(p config.EgressPresets) []string {
	var out []string
	if p.PythonPackages {
		out = append(out, presetPython...)
	}
	if p.NPMPackages {
		out = append(out, presetNPM...)
	}
	return out
}

// pattern is a parsed allow/deny/grant entry: "host", "host:port",
// "*.suffix[:port]", "ip:port".
type pattern struct {
	host     hostname
	suffix   string // set for "*.suffix" entries
	port     int    // 0 = any allowed port
	wildcard bool
}

func parsePattern(s string) (pattern, bool) {
	hostPart, port := s, 0
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := parsePort(p)
		if err != nil {
			return pattern{}, false
		}
		hostPart, port = h, n
	}
	if rest, ok := strings.CutPrefix(hostPart, "*."); ok {
		h, err := parseHost(rest)
		if err != nil || h.isIP() {
			return pattern{}, false
		}
		return pattern{suffix: h.name, port: port, wildcard: true}, true
	}
	h, err := parseHost(hostPart)
	if err != nil {
		return pattern{}, false
	}
	return pattern{host: h, port: port}, true
}

// matches reports whether the pattern admits host:port. IP literal targets
// match only an exact ip:port entry unless loose is set (deny lists).
func (p pattern) matches(h hostname, port int, loose bool) bool {
	if p.port != 0 && p.port != port {
		return false
	}
	if p.wildcard {
		return !h.isIP() && strings.HasSuffix(h.name, "."+p.suffix)
	}
	if h.isIP() != p.host.isIP() {
		return false
	}
	if h.isIP() && p.port == 0 && !loose {
		return false
	}
	return p.host.name == h.name
}

func matchAny(entries []string, h hostname, port int, allowWildcard, loose bool) (string, bool) {
	for _, e := range entries {
		p, ok := parsePattern(e)
		if !ok || (p.wildcard && !allowWildcard) {
			continue
		}
		if p.matches(h, port, loose) {
			return e, true
		}
	}
	return "", false
}

// sharedTenant reports whether host lives under a suffix where anyone can
// publish content (S3 buckets, workers.dev, github.io pages).
func sharedTenant(h hostname, suffixes []string) bool {
	if h.isIP() {
		return false
	}
	for _, s := range suffixes {
		s = strings.Trim(strings.ToLower(s), ".")
		if s != "" && (h.name == s || strings.HasSuffix(h.name, "."+s)) {
			return true
		}
	}
	return false
}

// portAllowed applies egress.ports, with port 80 reserved for allow_plain_http.
func portAllowed(c *config.EgressCfg, port int) bool {
	if port == 80 {
		return c.AllowPlainHTTP
	}
	return slices.Contains(c.Ports, port)
}
