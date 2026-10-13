package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/skills/treehash"
)

var errDrifted = errors.New("install tree changed on disk since it was installed (drifted); reinstall to re-enable")

// discover lists the server's tools through a short-lived session (for
// stdio, a discovery box with no workspace that is destroyed afterwards),
// records every definition as a pin and registers the approved,
// hash-matching tools.
func (m *Manager) discover(ctx context.Context, s *server) {
	s.op.Lock()
	defer s.op.Unlock()
	m.discoverLocked(ctx, s)
}

func (m *Manager) discoverLocked(ctx context.Context, s *server) {
	if s.isRemoved() || ctx.Err() != nil {
		return
	}
	now := m.clock.Now()
	switch {
	case s.spec == nil:
		return
	case !s.spec.enabled:
		s.setState(now, stDisabled, "")
		m.syncTools(ctx, s)
		return
	}
	s.setState(now, stConnecting, "")
	dctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	var c *conn
	var err error
	switch s.spec.transport {
	case "stdio":
		c, err = m.discoverStdio(dctx, s)
	default:
		c, err = m.connectHTTP(dctx, s, "")
	}
	if err != nil {
		state := stError
		switch {
		case errors.Is(err, core.ErrNoSandbox):
			state, err = stNoSandbox, errors.New(noSandboxMsg)
		case errors.Is(err, errDrifted):
			state = stDisabled
		case !errors.Is(err, errNotInstalled) && ctx.Err() == nil:
			s.failed(m.clock.Now())
		}
		// A server may echo request headers (vault secrets) in an error body.
		msg := m.redact(err.Error())
		m.log.Warn("mcp discovery failed", "server", s.name, "err", msg)
		s.setState(m.clock.Now(), state, msg)
		m.syncTools(ctx, s)
		return
	}
	m.closeConn(c)
	s.mu.Lock()
	s.protocol = c.protocol
	s.retries, s.retryAt = 0, time.Time{}
	s.mu.Unlock()
	m.observe(ctx, s, c.tools, true)
}

func (m *Manager) discoverStdio(ctx context.Context, s *server) (*conn, error) {
	if !m.d.Sandbox.Status(ctx).Available {
		return nil, core.ErrNoSandbox
	}
	if _, err := m.verifyInstall(ctx, s); err != nil {
		return nil, err
	}
	return m.connectStdio(ctx, s, discoverBox(s.name), "", "")
}

// verifyInstall loads the server's current install and re-hashes its tree. A
// mismatch marks the install drifted (persisted, audited, banner) and keeps
// the server disabled until it is reinstalled.
func (m *Manager) verifyInstall(ctx context.Context, s *server) (*core.MCPInstall, error) {
	in, err := m.d.Store.MCP().Install(ctx, s.name)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("%w: an admin must run Install", errNotInstalled)
	}
	if err != nil {
		return nil, err
	}
	switch {
	case in.Status == "drifted":
		m.setDrifted(s, true)
		return nil, errDrifted
	case in.Status != "ready":
		return nil, fmt.Errorf("%w: last install is %s %s", errNotInstalled, in.Status, in.Error)
	case in.Version != s.spec.pkg.Version:
		return nil, fmt.Errorf("installed version %s differs from the configured %s; run Install", in.Version, s.spec.pkg.Version)
	}
	sum, err := treehash.Sum(filepath.Join(m.d.WorkDir, filepath.FromSlash(s.spec.installTree(in.InstallID))))
	if err != nil || sum != in.TreeHash {
		reason := "tree hash mismatch"
		if err != nil {
			reason = err.Error()
		}
		m.markDrifted(ctx, s, in, reason)
		return nil, errDrifted
	}
	s.mu.Lock()
	s.install = in
	s.mu.Unlock()
	m.setDrifted(s, false)
	return in, nil
}

func (m *Manager) markDrifted(ctx context.Context, s *server, in *core.MCPInstall, reason string) {
	in.Status, in.Error = "drifted", reason
	if err := m.d.Store.MCP().PutInstall(ctx, in); err != nil {
		m.log.Error("record drifted mcp install", "server", s.name, "err", err)
	}
	m.record(ctx, "system", "mcp.changed", s.name, "ok", map[string]any{"kind": "tree_drift", "install_id": in.InstallID, "reason": reason})
	m.log.Warn("mcp install drifted; server disabled", "server", s.name, "reason", reason)
	m.closeInstances(s)
	s.mu.Lock()
	s.install = nil
	s.mu.Unlock()
	s.setState(m.clock.Now(), stDisabled, errDrifted.Error())
	m.setDrifted(s, true)
	m.syncTools(ctx, s)
}

func (m *Manager) setDrifted(s *server, on bool) {
	s.mu.Lock()
	changed := s.drifted != on
	s.drifted = on
	s.mu.Unlock()
	if changed {
		m.refreshBanners()
	}
}

// observe records the tools a session reported. Each definition is pinned
// (an unseen hash is pending); a pending definition for a tool that has an
// approved one is a change, audited as mcp.changed with a field diff. The
// tool then stays unregistered until an admin approves the new definition,
// because exposure requires the live hash to equal the approved hash. full
// replaces the server's live tool set (discovery); otherwise the tools are
// merged in (a runtime session that saw a different definition).
func (m *Manager) observe(ctx context.Context, s *server, tools map[string]discovered, full bool) {
	approved, err := m.d.Store.MCP().Approved(ctx, s.name)
	if err != nil {
		m.log.Error("load approved mcp pins", "server", s.name, "err", err)
		s.setState(m.clock.Now(), stError, err.Error())
		return
	}
	now := m.clock.Now()
	live := map[string]string{}
	var pending []string
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		d := tools[name]
		st, err := m.d.Store.MCP().UpsertPin(ctx, &core.MCPPin{Server: s.name, Tool: name, Hash: d.hash, Definition: d.canon,
			SideEffect: s.spec.suggestedClass(name, d.def), Private: s.spec.privateOutput(name), FirstSeen: now})
		if err != nil {
			m.log.Error("record mcp pin", "server", s.name, "tool", name, "err", err)
			continue
		}
		live[name] = d.hash
		if st != core.PinPending {
			continue
		}
		pending = append(pending, name)
		if prev := approved[name]; prev != nil && prev.Hash != d.hash {
			m.log.Warn("mcp tool definition changed; tool disabled until re-approved", "server", s.name, "tool", name)
			m.record(ctx, "system", "mcp.changed", s.name, "ok", map[string]any{
				"kind": "definition", "tool": name, "old_hash": prev.Hash, "new_hash": d.hash,
				"diff": diffDefinitions(prev.Definition, d.canon),
			})
		}
	}
	s.mu.Lock()
	if full || s.live == nil {
		s.live = live
	} else {
		maps.Copy(s.live, live)
	}
	s.mu.Unlock()
	if len(pending) > 0 {
		m.publish(core.MCPPinData{Server: s.name, Tools: pending})
	}
	m.syncTools(ctx, s)
}

// syncTools re-registers the server's tools: exactly those whose live hash
// equals the approved pin's hash, swapped in with one ReplaceSource. The
// registry is touched only when that set (or any registered spec) changed. It
// also refreshes the tool and pending counts, the ready/pins_pending state and
// the banners.
func (m *Manager) syncTools(ctx context.Context, s *server) {
	m.regMu.Lock()
	defer m.regMu.Unlock()
	s.mu.Lock()
	live := maps.Clone(s.live)
	removed := s.removed
	exposable := !removed && s.spec != nil && s.spec.enabled && !s.drifted
	if removed {
		// applyConfig already dropped the source. A late discovery of a
		// replaced server must not touch the registry: the source name now
		// belongs to its successor.
		s.tools, s.pending, s.regKey, s.exposed = 0, 0, "", nil
	}
	s.mu.Unlock()
	if removed {
		m.refreshBanners()
		return
	}
	var want []*tool
	pending := 0
	if exposable && len(live) > 0 {
		approved, err := m.d.Store.MCP().Approved(ctx, s.name)
		if err != nil {
			m.log.Error("load approved mcp pins", "server", s.name, "err", err)
		}
		latest, err := m.d.Store.MCP().Pins(ctx, s.name)
		if err != nil {
			m.log.Error("load mcp pins", "server", s.name, "err", err)
		}
		for _, p := range latest {
			if p.Status == core.PinPending && live[p.Tool] == p.Hash {
				pending++
			}
		}
		for _, name := range slices.Sorted(maps.Keys(live)) {
			p := approved[name]
			if p == nil || p.Hash != live[name] {
				continue
			}
			t, err := newTool(m, s.spec, p)
			if err != nil {
				m.log.Warn("mcp tool not registered", "server", s.name, "tool", name, "err", err)
				continue
			}
			want = append(want, t)
		}
	}
	key := registrationKey(want)
	s.mu.Lock()
	same := key == s.regKey
	s.mu.Unlock()
	var exposed map[string]string
	if !same {
		src := source(s.name)
		tools := make([]core.Tool, len(want))
		for i, t := range want {
			tools[i] = t
		}
		if err := m.d.Registry.ReplaceSource(src, tools); err != nil {
			m.log.Warn("mcp tools not registered", "server", s.name, "err", err)
			// Never equal to a computed key, so the next sync retries.
			key = "\x00partial"
		}
		exposed = make(map[string]string, len(want))
		for _, t := range want {
			if rt, ok := m.d.Registry.Get(t.spec.Def.Name); ok && rt.Spec().Source == src {
				exposed[t.name] = t.hash
			}
		}
	}
	s.mu.Lock()
	if !same {
		s.exposed, s.tools, s.regKey = exposed, len(exposed), key
	}
	s.pending = pending
	if s.state == stReady || s.state == stPinsPending || s.state == stConnecting && exposable && live != nil {
		next := stReady
		if pending > 0 {
			next = stPinsPending
		}
		if next != s.state {
			s.state, s.err, s.since = next, "", m.clock.Now()
		}
	}
	s.mu.Unlock()
	m.refreshBanners()
}

func registrationKey(tools []*tool) string {
	var b strings.Builder
	for _, t := range tools {
		j, err := json.Marshal(struct {
			Spec     core.ToolSpec
			Hash     string
			FieldMap map[string]string
		}{t.spec, t.hash, t.fieldMap})
		if err != nil {
			return "unkeyable"
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}
