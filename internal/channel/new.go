package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Config    config.View
	Auth      core.Auth
	Runner    core.Runner
	Approvals core.Approvals
	Budget    core.Budget
	Sandbox   core.Sandbox // /status only
	LLM       core.LLM     // /model only: plain model names; nil shows refs
	Store     core.Store
	Bus       core.Bus
	Audit     core.Auditor
	Redactor  core.Redactor
	WorkDir   string // attachments are written to <WorkDir>/ws/<workspace>/.bothy/in/
	Clock     core.Clock
	Log       *slog.Logger
}

var (
	_ core.ChannelHub    = (*Hub)(nil)
	_ core.CheckProvider = (*Hub)(nil)
)

// Hub implements core.ChannelHub and core.CheckProvider.
type Hub struct {
	d     Deps
	log   *slog.Logger
	clock core.Clock

	// ctx lives until Run returns; dispatchers, chat workers, stream sinks and
	// card deliveries all run under it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	reconfig  chan struct{}
	actionSem chan struct{}

	// Timings are fields so tests can shorten them.
	statusEvery   time.Duration
	placeholderIn time.Duration
	typingEvery   time.Duration
	sinkIdle      time.Duration

	mu        sync.Mutex
	adapters  map[string]*adapter
	running   bool
	stopped   bool
	runCtx    context.Context
	chats     map[string]*chatQueue
	delivered map[core.ApprovalID]time.Time
	resolved  map[string]time.Time
	slowed    map[string]time.Time
	replied   map[string]time.Time
	noted     map[string]time.Time // noteOnce keys, until they may be noted again
	// compacting holds the sessions with a /compact in flight.
	compacting map[core.SessionID]struct{}
	// linkNoted holds the sessions told why their links show as plain text.
	linkNoted map[core.SessionID]time.Time
	asks      askLimiter
}

func New(d Deps) (*Hub, error) {
	var missing []string
	for name, ok := range map[string]bool{
		"Config": d.Config != nil, "Auth": d.Auth != nil, "Runner": d.Runner != nil, "Approvals": d.Approvals != nil,
		"Budget": d.Budget != nil, "Store": d.Store != nil, "Bus": d.Bus != nil,
	} {
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("channel hub: missing deps %v: %w", missing, core.ErrInvalid)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		d: d, log: d.Log, clock: d.Clock, ctx: ctx, cancel: cancel,
		reconfig:      make(chan struct{}, 1),
		actionSem:     make(chan struct{}, maxConcurrentActions),
		statusEvery:   2 * time.Second,
		placeholderIn: 1500 * time.Millisecond,
		typingEvery:   4 * time.Second,
		sinkIdle:      25 * time.Hour,
		adapters:      map[string]*adapter{},
		chats:         map[string]*chatQueue{},
		delivered:     map[core.ApprovalID]time.Time{},
		resolved:      map[string]time.Time{},
		slowed:        map[string]time.Time{},
		replied:       map[string]time.Time{},
		noted:         map[string]time.Time{},
		compacting:    map[core.SessionID]struct{}{},
		linkNoted:     map[core.SessionID]time.Time{},
	}, nil
}

func (h *Hub) Checks() []core.Check { return []core.Check{authFailedCheck{h}} }

var errRunTwice = errors.New("channel hub: Run called more than once")

// cfg returns the current config, falling back to the defaults when the view
// has none (tests with a bare config.Static).
func (h *Hub) cfg() *config.Config {
	if c := h.d.Config.Current(); c != nil {
		return c
	}
	return config.Default()
}

// goAsync runs fn on a hub-owned goroutine unless the hub has shut down.
func (h *Hub) goAsync(fn func(ctx context.Context)) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return false
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.contain("async work", func() { fn(h.ctx) })
	}()
	return true
}
