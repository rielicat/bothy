package core

import (
	"context"
	"time"
)

// GitHub connections and repositories (v0.2 F4). A connection's personal
// access token or GitHub App private key lives in the vault or in AWS
// Secrets Manager; the rows below hold only its "secret:<name>" or
// "aws-sm:<secret>" reference, and only the gateway resolves it.

const PrefixGitHubConn = "ghc"

type GitHubAuth string

const (
	GitHubPAT GitHubAuth = "pat" // fine-grained (or classic) personal access token
	GitHubApp GitHubAuth = "app" // GitHub App: installation tokens minted on demand
)

type GitHubConnection struct {
	ID             string     `json:"id"` // ghc_<26>
	Auth           GitHubAuth `json:"auth"`
	Account        string     `json:"account"`      // the token's user, or the installation's account
	AccountType    string     `json:"account_type"` // User | Organization
	AppID          string     `json:"app_id,omitempty"`
	AppSlug        string     `json:"app_slug,omitempty"`
	InstallationID int64      `json:"installation_id,omitempty"`
	SecretRef      string     `json:"secret_ref"`  // "secret:<name>" or "aws-sm:<secret>" of the PAT or the App private key
	OwnsSecret     bool       `json:"owns_secret"` // stored by Connect, so Disconnect deletes it
	Status         string     `json:"status"`      // ok | error
	Error          string     `json:"error,omitempty"`
	TokenExpires   time.Time  `json:"token_expires_at"` // PAT expiry as GitHub reports it; zero = none
	CheckedAt      time.Time  `json:"checked_at"`
	CreatedBy      UserID     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// GitHubRepo is a repository a connection can reach. Only Allowed ones are
// offered to the agent. Owner and name are unique case-insensitively across
// connections and keep GitHub's spelling. LastIndexedSHA is never stored:
// indexes are per principal, so GET /api/github/repos fills in the commit
// the caller's own index of the repository last read (IndexedRepo.IndexedSHA).
type GitHubRepo struct {
	Connection     string    `json:"connection_id"`
	GitHubID       int64     `json:"github_id"`
	Owner          string    `json:"owner"`
	Name           string    `json:"name"`
	DefaultBranch  string    `json:"default_branch"`
	Private        bool      `json:"private"`
	Allowed        bool      `json:"allowed"`
	LastIndexedSHA string    `json:"last_indexed_sha,omitempty"` // the caller's own index state
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (r GitHubRepo) FullName() string { return r.Owner + "/" + r.Name }

// GitHubStore persists connections and repositories (migration 0002). It is
// not part of Store: *store.DB and memstore provide it through GitHub().
type GitHubStore interface {
	PutConnection(ctx context.Context, c *GitHubConnection) error         // insert or replace by ID
	Connection(ctx context.Context, id string) (*GitHubConnection, error) // ErrNotFound
	Connections(ctx context.Context) ([]*GitHubConnection, error)         // oldest first
	DeleteConnection(ctx context.Context, id string) error                // and its repos; ErrNotFound
	// PutRepo inserts or updates by owner/name (case-insensitive). A repo
	// another connection allows is ErrConflict (one it no longer allows moves
	// over, keeping created_at); an unknown connection ErrInvalid.
	PutRepo(ctx context.Context, r *GitHubRepo) error
	Repo(ctx context.Context, owner, name string) (*GitHubRepo, error)   // case-insensitive; ErrNotFound
	Repos(ctx context.Context, connection string) ([]*GitHubRepo, error) // "" = every connection; by owner, name
}

// GitHubCredentials is a connect or test request. Exactly one of Token,
// PrivateKey or SecretRef carries the secret.
type GitHubCredentials struct {
	Auth           GitHubAuth
	Token          Secret // PAT; Connect stores it in the global vault scope
	PrivateKey     Secret // App private key (PEM); Connect stores it in the global vault scope
	SecretRef      string // an existing "secret:<name>" (e.g. BOTHY_SECRET_<NAME>) or "aws-sm:<secret>" instead
	AppID          string
	InstallationID int64 // App: 0 picks the app's only installation
}

type GitHubInstallation struct {
	ID                  int64  `json:"id"`
	Account             string `json:"account"`
	AccountType         string `json:"account_type"`
	RepositorySelection string `json:"repository_selection"` // all | selected
}

type GitHubRepoAccess struct {
	GitHubID      int64  `json:"github_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description,omitempty"` // repository-authored text
	Allowed       bool   `json:"allowed"`
	Connection    string `json:"connection_id,omitempty"` // the connection that holds it, if any
}

type GitHubRateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
	Resource  string    `json:"resource,omitempty"`
}

// GitHubProbe is what a credential test found. OK is false with Error set
// when GitHub refused the credentials or could not be reached.
type GitHubProbe struct {
	OK             bool                 `json:"ok"`
	Error          string               `json:"error,omitempty"`
	Code           FailureCode          `json:"code,omitempty"` // why it failed, next to Error
	Auth           GitHubAuth           `json:"auth"`
	Account        string               `json:"account"`
	AccountType    string               `json:"account_type"`
	AppSlug        string               `json:"app_slug,omitempty"`
	InstallationID int64                `json:"installation_id,omitempty"`
	Installations  []GitHubInstallation `json:"installations,omitempty"` // App: set when one must be chosen
	Repos          []GitHubRepoAccess   `json:"repos"`
	Truncated      bool                 `json:"truncated"` // more repositories than were listed
	TokenExpires   time.Time            `json:"token_expires_at"`
	RateLimit      GitHubRateLimit      `json:"rate_limit"`
}

// GitHub manages connections for the HTTP API and contributes the repo_* and
// github_* tools. Every method needs an admin caller except Repos.
type GitHub interface {
	ToolProvider
	// Test checks credentials without storing anything. Only malformed input
	// is an error (ErrInvalid); GitHub's answer goes into the probe.
	Test(ctx context.Context, c GitHubCredentials) (*GitHubProbe, error)
	// Connect tests, stores the secret in the vault and allowlists repos
	// (each must be reachable through the credentials).
	Connect(ctx context.Context, c GitHubCredentials, repos []string, by UserID) (*GitHubConnection, error)
	Connections(ctx context.Context) ([]*GitHubConnection, error)
	Connection(ctx context.Context, id string) (*GitHubConnection, []*GitHubRepo, error)
	// Probe re-tests a stored connection, records its status and lists the
	// repositories it reaches with their allowlist state.
	Probe(ctx context.Context, id string) (*GitHubProbe, error)
	// SetRepos replaces the connection's allowlist.
	SetRepos(ctx context.Context, id string, repos []string, by UserID) ([]*GitHubRepo, error)
	// Disconnect deletes the connection, its repositories and a secret it owns.
	Disconnect(ctx context.Context, id string, by UserID) error
	Repos(ctx context.Context) ([]*GitHubRepo, error) // allowed repositories of every connection
}

// EvRepoCheckout is published (Principal = the workspace's principal;
// Session and Run name the run that asked, when one did) when repo_checkout
// has written a repository into a workspace. The knowledge module indexes it.
const EvRepoCheckout EventType = "repo.checkout"

// RepoCheckoutData: Dir holds Repo's files at commit SHA. Consumers also
// accept an absolute path inside the principal's workspace or the path boxes
// see (/workspace/...).
type RepoCheckoutData struct {
	Repo      string      `json:"repo"` // owner/name
	Ref       string      `json:"ref"`
	SHA       string      `json:"sha"`
	Workspace WorkspaceID `json:"workspace_id"`
	Dir       string      `json:"dir"` // workspace-relative: repos/<owner>/<name>
	Files     int         `json:"files"`
}

func (RepoCheckoutData) eventData() {}
