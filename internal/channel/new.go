package channel

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config    config.View
	Auth      core.Auth
	Runner    core.Runner
	Approvals core.Approvals
	Budget    core.Budget
	Sandbox   core.Sandbox // /status only
	Store     core.Store
	Bus       core.Bus
	Audit     core.Auditor
	Redactor  core.Redactor
	WorkDir   string // attachments are written to <WorkDir>/ws/<workspace>/.bothy/in/
	Clock     core.Clock
	Log       *slog.Logger
}

// Hub implements core.ChannelHub and core.CheckProvider.
type Hub struct{ core.ChannelHub }

func New(d Deps) (*Hub, error) { return &Hub{}, nil }

func (h *Hub) Checks() []core.Check { return nil }

func (h *Hub) Add(ch core.Channel) {}

func (h *Hub) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
