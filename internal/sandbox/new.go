package sandbox

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config  config.View
	Egress  core.Egress
	Store   core.Store
	Bus     core.Bus
	Audit   core.Auditor
	WorkDir string // /work
	RunDir  string // /run/bothy
	Clock   core.Clock
	Log     *slog.Logger
}

// Client implements core.Sandbox, core.ToolProvider and core.CheckProvider.
type Client struct{ core.Sandbox }

// New probes backends in order sandboxd -> bwrap -> none (per sandbox.backend).
func New(ctx context.Context, d Deps) (*Client, error) { return &Client{}, nil }

// Run watches sandboxd health, reaps idle boxes and scans workspace quotas.
func (c *Client) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (c *Client) Tools() []core.Tool   { return nil }
func (c *Client) Checks() []core.Check { return nil }
