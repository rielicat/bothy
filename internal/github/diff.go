package github

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	diffContext = 3
	// maxEdit bounds the Myers search; past it the changed middle is shown
	// as one replacement, still an exact diff.
	maxEdit = 1000
	// maxDiffFile is the largest file content a diff reads; larger changes
	// are summarised by size.
	maxDiffFile = 1 << 20
)

type diffOp struct {
	kind byte // ' ', '-', '+'
	line string
}

// side is one version of a file in a diff. data may be cut at
// maxDiffFile+1 bytes; size is the real size.
type side struct {
	present bool
	data    []byte
	size    int64
	exec    bool
	sha256  string
}

// fileDiff renders one file's change in git's format. complete is false
// when the content was not shown line by line (too large); binary files
// show sizes and the new content's hash, which identify the bytes exactly.
func fileDiff(p string, old, cur side) (text string, complete bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", p, p)
	switch {
	case !old.present:
		fmt.Fprintf(&b, "new file mode %s\n", gitMode(cur.exec))
	case !cur.present:
		fmt.Fprintf(&b, "deleted file mode %s\n--- a/%s\n+++ /dev/null\n(file deleted: %s)\n", gitMode(old.exec), p, humanSize(old.size))
		return b.String(), true
	case old.exec != cur.exec:
		fmt.Fprintf(&b, "old mode %s\nnew mode %s\n", gitMode(old.exec), gitMode(cur.exec))
	}
	if old.present && old.sha256 == cur.sha256 {
		return b.String(), true
	}
	oldName := "a/" + p
	if !old.present {
		oldName = "/dev/null"
	}
	switch {
	case isBinary(old.data) || isBinary(cur.data):
		fmt.Fprintf(&b, "Binary files %s and b/%s differ (%s → %s, new sha256 %s)\n", oldName, p,
			humanSize(old.size), humanSize(cur.size), cur.sha256)
		return b.String(), true
	case old.size > maxDiffFile || cur.size > maxDiffFile:
		fmt.Fprintf(&b, "--- %s\n+++ b/%s\n(diff omitted: %s → %s)\n", oldName, p, humanSize(old.size), humanSize(cur.size))
		return b.String(), false
	}
	fmt.Fprintf(&b, "--- %s\n+++ b/%s\n", oldName, p)
	formatHunks(&b, diffLines(splitLines(string(old.data)), splitLines(string(cur.data))))
	return b.String(), true
}

func gitMode(exec bool) string {
	if exec {
		return "100755"
	}
	return "100644"
}

func isBinary(b []byte) bool {
	if len(b) > 8<<10 {
		b = b[:8<<10]
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return true
	}
	// A rune cut at the end of the sniffed window is not a sign of binary.
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return !utf8.Valid(b)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
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
	if len(ma) > 0 && len(mb) > 0 && max(len(ma)-len(mb), len(mb)-len(ma)) <= maxEdit {
		mid, ok = myers(ma, mb)
	}
	ops := make([]diffOp, 0, len(a)+len(b)-pre-suf)
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{' ', l})
	}
	if ok {
		ops = append(ops, mid...)
	} else {
		for _, l := range ma {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, diffOp{'+', l})
		}
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

// myers is the O(ND) greedy algorithm with one V snapshot per edit
// distance, so memory grows with D² rather than with the input.
func myers(a, b []string) ([]diffOp, bool) {
	n, m := len(a), len(b)
	maxD := min(n+m, maxEdit)
	off := maxD + 1
	v := make([]int, 2*maxD+3)
	var trace [][]int32
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
		vs := trace[d]
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
			rev = append(rev, diffOp{' ', a[x]})
		}
		if x == px {
			y--
			rev = append(rev, diffOp{'+', b[y]})
		} else {
			x--
			rev = append(rev, diffOp{'-', a[x]})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, diffOp{' ', a[x]})
	}
	ops := make([]diffOp, len(rev))
	for i, op := range rev {
		ops[len(rev)-1-i] = op
	}
	return ops
}

func formatHunks(b *strings.Builder, ops []diffOp) {
	oldLine, newLine := 1, 1
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			oldLine++
			newLine++
			i++
			continue
		}
		start := max(i-diffContext, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
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
			if op.kind != '+' {
				oCount++
			}
			if op.kind != '-' {
				nCount++
			}
		}
		b.WriteString("@@ -" + hunkRange(oStart, oCount) + " +" + hunkRange(nStart, nCount) + " @@\n")
		for _, op := range ops[start:end] {
			b.WriteByte(op.kind)
			b.WriteString(op.line)
			if !strings.HasSuffix(op.line, "\n") {
				b.WriteString("\n\\ No newline at end of file\n")
			}
		}
		for _, op := range ops[i:end] {
			if op.kind != '+' {
				oldLine++
			}
			if op.kind != '-' {
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
