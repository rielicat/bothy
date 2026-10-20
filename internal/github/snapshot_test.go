package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fx) codeloads() int {
	n := 0
	for _, r := range f.gh.Requests() {
		if strings.HasPrefix(r.Path, "/_codeload/") {
			n++
		}
	}
	return n
}

// Knowledge indexes what a commit holds, never a checkout a box edited.
func TestSnapshotHoldsTheCommitNotTheEditedCheckout(t *testing.T) {
	f := newFx(t)
	f.connect("octocat/Hello-World")
	env := f.toolEnv()
	f.checkout(env)
	head := f.repo.Head("main")
	writeWS(t, env, "repos/octocat/Hello-World/src/main.go", "package main\n\nfunc safeNext() {}\n")
	writeWS(t, env, "repos/octocat/Hello-World/CONTRIBUTING.md", "never committed\n")

	before := f.codeloads()
	dir, sha, done, err := f.svc.Snapshot(f.ctx, env.Principal.Workspace, "octocat/hello-world", head)
	if err != nil {
		t.Fatal(err)
	}
	files := readTree(t, dir)
	if sha != head || files["src/main.go"] != helloFiles["src/main.go"] || files["CONTRIBUTING.md"] != "" || len(files) != 5 {
		t.Errorf("snapshot of %s = %s %v", head, sha, files)
	}
	if rel, _ := filepath.Rel(env.Workspace, dir); !strings.HasPrefix(rel, "..") {
		t.Errorf("snapshot %s is inside the workspace", dir)
	}
	// The checkout's own pristine archive held that commit.
	if n := f.codeloads(); n != before {
		t.Errorf("snapshot of the checked-out commit downloaded the archive again (%d downloads)", n-before)
	}
	done()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("done left %s: %v", dir, err)
	}

	// "" reads the branch head on GitHub now, past the checkout.
	next := f.repo.Commit(map[string]string{"README.md": "# v2\n"}, "v2")
	dir, sha, done, err = f.svc.Snapshot(f.ctx, env.Principal.Workspace, "octocat/Hello-World", "")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if files := readTree(t, dir); sha != next || files["README.md"] != "# v2\n" || files["src/main.go"] != "" {
		t.Errorf("head snapshot = %s %v", sha, files)
	}
	if f.codeloads() == before {
		t.Error("a commit the checkout doesn't hold came from nowhere")
	}
}

func TestSnapshotRefusals(t *testing.T) {
	f := newFx(t, func(c *configT) { c.GitHub.Checkout.MaxFiles = 2 })
	f.connect("octocat/Hello-World")
	ws := core.WorkspaceID(core.NewID(core.PrefixWorkspace))
	if _, _, _, err := f.svc.Snapshot(f.ctx, ws, "octocat/other", ""); err == nil {
		t.Error("snapshot of a repository no connection allows")
	}
	if _, _, _, err := f.svc.Snapshot(f.ctx, ws, "octocat/Hello-World", "main; rm"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad sha: %v", err)
	}
	if _, _, _, err := f.svc.Snapshot(f.ctx, ws, "octocat/Hello-World", ""); !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "max_files") {
		t.Errorf("over the file cap: %v", err)
	}
	if left, _ := os.ReadDir(f.svc.snapshotDir()); len(left) != 0 {
		t.Errorf("failed snapshots left %v behind", left)
	}
}
