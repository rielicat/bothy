package discord

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Discord already renders the Bothy markdown subset (bold, italic, code,
// fences, lists, masked links). render escapes the Discord syntax the subset
// does not have, so text shows as written: underline and italics with "_",
// strikethrough "~", spoilers "|", mention and timestamp pills "<", quotes
// and subtext at line start. Headings become bold lines.

const inlineEscapes = "_~|<"

var headingRe = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]+(.+?)[ \t#]*$`)

func render(md string) string {
	var b strings.Builder
	b.Grow(len(md) + len(md)/16)
	fence := false
	for i, line := range strings.Split(md, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if isFence(strings.TrimLeft(line, " "), fence) {
			fence = !fence
			b.WriteString(line)
			continue
		}
		if fence {
			b.WriteString(line)
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			b.WriteString("**")
			writeInline(&b, m[1])
			b.WriteString("**")
			continue
		}
		rest := strings.TrimLeft(line, " ")
		b.WriteString(line[:len(line)-len(rest)])
		if strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, "-# ") {
			b.WriteByte('\\')
		}
		writeInline(&b, rest)
	}
	return b.String()
}

// isFence reports whether a line opens or closes a fenced block; "```x```"
// on one line is inline code.
func isFence(line string, inFence bool) bool {
	return strings.HasPrefix(line, "```") && (inFence || !strings.Contains(line[3:], "```"))
}

func writeInline(b *strings.Builder, s string) {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			// Bothy markdown escapes mean the same to Discord.
			_, n := utf8.DecodeRuneInString(s[i+1:])
			b.WriteString(s[i : i+1+n])
			i += 1 + n
		case c == '`':
			n := runLen(s[i:], '`')
			if end := closingRun(s[i+n:], n); end >= 0 {
				b.WriteString(s[i : i+n+end+n])
				i += n + end + n
			} else {
				b.WriteString(s[i : i+n])
				i += n
			}
		case c == '[':
			text, target, end, ok := parseLink(s, i)
			if !ok {
				b.WriteByte('[')
				i++
				continue
			}
			if isWebURL(target) {
				b.WriteByte('[')
				writeInline(b, text)
				b.WriteString("](")
				b.WriteString(target)
				b.WriteByte(')')
			} else {
				writeInline(b, text)
				b.WriteString(" (")
				writeInline(b, target)
				b.WriteByte(')')
			}
			i = end
		case strings.IndexByte(inlineEscapes, c) >= 0:
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
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

// isWebURL reports whether a link target can be a masked link; anything else
// (neutralized hxxps:// links, relative paths) is shown as plain text.
func isWebURL(target string) bool {
	lower := strings.ToLower(target)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}
