package config

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestKnowledgeValidation(t *testing.T) {
	c := Default()
	if k := c.Knowledge; !k.Enabled || !k.AutoNotes || k.MaxFileKB != 256 || k.ContextBytes != 2048 {
		t.Fatalf("defaults = %+v", k)
	}
	c.Knowledge = KnowledgeCfg{MaxFileKB: 0, MaxFiles: 0, MaxRepoMB: 5000, NotesMaxBytes: 10, ContextBytes: -1}
	v := &vctx{layerOf: func(string) int { return 0 }}
	validate(c, v)
	var paths []string
	for _, e := range v.errs {
		paths = append(paths, e.path)
	}
	for _, p := range []string{"knowledge.max_file_kb", "knowledge.max_files", "knowledge.max_repo_mb",
		"knowledge.notes_max_bytes", "knowledge.context_bytes"} {
		if !slices.Contains(paths, p) {
			t.Errorf("no error for %s (got %v)", p, paths)
		}
	}
}

func TestKnowledgeFromFileIsHot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "knowledge: {enabled: false, context_bytes: 0}\n")
	m := load(t, dir)
	if k := m.Current().Knowledge; k.Enabled || k.ContextBytes != 0 || k.MaxFiles != 20000 {
		t.Fatalf("knowledge = %+v", k)
	}
	tg, err := resolvePath("knowledge.context_bytes")
	if err != nil || !tg.f.hot || tg.f.locked {
		t.Fatalf("knowledge.context_bytes = %+v %v, want a hot, unlocked key", tg, err)
	}
}
