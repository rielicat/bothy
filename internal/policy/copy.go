package policy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// Tool names the plain copy knows. Unknown tools get the generic copy.
const (
	toolFSRead        = "fs_read"
	toolFSList        = "fs_list"
	toolFSWrite       = "fs_write"
	toolFSDelete      = "fs_delete"
	toolShell         = "shell"
	toolPython        = "python"
	toolWebSearch     = "web_search"
	toolMessage       = "message"
	toolMemory        = "memory"
	toolSessionSearch = "session_search"
	toolToolSearch    = "tool_search"
	toolSkillLoad     = "skill_load"
	toolSkillRead     = "skill_read"
	toolScheduleList  = "schedule_list"
	toolScheduleNew   = "schedule_create"
	toolScheduleDel   = "schedule_delete"
	toolReleaseStatus = "release_status"
	toolRepoList      = "repo_list"
	toolRepoCheckout  = "repo_checkout"
	toolRepoChanges   = "repo_changes"
	toolRepoOpenPR    = "repo_open_pr"
	toolGitHubIssues  = "github_issues"
	toolGitHubPR      = "github_pr"
	toolGitHubRuns    = "github_runs"
	toolGitHubComment = "github_comment"
	toolKnowledge     = "knowledge_search"
	toolNotes         = "notes"
)

const (
	cantUndo        = "This can't be undone."
	allowForChat    = "Allow for this chat"
	maxSchemaFields = 6
	maxLabelRunes   = 40
)

// plainCard is a card's plain-language copy (docs/design/ui.md §8.2). It is
// built from the Action, the tool's spec and the decision only: model prose
// has no way in, and every identifier goes through inline() or visible().
type plainCard struct {
	question, consequence, verb, ing string
	kind                             core.CardKind
	destructive                      bool
	fields                           []core.CardField
	// keepCode is false where a field already shows the exact action (a
	// shell command, a message), so no surface shows it twice.
	keepCode bool
}

func (p *plainCard) add(label, value string, mono bool) {
	if value != "" {
		p.fields = append(p.fields, core.CardField{Label: label, Value: visible(value), Mono: mono})
	}
}

// applyPlain fills the card's plain copy and fields.
func applyPlain(c *core.Card, r core.ApprovalRequest, doc any) {
	a := r.Action
	se := c.Risk
	p := plainFor(r, a, doc, se)
	if p.kind == "" {
		p.kind = core.CardAction
	}
	if p.consequence == "" {
		switch se {
		case core.SEFinancial:
			p.consequence = "Money will leave your account. " + cantUndo
		case core.SEIrreversible:
			if p.kind != core.CardDeploy {
				p.consequence = cantUndo
			}
		}
	}
	if p.kind != core.CardDeploy && (se == core.SEIrreversible || se == core.SEFinancial) {
		p.destructive = true
	}
	c.Question, c.Consequence, c.Ing, c.Kind = p.question, p.consequence, p.ing, p.kind
	c.Tone = core.ToneDefault
	if p.destructive {
		c.Tone = core.ToneDestructive
	}
	c.PrimaryLabel = p.verb
	if !slices.Contains(c.Choices, core.AllowOnce) && slices.Contains(c.Choices, core.AllowSession) {
		c.PrimaryLabel = allowForChat
	}
	c.Fields = p.fields
	if !p.keepCode {
		c.Code = ""
	}
}

// plainFor picks the copy by tool, then by action kind.
func plainFor(r core.ApprovalRequest, a *core.Action, doc any, se core.SideEffect) plainCard {
	text := func(ptr string) string { s, _ := mustPointer(doc, ptr).(string); return s }
	number := func(ptr string) string {
		if n, ok := mustPointer(doc, ptr).(json.Number); ok {
			return n.String()
		}
		return ""
	}
	path := firstPath(a, text("/path"))
	switch a.Tool {
	case toolFSWrite:
		return fsWriteCopy(a, path, text("/mode"), text("/content"))
	case toolFSDelete:
		p := plainCard{question: "Delete " + inline(path) + "?", verb: "Delete file", ing: "deleting " + inline(path), keepCode: true}
		if b, _ := mustPointer(doc, "/recursive").(bool); b {
			p.question, p.verb, p.ing = "Delete the folder "+inline(path)+"?", "Delete folder", "deleting the folder "+inline(path)
			p.add("Folder", path, true)
			return p
		}
		p.add("File", path, true)
		if _, removed := diffCounts(a.Diff); removed > 0 {
			p.add("Content", lines(removed), false)
		}
		return p
	case toolFSRead:
		p := plainCard{question: "Read " + inline(path) + "?", verb: "Read file", ing: "reading " + inline(path), keepCode: true}
		p.add("File", path, true)
		return p
	case toolFSList:
		where := inline(path)
		if path == "." || path == "" {
			where = "Bothy's files"
		}
		p := plainCard{question: "List the files in " + where + "?", verb: "List files", ing: "listing the files in " + where, keepCode: true}
		p.add("Folder", path, true)
		return p
	case toolMessage:
		to := chatName(a.Recipient)
		p := plainCard{question: "Send a message to " + to + "?", verb: "Send message", ing: "sending a message to " + to}
		p.add("To", recipientText(a.Recipient), false)
		p.add("Message", text("/text"), false)
		return p
	case toolMemory:
		return memoryCopy(text("/file"), text("/op"), text("/old"), text("/new"))
	case toolNotes:
		repo := text("/repo")
		if text("/op") == "read" {
			p := plainCard{question: "Read the notes for " + inline(repo) + "?", verb: "Read notes", ing: "reading the notes for " + inline(repo), keepCode: true}
			p.add("Repository", repo, true)
			return p
		}
		p := plainCard{question: "Update the notes for " + inline(repo) + "?", verb: "Update notes",
			ing: "updating the notes for " + inline(repo), kind: core.CardReview, keepCode: true}
		p.add("Repository", repo, true)
		p.add("Section", text("/section"), false)
		return p
	case toolScheduleNew:
		name := quoted(text("/name"))
		p := plainCard{question: "Create the schedule " + name + "?", verb: "Create schedule", ing: "creating the schedule " + name, keepCode: true}
		p.add("When", cmp.Or(text("/when"), text("/spec")), false)
		p.add("Sends to", deliverText(doc), false)
		p.add("Does", text("/prompt"), false)
		return p
	case toolScheduleDel:
		name := quoted(text("/name"))
		p := plainCard{question: "Delete the schedule " + name + "?", verb: "Delete schedule", ing: "deleting the schedule " + name,
			consequence: cantUndo, destructive: true, keepCode: true}
		p.add("Schedule", text("/name"), false)
		return p
	case toolScheduleList:
		return plainCard{question: "List your schedules?", verb: "List schedules", ing: "listing your schedules", keepCode: true}
	case toolReleaseStatus:
		p := plainCard{question: "Check the releases?", verb: "Check releases", ing: "checking the releases", keepCode: true}
		p.add("Environment", text("/environment"), false)
		return p
	case toolRepoList:
		return plainCard{question: "List the connected repositories?", verb: "List repositories", ing: "listing the connected repositories", keepCode: true}
	case toolRepoCheckout:
		return checkoutCopy(a, text("/repo"), text("/ref"), text("/discard") != "")
	case toolRepoChanges:
		repo := text("/repo")
		p := plainCard{question: "Look at your edits to " + inline(repo) + "?", verb: "Show edits", ing: "looking at your edits to " + inline(repo), keepCode: true}
		p.add("Repository", repo, true)
		return p
	case toolRepoOpenPR:
		return pullRequestCopy(a, doc, text)
	case toolGitHubComment:
		repo, n := text("/repo"), number("/number")
		on := "#" + n + " in " + inline(repo)
		p := plainCard{question: "Comment on " + on + "?", verb: "Post comment", ing: "commenting on " + on}
		p.add("On", "#"+n+" in "+repo, false)
		p.add("Comment", text("/body"), false)
		return p
	case toolGitHubIssues, toolGitHubPR, toolGitHubRuns:
		return githubReadCopy(a.Tool, text("/repo"), number("/number"), number("/run_id"))
	case toolWebSearch, toolSessionSearch, toolKnowledge, toolToolSearch:
		return searchCopy(a.Tool, text("/query"))
	case toolSkillLoad, toolSkillRead:
		name := quoted(text("/name"))
		p := plainCard{question: "Use the " + name + " skill?", verb: "Use skill", ing: "using the " + name + " skill", keepCode: true}
		if a.Tool == toolSkillRead {
			file := text("/path")
			p.question, p.verb, p.ing = "Read "+inline(file)+" from the "+name+" skill?", "Read file", "reading "+inline(file)+" from the "+name+" skill"
			p.add("Skill", text("/name"), false)
			p.add("File", file, true)
			return p
		}
		p.add("Skill", text("/name"), false)
		return p
	case toolShell, toolPython:
		return execCopy(a, doc)
	case toolReleaseDeploy:
		return deployCopy(doc, text)
	}
	switch a.Kind {
	case core.ActExec:
		return execCopy(a, doc)
	case core.ActHTTP:
		return httpCopy(a)
	case core.ActNet:
		return netCopy(a)
	case core.ActRelease:
		return deployCopy(doc, text)
	case core.ActMCP:
		return mcpCopy(r, a, doc, se)
	case core.ActFSRead, core.ActFSWrite, core.ActFSDelete:
		p := plainCard{keepCode: true}
		label := "File"
		if len(a.Paths) > 1 {
			label = "Files"
		}
		p.add(label, strings.Join(a.Paths, "\n"), true)
		p.question, p.verb, p.ing = toolQuestion(r)
		return p
	case core.ActSend:
		to := cmp.Or(recipientText(a.Recipient), "someone Bothy can't name")
		p := plainCard{question: "Send a message to " + inline(to) + "?", verb: "Send message", ing: "sending a message to " + inline(to), keepCode: true}
		p.add("To", cmp.Or(recipientText(a.Recipient), "unknown"), false)
		return p
	}
	if se == core.SEAdmin {
		p := plainCard{question: "Change Bothy's settings?", verb: "Allow", ing: "changing Bothy's settings", keepCode: true}
		p.fields = schemaFields(r.Spec, doc)
		return p
	}
	p := plainCard{keepCode: true}
	p.question, p.verb, p.ing = toolQuestion(r)
	p.fields = schemaFields(r.Spec, doc)
	return p
}

func fsWriteCopy(a *core.Action, path, mode, content string) plainCard {
	p := plainCard{keepCode: true}
	added, removed := diffCounts(a.Diff)
	created := strings.HasPrefix(a.Diff, "--- /dev/null\n")
	full := completeDiff(a.Diff, a.Paths)
	n := contentLines(content)
	switch {
	case mode == "append":
		p.question, p.verb, p.ing = "Add to "+inline(path)+"?", "Add to file", "adding to "+inline(path)
		p.add("File", path, true)
		p.add("Adds", lines(n), false)
	case mode == "create" || created:
		p.question, p.verb, p.ing = "Create "+inline(path)+"?", "Create file", "creating "+inline(path)
		p.add("File", path, true)
		p.add("Content", lines(n), false)
	default:
		p.question, p.verb, p.ing = "Change "+inline(path)+"?", "Save changes", "changing "+inline(path)
		p.add("File", path, true)
		if full {
			p.add("Changes", fmt.Sprintf("+%d −%d", added, removed), false)
		} else {
			p.add("Changes", "The whole file is replaced: "+lines(n), false)
		}
	}
	return p
}

func memoryCopy(file, op, old, neu string) plainCard {
	where := map[string]string{"USER": "About you", "HEARTBEAT": "Check-in list"}[file]
	if where == "" {
		where = "Things Bothy remembers"
	}
	var p plainCard
	switch op {
	case "remove":
		p = plainCard{question: "Forget this?", verb: "Forget", ing: "forgetting a note"}
		p.add("Note", old, false)
	case "replace":
		p = plainCard{question: "Change something Bothy remembers?", verb: "Save change", ing: "changing a note"}
		p.add("Note", neu, false)
		p.add("Was", old, false)
	default:
		p = plainCard{question: "Remember this?", verb: "Remember", ing: "remembering a note"}
		p.add("Note", neu, false)
	}
	p.add("In", where, false)
	return p
}

func checkoutCopy(a *core.Action, repo, ref string, discard bool) plainCard {
	p := plainCard{question: "Download " + inline(repo) + " at " + inline(ref) + "?", verb: "Download",
		ing: "downloading " + inline(repo), keepCode: true}
	if discard {
		p.question, p.verb, p.ing = "Throw away your edits to "+inline(repo)+"?", "Discard edits", "throwing away your edits to "+inline(repo)
		p.consequence, p.destructive = "Your edits will be lost and "+inline(repo)+" downloaded again. "+cantUndo, true
	}
	p.add("Repository", repo, true)
	p.add("Version", ref, true)
	p.add("Folder", firstPath(a, ""), true)
	return p
}

func pullRequestCopy(a *core.Action, doc any, text func(string) string) plainCard {
	repo := text("/repo")
	p := plainCard{question: "Open a pull request in " + inline(repo) + "?", verb: "Open pull request",
		ing: "opening a pull request in " + inline(repo), kind: core.CardPullRequest, keepCode: true}
	if head, base := text("/head"), text("/base"); head != "" || base != "" {
		p.add("Branch", head+" → "+base, true)
	}
	p.add("Title", text("/title"), false)
	files, _ := mustPointer(doc, "/files").([]any)
	added, removed := diffCounts(a.Diff)
	p.add("Changes", fmt.Sprintf("%s, +%d −%d", plural(len(files), "file"), added, removed), false)
	if d, _ := mustPointer(doc, "/draft").(bool); d {
		p.add("Draft", "Yes", false)
	}
	return p
}

func githubReadCopy(tool, repo, number, runID string) plainCard {
	r := inline(repo)
	var p plainCard
	switch {
	case tool == toolGitHubIssues && number != "":
		p = plainCard{question: "Read issue #" + number + " in " + r + "?", verb: "Read issue", ing: "reading issue #" + number + " in " + r}
	case tool == toolGitHubIssues:
		p = plainCard{question: "Read the issues in " + r + "?", verb: "Read issues", ing: "reading the issues in " + r}
	case tool == toolGitHubPR && number != "":
		p = plainCard{question: "Read pull request #" + number + " in " + r + "?", verb: "Read pull request", ing: "reading pull request #" + number + " in " + r}
	case tool == toolGitHubPR:
		p = plainCard{question: "Read the pull requests in " + r + "?", verb: "Read pull requests", ing: "reading the pull requests in " + r}
	case runID != "":
		p = plainCard{question: "Check workflow run " + runID + " in " + r + "?", verb: "Check run", ing: "checking a workflow run in " + r}
	default:
		p = plainCard{question: "Check the workflow runs in " + r + "?", verb: "Check runs", ing: "checking the workflow runs in " + r}
	}
	p.keepCode = true
	p.add("Repository", repo, true)
	return p
}

func searchCopy(tool, query string) plainCard {
	q := quoted(query)
	var p plainCard
	switch tool {
	case toolWebSearch:
		p = plainCard{question: "Search the web for " + q + "?", verb: "Search the web", ing: "searching the web for " + q}
	case toolSessionSearch:
		p = plainCard{question: "Search your past chats for " + q + "?", verb: "Search chats", ing: "searching your past chats for " + q}
	case toolKnowledge:
		p = plainCard{question: "Search the code and notes for " + q + "?", verb: "Search", ing: "searching the code and notes for " + q}
	default:
		p = plainCard{question: "Look for more tools for " + q + "?", verb: "Look for tools", ing: "looking for more tools"}
	}
	p.keepCode = true
	p.add("Search", query, false)
	return p
}

// execCopy shows the command exactly: bash -lc's script as it runs, any
// other argv shell-quoted so it reads back as the argv it is.
func execCopy(a *core.Action, doc any) plainCard {
	argv := a.Argv
	if v, ok := pointer(doc, "/argv"); ok {
		if list, ok := stringList(v); ok {
			argv = list
		}
	}
	cwd := a.Cwd
	if s, ok := mustPointer(doc, "/cwd").(string); ok && s != "" {
		cwd = s
	}
	if code, ok := mustPointer(doc, "/code").(string); ok {
		p := plainCard{question: "Run this Python script?", verb: "Run script", ing: "running a Python script", keepCode: true}
		p.add("Script", lines(contentLines(code)), false)
		p.add("In", cwd, true)
		return p
	}
	cmd := shellJoin(argv)
	if len(argv) == 3 && argv[0] == "bash" && argv[1] == "-lc" {
		cmd = argv[2]
	}
	p := plainCard{question: "Run this command?", verb: "Run command", ing: "running a command"}
	p.add("Command", cmd, true)
	p.add("In", cwd, true)
	return p
}

func httpCopy(a *core.Action) plainCard {
	u, _ := punycodeURL(a.URL)
	host := "a website Bothy can't name"
	if hp := actionHosts(a); len(hp) > 0 && hp[0].host != "" {
		host = hp[0].host
	}
	p := plainCard{question: "Open a page on " + inline(host) + "?", verb: "Open page", ing: "opening a page on " + inline(host), keepCode: true}
	if a.Method != "" && !strings.EqualFold(a.Method, "GET") {
		p.question, p.verb, p.ing = "Send data to "+inline(host)+"?", "Send data", "sending data to "+inline(host)
		p.add("Website", u, true)
		p.add("What", "Sends data ("+strings.ToUpper(a.Method)+")", false)
		return p
	}
	p.add("Website", u, true)
	return p
}

func netCopy(a *core.Action) plainCard {
	where := "a website Bothy can't name"
	if hp := actionHosts(a); len(hp) > 0 {
		where = hp[0].host
		if hp[0].port != 443 && hp[0].port != 0 {
			where = hp[0].String()
		}
	}
	p := plainCard{question: "Let Bothy reach " + inline(where) + "?", verb: "Allow access", ing: "letting Bothy reach " + inline(where), keepCode: true}
	if hp := actionHosts(a); len(hp) > 0 {
		p.add("Website", hp[0].String(), true)
	}
	return p
}

// deployCopy shows the workflow and its inputs besides the version: they
// are exactly what runs once the approvers agree.
func deployCopy(doc any, text func(string) string) plainCard {
	env, repo := text("/environment"), text("/repo")
	p := plainCard{question: "Deploy " + inline(repo) + " to " + inline(env) + "?", verb: "Approve deploy",
		ing: "deploying " + inline(repo) + " to " + inline(env), kind: core.CardDeploy, keepCode: true,
		consequence: "This updates " + inline(env) + " for everyone using it."}
	p.add("Environment", env, false)
	p.add("Version", text("/ref"), true)
	p.add("Workflow", text("/workflow"), true)
	if m, ok := mustPointer(doc, "/inputs").(map[string]any); ok && len(m) > 0 {
		lines := make([]string, 0, len(m))
		for _, k := range slices.Sorted(maps.Keys(m)) {
			v, _ := pointerText(m, "/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"))
			lines = append(lines, k+"="+v)
		}
		p.add("Inputs", strings.Join(lines, "\n"), true)
	}
	return p
}

func mcpCopy(r core.ApprovalRequest, a *core.Action, doc any, se core.SideEffect) plainCard {
	what := serverName(a.MCPServer) + ": " + cmp.Or(r.Spec.Manifest.Title, a.MCPTool, a.Tool)
	p := plainCard{question: "Use " + inline(what) + "?", verb: "Allow", ing: "using " + inline(what), keepCode: true}
	if se == core.SEFinancial {
		p.question, p.verb, p.ing = "Make a payment with "+inline(what)+"?", "Pay", "making a payment with "+inline(what)
		if a.Recipient != "" {
			p.question, p.ing = "Pay "+inline(a.Recipient)+" with "+inline(what)+"?", "paying "+inline(a.Recipient)
		}
	}
	p.add("To", a.Recipient, false)
	u, _ := punycodeURL(a.URL)
	p.add("Website", u, true)
	p.fields = append(p.fields, schemaFields(r.Spec, doc)...)
	return p
}

// toolQuestion is the copy for a tool the card has no words for.
func toolQuestion(r core.ApprovalRequest) (question, verb, ing string) {
	name := cmp.Or(r.Spec.Manifest.Title, r.Spec.Def.Name)
	if name == "" && r.Action != nil {
		name = r.Action.Tool
	}
	return "Let Bothy use " + inline(name) + "?", "Allow", "using " + inline(name)
}

// schemaFields shows the scalar arguments the tool's own schema declares
// (its definition is pinned and reviewed, unlike argument names the model
// could invent), with humanised labels; the full arguments stay in Code.
func schemaFields(spec core.ToolSpec, doc any) []core.CardField {
	args, ok := doc.(map[string]any)
	if !ok || len(spec.Def.Schema) == 0 {
		return nil
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(spec.Def.Schema, &schema) != nil {
		return nil
	}
	var out []core.CardField
	for _, k := range slices.Sorted(maps.Keys(schema.Properties)) {
		if len(out) == maxSchemaFields {
			break
		}
		var v string
		switch x := args[k].(type) {
		case string:
			v = x
		case json.Number:
			v = x.String()
		case bool:
			v = map[bool]string{true: "Yes", false: "No"}[x]
		default:
			continue
		}
		if v != "" {
			out = append(out, core.CardField{Label: humanLabel(k), Value: visible(v)})
		}
	}
	return out
}

// humanLabel turns an argument name into a label: "dry_run" and "dryRun"
// both read "Dry run".
func humanLabel(k string) string {
	var b strings.Builder
	prev := ' '
	for _, r := range k {
		switch {
		case r == '_' || r == '-' || r == '.':
			r = ' '
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			b.WriteRune(' ')
		}
		if r == ' ' && prev == ' ' {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	s := strings.TrimSpace(inline(b.String()))
	if utf8.RuneCountInString(s) > maxLabelRunes {
		s = string([]rune(s)[:maxLabelRunes-1]) + "…"
	}
	if s == "" {
		return "Value"
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func deliverText(doc any) string {
	d, ok := mustPointer(doc, "/deliver").(map[string]any)
	if !ok {
		return "Your usual chat"
	}
	ch, _ := d["channel"].(string)
	if ch == "web" || ch == "" {
		return "Bothy on the web"
	}
	id, _ := d["chat_id"].(string)
	return appName(ch) + " chat " + id
}

// chatName names the chat a message goes to in a question.
func chatName(key string) string {
	ch, _, _ := strings.Cut(key, ":")
	if ch == "" {
		return "a chat"
	}
	return "the " + appName(ch) + " chat"
}

// recipientText shows a recipient verbatim, a chat key as "Telegram chat
// 12345".
func recipientText(key string) string {
	ch, rest, ok := strings.Cut(key, ":")
	switch ch {
	case "telegram", "discord", "slack":
		if ok {
			id, thread, _ := strings.Cut(rest, ":")
			if thread != "" {
				return appName(ch) + " chat " + id + ", thread " + thread
			}
			return appName(ch) + " chat " + id
		}
	case "github":
		if ok {
			return rest + " on GitHub"
		}
	}
	return key
}

func appName(ch string) string {
	switch ch {
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	case "slack":
		return "Slack"
	case "web":
		return "web"
	}
	return serverName(ch)
}

// serverName capitalises a configured name for a question ("linear" →
// "Linear").
func serverName(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// inline is visible() for text inside a sentence: line breaks become
// visible too, so a value can never push words onto a line of their own.
func inline(s string) string {
	s = visible(s)
	if !strings.ContainsAny(s, "\n\t") {
		return s
	}
	return strings.NewReplacer("\n", "⟨U+000A⟩", "\t", "⟨U+0009⟩").Replace(s)
}

func quoted(s string) string { return "“" + inline(s) + "”" }

func firstPath(a *core.Action, fallback string) string {
	if len(a.Paths) > 0 {
		return a.Paths[0]
	}
	return fallback
}

// diffCounts counts the added and removed lines of a unified diff.
func diffCounts(d string) (added, removed int) {
	for line := range strings.Lines(d) {
		switch {
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func contentLines(s string) int {
	n := strings.Count(s, "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func lines(n int) string { return plural(n, "line") }
