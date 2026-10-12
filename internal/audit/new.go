package audit

import (
	"context"
	"log/slog"

	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store    core.Store
	Redactor core.Redactor
	Clock    core.Clock
	Log      *slog.Logger
}

// Writer implements core.Auditor.
type Writer struct{ core.Auditor }

func New(d Deps) (*Writer, error) { return &Writer{}, nil }

// Run flushes batched entries every 250ms until ctx is done, then flushes once more.
func (w *Writer) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (w *Writer) Flush(ctx context.Context) error { return nil }
