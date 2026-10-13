package wire

import (
	"fmt"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

// FileNote stands in for an attachment a wire format cannot carry inline, so
// the model still learns it exists and where the file tools can read it.
func FileNote(b *core.Blob) string {
	if b == nil {
		return "[attachment]"
	}
	name := b.Name
	if name == "" {
		name = b.Ref
	}
	return fmt.Sprintf("[attached file %s (%s, %d bytes) at %s]", name, b.MIME, b.Size, b.Ref)
}

// IsImage reports whether p is an image whose bytes were loaded.
func IsImage(p core.Part) bool {
	return p.Type == core.PartImage && p.Blob != nil && len(p.Blob.Data) > 0 && strings.HasPrefix(p.Blob.MIME, "image/")
}

// SystemText joins the system blocks for formats that take one system string.
func SystemText(blocks []core.SystemBlock) string {
	var b strings.Builder
	for _, s := range blocks {
		if s.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Text)
	}
	return b.String()
}

// MergeModels appends the models of extra whose ids base does not have.
func MergeModels(base, extra []core.ModelInfo) []core.ModelInfo {
	seen := make(map[string]bool, len(base)+len(extra))
	for _, m := range base {
		seen[m.Ref.Model] = true
	}
	for _, m := range extra {
		if !seen[m.Ref.Model] {
			seen[m.Ref.Model] = true
			base = append(base, m)
		}
	}
	return base
}
