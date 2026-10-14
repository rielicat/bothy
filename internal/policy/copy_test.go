package policy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

type copyCase struct {
	name        string
	a           *core.Action
	s           core.ToolSpec
	choices     []core.Choice
	question    string
	primary     string
	ing         string
	consequence string
	kind        core.CardKind
	tone        core.CardTone
	labels      []string
}

// TestPlainCopy is the copy contract of docs/design/ui.md §8.2 for every
// tool Bothy ships, plus the fallbacks for MCP and unknown tools.
func TestPlainCopy(t *testing.T) {
	write := func(mode, diff string) *core.Action {
		return action("fs_write", core.ActFSWrite, core.SELocalWrite, map[string]any{"path": "notes/a.md", "content": "one\ntwo\n", "mode": mode},
			func(a *core.Action) { a.Paths, a.Diff = []string{"notes/a.md"}, diff })
	}
	created := "--- /dev/null\n+++ b/notes/a.md\n@@ -0,0 +1,2 @@\n+one\n+two\n"
	changed := "--- a/notes/a.md\n+++ b/notes/a.md\n@@ -1,3 +1,2 @@\n one\n-old\n-older\n+two\n"
	del := action("fs_delete", core.ActFSDelete, core.SEIrreversible, map[string]any{"path": "notes/staging.md"},
		func(a *core.Action) {
			a.Paths, a.Diff = []string{"notes/staging.md"}, "--- a/notes/staging.md\n+++ /dev/null\n@@ -1,8 +0,0 @@\n-1\n-2\n-3\n-4\n-5\n-6\n-7\n-8\n"
		})
	msg := action("message", core.ActSend, core.SELocalWrite, map[string]any{"text": "Done: 3 files"}, func(a *core.Action) { a.Recipient = "telegram:42:" })
	pr := action("repo_open_pr", core.ActSend, core.SEExternalSend, map[string]any{"repo": "acme/web", "base": "main", "head": "bothy/fix-login",
		"title": "Fix login redirect", "body": "x", "files": []map[string]any{{"path": "a.go"}, {"path": "b.go"}, {"path": "c.go"}}},
		func(a *core.Action) {
			a.Recipient = "github:acme/web"
			a.Diff = "Repository: acme/web (private)\nBranch: bothy/fix-login → main\n\ndiff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n-x\n+y\n+z\n"
			a.Domains = []string{"api.github.com"}
		})
	mcp := action("mcp__linear__create_issue", core.ActMCP, core.SEExternalSend, map[string]any{"title": "Bug", "team_id": "ENG", "dryRun": true, "labels": []string{"x"}},
		func(a *core.Action) { a.MCPServer, a.MCPTool = "linear", "create_issue" })
	mcpSpec := spec("mcp__linear__create_issue", core.SEExternalSend, func(m *core.Manifest) { m.Title = "Create issue" })
	mcpSpec.Def.Schema = json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"team_id":{"type":"string"},"dryRun":{"type":"boolean"},"labels":{"type":"array"}}}`)
	mcpGone := action("mcp__linear__delete_issue", core.ActMCP, core.SEIrreversible, map[string]any{"id": "ENG-1"},
		func(a *core.Action) { a.MCPServer, a.MCPTool = "linear", "delete_issue" })
	pay := action("mcp__bank__pay", core.ActMCP, core.SEFinancial, map[string]any{"to": "acme@bank"},
		func(a *core.Action) { a.MCPServer, a.MCPTool, a.Recipient = "bank", "pay", "acme@bank" })

	once := []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}
	cases := []copyCase{
		{name: "fs_write create", a: write("create", created), s: spec("fs_write", core.SELocalWrite),
			question: "Create notes/a.md?", primary: "Create file", ing: "creating notes/a.md", labels: []string{"File", "Content"}},
		{name: "fs_write overwrite of a new file", a: write("overwrite", created), s: spec("fs_write", core.SELocalWrite),
			question: "Create notes/a.md?", primary: "Create file", ing: "creating notes/a.md", labels: []string{"File", "Content"}},
		{name: "fs_write overwrite", a: write("overwrite", changed), s: spec("fs_write", core.SELocalWrite),
			question: "Change notes/a.md?", primary: "Save changes", ing: "changing notes/a.md", labels: []string{"File", "Changes"}},
		{name: "fs_write append", a: write("append", changed), s: spec("fs_write", core.SELocalWrite),
			question: "Add to notes/a.md?", primary: "Add to file", ing: "adding to notes/a.md", labels: []string{"File", "Adds"}},
		{name: "fs_delete", a: del, s: spec("fs_delete", core.SEIrreversible),
			question: "Delete notes/staging.md?", primary: "Delete file", ing: "deleting notes/staging.md", consequence: "This can't be undone.",
			tone: core.ToneDestructive, labels: []string{"File", "Content"}},
		{name: "fs_read", a: action("fs_read", core.ActFSRead, core.SERead, map[string]any{"path": "a.txt"}, func(a *core.Action) { a.Paths = []string{"a.txt"} }),
			s: spec("fs_read", core.SERead), question: "Read a.txt?", primary: "Read file", ing: "reading a.txt", labels: []string{"File"}},
		{name: "fs_list", a: action("fs_list", core.ActFSRead, core.SERead, map[string]any{"path": "."}, func(a *core.Action) { a.Paths = []string{"."} }),
			s: spec("fs_list", core.SERead), question: "List the files in Bothy's files?", primary: "List files", ing: "listing the files in Bothy's files", labels: []string{"Folder"}},
		{name: "shell", a: shellAction("make test"), s: spec("shell", core.SELocalWrite),
			question: "Run this command?", primary: "Run command", ing: "running a command", labels: []string{"Command"}},
		{name: "python", a: action("python", core.ActExec, core.SELocalWrite, map[string]any{"argv": []string{"python3", "x.py"}, "code": "print(1)", "cwd": "work"}),
			s: spec("python", core.SELocalWrite), question: "Run this Python script?", primary: "Run script", ing: "running a Python script", labels: []string{"Script", "In"}},
		{name: "web_fetch", a: httpAction("web_fetch", "GET", "https://example.com/a", core.SERead), s: spec("web_fetch", core.SERead),
			question: "Open a page on example.com?", primary: "Open page", ing: "opening a page on example.com", labels: []string{"Website"}},
		{name: "web_fetch non-GET", a: httpAction("web_fetch", "POST", "https://api.example.com/a", core.SEExternalSend), s: spec("web_fetch", core.SEExternalSend),
			question: "Send data to api.example.com?", primary: "Send data", ing: "sending data to api.example.com", labels: []string{"Website", "What"}},
		{name: "message", a: msg, s: spec("message", core.SELocalWrite),
			question: "Send a message to the Telegram chat?", primary: "Send message", ing: "sending a message to the Telegram chat", labels: []string{"To", "Message"}},
		{name: "memory add", a: action("memory", core.ActMemory, core.SELocalWrite, map[string]any{"file": "USER", "op": "add", "new": "Prefers mornings"}),
			s: spec("memory", core.SELocalWrite), question: "Remember this?", primary: "Remember", ing: "remembering a note", labels: []string{"Note", "In"}},
		{name: "memory replace", a: action("memory", core.ActMemory, core.SELocalWrite, map[string]any{"file": "MEMORY", "op": "replace", "old": "a", "new": "b"}),
			s: spec("memory", core.SELocalWrite), question: "Change something Bothy remembers?", primary: "Save change", ing: "changing a note", labels: []string{"Note", "Was", "In"}},
		{name: "memory remove", a: action("memory", core.ActMemory, core.SELocalWrite, map[string]any{"file": "MEMORY", "op": "remove", "old": "a"}),
			s: spec("memory", core.SELocalWrite), question: "Forget this?", primary: "Forget", ing: "forgetting a note", labels: []string{"Note", "In"}},
		{name: "notes", a: action("notes", core.ActMemory, core.SELocalWrite, map[string]any{"repo": "acme/web", "op": "append", "section": "Conventions", "text": "x"}),
			s: spec("notes", core.SELocalWrite), question: "Update the notes for acme/web?", primary: "Update notes", ing: "updating the notes for acme/web",
			kind: core.CardReview, labels: []string{"Repository", "Section"}},
		{name: "notes read", a: action("notes", core.ActGeneric, core.SERead, map[string]any{"repo": "acme/web", "op": "read"}),
			s: spec("notes", core.SERead), question: "Read the notes for acme/web?", primary: "Read notes", ing: "reading the notes for acme/web", labels: []string{"Repository"}},
		{name: "schedule_create", a: action("schedule_create", core.ActSchedule, core.SEAdmin, map[string]any{"name": "Standup", "when": "Weekdays at 09:00",
			"spec": "0 9 * * 1-5", "prompt": "Summarise\nyesterday", "deliver": map[string]any{"channel": "slack", "chat_id": "C1"}}),
			s: spec("schedule_create", core.SEAdmin), question: "Create the schedule “Standup”?", primary: "Create schedule", ing: "creating the schedule “Standup”",
			labels: []string{"When", "Sends to", "Does"}},
		{name: "schedule_delete", a: action("schedule_delete", core.ActSchedule, core.SEAdmin, map[string]any{"id": "sch_x", "name": "Standup"}),
			s: spec("schedule_delete", core.SEAdmin), question: "Delete the schedule “Standup”?", primary: "Delete schedule", ing: "deleting the schedule “Standup”",
			consequence: "This can't be undone.", tone: core.ToneDestructive, labels: []string{"Schedule"}},
		{name: "schedule_list", a: action("schedule_list", core.ActSchedule, core.SERead, map[string]any{}), s: spec("schedule_list", core.SERead),
			question: "List your schedules?", primary: "List schedules", ing: "listing your schedules"},
		{name: "net_allow", a: netAction("pypi.org", 443), s: spec("net_allow", core.SEExternalSend), choices: []core.Choice{core.AllowSession, core.Deny},
			question: "Let Bothy reach pypi.org?", primary: "Allow for this chat", ing: "letting Bothy reach pypi.org", labels: []string{"Website"}},
		{name: "proxy hold on another port", a: netAction("db.internal", 5432), s: spec("net_allow", core.SEExternalSend),
			question: "Let Bothy reach db.internal:5432?", primary: "Allow access", ing: "letting Bothy reach db.internal:5432", labels: []string{"Website"}},
		{name: "repo_open_pr", a: pr, s: spec("repo_open_pr", core.SEExternalSend),
			question: "Open a pull request in acme/web?", primary: "Open pull request", ing: "opening a pull request in acme/web",
			kind: core.CardPullRequest, labels: []string{"Branch", "Title", "Changes", "Websites"}},
		{name: "github_comment", a: action("github_comment", core.ActSend, core.SEExternalSend, map[string]any{"repo": "acme/web", "number": 482, "body": "LGTM"}),
			s: spec("github_comment", core.SEExternalSend), question: "Comment on #482 in acme/web?", primary: "Post comment", ing: "commenting on #482 in acme/web",
			labels: []string{"On", "Comment"}},
		{name: "github_issues", a: action("github_issues", core.ActHTTP, core.SERead, map[string]any{"repo": "acme/web"}), s: spec("github_issues", core.SERead),
			question: "Read the issues in acme/web?", primary: "Read issues", ing: "reading the issues in acme/web", labels: []string{"Repository"}},
		{name: "github_pr", a: action("github_pr", core.ActHTTP, core.SERead, map[string]any{"repo": "acme/web", "number": 7}), s: spec("github_pr", core.SERead),
			question: "Read pull request #7 in acme/web?", primary: "Read pull request", ing: "reading pull request #7 in acme/web", labels: []string{"Repository"}},
		{name: "github_runs", a: action("github_runs", core.ActHTTP, core.SERead, map[string]any{"repo": "acme/web"}), s: spec("github_runs", core.SERead),
			question: "Check the workflow runs in acme/web?", primary: "Check runs", ing: "checking the workflow runs in acme/web", labels: []string{"Repository"}},
		{name: "repo_list", a: action("repo_list", core.ActGeneric, core.SERead, map[string]any{}), s: spec("repo_list", core.SERead),
			question: "List the connected repositories?", primary: "List repositories", ing: "listing the connected repositories"},
		{name: "repo_checkout", a: action("repo_checkout", core.ActFSWrite, core.SELocalWrite, map[string]any{"repo": "acme/web", "ref": "main"},
			func(a *core.Action) { a.Paths = []string{"repos/acme/web"} }), s: spec("repo_checkout", core.SELocalWrite),
			question: "Download acme/web at main?", primary: "Download", ing: "downloading acme/web", labels: []string{"Repository", "Version", "Folder"}},
		{name: "repo_checkout discarding edits", a: action("repo_checkout", core.ActFSWrite, core.SEIrreversible, map[string]any{"repo": "acme/web", "ref": "main", "discard": "abc"},
			func(a *core.Action) { a.Paths = []string{"repos/acme/web"} }), s: spec("repo_checkout", core.SELocalWrite),
			question: "Throw away your edits to acme/web?", primary: "Discard edits", ing: "throwing away your edits to acme/web",
			consequence: "Your edits will be lost and acme/web downloaded again. This can't be undone.", tone: core.ToneDestructive,
			labels: []string{"Repository", "Version", "Folder"}},
		{name: "repo_changes", a: action("repo_changes", core.ActFSRead, core.SERead, map[string]any{"repo": "acme/web", "diff": true}), s: spec("repo_changes", core.SERead),
			question: "Look at your edits to acme/web?", primary: "Show edits", ing: "looking at your edits to acme/web", labels: []string{"Repository"}},
		{name: "release_deploy", a: releaseAction("production", "main", nil), s: spec("release_deploy", core.SEIrreversible),
			question: "Deploy acme/web to production?", primary: "Approve deploy", ing: "deploying acme/web to production",
			consequence: "This updates production for everyone using it.", kind: core.CardDeploy, labels: []string{"Environment", "Version", "Workflow"}},
		{name: "release_status", a: action("release_status", core.ActGeneric, core.SERead, map[string]any{}), s: spec("release_status", core.SERead),
			question: "Check the releases?", primary: "Check releases", ing: "checking the releases"},
		{name: "web_search", a: action("web_search", core.ActSearch, core.SERead, map[string]any{"query": "go 1.27 release"}), s: spec("web_search", core.SERead),
			question: "Search the web for “go 1.27 release”?", primary: "Search the web", ing: "searching the web for “go 1.27 release”", labels: []string{"Search"}},
		{name: "session_search", a: action("session_search", core.ActSearch, core.SERead, map[string]any{"query": "invoice"}), s: spec("session_search", core.SERead),
			question: "Search your past chats for “invoice”?", primary: "Search chats", ing: "searching your past chats for “invoice”", labels: []string{"Search"}},
		{name: "knowledge_search", a: action("knowledge_search", core.ActSearch, core.SERead, map[string]any{"query": "auth"}), s: spec("knowledge_search", core.SERead),
			question: "Search the code and notes for “auth”?", primary: "Search", ing: "searching the code and notes for “auth”", labels: []string{"Search"}},
		{name: "tool_search", a: action("tool_search", core.ActSearch, core.SERead, map[string]any{"query": "calendar"}), s: spec("tool_search", core.SERead),
			question: "Search Bothy's other abilities for “calendar”?", primary: "Search", ing: "searching Bothy's other abilities for “calendar”", labels: []string{"Search"}},
		{name: "skill_load", a: action("skill_load", core.ActSkill, core.SERead, map[string]any{"name": "pdf"}), s: spec("skill_load", core.SERead),
			question: "Use the “pdf” skill?", primary: "Use skill", ing: "using the “pdf” skill", labels: []string{"Skill"}},
		{name: "skill_read", a: action("skill_read", core.ActSkill, core.SERead, map[string]any{"name": "pdf", "path": "forms.md"}), s: spec("skill_read", core.SERead),
			question: "Read forms.md from the “pdf” skill?", primary: "Read file", ing: "reading forms.md from the “pdf” skill", labels: []string{"Skill", "File"}},
		{name: "MCP tool", a: mcp, s: mcpSpec, question: "Use Linear: Create issue?", primary: "Allow", ing: "using Linear: Create issue",
			labels: []string{"Dry run", "Team id", "Title"}},
		{name: "MCP tool that can't be undone", a: mcpGone, s: spec("mcp__linear__delete_issue", core.SEIrreversible, func(m *core.Manifest) { m.Title = "Delete issue" }),
			question: "Use Linear: Delete issue?", primary: "Allow", ing: "using Linear: Delete issue", consequence: "This can't be undone.", tone: core.ToneDestructive},
		{name: "financial", a: pay, s: spec("mcp__bank__pay", core.SEFinancial, func(m *core.Manifest) { m.Title = "Pay" }),
			question: "Pay acme@bank with Bank: Pay?", primary: "Pay", ing: "paying acme@bank",
			consequence: "Money will leave your account. This can't be undone.", tone: core.ToneDestructive, labels: []string{"To"}},
		{name: "admin class", a: action("tune", core.ActGeneric, core.SEAdmin, map[string]any{"x": 1}), s: spec("tune", core.SEAdmin),
			question: "Change Bothy's settings?", primary: "Allow", ing: "changing Bothy's settings"},
		{name: "unknown tool", a: action("thing", core.ActGeneric, core.SELocalWrite, map[string]any{"v": 1}), s: spec("thing", core.SELocalWrite, func(m *core.Manifest) { m.Title = "Do a thing" }),
			question: "Let Bothy use Do a thing?", primary: "Allow", ing: "using Do a thing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := cardDecision()
			d.Choices = once
			if tc.choices != nil {
				d.Choices = tc.choices
			}
			c := renderCard(core.ApprovalRequest{Action: tc.a, Spec: tc.s, Decision: d, Context: "Web · session “x”"})
			kind, tone := cmpOr(tc.kind, core.CardAction), cmpOr(tc.tone, core.ToneDefault)
			if c.Question != tc.question || c.PrimaryLabel != tc.primary || c.Ing != tc.ing || c.Consequence != tc.consequence || c.Kind != kind || c.Tone != tone {
				t.Errorf("copy = question %q primary %q ing %q consequence %q kind %s tone %s", c.Question, c.PrimaryLabel, c.Ing, c.Consequence, c.Kind, c.Tone)
			}
			if !slices.Equal(labels(c), tc.labels) && (len(tc.labels) > 0 || len(c.Fields) > 0) {
				t.Errorf("labels %v, want %v", labels(c), tc.labels)
			}
			// No tool identifiers, classes or config words in the main copy.
			jargons := []string{"irreversible", "external_send", "session", "Allow once"}
			if strings.Contains(tc.a.Tool, "_") {
				jargons = append(jargons, tc.a.Tool)
			}
			for _, s := range append([]string{c.Question, c.PrimaryLabel, c.Ing, c.Consequence}, labels(c)...) {
				for _, jargon := range jargons {
					if strings.Contains(s, jargon) {
						t.Errorf("%q says %q", s, jargon)
					}
				}
			}
		})
	}
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// TestPlainCopyExactAction: where a field shows the action itself it shows
// it whole, and Code is left out only when nothing would be lost.
func TestPlainCopyExactAction(t *testing.T) {
	sh := cardFor(shellAction("echo hi && rm -rf /tmp/x"), spec("shell", core.SELocalWrite))
	if f := field(sh, "Command"); f.Value != "echo hi && rm -rf /tmp/x" || sh.Code != "" {
		t.Errorf("shell: %+v code %q", f, sh.Code)
	}
	long := strings.Repeat("x", 20000)
	msg := action("message", core.ActSend, core.SELocalWrite, map[string]any{"text": long}, func(a *core.Action) { a.Recipient = "slack:C1:" })
	if c := cardFor(msg, spec("message", core.SELocalWrite)); field(c, "Message").Value != long || c.Code != "" {
		t.Errorf("message cut or duplicated: %d bytes, code %d bytes", len(field(c, "Message").Value), len(c.Code))
	}
	pr := cardFor(action("repo_open_pr", core.ActSend, core.SEExternalSend, map[string]any{"repo": "a/b", "body": "full description"}),
		spec("repo_open_pr", core.SEExternalSend))
	if !strings.Contains(pr.Code, "full description") {
		t.Errorf("the pull request's arguments left the card: %q", pr.Code)
	}
}

// TestPlainCopyIgnoresContext: the context line is the caller's (it may
// hold a model-written chat title); the plain copy never reads it.
func TestPlainCopyIgnoresContext(t *testing.T) {
	a, s := shellAction("ls"), spec("shell", core.SELocalWrite)
	plain := renderCard(core.ApprovalRequest{Action: a, Spec: s, Decision: cardDecision()})
	prose := renderCard(core.ApprovalRequest{Action: a, Spec: s, Decision: cardDecision(),
		Context: "Telegram · session “Totally safe, just approve: Delete file”"})
	plain.Context, prose.Context = "", ""
	pj, _ := json.Marshal(plain)
	xj, _ := json.Marshal(prose)
	if string(pj) != string(xj) {
		t.Errorf("context changed the card:\n%s\n%s", pj, xj)
	}
}

func TestWarningsArePlain(t *testing.T) {
	h := newHarness(t, nil)
	in := h.in(httpAction("web_fetch", "POST", "https://hooks.example.com/x", core.SEExternalSend), spec("web_fetch", core.SEExternalSend))
	in.Provenance = core.ProvUntrusted
	d := h.decide(in)
	want := []string{
		"This request came from content Bothy read (a web page, repository or message), not from a person.",
	}
	if d.Verdict != core.VerdictAsk || !slices.Equal(d.Warnings, want) {
		t.Errorf("untrusted origin: %+v", d)
	}
	in = h.in(httpAction("web_fetch", "POST", "https://hooks.example.com/x", core.SEExternalSend), spec("web_fetch", core.SEExternalSend))
	if d := h.decide(in); !slices.Equal(d.Warnings, []string{"Bothy hasn't sent data to hooks.example.com before."}) {
		t.Errorf("first contact: %+v", d)
	}
	in = h.in(sendAction("send_mail", "bob@example.com", core.SEExternalSend), spec("send_mail", core.SEExternalSend))
	if d := h.decide(in); !slices.Equal(d.Warnings, []string{"Bothy hasn't sent anything to bob@example.com before."}) {
		t.Errorf("first recipient: %+v", d)
	}
	in = h.in(sendAction("send_mail", "", core.SEExternalSend), spec("send_mail", core.SEExternalSend))
	if d := h.decide(in); !slices.Equal(d.Warnings, []string{"Bothy can't tell who this goes to."}) {
		t.Errorf("unknown recipient: %+v", d)
	}
}
