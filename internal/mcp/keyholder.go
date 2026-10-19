package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// keyHolderMeta + server is the meta key naming the admin whose personal keys
// discovery of that server may use.
const keyHolderMeta = "mcp.discovery_key."

type keyHolder struct {
	User  core.UserID `json:"user"`
	Entry string      `json:"entry"` // entryHash of the config entry it was chosen for
}

// entryHash covers what decides where a key goes and how: turning the server
// off and on, or changing how its actions are classed, keeps the choice.
func entryHash(c config.MCPServer) string {
	b, err := json.Marshal(struct {
		Transport, URL string
		Headers        map[string]string
		AllowPrivate   bool
	}{c.Transport, c.URL, c.Headers, c.AllowPrivate})
	if err != nil {
		return ""
	}
	return sha256hex(b)
}

// DiscoverWith lets discovery of an HTTP server use admin's personal keys for
// the references that have no shared value, and rediscovers it. The choice
// holds while the entry's address and headers stay as they are (entryHash), so
// nobody can point that admin's keys at another address, and while admin is still an active
// admin. Calls never use those keys: each person's own key or the shared one
// makes theirs.
func (m *Manager) DiscoverWith(ctx context.Context, name string, admin core.UserID) error {
	s := m.server(name)
	switch {
	case s == nil:
		return fmt.Errorf("mcp server %q: %w", name, core.ErrNotFound)
	case s.spec == nil:
		return fmt.Errorf("mcp server %s: %w: %w", name, core.ErrInvalid, s.specErr)
	case s.spec.transport != "http":
		return fmt.Errorf("mcp server %s: %w: only a server at a web address uses keys", name, core.ErrInvalid)
	}
	if u, err := m.d.Store.Users().Get(ctx, admin); err != nil || u.Disabled || !u.Role.IsAdmin() {
		return fmt.Errorf("%w: only an admin's own keys can list a server's actions", core.ErrForbidden)
	}
	missing, err := m.ownKeysMissing(ctx, s, admin)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: you haven't saved your own key named %s. Add it in Saved keys, or add a shared key", core.ErrConflict, strings.Join(missing, ", "))
	}
	b, err := json.Marshal(keyHolder{User: admin, Entry: entryHash(s.raw)})
	if err != nil {
		return err
	}
	if err := m.d.Store.Meta().Set(ctx, keyHolderMeta+name, string(b)); err != nil {
		return err
	}
	m.record(ctx, "user:"+string(admin), "mcp.discovery_key", name, "ok", map[string]any{"user": admin})
	if !m.spawn(func(ctx context.Context) { m.discover(ctx, s) }) {
		return fmt.Errorf("mcp: manager closed: %w", core.ErrConflict)
	}
	return nil
}

// ownKeysMissing names the saved keys s refers to that have neither a shared
// value nor one of u's own. Names are listed, not read, so checking uses no key.
func (m *Manager) ownKeysMissing(ctx context.Context, s *server, u core.UserID) ([]string, error) {
	have := map[string]bool{}
	for _, sc := range []core.Scope{core.GlobalScope, core.UserScope(u)} {
		list, err := m.d.Vault.List(ctx, sc)
		if err != nil {
			return nil, err
		}
		for _, x := range list {
			have[x.Name] = true
		}
	}
	refs := maps.Clone(s.spec.headers)
	if refs == nil {
		refs = map[string]string{}
	}
	refs["\x00url"] = s.spec.url
	var out []string
	for _, n := range secretNames(refs) {
		// An aws-sm reference is company-wide: nobody has one of their own.
		if !strings.HasPrefix(n, config.AWSSecretPrefix) && !have[n] {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

// discoveryKeyOf is the admin whose personal keys discovery of s may use
// (DiscoverWith), or "" when nobody's may.
func (m *Manager) discoveryKeyOf(ctx context.Context, s *server) core.UserID {
	raw, err := m.d.Store.Meta().Get(ctx, keyHolderMeta+s.name)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			m.log.Warn("read mcp discovery key holder", "server", s.name, "err", err)
		}
		return ""
	}
	var k keyHolder
	if json.Unmarshal([]byte(raw), &k) != nil || k.Entry == "" || k.Entry != entryHash(s.raw) {
		return ""
	}
	u, err := m.d.Store.Users().Get(ctx, k.User)
	if err != nil || u.Disabled || !u.Role.IsAdmin() {
		return ""
	}
	return k.User
}
