package egress

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// skipped elements contribute no text. svg and math may legally self-close;
// the others are treated like browsers do even when written as <x/>.
var (
	skipped = map[atom.Atom]bool{
		atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
		atom.Iframe: true, atom.Object: true, atom.Canvas: true, atom.Select: true,
		atom.Svg: true, atom.Math: true,
	}
	selfClosable = map[atom.Atom]bool{atom.Svg: true, atom.Math: true}
	paragraphs   = map[atom.Atom]bool{
		atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true, atom.Header: true,
		atom.Footer: true, atom.Nav: true, atom.Aside: true, atom.Main: true, atom.Blockquote: true,
		atom.Table: true, atom.Form: true, atom.Figure: true, atom.Address: true, atom.Ul: true,
		atom.Ol: true, atom.Dl: true, atom.Hr: true, atom.Details: true, atom.Summary: true,
	}
	lines    = map[atom.Atom]bool{atom.Tr: true, atom.Dt: true, atom.Dd: true, atom.Figcaption: true, atom.Caption: true}
	headings = map[atom.Atom]int{atom.H1: 1, atom.H2: 2, atom.H3: 3, atom.H4: 4, atom.H5: 5, atom.H6: 6}
)

type textWriter struct {
	b     strings.Builder
	nl    int
	space bool
}

func (w *textWriter) pending() {
	if w.b.Len() == 0 {
		w.nl, w.space = 0, false
		return
	}
	switch {
	case w.nl > 0:
		w.b.WriteString(strings.Repeat("\n", w.nl))
	case w.space:
		w.b.WriteByte(' ')
	}
	w.nl, w.space = 0, false
}

func (w *textWriter) breakLine(n int) { w.nl = max(w.nl, n) }

func (w *textWriter) inline(t string) {
	f := strings.Fields(t)
	if len(f) == 0 {
		if t != "" {
			w.space = true
		}
		return
	}
	if isSpace(t[0]) {
		w.space = true
	}
	w.pending()
	w.b.WriteString(strings.Join(f, " "))
	w.space = isSpace(t[len(t)-1])
}

func (w *textWriter) raw(t string) {
	if t == "" {
		return
	}
	w.pending()
	w.b.WriteString(t)
}

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f' }

// htmlToText renders the readable text of a page: block structure as line
// breaks, headings and list items marked, links followed by their absolute
// URL, scripts, styles and embedded objects dropped.
func htmlToText(doc string, base *url.URL) (title, text string) {
	z := html.NewTokenizer(strings.NewReader(doc))
	var w textWriter
	skip, pre := 0, 0
	inTitle := false
	var links []string
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return title, tidy(w.b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			if skipped[a] {
				if tt == html.StartTagToken || !selfClosable[a] {
					skip++
				}
				continue
			}
			if skip > 0 {
				continue
			}
			switch {
			case a == atom.Title:
				inTitle = tt == html.StartTagToken
			case a == atom.Pre:
				pre++
				w.breakLine(1)
			case a == atom.Br:
				w.breakLine(1)
			case a == atom.Li:
				w.breakLine(1)
				w.raw("- ")
			case headings[a] > 0:
				w.breakLine(2)
				w.raw(strings.Repeat("#", headings[a]) + " ")
			case a == atom.Td || a == atom.Th:
				w.space = true
			case a == atom.A && tt == html.StartTagToken:
				links = append(links, linkTarget(z, hasAttr, base))
			case paragraphs[a]:
				w.breakLine(2)
			case lines[a]:
				w.breakLine(1)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			if skipped[a] {
				if skip > 0 {
					skip--
				}
				continue
			}
			if skip > 0 {
				continue
			}
			switch {
			case a == atom.Title:
				inTitle = false
			case a == atom.Pre:
				pre = max(pre-1, 0)
				w.breakLine(1)
			case a == atom.A && len(links) > 0:
				if l := links[len(links)-1]; l != "" {
					w.inline(" (" + l + ")")
				}
				links = links[:len(links)-1]
			case headings[a] > 0 || paragraphs[a]:
				w.breakLine(2)
			case lines[a] || a == atom.Li:
				w.breakLine(1)
			}
		case html.TextToken:
			if skip > 0 {
				continue
			}
			t := string(z.Text())
			switch {
			case inTitle:
				if title == "" {
					title = truncateRunes(strings.Join(strings.Fields(t), " "), 300)
				}
			case pre > 0:
				w.raw(t)
			default:
				w.inline(t)
			}
		}
	}
}

func linkTarget(z *html.Tokenizer, hasAttr bool, base *url.URL) string {
	for hasAttr {
		var k, v []byte
		k, v, hasAttr = z.TagAttr()
		if string(k) != "href" {
			continue
		}
		href := strings.TrimSpace(string(v))
		if href == "" || strings.HasPrefix(href, "#") || len(href) > 2048 || base == nil {
			return ""
		}
		u, err := base.Parse(href)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return ""
		}
		u.User = nil
		return u.String()
	}
	return ""
}

// tidy trims trailing spaces and squeezes runs of blank lines.
func tidy(s string) string {
	ls := strings.Split(s, "\n")
	out := ls[:0]
	blank := 0
	for _, l := range ls {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// decodeText returns valid UTF-8. Pages that are not UTF-8 are decoded as
// Latin-1 when they say so and otherwise get replacement characters; other
// legacy charsets are rare enough not to justify the tables.
func decodeText(b []byte, charset string) string {
	var s string
	switch {
	case utf8.Valid(b):
		s = string(b)
	case latin1(charset):
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		s = string(r)
	default:
		s = strings.ToValidUTF8(string(b), "�")
	}
	return strings.ReplaceAll(s, "\x00", "")
}

func latin1(charset string) bool {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252", "us-ascii", "ascii":
		return true
	}
	return false
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
