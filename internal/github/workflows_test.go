package github

import (
	"errors"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/github/githubtest"
)

func TestWorkflowsOfAnAllowedRepository(t *testing.T) {
	f := newFx(t)
	f.repo.Workflows = []githubtest.Workflow{
		{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml"},
		{ID: 2, Name: "Deploy ‮to prod\n", Path: ".github/workflows/deploy.yml"},
		{ID: 3, Name: "Dependabot Updates", Path: "dynamic/dependabot/dependabot-updates"},
	}
	if _, err := f.svc.Workflows(f.ctx, "octocat/Hello-World"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Workflows before the repository is allowed = %v", err)
	}
	f.connect("octocat/Hello-World")
	got, err := f.svc.Workflows(f.ctx, "OctoCat/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != ".github/workflows/ci.yml" || got[1].Name != "Deploy to prod" || got[1].State != "active" {
		t.Fatalf("workflows = %+v", got)
	}
	if _, err := f.svc.Workflows(f.ctx, "octocat/other"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Workflows of another repository = %v", err)
	}
}

func TestBranchHeadAndCommitSubject(t *testing.T) {
	f := newFx(t)
	c := f.connect("octocat/Hello-World")
	head := f.repo.Commit(map[string]string{"README.md": "v2"}, "Fix the login redirect‮\n\nLonger body that is never shown.")
	f.repo.Branch("main", head)
	sha, err := f.svc.BranchHead(f.ctx, "octocat/Hello-World")
	if err != nil || sha != head {
		t.Fatalf("BranchHead = %q %v, want %q", sha, err, head)
	}
	subject, err := f.svc.CommitSubject(f.ctx, "octocat/Hello-World", head)
	if err != nil || subject != "Fix the login redirect" {
		t.Fatalf("CommitSubject = %q %v", subject, err)
	}
	if _, err := f.svc.CommitSubject(f.ctx, "octocat/Hello-World", "main"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("CommitSubject of a branch name = %v", err)
	}
	// A deploy's commit is still read after the allowlist drops the
	// repository; its head is not.
	if _, err := f.svc.SetRepos(f.ctx, c.ID, nil, "usr_owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BranchHead(f.ctx, "octocat/Hello-World"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("BranchHead after the allowlist dropped it = %v", err)
	}
	if subject, err := f.svc.CommitSubject(f.ctx, "octocat/Hello-World", head); err != nil || subject == "" {
		t.Fatalf("CommitSubject after the allowlist dropped it = %q %v", subject, err)
	}
}
