package budget

import (
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

// Service implements core.Budget.
type Service struct{ core.Budget }

func New(d Deps) (*Service, error) { return &Service{}, nil }
