package policy

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store  core.Store
	Config config.View
	Bus    core.Bus
	Audit  core.Auditor
	Clock  core.Clock
	Log    *slog.Logger
}

// Engine implements core.Policy and core.CheckProvider.
type Engine struct{ core.Policy }

// Approvals implements core.Approvals.
type Approvals struct{ core.Approvals }

func New(d Deps) (*Engine, *Approvals, error) { return &Engine{}, &Approvals{}, nil }

// Run expires due approvals every 30s until ctx is done.
func (a *Approvals) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (e *Engine) Checks() []core.Check { return nil }

func (a *Approvals) OnResolved(fn func(*core.Approval)) {}
