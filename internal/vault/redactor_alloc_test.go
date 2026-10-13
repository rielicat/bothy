package vault

import (
	"fmt"
	"strings"
	"testing"
)

// The redactor runs on every SSE frame and on the whole history at every
// agent step, so text with nothing to mask must pass through without a
// copy, including text where "sk-" only appears inside words.
func TestRedactorPassesCleanTextWithoutCopying(t *testing.T) {
	r := NewRedactor()
	for i := range 20 {
		r.Add(fmt.Sprintf("s%d", i), fmt.Appendf(nil, "secret-value-number-%02d-xyz", i))
	}
	clean := strings.Repeat("A risk-free task-runner on a flask-app desk-lamp, with ordinary prose. ", 1000)
	b := []byte(clean)
	if got := r.Redact(clean); got != clean {
		t.Fatal("clean text changed")
	}
	if n := testing.AllocsPerRun(5, func() { _ = r.Redact(clean) }); n != 0 {
		t.Errorf("Redact allocated %v times on clean text", n)
	}
	if n := testing.AllocsPerRun(5, func() { _ = r.RedactBytes(b) }); n != 0 {
		t.Errorf("RedactBytes allocated %v times on clean text", n)
	}
	if got := r.RedactBytes(b); &got[0] != &b[0] {
		t.Error("RedactBytes copied clean input")
	}
	dirty := clean + " secret-value-number-07-xyz and sk-proj-abcdefghijklmnopqrstuvwxyz0123 end"
	out := r.Redact(dirty)
	if strings.Contains(out, "number-07") || strings.Contains(out, "sk-proj") || strings.Count(out, Mask) != 2 {
		t.Fatalf("dirty text not masked: %q", out[len(out)-80:])
	}
	if got := string(r.RedactBytes([]byte(dirty))); got != out {
		t.Fatal("RedactBytes and Redact disagree")
	}
	if got := r.Redact("x" + "sk-proj-abcdefghijklmnopqrstuvwxyz0123"); got != "xsk-proj-abcdefghijklmnopqrstuvwxyz0123" {
		t.Fatalf("token inside a word masked: %q", got)
	}
}

func BenchmarkRedactCleanHistory(b *testing.B) {
	r := NewRedactor()
	for i := range 20 {
		r.Add(fmt.Sprintf("s%d", i), fmt.Appendf(nil, "secret-value-number-%02d-xyz", i))
	}
	s := strings.Repeat("A risk-free task-runner on a flask-app desk-lamp, with ordinary prose. ", 6000)
	b.SetBytes(int64(len(s)))
	for b.Loop() {
		_ = r.Redact(s)
	}
}
