package github

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Config  config.View
	Store   core.GitHubStore
	Vault   core.Vault  // the PAT or App key; Redactor() masks minted installation tokens
	Egress  core.Egress // SystemClient only: the token never reaches a box
	Bus     core.Bus    // repo.checkout
	Audit   core.Auditor
	DataDir string // <data>/github/checkouts: manifests and pristine archives, outside every workspace
	Clock   core.Clock
	Log     *slog.Logger
}

var (
	_ core.GitHub        = (*Service)(nil)
	_ core.CheckProvider = (*Service)(nil)
)

// Service implements core.GitHub and core.CheckProvider.
type Service struct {
	cfg     config.View
	store   core.GitHubStore
	vault   core.Vault
	egress  core.Egress
	bus     core.Bus
	audit   core.Auditor
	dataDir string
	clock   core.Clock
	log     *slog.Logger

	enabled atomic.Bool // some repository is allowlisted; gates the tools

	mu      sync.Mutex
	sources map[string]*connSource // by connection id

	locks keyedMutex // one checkout or PR per workspace repository at a time
	tools []core.Tool

	// findFor and findEvery bound the search for the run a dispatch started
	// when GitHub does not return its id.
	findFor, findEvery time.Duration
}

type connSource struct {
	base string // api_base the source was built for
	src  tokenSource
}

func New(d Deps) (*Service, error) {
	if d.Config == nil || d.Store == nil || d.Vault == nil || d.Egress == nil {
		return nil, fmt.Errorf("github: %w: Config, Store, Vault and Egress are required", core.ErrInvalid)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		cfg: d.Config, store: d.Store, vault: d.Vault, egress: d.Egress, bus: d.Bus, audit: d.Audit,
		dataDir: d.DataDir, clock: d.Clock, log: d.Log,
		sources: map[string]*connSource{},
		findFor: 30 * time.Second, findEvery: 2 * time.Second,
	}
	s.tools = []core.Tool{
		&repoList{s}, &repoCheckout{s}, &repoChanges{s}, &repoOpenPR{s},
		&githubIssues{s}, &githubPR{s}, &githubComment{s}, &githubRuns{s},
	}
	if s.dataDir != "" {
		// Snapshots live only while Knowledge reads them; a restart strands any.
		_ = os.RemoveAll(s.snapshotDir())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.refresh(ctx); err != nil {
		return nil, fmt.Errorf("github: reading repositories: %w", err)
	}
	return s, nil
}

func (s *Service) Tools() []core.Tool { return s.tools }

func (s *Service) now() time.Time { return s.clock.Now() }

// refresh recomputes whether any repository is allowlisted.
func (s *Service) refresh(ctx context.Context) error {
	repos, err := s.store.Repos(ctx, "")
	if err != nil {
		return err
	}
	on := false
	for _, r := range repos {
		on = on || r.Allowed
	}
	s.enabled.Store(on)
	return nil
}

func (s *Service) endpoint() (endpoint, error) { return newEndpoint(s.cfg.Current().GitHub.APIBase) }

// httpClient wraps the egress system client for the endpoint's hosts; it
// follows no redirects on its own, so a token never leaves the API host.
func (s *Service) httpClient(ep endpoint) *http.Client {
	sys := s.egress.SystemClient(core.SystemDest{Name: "github", Hosts: ep.hosts, AllowPrivate: ep.private})
	return &http.Client{Transport: sys.Transport, CheckRedirect: noRedirects}
}

func (s *Service) vaultSecret(ref, connID string) secretFunc {
	return func(ctx context.Context) (core.Secret, error) {
		return s.vault.Resolve(ctx, ref, "", "github:"+connID)
	}
}

func (s *Service) newSource(ep endpoint, auth core.GitHubAuth, secret secretFunc, appID string, install int64, connID string) tokenSource {
	if auth == core.GitHubApp {
		return &appSource{secret: secret, appID: appID, install: install, ep: ep, http: s.httpClient(ep), now: s.now,
			red: s.vault.Redactor(), redID: "github:" + cmp.Or(connID, "test") + ":installation"}
	}
	return &patSource{secret: secret, now: s.now}
}

// source returns the cached token source of a stored connection.
func (s *Service) source(c *core.GitHubConnection, ep endpoint) tokenSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.sources[c.ID]; ok && cs.base == ep.base.String() {
		return cs.src
	}
	if old, ok := s.sources[c.ID]; ok {
		forget(old.src)
	}
	src := s.newSource(ep, c.Auth, s.vaultSecret(c.SecretRef, c.ID), c.AppID, c.InstallationID, c.ID)
	s.sources[c.ID] = &connSource{base: ep.base.String(), src: src}
	return src
}

func (s *Service) dropSource(id string) {
	s.mu.Lock()
	cs, ok := s.sources[id]
	delete(s.sources, id)
	s.mu.Unlock()
	if ok {
		forget(cs.src)
	}
}

func forget(src tokenSource) {
	if a, ok := src.(*appSource); ok {
		a.forget()
		return
	}
	src.invalidate()
}

func (s *Service) apiClient(ep endpoint, src tokenSource) *apiClient {
	return &apiClient{ep: ep, http: s.httpClient(ep), auth: src.token, authFailed: src.invalidate, now: s.now}
}

// client returns an API client for a stored connection. What the calls
// learn about the credentials is recorded on the connection, so Settings
// shows a revoked token without waiting for a manual test.
func (s *Service) client(c *core.GitHubConnection) (*apiClient, endpoint, error) {
	ep, err := s.endpoint()
	if err != nil {
		return nil, endpoint{}, err
	}
	cl := s.apiClient(ep, s.source(c, ep))
	cl.health = s.health(c.ID, c.Status)
	return cl, ep, nil
}

// health writes a connection's status when it changes.
func (s *Service) health(id, status string) func(ok bool) {
	var mu sync.Mutex
	return func(ok bool) {
		want, msg := "ok", ""
		if !ok {
			want, msg = "error", "GitHub rejected the credentials (wrong, expired or revoked token)"
		}
		mu.Lock()
		defer mu.Unlock()
		if status == want {
			return
		}
		status = want
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := s.store.Connection(ctx, id)
		if err != nil {
			return
		}
		c.Status, c.Error, c.CheckedAt = want, msg, s.now()
		if err := s.store.PutConnection(ctx, c); err != nil {
			s.log.Warn("github: recording the connection status", "connection", id, "err", err)
		}
	}
}

func (s *Service) record(ctx context.Context, actor, action, target, outcome string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	b, err := core.CanonicalJSON(detail)
	if err != nil {
		b = []byte("{}")
	}
	if err := s.audit.Record(ctx, core.AuditEntry{At: s.now(), Actor: actor, Action: action, Target: target, Outcome: outcome, Detail: b}); err != nil {
		s.log.Error("github: audit write failed", "action", action, "err", err)
	}
}

func userActor(u core.UserID) string {
	if u == "" {
		return "system"
	}
	return "user:" + string(u)
}

// checkoutStore is where a workspace's checkout manifest and pristine
// archive live: under the data dir, which no box mounts.
func (s *Service) checkoutStore(ws core.WorkspaceID, owner, name string) (string, error) {
	if s.dataDir == "" {
		return "", errors.New("github: no data directory for checkout manifests")
	}
	if !core.ValidID(core.PrefixWorkspace, string(ws)) {
		return "", fmt.Errorf("%w: workspace id %q", core.ErrInvalid, ws)
	}
	return filepath.Join(s.dataDir, "github", "checkouts", string(ws), strings.ToLower(owner), strings.ToLower(name)), nil
}

// keyedMutex serialises work per key without holding a global lock.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
