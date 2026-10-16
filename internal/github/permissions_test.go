package github

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/github/githubtest"
)

// appOps is every GitHub call path of the tools and the release dispatcher,
// in an order where each step can use the previous ones. It returns the
// errors by step.
func appOps(f *fx) map[string]error {
	env := f.toolEnv()
	errs := map[string]error{}
	run := func(step, tool string, args map[string]any) string {
		b, _ := json.Marshal(args)
		a, err := f.tool(tool).Plan(f.ctx, env, b)
		if err != nil {
			errs[step] = err
			return ""
		}
		res, err := f.tool(tool).Exec(f.ctx, env, a)
		if err != nil {
			errs[step] = err
			return ""
		}
		return res.Content
	}
	const repo = "octocat/Hello-World"
	issue := f.repo.AddIssue("Found a bug", "Steps to reproduce.", "bug")
	run("repo_list", repoListName, map[string]any{})
	run("repo_checkout", repoCheckoutName, map[string]any{"repo": repo})
	writeWS(f.t, env, "repos/octocat/Hello-World/README.md", "# Hello World\n\nEdited by Bothy.\n")
	run("repo_changes", repoChangesName, map[string]any{"repo": repo})
	run("repo_open_pr", repoOpenPRName, map[string]any{"repo": repo, "title": "Edit the README"})
	pr := 0
	if ps := f.repo.Pulls(); len(ps) > 0 {
		pr = ps[len(ps)-1].Number
	}
	run("github_issues list", githubIssuesName, map[string]any{"repo": repo})
	run("github_issues one", githubIssuesName, map[string]any{"repo": repo, "number": issue})
	run("github_pr list", githubPRName, map[string]any{"repo": repo})
	if pr > 0 {
		run("github_pr one", githubPRName, map[string]any{"repo": repo, "number": pr, "files": true, "diff": true, "comments": true})
		run("github_comment pr", githubCommentName, map[string]any{"repo": repo, "number": pr, "body": "Ready for review."})
	}
	run("github_comment issue", githubCommentName, map[string]any{"repo": repo, "number": issue, "body": "Looking into it."})
	run("github_runs list", githubRunsName, map[string]any{"repo": repo})
	if _, err := f.svc.Workflows(f.ctx, repo); err != nil {
		errs["Workflows"] = err
	}
	head, err := f.svc.BranchHead(f.ctx, repo)
	if err != nil {
		errs["BranchHead"] = err
	}
	if head != "" {
		if _, err := f.svc.CommitSubject(f.ctx, repo, head); err != nil {
			errs["CommitSubject"] = err
		}
	}
	wr, err := f.svc.Dispatch(f.ctx, core.WorkflowDispatch{Repo: repo, Workflow: "deploy.yml", Ref: "main"})
	if err != nil {
		errs["Dispatch"] = err
	}
	if wr != nil {
		run("github_runs one", githubRunsName, map[string]any{"repo": repo, "run_id": wr.ID})
		if _, err := f.svc.RunStatus(f.ctx, repo, wr.ID); err != nil {
			errs["RunStatus"] = err
		}
		if _, err := f.svc.FailedStep(f.ctx, repo, wr.ID); err != nil {
			errs["FailedStep"] = err
		}
	}
	return errs
}

// appWith connects octocat/Hello-World through an app Bothy made, with an
// installation that carries perms (nil: what the manifest asked for, as
// GitHub's install page grants it).
func appWith(t *testing.T, perms map[string]string) (*fx, error) {
	f := newFx(t)
	f.repo.Workflows = []githubtest.Workflow{{ID: 1, Name: "Deploy", Path: ".github/workflows/deploy.yml"}}
	a := f.makeApp("")
	var id int64
	if perms == nil {
		id = f.installOnGitHub(a)
	} else {
		id = f.gh.Install(a.AppID, githubtest.Installation{Account: "octocat", Type: "User", Permissions: perms}).ID
	}
	conn, err := f.svc.InstallApp(f.ctx, a.ID, id, "usr_owner")
	if err != nil {
		return f, err
	}
	_, err = f.svc.SetRepos(f.ctx, conn.ID, []string{"octocat/Hello-World"}, "usr_owner")
	return f, err
}

// The manifest asks for exactly what the tools and releases use: with it
// every call works, and without any one of its permissions something fails.
func TestManifestPermissionsAreEnough(t *testing.T) {
	f, err := appWith(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if errs := appOps(f); len(errs) != 0 {
		t.Fatalf("with the manifest's permissions: %v", errs)
	}
	for _, r := range f.gh.Requests() {
		if strings.HasPrefix(r.Path, "/repos/") && r.Auth != "installation" {
			t.Errorf("%s %s sent as %s", r.Method, r.Path, r.Auth)
		}
	}

	for perm := range appPermissions {
		t.Run("without "+perm, func(t *testing.T) {
			less := maps.Clone(appPermissions)
			delete(less, perm)
			f, err := appWith(t, less)
			if err != nil {
				if !strings.Contains(err.Error(), "lacks the permission") {
					t.Fatalf("connecting failed for another reason: %v", err)
				}
				return
			}
			denied := false
			for _, err := range appOps(f) {
				denied = denied || strings.Contains(err.Error(), "lacks the permission")
			}
			if !denied {
				t.Errorf("nothing needed %s", perm)
			}
		})
	}
}

// Without the workflows permission, which the manifest never asks for, a
// pull request that edits CI fails with GitHub's own reason.
func TestAppCannotChangeWorkflows(t *testing.T) {
	f, err := appWith(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := f.toolEnv()
	f.checkout(env)
	writeWS(t, env, "repos/octocat/Hello-World/.github/workflows/ci.yml", "on: push\n")
	_, _, err = f.call(repoOpenPRName, env, map[string]any{"repo": "octocat/Hello-World", "title": "Add CI"})
	if err == nil || !strings.Contains(err.Error(), "without `workflows` permission") {
		t.Fatalf("pull request touching CI = %v", err)
	}
	if len(f.repo.Pulls()) != 0 {
		t.Error("a pull request was opened")
	}
}
