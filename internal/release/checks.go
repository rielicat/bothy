package release

import (
	"context"
	"strings"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// Checks implements core.CheckProvider: releases.github_token.
func (s *Service) Checks() []core.Check { return []core.Check{tokenCheck{s}} }

type tokenCheck struct{ s *Service }

func (tokenCheck) ID() string { return "releases.github_token" }

func (tokenCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

// Run reports configured environments whose deploys cannot work for lack of
// a GitHub token: their repository is on no GitHub connection's allowlist
// and releases.github_token does not resolve. It resolves the reference
// only; nothing is sent to GitHub.
func (c tokenCheck) Run(ctx context.Context) []core.Finding {
	r := c.s.d.Config.Current().Releases
	var uncovered []string
	for _, e := range r.Environments {
		if c.s.d.Connections == nil || !c.s.d.Connections.Serves(ctx, e.Repo) {
			uncovered = append(uncovered, e.Name)
		}
	}
	if len(uncovered) == 0 {
		return nil
	}
	envs, whose := uncovered[0], "its repository"
	if n := len(uncovered); n > 1 {
		envs, whose = strings.Join(uncovered[:n-1], ", ")+" and "+uncovered[n-1], "their repositories"
	}
	f := core.Finding{ID: "releases.github_token", Area: "releases", Severity: core.SevMedium,
		Title: "Deploys to " + envs + " can't start because Bothy has no GitHub access to " + whose, Fix: "/settings/vault"}
	which := "The repositories of " + strings.Join(uncovered, ", ") + " are on no GitHub connection's allowlist"
	switch {
	case strings.TrimSpace(r.GitHubToken) == "":
		f.Detail = which + " and releases.github_token is empty. Connect them in Settings → Connections → GitHub, or set a token."
		return []core.Finding{f}
	case c.s.d.Vault == nil:
		return nil
	}
	sec, err := c.s.d.Vault.Resolve(ctx, r.GitHubToken, "", "doctor:releases")
	if err != nil && strings.HasPrefix(r.GitHubToken, config.AWSSecretPrefix) {
		f.Detail = which + ", and releases.github_token cannot be read from AWS Secrets Manager: " + err.Error()
		return []core.Finding{f}
	}
	if err != nil || strings.TrimSpace(sec.Reveal()) == "" {
		name := strings.TrimPrefix(r.GitHubToken, "secret:")
		f.Detail = which + ", and releases.github_token refers to the secret " + name + ", which is not in the vault. Connect the repositories in Settings → Connections → GitHub, or add a GitHub token with Actions read and write on them."
		return []core.Finding{f}
	}
	return nil
}
