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
	// Repo is the viewer's newest ready knowledge index, else the first
	// repository GitHub connections allow; "" when there is neither.
	Repo string
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

// plain keeps a name from turning into Markdown in the welcome message, or
// into a line break anywhere.
func plain(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune("*_`[]()<>\\#|~", r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
