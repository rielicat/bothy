package core

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Link neutralization for Exposed sessions (SPEC §5.3), shared by the
// channel router and the agent's run sink (which also feeds /v1), so the two
// surfaces cannot drift apart.

var (
	mdLinkRe   = regexp.MustCompile(`\[([^\]\n]*)\]\(([^)\s]+)\)`)
	autoLinkRe = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9+.-]*:[^\s<>]+)>`)
	// Every "scheme://" matches, even with nothing after it, and a bracketed
	// IPv6 host ("https://[2001:db8::1]/…") is part of the URL; it must hold
	// a colon, so a defanged "[.]" is never taken for one. No \b:
	// "x_https://…" has no word boundary before "https", yet clients still
	// find the URL in it.
	schemeURLRe = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://(?:\[[0-9a-z.%]*:[0-9a-z:.%]*\])?[^\s<>"'()\[\]]*`)
	// Chat clients and markdown renderers turn bare domains
	// ("attacker.com/?q=…") into links, so anything that looks like host.tld
	// is defanged too: internationalized names and the dot look-alikes that
	// IDNA or NFKC map to "." included.
	bareHostRe = regexp.MustCompile(`(?:[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?[.。．｡․﹒])+\p{L}[\p{L}\p{N}-]*[\p{L}\p{N}]`)
	// More than four parts too: a match ending inside a longer run would
	// leave the rest of it a live address.
	bareIPv4Re = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3,}`)
	hostDotRe  = regexp.MustCompile(`[.。．｡․﹒]`)
	// schemeStartRe finds a "scheme://" left in rendered text; the defanged
	// schemes are skipped by the caller.
	schemeStartRe = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://`)
)

// LooksLikeLink reports whether a chat client could turn part of s into a
// link: a "scheme://" URL, a bare host name or an IPv4 address.
func LooksLikeLink(s string) bool {
	return schemeURLRe.MatchString(s) || bareHostRe.MatchString(s) || bareIPv4Re.MatchString(s)
}

// NeutralizeLinks defangs every link in s, code included
// ("https://evil.com/x" becomes "hxxps://evil[.]com/x"), so nothing in it is
// clickable. It is idempotent.
func NeutralizeLinks(s string) string {
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRe.FindStringSubmatch(m)
		return sub[1] + " (" + defangURL(sub[2]) + ")"
	})
	s = autoLinkRe.ReplaceAllStringFunc(s, func(m string) string { return defangURL(m[1 : len(m)-1]) })
	// Unwrapping a nested autolink ("<a:<b:c>>") can complete the outer one;
	// breaking its scheme colon leaves no autolink without another pass.
	s = autoLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		i := strings.IndexByte(m, ':')
		return m[:i] + "[:]" + m[i+1:]
	})
	s = schemeURLRe.ReplaceAllStringFunc(s, defangURL)
	s = bareHostRe.ReplaceAllStringFunc(s, func(h string) string { return hostDotRe.ReplaceAllString(h, "[.]") })
	s = bareIPv4Re.ReplaceAllStringFunc(s, func(ip string) string { return strings.ReplaceAll(ip, ".", "[.]") })
	s = defangRendered(s)
	// mdLinkRe misses link text with nested brackets; no "](" left means no
	// renderer can still form a link.
	return strings.ReplaceAll(s, "](", "] (")
}

// NeutralizeMarkdown defangs the links in md's prose and leaves fenced code
// blocks and inline code spans untouched, so code keeps its dots
// ("os.path").
func NeutralizeMarkdown(md string) string { return NewLinkFilter()(md) }

// NewLinkFilter returns NeutralizeMarkdown for a stream: each call takes the
// next chunk and returns it neutralized. Chunks must end at whitespace so no
// link is split; the fenced-block state and the start of the current line
// carry over from one chunk to the next.
func NewLinkFilter() func(chunk string) string {
	f := &proseMapper{fn: NeutralizeLinks}
	return f.apply
}

// proseMapper applies fn to the prose of streamed markdown: text outside
// fenced code blocks and inline code spans.
type proseMapper struct {
	fn     func(string) string
	open   bool
	marker string
	line   string
}

func (f *proseMapper) apply(chunk string) string {
	var b strings.Builder
	for chunk != "" {
		part, rest, eol := chunk, "", false
		if i := strings.IndexByte(chunk, '\n'); i >= 0 {
			part, rest, eol = chunk[:i+1], chunk[i+1:], true
		}
		full := f.line + strings.TrimSuffix(part, "\n")
		t := fenceCandidate(full)
		switch {
		case f.open:
			b.WriteString(part)
			if eol && closesFence(t, f.marker) {
				f.open, f.marker = false, ""
			}
		case openingFence(t) != "":
			b.WriteString(part)
			if eol {
				f.open, f.marker = true, openingFence(t)
			}
		default:
			b.WriteString(mapOutsideCodeSpans(part, f.fn))
		}
		f.line = full
		if eol {
			f.line = ""
		}
		chunk = rest
	}
	return b.String()
}

func defangURL(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		scheme, rest, ok = strings.Cut(u, ":")
		if !ok {
			return strings.ReplaceAll(u, ".", "[.]")
		}
		return scheme + "[:]" + strings.ReplaceAll(rest, ".", "[.]")
	}
	// A URL nested in this one ("?next=https://…") must not stay live either.
	rest = strings.ReplaceAll(rest, "://", "[:]//")
	switch strings.ToLower(scheme) {
	case "http":
		scheme = "hxxp"
	case "https":
		scheme = "hxxps"
	case "ftp":
		scheme = "fxp"
	case "hxxp", "hxxps", "fxp": // defanged by an earlier pass
	default:
		// tg://, intent:// and the like have no dotted host to break, but
		// clients still open them; breaking the "://" makes them inert.
		return scheme + "[:]//" + strings.ReplaceAll(rest, ".", "[.]")
	}
	host, path, _ := strings.Cut(rest, "/")
	out := scheme + "://" + strings.ReplaceAll(host, ".", "[.]")
	if path != "" || strings.HasSuffix(rest, "/") {
		out += "/" + path
	}
	return out
}

// defangRendered breaks links that only form once the markdown is rendered:
// a backslash escape ("evil\.com", "https\://"), emphasis markers around a
// dot ("evil**.**com") or invisible format characters ("evil​.com")
// disappear on the way to the client, which then finds "evil.com". It
// matches on the text as rendered and defangs the dots and scheme colons in
// the source.
func defangRendered(text string) string {
	var vis strings.Builder
	var at []int // source offset of every byte of vis
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		hidden := r == '*' || r == '_' || r == '~' || unicode.Is(unicode.Cf, r) ||
			r == '\\' && i+1 < len(text) && isASCIIPunct(text[i+1])
		if !hidden {
			vis.WriteString(text[i : i+n])
			for range n {
				at = append(at, i)
			}
		}
		i += n
	}
	v := vis.String()
	if len(v) == len(text) {
		return text
	}
	fix := map[int]string{} // source offset of a rune -> its replacement
	for _, m := range bareHostRe.FindAllStringIndex(v, -1) {
		for _, d := range hostDotRe.FindAllStringIndex(v[m[0]:m[1]], -1) {
			fix[at[m[0]+d[0]]] = "[.]"
		}
	}
	for _, m := range bareIPv4Re.FindAllStringIndex(v, -1) {
		for j := m[0]; j < m[1]; j++ {
			if v[j] == '.' {
				fix[at[j]] = "[.]"
			}
		}
	}
	for _, m := range schemeStartRe.FindAllStringIndex(v, -1) {
		colon := m[1] - 3
		switch strings.ToLower(v[m[0]:colon]) {
		case "hxxp", "hxxps", "fxp":
		default:
			fix[at[colon]] = "[:]"
		}
	}
	if len(fix) == 0 {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		_, n := utf8.DecodeRuneInString(text[i:])
		if rep, ok := fix[i]; ok {
			b.WriteString(rep)
		} else {
			b.WriteString(text[i : i+n])
		}
		i += n
	}
	return b.String()
}

func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// fenceCandidate strips the up to three spaces a fence line may be indented
// by; a line indented further is never a fence (CommonMark), so it yields "".
func fenceCandidate(l string) string {
	t := strings.TrimLeft(l, " ")
	if len(l)-len(t) > 3 {
		return ""
	}
	return t
}

func fenceRun(t string) string {
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return ""
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	return t[:n]
}

// openingFence returns the marker when t opens a fenced code block
// (CommonMark: three or more backticks or tildes; a backtick info string
// cannot contain backticks).
func openingFence(t string) string {
	m := fenceRun(t)
	if len(m) < 3 || m[0] == '`' && strings.Contains(t[len(m):], "`") {
		return ""
	}
	return m
}

func closesFence(t, marker string) bool {
	t = strings.TrimRight(t, " \t")
	return marker != "" && len(t) >= len(marker) && strings.Trim(t, marker[:1]) == ""
}

// mapOutsideCodeSpans applies fn to the text of line outside inline code
// spans; an unclosed backtick run counts as text.
func mapOutsideCodeSpans(line string, fn func(string) string) string {
	var b strings.Builder
	for line != "" {
		i := strings.IndexByte(line, '`')
		if i < 0 {
			b.WriteString(fn(line))
			break
		}
		b.WriteString(fn(line[:i]))
		run := fenceRun(line[i:])
		closeAt := findRun(line[i+len(run):], len(run))
		if closeAt < 0 {
			b.WriteString(run)
			line = line[i+len(run):]
			continue
		}
		end := i + len(run) + closeAt + len(run)
		b.WriteString(line[i:end])
		line = line[end:]
	}
	return b.String()
}

// findRun returns the index of the first run of exactly n backticks in s.
func findRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return i
		}
		i = j
	}
	return -1
}
