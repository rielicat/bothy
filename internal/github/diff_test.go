package github

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func textSide(s string, exec bool) side {
	sum := sha256.Sum256([]byte(s))
	return side{present: true, data: []byte(s), size: int64(len(s)), exec: exec, sha256: hex.EncodeToString(sum[:])}
}

func TestFileDiff(t *testing.T) {
	old := textSide("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n", false)
	cur := textSide("one\ntwo\nTHREE\nfour\nfive\nsix\nseven\neight\nnine", false)
	got, complete := fileDiff("src/a.txt", old, cur)
	want := "diff --git a/src/a.txt b/src/a.txt\n--- a/src/a.txt\n+++ b/src/a.txt\n" +
		"@@ -1,8 +1,9 @@\n one\n two\n-three\n+THREE\n four\n five\n six\n seven\n eight\n+nine\n\\ No newline at end of file\n"
	if got != want || !complete {
		t.Fatalf("modified diff (complete=%v):\n%s\nwant:\n%s", complete, got, want)
	}

	got, _ = fileDiff("new.sh", side{}, textSide("#!/bin/sh\necho hi\n", true))
	if !strings.HasPrefix(got, "diff --git a/new.sh b/new.sh\nnew file mode 100755\n--- /dev/null\n+++ b/new.sh\n@@ -0,0 +1,2 @@\n+#!/bin/sh\n+echo hi\n") {
		t.Errorf("added diff:\n%s", got)
	}
	got, _ = fileDiff("gone.txt", textSide("bye\n", false), side{})
	if got != "diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\n--- a/gone.txt\n+++ /dev/null\n(file deleted: 4 B)\n" {
		t.Errorf("deleted diff:\n%s", got)
	}
	got, _ = fileDiff("run.sh", textSide("x\n", false), textSide("x\n", true))
	if got != "diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n" {
		t.Errorf("mode-only diff:\n%s", got)
	}
	bin := textSide("PNG\x00\x01\x02", false)
	got, complete = fileDiff("logo.png", side{}, bin)
	if !complete || !strings.Contains(got, "Binary files /dev/null and b/logo.png differ (0 B → 6 B, new sha256 "+bin.sha256+")") {
		t.Errorf("binary diff (complete=%v):\n%s", complete, got)
	}
	big := side{present: true, data: []byte(strings.Repeat("a\n", 10)), size: maxDiffFile + 1, sha256: "x"}
	got, complete = fileDiff("big.txt", textSide("a\n", false), big)
	if complete || !strings.Contains(got, "(diff omitted: 2 B → 1.0 MB)") {
		t.Errorf("large diff (complete=%v):\n%s", complete, got)
	}
	unhashed := side{present: true, data: []byte("\x00"), size: 64 << 30}
	got, complete = fileDiff("huge.bin", side{}, unhashed)
	if complete || !strings.Contains(got, "(too large to compare: 0 B → 65536.0 MB)") || strings.Contains(got, "sha256") {
		t.Errorf("unhashed diff (complete=%v):\n%s", complete, got)
	}
}

func TestFileDiffSeparateHunks(t *testing.T) {
	lines := strings.Repeat("x\n", 20)
	got, _ := fileDiff("f", textSide("a\n"+lines+"b\n", false), textSide("A\n"+lines+"B\n", false))
	if strings.Count(got, "\n@@ ") != 2 || !strings.Contains(got, "@@ -1,4 +1,4 @@\n-a\n+A\n") ||
		!strings.Contains(got, "@@ -19,4 +19,4 @@\n x\n x\n x\n-b\n+B\n") {
		t.Fatalf("two hunks:\n%s", got)
	}
}

func TestDiffLinesLargeEditFallsBack(t *testing.T) {
	var a, b []string
	for i := 0; i < 3000; i++ {
		a = append(a, "a\n")
		b = append(b, "b\n")
	}
	ops := diffLines(a, b)
	if len(ops) != 6000 || ops[0].kind != '-' || ops[5999].kind != '+' {
		t.Fatalf("fallback ops = %d", len(ops))
	}
}
