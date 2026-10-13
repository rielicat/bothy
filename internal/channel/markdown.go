package channel

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ulen measures text the way the strictest platform does (Telegram counts
// UTF-16 code units), so a piece that fits here fits every adapter's MaxLen.
func ulen(s string) int {
	n := 0
	for _, r := range s {
		n += runeUnits(r)
	}
	return n
}

func runeUnits(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// prefixFitting returns the largest rune-aligned byte index i with
// ulen(s[:i]) <= limit.
func prefixFitting(s string, limit int) int {
	n := 0
	for i, r := range s {
		w := runeUnits(r)
		if n+w > limit {
			return i
		}
		n += w
	}
	return len(s)
}

// splitAll splits markdown into pieces of at most limit units, preferring
// paragraph, then line, then word boundaries, and closing/reopening a fenced
// code block that straddles a cut.
func splitAll(s string, limit int) []string {
	if limit <= 0 || ulen(s) <= limit {
		return []string{s}
	}
	var out []string
	carry := ""
	for s != "" {
		chunk, used, next := splitFirst(s, carry, limit)
		if strings.TrimSpace(chunk) != "" {
			out = append(out, chunk)
		}
		s, carry = s[used:], next
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// splitFirst returns the first piece of s (reopening carry, a fence line left
// open by the previous piece), how many bytes of s it consumed and the fence
// line the next piece must reopen.
func splitFirst(s, carry string, limit int) (chunk string, used int, next string) {
	prefix := ""
	if carry != "" {
		prefix = carry + "\n"
	}
	if ulen(prefix)+ulen(s) <= limit {
		return prefix + s, len(s), ""
	}
	budget := limit - ulen(prefix)
	for budget > 0 {
		end, rest := cutPoint(s, budget)
		body := strings.TrimRight(s[:end], " \t\n")
		open, line, marker := fenceState(carry, body)
		closing := ""
		if open {
			closing = "\n" + marker
		}
		if ulen(prefix)+ulen(body)+ulen(closing) <= limit {
			if open {
				next = line
			}
			return prefix + body + closing, rest, next
		}
		budget -= max(1, ulen(closing))
	}
	// The limit cannot even hold the fence lines: hard cut, always progressing.
	end := max(prefixFitting(s, limit), firstRuneLen(s))
	return s[:end], end, ""
}

func firstRuneLen(s string) int {
	_, n := utf8.DecodeRuneInString(s)
	return max(n, 1)
}

// cutPoint picks where a piece of at most budget units ends (end) and where
// the next piece starts (rest), skipping the separator.
func cutPoint(s string, budget int) (end, rest int) {
	limit := prefixFitting(s, budget)
	if limit >= len(s) {
		return len(s), len(s)
	}
	if limit == 0 {
		limit = firstRuneLen(s)
	}
	half := limit / 2
	window := func(extra int) string { return s[:min(len(s), limit+extra)] }
	skip := func(i int) int {
		for i < len(s) && s[i] == '\n' {
			i++
		}
		return i
	}
	if i := strings.LastIndex(window(2), "\n\n"); i > 0 && i >= half {
		return i, skip(i)
	}
	if i := strings.LastIndexByte(window(1), '\n'); i > 0 && i >= half {
		return i, skip(i + 1)
	}
	// A cut inside a line must not change what is a fence line: the text
	// after it becomes a line start (where a backtick run opens a fence), and
	// a fence-like line cut short can start or end a block it did not before.
	w := window(1)
	for i := strings.LastIndexAny(w, " \t"); i > 0 && i >= half; i = strings.LastIndexAny(w[:i], " \t") {
		if !startsFenceRun(s[i+1:]) && !startsFenceRun(s[strings.LastIndexByte(s[:i], '\n')+1:]) {
			return i, i + 1
		}
	}
	if i := strings.LastIndexByte(window(1), '\n'); i > 0 {
		return i, skip(i + 1)
	}
	for limit > 1 && startsFenceRun(s[limit:]) {
		limit -= firstRuneLenBefore(s, limit)
	}
	return limit, limit
}

func startsFenceRun(s string) bool {
	s = strings.TrimLeft(s, " \t")
	return strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~")
}

func firstRuneLenBefore(s string, i int) int {
	_, n := utf8.DecodeLastRuneInString(s[:i])
	return max(n, 1)
}

// fenceState scans body, starting inside the fence opened by carry (if any),
// and reports whether it ends inside a fenced code block, with that block's
// opening line and marker.
func fenceState(carry, body string) (open bool, line, marker string) {
	if carry != "" {
		open, line, marker = true, carry, fenceRun(carry)
	}
	for l := range strings.SplitSeq(body, "\n") {
		t := fenceCandidate(l)
		if open {
			if closesFence(t, marker) {
				open, line, marker = false, "", ""
			}
			continue
		}
		if m := openingFence(t); m != "" {
			open, line, marker = true, strings.TrimSpace(t), m
		}
	}
	return open, line, marker
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

// openingFence returns the fence marker when t opens a fenced code block
// (CommonMark: three or more backticks or tildes; a backtick info string
// cannot contain backticks).
func openingFence(t string) string {
	m := fenceRun(t)
	if len(m) < 3 {
		return ""
	}
	if m[0] == '`' && strings.Contains(t[len(m):], "`") {
		return ""
	}
	return m
}

func closesFence(t, marker string) bool {
	t = strings.TrimRight(t, " \t")
	return marker != "" && len(t) >= len(marker) && strings.Trim(t, marker[:1]) == ""
}

// ---- escaping for system-rendered text (cards, notices) ----

// escapeMD backslash-escapes markdown syntax so s renders verbatim. Adapters
// honour CommonMark backslash escapes of ASCII punctuation.
func escapeMD(s string) string {
	var b strings.Builder
	atLineStart := true
	for _, r := range s {
		switch {
		case strings.ContainsRune("\\`*_[]<>|~", r):
			b.WriteByte('\\')
		case atLineStart && strings.ContainsRune("#-+=>", r):
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		atLineStart = r == '\n'
	}
	return escapeOrderedMarkers(b.String())
}

var orderedMarkerRe = regexp.MustCompile(`(?m)^(\d+)([.)])`)

func escapeOrderedMarkers(s string) string { return orderedMarkerRe.ReplaceAllString(s, `$1\$2`) }

// codeSpan wraps s in a backtick run longer than any run inside it, so the
// content cannot close the span early.
func codeSpan(s string) string {
	fence := strings.Repeat("`", longestRun(s, '`')+1)
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || s == "" {
		pad = " "
	}
	return fence + pad + s + pad + fence
}

// codeBlock fences s with a backtick fence longer than any run inside it.
func codeBlock(info, s string) string {
	fence := strings.Repeat("`", max(3, longestRun(s, '`')+1))
	return fence + info + "\n" + strings.TrimRight(s, "\n") + "\n" + fence
}

func longestRun(s string, c byte) int {
	best, n := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}

// revealInvisible renders bidi controls and zero-width characters visibly
// (⟨U+202E⟩) so a card cannot hide or reorder what it shows (SPEC §5.9).
func revealInvisible(s string) string {
	if !strings.ContainsFunc(s, invisible) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if invisible(r) {
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func invisible(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x061C, r == 0x180E, r == 0xFEFF:
		return true
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E,
		r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// truncateRunes shortens s to at most n runes, marking the cut with "…".
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:max(0, n-1)]) + "…"
}
