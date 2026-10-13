package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rielicat/bothy/internal/core"
)

// toolDefinition is the pinned part of an MCP tool: exactly the fields the
// pin hash covers. Annotations are spelled out here rather than taken from the
// SDK type so that its marshalling options cannot change a hash.
type toolDefinition struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  *annotations    `json:"annotations,omitempty"`
}

type annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// definitionOf converts a listed tool and returns its canonical JSON (the pin
// Definition) and the hex sha256 of those bytes (the pin Hash).
func definitionOf(t *mcpsdk.Tool) (*toolDefinition, []byte, string, error) {
	in, err := schemaJSON(t.InputSchema)
	if err != nil {
		return nil, nil, "", fmt.Errorf("tool %q input schema: %w", t.Name, err)
	}
	if in == nil {
		in = json.RawMessage(`{"type":"object"}`)
	}
	out, err := schemaJSON(t.OutputSchema)
	if err != nil {
		return nil, nil, "", fmt.Errorf("tool %q output schema: %w", t.Name, err)
	}
	d := &toolDefinition{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: in, OutputSchema: out}
	if a := t.Annotations; a != nil {
		d.Annotations = &annotations{Title: a.Title, ReadOnlyHint: a.ReadOnlyHint, IdempotentHint: a.IdempotentHint,
			DestructiveHint: a.DestructiveHint, OpenWorldHint: a.OpenWorldHint}
	}
	canon, err := core.CanonicalJSON(d)
	if err != nil {
		return nil, nil, "", fmt.Errorf("tool %q: %w", t.Name, err)
	}
	sum := sha256.Sum256(canon)
	return d, canon, hex.EncodeToString(sum[:]), nil
}

func schemaJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil || bytes.Equal(b, []byte("null")) {
		return nil, err
	}
	return core.CanonicalJSON(b)
}

func parseDefinition(raw json.RawMessage) (*toolDefinition, error) {
	var d toolDefinition
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// annotationFloor is the least side effect a tool's own annotations imply.
// Annotations come from the server and are untrusted, so they may raise the
// class an admin or config picked but never lower it; only explicit hints
// count (the MCP defaults would make nearly every tool destructive).
func (d *toolDefinition) annotationFloor() core.SideEffect {
	a := d.Annotations
	switch {
	case a == nil || a.ReadOnlyHint:
		return core.SERead
	case a.DestructiveHint != nil && *a.DestructiveHint:
		return core.SEIrreversible
	case a.OpenWorldHint != nil && *a.OpenWorldHint:
		return core.SEExternalSend
	}
	return core.SERead
}

// toolName maps an MCP tool to its registry name, mcp__<server>__<tool>.
// Characters providers reject (MCP allows ".") become "_"; such a name, or
// one over 64 characters, ends in "_" + the first 8 hex of sha256 of the full
// unmodified name so distinct tools keep distinct names.
func toolName(server, tool string) string {
	full := "mcp__" + server + "__" + tool
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, full)
	if name == full && len(full) <= 64 {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "_" + hex.EncodeToString(sum[:4])
	if len(name) > 55 {
		name = name[:55]
	}
	return name + suffix
}

// fieldChange is one top-level difference between two pinned definitions.
type fieldChange struct {
	Field string          `json:"field"`
	Old   json.RawMessage `json:"old,omitempty"`
	New   json.RawMessage `json:"new,omitempty"`
}

const maxDiffValue = 2048

// diffDefinitions compares two canonical definitions field by field, the
// form the audit log and the review UI show for a changed tool.
func diffDefinitions(old, cur json.RawMessage) []fieldChange {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(old, &a) != nil || json.Unmarshal(cur, &b) != nil {
		return []fieldChange{{Field: "*", Old: clip(old), New: clip(cur)}}
	}
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []fieldChange
	for _, k := range keys {
		if !bytes.Equal(a[k], b[k]) {
			out = append(out, fieldChange{Field: k, Old: clip(a[k]), New: clip(b[k])})
		}
	}
	return out
}

// clip keeps a value valid JSON while bounding its size.
func clip(v json.RawMessage) json.RawMessage {
	if len(v) <= maxDiffValue {
		return v
	}
	s, err := json.Marshal(string(v[:maxDiffValue]) + "…")
	if err != nil {
		return nil
	}
	return s
}
