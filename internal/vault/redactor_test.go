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
