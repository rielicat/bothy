package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// Approving keeps the files an admin saw, so a later change can be shown
// against them; the copy lives in the store, not next to the skill.
func TestEnableKeepsApprovedFiles(t *testing.T) {
	f := newFixture(t, 600)
	writeSkill(t, f.dir, "notes", "Take notes.", "Write notes.\n", map[string]string{"tpl.md": "v1", "bin/blob": "a\x00b"})
	l := f.open(t)
	ctx := context.Background()
	if err := l.SetEnabled(ctx, "notes", true, "", admin); err != nil {
		t.Fatal(err)
	}
	files, err := f.store.Skills().Approved(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]core.SkillFile{}
	for _, x := range files {
		got[x.Path] = x
	}
	if len(got) != 3 || !got[skillFile].Text || !strings.Contains(got[skillFile].Content, "Write notes.") || got["tpl.md"].Content != "v1" ||
		got["tpl.md"].SHA256 == "" || got["bin/blob"].Text || got["bin/blob"].Content != "" || got["bin/blob"].Size != 3 {
		t.Fatalf("approved files = %+v", files)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "notes", "tpl.md"), []byte("v2"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := l.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	// Disabling or drifting leaves the approved copy as it was.
	if err := l.SetEnabled(ctx, "notes", false, "", admin); err != nil {
		t.Fatal(err)
	}
	if c := approvedContent(t, f, "tpl.md"); c != "v1" {
		t.Fatalf("approved copy followed the edit: %q", c)
	}
	if err := l.SetEnabled(ctx, "notes", true, "", admin); err != nil {
		t.Fatal(err)
	}
	if c := approvedContent(t, f, "tpl.md"); c != "v2" {
		t.Fatalf("re-approval kept the old copy: %q", c)
	}
}

func approvedContent(t *testing.T, f *fixture, path string) string {
	t.Helper()
	files, err := f.store.Skills().Approved(context.Background(), "notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range files {
		if x.Path == path {
			return x.Content
		}
	}
	return ""
}
