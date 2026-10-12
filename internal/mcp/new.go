package mcp

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config   config.View
	Store    core.Store
	Sandbox  core.Sandbox
	Egress   core.Egress
	Registry core.ToolRegistry
	Vault    core.Vault
	Bus      core.Bus
	Audit    core.Auditor
	WorkDir  string // resolved work dir: install trees under <WorkDir>/mcp/<server>/<install_id>
	Clock    core.Clock
	Log      *slog.Logger
}

// Manager implements core.MCP and core.CheckProvider.
type Manager struct{ core.MCP }

func New(d Deps) (*Manager, error) { return &Manager{}, nil }

func (m *Manager) Checks() []core.Check { return nil }

func (m *Manager) Start(ctx context.Context) error { return nil }
func (m *Manager) Close() error                    { return nil }
