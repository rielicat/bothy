package agent

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
)

var idEncoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// spotID derives the id of a spotlight block from the per-install key and the
// block's position, so history bytes stay identical across calls (prompt
// caching) while untrusted authors still cannot predict the closing tag.
func (r *Runner) spotID(parts ...string) string {
	m := hmac.New(sha256.New, r.spotKey)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return idEncoding.EncodeToString(m.Sum(nil)[:5])
}

// randomID is for spotlight blocks that are persisted already wrapped.
func randomID() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return idEncoding.EncodeToString(b)
}

// wrapUntrusted renders a spotlight block (SPEC §5.6).
func wrapUntrusted(id, content string, attrs ...string) string {
	var b strings.Builder
	b.WriteString("<untrusted")
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] == "" {
			continue
		}
		b.WriteString(" " + attrs[i] + `="` + attrValue(attrs[i+1]) + `"`)
	}
	b.WriteString(` id="` + id + `">` + "\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(`</untrusted id="` + id + `">`)
	return b.String()
}

// attrValue makes a short, single-line attribute value out of text an
// untrusted party may control (display names, hosts).
func attrValue(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == 64 {
			b.WriteString("…")
			break
		}
		switch {
		case r == '"':
			r = '\''
		case r == '<':
			r = '‹'
		case r == '>':
			r = '›'
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// escapeSection keeps embedded content from closing the S2 section it sits in.
func escapeSection(content, tag string) string {
	return strings.ReplaceAll(content, "</"+tag+">", "</ "+tag+">")
}

var (
	urlRE      = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s<>"'` + "`" + `]+`)
	wwwRE      = regexp.MustCompile(`(?i)\bwww\.[^\s<>"'` + "`" + `]+`)
	urlTrimSet = ".,;:!?)]}'\""
)

// extractURLs returns the http(s) URLs in user text, for URL provenance.
func extractURLs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range urlRE.FindAllString(text, -1) {
		u = strings.TrimRight(u, urlTrimSet)
		low := strings.ToLower(u)
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			continue
		}
		if len(u) > len("https://") && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// neutralizeLinks defangs links for exposed sessions: hxxps://evil[.]com/…
func neutralizeLinks(s string) string {
	s = urlRE.ReplaceAllStringFunc(s, func(u string) string {
		i := strings.Index(u, "://")
		scheme, rest := u[:i], u[i+3:]
		switch strings.ToLower(scheme) {
		case "http":
			scheme = "hxxp"
		case "https":
			scheme = "hxxps"
		default:
			scheme = "fxp"
		}
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		return scheme + "://" + strings.ReplaceAll(rest[:end], ".", "[.]") + rest[end:]
	})
	return wwwRE.ReplaceAllStringFunc(s, func(u string) string {
		end := strings.IndexAny(u, "/?#")
		if end < 0 {
			end = len(u)
		}
		return strings.ReplaceAll(u[:end], ".", "[.]") + u[end:]
	})
}

var standingRE = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:don'?t|do not|never)\b.*\buntil\b`),
	regexp.MustCompile(`(?i)\bnever\b`),
	regexp.MustCompile(`(?i)\balways\b`),
	regexp.MustCompile(`(?i)\bfrom now on\b`),
	regexp.MustCompile(`(?i)\bstop\s+\w+ing\b`),
	regexp.MustCompile(`(?i)\bonly\b.*\bif\b`),
}

var sentenceEnd = regexp.MustCompile(`[.!?]+(?:\s+|$)|\n+`)

// standingInstructions finds the user's standing instructions in text: the
// imperative patterns of SPEC §5.10. Questions are skipped.
func standingInstructions(text string) []string {
	var out []string
	idx := sentenceEnd.FindAllStringIndex(text, -1)
	start := 0
	emit := func(end, stop int) {
		sent := strings.TrimSpace(text[start:end])
		start = stop
		if sent == "" || utf8.RuneCountInString(sent) > 500 || strings.HasSuffix(sent, "?") {
			return
		}
		for _, re := range standingRE {
			if re.MatchString(sent) {
				out = append(out, sent)
				return
			}
		}
	}
	for _, m := range idx {
		end := m[0]
		if mark := strings.TrimRight(text[m[0]:m[1]], " \t\r\n"); mark != "" {
			end = m[0] + len(mark) // keep the sentence's own punctuation
		}
		emit(end, m[1])
	}
	if start < len(text) {
		emit(len(text), len(text))
	}
	return out
}

// headTail keeps the first 60% and last 40% of max bytes of s around note,
// cutting on rune boundaries.
func headTail(s string, maxBytes int, note string) string {
	if len(s) <= maxBytes {
		return s
	}
	head := maxBytes * 6 / 10
	tail := maxBytes - head
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	from := len(s) - tail
	for from < len(s) && !utf8.RuneStart(s[from]) {
		from++
	}
	return s[:head] + "\n" + note + "\n" + s[from:]
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func fnv64(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// nonZero keeps a result hash distinct from the "no result yet" marker of
// RunMeter.ObserveTool.
func nonZero(h uint64) uint64 {
	if h == 0 {
		return 1
	}
	return h
}

// actionDetail is the run.status detail for a tool: the command line for
// exec actions, else the URL, paths or tool title.
func actionDetail(spec core.ToolSpec, a *core.Action) string {
	if a != nil {
		switch {
		case len(a.Argv) == 3 && (a.Argv[1] == "-lc" || a.Argv[1] == "-c"):
			return "Running `" + truncateRunes(firstLine(a.Argv[2]), 120) + "`"
		case len(a.Argv) > 0:
			return "Running `" + truncateRunes(strings.Join(a.Argv, " "), 120) + "`"
		case a.URL != "":
			return truncateRunes(a.URL, 120)
		case len(a.Paths) > 0:
			return truncateRunes(strings.Join(a.Paths, ", "), 120)
		}
	}
	if spec.Manifest.Title != "" {
		return spec.Manifest.Title
	}
	return spec.Def.Name
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func plainText(parts []core.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == core.PartText {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
