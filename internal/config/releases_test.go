package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const validReleases = `releases:
  poll_interval: 5s
  environments:
    - name: staging
      repo: acme/web
      workflow: deploy.yml
      ref_pattern: main
      inputs: {target: staging}
      approvers: [admin]
      required: 2
    - name: prod
      repo: acme/web
      workflow: "123456"
      ref_pattern: "v*"
      approvers: [usr_01jaaaaaaaaaaaaaaaaaaaaaaa, usr_01jbbbbbbbbbbbbbbbbbbbbbbb, owner]
`

func TestReleasesLoad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), validReleases)
	m := load(t, dir)
	if eff := m.Effective(); len(eff.Errors) != 0 {
		t.Fatalf("errors: %+v", eff.Errors)
	}
	r := m.Current().Releases
	if r.PollInterval.D() != 5*time.Second || r.FollowFor.D() != 6*time.Hour || r.GitHubToken != "secret:github_token" ||
		r.GitHubAPI != "https://api.github.com" {
		t.Fatalf("releases = %+v", r)
	}
	st, ok := r.Env("staging")
	if !ok || st.Inputs["target"] != "staging" || st.Needed() != 2 {
		t.Fatalf("staging = %+v", st)
	}
	users, roles := st.ApproverSet()
	if len(users) != 0 || !slices.Equal(roles, []core.Role{core.RoleAdmin}) {
		t.Fatalf("staging approvers = %v %v", users, roles)
	}
	prod, _ := r.Env("prod")
	users, roles = prod.ApproverSet()
	if len(users) != 2 || !slices.Equal(roles, []core.Role{core.RoleOwner}) || prod.Needed() != 1 {
		t.Fatalf("prod approvers = %v %v needed %d", users, roles, prod.Needed())
	}
	if _, ok := r.Env("dev"); ok {
		t.Fatal("unknown environment found")
	}
}

func TestReleasesValidation(t *testing.T) {
	for _, tc := range []struct {
		name, env, path, msg string
	}{
		{"bad name", `{name: Staging!, repo: a/b, workflow: d.yml, ref_pattern: main, approvers: [admin]}`, "releases.environments.Staging!", "invalid name"},
		{"no repo", `{name: s, workflow: d.yml, ref_pattern: main, approvers: [admin]}`, "releases.environments.s", "repo is required"},
		{"no workflow", `{name: s, repo: a/b, ref_pattern: main, approvers: [admin]}`, "releases.environments.s", "workflow is required"},
		{"no ref pattern", `{name: s, repo: a/b, workflow: d.yml, approvers: [admin]}`, "releases.environments.s", "ref_pattern is required"},
		{"no approvers", `{name: s, repo: a/b, workflow: d.yml, ref_pattern: main}`, "releases.environments.s", "approvers is required"},
		{"bad repo", `{name: s, repo: "a/b/c", workflow: d.yml, ref_pattern: main, approvers: [admin]}`, "releases.environments.s.repo", "owner/name"},
		{"dot repo", `{name: s, repo: "a/..", workflow: d.yml, ref_pattern: main, approvers: [admin]}`, "releases.environments.s.repo", "owner/name"},
		{"bad workflow", `{name: s, repo: a/b, workflow: "../x.yml", ref_pattern: main, approvers: [admin]}`, "releases.environments.s.workflow", "workflow file name"},
		{"bad pattern", `{name: s, repo: a/b, workflow: d.yml, ref_pattern: "main[", approvers: [admin]}`, "releases.environments.s.ref_pattern", "invalid ref pattern"},
		{"bad input", `{name: s, repo: a/b, workflow: d.yml, ref_pattern: main, approvers: [admin], inputs: {"bad key": x}}`, "releases.environments.s.inputs.bad key", "invalid input name"},
		{"bad approver", `{name: s, repo: a/b, workflow: d.yml, ref_pattern: main, approvers: [admins]}`, "releases.environments.s.approvers", "did you mean"},
		{"duplicate approver", `{name: s, repo: a/b, workflow: d.yml, ref_pattern: main, approvers: [admin, admin]}`, "releases.environments.s.approvers", "duplicate approver"},
		{"required 3", `{name: s, repo: a/b, workflow: d.yml, ref_pattern: main, approvers: [admin], required: 3}`, "releases.environments.s.required", "between 0 and 2"},
		{"too few users", `{name: s, repo: a/b, workflow: d.yml, ref_pattern: main, approvers: [usr_01jaaaaaaaaaaaaaaaaaaaaaaa], required: 2}`, "releases.environments.s.required", "needs 2 approvers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "bothy.yaml"), "releases:\n  environments:\n    - "+tc.env+"\n")
			eff := load(t, dir).Effective()
			var hit bool
			for _, fe := range eff.Errors {
				hit = hit || strings.HasPrefix(fe.Path, tc.path) && strings.Contains(fe.Message, tc.msg)
			}
			if !hit {
				t.Fatalf("errors = %+v, want %q at %s", eff.Errors, tc.msg, tc.path)
			}
		})
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "releases:\n  poll_interval: 0s\n  follow_for: -1s\n")
	eff := load(t, dir).Effective()
	if len(errorsMatching(eff, "positive duration")) != 2 {
		t.Fatalf("errors = %+v", eff.Errors)
	}
}

func TestReleaseRefs(t *testing.T) {
	for _, ref := range []string{"main", "release/1.2", "v1.2.3", "feature/x_y-z"} {
		if !ValidReleaseRef(ref) {
			t.Errorf("ValidReleaseRef(%q) = false", ref)
		}
	}
	for _, ref := range []string{"", "-main", "a..b", "a//b", "a/", "a.lock", "a/.b", "a.", "a@{1}", "a b", "refs/heads/main\n", strings.Repeat("a", 201)} {
		if ValidReleaseRef(ref) {
			t.Errorf("ValidReleaseRef(%q) = true", ref)
		}
	}
	e := ReleaseEnv{RefPattern: "release/*"}
	if !e.AllowsRef("release/1.2") || e.AllowsRef("release/1.2/x") || e.AllowsRef("main") || e.AllowsRef("release/..") {
		t.Error("release/* matching")
	}
	if _, ok := e.DefaultRef(); ok {
		t.Error("a glob has no default ref")
	}
	if ref, ok := (ReleaseEnv{RefPattern: "main"}).DefaultRef(); !ok || ref != "main" {
		t.Errorf("DefaultRef = %q %v", ref, ok)
	}
	if (ReleaseEnv{}).AllowsRef("main") {
		t.Error("an empty pattern admitted a ref")
	}
}
