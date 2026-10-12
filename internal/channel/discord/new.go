package discord

import (
	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Discord, d core.AdapterDeps) (core.Channel, error) {
	return nil, core.ErrNotImplemented
}
