package config

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

const (
	maxReleaseInputs   = 25 // GitHub's limit for workflow_dispatch inputs
	maxReleaseInputLen = 1024
)

var (
	releaseEnvRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	releaseRepoRe     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	releaseWorkflowRe = regexp.MustCompile(`^(?:[A-Za-z0-9._-]{1,200}\.ya?ml|[0-9]{1,19})$`)
	releaseInputRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,99}$`)
	releaseRefRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	releasePatternRe  = regexp.MustCompile(`^[A-Za-z0-9._/*?\[\]-]{1,200}$`)
	releaseRoles      = []string{string(core.RoleOwner), string(core.RoleAdmin), string(core.RoleMember)}
)

// Env returns the environment with this name.
func (r *ReleasesCfg) Env(name string) (ReleaseEnv, bool) {
	for _, e := range r.Environments {
		if e.Name == name {
			return e, true
		}
	}
	return ReleaseEnv{}, false
}

// ApproverSet splits approvers into user ids and roles; entries that are
// neither are dropped (validation reports them).
func (e ReleaseEnv) ApproverSet() (users []core.UserID, roles []core.Role) {
	for _, a := range e.Approvers {
		switch {
		case core.ValidID(core.PrefixUser, a):
			if !slices.Contains(users, core.UserID(a)) {
				users = append(users, core.UserID(a))
			}
		case slices.Contains(releaseRoles, a):
			if !slices.Contains(roles, core.Role(a)) {
				roles = append(roles, core.Role(a))
			}
		}
	}
	return users, roles
}

// Needed is how many distinct approvers other than the requester a deploy to
// this environment needs.
func (e ReleaseEnv) Needed() int { return max(e.Required, 1) }

// AllowsRef reports whether ref is a valid branch or tag name that
// ref_pattern admits.
func (e ReleaseEnv) AllowsRef(ref string) bool {
	if !ValidReleaseRef(ref) || e.RefPattern == "" {
		return false
	}
	ok, err := path.Match(e.RefPattern, ref)
	return err == nil && ok
}

// DefaultRef is the ref a deploy uses when none is given: ref_pattern itself
// when it names exactly one ref.
func (e ReleaseEnv) DefaultRef() (string, bool) {
	if strings.ContainsAny(e.RefPattern, `*?[\`) || !ValidReleaseRef(e.RefPattern) {
		return "", false
	}
	return e.RefPattern, true
}

// ValidReleaseRef accepts branch and tag names that git and the GitHub API
// agree on: no "..", "//", "/.", "@", trailing "/", "." or ".lock", no
// leading "-".
func ValidReleaseRef(ref string) bool {
	switch {
	case !releaseRefRe.MatchString(ref),
		strings.Contains(ref, ".."), strings.Contains(ref, "//"), strings.Contains(ref, "/."),
		strings.HasSuffix(ref, "/"), strings.HasSuffix(ref, "."), strings.HasSuffix(ref, ".lock"):
		return false
	}
	return true
}

// PlainHTTPAllowed reports whether github.api_base or releases.github_api may
// use http rather than https: only for a loopback host, so the GitHub token
// never crosses a network unencrypted.
func PlainHTTPAllowed(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateReleases(c *Config, v *vctx) {
	r := c.Releases
	v.url("releases.github_api", r.GitHubAPI, true, "http", "https")
	if u, err := url.Parse(r.GitHubAPI); err == nil && u.Scheme == "http" && u.Host != "" && !PlainHTTPAllowed(u.Hostname()) {
		v.add("releases.github_api", "use https: plain http would send the GitHub token unencrypted (http is accepted only for a loopback address)")
	}
	v.positive("releases.poll_interval", r.PollInterval)
	v.positive("releases.follow_for", r.FollowFor)
	for _, e := range r.Environments {
		base := "releases.environments." + e.Name
		if !releaseEnvRe.MatchString(e.Name) {
			v.item(base, "invalid name %q: lower-case letters, digits, '_' and '-', at most 32", e.Name)
			continue
		}
		switch {
		case e.Repo == "":
			v.item(base, "repo is required")
			continue
		case e.Workflow == "":
			v.item(base, "workflow is required")
			continue
		case e.RefPattern == "":
			v.item(base, "ref_pattern is required (for example main or release/*)")
			continue
		case len(e.Approvers) == 0:
			v.item(base, "approvers is required: user ids or roles who may approve a deploy")
			continue
		}
		if parts := strings.Split(e.Repo, "/"); !releaseRepoRe.MatchString(e.Repo) || parts[1] == "." || parts[1] == ".." {
			v.add(base+".repo", "want owner/name, got %q", e.Repo)
		}
		if !releaseWorkflowRe.MatchString(e.Workflow) {
			v.add(base+".workflow", "want a workflow file name such as deploy.yml or a numeric workflow id, got %q", e.Workflow)
		}
		if !releasePatternRe.MatchString(e.RefPattern) || !validGlob(e.RefPattern) {
			v.add(base+".ref_pattern", "invalid ref pattern %q", e.RefPattern)
		}
		if len(e.Inputs) > maxReleaseInputs {
			v.add(base+".inputs", "at most %d inputs, got %d", maxReleaseInputs, len(e.Inputs))
		}
		for _, k := range keysOf(e.Inputs) {
			switch {
			case !releaseInputRe.MatchString(k):
				v.add(base+".inputs."+k, "invalid input name")
			case len(e.Inputs[k]) > maxReleaseInputLen:
				v.add(base+".inputs."+k, "at most %d bytes", maxReleaseInputLen)
			}
		}
		var ids []string
		for i, a := range e.Approvers {
			switch {
			case slices.Contains(e.Approvers[:i], a):
				v.addAt(base+".approvers", i, "duplicate approver %q", a)
			case core.ValidID(core.PrefixUser, a):
				ids = append(ids, a)
			case !slices.Contains(releaseRoles, a):
				v.addAt(base+".approvers", i, "want a user id (usr_…) or a role (%s), got %q%s",
					strings.Join(releaseRoles, ", "), a, didYouMean(a, releaseRoles))
			}
		}
		v.intRange(base+".required", e.Required, 0, 2)
		if _, roles := e.ApproverSet(); len(roles) == 0 && len(ids) < e.Needed() {
			v.add(v.blame(base+".required", base+".approvers"), "needs %d approvers but only %d users are listed", e.Needed(), len(ids))
		}
	}
}
