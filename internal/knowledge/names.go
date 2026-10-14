package knowledge

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// A repo name segment doubles as a directory name under <data>/knowledge, so
// it starts with a letter or digit ("." and ".." cannot pass).
var (
	segmentRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	shaRE     = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	unsafeRE  = regexp.MustCompile(`[^a-z0-9._-]+`)
)

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", core.ErrInvalid, fmt.Sprintf(format, args...))
}

// normalizeRepo lowercases "Owner/Name(.git)" and checks its shape.
func normalizeRepo(s string) (string, error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".git")
	owner, name, ok := strings.Cut(s, "/")
	if !ok || !segmentRE.MatchString(owner) || !segmentRE.MatchString(name) {
		return "", invalidf("repo must look like owner/name (letters, digits, '.', '-' and '_'), got %q", s)
	}
	return owner + "/" + name, nil
}

// defaultRepoName names a plain workspace directory "local/<directory>".
func defaultRepoName(dir string) string {
	base := strings.ToLower(path.Base(dir))
	if dir == "." || dir == "" {
		base = "workspace"
	}
	base = strings.TrimLeft(unsafeRE.ReplaceAllString(base, "-"), "._-")
	if len(base) > 100 {
		base = base[:100]
	}
	if base == "" {
		base = "workspace"
	}
	return "local/" + base
}

// normalizeSHA accepts a hex commit id (any case) or nothing.
func normalizeSHA(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s != "" && !shaRE.MatchString(s) {
		return "", invalidf("sha must be a hex commit id")
	}
	return s, nil
}

// workspaceDir is <work>/ws/<workspace>, or "" when p has no valid workspace.
func workspaceDir(workDir string, p *core.Principal) string {
	if workDir == "" || p == nil || !core.ValidID(core.PrefixWorkspace, string(p.Workspace)) {
		return ""
	}
	return filepath.Join(workDir, "ws", string(p.Workspace))
}

// relDir turns dir into a slash-separated path inside the workspace ("." for
// the workspace itself). An absolute dir must lie inside ws; a relative one
// may not climb out of it.
func relDir(ws, dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return ".", nil
	}
	if filepath.IsAbs(dir) {
		rel, err := filepath.Rel(filepath.Clean(ws), filepath.Clean(dir))
		if err != nil || !filepath.IsLocal(rel) {
			return "", invalidf("directory %q is outside the workspace", dir)
		}
		dir = rel
	} else if !filepath.IsLocal(dir) {
		return "", invalidf("directory %q is outside the workspace", dir)
	}
	return path.Clean(filepath.ToSlash(dir)), nil
}

// notesDir is <data>/knowledge/<principal>/<owner>/<repo>; names were checked
// by normalizeRepo and principal ids by core.ValidID.
func notesDir(dataDir string, p core.PrincipalID, repo string) (string, error) {
	if !core.ValidID(core.PrefixUser, string(p)) && !core.ValidID(core.PrefixSpace, string(p)) {
		return "", invalidf("bad principal id")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !segmentRE.MatchString(owner) || !segmentRE.MatchString(name) {
		return "", invalidf("bad repo name %q", repo)
	}
	return filepath.Join(dataDir, "knowledge", string(p), owner, name), nil
}
