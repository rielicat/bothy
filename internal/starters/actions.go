package starters

// Row is one starter row: a prompt that fills the message box (Fill, else
// Text) and is never sent by itself, or, with Href, a link to set
// something up.
type Row struct {
	Text string
	Fill string
	Href string
}

// MaxRows is how many rows the empty chat and the welcome chat show.
const MaxRows = 4

// AskWhat is the last row, always; the web shows it alone when the rows
// can't be loaded.
const AskWhat = "What can you do for our team?"

// Rows are up to MaxRows starters for s: two prompts for work Bothy can do
// with what is set up, at most one link to something not set up yet (only
// admins get settings links), and AskWhat.
func Rows(s State) []Row {
	var rows []Row
	switch repo := plain(s.Repo); {
	case repo != "":
		rows = append(rows, Row{Text: "Summarise the open pull requests in " + repo}, Row{Text: "What changed in " + repo + " this week?"})
	case s.WebSearch:
		rows = append(rows, Row{Text: "Research a topic on the web", Fill: "Research this on the web and give me a short summary with links to sources: "},
			Row{Text: "Draft a release checklist for our next version"})
	default:
		rows = append(rows, Row{Text: "Draft a release checklist for our next version"},
			Row{Text: "Turn meeting notes into a list of tasks", Fill: "Turn these meeting notes into a list of tasks, with an owner for each:\n\n"})
	}
	if r, ok := discover(s); ok {
		rows = append(rows, r)
	}
	return append(rows, Row{Text: AskWhat})
}

// discover is the one row that points at something not set up yet, the
// first that applies.
func discover(s State) (Row, bool) {
	switch {
	case s.Admin && !s.GitHub:
		return Row{Text: "Connect GitHub so Bothy can read your code", Href: "/settings/connections/github"}, true
	case s.Admin && !s.GitHubRepos:
		return Row{Text: "Choose which repositories Bothy can read", Href: "/settings/connections/github"}, true
	case s.Admin && !s.ChatApp:
		return Row{Text: "Connect Slack, Discord or Telegram so your team can ask Bothy there", Href: "/settings/connections"}, true
	case s.Admin && s.Accounts <= 1:
		return Row{Text: "Add people so they get their own sign-in", Href: "/settings/people?add=1"}, true
	case len(s.Schedules) == 0 && len(Examples(s)) > 0:
		return Row{Text: "Have Bothy do something on a schedule", Href: "/activity/schedules"}, true
	}
	return Row{}, false
}
