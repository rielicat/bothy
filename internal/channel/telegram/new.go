package telegram

import (
	"fmt"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Telegram, d core.AdapterDeps) (core.Channel, error) {
	if d.HTTP == nil || d.Secret == nil {
		return nil, fmt.Errorf("telegram: AdapterDeps.HTTP and AdapterDeps.Secret are required: %w", core.ErrInvalid)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	a := &Adapter{deps: d, log: d.Log, cfg: cfg, titles: map[string]string{}}
	a.status = core.ChannelStatus{Name: Name, State: "connecting", Since: d.Clock.Now()}
	return a, nil
}
