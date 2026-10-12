package vault

import (
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// Redactor implements core.Redactor. Created first: the logger, audit and vault use it.
type Redactor struct{ core.Redactor }

func NewRedactor() *Redactor { return &Redactor{} }

// NewSlogHandler wraps h so the message and every string attribute pass through
// r.Redact (the skeleton stub returns h unchanged until M1 lands).
func NewSlogHandler(h slog.Handler, r *Redactor) slog.Handler { return h }

type Deps struct {
	Store    core.Store
	Config   config.View
	Redactor *Redactor
	Audit    core.Auditor
	Env      func() []string // os.Environ: BOTHY_SECRET_<NAME>[_FILE], BOTHY_KEK
	Clock    core.Clock
	Log      *slog.Logger
}

// Vault implements core.Vault and core.CheckProvider.
type Vault struct{ core.Vault }

func New(d Deps) (*Vault, error) { return &Vault{}, nil }

func (v *Vault) Checks() []core.Check { return nil }
