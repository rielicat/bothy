package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
)

// schemaNode is the part of config.Manager.Schema the renderer needs.
type schemaNode struct {
	Properties           map[string]*schemaNode `json:"properties"`
	Items                *schemaNode            `json:"items"`
	AdditionalProperties json.RawMessage        `json:"additionalProperties"` // false or a schema
}

// item is the schema of a keyed-list item or map value with fields, or nil.
func (s *schemaNode) item() *schemaNode {
	if s == nil {
		return nil
	}
	if s.Items != nil && s.Items.Properties != nil {
		return s.Items
	}
	if len(s.AdditionalProperties) > 0 && s.AdditionalProperties[0] == '{' {
		var ap schemaNode
		if json.Unmarshal(s.AdditionalProperties, &ap) == nil && ap.Properties != nil {
			return &ap
		}
	}
	return nil
}

type yamlNode struct {
	entry  *config.Entry
	schema *schemaNode
	keys   []string // insertion order, which is the entries' order
	kids   map[string]*yamlNode
}

// effectiveYAML renders effective-config leaves as YAML with a source comment
// on every value, like Settings → Advanced (web/src/lib/yaml.ts). Under
// keyed-list and map nodes the item key is everything up to the first known
// field name, so ids that contain dots (model refs in pricing) stay whole.
func effectiveYAML(entries []config.Entry, schema json.RawMessage) (string, error) {
	var root schemaNode
	if err := json.Unmarshal(schema, &root); err != nil {
		return "", fmt.Errorf("config schema: %w", err)
	}
	top := &yamlNode{schema: &root}
	for i := range entries {
		segs := strings.Split(entries[i].Path, ".")
		n := top
		for j := 0; j < len(segs); j++ {
			item := n.schema.item()
			key := segs[j]
			if item != nil {
				for k := j + 1; k < len(segs); k++ {
					if item.Properties[segs[k]] != nil {
						key, j = strings.Join(segs[j:k], "."), k-1
						break
					}
				}
			}
			next := n.kids[key]
			if next == nil {
				next = &yamlNode{schema: item}
				if item == nil && n.schema != nil {
					next.schema = n.schema.Properties[key]
				}
				if n.kids == nil {
					n.kids = map[string]*yamlNode{}
				}
				n.kids[key] = next
				n.keys = append(n.keys, key)
			}
			n = next
		}
		n.entry = &entries[i]
	}
	var b strings.Builder
	top.write(&b, "")
	return b.String(), nil
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

func (n *yamlNode) write(b *strings.Builder, indent string) {
	list := n.schema != nil && n.schema.Items != nil && n.schema.Items.Properties != nil
	for _, k := range n.keys {
		c := n.kids[k]
		key := k
		if !plainKey.MatchString(k) {
			q, _ := json.Marshal(k)
			key = string(q)
		}
		switch {
		case list:
			fmt.Fprintf(b, "%s- # %s\n", indent, k)
			c.write(b, indent+"  ")
		case c.entry != nil && len(c.kids) == 0:
			v := string(c.entry.Value)
			if v == "" {
				v = "null"
			}
			fmt.Fprintf(b, "%s%s: %s  # %s\n", indent, key, v, sourceComment(*c.entry))
		default:
			fmt.Fprintf(b, "%s%s:\n", indent, key)
			c.write(b, indent+"  ")
		}
	}
}

func sourceComment(e config.Entry) string {
	s := e.Source
	var c string
	switch s.Layer {
	case config.LayerFile, config.LayerUI:
		c = string(s.Layer)
		if s.File != "" {
			c += " " + s.File
			if s.Line > 0 {
				c += fmt.Sprintf(":%d", s.Line)
			}
		}
		if s.By != "" {
			c += " by " + s.By
		}
		if !s.At.IsZero() {
			c += " at " + s.At.Format(time.RFC3339)
		}
	case config.LayerEnv:
		c = "env " + s.EnvVar
	default:
		c = string(config.LayerDefault)
	}
	if e.Locked {
		c += ", locked"
	}
	return c
}
