package policy

import (
	"bytes"
	"encoding/json"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/bothyhq/bothy/internal/core"
)

// globMatch uses path.Match syntax, except that '*' and '?' also match '/'
// and newlines: rule and grant globs apply to command lines, URLs and other
// payload values, not to path segments. A malformed pattern never matches.
func globMatch(pattern, s string) bool {
	px, sx := 0, 0
	starP, starS := -1, 0
	for px < len(pattern) || sx < len(s) {
		if px < len(pattern) {
			if pattern[px] == '*' {
				starP, starS = px, sx
				px++
				continue
			}
			if sx < len(s) {
				if pn, sn, ok := matchOne(pattern[px:], s[sx:]); ok {
					px, sx = px+pn, sx+sn
					continue
				}
			}
		}
		if starP < 0 || starS >= len(s) {
			return false
		}
		_, w := utf8.DecodeRuneInString(s[starS:])
		starS += w
		px, sx = starP+1, starS
	}
	return true
}

// matchOne matches one pattern element (a literal, '?', an escape or a class)
// against the first rune of s.
func matchOne(p, s string) (pn, sn int, ok bool) {
	r, sw := utf8.DecodeRuneInString(s)
	switch p[0] {
	case '?':
		return 1, sw, true
	case '[':
		n, hit := matchClass(p, r)
		return n, sw, n > 0 && hit
	case '\\':
		if len(p) < 2 {
			return 0, 0, false
		}
		pr, pw := utf8.DecodeRuneInString(p[1:])
		return 1 + pw, sw, pr == r
	}
	pr, pw := utf8.DecodeRuneInString(p)
	return pw, sw, pr == r
}

// matchClass parses the class at the start of p ("[...]", "[^...]") and
// reports its length (0 when malformed) and whether r is in it.
func matchClass(p string, r rune) (int, bool) {
	i := 1
	negate := i < len(p) && p[i] == '^'
	if negate {
		i++
	}
	hit, first := false, true
	for {
		if i >= len(p) {
			return 0, false
		}
		if p[i] == ']' && !first {
			return i + 1, hit != negate
		}
		first = false
		lo, n := classChar(p[i:])
		if n == 0 {
			return 0, false
		}
		i += n
		hi := lo
		if i+1 < len(p) && p[i] == '-' && p[i+1] != ']' {
			hi, n = classChar(p[i+1:])
			if n == 0 {
				return 0, false
			}
			i += 1 + n
		}
		if lo <= r && r <= hi {
			hit = true
		}
	}
}

func classChar(p string) (rune, int) {
	if p[0] == '\\' {
		if len(p) < 2 {
			return 0, 0
		}
		r, w := utf8.DecodeRuneInString(p[1:])
		return r, 1 + w
	}
	r, w := utf8.DecodeRuneInString(p)
	return r, w
}

// validGlob reports whether g parses as a glob (path.Match syntax).
func validGlob(g string) bool {
	_, err := path.Match(g, "")
	return err == nil
}

// globEscape quotes s so that globMatch matches it literally.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hasGlobMeta(s string) bool { return strings.ContainsAny(s, `*?[\`) }

// trivialGlob reports whether g matches every string.
func trivialGlob(g string) bool { return strings.Trim(g, "*") == "" }

// pathGlob matches workspace-relative paths segment by segment: '*' stays
// within a segment and a "**" segment spans any number of segments.
func pathGlob(pattern, name string) bool {
	return matchSegments(strings.Split(cleanRel(pattern), "/"), strings.Split(cleanRel(name), "/"))
}

func matchSegments(ps, ns []string) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			for i := 0; i <= len(ns); i++ {
				if matchSegments(ps[1:], ns[i:]) {
					return true
				}
			}
			return false
		}
		if len(ns) == 0 {
			return false
		}
		if ok, err := path.Match(ps[0], ns[0]); err != nil || !ok {
			return false
		}
		ps, ns = ps[1:], ns[1:]
	}
	return len(ns) == 0
}

func cleanRel(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

// hostPort is a normalised network destination; port 0 means unknown.
type hostPort struct {
	host string
	port int
}

func (h hostPort) String() string {
	if h.port == 0 {
		return h.host
	}
	return net.JoinHostPort(h.host, strconv.Itoa(h.port))
}

// normalizeHost lowercases, drops a trailing dot and IPv6 brackets, and
// converts IDNs to punycode so homographs compare by their ASCII form.
func normalizeHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if h == "" || net.ParseIP(h) != nil {
		return h
	}
	if a, err := idna.Lookup.ToASCII(h); err == nil {
		return a
	}
	return h
}

// splitHostPort accepts "host", "host:port" and "[v6]:port".
func splitHostPort(s string) (hostPort, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return hostPort{}, false
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return hostPort{}, false
		}
		return hostPort{host: normalizeHost(h), port: n}, h != ""
	}
	if strings.Count(s, ":") == 1 && !strings.HasPrefix(s, "[") {
		return hostPort{}, false
	}
	return hostPort{host: normalizeHost(s)}, true
}

// hostPattern is "host", "host:port", "*.suffix" or "*.suffix:port"; a
// wildcard matches subdomains only, never the bare suffix.
type hostPattern struct {
	hostPort
	wildcard bool
}

func parseHostPattern(s string) (hostPattern, bool) {
	wild := strings.HasPrefix(strings.TrimSpace(s), "*.")
	if wild {
		s = strings.TrimPrefix(strings.TrimSpace(s), "*.")
	}
	hp, ok := splitHostPort(s)
	if !ok || hp.host == "" || strings.ContainsAny(hp.host, "*?[") {
		return hostPattern{}, false
	}
	return hostPattern{hostPort: hp, wildcard: wild}, true
}

func (p hostPattern) match(h hostPort) bool {
	if p.port != 0 && p.port != h.port {
		return false
	}
	if p.wildcard {
		return len(h.host) > len(p.host)+1 && strings.HasSuffix(h.host, "."+p.host)
	}
	return h.host == p.host
}

func (p hostPattern) String() string {
	if p.wildcard {
		return "*." + p.hostPort.String()
	}
	return p.hostPort.String()
}

// underSuffix reports whether host is below any of the suffixes (".github.io").
func underSuffix(host string, suffixes []string) bool {
	for _, s := range suffixes {
		s = normalizeHost(strings.TrimPrefix(strings.TrimSpace(s), "*"))
		if s == "" {
			continue
		}
		if !strings.HasPrefix(s, ".") {
			s = "." + s
		}
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// urlHostPort extracts the destination of an absolute http(s) URL.
func urlHostPort(raw string) (hostPort, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return hostPort{}, false
	}
	hp := hostPort{host: normalizeHost(u.Hostname())}
	switch {
	case u.Port() != "":
		n, err := strconv.Atoi(u.Port())
		if err != nil || n < 1 || n > 65535 {
			return hostPort{}, false
		}
		hp.port = n
	case strings.EqualFold(u.Scheme, "https"):
		hp.port = 443
	case strings.EqualFold(u.Scheme, "http"):
		hp.port = 80
	}
	return hp, true
}

// decodePayload parses an Action payload keeping numbers verbatim.
func decodePayload(raw json.RawMessage) (any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// pointer resolves an RFC 6901 JSON pointer.
func pointer(doc any, ptr string) (any, bool) {
	if ptr == "" {
		return doc, true
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, false
	}
	cur := doc
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[tok]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(v) || strconv.Itoa(i) != tok {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// pointerText is the string a glob is matched against: strings as they are,
// everything else as canonical JSON.
func pointerText(doc any, ptr string) (string, bool) {
	v, ok := pointer(doc, ptr)
	if !ok {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	b, err := core.CanonicalJSON(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// pointersTo returns the pointers of every string in doc equal to want.
func pointersTo(doc any, want string) []string {
	var out []string
	var walk func(v any, at string)
	walk = func(v any, at string) {
		switch x := v.(type) {
		case string:
			if x == want {
				out = append(out, at)
			}
		case map[string]any:
			for k, e := range x {
				walk(e, at+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"))
			}
		case []any:
			for i, e := range x {
				walk(e, at+"/"+strconv.Itoa(i))
			}
		}
	}
	walk(doc, "")
	return out
}
