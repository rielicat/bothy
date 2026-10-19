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

	"github.com/rielicat/bothy/internal/core"
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

// tokenMarkers gate the regexp: text without one of them right after a word
// boundary cannot match.
var tokenMarkers = []string{"sk-", "xoxa-", "xoxb-", "xoxp-", "xoxr-", "xapp-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"github_pat_", "AIza", "bothy_pat_", "eyJ"}

// markersByByte indexes tokenMarkers by their first byte.
var markersByByte = func() (m [256][]string) {
	for _, s := range tokenMarkers {
		m[s[0]] = append(m[s[0]], s)
	}
	return m
}()

// authScheme matches an Authorization-style value, capturing its token.
var authScheme = regexp.MustCompile(`(?i)^(?:bearer|basic|token|bot|apikey|api-key|key)\s+(\S{8,})\s*$`)

// text is what the scans accept without copying a []byte into a string.
type text interface{ ~string | ~[]byte }

// Redactor implements core.Redactor. Created first: the logger, audit and vault use it.
type Redactor struct {
	mu     sync.Mutex
	values map[string][]byte
	snap   atomic.Pointer[snapshot]
	scoped map[string]*snapshot // ForScope snapshots by owner prefix, dropped on every change
}

// snapshot is the compiled form of the registered values.
type snapshot struct {
	rep     *strings.Replacer
	sorted  []string // every needle in byte order, for prefix lookups
	longest int
	// heads holds a bit per hashed 4-byte needle prefix and byHead the
	// needles behind each prefix, so text holding no needle is recognised
	// in one pass without the copy strings.Replacer always makes.
	heads  [1 << headBits / 64]uint64
	byHead map[uint32][]string
}

const headBits = 16

func head4[T text](s T, i int) uint32 {
	return uint32(s[i]) | uint32(s[i+1])<<8 | uint32(s[i+2])<<16 | uint32(s[i+3])<<24
}

func headHash(k uint32) uint32 { return k * 0x9E3779B1 >> (32 - headBits) }

// contains reports whether any needle occurs in s. Every needle is at
// least minRedactLen (4) bytes long.
func contains[T text](sn *snapshot, s T) bool {
	for i := 0; i+minRedactLen <= len(s); i++ {
		k := head4(s, i)
		if h := headHash(k); sn.heads[h/64]&(1<<(h%64)) == 0 {
			continue
		}
		for _, n := range sn.byHead[k] {
			if len(s)-i >= len(n) && string(s[i:i+len(n)]) == n {
				return true
			}
		}
	}
	return false
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

// Retire keeps id's value masked under a retired id, in place of the one
// retired before, and frees id. The vault retires a key it replaces or
// deletes: a session or provider that read it a moment ago may still send
// it, and what comes back must not show it.
func (r *Redactor) Retire(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[id]
	if !ok {
		return
	}
	r.values[id+"@retired"] = v
	delete(r.values, id)
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
	r.scoped = nil
	r.snap.Store(compile(r.values))
}

// compile builds the snapshot for values; nil when there is nothing to mask.
func compile(values map[string][]byte) *snapshot {
	set := map[string]struct{}{}
	for _, v := range values {
		for _, n := range needles(v) {
			set[n] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	sorted := slices.Sorted(maps.Keys(set))
	list := slices.Clone(sorted)
	slices.SortStableFunc(list, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	args := make([]string, 0, 2*len(list))
	sn := &snapshot{sorted: sorted, longest: len(list[0]), byHead: map[uint32][]string{}}
	for _, n := range list {
		args = append(args, n, Mask)
		k := head4(n, 0)
		h := headHash(k)
		sn.heads[h/64] |= 1 << (h % 64)
		sn.byHead[k] = append(sn.byHead[k], n)
	}
	sn.rep = strings.NewReplacer(args...)
	return sn
}

// ForScope masks only values no member chooses (shared, environment and the
// vault's own keys) plus own's personal keys, and the credential patterns.
// Text stored for good is masked with it: a member's personal key such as
// "deploy" must not erase that word from everyone else's chats.
func (r *Redactor) ForScope(own core.Scope) core.Redactor {
	return scopedRedactor{r: r, own: string(own) + "/"}
}

func (r *Redactor) scopedSnap(own string) *snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sn, ok := r.scoped[own]; ok {
		return sn
	}
	vals := map[string][]byte{}
	for id, v := range r.values {
		if !strings.HasPrefix(id, "user:") || strings.HasPrefix(id, own) {
			vals[id] = v
		}
	}
	sn := compile(vals)
	if r.scoped == nil {
		r.scoped = map[string]*snapshot{}
	}
	r.scoped[own] = sn
	return sn
}

type scopedRedactor struct {
	r   *Redactor
	own string // "<scope>/", the id prefix of the owner's keys
}

func (s scopedRedactor) Redact(str string) string {
	if sn := s.r.scopedSnap(s.own); sn != nil && contains(sn, str) {
		str = sn.rep.Replace(str)
	}
	return redactPatterns(str)
}

func (s scopedRedactor) RedactBytes(b []byte) []byte {
	if sn := s.r.scopedSnap(s.own); (sn == nil || !contains(sn, b)) && !hasMarker(b) {
		return b
	}
	return []byte(s.Redact(string(b)))
}

func (s scopedRedactor) Add(id string, value []byte) { s.r.Add(id, value) }
func (s scopedRedactor) Remove(id string)            { s.r.Remove(id) }

// Redact returns s itself when nothing in it is masked.
func (r *Redactor) Redact(s string) string {
	if sn := r.snap.Load(); sn != nil && contains(sn, s) {
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

// RedactBytes returns b itself when nothing in it is masked; it never
// modifies b.
func (r *Redactor) RedactBytes(b []byte) []byte {
	if sn := r.snap.Load(); (sn == nil || !contains(sn, b)) && !hasMarker(b) {
		return b
	}
	return []byte(r.Redact(string(b)))
}

func redactPatterns(s string) string {
	if !hasMarker(s) {
		return s
	}
	return tokenPattern.ReplaceAllString(s, Mask)
}

// hasMarker reports whether a token marker starts at a word boundary, the
// only place tokenPattern can match: "sk-" inside "risk-free" or "task-" is
// no reason to run the regexp over the whole text.
func hasMarker[T text](s T) bool {
	for i := 0; i < len(s); i++ {
		ms := markersByByte[s[i]]
		if ms == nil || i > 0 && isWordByte(s[i-1]) {
			continue
		}
		for _, m := range ms {
			if len(s)-i >= len(m) && string(s[i:i+len(m)]) == m {
				return true
			}
		}
	}
	return false
}

// isWordByte is RE2's ASCII \w, which decides \b.
func isWordByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// needles lists the forms of v to mask: the raw value, plus for values of at
// least minEncodedLen bytes hex, percent encodings and base64/base64url, and
// each of those as it appears inside a JSON string. Base64 adds the run of
// characters fixed by v at each of the three byte alignments, which catches v
// inside a longer encoded blob such as an HTTP Basic credential.
func needles(v []byte) []string {
	forms := []string{string(v)}
	// A header saved whole ("Bearer abc123", as the MCP dialog asks for)
	// leaves the token alone in an error that quotes only it.
	if m := authScheme.FindSubmatch(v); m != nil {
		forms = append(forms, needles(m[1])...)
	}
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
