package config

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

type kind uint8

const (
	kScalar     kind = iota // string, number, bool, Duration, *bool
	kScalarList             // replaced wholesale by higher layers
	kStruct
	kKeyedList // list of structs merged by their id/name field
	kMap       // merged key by key
)

// field describes one node of the Config schema, built once by reflection.
// Paths address keyed-list items and map entries by key, so a single field
// describes every item of its list.
type field struct {
	name   string
	typ    reflect.Type
	kind   kind
	hot    bool // reload:"hot" here or on an ancestor
	locked bool // locked:"true" here or on an ancestor
	server bool // server:"true" here or on an ancestor
	secret bool
	embed  bool // secret map value: a ref may sit inside a longer string
	isKey  bool // the id/name field of a keyed-list item
	fields []*field
	byName map[string]*field
	byEnv  map[string]*field
	names  []string
	elem   *field // kKeyedList: the item struct; kMap: the value
	key    string // kKeyedList: "id" or "name"
}

var durationType = reflect.TypeFor[Duration]()

var rootField = sync.OnceValue(func() *field {
	return buildField("", reflect.TypeFor[Config](), "", nil)
})

func buildField(name string, t reflect.Type, tag reflect.StructTag, parent *field) *field {
	f := &field{name: name, typ: t}
	if parent != nil {
		f.hot, f.locked, f.server = parent.hot, parent.locked, parent.server
	}
	f.hot = f.hot || tag.Get("reload") == "hot"
	f.locked = f.locked || tag.Get("locked") == "true"
	f.server = f.server || tag.Get("server") == "true"
	f.secret = tag.Get("secret") == "true"
	base := t
	if base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	switch {
	case base.Kind() == reflect.Struct && base != durationType:
		f.kind = kStruct
		f.byName = map[string]*field{}
		f.byEnv = map[string]*field{}
		for i := range base.NumField() {
			sf := base.Field(i)
			key, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
			if key == "" || key == "-" || !sf.IsExported() {
				continue
			}
			c := buildField(key, sf.Type, sf.Tag, f)
			f.fields = append(f.fields, c)
			f.byName[key] = c
			f.byEnv[strings.ToUpper(key)] = c
			f.names = append(f.names, key)
		}
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct:
		item := buildField("", t.Elem(), "", f)
		for _, k := range []string{"id", "name"} {
			if kf := item.byName[k]; kf != nil {
				kf.isKey = true
				f.kind, f.elem, f.key = kKeyedList, item, k
				break
			}
		}
		if f.elem == nil {
			f.kind = kScalarList
		}
	case t.Kind() == reflect.Slice:
		f.kind = kScalarList
	case t.Kind() == reflect.Map:
		f.kind = kMap
		f.elem = buildField("", t.Elem(), "", f)
		if f.secret && f.elem.kind == kScalar {
			f.elem.secret, f.elem.embed = true, true
		}
	default:
		f.kind = kScalar
	}
	return f
}

func (f *field) isPtrStruct() bool { return f.kind == kStruct && f.typ.Kind() == reflect.Pointer }

func (f *field) scalarKind() reflect.Kind {
	t := f.typ
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind()
}

func pathKey(segs []string) string { return strings.Join(segs, ".") }

func appendSeg(segs []string, s string) []string { return append(slices.Clip(segs), s) }

func hasPrefix(segs, prefix []string) bool {
	return len(segs) >= len(prefix) && slices.Equal(segs[:len(prefix)], prefix)
}

// target is a resolved config path.
type target struct {
	segs   []string
	f      *field
	parent *field // the field holding the last segment
	item   bool   // the last segment is a keyed-list id
}

// resolvePath parses a dotted path. Item ids and map keys may contain dots
// ("pricing.openai/gpt-5.1.in"), so the split is decided by the schema: the
// shortest key whose remainder is a valid sub-path wins.
func resolvePath(p string) (target, error) {
	if p == "" {
		return target{}, errors.New("empty path")
	}
	if t, ok := resolveFrom(rootField(), nil, p, nil); ok {
		return t, nil
	}
	return target{}, errors.New("unknown key" + explainUnknown(p))
}

func resolveFrom(f, parent *field, rest string, segs []string) (target, bool) {
	if rest == "" {
		return target{segs: segs, f: f, parent: parent, item: parent != nil && parent.kind == kKeyedList}, true
	}
	switch f.kind {
	case kStruct:
		name, tail, found := strings.Cut(rest, ".")
		c := f.byName[name]
		if c == nil || (found && tail == "") {
			return target{}, false
		}
		return resolveFrom(c, f, tail, appendSeg(segs, name))
	case kKeyedList, kMap:
		for i := 1; i <= len(rest); i++ {
			if i < len(rest) && rest[i] != '.' {
				continue
			}
			tail := ""
			if i < len(rest) {
				if tail = rest[i+1:]; tail == "" {
					continue
				}
			}
			if t, ok := resolveFrom(f.elem, f, tail, appendSeg(segs, rest[:i])); ok {
				return t, true
			}
		}
	}
	return target{}, false
}

// explainUnknown walks p greedily to name the first unknown segment.
func explainUnknown(p string) string {
	f := rootField()
	segs := strings.Split(p, ".")
	for i := 0; i < len(segs); i++ {
		s := segs[i]
		switch f.kind {
		case kStruct:
			c := f.byName[s]
			if c == nil {
				return fmt.Sprintf(" %q%s", s, didYouMean(s, f.names))
			}
			f = c
		case kKeyedList, kMap:
			f = f.elem
		default:
			return fmt.Sprintf(" %q (%s has no sub-keys)", s, pathKey(segs[:i]))
		}
	}
	return fmt.Sprintf(" %q", p)
}

func parseLocks(v string) []string {
	var out []string
	for g := range strings.SplitSeq(v, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// globLocked reports whether a BOTHY_CONFIG_LOCK glob covers segs: the path
// itself, any ancestor, or (for "a.*") the container "a".
func globLocked(globs, segs []string) bool {
	if len(globs) == 0 || len(segs) == 0 {
		return false
	}
	full := pathKey(segs)
	for _, g := range globs {
		if base, ok := strings.CutSuffix(g, ".*"); ok && globMatch(base, full) {
			return true
		}
		for i := 1; i <= len(segs); i++ {
			if globMatch(g, pathKey(segs[:i])) {
				return true
			}
		}
	}
	return false
}

// globMatch matches '*' (any run, dots included) and '?' (one rune).
func globMatch(pattern, s string) bool {
	px, sx := 0, 0
	nextPx, nextSx := 0, 0
	for px < len(pattern) || sx < len(s) {
		if px < len(pattern) {
			switch c := pattern[px]; c {
			case '*':
				nextPx, nextSx = px, sx+1
				px++
				continue
			case '?':
				if sx < len(s) {
					_, w := utf8.DecodeRuneInString(s[sx:])
					px++
					sx += w
					continue
				}
			default:
				if sx < len(s) && s[sx] == c {
					px++
					sx++
					continue
				}
			}
		}
		if 0 < nextSx && nextSx <= len(s) {
			px, sx = nextPx, nextSx
			continue
		}
		return false
	}
	return true
}

func didYouMean(s string, opts []string) string {
	if best := suggest(s, opts); best != "" {
		return fmt.Sprintf(" (did you mean %q?)", best)
	}
	return ""
}

func suggest(s string, opts []string) string {
	best, bestD := "", -1
	ls := strings.ToLower(s)
	for _, o := range opts {
		if o == "" {
			continue
		}
		if d := levenshtein(ls, strings.ToLower(o)); bestD < 0 || d < bestD {
			best, bestD = o, d
		}
	}
	limit := max(1, min(3, utf8.RuneCountInString(s)/3))
	if bestD < 0 || bestD > limit {
		return ""
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
