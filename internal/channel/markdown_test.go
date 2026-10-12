package channel

import (
	"slices"
	"strings"
	"testing"
)

func TestSplitAllPrefersParagraphs(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 25), strings.Repeat("c", 30)
	got := splitAll(a+"\n\n"+b+"\n"+c+" tail words", 60)
	if want := []string{a, b + "\n" + c, "tail words"}; !slices.Equal(got, want) {
		t.Fatalf("splitAll = %q, want %q", got, want)
	}
}

func TestSplitAllKeepsFencesBalanced(t *testing.T) {
	var b strings.Builder
	b.WriteString("intro line\n```go\n")
	for range 30 {
		b.WriteString("fmt.Println(\"hello\")\n")
	}
	b.WriteString("```\noutro")
	pieces := splitAll(b.String(), 200)
	if len(pieces) < 3 {
		t.Fatalf("pieces = %d", len(pieces))
	}
	for i, p := range pieces {
		if ulen(p) > 200 {
			t.Fatalf("piece %d over limit (%d)", i, ulen(p))
		}
		if open, _, _ := fenceState("", p); open {
			t.Fatalf("piece %d leaves its fence open: %q", i, p)
		}
		if i > 0 && i < len(pieces)-1 && !strings.HasPrefix(p, "```go\n") {
			t.Fatalf("piece %d does not reopen the go fence: %q", i, p)
		}
	}
	if got, want := contentLines(strings.Join(pieces, "\n")), contentLines(b.String()); !slices.Equal(got, want) {
		t.Fatalf("content changed:\n%q\n%q", got, want)
	}
}

func TestSplitAllHardCutsAndCountsUTF16(t *testing.T) {
	if got := splitAll(strings.Repeat("x", 25), 10); len(got) != 3 || got[0] != strings.Repeat("x", 10) {
		t.Fatalf("hard cut = %q", got)
	}
	emoji := strings.Repeat("🙂", 6) // 12 UTF-16 units
	for _, p := range splitAll(emoji, 5) {
		if ulen(p) > 5 {
			t.Fatalf("piece %q has %d units", p, ulen(p))
		}
	}
	if got := splitAll("fits", 4096); len(got) != 1 || got[0] != "fits" {
		t.Fatalf("short text = %q", got)
	}
}

func TestNeutralize(t *testing.T) {
	for in, want := range map[string]string{
		"go to https://evil.com/a.b?x=1":     "go to hxxps://evil[.]com/a.b?x=1",
		"[docs](http://docs.example.org/p)":  "docs (hxxp://docs[.]example[.]org/p)",
		"<https://auto.link>":                "hxxps://auto[.]link",
		"mail me at bob@evil.co.uk":          "mail me at bob@evil[.]co[.]uk",
		"keep `curl https://x.io` in code":   "keep `curl https://x.io` in code",
		"```\nwget http://x.io/y\n```\nx.io": "```\nwget http://x.io/y\n```\nx[.]io",
		"pi is 3.14 and e.g. this is fine":   "pi is 3.14 and e.g. this is fine",
		"ftp://files.example/readme":         "fxp://files[.]example/readme",
		"[x](mailto:a@b.example)":            "x (mailto[:]a@b[.]example)",
	} {
		if got := neutralize(in); got != want {
			t.Errorf("neutralize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeMDAndCode(t *testing.T) {
	if got := escapeMD("# **x** [a](b) 1. _y_ `z`\n- item"); got != `\# \*\*x\*\* \[a\](b) 1. \_y\_ \`+"`z\\`"+"\n\\- item" {
		t.Fatalf("escapeMD = %q", got)
	}
	if got := escapeMD("1. first"); got != `1\. first` {
		t.Fatalf("escapeMD ordered marker = %q", got)
	}
	if got := codeSpan("a``b"); got != "```a``b```" {
		t.Fatalf("codeSpan = %q", got)
	}
	if got := codeSpan("`x`"); got != "`` `x` ``" {
		t.Fatalf("codeSpan with edge backticks = %q", got)
	}
	if got := codeBlock("", "```\nx\n```"); got != "````\n```\nx\n```\n````" {
		t.Fatalf("codeBlock = %q", got)
	}
	if got := revealInvisible("a‮b​c"); got != "a⟨U+202E⟩b⟨U+200B⟩c" {
		t.Fatalf("revealInvisible = %q", got)
	}
}
