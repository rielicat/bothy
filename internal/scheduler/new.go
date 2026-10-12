package scheduler

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store  core.Store
	Config config.View
	Runner core.Runner
	Hub    core.ChannelHub // delivery sinks
	Memory core.Memory     // HEARTBEAT.md
	Bus    core.Bus
	Audit  core.Auditor
	Clock  core.Clock
	Log    *slog.Logger
}

// Service implements core.Scheduler.
type Service struct{ core.Scheduler }

func New(d Deps) (*Service, error) { return &Service{}, nil }

func (s *Service) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (s *Service) Tools() []core.Tool { return nil }
