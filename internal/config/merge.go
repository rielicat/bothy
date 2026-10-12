package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

type cand struct {
	layer int
	lf    *leaf
	bad   bool
}

// slot gathers every layer's value for one leaf path.
type slot struct {
	key    string
	segs   []string
	f      *field
	cands  []*cand // highest layer first
	chosen *cand
}

type merger struct {
	layers  []*layer
	slots   map[string]*slot
	order   []string
	items   map[string][]string
	locked  func([]string, *field) bool
	errs    []issue
	errSeen map[string]bool
}

// maxPasses bounds the validate-and-demote loop; every pass that continues
// demotes at least one candidate, so this is only a guard.
const maxPasses = 64

func newMerger(layers []*layer, locked func([]string, *field) bool) *merger {
	mg := &merger{layers: layers, slots: map[string]*slot{}, items: map[string][]string{}, locked: locked, errSeen: map[string]bool{}}
	for li, l := range layers {
		for _, k := range l.keys {
			lf := l.leaves[k]
			s := mg.slots[k]
			if s == nil {
				s = &slot{key: k, segs: lf.segs, f: lf.f}
				mg.slots[k] = s
				mg.order = append(mg.order, k)
			}
			s.cands = append([]*cand{{layer: li, lf: lf}}, s.cands...)
		}
		// Lower layers keep their item order; higher layers only append new
		// items, so overriding one field never reorders policy rules.
		for ck, c := range l.items {
			for _, id := range c.keys {
				if !slices.Contains(mg.items[ck], id) {
					mg.items[ck] = append(mg.items[ck], id)
				}
			}
		}
	}
	return mg
}

func (mg *merger) run() *Config {
	mg.screen()
	var cfg *Config
	for pass := 0; ; pass++ {
		mg.choose()
		cfg = mg.decode()
		if pass == maxPasses {
			break
		}
		v := &vctx{layerOf: mg.layerOf}
		validate(cfg, v)
		progress := false
		for _, e := range v.errs {
			if mg.apply(e) {
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	return cfg
}

// screen rejects candidates that cannot be a value at all: wrong type,
// plaintext secrets, and UI overrides of locked keys.
func (mg *merger) screen() {
	for _, k := range mg.order {
		s := mg.slots[k]
		for _, c := range s.cands {
			if c.lf.container {
				continue
			}
			var msg string
			if mg.layers[c.layer].kind == LayerUI && mg.locked(s.segs, s.f) {
				msg = "locked: the bothy.ui.yaml override is ignored"
			} else if _, err := leafValue(s.f, c.lf.node); err != nil {
				msg = typeMessage(s.f, c.lf.node)
			} else if s.f.secret {
				msg = secretMessage(s.segs, s.f, c.lf.node)
			}
			if msg != "" {
				c.bad = true
				mg.addErr(newIssue(k, "", c.lf.src, msg))
			}
		}
	}
}

func (mg *merger) choose() {
	for _, s := range mg.slots {
		s.chosen = nil
		if s.f.isKey {
			// An item's id belongs to the lowest layer that defines the item.
			for i := len(s.cands) - 1; i >= 0; i-- {
				if !s.cands[i].bad {
					s.chosen = s.cands[i]
					break
				}
			}
			continue
		}
		for _, c := range s.cands {
			if !c.bad {
				s.chosen = c
				break
			}
		}
	}
}

func (mg *merger) layerOf(path string) int {
	if s := mg.slots[path]; s != nil && s.chosen != nil {
		return s.chosen.layer
	}
	return -1
}

func (mg *merger) addErr(is issue) {
	k := is.id + "\x00" + where(is.Source)
	if !mg.errSeen[k] {
		mg.errSeen[k] = true
		mg.errs = append(mg.errs, is)
	}
}

// apply records a validation error and demotes the offending value to the
// next lower layer. Item errors drop every non-default contribution to the
// item. Built-in defaults are never demoted.
func (mg *merger) apply(e verr) bool {
	suffix := ""
	if e.idx >= 0 {
		suffix = fmt.Sprintf("[%d]", e.idx)
	}
	if e.item {
		var best *cand
		progress := false
		for _, k := range mg.order {
			if k != e.path && !strings.HasPrefix(k, e.path+".") {
				continue
			}
			for _, c := range mg.slots[k].cands {
				if c.bad || mg.layers[c.layer].kind == LayerDefault {
					continue
				}
				if best == nil || c.layer > best.layer || (c.layer == best.layer && c.lf.f.isKey) {
					best = c
				}
				c.bad = true
				progress = true
			}
		}
		src := Source{Layer: LayerDefault}
		if best != nil {
			src = best.lf.src
		}
		mg.addErr(newIssue(e.path, suffix, src, e.msg))
		return progress
	}
	s := mg.slots[e.path]
	if s == nil || s.chosen == nil {
		mg.addErr(newIssue(e.path, suffix, Source{Layer: LayerDefault}, e.msg))
		return false
	}
	src := s.chosen.lf.src
	if n := deref(s.chosen.lf.node); e.idx >= 0 && n != nil && e.idx < len(n.Content) && n.Content[e.idx].Line > 0 && src.Line > 0 {
		src.Line = n.Content[e.idx].Line
	}
	mg.addErr(newIssue(e.path, suffix, src, e.msg))
	if mg.layers[s.chosen.layer].kind == LayerDefault {
		return false
	}
	s.chosen.bad = true
	return true
}

func (mg *merger) decode() *Config {
	var chosen []*leaf
	for _, k := range mg.order {
		if s := mg.slots[k]; s.chosen != nil {
			chosen = append(chosen, s.chosen.lf)
		}
	}
	n := assemble(chosen, func(ck string) []string { return mg.items[ck] }, nil)
	cfg, err := decodeStrict(n)
	if err != nil {
		mg.addErr(newIssue("", "", Source{Layer: LayerDefault}, "internal: "+err.Error()))
	}
	return cfg
}

func decodeStrict(n *yaml.Node) (*Config, error) {
	c := &Config{}
	if n == nil {
		return c, nil
	}
	b, err := yaml.Marshal(n)
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return c, err
	}
	return c, nil
}

// entries lists every leaf of cfg in schema order with its provenance.
func (mg *merger) entries(cfg *Config) []Entry {
	fl := configLayer(cfg)
	out := make([]Entry, 0, len(fl.keys))
	for _, k := range fl.keys {
		lf := fl.leaves[k]
		e := Entry{
			Path:     k,
			Value:    leafJSON(lf.f, lf.node),
			Source:   Source{Layer: LayerDefault},
			Shadowed: []Shadow{},
			Locked:   mg.locked(lf.segs, lf.f),
			Hot:      lf.f.hot,
			Secret:   lf.f.secret,
		}
		if s := mg.slots[k]; s != nil && s.chosen != nil {
			e.Source = s.chosen.lf.src
			if !s.f.isKey {
				below := false
				for _, c := range s.cands {
					if below {
						e.Shadowed = append(e.Shadowed, Shadow{Source: c.lf.src, Value: shadowJSON(s, c)})
					}
					below = below || c == s.chosen
				}
			}
		}
		out = append(out, e)
	}
	return out
}

func shadowJSON(s *slot, c *cand) json.RawMessage {
	if c.bad && s.f.secret {
		return json.RawMessage(`"[redacted]"`)
	}
	if _, err := leafValue(s.f, c.lf.node); err != nil {
		b, _ := json.Marshal(deref(c.lf.node).Value)
		return b
	}
	return leafJSON(s.f, c.lf.node)
}

func leafValue(f *field, n *yaml.Node) (any, error) {
	v := reflect.New(f.typ)
	if n != nil {
		if err := n.Decode(v.Interface()); err != nil {
			return nil, err
		}
	}
	return v.Elem().Interface(), nil
}

func leafJSON(f *field, n *yaml.Node) json.RawMessage {
	v, err := leafValue(f, n)
	if err != nil {
		return json.RawMessage("null")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// subtreeValue converts a node to plain JSON-ready values keyed by YAML names.
func subtreeValue(f *field, n *yaml.Node) any {
	n = deref(n)
	if isNull(n) {
		return nil
	}
	switch f.kind {
	case kStruct:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if c := f.byName[n.Content[i].Value]; c != nil {
				m[c.name] = subtreeValue(c, n.Content[i+1])
			}
		}
		return m
	case kKeyedList:
		out := []any{}
		for _, it := range n.Content {
			out = append(out, subtreeValue(f.elem, it))
		}
		return out
	case kMap:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i].Value] = subtreeValue(f.elem, n.Content[i+1])
		}
		return m
	}
	v, err := leafValue(f, n)
	if err != nil {
		return n.Value
	}
	return v
}

// valueAt returns the JSON value of the subtree at segs in c (null if absent).
func valueAt(c *Config, segs []string) json.RawMessage {
	var n yaml.Node
	if err := n.Encode(c); err != nil {
		return json.RawMessage("null")
	}
	cur, f := &n, rootField()
	for _, s := range segs {
		var next *yaml.Node
		switch f.kind {
		case kStruct:
			_, next = lookup(cur, s)
			f = f.byName[s]
		case kKeyedList:
			for _, it := range cur.Content {
				if _, kv := lookup(it, f.key); kv != nil && kv.Value == s {
					next = it
				}
			}
			f = f.elem
		case kMap:
			_, next = lookup(cur, s)
			f = f.elem
		}
		if next == nil || f == nil {
			return json.RawMessage("null")
		}
		cur = next
	}
	b, err := json.Marshal(subtreeValue(f, cur))
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

type tnode struct {
	kids map[string]*tnode
	lf   *leaf
}

// assemble rebuilds a YAML tree from leaves. order gives keyed-list item order
// per container key; comment, when set, annotates each leaf value.
func assemble(leaves []*leaf, order func(string) []string, comment func(*leaf) string) *yaml.Node {
	root := &tnode{}
	for _, lf := range leaves {
		t := root
		for _, s := range lf.segs {
			if t.kids == nil {
				t.kids = map[string]*tnode{}
			}
			if t.kids[s] == nil {
				t.kids[s] = &tnode{}
			}
			t = t.kids[s]
		}
		t.lf = lf
	}
	return root.node(rootField(), nil, order, comment)
}

func (t *tnode) node(f *field, segs []string, order func(string) []string, comment func(*leaf) string) *yaml.Node {
	if len(t.kids) == 0 {
		if t.lf == nil {
			return nil
		}
		n := cloneNode(t.lf.node)
		if n.Kind == yaml.SequenceNode || n.Kind == yaml.MappingNode {
			n.Style = yaml.FlowStyle
		}
		if comment != nil {
			if c := comment(t.lf); c != "" {
				n.LineComment = "# " + c
			}
		}
		return n
	}
	switch f.kind {
	case kStruct:
		m := &yaml.Node{Kind: yaml.MappingNode}
		for _, c := range f.fields {
			if k := t.kids[c.name]; k != nil {
				if v := k.node(c, appendSeg(segs, c.name), order, comment); v != nil {
					m.Content = append(m.Content, strNode(c.name), v)
				}
			}
		}
		return m
	case kKeyedList:
		s := &yaml.Node{Kind: yaml.SequenceNode}
		var ids []string
		for _, id := range order(pathKey(segs)) {
			if t.kids[id] != nil {
				ids = append(ids, id)
			}
		}
		for _, id := range sortedKeys(t.kids) {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			item := t.kids[id].node(f.elem, appendSeg(segs, id), order, comment)
			if item == nil || item.Kind != yaml.MappingNode {
				continue
			}
			if _, kv := lookup(item, f.key); kv == nil {
				item.Content = append([]*yaml.Node{strNode(f.key), strNode(id)}, item.Content...)
			}
			s.Content = append(s.Content, item)
		}
		return s
	case kMap:
		m := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range sortedKeys(t.kids) {
			if v := t.kids[k].node(f.elem, appendSeg(segs, k), order, comment); v != nil {
				m.Content = append(m.Content, strNode(k), v)
			}
		}
		return m
	}
	return nil
}

func sortedKeys(m map[string]*tnode) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func cloneNode(n *yaml.Node) *yaml.Node {
	n = deref(n)
	c := &yaml.Node{Kind: n.Kind, Style: n.Style, Tag: n.Tag, Value: n.Value}
	for _, x := range n.Content {
		c.Content = append(c.Content, cloneNode(x))
	}
	return c
}
