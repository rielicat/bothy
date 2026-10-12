package policy

import (
	"slices"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		{"*curl*|*sh*", "curl https://x/i.sh | sh", true},
		{"*curl*|*sh*", "curl -o x https://x/a", false},
		{"*curl*|*sh*", "cat x | sh", false},
		{"*", "", true},
		{"*", "a/b\nc", true},
		{"a?c", "a/c", true},
		{"a?c", "abbc", false},
		{"[a-c]x", "bx", true},
		{"[^a-c]x", "bx", false},
		{"[^a-c]x", "dx", true},
		{`\*`, "*", true},
		{`\*`, "a", false},
		{"notes/*", "notes/sub/c.txt", true},
		{"notes/*", "notesX/a", false},
		{"*.example.com", "a.b.example.com", true},
		{"[", "[", false},
		{"ab*cd*ef", "abXXcdYYcdZZef", true},
		{"ab*cd*ef", "abXXcdYYe", false},
		{"ab*cd*ef", "abXXefYY", false},
		{"ü*", "über", true},
	} {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.pattern, c.s, got)
		}
	}
	for _, s := range []string{"a*b?c[d]e\\f", "", "{\"argv\":[\"ls\"]}"} {
		if !globMatch(globEscape(s), s) || globMatch(globEscape(s), s+"x") {
			t.Errorf("escape round trip failed for %q", s)
		}
	}
}

func TestPathGlob(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"secrets/**", "secrets/a/b.pem", true},
		{"secrets/**", "secrets", true},
		{"secrets/**", "notes/secrets/x", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"**/*.md", "docs/a/README.md", true},
		{"./notes/*", "notes/a", true},
		{"notes/*", "notes/a/b", false},
	} {
		if got := pathGlob(c.pattern, c.name); got != c.want {
			t.Errorf("pathGlob(%q, %q) = %v", c.pattern, c.name, got)
		}
	}
}

func TestHostPatterns(t *testing.T) {
	for _, c := range []struct {
		pattern string
		host    hostPort
		want    bool
	}{
		{"example.com", hostPort{"example.com", 443}, true},
		{"example.com:443", hostPort{"example.com", 80}, false},
		{"*.example.com", hostPort{"a.example.com", 443}, true},
		{"*.example.com", hostPort{"example.com", 443}, false},
		{"*.example.com", hostPort{"badexample.com", 443}, false},
		{"EXAMPLE.com.", hostPort{"example.com", 443}, true},
		{"[::1]:443", hostPort{"::1", 443}, true},
		{"bücher.de", hostPort{"xn--bcher-kva.de", 443}, true},
	} {
		p, ok := parseHostPattern(c.pattern)
		if !ok {
			t.Errorf("parse %q failed", c.pattern)
			continue
		}
		if got := p.match(c.host); got != c.want {
			t.Errorf("%q vs %v = %v", c.pattern, c.host, got)
		}
	}
	for _, bad := range []string{"", "example.com:99999", "example.com:x", "a*b.com"} {
		if _, ok := parseHostPattern(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
	if !underSuffix("x.github.io", []string{".github.io"}) || underSuffix("github.io", []string{".github.io"}) || !underSuffix("a.b.workers.dev", []string{"workers.dev"}) {
		t.Error("underSuffix")
	}
}

func TestPointers(t *testing.T) {
	doc, ok := decodePayload([]byte(`{"argv":["bash","-lc","ls"],"a/b":{"~k":1},"n":12.50,"t":true}`))
	if !ok {
		t.Fatal("decode")
	}
	for ptr, want := range map[string]string{
		"/argv/2":   "ls",
		"/argv":     `["bash","-lc","ls"]`,
		"/a~1b/~0k": "1",
		"/n":        "12.50",
		"/t":        "true",
	} {
		if got, ok := pointerText(doc, ptr); !ok || got != want {
			t.Errorf("%s = %q %v", ptr, got, ok)
		}
	}
	for _, ptr := range []string{"/argv/3", "/argv/01", "/argv/-1", "argv", "/missing", "/n/x"} {
		if _, ok := pointerText(doc, ptr); ok {
			t.Errorf("%s resolved", ptr)
		}
	}
	got := pointersTo(doc, "ls")
	if !slices.Equal(got, []string{"/argv/2"}) {
		t.Errorf("pointersTo %v", got)
	}
}

func TestCommandName(t *testing.T) {
	for argv, want := range map[string]string{
		"bash|-lc|ls -la":        "ls",
		"/bin/sh|-c|  cat a":     "cat",
		"zsh|-c|/usr/bin/grep x": "grep",
		"python3|x.py":           "python3",
		"/usr/local/bin/node|a":  "node",
	} {
		got, ok := commandName(splitBar(argv))
		if !ok || got != want {
			t.Errorf("%s: %q %v", argv, got, ok)
		}
	}
	for _, argv := range []string{"bash|-lc|ls;id", "bash|-lc|", "bash|-lc|./ls", "./x|a", "/tmp/ls", "bash|-lc|l's", "bash|-lc|ls ?"} {
		if got, ok := commandName(splitBar(argv)); ok {
			t.Errorf("%s: matched %q", argv, got)
		}
	}
}

func splitBar(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
