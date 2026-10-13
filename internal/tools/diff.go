package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	diffContext = 3
	// maxEdit bounds the Myers search; beyond it the changed middle is shown
	// as one replacement, which is still an exact (if less minimal) diff.
	maxEdit = 1000
)

type opKind byte

const (
	opEqual opKind = ' '
	opDel   opKind = '-'
	opIns   opKind = '+'
)

type diffOp struct {
	kind opKind
	line string // with its newline, if it had one
}

// unifiedDiff renders a unified diff of old → new for an approval card,
// capped at maxDiff bytes. Empty when nothing changed.
func unifiedDiff(oldName, newName string, old, new []byte) string {
	if string(old) == string(new) {
		return ""
	}
	header := fmt.Sprintf("--- %s\n+++ %s\n", oldName, newName)
	if len(old)+len(new) > maxDiffInput {
		return header + fmt.Sprintf("(diff omitted: %s → %s)\n", humanSize(int64(len(old))), humanSize(int64(len(new))))
	}
	if isBinary(old) || isBinary(new) {
		return header + "Binary files differ\n"
	}
	ops := diffLines(splitLines(string(old)), splitLines(string(new)))
	// Only maxDiff bytes are ever shown, so formatting stops storing there:
	// rewriting a 4 MB file must not build a 4 MB diff text first.
	out := &cappedText{limit: maxDiff}
	out.WriteString(header)
	formatHunks(out, ops)
	return out.result()
}

// cappedText keeps the first limit bytes written and counts the rest.
type cappedText struct {
	b     strings.Builder
	limit int
	total int
}

func (c *cappedText) WriteString(s string) {
	c.total += len(s)
	if room := c.limit - c.b.Len(); room > 0 {
		c.b.WriteString(s[:min(len(s), room)])
	}
}

func (c *cappedText) writeByte(b byte) {
	c.total++
	if c.b.Len() < c.limit {
		_ = c.b.WriteByte(b)
	}
}

// result is what capText would return for the whole text.
func (c *cappedText) result() string {
	s := c.b.String()
	if c.total <= c.limit {
		return s
	}
	return capCut(s, c.limit, c.total)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func diffLines(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var mid []diffOp
	ok := false
	// The edit distance is at least the length difference, so a search
	// that cannot finish within maxEdit is not started (its trace alone
	// would hold maxEdit² entries).
	if len(ma) > 0 && len(mb) > 0 && max(len(ma)-len(mb), len(mb)-len(ma)) <= maxEdit {
		mid, ok = myers(ma, mb)
	}
	ops := make([]diffOp, 0, len(a)+len(b)-pre-suf)
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{opEqual, l})
	}
	if ok {
		ops = append(ops, mid...)
	} else {
		for _, l := range ma {
			ops = append(ops, diffOp{opDel, l})
		}
		for _, l := range mb {
			ops = append(ops, diffOp{opIns, l})
		}
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{opEqual, l})
	}
	return ops
}

// myers is the O(ND) greedy algorithm keeping one V snapshot per edit
// distance, so memory grows with D² rather than with the input size.
func myers(a, b []string) ([]diffOp, bool) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, true
	}
	maxD := min(n+m, maxEdit)
	off := maxD + 1
	v := make([]int, 2*maxD+3)
	var trace [][]int32 // x fits: inputs are at most maxDiffInput lines
	for d := 0; d <= maxD; d++ {
		snap := make([]int32, 2*d+1)
		for i, x := range v[off-d : off+d+1] {
			snap[i] = int32(x)
		}
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[off+k-1] < v[off+k+1] {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, d), true
			}
		}
	}
	return nil, false
}

func backtrack(trace [][]int32, a, b []string, d int) []diffOp {
	x, y := len(a), len(b)
	var rev []diffOp
	for ; d > 0; d-- {
		vs := trace[d] // V after step d-1, indexed k+d
		at := func(k int) int { return int(vs[k+d]) }
		k := x - y
		var pk int
		if k == -d || k != d && at(k-1) < at(k+1) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := at(pk)
		py := px - pk
		for x > px && y > py {
			x--
			y--
			rev = append(rev, diffOp{opEqual, a[x]})
		}
		if x == px {
			y--
			rev = append(rev, diffOp{opIns, b[y]})
		} else {
			x--
			rev = append(rev, diffOp{opDel, a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, diffOp{opEqual, a[x]})
	}
	ops := make([]diffOp, len(rev))
	for i, op := range rev {
		ops[len(rev)-1-i] = op
	}
	return ops
}

func formatHunks(b *cappedText, ops []diffOp) {
	oldLine, newLine := 1, 1
	for i := 0; i < len(ops); {
		if ops[i].kind == opEqual {
			oldLine++
			newLine++
			i++
			continue
		}
		start := max(i-diffContext, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != opEqual {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == opEqual {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(end+diffContext, len(ops))
				break
			}
			end = run
		}
		oStart, nStart := oldLine-(i-start), newLine-(i-start)
		var oCount, nCount int
		for _, op := range ops[start:end] {
			if op.kind != opIns {
				oCount++
			}
			if op.kind != opDel {
				nCount++
			}
		}
		b.WriteString("@@ -" + hunkRange(oStart, oCount) + " +" + hunkRange(nStart, nCount) + " @@\n")
		for _, op := range ops[start:end] {
			b.writeByte(byte(op.kind))
			b.WriteString(op.line)
			if !strings.HasSuffix(op.line, "\n") {
				b.WriteString("\n\\ No newline at end of file\n")
			}
		}
		for _, op := range ops[i:end] {
			if op.kind != opIns {
				oldLine++
			}
			if op.kind != opDel {
				newLine++
			}
		}
		i = end
	}
}

func hunkRange(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start-1)
	case 1:
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

// capCut cuts s, the first bytes of a total-byte text, to at most n bytes
// at a line boundary, with a marker.
func capCut(s string, n, total int) string {
	marker := fmt.Sprintf("… diff truncated (%s total)\n", humanSize(int64(total)))
	cut := s[:min(max(n-len(marker), 0), len(s))]
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		cut = cut[:i+1]
	}
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + marker
}
