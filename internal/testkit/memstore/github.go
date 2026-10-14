package memstore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// NewGitHub returns an empty in-memory core.GitHubStore that passes
// conformance.GitHubStore. It is separate from New because the GitHub store
// is not part of core.Store.
func NewGitHub() core.GitHubStore {
	return &githubStore{conns: map[string]*core.GitHubConnection{}, repos: map[string]*core.GitHubRepo{}}
}

type githubStore struct {
	mu    sync.Mutex
	conns map[string]*core.GitHubConnection
	repos map[string]*core.GitHubRepo // key: lowercase owner/name
}

func repoKey(owner, name string) string { return strings.ToLower(owner) + "/" + strings.ToLower(name) }

func msTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return time.UnixMilli(t.UnixMilli()).UTC()
}

func (s *githubStore) PutConnection(_ context.Context, c *core.GitHubConnection) error {
	if !core.ValidID(core.PrefixGitHubConn, c.ID) {
		return fmt.Errorf("memstore: github connection id: %w", core.ErrInvalid)
	}
	status := cmp.Or(c.Status, "ok")
	switch {
	case c.Auth != core.GitHubPAT && c.Auth != core.GitHubApp,
		c.Auth == core.GitHubApp && (c.AppID == "" || c.InstallationID <= 0),
		!strings.HasPrefix(c.SecretRef, "secret:") || len(c.SecretRef) <= len("secret:"),
		status != "ok" && status != "error":
		return fmt.Errorf("memstore: github connection %s: %w", c.ID, core.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.Status = status
	cp := *c
	if old, ok := s.conns[c.ID]; ok {
		cp.CreatedAt, cp.CreatedBy = old.CreatedAt, old.CreatedBy
	}
	cp.TokenExpires, cp.CheckedAt, cp.CreatedAt, cp.UpdatedAt = msTime(cp.TokenExpires), msTime(cp.CheckedAt), msTime(cp.CreatedAt), msTime(cp.UpdatedAt)
	s.conns[c.ID] = &cp
	return nil
}

func (s *githubStore) Connection(_ context.Context, id string) (*core.GitHubConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if !ok {
		return nil, fmt.Errorf("memstore: github connection %s: %w", id, core.ErrNotFound)
	}
	cp := *c
	return &cp, nil
}

func (s *githubStore) Connections(context.Context) ([]*core.GitHubConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*core.GitHubConnection, 0, len(s.conns))
	for _, c := range s.conns {
		cp := *c
		out = append(out, &cp)
	}
	slices.SortFunc(out, func(a, b *core.GitHubConnection) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (s *githubStore) DeleteConnection(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[id]; !ok {
		return fmt.Errorf("memstore: github connection %s: %w", id, core.ErrNotFound)
	}
	delete(s.conns, id)
	for k, r := range s.repos {
		if r.Connection == id {
			delete(s.repos, k)
		}
	}
	return nil
}

func (s *githubStore) PutRepo(_ context.Context, r *core.GitHubRepo) error {
	if strings.TrimSpace(r.Owner) == "" || strings.TrimSpace(r.Name) == "" || r.Connection == "" {
		return fmt.Errorf("memstore: repo owner/name/connection: %w", core.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[r.Connection]; !ok {
		return fmt.Errorf("memstore: repo %s: unknown connection: %w", r.FullName(), core.ErrInvalid)
	}
	now := time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	k := repoKey(r.Owner, r.Name)
	cp := *r
	if old, ok := s.repos[k]; ok {
		if old.Connection != r.Connection {
			return fmt.Errorf("memstore: repo %s belongs to another connection: %w", r.FullName(), core.ErrConflict)
		}
		cp.CreatedAt, cp.LastIndexedSHA = old.CreatedAt, old.LastIndexedSHA
	}
	cp.CreatedAt, cp.UpdatedAt = msTime(cp.CreatedAt), msTime(cp.UpdatedAt)
	s.repos[k] = &cp
	return nil
}

func (s *githubStore) Repo(_ context.Context, owner, name string) (*core.GitHubRepo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[repoKey(owner, name)]
	if !ok {
		return nil, fmt.Errorf("memstore: repo %s/%s: %w", owner, name, core.ErrNotFound)
	}
	cp := *r
	return &cp, nil
}

func (s *githubStore) Repos(_ context.Context, connection string) ([]*core.GitHubRepo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*core.GitHubRepo
	for _, r := range s.repos {
		if connection == "" || r.Connection == connection {
			cp := *r
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *core.GitHubRepo) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Owner), strings.ToLower(b.Owner)),
			strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return out, nil
}

func (s *githubStore) SetIndexed(_ context.Context, owner, name, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[repoKey(owner, name)]
	if !ok {
		return fmt.Errorf("memstore: repo %s/%s: %w", owner, name, core.ErrNotFound)
	}
	r.LastIndexedSHA, r.UpdatedAt = sha, msTime(time.Now())
	return nil
}
