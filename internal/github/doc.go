// Package github implements core.GitHub: GitHub connections (a personal
// access token or a GitHub App) with a repository allowlist, and the
// gateway-side repo_* and github_* tools. Credentials come from the vault
// and travel only through the egress system client; no token ever enters a
// box, a log or model context.
package github
