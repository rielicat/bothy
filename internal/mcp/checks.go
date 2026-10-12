package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/skills/treehash"
)

const (
	pendingID = "mcp.pending_pins"
	driftID   = "mcp.drifted"
)

// pendingCheck reports tools waiting for an admin's review.
type pendingCheck struct{ m *Manager }

func (pendingCheck) ID() string { return pendingID }

func (c pendingCheck) Run(ctx context.Context) []core.Finding {
	var parts []string
	for _, s := range c.m.allServers() {
		pins, err := c.m.d.Store.MCP().Pins(ctx, s.name)
		if err != nil {
			continue
		}
		var tools []string
		for _, p := range pins {
			if p.Status == core.PinPending {
				tools = append(tools, p.Tool)
			}
		}
		if len(tools) > 0 {
			parts = append(parts, s.name+": "+strings.Join(tools, ", "))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: pendingID, Area: "mcp", Title: "MCP tools are waiting for review",
		Detail:   "New or changed tool definitions are not offered to the agent until an admin approves them (" + strings.Join(parts, "; ") + ").",
		Fix:      "/settings/mcp",
		Severity: core.SevMedium, Security: true,
	}}
}

func (pendingCheck) Fix(context.Context, core.Finding) error {
	return fmt.Errorf("mcp: %s needs an admin's review: %w", pendingID, core.ErrNotImplemented)
}

// driftCheck re-hashes every ready install tree and reports installs whose
// files changed (or that are already marked drifted). Run only reads; Fix
// marks them drifted and disables those servers until they are reinstalled.
type driftCheck struct{ m *Manager }

func (driftCheck) ID() string { return driftID }

func (c driftCheck) drifted(ctx context.Context) []string {
	var out []string
	for _, s := range c.m.allServers() {
		if s.spec == nil || s.spec.transport != "stdio" {
			continue
		}
		in, err := c.m.d.Store.MCP().Install(ctx, s.name)
		if err != nil {
			continue
		}
		switch in.Status {
		case "drifted":
			out = append(out, s.name)
		case "ready":
			sum, err := treehash.Sum(filepath.Join(c.m.d.WorkDir, filepath.FromSlash(s.spec.installTree(in.InstallID))))
			if err != nil || sum != in.TreeHash {
				out = append(out, s.name)
			}
		}
	}
	return out
}

func (c driftCheck) Run(ctx context.Context) []core.Finding {
	names := c.drifted(ctx)
	if len(names) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: driftID, Area: "mcp", Title: "MCP install trees changed on disk",
		Detail:   fmt.Sprintf("The installed files of %s no longer match the hash recorded at install time.", strings.Join(names, ", ")),
		Fix:      "Disable the drifted servers (doctor --fix), then reinstall them under Settings → MCP",
		Severity: core.SevMedium, Security: true, AutoFix: true,
	}}
}

func (c driftCheck) Fix(ctx context.Context, f core.Finding) error {
	if f.ID != driftID {
		return fmt.Errorf("mcp: finding %q: %w", f.ID, core.ErrNotImplemented)
	}
	for _, name := range c.drifted(ctx) {
		s := c.m.server(name)
		if s == nil {
			continue
		}
		in, err := c.m.d.Store.MCP().Install(ctx, name)
		if err != nil {
			return err
		}
		s.op.Lock()
		c.m.markDrifted(ctx, s, in, "disabled by doctor: "+driftID)
		s.op.Unlock()
	}
	return nil
}
