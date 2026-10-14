//go:build e2e

package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/github/githubtest"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestGitHubPullRequest is the F4 scenario: an admin connects GitHub (the
// fake API) with a personal access token, the agent checks a repository
// out, edits a file with fs_write and proposes it with repo_open_pr; the
// card shows the repository, branches and the full diff, and approving it
// creates the pull request with exactly that change. The checkout also feeds
// the knowledge index (F6), which drafts project notes and records the
// indexed commit on the repository. The token reaches neither the logs nor
// the model.
func TestGitHubPullRequest(t *testing.T) {
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	repo := gh.AddRepo("octocat", "Hello-World", true, map[string]string{
		"README.md":   "# Hello World\n\nA test repository.\n",
		"src/main.go": "package main\n\nfunc main() {}\n",
	})
	base := repo.Head("main")

	const readme = "# Hello World\n\nA test repository, now with a README edit by Bothy.\n"
	llm := pltLLM(t,
		mockllm.Rule{MatchSystem: `^You keep short project notes`, Text: []string{"## Summary\nHello World is a test repository.\n"}},
		mockllm.Rule{MatchUser: `F4-PR`, MatchToolResult: `Opened pull request`, Text: []string{"The pull request is open."}},
		mockllm.Rule{MatchUser: `F4-PR`, MatchToolResult: `Wrote \d+ bytes`, ToolCalls: []core.ToolCall{pltCall("call_pr", "use_tool",
			`{"name":"repo_open_pr","args":{"repo":"octocat/Hello-World","title":"Clarify the README","body":"Explains what the repository is for."}}`)}},
		mockllm.Rule{MatchUser: `F4-PR`, MatchToolResult: `Checked out`, ToolCalls: []core.ToolCall{pltCall("call_w", "fs_write",
			`{"path":"repos/octocat/Hello-World/README.md","content":"`+strings.ReplaceAll(readme, "\n", `\n`)+`"}`)}},
		mockllm.Rule{MatchUser: `F4-PR`, ToolCalls: []core.ToolCall{pltCall("call_co", "use_tool",
			`{"name":"repo_checkout","args":{"repo":"octocat/Hello-World"}}`)}},
	)
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, "github:\n  api_base: "+gh.URL+"\n")})
	owner := pltOwner(t, g)

	var probe core.GitHubProbe
	if code := owner.JSON(t, http.MethodPost, "/api/github/connections/test", dto.GitHubConnectReq{Auth: core.GitHubPAT, Token: gh.PAT}, &probe); code != http.StatusOK ||
		!probe.OK || probe.Account != "octocat" || len(probe.Repos) != 1 {
		t.Fatalf("test connection = %d %+v", code, probe)
	}
	var conn core.GitHubConnection
	req := dto.GitHubConnectReq{Auth: core.GitHubPAT, Token: gh.PAT, Repos: []string{"octocat/Hello-World"}}
	if code := owner.JSON(t, http.MethodPost, "/api/github/connections", req, &conn); code != http.StatusOK || conn.ID == "" || !conn.OwnsSecret {
		t.Fatalf("connect = %d %+v", code, conn)
	}
	var repos []core.GitHubRepo
	if code := owner.JSON(t, http.MethodGet, "/api/github/repos", nil, &repos); code != http.StatusOK || len(repos) != 1 || !repos[0].Allowed {
		t.Fatalf("repos = %d %+v", code, repos)
	}

	sid := owner.NewSession(t).ID
	run := owner.Send(t, sid, "F4-PR fix the README of octocat/Hello-World")
	ap := owner.WaitApproval(t, run, core.ApprovalPending)
	// The checkout made the session private and untrusted, so the trifecta
	// floor asks before the always-ask one would.
	if ap.Action == nil || ap.Action.Tool != "repo_open_pr" || ap.Decision.Rule != "floor:trifecta" || !ap.Decision.Floor {
		t.Fatalf("approval = %+v %+v", ap.Action, ap.Decision)
	}
	if !slices.Contains(ap.Action.Paths, "repos/octocat/Hello-World/README.md") {
		t.Errorf("action paths = %v", ap.Action.Paths)
	}
	card := ap.Card
	if card.Title != "Open pull request" || !slices.Equal(card.Choices, []core.Choice{core.AllowOnce, core.Deny}) {
		t.Errorf("card = %+v", card)
	}
	recipient := slices.IndexFunc(card.Fields, func(f core.CardField) bool { return f.Label == "Recipient" && f.Value == "github:octocat/Hello-World" })
	if recipient < 0 || !strings.Contains(card.Code, `"base": "main"`) || !strings.Contains(card.Code, `"head": "bothy/clarify-the-readme-`) ||
		!strings.Contains(card.Code, `"title": "Clarify the README"`) {
		t.Errorf("card fields %+v code:\n%s", card.Fields, card.Code)
	}
	if !strings.HasPrefix(card.Diff, "Repository: octocat/Hello-World (private)\nBranch: bothy/clarify-the-readme-") ||
		!strings.Contains(card.Diff, " → main\nTitle: Clarify the README\n\n    Explains what the repository is for.\n---\n") ||
		!strings.Contains(card.Diff, "-A test repository.") || !strings.Contains(card.Diff, "+A test repository, now with a README edit by Bothy.") ||
		!strings.Contains(card.Diff, "diff --git a/README.md b/README.md") {
		t.Errorf("card diff:\n%s", card.Diff)
	}
	if !slices.Contains(card.Warnings, "First contact with github:octocat/Hello-World.") {
		t.Errorf("warnings = %v", card.Warnings)
	}
	if len(repo.Pulls()) != 0 {
		t.Fatal("a pull request exists before approval")
	}

	owner.Resolve(t, ap.ID, core.AllowOnce)
	d := owner.Finished(t, run)
	if d.Run.Status != core.RunDone {
		t.Fatalf("run = %+v %+v", d.Run, d.ToolCalls)
	}
	var tools []string
	for _, tc := range d.ToolCalls {
		tools = append(tools, tc.Tool+":"+string(tc.Status))
	}
	if !slices.Equal(tools, []string{"repo_checkout:ok", "fs_write:ok", "repo_open_pr:ok"}) {
		t.Errorf("tool calls = %v", tools)
	}
	if !d.ToolCalls[0].Untrusted {
		t.Error("checkout result not marked untrusted")
	}
	pulls := repo.Pulls()
	if len(pulls) != 1 || pulls[0].Title != "Clarify the README" || pulls[0].Base != "main" || !strings.HasPrefix(pulls[0].Head, "bothy/clarify-the-readme-") {
		t.Fatalf("pulls = %+v", pulls)
	}
	if got, _ := repo.File(pulls[0].HeadSHA, "README.md"); got != readme {
		t.Errorf("README at the PR head = %q", got)
	}
	if got, _ := repo.File(pulls[0].HeadSHA, "src/main.go"); got != "package main\n\nfunc main() {}\n" {
		t.Errorf("untouched file at the PR head = %q", got)
	}
	if _, parents := repo.CommitMessage(pulls[0].HeadSHA); !slices.Equal(parents, []string{base}) {
		t.Errorf("commit parents = %v, want the checked-out %s", parents, base)
	}
	owner.WaitAudit(t, "github.pull_request", func(e core.AuditEntry) bool { return e.Target == "octocat/Hello-World" && e.Outcome == "ok" })
	owner.WaitAudit(t, "github.connect", func(e core.AuditEntry) bool { return e.Target == conn.ID })

	// The token went only to GitHub: not to the archive download, the logs,
	// the model or the session.
	for _, r := range gh.Requests() {
		if strings.HasPrefix(r.Path, "/_codeload/") && r.Auth != "none" {
			t.Errorf("archive download carried credentials (%s)", r.Auth)
		}
	}
	if strings.Contains(g.Logs(), gh.PAT) {
		t.Error("the PAT is in the gateway logs")
	}
	for _, body := range llm.Requests() {
		if strings.Contains(string(body), gh.PAT) {
			t.Fatal("the PAT reached the model")
		}
	}
	for _, m := range owner.Messages(t, sid) {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, gh.PAT) || p.ToolResult != nil && strings.Contains(p.ToolResult.Content, gh.PAT) {
				t.Fatal("the PAT is in the session")
			}
		}
	}

	// repo.checkout reached the knowledge module: the checkout is indexed for
	// the owner at the checked-out commit, notes are drafted, and the
	// repository row carries the indexed commit.
	var indexed core.IndexedRepo
	deadline := time.Now().Add(30 * time.Second)
	for indexed.ID == 0 {
		var list dto.List[core.IndexedRepo]
		if code := owner.JSON(t, http.MethodGet, "/api/knowledge/repos", nil, &list); code != http.StatusOK {
			t.Fatalf("knowledge repos = %d", code)
		}
		for _, r := range list.Items {
			if r.Repo == "octocat/hello-world" {
				indexed = r
			}
		}
		if indexed.ID == 0 && time.Now().After(deadline) {
			t.Fatalf("the checkout was not indexed: %+v", list.Items)
		}
		time.Sleep(50 * time.Millisecond)
	}
	indexed = kWaitRepo(t, owner, indexed.ID, func(r core.IndexedRepo) bool {
		return r.Status == core.IndexReady && r.Notes.Status == core.NotesReady
	})
	if indexed.Source != core.KnowledgeGitHub || indexed.SHA != base || indexed.Dir != "repos/octocat/Hello-World" || indexed.Files != 2 ||
		indexed.Principal != owner.Principal(t).ID {
		t.Errorf("indexed checkout = %+v", indexed)
	}
	if code := owner.JSON(t, http.MethodGet, "/api/github/repos", nil, &repos); code != http.StatusOK || len(repos) != 1 || repos[0].LastIndexedSHA != base {
		t.Errorf("repos after indexing = %d %+v, want last_indexed_sha %s", code, repos, base)
	}

	// Disconnecting removes the allowlist and the stored token.
	stored := func() bool {
		var secrets []core.SecretMeta
		if code := owner.JSON(t, http.MethodGet, "/api/vault?scope=global", nil, &secrets); code != http.StatusOK {
			t.Fatalf("vault list = %d", code)
		}
		return slices.ContainsFunc(secrets, func(s core.SecretMeta) bool { return "secret:"+s.Name == conn.SecretRef })
	}
	if !stored() {
		t.Fatal("the token is not in the vault")
	}
	if code := owner.JSON(t, http.MethodDelete, "/api/github/connections/"+conn.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("disconnect = %d", code)
	}
	if code := owner.JSON(t, http.MethodGet, "/api/github/repos", nil, &repos); code != http.StatusOK || len(repos) != 0 {
		t.Errorf("repos after disconnect = %d %+v", code, repos)
	}
	if stored() {
		t.Error("the connection's secret survived the disconnect")
	}
}
