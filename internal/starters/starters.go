// Package starters holds what Bothy offers people who don't know where to
// begin: the rows under an empty chat, the schedules they can start from,
// lines for an empty check-in list and the welcome chat's message. The
// copy is served from here so it costs the web app nothing, and every
// choice depends only on State, which the HTTP API gathers per viewer.
//
// Nothing here quotes untrusted text: repository names come from GitHub's
// allowlist or the viewer's own index, never from pull requests or commits.
package starters

import (
	"strings"
	"unicode"
)

// State is what the offers depend on, for one viewer.
type State struct {
	Admin bool
	// Repo is the viewer's newest ready knowledge index, else a repository
	// GitHub connections allow (PickRepo); "" when there is neither.
	Repo string
	// RepoUnread: Repo came from GitHub, and the viewer has no index of it.
	RepoUnread bool
	// GitHub: a connection exists; GitHubRepos: it allows at least one
	// repository.
	GitHub      bool
	GitHubRepos bool
	WebSearch   bool
	ChatApp     bool // any chat app is on
	Accounts    int
	// Schedules names the viewer's own schedules, the check-in left out.
	Schedules []string
	Releases  []Release
}

// Release is a release environment and the repository it deploys.
type Release struct{ Name, Repo string }

// oneLine keeps a name from turning into a line break in text that isn't
// Markdown. Repository and environment names stay whole: "acme/my_repo"
// and "prod_eu" are valid names.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// plain keeps a name from turning into Markdown in the welcome message, or
// into a line break. An underscore is escaped rather than dropped, since
// names (a repository's included) may hold one.
func plain(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune("*`[]()<>\\#|~", r) {
			return -1
		}
		return r
	}, oneLine(s))
	return strings.ReplaceAll(s, "_", `\_`)
}

// PickRepo is the repository offered to a viewer with no index of their
// own: the first one a release environment deploys, since that is where
// the team works, else the first one GitHub allows (the alphabetical first
// may be a stub nobody uses).
func PickRepo(allowed []string, releases []Release) string {
	for _, r := range releases {
		for _, a := range allowed {
			if strings.EqualFold(a, r.Repo) {
				return a
			}
		}
	}
	if len(allowed) > 0 {
		return allowed[0]
	}
	return ""
}
