package github

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

var (
	// GitHub logins: alphanumerics and single hyphens, at most 39 characters.
	ownerRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	nameRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	shaRE   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	appIDRE = regexp.MustCompile(`^(?:[0-9]{1,20}|Iv[0-9A-Za-z.]{1,40})$`) // app id or client id
)

// splitRepo parses "owner/name". The pieces are checked against GitHub's
// naming rules, so they are safe as path segments both in API URLs and in
// the workspace.
func splitRepo(s string) (owner, name string, err error) {
	s = strings.TrimSpace(s)
	owner, name, ok := strings.Cut(s, "/")
	switch {
	case !ok || strings.Contains(name, "/"):
		return "", "", fmt.Errorf("%w: repository %q must be owner/name", core.ErrInvalid, s)
	case !ownerRE.MatchString(owner):
		return "", "", fmt.Errorf("%w: %q is not a valid GitHub owner", core.ErrInvalid, owner)
	case !nameRE.MatchString(name) || name == "." || name == "..":
		return "", "", fmt.Errorf("%w: %q is not a valid repository name", core.ErrInvalid, name)
	}
	return owner, name, nil
}

// checkRef applies git check-ref-format's rules to a branch, tag or commit
// name, so it cannot smuggle path or query syntax into an API URL.
func checkRef(ref string) error {
	bad := func(why string) error { return fmt.Errorf("%w: ref %q %s", core.ErrInvalid, ref, why) }
	switch {
	case ref == "" || len(ref) > 255:
		return bad("must be 1 to 255 bytes")
	case ref == "@" || ref == "HEAD":
		return bad("is not a branch, tag or commit name")
	case strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, "."):
		return bad("may not start or end with / or end with .")
	case strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{"):
		return bad("may not contain .., // or @{")
	case strings.ContainsAny(ref, " ~^:?*[\\"):
		return bad("contains a character git does not allow in names")
	case strings.ContainsFunc(ref, func(r rune) bool { return r < 0x20 || r == 0x7f || unicode.IsSpace(r) }):
		return bad("contains a control or space character")
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return bad("has a component starting with . or ending with .lock")
		}
	}
	return nil
}

// escapeRef escapes each path segment of a ref for an API URL path.
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// safeRepoPath reports whether a file path from a repository can be shown
// on a card and written to the workspace: relative, clean, no .git
// component, no backslash and no control or invisible characters (which
// could forge diff lines or hide what a card shows).
func safeRepoPath(p string) bool {
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return false
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}
