package core

import (
	"context"
	"errors"
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

// GitHubStore persists connections, repositories (migration 0002) and
// Bothy-made apps (0012). It is not part of Store: *store.DB and memstore
// provide it through GitHub().
type GitHubStore interface {
	PutConnection(ctx context.Context, c *GitHubConnection) error         // insert or replace by ID
	Connection(ctx context.Context, id string) (*GitHubConnection, error) // ErrNotFound
	Connections(ctx context.Context) ([]*GitHubConnection, error)         // oldest first
	// DeleteConnection deletes its repos and clears the connection of the app
	// that held it; ErrNotFound.
	DeleteConnection(ctx context.Context, id string) error
	// PutRepo inserts or updates by owner/name (case-insensitive). A repo
	// another connection allows is ErrConflict (one it no longer allows moves
	// over, keeping created_at); an unknown connection ErrInvalid.
	PutRepo(ctx context.Context, r *GitHubRepo) error
	Repo(ctx context.Context, owner, name string) (*GitHubRepo, error)   // case-insensitive; ErrNotFound
	Repos(ctx context.Context, connection string) ([]*GitHubRepo, error) // "" = every connection; by owner, name
	// PutApp inserts or replaces by ID, keeping created_at and created_by.
	// An app_id or connection another app holds is ErrConflict; an unknown
	// connection ErrInvalid. The derived fields are never stored.
	PutApp(ctx context.Context, a *GitHubOwnApp) error
	App(ctx context.Context, id string) (*GitHubOwnApp, error)                 // ErrNotFound
	Apps(ctx context.Context) ([]*GitHubOwnApp, error)                         // oldest first
	AppByConnection(ctx context.Context, connID string) (*GitHubOwnApp, error) // ErrNotFound
	DeleteApp(ctx context.Context, id string) error                            // ErrNotFound
}

const PrefixGitHubApp = "gha"

// GitHubOwnApp is a GitHub App Bothy created from a manifest (migration 0012).
// Its private key and client secret live in the global vault scope; the row
// holds their references. Pending until installed, connected while a
// connection uses it, retired once that connection is gone or the admin
// started over (secrets deleted; kept so Settings can say where to delete it
// on GitHub, which Bothy never does itself).
type GitHubOwnApp struct {
	ID              string    `json:"id"` // gha_<26>
	AppID           int64     `json:"app_id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Owner           string    `json:"owner"`
	OwnerID         int64     `json:"owner_id"`   // GitHub's id of the owner account, which a rename keeps and a transfer changes (GET /app); installations must be on it
	OwnerType       string    `json:"owner_type"` // Organization | User
	URL             string    `json:"html_url"`   // <web> + GitHub's path for it: /apps/<slug>, or /github-apps/<slug> on Enterprise Server
	ClientID        string    `json:"client_id"`
	KeyRef          string    `json:"key_ref"`           // "secret:github_<id>_key"; "" once retired
	ClientSecretRef string    `json:"client_secret_ref"` // "secret:github_<id>_client_secret"; kept for a possible GitHub sign-in, unused today
	Connection      string    `json:"connection_id,omitempty"`
	Replaces        string    `json:"replaces,omitempty"` // connection swapped out once the app is installed
	RetiredAt       time.Time `json:"retired_at"`
	CreatedBy       UserID    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// Filled in by the GitHub service from the state and URL; never stored.
	State       string `json:"state"`        // pending | connected | retired
	InstallURL  string `json:"install_url"`  // <URL>/installations/new
	SettingsURL string `json:"settings_url"` // <web>/organizations/<owner>/settings/apps/<slug> or <web>/settings/apps/<slug>
}

// GitHubAppTarget is where a manifest flow creates the app.
type GitHubAppTarget struct {
	Org      string // organization login; "" = the admin's personal account
	Name     string // "" = "Bothy (<Org, or Fallback>)", cut to GitHub's 34 characters; "… 2" and on while an app Bothy recorded has it
	Fallback string // the admin's username, naming a personal account's app
}

// GitHubAppHandoff is what the handoff page posts to GitHub.
type GitHubAppHandoff struct {
	Action    string // https://<web>/organizations/<org>/settings/apps/new or …/settings/apps/new, without query
	WebOrigin string // scheme://host[:port] of Action, for the page's form-action
	Manifest  string // JSON for the form field "manifest"
	Name      string // the proposed app name
	Org       string // "" for a personal account
}

// ErrAppExchange marks a CreateApp failure on GitHub's side: the code was
// refused or had expired, GitHub failed or was out of reach, or its answer
// held no app Bothy can use. CreateApp's other failures once the code is
// spent are Bothy's own (vault, store).
var ErrAppExchange = errors.New("redeeming the new app's code")

// GitHubAppMaker is the extra *github.Service implements: Bothy's own
// GitHub App created from a manifest and connected through its installation
// (SPEC §14.3). Every method needs an admin caller.
type GitHubAppMaker interface {
	// AppManifest builds the manifest for an app reached back at origin
	// (Bothy's own address); ErrInvalid for a bad target or origin.
	AppManifest(ctx context.Context, t GitHubAppTarget, origin string) (*GitHubAppHandoff, error)
	// CreateApp redeems the code GitHub's redirect carried and keeps the new
	// app's keys. ErrSealed before redeeming while the vault is locked;
	// ErrAppExchange when GitHub's side failed.
	CreateApp(ctx context.Context, code string, t GitHubAppTarget, replaces string, by UserID) (*GitHubOwnApp, error)
	// InstallApp verifies installationID belongs to the app and connects it.
	InstallApp(ctx context.Context, id string, installationID int64, by UserID) (*GitHubConnection, error)
	// CheckApp looks for the app's installation on its owner and connects
	// it; nil, nil when it is not installed there yet.
	CheckApp(ctx context.Context, id string, by UserID) (*GitHubConnection, error)
	Apps(ctx context.Context) ([]*GitHubOwnApp, error)                          // oldest first
	ConnectionApp(ctx context.Context, connID string) (*GitHubOwnApp, error)    // ErrNotFound
	RetireApp(ctx context.Context, id string, by UserID) (*GitHubOwnApp, error) // ErrConflict while connected
	DeleteApp(ctx context.Context, id string, by UserID) error                  // ErrConflict unless retired
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
	// Workflows lists the GitHub Actions workflows of an allowed repository
	// (ErrNotFound for any other), for picking a deploy workflow.
	Workflows(ctx context.Context, repo string) ([]GitHubWorkflow, error)
	// BranchHead is the commit the default branch of an allowed repository
	// points at now (ErrNotFound for any other).
	BranchHead(ctx context.Context, repo string) (string, error)
	// Prefetch checks allowed repositories out into env's workspace on their
	// default branch, skipping any already checked out there, so Knowledge
	// reads them (EvRepoCheckout) without waiting for a chat to need them.
	// Failures are logged and audited, not returned.
	Prefetch(ctx context.Context, env *ToolEnv, repos []string)
}

// GitHubWorkflow is one workflow file of a repository. Name is written by
// whoever can push to it.
type GitHubWorkflow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`  // .github/workflows/deploy.yml
	State string `json:"state"` // active | disabled_manually | disabled_inactivity | …
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
