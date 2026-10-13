package vault

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Mask replaces every redacted value.
const Mask = "[redacted]"

const (
	// minRedactLen keeps one- to three-byte "secrets" from masking ordinary
	// text everywhere they occur.
	minRedactLen = 4
	// minEncodedLen is the SPEC threshold for also masking encoded forms.
	minEncodedLen = 8
)

// tokenPattern matches well-known credential shapes even when the value was
// never registered: OpenAI/Anthropic sk-…, Slack xox?-…/xapp-…, GitHub
// tokens, Google API keys, JWTs and Bothy personal access tokens.
var tokenPattern = regexp.MustCompile(`\b(?:` +
	`sk-[A-Za-z0-9_\-]{20,}` +
	`|xox[abpr]-[A-Za-z0-9\-]{10,}` +
	`|xapp-[A-Za-z0-9\-]{10,}` +
	`|gh[opusr]_[A-Za-z0-9]{30,}` +
	`|github_pat_[A-Za-z0-9_]{20,}` +
	`|AIza[0-9A-Za-z_\-]{35}` +
	`|bothy_pat_[A-Za-z0-9_]{10,}` +
	`|eyJ[A-Za-z0-9_\-]{5,}\.eyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*` +
	`)`)

// tokenMarkers gate the regexp: text without any of them cannot match.
var tokenMarkers = []string{"sk-", "xoxa-", "xoxb-", "xoxp-", "xoxr-", "xapp-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"github_pat_", "AIza", "bothy_pat_", "eyJ"}

// Redactor implements core.Redactor. Created first: the logger, audit and vault use it.
type Redactor struct {
	mu     sync.Mutex
	values map[string][]byte
	snap   atomic.Pointer[snapshot]
}

// snapshot is the compiled form of the registered values.
type snapshot struct {
	rep     *strings.Replacer
	sorted  []string // every needle in byte order, for prefix lookups
	longest int
}

func NewRedactor() *Redactor { return &Redactor{values: map[string][]byte{}} }

// Add registers a value under id; re-adding an id replaces its value. Values
// shorter than minRedactLen are ignored.
func (r *Redactor) Add(id string, value []byte) {
	if len(value) < minRedactLen {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string][]byte{}
	}
	r.values[id] = bytes.Clone(value)
	r.rebuild()
}

func (r *Redactor) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[id]; !ok {
		return
	}
	delete(r.values, id)
	r.rebuild()
}

// rebuild compiles every registered value and its encodings into one
// strings.Replacer. Needles go longest first: the replacer tries them in
// argument order at each position, so a value containing another is masked
// whole.
func (r *Redactor) rebuild() {
	set := map[string]struct{}{}
	for _, v := range r.values {
		for _, n := range needles(v) {
			set[n] = struct{}{}
		}
	}
	if len(set) == 0 {
		r.snap.Store(nil)
		return
	}
	sorted := slices.Sorted(maps.Keys(set))
	list := slices.Clone(sorted)
	slices.SortStableFunc(list, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	args := make([]string, 0, 2*len(list))
	for _, n := range list {
		args = append(args, n, Mask)
	}
	r.snap.Store(&snapshot{rep: strings.NewReplacer(args...), sorted: sorted, longest: len(list[0])})
}

func (r *Redactor) Redact(s string) string {
	if sn := r.snap.Load(); sn != nil {
		s = sn.rep.Replace(s)
	}
	return redactPatterns(s)
}

// PartialAt returns where the earliest suffix of s that is a proper prefix
// of a registered value, or of one of its encodings, starts; len(s) when no
// suffix is. Text streamed in pieces must hold that tail back: the rest of
// the value may still be coming, and redacting the part already shown cannot
// mask it. Token patterns have no whitespace, so holding back the partial
// last word covers them.
func (r *Redactor) PartialAt(s string) int {
	sn := r.snap.Load()
	if sn == nil {
		return len(s)
	}
	for i := max(0, len(s)-sn.longest+1); i < len(s); i++ {
		if sn.extends(s[i:]) {
			return i
		}
	}
	return len(s)
}

// extends reports whether some needle is longer than p and starts with it.
func (sn *snapshot) extends(p string) bool {
	j, found := slices.BinarySearch(sn.sorted, p)
	if found {
		j++
	}
	return j < len(sn.sorted) && strings.HasPrefix(sn.sorted[j], p)
}

func (r *Redactor) RedactBytes(b []byte) []byte { return []byte(r.Redact(string(b))) }

func redactPatterns(s string) string {
	for _, m := range tokenMarkers {
		if strings.Contains(s, m) {
			return tokenPattern.ReplaceAllString(s, Mask)
		}
	}
	return s
}

// needles lists the forms of v to mask: the raw value, plus for values of at
// least minEncodedLen bytes hex, percent encodings and base64/base64url, and
// each of those as it appears inside a JSON string. Base64 adds the run of
// characters fixed by v at each of the three byte alignments, which catches v
// inside a longer encoded blob such as an HTTP Basic credential.
func needles(v []byte) []string {
	forms := []string{string(v)}
	if len(v) >= minEncodedLen {
		h := hex.EncodeToString(v)
		forms = append(forms, h, strings.ToUpper(h), url.QueryEscape(string(v)), url.PathEscape(string(v)),
			percentAll(v, "0123456789ABCDEF"), percentAll(v, "0123456789abcdef"))
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			forms = append(forms, enc.EncodeToString(v))
			raw := enc.WithPadding(base64.NoPadding)
			for k := range 3 {
				forms = append(forms, base64Core(raw, v, k))
			}
		}
	}
	out := slices.Clone(forms)
	for _, f := range forms {
		out = append(out, jsonForms(f)...)
	}
	return slices.DeleteFunc(out, func(n string) bool { return len(n) < minRedactLen })
}

// jsonForms is s as it appears between the quotes of a JSON string, with and
// without HTML escaping.
func jsonForms(s string) []string {
	var out []string
	if b, err := json.Marshal(s); err == nil {
		out = append(out, string(b[1:len(b)-1]))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err == nil {
		b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		out = append(out, string(b[1:len(b)-1]))
	}
	return out
}

func percentAll(v []byte, digits string) string {
	var b strings.Builder
	b.Grow(3 * len(v))
	for _, c := range v {
		b.WriteByte('%')
		b.WriteByte(digits[c>>4])
		b.WriteByte(digits[c&15])
	}
	return b.String()
}

// base64Core returns the characters of the encoding of v that do not depend on
// the bytes around it when v starts k bytes into a 3-byte group.
func base64Core(enc *base64.Encoding, v []byte, k int) string {
	buf := make([]byte, k+len(v))
	copy(buf[k:], v)
	full := len(buf) / 3 * 3
	s := enc.EncodeToString(buf[:full])
	if rem := len(buf) - full; rem > 0 {
		s += enc.EncodeToString(buf[full:])[:rem]
	}
	return s[[3]int{0, 2, 3}[k]:]
}
