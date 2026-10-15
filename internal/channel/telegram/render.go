package telegram

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// render converts the Bothy markdown subset to Telegram HTML, escaping all
// other text, and also returns the plain text the reader sees (sent without
// parse mode if Telegram ever rejects the HTML).
//
// Telegram forbids code inside bold, italic or links. A code span in bold or
// italic text closes them and opens them again after it, so it keeps its
// code style, which is what keeps a file name from becoming a link; in a
// link's text it is plain text.
func render(md string) (html, plain string) {
	var hb, pb strings.Builder
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		if i > 0 {
			hb.WriteByte('\n')
			pb.WriteByte('\n')
		}
		line := lines[i]
		t := strings.TrimLeft(line, " ")
		indent := len(line) - len(t)
		if m := openingFence(t); m != "" && indent <= 3 {
			lang := codeLang(strings.TrimSpace(t[len(m):]))
			var body []string
			j := i + 1
			for ; j < len(lines); j++ {
				c := strings.TrimLeft(lines[j], " ")
				if len(lines[j])-len(c) <= 3 && closesFence(c, m) {
					break
				}
				body = append(body, lines[j])
			}
			code := strings.Join(body, "\n")
			if lang != "" {
				hb.WriteString(`<pre><code class="language-` + lang + `">` + escapeHTML(code) + "</code></pre>")
			} else {
				hb.WriteString("<pre>" + escapeHTML(code) + "</pre>")
			}
			pb.WriteString(code)
			i = j
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			h, p := inline(strings.TrimRight(strings.TrimRight(m[1], "#"), " "), []string{"b"}, false)
			hb.WriteString(dropEmpty("<b>" + h + "</b>"))
			pb.WriteString(p)
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			h, p := inline(m[2], nil, false)
			hb.WriteString(m[1] + "• " + dropEmpty(h))
			pb.WriteString(m[1] + "• " + p)
			continue
		}
		h, p := inline(line, nil, false)
		hb.WriteString(dropEmpty(h))
		pb.WriteString(p)
	}
	return hb.String(), pb.String()
}

var (
	headingRe = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]+(.*)$`)
	bulletRe  = regexp.MustCompile(`^([ \t]*)[-*+][ \t]+(.*)$`)
	langRe    = regexp.MustCompile(`^[A-Za-z0-9_+#.-]{1,32}$`)
)

func codeLang(info string) string {
	f := strings.Fields(info)
	if len(f) == 0 || !langRe.MatchString(f[0]) {
		return ""
	}
	return f[0]
}

// inline renders one line's inline markup. open holds the bold and italic
// tags s sits in, which a code span closes and reopens; inLink disables code
// tags and nested links.
func inline(s string, open []string, inLink bool) (string, string) {
	var hb, pb strings.Builder
	lit := func(x string) {
		hb.WriteString(escapeHTML(x))
		pb.WriteString(x)
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			lit(s[i+1 : i+2])
			i += 2
		case c == '`':
			run := runLen(s[i:], '`')
			closeAt := findRun(s[i+run:], run)
			if closeAt < 0 {
				lit(s[i : i+run])
				i += run
				continue
			}
			code := s[i+run : i+run+closeAt]
			if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.Trim(code, " ") != "" {
				code = code[1 : len(code)-1]
			}
			if inLink {
				lit(code)
			} else {
				for j := len(open) - 1; j >= 0; j-- {
					hb.WriteString("</" + open[j] + ">")
				}
				hb.WriteString("<code>" + escapeHTML(code) + "</code>")
				for _, t := range open {
					hb.WriteString("<" + t + ">")
				}
				pb.WriteString(code)
			}
			i += 2*run + closeAt
		case c == '[' && !inLink:
			text, href, n, ok := parseLink(s[i:])
			if !ok || !safeLinkURL(href) {
				lit("[")
				i++
				continue
			}
			h, p := inline(text, open, true)
			hb.WriteString(`<a href="` + escapeHTML(href) + `">` + h + "</a>")
			pb.WriteString(p)
			i += n
		case c == '*' && strings.HasPrefix(s[i:], "**"):
			end := closingDelim(s, i+2, "**")
			if end < 0 {
				lit("**")
				i += 2
				continue
			}
			h, p := inline(s[i+2:end], append(slices.Clip(open), "b"), inLink)
			hb.WriteString("<b>" + h + "</b>")
			pb.WriteString(p)
			i = end + 2
		case c == '*' || c == '_':
			end := italicEnd(s, i)
			if end < 0 {
				lit(s[i : i+1])
				i++
				continue
			}
			h, p := inline(s[i+1:end], append(slices.Clip(open), "i"), inLink)
			hb.WriteString("<i>" + h + "</i>")
			pb.WriteString(p)
			i = end + 1
		default:
			_, n := utf8.DecodeRuneInString(s[i:])
			lit(s[i : i+n])
			i += n
		}
	}
	return hb.String(), pb.String()
}

// closingDelim finds delim closing an emphasis opened just before from: the
// content must be non-empty and not start or end with a space. A delimiter
// inside a code span closes nothing.
func closingDelim(s string, from int, delim string) int {
	if from >= len(s) || s[from] == ' ' {
		return -1
	}
	for j := from; j < len(s); j++ {
		if s[j] == '`' {
			j = codeSpanEnd(s, j) - 1
			continue
		}
		if strings.HasPrefix(s[j:], delim) {
			if j == from || s[j-1] == ' ' {
				return -1
			}
			return j
		}
	}
	return -1
}

// codeSpanEnd returns the index just past the code span opening at i, or
// past the backtick run there when nothing closes it.
func codeSpanEnd(s string, i int) int {
	n := runLen(s[i:], '`')
	if end := findRun(s[i+n:], n); end >= 0 {
		return i + n + end + n
	}
	return i + n
}

// dropEmpty removes the empty bold and italic pairs left where a code span
// starts or ends one.
func dropEmpty(h string) string {
	for strings.Contains(h, "<b></b>") || strings.Contains(h, "<i></i>") {
		h = strings.ReplaceAll(strings.ReplaceAll(h, "<b></b>", ""), "<i></i>", "")
	}
	return h
}

// italicEnd finds the closing delimiter of a single * or _ at i. Underscores
// inside words (snake_case) are not emphasis.
func italicEnd(s string, i int) int {
	c := s[i]
	if c == '_' && i > 0 && isWordByte(s[i-1]) {
		return -1
	}
	if i+1 >= len(s) || s[i+1] == ' ' || s[i+1] == c {
		return -1
	}
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\\' {
			j++
			continue
		}
		if s[j] == '`' {
			j = codeSpanEnd(s, j) - 1
			continue
		}
		if s[j] != c || s[j-1] == ' ' {
			continue
		}
		if c == '*' && j+1 < len(s) && s[j+1] == '*' {
			j++
			continue
		}
		if c == '_' && j+1 < len(s) && isWordByte(s[j+1]) {
			continue
		}
		return j
	}
	return -1
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// parseLink parses "[text](url)" at the start of s, returning the bytes used.
func parseLink(s string) (text, href string, n int, ok bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '[':
			depth++
		case ']':
			depth--
			if depth > 0 {
				continue
			}
			if i+1 >= len(s) || s[i+1] != '(' {
				return "", "", 0, false
			}
			end := strings.IndexByte(s[i+2:], ')')
			if end < 0 {
				return "", "", 0, false
			}
			href = s[i+2 : i+2+end]
			if href == "" || strings.ContainsFunc(href, unicode.IsSpace) {
				return "", "", 0, false
			}
			return s[1:i], href, i + 3 + end, true
		case '\n':
			return "", "", 0, false
		}
	}
	return "", "", 0, false
}

func safeLinkURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto", "tg":
		return true
	}
	return false
}

func escapeHTML(s string) string {
	if !strings.ContainsAny(s, `&<>"`) {
		return s
	}
	return htmlEscaper.Replace(s)
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func isASCIIPunct(b byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", b) >= 0
}

func runLen(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// findRun returns the index of the first run of exactly n backticks in s.
func findRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i + runLen(s[i:], '`')
		if j-i == n {
			return i
		}
		i = j
	}
	return -1
}

// openingFence returns the marker when t opens a fenced code block
// (CommonMark: three or more backticks or tildes; a backtick info string may
// not contain backticks).
func openingFence(t string) string {
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return ""
	}
	n := runLen(t, t[0])
	if n < 3 || (t[0] == '`' && strings.Contains(t[n:], "`")) {
		return ""
	}
	return t[:n]
}

func closesFence(t, marker string) bool {
	t = strings.TrimRight(t, " \t")
	return len(t) >= len(marker) && strings.Trim(t, marker[:1]) == ""
}

// utf16Len is Telegram's message length unit.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
