package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const uiHeader = `# bothy.ui.yaml: settings saved from the web UI (Settings). This sparse overlay
# is the highest-precedence layer: defaults < bothy.yaml < env < this file.
# Each value notes who set it and when; locked keys are ignored here.`

// renderUI serialises the UI layer as a sparse overlay, annotating every value
// with "set by <who> at <time>" so provenance survives restarts.
func renderUI(ui *layer) ([]byte, error) {
	leaves := make([]*leaf, 0, len(ui.keys))
	for _, k := range ui.keys {
		leaves = append(leaves, ui.leaves[k])
	}
	root := assemble(leaves, ui.childKeysByPath, uiComment)
	if root == nil || len(root.Content) == 0 {
		root = &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, HeadComment: uiHeader, Content: []*yaml.Node{root}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (l *layer) childKeysByPath(ck string) []string {
	if c := l.items[ck]; c != nil {
		return c.keys
	}
	return nil
}

func uiComment(lf *leaf) string {
	if lf.f.isKey || lf.src.By == "" {
		return ""
	}
	s := "set by " + strings.Join(strings.Fields(lf.src.By), "_")
	if !lf.src.At.IsZero() {
		s += " at " + lf.src.At.UTC().Format(time.RFC3339)
	}
	return s
}

// writeFileAtomic replaces path with data via a 0600 temp file in the same
// directory: write, fsync, rename, then fsync the directory.
func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".bothy.ui.yaml.*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes the rename durable; directories cannot be opened for sync on
// every platform, so failures are ignored.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
