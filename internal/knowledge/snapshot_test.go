package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// fakeGitHub serves commits as folders, like github.Service.Snapshot.
type fakeGitHub struct {
	t     *testing.T
	head  string
	trees map[string]map[string]string
	asked []string
	err   error
}

func (g *fakeGitHub) snapshot(_ context.Context, _ core.WorkspaceID, repo, sha string) (string, string, func(), error) {
	g.asked = append(g.asked, repo+"@"+sha)
	if g.err != nil {
		return "", "", nil, g.err
	}
	if sha == "" {
		sha = g.head
	}
	dir := g.t.TempDir()
	for p, body := range g.trees[sha] {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o640); err != nil {
			g.t.Fatal(err)
		}
	}
	return dir, sha, func() { _ = os.RemoveAll(dir) }, nil
}

func (h *harness) indexedPaths(id int64) []string {
	h.t.Helper()
	files, err := h.repo.Files(h.ctx, core.SystemViewer(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// An index of a GitHub repository holds what its commit holds: edits a box
// made to the workspace checkout are never cited at that commit, and
// reading it again reads the branch GitHub has now.
func TestGitHubIndexReadsTheCommitNotTheCheckout(t *testing.T) {
	h := newHarness(t)
	const old, head = "a227002a1647aaaa", "bbbb0000cccc1111"
	g := &fakeGitHub{t: t, head: head, trees: map[string]map[string]string{
		old:  {"src/auth/callback.ts": "export function callback() {}\n", "README.md": "# web\n"},
		head: {"src/auth/callback.ts": "export function callback() { return '/home' }\n", "README.md": "# web\n", "docs/new.md": "# New\n"},
	}}
	h.svc.d.Snapshot = g.snapshot
	h.write("repos/acme/web/src/auth/callback.ts", "function safeNext(next) { return next }\n")
	h.write("repos/acme/web/CONTRIBUTING.md", "never committed\n")
	h.svc.onCheckout(h.ctx, core.Event{Type: core.EvRepoCheckout, Principal: h.p.ID,
		Data: core.RepoCheckoutData{Repo: "acme/web", SHA: old, Dir: "repos/acme/web"}})
	repos, err := h.svc.Repos(h.ctx, h.viewer(), "")
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v %v", repos, err)
	}
	r := h.runJob(repos[0].ID)
	if r.Status != core.IndexReady || r.IndexedSHA != old || !slices.Equal(h.indexedPaths(r.ID), []string{"README.md", "src/auth/callback.ts"}) {
		t.Fatalf("index = %+v %v", r, h.indexedPaths(r.ID))
	}
	hits, err := h.svc.Search(h.ctx, h.viewer(), core.KnowledgeQuery{Text: "safeNext"})
	if err != nil || len(hits) != 0 {
		t.Errorf("the checkout's edit is searchable: %+v %v", hits, err)
	}

	q, err := h.svc.Reindex(h.ctx, h.viewer(), r.ID, h.user.ID)
	if err != nil || q.SHA != "" {
		t.Fatalf("reindex = %+v %v", q, err)
	}
	r = h.runJob(r.ID)
	if r.Status != core.IndexReady || r.IndexedSHA != head || !slices.Contains(h.indexedPaths(r.ID), "docs/new.md") {
		t.Fatalf("read again = %+v %v", r, h.indexedPaths(r.ID))
	}
	hits, err = h.svc.Search(h.ctx, h.viewer(), core.KnowledgeQuery{Text: "callback"})
	if err != nil || len(hits) == 0 || hits[0].SHA != head || !strings.HasPrefix(hits[0].Citation, "acme/web@"+head[:12]+":") {
		t.Errorf("citation after reading again = %+v %v", hits, err)
	}
	if !slices.Equal(g.asked, []string{"acme/web@" + old, "acme/web@"}) {
		t.Errorf("snapshots asked = %v", g.asked)
	}

	g.err = errors.New("GitHub rejected the credentials (wrong, expired or revoked token): Bad credentials")
	if _, err := h.svc.Reindex(h.ctx, h.viewer(), r.ID, h.user.ID); err != nil {
		t.Fatal(err)
	}
	if r = h.runJob(r.ID); r.Status != core.IndexFailed || r.IndexedSHA != head || !strings.Contains(r.Error, "Bad credentials") {
		t.Errorf("failed read = %+v", r)
	}
}

// A plain workspace folder has no commit to read: it is indexed as it is.
func TestWorkspaceIndexIgnoresSnapshots(t *testing.T) {
	h := newHarness(t)
	g := &fakeGitHub{t: t}
	h.svc.d.Snapshot = g.snapshot
	h.write("proj/a.go", "package a\n")
	if r := h.index("proj", "", ""); r.Status != core.IndexReady || len(g.asked) != 0 {
		t.Fatalf("index = %+v, snapshots %v", r, g.asked)
	}
}

// The notes draft reads the commit the index read as well: a README a box
// edited in the checkout never reaches the notes.
func TestGitHubNotesDraftReadsTheCommit(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Knowledge.AutoNotes = true })
	const sha = "a227002a1647aaaa"
	g := &fakeGitHub{t: t, head: sha, trees: map[string]map[string]string{
		sha: {"README.md": "# web\n\nThe committed readme.\n", "go.mod": "module acme/web\n"},
	}}
	h.svc.d.Snapshot = g.snapshot
	h.write("repos/acme/web/README.md", "# web\n\nAn edit nobody committed.\n")
	h.write("repos/acme/web/go.mod", "module acme/web\n")
	h.llm.Steps = []fakes.LLMStep{draftStep("## Summary\nThe web app.\n")}
	h.svc.onCheckout(h.ctx, core.Event{Type: core.EvRepoCheckout, Principal: h.p.ID,
		Data: core.RepoCheckoutData{Repo: "acme/web", SHA: sha, Dir: "repos/acme/web"}})
	repos, err := h.svc.Repos(h.ctx, h.viewer(), "")
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v %v", repos, err)
	}
	h.runJob(repos[0].ID)
	h.svc.bgWG.Wait()
	if len(h.llm.Requests) != 1 {
		t.Fatalf("model calls = %d", len(h.llm.Requests))
	}
	user := h.llm.Requests[0].Messages[0].PlainText()
	if !strings.Contains(user, "The committed readme.") || strings.Contains(user, "nobody committed") || !strings.Contains(user, "(commit "+sha[:12]+")") {
		t.Fatalf("draft read:\n%s", user)
	}
	if !slices.Equal(g.asked, []string{"acme/web@" + sha, "acme/web@" + sha}) {
		t.Errorf("snapshots asked = %v", g.asked)
	}
}
