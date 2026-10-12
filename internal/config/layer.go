package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// leaf is one addressable value contributed by a layer: a scalar, a scalar
// list, or an empty (or null) container that has no leaves of its own.
type leaf struct {
	segs      []string
	key       string
	f         *field
	node      *yaml.Node
	src       Source
	container bool
}

type children struct {
	segs []string
	keys []string
}

type layer struct {
	kind   Layer
	leaves map[string]*leaf
	keys   []string             // leaf keys in document order
	items  map[string]*children // keyed-list ids and map keys per container, in document order
	errs   []issue
}

func newLayer(k Layer) *layer {
	return &layer{kind: k, leaves: map[string]*leaf{}, items: map[string]*children{}}
}

func (l *layer) put(lf *leaf) {
	if _, ok := l.leaves[lf.key]; !ok {
		l.keys = append(l.keys, lf.key)
	}
	l.leaves[lf.key] = lf
}

func (l *layer) addChild(segs []string, k string) {
	ck := pathKey(segs)
	c := l.items[ck]
	if c == nil {
		c = &children{segs: slices.Clone(segs)}
		l.items[ck] = c
	}
	if !slices.Contains(c.keys, k) {
		c.keys = append(c.keys, k)
	}
}

func (l *layer) childKeys(segs []string) []string {
	if c := l.items[pathKey(segs)]; c != nil {
		return c.keys
	}
	return nil
}

func (l *layer) clone() *layer {
	c := newLayer(l.kind)
	c.keys = slices.Clone(l.keys)
	maps.Copy(c.leaves, l.leaves)
	for k, v := range l.items {
		c.items[k] = &children{segs: v.segs, keys: slices.Clone(v.keys)}
	}
	c.errs = slices.Clone(l.errs)
	return c
}

// removeUnder drops every leaf at or below segs and reports whether any existed.
func (l *layer) removeUnder(segs []string) bool {
	removed := false
	l.keys = slices.DeleteFunc(l.keys, func(k string) bool {
		if hasPrefix(l.leaves[k].segs, segs) {
			delete(l.leaves, k)
			removed = true
			return true
		}
		return false
	})
	for k, c := range l.items {
		switch {
		case hasPrefix(c.segs, segs):
			delete(l.items, k)
		case len(segs) == len(c.segs)+1 && hasPrefix(segs, c.segs):
			last := segs[len(segs)-1]
			c.keys = slices.DeleteFunc(c.keys, func(x string) bool { return x == last })
		}
	}
	return removed
}

// issue is a FieldError plus a location-free identity used to tell new
// validation failures from ones that already existed.
type issue struct {
	FieldError
	id string
}

func newIssue(path, suffix string, src Source, msg string) issue {
	display := path + suffix
	m := where(src) + ": "
	if display != "" {
		m += display + ": "
	}
	return issue{
		FieldError: FieldError{Path: path, Source: src, Message: m + msg},
		id:         string(src.Layer) + "\x00" + src.EnvVar + "\x00" + display + "\x00" + msg,
	}
}

func where(s Source) string {
	switch s.Layer {
	case LayerFile, LayerUI:
		name := filepath.Base(s.File)
		if s.File == "" {
			name = "bothy.ui.yaml"
			if s.Layer == LayerFile {
				name = "bothy.yaml"
			}
		}
		if s.Line > 0 {
			return name + ":" + strconv.Itoa(s.Line)
		}
		return name
	case LayerEnv:
		return "env " + s.EnvVar
	default:
		return "default"
	}
}

type walker struct {
	l   *layer
	src func(key, val *yaml.Node) Source
}

func (w *walker) errf(segs []string, suffix string, key, val *yaml.Node, format string, a ...any) {
	w.l.errs = append(w.l.errs, newIssue(pathKey(segs), suffix, w.src(key, val), fmt.Sprintf(format, a...)))
}

func (w *walker) leaf(segs []string, f *field, n, key *yaml.Node, container bool) {
	w.l.put(&leaf{segs: segs, key: pathKey(segs), f: f, node: n, src: w.src(key, n), container: container})
}

func (w *walker) walk(n *yaml.Node, f *field, segs []string, key *yaml.Node) {
	n = deref(n)
	if isNull(n) {
		switch {
		case f.kind == kScalar:
			w.leaf(segs, f, n, key, false)
		case f.isPtrStruct():
			w.leaf(segs, f, n, key, true)
		}
		return
	}
	switch f.kind {
	case kScalar:
		if n.Kind != yaml.ScalarNode {
			w.errf(segs, "", key, n, "expected a single value, got a %s", kindName(n))
			return
		}
		w.leaf(segs, f, n, key, false)
	case kScalarList:
		if n.Kind != yaml.SequenceNode {
			w.errf(segs, "", key, n, "expected a list, got a %s", kindName(n))
			return
		}
		for i, e := range n.Content {
			if e = deref(e); e.Kind != yaml.ScalarNode {
				w.errf(segs, fmt.Sprintf("[%d]", i), key, e, "expected a single value, got a %s", kindName(e))
				return
			}
		}
		w.leaf(segs, f, n, key, false)
	case kStruct:
		if n.Kind != yaml.MappingNode {
			w.errf(segs, "", key, n, "expected a mapping, got a %s", kindName(n))
			return
		}
		w.mapping(n, segs, func(k, v *yaml.Node) {
			c := f.byName[k.Value]
			if c == nil {
				w.errf(appendSeg(segs, k.Value), "", k, nil, "unknown key%s", didYouMean(k.Value, f.names))
				return
			}
			w.walk(v, c, appendSeg(segs, c.name), k)
		})
	case kKeyedList:
		if n.Kind != yaml.SequenceNode {
			w.errf(segs, "", key, n, "expected a list, got a %s", kindName(n))
			return
		}
		if len(n.Content) == 0 {
			w.leaf(segs, f, n, key, true)
			return
		}
		seen := map[string]bool{}
		for i, it := range n.Content {
			if it = deref(it); it.Kind != yaml.MappingNode {
				w.errf(segs, fmt.Sprintf("[%d]", i), nil, it, "expected a mapping with %q, got a %s", f.key, kindName(it))
				continue
			}
			kk, kv := lookup(it, f.key)
			if kv = deref(kv); kv == nil || kv.Kind != yaml.ScalarNode || isNull(kv) || kv.Value == "" {
				w.errf(segs, fmt.Sprintf("[%d]", i), nil, it, "list item has no %q", f.key)
				continue
			}
			if seen[kv.Value] {
				w.errf(appendSeg(segs, kv.Value), "", kk, kv, "duplicate %s %q", f.key, kv.Value)
				continue
			}
			seen[kv.Value] = true
			w.item(it, f, appendSeg(segs, kv.Value), kk)
		}
	case kMap:
		if n.Kind != yaml.MappingNode {
			w.errf(segs, "", key, n, "expected a mapping, got a %s", kindName(n))
			return
		}
		if len(n.Content) == 0 {
			w.leaf(segs, f, n, key, true)
			return
		}
		w.mapping(n, segs, func(k, v *yaml.Node) {
			if k.Value == "" {
				w.errf(segs, "", k, nil, "empty key")
				return
			}
			w.l.addChild(segs, k.Value)
			w.walk(v, f.elem, appendSeg(segs, k.Value), k)
		})
	}
}

func (w *walker) item(n *yaml.Node, list *field, segs []string, key *yaml.Node) {
	w.l.addChild(segs[:len(segs)-1], segs[len(segs)-1])
	w.walk(n, list.elem, segs, key)
}

// target walks n as the value of a resolved path; an item path accepts a
// mapping without its id field.
func (w *walker) target(t target, n *yaml.Node) {
	if !t.item {
		w.walk(n, t.f, t.segs, nil)
		return
	}
	n = deref(n)
	id := t.segs[len(t.segs)-1]
	if n.Kind == yaml.MappingNode {
		_, kv := lookup(n, t.parent.key)
		if kv = deref(kv); kv != nil && kv.Value != id {
			w.errf(t.segs, "", nil, kv, "%s %q does not match the path", t.parent.key, kv.Value)
			return
		}
		if kv == nil {
			c := *n
			c.Content = append([]*yaml.Node{strNode(t.parent.key), strNode(id)}, n.Content...)
			n = &c
		}
	}
	w.item(n, t.parent, t.segs, nil)
}

// mapping visits key/value pairs; YAML merge keys apply first so explicit keys
// win, and duplicate explicit keys are reported.
func (w *walker) mapping(n *yaml.Node, segs []string, fn func(k, v *yaml.Node)) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := deref(n.Content[i]); isMergeKey(k) {
			for _, src := range mergeSources(n.Content[i+1]) {
				for j := 0; j+1 < len(src.Content); j += 2 {
					fn(deref(src.Content[j]), src.Content[j+1])
				}
			}
		}
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := deref(n.Content[i])
		if isMergeKey(k) {
			continue
		}
		if seen[k.Value] {
			w.errf(appendSeg(segs, k.Value), "", k, nil, "duplicate key")
			continue
		}
		seen[k.Value] = true
		fn(k, n.Content[i+1])
	}
}

func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "!!merge" || k.Style == 0)
}

func mergeSources(v *yaml.Node) []*yaml.Node {
	v = deref(v)
	switch v.Kind {
	case yaml.MappingNode:
		return []*yaml.Node{v}
	case yaml.SequenceNode:
		var out []*yaml.Node
		for _, e := range v.Content {
			if e = deref(e); e.Kind == yaml.MappingNode {
				out = append(out, e)
			}
		}
		return out
	}
	return nil
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null")
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "list"
	default:
		return "single value"
	}
}

func lookup(m *yaml.Node, key string) (k, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if deref(m.Content[i]).Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func strNode(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

func lineOf(key, val *yaml.Node) int {
	if key != nil && key.Line > 0 {
		return key.Line
	}
	if val != nil {
		return val.Line
	}
	return 0
}

func constSource(s Source) func(_, _ *yaml.Node) Source {
	return func(_, _ *yaml.Node) Source { return s }
}

// fileSource reads UI provenance from the "# set by <who> at <time>" line
// comments that the UI writer puts on every value it owns.
func fileSource(k Layer, path string) func(key, val *yaml.Node) Source {
	return func(key, val *yaml.Node) Source {
		s := Source{Layer: k, File: path, Line: lineOf(key, val)}
		if k == LayerUI {
			for _, n := range []*yaml.Node{val, key} {
				if n != nil && n.LineComment != "" {
					s.By, s.At = parseMeta(n.LineComment)
					break
				}
			}
		}
		return s
	}
}

func parseMeta(c string) (by string, at time.Time) {
	f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(c), "#"))
	if len(f) >= 3 && f[0] == "set" && f[1] == "by" {
		by = f[2]
		if len(f) >= 5 && f[3] == "at" {
			at, _ = time.Parse(time.RFC3339, f[4])
		}
	}
	return by, at
}

func configLayer(c *Config) *layer {
	l := newLayer(LayerDefault)
	var n yaml.Node
	if err := n.Encode(c); err != nil {
		return l
	}
	w := walker{l: l, src: constSource(Source{Layer: LayerDefault})}
	w.walk(&n, rootField(), nil, nil)
	return l
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

// parseFile builds a file layer. A non-nil issue means the document could not
// be read or parsed at all (safe mode for bothy.yaml).
func parseFile(path string, k Layer, data []byte) (*layer, *issue) {
	l := newLayer(k)
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		src := Source{Layer: k, File: path}
		if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
			src.Line, _ = strconv.Atoi(m[1])
			// yaml.v3 reports parser errors ("did not find expected …") with
			// 0-based lines and scanner errors with 1-based ones.
			if strings.Contains(err.Error(), "did not find expected") {
				src.Line++
			}
		}
		is := newIssue("", "", src, "cannot parse: "+strings.TrimPrefix(err.Error(), "yaml: "))
		return l, &is
	}
	if len(doc.Content) == 0 {
		return l, nil
	}
	root := deref(doc.Content[0])
	if isNull(root) {
		return l, nil
	}
	if root.Kind != yaml.MappingNode {
		is := newIssue("", "", Source{Layer: k, File: path, Line: root.Line}, "cannot parse: the top level must be a mapping")
		return l, &is
	}
	w := walker{l: l, src: fileSource(k, path)}
	w.walk(root, rootField(), nil, nil)
	return l, nil
}

// readLayerFile reads and parses path; a missing file is an empty layer unless
// required.
func readLayerFile(path string, k Layer, required bool) (*layer, *issue) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !required {
			return newLayer(k), nil
		}
		is := newIssue("", "", Source{Layer: k, File: path}, "cannot read: "+err.Error())
		return newLayer(k), &is
	}
	return parseFile(path, k, data)
}

func envMap(kv []string) map[string]string {
	m := make(map[string]string, len(kv))
	for _, e := range kv {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

// reservedEnv names BOTHY_ variables that are not config paths.
func reservedEnv(name string) bool {
	switch name {
	case "BOTHY_CONFIG", "BOTHY_CONFIG_LOCK", "BOTHY_DEPLOYMENT", "BOTHY_KEK":
		return true
	}
	return strings.HasPrefix(name, "BOTHY_SECRET_") || strings.HasPrefix(name, "BOTHY_SBX_")
}

// envLayer maps BOTHY_A__B variables onto config paths. Item ids and map keys
// are matched against the lower layers so BOTHY_MCP__SERVERS__MY_DOCS__URL
// finds "my-docs"; unknown ones are created lower-cased.
func envLayer(env map[string]string, lower ...*layer) *layer {
	l := newLayer(LayerEnv)
	var names []string
	for name := range env {
		if len(name) > len("BOTHY_") && strings.HasPrefix(name, "BOTHY_") && !reservedEnv(name) {
			names = append(names, name)
		}
	}
	// Broad variables first so BOTHY_PROVIDERS__X__BASE_URL refines BOTHY_PROVIDERS.
	slices.SortFunc(names, func(a, b string) int {
		if d := strings.Count(a, "__") - strings.Count(b, "__"); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for _, name := range names {
		src := Source{Layer: LayerEnv, EnvVar: name}
		t, path, msg := resolveEnv(strings.Split(strings.TrimPrefix(name, "BOTHY_"), "__"), lower)
		if t == nil {
			if msg != "" {
				l.errs = append(l.errs, newIssue(path, "", src, msg))
			}
			continue
		}
		n, err := envNode(t.f, env[name])
		if err != nil {
			l.errs = append(l.errs, newIssue(pathKey(t.segs), "", src, err.Error()))
			continue
		}
		w := walker{l: l, src: constSource(src)}
		w.target(*t, n)
	}
	return l
}

// resolveEnv returns nil and an empty message for variables that are not
// config at all (a single unknown segment with no near match).
func resolveEnv(segs []string, lower []*layer) (t *target, path, msg string) {
	f := rootField()
	var out []string
	var parent *field
	for i, s := range segs {
		if s = strings.ToUpper(s); s == "" {
			return nil, pathKey(out), "empty segment in the variable name"
		}
		switch f.kind {
		case kStruct:
			c := f.byEnv[s]
			if c == nil {
				name := strings.ToLower(s)
				hint := didYouMean(name, f.names)
				if i == 0 && len(segs) == 1 && hint == "" {
					return nil, "", ""
				}
				return nil, pathKey(appendSeg(out, name)), "unknown key" + hint
			}
			out, parent, f = appendSeg(out, c.name), f, c
		case kKeyedList, kMap:
			out, parent, f = appendSeg(out, matchChild(s, out, lower)), f, f.elem
		default:
			return nil, pathKey(out), "has no sub-keys"
		}
	}
	return &target{segs: out, f: f, parent: parent, item: parent != nil && parent.kind == kKeyedList}, "", ""
}

func matchChild(s string, segs []string, lower []*layer) string {
	for _, l := range lower {
		for _, k := range l.childKeys(segs) {
			if envNorm(k) == s {
				return k
			}
		}
	}
	return strings.ToLower(s)
}

// envNorm is the env spelling of a key: upper case, anything else as "_".
func envNorm(k string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, k)
}

func envNode(f *field, v string) (*yaml.Node, error) {
	if f.kind == kScalar {
		switch f.scalarKind() {
		case reflect.String:
			return strNode(v), nil
		case reflect.Bool:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("expected true or false, got %q", v)
			}
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}, nil
		}
		if v == "" {
			return nil, errors.New("empty value")
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Value: v}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(v), &doc); err != nil || len(doc.Content) == 0 {
		return nil, errors.New("expected a JSON value")
	}
	return doc.Content[0], nil
}
