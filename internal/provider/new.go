package provider

import (
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config config.View
	Vault  core.Vault
	Egress core.Egress     // SystemClient per provider base URL
	Env    func() []string // Detect only: names of well-known key env vars
	Clock  core.Clock
	Log    *slog.Logger
}

// Facade implements core.LLM and core.CheckProvider.
type Facade struct{ core.LLM }

func New(d Deps) (*Facade, error) { return &Facade{}, nil }

func (f *Facade) Checks() []core.Check { return nil }
