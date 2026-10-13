package memory

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"

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

var (
	_ core.Memory        = (*Service)(nil)
	_ core.CheckProvider = (*Service)(nil)
)

// Service implements core.Memory and core.CheckProvider.
type Service struct {
	store core.Store
	cfg   config.View
	bus   core.Bus
	audit core.Auditor
	dir   string
	clock core.Clock
	log   *slog.Logger

	// mu serializes every file mutation with its memory_events row, and the
	// snapshot read, so a snapshot's LastID always matches the files it read.
	mu sync.Mutex

	tools []core.Tool
}

func New(d Deps) (*Service, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("memory: Deps.Store is required")
	case d.Config == nil:
		return nil, errors.New("memory: Deps.Config is required")
	case d.DataDir == "":
		return nil, errors.New("memory: Deps.DataDir is required")
	}
	s := &Service{
		store: d.Store,
		cfg:   d.Config,
		bus:   d.Bus,
		audit: d.Audit,
		dir:   filepath.Join(d.DataDir, "memory"),
		clock: d.Clock,
		log:   d.Log,
	}
	if s.clock == nil {
		s.clock = core.SystemClock{}
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s.tools = []core.Tool{newMemoryTool(s), newSessionSearchTool(s)}
	return s, nil
}

// Tools returns the memory and session_search tools.
func (s *Service) Tools() []core.Tool { return s.tools }

// Checks returns the memory.over_cap doctor check.
func (s *Service) Checks() []core.Check { return []core.Check{overCapCheck{s}} }
