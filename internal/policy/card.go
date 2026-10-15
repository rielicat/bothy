package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const maxCardDiff = 64 << 10

// renderCard builds the approval card from the Action alone: model prose never
// reaches it, so a convincing preamble cannot change what the user approves.
// Every string is passed through visible() so bidi overrides and zero-width
// characters show up instead of silently reordering or hiding text.
func renderCard(r core.ApprovalRequest) core.Card {
	title := r.Spec.Manifest.Title
	if title == "" {
		title = r.Spec.Def.Name
	}
	c := core.Card{
		Title:   visible(title),
		Context: visible(r.Context),
		Choices: slices.Clone(r.Decision.Choices),
		Kind:    core.CardAction,
		Tone:    core.ToneDefault,
	}
	for _, w := range r.Decision.Warnings {
		c.Warnings = append(c.Warnings, visible(w))
	}
	a := r.Action
	if a == nil {
		c.Question, c.PrimaryLabel, c.Ing = toolQuestion(r)
		return c
	}
	if c.Title == "" {
		c.Title = visible(a.Tool)
	}
	c.Risk = actionClass(a, r.Spec.Manifest)
	doc, _ := decodePayload(a.Payload)

	switch a.Kind {
	case core.ActExec:
		if code, ok := mustPointer(doc, "/code").(string); ok {
			c.Code = visible(code)
		} else {
			c.Code = visible(shellJoin(execArgv(a, doc)))
		}
	case core.ActFSWrite:
		// The diff stands for the content only when it is a complete text
		// diff; an empty, omitted, binary or truncated one would let a card
		// approve bytes nobody was shown.
		if !completeDiff(a.Diff, a.Paths) {
			if content, ok := mustPointer(doc, "/content").(string); ok {
				c.Code = visible(content)
			} else {
				c.Code = visible(prettyPayload(a.Payload))
			}
		}
	case core.ActHTTP:
		if _, idn := punycodeURL(a.URL); idn {
			c.Warnings = append(c.Warnings, "The address uses international characters. It's shown in a form that reveals look-alike letters.")
		}
	case core.ActFSRead, core.ActFSDelete, core.ActNet, core.ActRelease:
	default:
		c.Code = visible(prettyPayload(a.Payload))
	}
	applyPlain(&c, r, doc)
	c.Diff = visible(capDiff(a.Diff))
	return c
}

// completeDiff reports whether a tool's unified diff shows every changed
// line: it has hunks and neither the tool (64 KB) nor the card cut it. A
// path with a line break could forge hunk lines in the diff header.
func completeDiff(d string, paths []string) bool {
	for _, p := range paths {
		if strings.ContainsAny(p, "\r\n") || strings.ContainsFunc(p, hidden) {
			return false
		}
	}
	return len(d) <= maxCardDiff && strings.Contains(d, "\n@@ -") && !strings.Contains(d, "diff truncated")
}

// visible replaces control, format (bidi, zero-width), separator and filler
// characters with a visible ⟨U+XXXX⟩ token, keeping newlines and tabs.
func visible(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if !strings.ContainsFunc(s, hidden) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if hidden(r) {
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hidden(r rune) bool {
	switch r {
	case '\n', '\t':
		return false
	case '\u2028', '\u2029', '\u115F', '\u1160', '\u3164', '\uFFA0':
		return true
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r)
}

// shellJoin quotes argv so the line reads back exactly as the argv it shows.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for i := 0; i < len(s); i++ {
		if !shellSafe(s[i]) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func shellSafe(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_@%+=:,./-", c) >= 0
}

// punycodeURL rewrites only the URL's host into its ASCII form, keeping the
// rest verbatim; idn reports whether the host changed.
func punycodeURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, false
	}
	start := strings.Index(raw, "://")
	if start < 0 {
		return raw, false
	}
	start += 3
	end := len(raw)
	if i := strings.IndexAny(raw[start:], "/?#"); i >= 0 {
		end = start + i
	}
	ascii := normalizeHost(u.Hostname())
	host := ascii
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	authority := host
	if p := u.Port(); p != "" {
		authority = net.JoinHostPort(strings.Trim(host, "[]"), p)
	}
	if at := strings.LastIndex(raw[start:end], "@"); at >= 0 {
		authority = raw[start:start+at+1] + authority
	}
	out := raw[:start] + authority + raw[end:]
	return out, ascii != strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

func prettyPayload(p json.RawMessage) string {
	if len(bytes.TrimSpace(p)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, p, "", "  "); err != nil {
		return string(p)
	}
	return buf.String()
}

// capDiff keeps diffs at or under 64 KB, cutting at a line boundary.
func capDiff(d string) string {
	if len(d) <= maxCardDiff {
		return d
	}
	const marker = "\n… diff truncated …\n"
	cut := d[:maxCardDiff-len(marker)]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + marker
}
