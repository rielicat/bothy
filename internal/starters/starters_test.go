package starters

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

// set up is an admin's server with everything connected.
var setUp = State{Admin: true, Repo: "acme/api", GitHub: true, GitHubRepos: true, WebSearch: true, ChatApp: true, Accounts: 3}

func with(s State, f func(*State)) State {
	f(&s)
	return s
}

func TestRows(t *testing.T) {
	const (
		prs       = "Summarise the open pull requests in acme/api"
		changed   = "What changed in acme/api this week?"
		research  = "Research a topic on the web"
		checklist = "Draft a release checklist for our next version"
		notes     = "Turn meeting notes into a list of tasks"
		github    = "Connect GitHub so Bothy can read your code"
		repos     = "Choose which repositories Bothy can read"
		apps      = "Connect Slack, Discord or Telegram so your team can ask Bothy there"
		people    = "Add people so they get their own sign-in"
		schedule  = "Have Bothy do something on a schedule"
	)
	for _, c := range []struct {
		name string
		s    State
		want []string
	}{
		{"everything set up, no schedules", setUp, []string{prs, changed, schedule, AskWhat}},
		{"everything set up, a schedule", with(setUp, func(s *State) { s.Schedules = []string{"Standup"} }), []string{prs, changed, AskWhat}},
		{"admin without GitHub", State{Admin: true, Accounts: 2}, []string{checklist, notes, github, AskWhat}},
		{"admin without GitHub, with web search", State{Admin: true, WebSearch: true}, []string{research, checklist, github, AskWhat}},
		{"admin with GitHub, no repository allowed", with(setUp, func(s *State) { s.Repo, s.GitHubRepos = "", false }),
			[]string{research, checklist, repos, AskWhat}},
		{"admin without a chat app", with(setUp, func(s *State) { s.ChatApp = false }), []string{prs, changed, apps, AskWhat}},
		{"admin alone", with(setUp, func(s *State) { s.Accounts = 1 }), []string{prs, changed, people, AskWhat}},
		{"member with nothing set up", State{}, []string{checklist, notes, AskWhat}},
		{"member with web search", State{WebSearch: true}, []string{research, checklist, schedule, AskWhat}},
		{"member with a repository indexed but no GitHub", State{Repo: "local/site"},
			[]string{"How is local/site organised?", "Find where something is handled in local/site", AskWhat}},
		{"member, everything set up", with(setUp, func(s *State) { s.Admin = false; s.Schedules = []string{"a"} }), []string{prs, changed, AskWhat}},
	} {
		var got []string
		for _, r := range Rows(c.s) {
			got = append(got, r.Text)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: rows %q, want %q", c.name, got, c.want)
		}
	}
	links := map[string]string{github: "/settings/connections/github", repos: "/settings/connections/github", apps: "/settings/connections",
		people: "/settings/people?add=1", schedule: "/activity/schedules"}
	fills := map[string]string{research: "Research this on the web and give me a short summary with links to sources: ",
		notes: "Turn these meeting notes into a list of tasks, with an owner for each:\n\n"}
	for _, s := range []State{setUp, {Admin: true}, {Admin: true, WebSearch: true}, {WebSearch: true}, with(setUp, func(s *State) { s.Accounts = 1 })} {
		for _, r := range Rows(s) {
			if r.Href != links[r.Text] || r.Fill != fills[r.Text] {
				t.Errorf("row %q: href %q fill %q, want %q and %q", r.Text, r.Href, r.Fill, links[r.Text], fills[r.Text])
			}
		}
	}
}

// Members never get a settings link, and nobody gets more than MaxRows.
func TestRowsMembersNeverGetSettings(t *testing.T) {
	for mask := range 1 << 6 {
		s := State{GitHub: mask&1 != 0, GitHubRepos: mask&2 != 0, WebSearch: mask&4 != 0, ChatApp: mask&8 != 0, Accounts: 1 + mask&16}
		if mask&32 != 0 {
			s.Repo = "acme/api"
		}
		for _, admin := range []bool{false, true} {
			s.Admin = admin
			rows := Rows(s)
			if len(rows) > MaxRows || rows[len(rows)-1].Text != AskWhat {
				t.Errorf("%+v: %d rows ending %q", s, len(rows), rows[len(rows)-1].Text)
			}
			for _, r := range rows {
				if !admin && strings.HasPrefix(r.Href, "/settings") {
					t.Errorf("member %+v got a settings link %q", s, r.Href)
				}
			}
		}
	}
}

func TestExamples(t *testing.T) {
	ids := func(s State) []string {
		var out []string
		for _, e := range Examples(s) {
			out = append(out, e.ID)
		}
		return out
	}
	for _, c := range []struct {
		name string
		s    State
		want []string
	}{
		{"everything set up", setUp, []string{"pr-summary", "build-check", "news"}},
		{"GitHub only", with(setUp, func(s *State) { s.WebSearch = false }), []string{"pr-summary", "build-check", "release-notes"}},
		{"web search only", State{WebSearch: true}, []string{"news"}},
		{"a repository indexed without GitHub", State{Repo: "local/site"}, nil},
		{"nothing set up", State{}, nil},
		{"names already used, in any case", with(setUp, func(s *State) { s.Schedules = []string{" monday PULL request summary", "Standup"} }),
			[]string{"build-check", "news", "release-notes"}},
		{"three schedules of their own", with(setUp, func(s *State) { s.Schedules = []string{"a", "b", "c"} }), nil},
	} {
		if got := ids(c.s); !slices.Equal(got, c.want) {
			t.Errorf("%s: examples %q, want %q", c.name, got, c.want)
		}
	}
	all := Examples(with(setUp, func(s *State) { s.Schedules = []string{"Morning news on a topic"} }))
	want := []Example{
		{ID: "pr-summary", Name: "Monday pull request summary", Spec: "0 9 * * 1", Slot: core.SlotFast, Needs: NeedsGitHub, Ready: true,
			Prompt: "Summarise the pull requests in acme/api from the last 7 days: what was merged, what's still open, and anything that has waited more than 3 days for a review. Keep it short."},
		{ID: "build-check", Name: "Morning build check", Spec: "30 8 * * 1-5", Slot: core.SlotFast, Needs: NeedsGitHub, Ready: true,
			Prompt: "Check the latest builds on the default branch of acme/api. If any failed since yesterday, say which ones and why in a sentence or two. If they all passed, say so in one line."},
		{ID: "release-notes", Name: "Friday release notes draft", Spec: "0 15 * * 5", Slot: core.SlotMain, Needs: NeedsGitHub, Ready: true,
			Prompt: "Draft release notes from the pull requests merged in acme/api in the last 7 days, grouped as New, Improved and Fixed, in plain words for customers. Don't publish anything."},
	}
	if !slices.Equal(all, want) {
		t.Errorf("examples = %+v\nwant %+v", all, want)
	}
	news := Examples(State{WebSearch: true})
	if len(news) != 1 || !strings.Contains(news[0].Prompt, "[your topic]") || news[0].Needs != NeedsWebSearch {
		t.Errorf("news example = %+v; its placeholder must stay for the form to refuse", news)
	}
}

func TestCheckinLines(t *testing.T) {
	prs := "Tell me about pull requests in acme/api that have waited more than 2 days for a review."
	builds := "Tell me if a build on the default branch of acme/api failed."
	for _, c := range []struct {
		name string
		s    State
		want []string
	}{
		{"a release environment of the repository", with(setUp, func(s *State) {
			s.Releases = []Release{{Name: "staging", Repo: "acme/web"}, {Name: "production", Repo: "Acme/API"}, {Name: "canary", Repo: "acme/api"}}
		}), []string{"Tell me if a deploy to production failed.", prs, builds}},
		{"environments of other repositories only", with(setUp, func(s *State) { s.Releases = []Release{{Name: "staging", Repo: "acme/web"}} }),
			[]string{prs, builds}},
		{"no repository", State{WebSearch: true}, []string{}},
		{"a repository without GitHub", State{Repo: "local/site"}, []string{}},
	} {
		if got := CheckinLines(c.s); !slices.Equal(got, c.want) || got == nil {
			t.Errorf("%s: lines %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWelcome(t *testing.T) {
	want := "Hi Maya, I'm Bothy. I work for your team: I can research questions, draft documents, read your code in acme/api and keep an eye on things on a schedule.\n\n" +
		"Before I do anything you'd want to check, like changing a file or opening a pull request, I ask first and show exactly what I want to do.\n\n" +
		"- **Approvals** shows what's waiting for a decision.\n" +
		"- **Knowledge** holds your projects, what I remember and the skills your team has taught me.\n" +
		"- **Activity** shows what I've worked on, your schedules and what it cost.\n\n" +
		"Ask me anything to get started."
	if got := Welcome("Maya Chen", "Bothy", "acme/api"); got != want {
		t.Errorf("welcome =\n%s\nwant\n%s", got, want)
	}
	got := Welcome("  ", "", "")
	if !strings.HasPrefix(got, "Hi, I'm Bothy. I work for your team: I can research questions, draft documents, work through lists of tasks and ") {
		t.Errorf("welcome without a name or repository = %q", got)
	}
	// Names come from people and admins: none of them turns into Markdown.
	got = Welcome("[Maya](javascript:x) Chen", "**Ops** `bot`", "acme/api)")
	if !strings.HasPrefix(got, "Hi Mayajavascript:x, I'm Ops bot. ") || strings.Contains(got, "acme/api)") {
		t.Errorf("welcome with Markdown in names = %q", got)
	}
	if WelcomeTitle("") != "Welcome to Bothy" || WelcomeTitle("Ops bot") != "Welcome to Ops bot" {
		t.Errorf("titles %q, %q", WelcomeTitle(""), WelcomeTitle("Ops bot"))
	}
	// Underscores are valid in names and stay, escaped where Markdown
	// would read them as emphasis.
	got = Welcome("maya_c", "ops_bot", "acme/_tools_")
	if !strings.HasPrefix(got, `Hi maya\_c, I'm ops\_bot. I work for your team: I can research questions, draft documents, read your code in acme/\_tools\_ and `) {
		t.Errorf("welcome with underscores = %q", got)
	}
	if WelcomeTitle("ops_bot") != "Welcome to ops_bot" {
		t.Errorf("title %q", WelcomeTitle("ops_bot"))
	}
}

// Repository and environment names keep their underscores and dots in the
// rows, examples and check-in lines, which are plain text.
func TestNamesStayWhole(t *testing.T) {
	s := with(setUp, func(s *State) {
		s.Repo = "acme/my_repo.go"
		s.Releases = []Release{{Name: "prod_eu", Repo: "acme/my_repo.go"}}
	})
	var texts []string
	for _, r := range Rows(s) {
		texts = append(texts, r.Text)
	}
	for _, e := range Examples(s) {
		texts = append(texts, e.Prompt)
	}
	lines := CheckinLines(s)
	texts = append(texts, lines...)
	named := 0
	for _, text := range texts {
		if strings.Contains(text, "acme/my_repo.go") {
			named++
		}
	}
	// Two rows, the two GitHub examples offered and two check-in lines.
	if named != 6 || len(lines) != 3 || lines[0] != "Tell me if a deploy to prod_eu failed." {
		t.Errorf("%d texts name acme/my_repo.go, lines %q:\n%q", named, lines, texts)
	}
}

// Every string a person reads follows the copy guide (docs/design/ui.md
// §14): no exclamation marks, emoji, internal words or filler, no all-caps
// words, and "I" only in the welcome message, which speaks as Bothy.
func TestCopy(t *testing.T) {
	var texts []string
	states := []State{setUp, {}, {Admin: true}, {Admin: true, WebSearch: true}, {WebSearch: true}, with(setUp, func(s *State) { s.Accounts = 1 }),
		with(setUp, func(s *State) { s.ChatApp = false }), with(setUp, func(s *State) { s.Repo, s.GitHubRepos = "", false }),
		with(setUp, func(s *State) { s.Releases = []Release{{Name: "production", Repo: "acme/api"}} })}
	for _, s := range states {
		for _, r := range Rows(s) {
			texts = append(texts, r.Text, r.Fill)
		}
		for _, e := range Examples(s) {
			texts = append(texts, e.Name, e.Prompt)
		}
		for _, e := range Examples(with(s, func(s *State) {
			s.WebSearch, s.GitHub = true, true
			s.Repo = "acme/api"
			s.Schedules = []string{"Morning build check"}
		})) {
			texts = append(texts, e.Name, e.Prompt)
		}
		texts = append(texts, CheckinLines(s)...)
	}
	banned := regexp.MustCompile(`(?i)!|heartbeat|teammate|\btokens?\b|\bplease\b|\bsimply\b|\beasily\b|\bjust `)
	capsWord := regexp.MustCompile(`\b[A-Z]{2,}\b`)
	pronounI := regexp.MustCompile(`\bI\b`)
	check := func(text string, welcome bool) {
		if m := banned.FindString(text); m != "" {
			t.Errorf("%q uses %q", text, m)
		}
		if m := capsWord.FindString(text); m != "" {
			t.Errorf("%q has the all-caps word %q", text, m)
		}
		if !welcome && pronounI.MatchString(text) {
			t.Errorf("%q says I outside the welcome message", text)
		}
		if i := strings.IndexFunc(text, isSymbol); i >= 0 {
			t.Errorf("%q has the symbol %q", text, []rune(text[i:])[0])
		}
	}
	for _, text := range texts {
		check(text, false)
	}
	for _, w := range []string{Welcome("Maya", "Bothy", "acme/api"), Welcome("", "Bothy", ""), WelcomeTitle("Bothy")} {
		check(w, true)
	}
	if !pronounI.MatchString(Welcome("", "", "")) {
		t.Error("the welcome message no longer speaks as Bothy")
	}
	// The check itself catches what it is for.
	for _, bad := range []string{"Done!", "Check HEARTBEAT.md", "Ask a teammate", "Uses tokens", "Please wait", "Simply ask", "Easily done",
		"Just ask", "Read the PR", "I can help", "🎉 Ready"} {
		if !banned.MatchString(bad) && !capsWord.MatchString(bad) && !pronounI.MatchString(bad) && !strings.ContainsFunc(bad, isSymbol) {
			t.Errorf("the copy check lets %q through", bad)
		}
	}
}

// isSymbol is an emoji or other pictograph.
func isSymbol(r rune) bool {
	return unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) || r >= 0x1F000
}

// A new install shows admins what schedules can do: the examples they can't
// use yet come after the ones they can, with no prompt, marked not ready.
func TestOfferedShowsAdminsWhatToSetUp(t *testing.T) {
	ids := func(s State) (out []string) {
		for _, e := range Offered(s) {
			out = append(out, fmt.Sprintf("%s:%v", e.ID, e.Ready))
			if !e.Ready && e.Prompt != "" {
				t.Errorf("%s offered without what it needs, but with a prompt", e.ID)
			}
		}
		return out
	}
	for _, c := range []struct {
		name string
		s    State
		want []string
	}{
		{"admin, nothing set up", State{Admin: true}, []string{"pr-summary:false", "build-check:false", "news:false"}},
		{"admin with web search", State{Admin: true, WebSearch: true}, []string{"news:true", "pr-summary:false", "build-check:false"}},
		{"admin, everything set up", with(setUp, func(s *State) {}), []string{"pr-summary:true", "build-check:true", "news:true"}},
		{"member, nothing set up", State{}, nil},
		{"member with web search", State{WebSearch: true}, []string{"news:true"}},
		{"admin with three schedules", State{Admin: true, Schedules: []string{"a", "b", "c"}}, nil},
	} {
		if got := ids(c.s); !slices.Equal(got, c.want) {
			t.Errorf("%s: offered %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPickRepo(t *testing.T) {
	allowed := []string{"acme/api", "acme/web", "acme/docs"}
	for _, c := range []struct {
		name     string
		releases []Release
		want     string
	}{
		{"no environments", nil, "acme/api"},
		{"an environment's repository", []Release{{Name: "production", Repo: "ACME/Web"}}, "acme/web"},
		{"the first environment that is allowed", []Release{{Name: "x", Repo: "other/repo"}, {Name: "docs", Repo: "acme/docs"}, {Name: "p", Repo: "acme/web"}}, "acme/docs"},
		{"environments of other repositories only", []Release{{Name: "x", Repo: "other/repo"}}, "acme/api"},
	} {
		if got := PickRepo(allowed, c.releases); got != c.want {
			t.Errorf("%s: PickRepo = %q, want %q", c.name, got, c.want)
		}
	}
	if got := PickRepo(nil, []Release{{Name: "p", Repo: "acme/web"}}); got != "" {
		t.Errorf("nothing allowed: %q", got)
	}
}
