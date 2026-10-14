package dto

import "github.com/rielicat/bothy/internal/core"

// ---- github (v0.2 F4) ----

// GitHubConnectReq is the body of POST /api/github/connections (connect) and
// POST /api/github/connections/test (probe only, nothing stored). Give
// exactly one of Token (auth "pat"), PrivateKey (auth "app", the .pem GitHub
// generated) or SecretRef (an existing "secret:<name>", or "aws-sm:<secret>"
// in AWS Secrets Manager). Token and PrivateKey are write-only: Connect
// stores them in the global vault scope and no response ever carries them.
type GitHubConnectReq struct {
	Auth           core.GitHubAuth `json:"auth"`
	Token          string          `json:"token,omitempty"`
	PrivateKey     string          `json:"private_key,omitempty"`
	SecretRef      string          `json:"secret_ref,omitempty"`
	AppID          string          `json:"app_id,omitempty"`
	InstallationID int64           `json:"installation_id,omitempty"` // App: 0 = its only installation
	Repos          []string        `json:"repos,omitempty"`           // connect: owner/name to allowlist
}

// GitHubReposReq replaces a connection's allowlist (PUT
// /api/github/connections/{id}/repos); an empty list allows none.
type GitHubReposReq struct {
	Repos []string `json:"repos"`
}

// GitHubConnectionDetail is GET /api/github/connections/{id}: the stored
// connection and every repository it holds, allowed or not.
type GitHubConnectionDetail struct {
	Connection *core.GitHubConnection `json:"connection"`
	Repos      []*core.GitHubRepo     `json:"repos"`
}
