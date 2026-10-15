package knowledge

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	snippetLines   = 30
	snippetBytes   = 1600
	snippetLineMax = 240
	snippetLead    = 3 // context lines kept above the first matching line
	queryMaxRunes  = 200
)

// queryWords tokenizes like the FTS index (letters, digits, marks), lowercased.
func queryWords(q string) []string {
	return strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	})
}

// normalizeQuery collapses whitespace and checks the length limits.
func normalizeQuery(q string) (string, error) {
	q = strings.Join(strings.Fields(q), " ")
	switch {
	case q == "":
		return "", invalidf("query is empty")
	case !utf8.ValidString(q):
		return "", invalidf("query is not valid UTF-8")
	case utf8.RuneCountInString(q) > queryMaxRunes:
		return "", invalidf("query is longer than %d characters", queryMaxRunes)
	case len(queryWords(q)) == 0:
		return "", invalidf("query has no words to search for")
	}
	return q, nil
}

// citation is "owner/repo@sha:path#L10-L42" (sha cut to 12 characters;
// "owner/repo:path#…" for a directory without a commit).
func citation(repo, sha, p string, start, end int) string {
	var b strings.Builder
	b.WriteString(repo)
	if sha != "" {
		b.WriteString("@" + shortSHA(sha))
	}
	b.WriteString(":" + p + "#L" + strconv.Itoa(start))
	if end > start {
		b.WriteString("-L" + strconv.Itoa(end))
	}
	return b.String()
}

func capLine(l string) string {
	if len(l) <= snippetLineMax {
		return l
	}
	cut := snippetLineMax
	for cut > 0 && !utf8.RuneStart(l[cut]) {
		cut--
	}
	return l[:cut] + "…"
}

// refine narrows a hit's chunk to the lines around its first match and sets
// the citation to exactly those lines.
func refine(h core.KnowledgeHit, words []string) core.KnowledgeHit {
	lines := strings.Split(h.Snippet, "\n")
	first := firstMatch(lines, words)
	if first < 0 {
		first = max(firstMatch(lines, stems(words)), 0)
	}
	start := 0
	if len(lines) > snippetLines {
		start = max(0, min(first-snippetLead, len(lines)-snippetLines))
	}
	end, size := start, 0
	for end < len(lines) && end-start < snippetLines {
		l := capLine(lines[end])
		if end > start && size+len(l)+1 > snippetBytes {
			break
		}
		lines[end] = l
		size += len(l) + 1
		end++
	}
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	chunkStart := h.StartLine
	h.Snippet = strings.Join(lines[start:end], "\n")
	h.StartLine, h.EndLine = chunkStart+start, chunkStart+end-1
	h.Citation = citation(h.Repo, h.SHA, h.Path, h.StartLine, h.EndLine)
	return h
}

func firstMatch(lines, words []string) int {
	for i, l := range lines {
		if containsAny(strings.ToLower(l), words) {
			return i
		}
	}
	return -1
}

// stems cuts common English endings off the query words. The index stems
// words (porter), so a chunk found for "redirects" may only say "redirect";
// this rougher cut is enough to find the line it says it on.
func stems(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w
		for _, suffix := range []string{"ies", "ing", "es", "ed", "s"} {
			if s, ok := strings.CutSuffix(w, suffix); ok && utf8.RuneCountInString(s) >= 3 {
				out[i] = s
				break
			}
		}
	}
	return out
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// formatHits renders search results for the model, with line numbers.
func formatHits(query string, hits []core.KnowledgeHit) string {
	if len(hits) == 0 {
		return fmt.Sprintf("No indexed code or docs match %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results for %q. Cite them as shown (owner/repo@sha:path#Lx-Ly).\n", len(hits), query)
	for i, h := range hits {
		title := ""
		if h.Title != "" {
			title = " · " + h.Title
		}
		fmt.Fprintf(&b, "\n[%d] %s (%s %s%s)\n", i+1, h.Citation, h.Lang, h.Kind, title)
		width := len(strconv.Itoa(h.EndLine))
		for j, l := range strings.Split(h.Snippet, "\n") {
			fmt.Fprintf(&b, "%*d  %s\n", width, h.StartLine+j, l)
		}
	}
	return b.String()
}

var spotEncoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// spotlight wraps untrusted text the way the runner does (SPEC §5.6): a
// fresh random id per block, so a closing tag inside the text cannot match.
func spotlight(content string, attrs ...string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	id := spotEncoding.EncodeToString(b)
	var sb strings.Builder
	sb.WriteString("<untrusted")
	for i := 0; i+1 < len(attrs); i += 2 {
		sb.WriteString(" " + attrs[i] + `="` + attrValue(attrs[i+1]) + `"`)
	}
	sb.WriteString(` id="` + id + `">` + "\n" + content)
	if !strings.HasSuffix(content, "\n") {
		sb.WriteByte('\n')
	}
	sb.WriteString(`</untrusted id="` + id + `">`)
	return sb.String()
}

func attrValue(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case r == '<':
			return '‹'
		case r == '>':
			return '›'
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
}
