package knowledge

import (
	"errors"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestApplyNotesOp(t *testing.T) {
	base := "# acme/api\n\n## Summary\nAn API.\n\n### Detail\nMore.\n\n## Decisions\n- One.\n"
	got, err := applyNotesOp(base, notesOp{Repo: "acme/api", Op: opAppend, Section: "summary", Text: "Second line."})
	if err != nil {
		t.Fatal(err)
	}
	if want := "# acme/api\n\n## Summary\nAn API.\n\n### Detail\nMore.\nSecond line.\n\n## Decisions\n- One.\n"; got != want {
		t.Fatalf("append into a section with a subsection:\n%q\nwant\n%q", got, want)
	}
	got, _ = applyNotesOp(base, notesOp{Repo: "acme/api", Op: opAppend, Section: "Decisions", Text: "- Two."})
	if !strings.HasSuffix(got, "## Decisions\n- One.\n- Two.\n") {
		t.Fatalf("append at the end = %q", got)
	}
	got, _ = applyNotesOp("", notesOp{Repo: "acme/api", Op: opAppend, Section: "Build", Text: "make"})
	if got != "# acme/api\n\n## Build\nmake\n" {
		t.Fatalf("first append = %q", got)
	}
	for name, op := range map[string]notesOp{
		"empty text":        {Op: opAppend, Section: "S", Text: " \n"},
		"long text":         {Op: opAppend, Section: "S", Text: strings.Repeat("x", notesTextMax+1)},
		"multiline section": {Op: opAppend, Section: "A\nB", Text: "x"},
		"no section":        {Op: opAppend, Text: "x"},
		"empty old":         {Op: opReplace, Old: " ", New: "x"},
		"missing old":       {Op: opReplace, Old: "Nope", New: "x"},
		"ambiguous old":     {Op: opReplace, Old: "e.", New: "x"},
		"unknown op":        {Op: "delete"},
	} {
		if _, err := applyNotesOp(base, op); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	got, _ = applyNotesOp(base, notesOp{Op: opReplace, Old: "- One.\n", New: ""})
	if strings.Contains(got, "One") {
		t.Fatalf("replace with empty = %q", got)
	}
}

func TestNotesDiffAndSummary(t *testing.T) {
	d := notesDiff("acme/api", "a\nb\nc\n", "a\nB\nc\nd\n")
	if d != "--- acme/api/NOTES.md\n+++ acme/api/NOTES.md\n@@ -2,2 +2,3 @@\n-b\n-c\n+B\n+c\n+d\n" {
		t.Fatalf("diff = %q", d)
	}
	if d := notesDiff("acme/api", "a\nb\n", "a\nb\nc\n"); d != "--- acme/api/NOTES.md\n+++ acme/api/NOTES.md\n@@ -3,0 +3,1 @@\n+c\n" {
		t.Fatalf("append diff = %q", d)
	}
	if d := notesDiff("acme/api", "", strings.Repeat("x\n", 40000)); len(d) > notesDiffMax+40 || !strings.HasSuffix(d, "(diff truncated)\n") {
		t.Fatalf("big diff = %d bytes", len(d))
	}
	sum, more := summarize("# t\n\nalpha\nbeta\ngamma\n", 12)
	if sum != "alpha\nbeta" || !more {
		t.Fatalf("summarize = %q %v", sum, more)
	}
	sum, more = summarize("# t\n"+strings.Repeat("é", 100), 11)
	if !more || len(sum) > 10 || !strings.HasPrefix(sum, "éé") {
		t.Fatalf("cut first line = %q", sum)
	}
	if sum, more = summarize("# t\nall\n", 100); sum != "all" || more {
		t.Fatalf("fits = %q %v", sum, more)
	}
}

func TestCleanDraft(t *testing.T) {
	if got := cleanDraft("```md\n# acme/x\n## Summary\nHi.\x1b[31m\n```", "acme/api", 1000); got != "# acme/api\n\n## Summary\nHi.[31m\n" {
		t.Fatalf("cleanDraft = %q", got)
	}
	if got := cleanDraft("   ", "acme/api", 1000); got != "" {
		t.Fatalf("empty = %q", got)
	}
	long := "## Summary\n" + strings.Repeat("word word word\n", 200)
	if got := cleanDraft(long, "acme/api", 1024); len(got) > 1024 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("capped draft = %d bytes", len(got))
	}
}
