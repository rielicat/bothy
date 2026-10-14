package release

import (
	"context"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// Checks implements core.CheckProvider: releases.github_token.
func (s *Service) Checks() []core.Check { return []core.Check{tokenCheck{s}} }

type tokenCheck struct{ s *Service }

func (tokenCheck) ID() string { return "releases.github_token" }

func (tokenCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

// Run reports configured environments whose deploys cannot work for lack of
// a GitHub token. It resolves the reference only; nothing is sent to GitHub.
func (c tokenCheck) Run(ctx context.Context) []core.Finding {
	r := c.s.d.Config.Current().Releases
	if len(r.Environments) == 0 {
		return nil
	}
	f := core.Finding{ID: "releases.github_token", Area: "releases", Severity: core.SevMedium,
		Title: "Deploys cannot start: no GitHub token", Fix: "/settings/vault"}
	switch {
	case strings.TrimSpace(r.GitHubToken) == "":
		f.Detail = "releases.environments is set but releases.github_token is empty."
		return []core.Finding{f}
	case c.s.d.Vault == nil:
		return nil
	}
	sec, err := c.s.d.Vault.Resolve(ctx, r.GitHubToken, "", "doctor:releases")
	if err != nil || strings.TrimSpace(sec.Reveal()) == "" {
		name := strings.TrimPrefix(r.GitHubToken, "secret:")
		f.Detail = "releases.github_token refers to the secret " + name + ", which is not in the vault. Add a GitHub token with Actions read and write on the release repositories."
		return []core.Finding{f}
	}
	return nil
}
