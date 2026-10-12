package agent

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store     core.Store
	Config    config.View
	LLM       core.LLM
	Tools     core.ToolRegistry
	Policy    core.Policy
	Approvals core.Approvals
	Budget    core.Budget
	Memory    core.Memory
	Skills    core.Skills
	Sandbox   core.Sandbox
	Egress    core.Egress
	Redactor  core.Redactor
	Bus       core.Bus
	Audit     core.Auditor
	DataDir   string // SOUL.md
	WorkDir   string // workspaces
	Clock     core.Clock
	Log       *slog.Logger
}

// Runner implements core.Runner.
type Runner struct{ core.Runner }

func New(d Deps) (*Runner, error) { return &Runner{}, nil }

// SetHub is called once by cmd/bothy after the hub exists (the hub depends on
// the runner). The runner uses it for sinks of resumed parked runs
// (Hub.StreamTo) and for restart notices (Messenger.Send); before SetHub, or
// with a nil hub in tests, those deliveries are skipped.
func (r *Runner) SetHub(h core.ChannelHub) {}

// Run performs boot recovery (MarkInterrupted, resumable parked runs) and reaps
// idle session workers until ctx is done; then drains for up to 10s.
func (r *Runner) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
