package egress

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config    config.View
	Store     core.Store
	Vault     core.Vault // search API keys
	Approvals core.Approvals
	Policy    core.Policy // net_allow grants
	Bus       core.Bus
	Audit     core.Auditor
	RunDir    string // /run/bothy
	Clock     core.Clock
	Log       *slog.Logger
}

// Service implements core.Egress, core.ToolProvider and core.CheckProvider.
type Service struct{ core.Egress }

func New(d Deps) (*Service, error) { return &Service{}, nil }

// Run batches egress_log writes until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (s *Service) Tools() []core.Tool   { return nil }
func (s *Service) Checks() []core.Check { return nil }
