package httpapi

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// Search snippets are plain text: Markdown syntax is dropped so the UI can
// show the words and mark the matches without rendering anything.

const (
	snippetRunes  = 160 // a snippet's length before the ellipses
	snippetBefore = 40  // context kept ahead of the first match
)

var (
	mdImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdAutoLink = regexp.MustCompile(`<((?:https?|mailto):[^>\s]+)>`)
	mdLineHead = regexp.MustCompile(`^\s{0,3}(?:#{1,6}\s+|>\s?|[-*+]\s+(?:\[[ xX]\]\s+)?|\d{1,9}[.)]\s+)`)
	mdFence    = regexp.MustCompile("^\\s*(?:```|~~~)")
	mdRule     = regexp.MustCompile(`^\s*(?:[-*_]\s*){3,}$`)
	mdMarks    = strings.NewReplacer("**", "", "__", "", "~~", "", "`", "", "*", "")
)

// markdownPlain strips Markdown from s and puts it on one line.
func markdownPlain(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if mdFence.MatchString(line) || mdRule.MatchString(line) {
			continue
		}
		line = mdLineHead.ReplaceAllString(line, "")
		line = mdImage.ReplaceAllString(line, "$1")
		line = mdLink.ReplaceAllString(line, "$1")
		line = mdAutoLink.ReplaceAllString(line, "$1")
		line = mdMarks.Replace(line)
		line = strings.ReplaceAll(line, "|", " ")
		b.WriteString(line)
		b.WriteByte(' ')
	}
	return strings.Join(strings.FieldsFunc(b.String(), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}), " ")
}

// searchTerms splits a query the way the full-text index does: runs of
// letters, digits and marks, folded.
func searchTerms(q string) [][]rune {
	var out [][]rune
	for _, w := range strings.FieldsFunc(q, notWordRune) {
		t := foldRunes([]rune(w))
		dup := false
		for _, o := range out {
			dup = dup || string(o) == string(t)
		}
		if !dup {
			out = append(out, t)
		}
	}
	return out
}

func notWordRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
}

// foldRunes lowercases and drops the accents of common Latin letters, one
// rune for one, so indices into the folded text are indices into the text.
func foldRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		r = unicode.ToLower(r)
		if f, ok := accentFold[r]; ok {
			r = f
		}
		out[i] = r
	}
	return out
}

var accentFold = func() map[rune]rune {
	m := map[rune]rune{}
	for base, accented := range map[rune]string{
		'a': "àáâãäåāăą", 'c': "çćĉċč", 'd': "ďđ", 'e': "èéêëēĕėęě", 'g': "ĝğġģ", 'h': "ĥħ", 'i': "ìíîïĩīĭįı",
		'j': "ĵ", 'k': "ķ", 'l': "ĺļľŀł", 'n': "ñńņňŉ", 'o': "òóôõöøōŏő", 'r': "ŕŗř", 's': "śŝşš", 't': "ţťŧ",
		'u': "ùúûüũūŭůűų", 'w': "ŵ", 'y': "ýÿŷ", 'z': "źżž",
	} {
		for _, r := range accented {
			m[r] = base
		}
	}
	return m
}()

// matchRanges finds the terms in text: whole words, or anywhere in a word
// when words is false (code identifiers). Ranges are rune indices, merged.
func matchRanges(text []rune, terms [][]rune, words bool) [][2]int {
	folded := foldRunes(text)
	var out [][2]int
	for _, t := range terms {
		if len(t) == 0 {
			continue
		}
		for i := 0; i+len(t) <= len(folded); i++ {
			if string(folded[i:i+len(t)]) != string(t) {
				continue
			}
			if words && (i > 0 && !notWordRune(folded[i-1]) || i+len(t) < len(folded) && !notWordRune(folded[i+len(t)])) {
				continue
			}
			out = append(out, [2]int{i, i + len(t)})
			i += len(t) - 1
		}
	}
	return mergeRanges(out)
}

func mergeRanges(rs [][2]int) [][2]int {
	if len(rs) < 2 {
		return rs
	}
	for i := 1; i < len(rs); i++ { // insertion sort: few ranges
		for j := i; j > 0 && rs[j][0] < rs[j-1][0]; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
	out := [][2]int{rs[0]}
	for _, r := range rs[1:] {
		last := &out[len(out)-1]
		if r[0] <= last[1] {
			last[1] = max(last[1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

// snippet cuts plain text around its first match and returns the matches
// in it as UTF-16 ranges; a text without one starts at its beginning.
func snippet(text string, terms [][]rune, words bool) (string, []dto.TextRange) {
	rs := []rune(text)
	ms := matchRanges(rs, terms, words)
	start := 0
	if len(ms) > 0 && ms[0][0] > snippetBefore {
		start = ms[0][0] - snippetBefore
		for start < ms[0][0] && !unicode.IsSpace(rs[start-1]) {
			start++
		}
	}
	end := min(len(rs), start+snippetRunes)
	if end < len(rs) {
		for e := end; e > start+snippetRunes/2; e-- {
			if unicode.IsSpace(rs[e]) {
				end = e
				break
			}
		}
	}
	// markdownPlain leaves single spaces, and start and end sit next to one, so
	// the cut needs no trimming.
	var b []rune
	if start > 0 {
		b = append(b, '…')
	}
	shift := len(b) - start
	b = append(b, rs[start:end]...)
	if end < len(rs) {
		b = append(b, '…')
	}
	var out []dto.TextRange
	for _, m := range ms {
		s, e := m[0]+shift, m[1]+shift
		if m[0] < start || m[1] > end || s < 0 || e > len(b) {
			continue
		}
		out = append(out, dto.TextRange{Start: utf16Len(b[:s]), End: utf16Len(b[:e])})
	}
	return string(b), nonNil(out)
}

func utf16Len(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += utf16.RuneLen(r)
	}
	return n
}
