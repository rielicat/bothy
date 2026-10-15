package vault

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

const canary = "CANARY-s3cr3t/value+with=odd&chars"

func encodings(v string) map[string]string {
	b := []byte(v)
	return map[string]string{
		"raw":            v,
		"base64":         base64.StdEncoding.EncodeToString(b),
		"base64 raw":     base64.RawStdEncoding.EncodeToString(b),
		"base64url":      base64.URLEncoding.EncodeToString(b),
		"base64url raw":  base64.RawURLEncoding.EncodeToString(b),
		"hex":            hex.EncodeToString(b),
		"hex upper":      strings.ToUpper(hex.EncodeToString(b)),
		"query escaped":  url.QueryEscape(v),
		"path escaped":   url.PathEscape(v),
		"percent all":    percentAll(b, "0123456789ABCDEF"),
		"basic auth b64": base64.StdEncoding.EncodeToString([]byte("user:" + v)),
		"offset 1 b64":   base64.StdEncoding.EncodeToString([]byte("x" + v + "tail")),
		"offset 2 b64":   base64.URLEncoding.EncodeToString([]byte("xy" + v + "t")),
	}
}

func TestRedactorEncodedFormsInsideJSON(t *testing.T) {
	r := NewRedactor()
	r.Add("canary", []byte(canary))
	for name, enc := range encodings(canary) {
		doc, err := json.Marshal(map[string]any{"header": "Bearer " + enc, "nested": []string{"a", enc + "!"}})
		if err != nil {
			t.Fatal(err)
		}
		out := r.RedactBytes(doc)
		if !json.Valid(out) {
			t.Errorf("%s: redaction broke the JSON: %s", name, out)
		}
		core := enc
		if strings.Contains(name, "b64") {
			// Only the characters fixed by the secret are masked; the
			// prefix/suffix characters depend on the neighbours.
			core = base64Core(base64.RawStdEncoding, []byte(canary), 0)[4:20]
			if strings.Contains(name, "offset 2") {
				core = base64Core(base64.RawURLEncoding, []byte(canary), 2)[4:20]
			}
			if strings.Contains(name, "offset 1") || strings.Contains(name, "basic") {
				k := 1
				if strings.Contains(name, "basic") {
					k = len("user:") % 3
				}
				core = base64Core(base64.RawStdEncoding, []byte(canary), k)[4:20]
			}
		}
		if strings.Contains(string(out), core) {
			t.Errorf("%s: %q still visible in %s", name, core, out)
		}
		if !strings.Contains(string(out), Mask) {
			t.Errorf("%s: nothing masked in %s", name, out)
		}
	}
}

func TestRedactorJSONEscapedValue(t *testing.T) {
	r := NewRedactor()
	v := `pa"ss\word<tag>&more`
	r.Add("q", []byte(v))
	doc, _ := json.Marshal(map[string]string{"p": v})
	if out := string(r.RedactBytes(doc)); strings.Contains(out, `pa\"ss`) || !json.Valid([]byte(out)) {
		t.Errorf("JSON-escaped value survived: %s", out)
	}
}

func TestRedactorPatterns(t *testing.T) {
	r := NewRedactor()
	hits := []string{
		"sk-proj-abcdefghijklmnopqrstuvwxyz0123",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyzABCD",
		"xoxb-123456789012-abcdefghij",
		"xapp-1-A0123456789-abcdef",
		"ghp_" + strings.Repeat("a1", 18),
		"gho_" + strings.Repeat("Z9", 18),
		"github_pat_" + strings.Repeat("x", 30),
		"AIza" + strings.Repeat("B", 35),
		"bothy_pat_tok_01h2xcejqtf2nbrexx3vqjhp41_abcdef",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	}
	for _, h := range hits {
		in := `{"auth":"Bearer ` + h + `"}`
		if out := r.Redact(in); strings.Contains(out, h) || !strings.Contains(out, Mask) {
			t.Errorf("pattern not masked: %s -> %s", in, out)
		}
	}
	misses := []string{
		"task-management-system-overview-document",
		"desk-lamp",
		"high_light_" + strings.Repeat("a", 40),
		"eyJ alone",
		"a short sk-key",
	}
	for _, m := range misses {
		if out := r.Redact(m); out != m {
			t.Errorf("false positive: %q -> %q", m, out)
		}
	}
}

func TestRedactorShortValuesAndRemove(t *testing.T) {
	r := NewRedactor()
	r.Add("tiny", []byte("abc"))
	if out := r.Redact("abcabc"); out != "abcabc" {
		t.Errorf("a 3-byte value masked text: %q", out)
	}
	r.Add("short", []byte("pin42"))
	if out := r.Redact("pin42 and cGluNDI="); out != Mask+" and cGluNDI=" {
		t.Errorf("short values mask only their raw form: %q", out)
	}
	r.Add("long", []byte("long-secret-value"))
	r.Add("longer", []byte("long-secret-value-extended"))
	if out := r.Redact("x long-secret-value-extended y"); out != "x "+Mask+" y" {
		t.Errorf("a value containing another is masked whole: %q", out)
	}
	r.Remove("longer")
	r.Remove("never-added")
	if out := r.Redact("long-secret-value-extended"); out != Mask+"-extended" {
		t.Errorf("after Remove = %q", out)
	}
	r.Remove("long")
	r.Remove("short")
	if out := r.Redact("long-secret-value pin42"); out != "long-secret-value pin42" {
		t.Errorf("after removing everything = %q", out)
	}
	var zero Redactor
	zero.Add("z", []byte("zero-value-ok"))
	if out := zero.Redact("zero-value-ok"); out != Mask {
		t.Errorf("zero Redactor = %q", out)
	}
}

// PartialAt finds the earliest tail that a registered value (or one of its
// encodings) could still grow out of, which a stream must hold back.
func TestRedactorPartialAt(t *testing.T) {
	r := NewRedactor()
	if got := r.PartialAt("correct hor"); got != len("correct hor") {
		t.Fatalf("nothing registered: PartialAt = %d", got)
	}
	r.Add("pw", []byte("correct horse battery"))
	r.Add("key", []byte(canary))
	var all []string
	for _, v := range r.values {
		all = append(all, needles(v)...)
	}
	// The reference: the smallest i whose suffix some longer needle starts with.
	want := func(s string) int {
		for i := range len(s) {
			for _, n := range all {
				if len(n) > len(s)-i && strings.HasPrefix(n, s[i:]) {
					return i
				}
			}
		}
		return len(s)
	}
	inputs := []string{
		"my password is correct hor",
		"my password is correct horse battery",
		"my password is correct horse battery and more",
		"the key: CANARY-s3cr",
		"encoded: " + base64.StdEncoding.EncodeToString([]byte(canary))[:12],
		"hex " + hex.EncodeToString([]byte("correct horse battery"))[:9],
		"nothing to see here.",
		"",
	}
	for _, s := range inputs {
		if got := r.PartialAt(s); got != want(s) {
			t.Errorf("PartialAt(%q) = %d, want %d", s, got, want(s))
		}
	}
	if got := r.PartialAt("my password is correct hor"); got > len("my password is ") {
		t.Errorf("the half-sent password is not held back: PartialAt = %d", got)
	}
	if s := "my password is correct horse battery and more"; r.PartialAt(s) != len(s) {
		t.Errorf("a complete value followed by text holds back %q", s[r.PartialAt(s):])
	}
}

func TestRedactorInputUnchanged(t *testing.T) {
	r := NewRedactor()
	r.Add("x", []byte("mutate-me-not"))
	in := []byte("mutate-me-not")
	_ = r.RedactBytes(in)
	if string(in) != "mutate-me-not" {
		t.Errorf("RedactBytes changed its input to %q", in)
	}
}

func TestRedactorConcurrent(t *testing.T) {
	r := NewRedactor()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				id := fmt.Sprintf("s%d-%d", i, j)
				r.Add(id, []byte("value-"+id))
				if out := r.Redact("value-" + id); out != Mask {
					t.Errorf("%s not masked: %q", id, out)
					return
				}
				r.Remove(id)
			}
		})
	}
	wg.Wait()
}

func TestRedactorForScope(t *testing.T) {
	r := NewRedactor()
	r.Add("global/openai", []byte("shared-key-0123456789"))
	r.Add("env:ANTHROPIC_API_KEY", []byte("env-key-0123456789"))
	r.Add("user:usr_a/mine", []byte("deploy"))
	r.Add("user:usr_b/theirs", []byte("staging"))
	text := "deploy to staging with shared-key-0123456789 and env-key-0123456789 or sk-abcdefghijklmnopqrstuvwxyz"
	want := map[string]string{
		"user:usr_a": "[redacted] to staging with [redacted] and [redacted] or [redacted]",
		"user:usr_b": "deploy to [redacted] with [redacted] and [redacted] or [redacted]",
		"":           "deploy to staging with [redacted] and [redacted] or [redacted]",
	}
	for own, w := range want {
		red := r.ForScope(core.Scope(own))
		if got := red.Redact(text); got != w {
			t.Errorf("ForScope(%q).Redact = %q, want %q", own, got, w)
		}
		if got := string(red.RedactBytes([]byte(text))); got != w {
			t.Errorf("ForScope(%q).RedactBytes = %q, want %q", own, got, w)
		}
	}
	// A personal key is still masked everywhere else: display and model input.
	if got := r.Redact(text); strings.Contains(got, "deploy") || strings.Contains(got, "staging") {
		t.Errorf("Redact = %q", got)
	}
	// Changes reach the scoped snapshots.
	red := r.ForScope("user:usr_b")
	r.Remove("user:usr_b/theirs")
	r.Add("global/web", []byte("deploy"))
	if got := red.Redact("deploy to staging"); got != "[redacted] to staging" {
		t.Errorf("after changes = %q", got)
	}
}
