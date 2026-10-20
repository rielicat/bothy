package starters

import (
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// Example is a schedule people can start from. Nothing is created until
// they save it through the usual form, which a prompt still holding a
// [placeholder] can't pass.
type Example struct {
	ID     string
	Name   string
	Spec   string // 5-field cron, in the viewer's time zone
	Slot   core.Slot
	Prompt string
	Needs  string // NeedsGitHub | NeedsWebSearch
	// Ready: what it needs is set up. Offered also lists the others for
	// admins, without a prompt, pointing at what to set up first.
	Ready bool
}

const (
	NeedsGitHub    = "github"
	NeedsWebSearch = "web_search"
)

const (
	// MaxExamples is how many examples are offered at once.
	MaxExamples = 3
	// examplesUntil: people with this many schedules of their own know how.
	examplesUntil = 3
)

// examples in the order they are offered; {repo} is filled in.
var examples = []Example{
	{ID: "pr-summary", Name: "Monday pull request summary", Spec: "0 9 * * 1", Slot: core.SlotFast, Needs: NeedsGitHub,
		Prompt: "Summarise the pull requests in {repo} from the last 7 days: what was merged, what's still open, and anything that " +
			"has waited more than 3 days for a review. Keep it short."},
	{ID: "build-check", Name: "Morning build check", Spec: "30 8 * * 1-5", Slot: core.SlotFast, Needs: NeedsGitHub,
		Prompt: "Check the latest builds on the default branch of {repo}. If any failed since yesterday, say which ones and why in a " +
			"sentence or two. If they all passed, say so in one line."},
	{ID: "news", Name: "Morning news on a topic", Spec: "0 8 * * 1-5", Slot: core.SlotFast, Needs: NeedsWebSearch,
		Prompt: "Search the web for news from the last day about [your topic] and list the 5 most useful items, each with a " +
			"one-line summary and a link."},
	{ID: "release-notes", Name: "Friday release notes draft", Spec: "0 15 * * 5", Slot: core.SlotMain, Needs: NeedsGitHub,
		Prompt: "Draft release notes from the pull requests merged in {repo} in the last 7 days, grouped as New, Improved and Fixed, " +
			"in plain words for customers. Don't publish anything."},
}

// Examples are the schedules s can use, at most MaxExamples, leaving out
// any whose name the viewer already uses; none once the viewer has
// examplesUntil schedules of their own.
func Examples(s State) []Example {
	if len(s.Schedules) >= examplesUntil {
		return nil
	}
	repo := oneLine(s.Repo)
	var out []Example
	for _, e := range examples {
		if !ready(s, e) || used(s.Schedules, e.Name) {
			continue
		}
		e.Prompt = strings.ReplaceAll(e.Prompt, "{repo}", repo)
		e.Ready = true
		out = append(out, e)
		if len(out) == MaxExamples {
			break
		}
	}
	return out
}

// Offered are the examples the Schedules page shows: those s can use, then,
// for an admin, the others with no prompt, so a new install still shows
// what schedules can do and what to connect for them; at most MaxExamples.
func Offered(s State) []Example {
	out := Examples(s)
	if !s.Admin || len(s.Schedules) >= examplesUntil {
		return out
	}
	for _, e := range examples {
		if len(out) == MaxExamples {
			break
		}
		if ready(s, e) || used(s.Schedules, e.Name) {
			continue
		}
		e.Prompt = ""
		out = append(out, e)
	}
	return out
}

func ready(s State, e Example) bool {
	switch e.Needs {
	case NeedsGitHub:
		return oneLine(s.Repo) != "" && s.GitHub
	case NeedsWebSearch:
		return s.WebSearch
	}
	return true
}

func used(names []string, name string) bool {
	for _, n := range names {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			return true
		}
	}
	return false
}

// CheckinLines are lines someone with an empty check-in list may start
// from, about what s can watch: deploys to an environment of the
// repository, its pull requests waiting for review and its builds.
func CheckinLines(s State) []string {
	repo := oneLine(s.Repo)
	if repo == "" || !s.GitHub {
		return []string{}
	}
	var out []string
	for _, r := range s.Releases {
		if name := oneLine(r.Name); name != "" && strings.EqualFold(r.Repo, s.Repo) {
			out = append(out, "Tell me if a deploy to "+name+" failed.")
			break
		}
	}
	return append(out,
		"Tell me about pull requests in "+repo+" that have waited more than 2 days for a review.",
		"Tell me if a build on the default branch of "+repo+" failed.")
}
