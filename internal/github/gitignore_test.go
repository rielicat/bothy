package github

import "testing"

// Cases follow the gitignore(5) examples and pattern rules.
func TestIgnorer(t *testing.T) {
	ig := &ignorer{}
	ig.add("", []byte(`# comment
*.log
!keep.log
build/
/root-only.txt
doc/frotz/
**/generated
logs/**
a/**/z
\#hash
`+"trailing.txt   \n"+`escaped\ `+"\n"))
	ig.add("sub", []byte("local.txt\n/anchored\n!re.log\n"))
	for _, tc := range []struct {
		path  string
		dir   bool
		want  bool
		about string
	}{
		{"app.log", false, true, "glob at root"},
		{"deep/dir/app.log", false, true, "glob without slash matches at any level"},
		{"keep.log", false, false, "negation"},
		{"build", true, true, "directory pattern"},
		{"build", false, false, "directory-only pattern skips files"},
		{"src/build", true, true, "directory pattern at any level"},
		{"root-only.txt", false, true, "anchored"},
		{"sub/root-only.txt", false, false, "anchored does not match deeper"},
		{"doc/frotz", true, true, "pattern with inner slash is anchored"},
		{"a/doc/frotz", true, false, "anchored inner-slash pattern"},
		{"generated", true, true, "leading ** matches at root"},
		{"x/y/generated", false, true, "leading ** matches deep"},
		{"logs/2026/x.txt", false, true, "trailing ** matches inside"},
		{"logs", true, false, "trailing ** does not match the folder itself"},
		{"a/z", false, true, "inner ** matches zero folders"},
		{"a/b/c/z", false, true, "inner ** matches several folders"},
		{"#hash", false, true, "escaped #"},
		{"trailing.txt", false, true, "trailing spaces trimmed"},
		{"escaped ", false, true, "escaped trailing space kept"},
		{"sub/local.txt", false, true, "nested .gitignore is relative to its folder"},
		{"local.txt", false, false, "nested rules do not apply above"},
		{"sub/anchored", false, true, "nested anchored"},
		{"sub/x/anchored", false, false, "nested anchored is not recursive"},
		{"sub/re.log", false, false, "deeper file re-includes"},
		{"README.md", false, false, "no rule"},
	} {
		if got := ig.ignored(tc.path, tc.dir); got != tc.want {
			t.Errorf("%s: ignored(%q, dir=%v) = %v, want %v", tc.about, tc.path, tc.dir, got, tc.want)
		}
	}
}
