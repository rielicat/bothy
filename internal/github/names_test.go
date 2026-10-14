package github

import (
	"errors"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestSplitRepo(t *testing.T) {
	for _, ok := range []string{"octocat/Hello-World", " octo-org/repo.js ", "a/b_c", "x1/.github"} {
		if _, _, err := splitRepo(ok); err != nil {
			t.Errorf("splitRepo(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "octocat", "octocat/", "/repo", "a/b/c", "-octo/x", "octo_cat/x", "octocat/..", "octocat/.",
		"octocat/a b", "octocat/a%2Fb", "octocat/a?b", strings.Repeat("a", 40) + "/x", "o/" + strings.Repeat("r", 101)} {
		if _, _, err := splitRepo(bad); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("splitRepo(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestCheckRef(t *testing.T) {
	for _, ok := range []string{"main", "feature/login", "v1.2.3", "7638417db6d59f3c431d3e1f261cc637155684cd", "release-2026#1", "a&b", "100%"} {
		if err := checkRef(ok); err != nil {
			t.Errorf("checkRef(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "HEAD", "@", "/main", "main/", "a..b", "a//b", "a@{1}", "a b", "a~1", "a^", "a:b", "a?", "a*", "a[b",
		`a\b`, "a\x00b", "a\nb", ".hidden", "x/.y", "x.lock", "a.", strings.Repeat("r", 256)} {
		if err := checkRef(bad); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("checkRef(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if got := escapeRef("feature/a#b c"); got != "feature/a%23b%20c" {
		t.Errorf("escapeRef = %q", got)
	}
}

func TestSafeRepoPath(t *testing.T) {
	for _, ok := range []string{"README.md", "src/main.go", ".github/workflows/ci.yml", "a/.gitignore", "dir/文件.txt"} {
		if !safeRepoPath(ok) {
			t.Errorf("safeRepoPath(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "a/./b", "a//b", ".git/config", "sub/.GIT/hooks/pre-commit",
		`a\b`, "a\nb", "evil‮txt.exe", "zero​width", "tab\tname", strings.Repeat("a", 256)} {
		if safeRepoPath(bad) {
			t.Errorf("safeRepoPath(%q) = true", bad)
		}
	}
}
