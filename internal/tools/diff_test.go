package tools

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     string
	}{
		{"identical", "a\n", "a\n", ""},
		{"change middle", "1\n2\n3\n4\n5\n6\n7\n8\n9\n", "1\n2\n3\n4\nFIVE\n6\n7\n8\n9\n",
			"--- a\n+++ b\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+FIVE\n 6\n 7\n 8\n"},
		{"insert at start", "b\nc\n", "a\nb\nc\n", "--- a\n+++ b\n@@ -1,2 +1,3 @@\n+a\n b\n c\n"},
		{"append", "a\n", "a\nb\n", "--- a\n+++ b\n@@ -1 +1,2 @@\n a\n+b\n"},
		{"create", "", "x\ny\n", "--- a\n+++ b\n@@ -0,0 +1,2 @@\n+x\n+y\n"},
		{"delete all", "x\n", "", "--- a\n+++ b\n@@ -1 +0,0 @@\n-x\n"},
		{"no newline", "a\nb", "a\nc", "--- a\n+++ b\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n"},
		{"newline added", "a", "a\n", "--- a\n+++ b\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+a\n"},
		{"binary", "a\x00", "b", "--- a\n+++ b\nBinary files differ\n"},
	}
	for _, c := range cases {
		if got := unifiedDiff("a", "b", []byte(c.old), []byte(c.new)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestUnifiedDiffHunks(t *testing.T) {
	var a, b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&a, "%d\n", i)
		switch i {
		case 5, 30:
			fmt.Fprintf(&b, "X%d\n", i)
		default:
			fmt.Fprintf(&b, "%d\n", i)
		}
	}
	got := unifiedDiff("a", "b", []byte(a.String()), []byte(b.String()))
	if strings.Count(got, "@@ -") != 2 || !strings.Contains(got, "@@ -2,7 +2,7 @@") || !strings.Contains(got, "@@ -27,7 +27,7 @@") {
		t.Errorf("two hunks expected:\n%s", got)
	}
	// Changes closer than twice the context merge into one hunk.
	near := strings.Replace(strings.Replace(a.String(), "\n5\n", "\nX5\n", 1), "\n10\n", "\nX10\n", 1)
	if got := unifiedDiff("a", "b", []byte(a.String()), []byte(near)); strings.Count(got, "@@ -") != 1 || !strings.Contains(got, "+X10") {
		t.Errorf("one hunk expected:\n%s", got)
	}
}

// applyDiff replays a diff's -/+ lines over old to check it is exact.
func applyDiff(t *testing.T, old, diff string) string {
	t.Helper()
	oldLines := splitLines(old)
	var out []string
	pos := 0
	lines := strings.SplitAfter(diff, "\n")
	for i := 2; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "@@ -"):
			var start, count int
			if _, err := fmt.Sscanf(l, "@@ -%d,%d", &start, &count); err != nil {
				if _, err := fmt.Sscanf(l, "@@ -%d", &start); err != nil {
					t.Fatalf("bad hunk header %q", l)
				}
			}
			if count == 0 && strings.Contains(l, ",0 ") {
				start++
			}
			for pos < start-1 {
				out = append(out, oldLines[pos])
				pos++
			}
		case strings.HasPrefix(l, " "):
			out = append(out, oldLines[pos])
			pos++
		case strings.HasPrefix(l, "-"):
			pos++
		case strings.HasPrefix(l, "+"):
			text := l[1:]
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\\ No newline") {
				text = strings.TrimSuffix(text, "\n")
				i++
			}
			out = append(out, text)
		}
	}
	out = append(out, oldLines[pos:]...)
	return strings.Join(out, "")
}

func TestUnifiedDiffIsExact(t *testing.T) {
	pairs := [][2]string{
		{"a\nb\nc\nd\ne\nf\ng\nh\n", "a\nB\nc\nd\ne\nf\nG\nh\ni\n"},
		{strings.Repeat("same\n", 50) + "x\n", "y\n" + strings.Repeat("same\n", 50)},
		{"1\n2\n3\n", "3\n2\n1\n"},
		{"", "only\n"},
	}
	for _, p := range pairs {
		d := unifiedDiff("a", "b", []byte(p[0]), []byte(p[1]))
		if got := applyDiff(t, p[0], d); got != p[1] {
			t.Errorf("diff does not reproduce the new text:\n%s\n got %q\nwant %q", d, got, p[1])
		}
	}
}

func TestUnifiedDiffLimits(t *testing.T) {
	var a, b strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&a, "line %d\n", i)
		fmt.Fprintf(&b, "other %d\n", i)
	}
	got := unifiedDiff("a", "b", []byte(a.String()), []byte(b.String()))
	if len(got) > maxDiff || !strings.Contains(got, "diff truncated") {
		t.Errorf("cap: %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "--- a\n+++ b\n@@ -1,5000 +1,5000 @@\n-line 0\n") {
		t.Errorf("fallback replacement: %q", got[:80])
	}
	large := strings.Repeat("x", maxDiffInput)
	if got := unifiedDiff("a", "b", []byte(large), []byte("y")); !strings.Contains(got, "diff omitted") {
		t.Errorf("large input: %q", got)
	}
}

// TestUnifiedDiffMemory bounds what one diff allocates: Plan computes it for
// every fs_write and fs_delete, auto-allowed ones included, under the
// gateway's 48 MiB memory limit.
func TestUnifiedDiffMemory(t *testing.T) {
	var old strings.Builder
	for i := 0; old.Len() < maxDiffInput-40<<10; i++ {
		fmt.Fprintf(&old, "%08d,some,data,row,with,several,columns,%d\n", i, i*7)
	}
	o := []byte(old.String())
	lines := splitLines(old.String())
	lines = lines[:len(lines)/2]
	half := []byte(strings.Join(lines, ""))
	for i := 0; i < len(lines); i += len(lines) / 500 {
		lines[i] = "changed\n"
	}
	scattered := []byte(strings.Join(lines, ""))
	cases := map[string][2][]byte{
		"overwrite": {o, []byte(strings.Repeat("fresh content line\n", 30<<10/19))},
		"delete":    {o, nil},
		"create":    {nil, o},
		"scattered": {half, scattered},
	}
	for name, c := range cases {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		d := unifiedDiff("a", "b", c[0], c[1])
		runtime.ReadMemStats(&after)
		if got := after.TotalAlloc - before.TotalAlloc; got > 16<<20 {
			t.Errorf("%s: one diff allocated %d MB", name, got>>20)
		}
		if len(d) > maxDiff || !strings.Contains(d, "diff truncated") {
			t.Errorf("%s: %d bytes, truncated %v", name, len(d), strings.Contains(d, "diff truncated"))
		}
	}
}
