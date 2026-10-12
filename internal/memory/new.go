package memory

import (
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store   core.Store
	Config  config.View
	Bus     core.Bus
	Audit   core.Auditor
	DataDir string // files under <data>/memory/<principal>/
	Clock   core.Clock
	Log     *slog.Logger
}

// Service implements core.Memory and core.CheckProvider.
type Service struct{ core.Memory }

func New(d Deps) (*Service, error) { return &Service{}, nil }

func (s *Service) Tools() []core.Tool   { return nil }
func (s *Service) Checks() []core.Check { return nil }
