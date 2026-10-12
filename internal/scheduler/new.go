package scheduler

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

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

var _ core.Scheduler = (*Service)(nil)

// Service implements core.Scheduler.
type Service struct {
	store  core.Store
	cfg    config.View
	runner core.Runner
	hub    core.ChannelHub
	memory core.Memory
	bus    core.Bus
	audit  core.Auditor
	clock  core.Clock
	log    *slog.Logger

	jitter func() float64 // uniform in [-1, 1); replaced in tests

	// mu serializes every write to schedule rows (firing, run outcomes,
	// Upsert, Delete, RunNow, heartbeat sync) so their read-modify-write of
	// next_run_at, failures and last_status never interleave. It also guards
	// unsaved and notices.
	mu      sync.Mutex
	unsaved map[core.ScheduleID]runState
	notices []notice

	paused atomic.Bool
	syncHB atomic.Bool
	wake   chan struct{}

	tools []core.Tool
}

func New(d Deps) (*Service, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("scheduler: Deps.Store is required")
	case d.Config == nil:
		return nil, errors.New("scheduler: Deps.Config is required")
	case d.Runner == nil:
		return nil, errors.New("scheduler: Deps.Runner is required")
	}
	s := &Service{
		store:   d.Store,
		cfg:     d.Config,
		runner:  d.Runner,
		hub:     d.Hub,
		memory:  d.Memory,
		bus:     d.Bus,
		audit:   d.Audit,
		clock:   d.Clock,
		log:     d.Log,
		jitter:  cryptoJitter,
		wake:    make(chan struct{}, 1),
		unsaved: map[core.ScheduleID]runState{},
	}
	if s.clock == nil {
		s.clock = core.SystemClock{}
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s.tools = []core.Tool{newListTool(s), newCreateTool(s), newDeleteTool(s)}
	return s, nil
}

// Tools returns schedule_list, schedule_create and schedule_delete.
func (s *Service) Tools() []core.Tool { return s.tools }

// poke wakes the Run loop without blocking.
func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func cryptoJitter() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return float64(binary.BigEndian.Uint64(b[:])>>11)/(1<<53)*2 - 1
}
