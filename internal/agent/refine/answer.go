package refine

import (
	"cmp"
	"slices"
	"strings"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

const marker = "final answer"

// Extract returns the text after the last line that starts with
// "Final answer:" (case-insensitive, Markdown emphasis, heading and quote
// marks around the label tolerated), through the end of the text, or "" when
// there is none. A lone \boxed{…} or $…$ wrapper and a trailing full stop are
// removed, since models add them to the same answer inconsistently.
func Extract(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		rest, ok := afterMarker(lines[i])
		if !ok {
			continue
		}
		if tail := strings.TrimSpace(strings.Join(lines[i+1:], "\n")); tail != "" {
			rest = strings.TrimSpace(rest + "\n" + tail)
		}
		return unwrap(rest)
	}
	return ""
}

// Strip removes the closing "Final answer:" section that drafts end with
// for the vote, since the reader has the full answer above it. A text that
// is only that section keeps the answer it gives.
func Strip(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		rest, ok := afterMarker(lines[i])
		if !ok {
			continue
		}
		if body := strings.TrimSpace(strings.Join(lines[:i], "\n")); body != "" {
			return body
		}
		return strings.TrimSpace(rest + "\n" + strings.Join(lines[i+1:], "\n"))
	}
	return strings.TrimSpace(text)
}

func afterMarker(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t>#*_`")
	if len(s) < len(marker) || !strings.EqualFold(s[:len(marker)], marker) {
		return "", false
	}
	s = strings.TrimLeft(s[len(marker):], " *_`")
	if !strings.HasPrefix(s, ":") {
		return "", false
	}
	return strings.TrimSpace(strings.Trim(s[1:], " \t*_")), true
}

func unwrap(s string) string {
	s = strings.TrimSpace(s)
	for _, d := range [][2]string{{"$$", "$$"}, {"$", "$"}, {`\(`, `\)`}, {`\[`, `\]`}} {
		if len(s) > len(d[0])+len(d[1]) && strings.HasPrefix(s, d[0]) && strings.HasSuffix(s, d[1]) {
			s = strings.TrimSpace(s[len(d[0]) : len(s)-len(d[1])])
			break
		}
	}
	if inner, ok := strings.CutPrefix(s, `\boxed`); ok {
		if b, ok := braced(strings.TrimSpace(inner)); ok {
			s = strings.TrimSpace(b)
		}
	}
	if strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "..") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "."))
	}
	return s
}

// braced returns the inside of s when s is one {…} group whose first brace
// closes at its last character.
func braced(s string) (string, bool) {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return "", false
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // an escaped brace does not count
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 && i != len(s)-1 {
				return "", false
			}
		}
	}
	if depth != 0 {
		return "", false
	}
	return s[1 : len(s)-1], true
}

// LaTeX spacing commands: the thin ones sit inside numbers (1\,000) and are
// dropped; the word-sized ones separate words and become a space. Longer
// names come first so \qquad is not read as \q + quad.
var (
	latexThin = strings.NewReplacer(`\negthinspace`, "", `\thinspace`, "", `\medspace`, "", `\thickspace`, "",
		`\,`, "", `\:`, "", `\;`, "", `\!`, "")
	latexWide = strings.NewReplacer(`\qquad`, " ", `\quad`, " ", `\enspace`, " ", `\ `, " ")
)

// Normalize makes equal answers compare equal: trim, strip LaTeX spacing
// tokens, collapse whitespace, unwrap one layer of braces, drop trailing
// decimal zeros (3.500 → 3.5, 2.0 → 2) and lowercase.
func Normalize(s string) string {
	s = latexThin.Replace(latexWide.Replace(strings.TrimSpace(s)))
	s = strings.Join(strings.Fields(s), " ")
	if inner, ok := braced(s); ok {
		s = strings.Join(strings.Fields(inner), " ")
	}
	s = trimDecimalZeros(s)
	return strings.ToLower(s)
}

// trimDecimalZeros rewrites every standalone decimal number (digits, one
// dot, digits) without its trailing zeros. Runs with more than one dot, such
// as versions and dates, are left alone.
func trimDecimalZeros(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if !isDigit(s[i]) || (i > 0 && (isDigit(s[i-1]) || s[i-1] == '.')) {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
			j++
		}
		b.WriteString(trimNumber(s[i:j]))
		i = j
	}
	return b.String()
}

func trimNumber(n string) string {
	whole, frac, ok := strings.Cut(n, ".")
	if !ok || frac == "" || strings.Contains(frac, ".") {
		return n
	}
	for _, c := range frac {
		if !unicode.IsDigit(c) {
			return n
		}
	}
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Vote is the plurality vote over normalized answers, one per draft in draft
// order; empty answers do not vote. The winner is the earliest draft holding
// the answer with the most votes, ties going to the answer whose first draft
// is earliest; -1 when nothing voted.
func Vote(answers []string) (winner int, votes []core.RefineVote) {
	at := map[string]int{}
	for i, a := range answers {
		if a == "" {
			continue
		}
		k, ok := at[a]
		if !ok {
			k = len(votes)
			at[a] = k
			votes = append(votes, core.RefineVote{Answer: a})
		}
		votes[k].Count++
		votes[k].Drafts = append(votes[k].Drafts, i)
	}
	if len(votes) == 0 {
		return -1, []core.RefineVote{}
	}
	slices.SortStableFunc(votes, func(a, b core.RefineVote) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Drafts[0], b.Drafts[0]))
	})
	return votes[0].Drafts[0], votes
}
