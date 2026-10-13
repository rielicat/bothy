package slack

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// render converts Bothy markdown to Slack mrkdwn: **bold** becomes *bold*,
// *italic* becomes _italic_, [text](url) becomes <url|text>, headings become
// bold lines and list bullets "•". Every &, < and > is escaped, which is
// what keeps model text from forming mentions (<!channel>, <@U…>) or links
// whose label hides their target. mrkdwn has no other escape, so stray
// formatting characters are left as they are.

var (
	headingRe = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]+(.+?)[ \t#]*$`)
	bulletRe  = regexp.MustCompile(`^( *)[-*+] `)
	escaper   = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
)

func escape(s string) string { return escaper.Replace(s) }

func render(md string) string {
	var b strings.Builder
	b.Grow(len(md) + len(md)/16)
	fence := false
	for i, line := range strings.Split(md, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if rest := strings.TrimLeft(line, " "); isFence(rest, fence) {
			// mrkdwn fences take no info string ("```go" would print "go").
			fence = !fence
			b.WriteString("```")
			if !fence {
				b.WriteString(escape(strings.TrimLeft(rest[3:], "`")))
			}
			continue
		}
		if fence {
			b.WriteString(escape(line))
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			b.WriteByte('*')
			writeInline(&b, m[1], true)
			b.WriteByte('*')
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			b.WriteString(m[1] + "• ")
			line = line[len(m[0]):]
		}
		writeInline(&b, line, false)
	}
	return b.String()
}

// isFence reports whether a line opens or closes a fenced block; "```x```"
// on one line is inline code.
func isFence(line string, inFence bool) bool {
	return strings.HasPrefix(line, "```") && (inFence || !strings.Contains(line[3:], "```"))
}

// writeInline converts one line. inBold drops nested bold markers, which
// mrkdwn cannot nest.
func writeInline(b *strings.Builder, s string, inBold bool) {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			_, n := utf8.DecodeRuneInString(s[i+1:])
			b.WriteString(escape(s[i+1 : i+1+n]))
			i += 1 + n
		case c == '`':
			n := runLen(s[i:], '`')
			if end := closingRun(s[i+n:], n); end >= 0 {
				b.WriteByte('`')
				b.WriteString(escape(strings.ReplaceAll(s[i+n:i+n+end], "`", "'")))
				b.WriteByte('`')
				i += n + end + n
			} else {
				b.WriteString(s[i : i+n])
				i += n
			}
		case strings.HasPrefix(s[i:], "**"):
			end := strings.Index(s[i+2:], "**")
			if end <= 0 {
				b.WriteString("**")
				i += 2
				continue
			}
			if !inBold {
				b.WriteByte('*')
			}
			writeInline(b, s[i+2:i+2+end], true)
			if !inBold {
				b.WriteByte('*')
			}
			i += 2 + end + 2
		case c == '*':
			if end := italicEnd(s, i); end > 0 {
				b.WriteByte('_')
				writeInline(b, s[i+1:end], inBold)
				b.WriteByte('_')
				i = end + 1
			} else {
				b.WriteByte('*')
				i++
			}
		case c == '[':
			text, target, end, ok := parseLink(s, i)
			if !ok {
				b.WriteByte('[')
				i++
				continue
			}
			if isWebURL(target) {
				b.WriteByte('<')
				b.WriteString(linkTarget(target))
				b.WriteByte('|')
				b.WriteString(escape(strings.ReplaceAll(text, "|", "¦")))
				b.WriteByte('>')
			} else {
				writeInline(b, text, inBold)
				b.WriteString(" (")
				b.WriteString(escape(target))
				b.WriteByte(')')
			}
			i = end
		default:
			_, n := utf8.DecodeRuneInString(s[i:])
			b.WriteString(escape(s[i : i+n]))
			i += n
		}
	}
}

// italicEnd returns the index of the "*" closing an italic span opened at
// s[i], or -1: the opener must be followed, and the closer preceded, by a
// non-space, as in markdown.
func italicEnd(s string, i int) int {
	if i+1 >= len(s) || s[i+1] == '*' || unicode.IsSpace(rune(s[i+1])) {
		return -1
	}
	for j := i + 2; j < len(s); j++ {
		if s[j] == '*' && !unicode.IsSpace(rune(s[j-1])) && (j+1 >= len(s) || s[j+1] != '*') {
			return j
		}
	}
	return -1
}

// linkTarget makes a URL safe inside <…|…>: the delimiters are
// percent-encoded and & escaped as mrkdwn requires.
func linkTarget(u string) string {
	return strings.NewReplacer("&", "&amp;", "<", "%3C", ">", "%3E", "|", "%7C").Replace(u)
}

func runLen(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// closingRun returns the offset of the first run of exactly n backticks in s.
func closingRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		r := runLen(s[i:], '`')
		if r == n {
			return i
		}
		i += r
	}
	return -1
}

// parseLink parses "[text](target)" at s[i]; end is the index after it.
func parseLink(s string, i int) (text, target string, end int, ok bool) {
	closeText := strings.IndexByte(s[i:], ']')
	if closeText < 0 || i+closeText+1 >= len(s) || s[i+closeText+1] != '(' {
		return "", "", 0, false
	}
	start := i + closeText + 2
	closeURL := strings.IndexByte(s[start:], ')')
	if closeURL < 0 {
		return "", "", 0, false
	}
	target = s[start : start+closeURL]
	if target == "" || strings.ContainsAny(target, " \t") {
		return "", "", 0, false
	}
	return s[i+1 : i+closeText], target, start + closeURL + 1, true
}

// isWebURL reports whether a link target can be a Slack link; anything else
// (neutralized hxxps:// links, relative paths) is shown as plain text.
func isWebURL(target string) bool {
	lower := strings.ToLower(target)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}
