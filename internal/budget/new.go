package budget

import (
	"errors"
	"log/slog"
	"sync"

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
type Service struct {
	store core.Store
	cfg   config.View
	bus   core.Bus
	audit core.Auditor
	clock core.Clock
	log   *slog.Logger

	senders *senderLimiter
	zones   sync.Map // timezone name -> *time.Location

	mu     sync.Mutex
	warned map[warnKey]string // spend day of the last 80% warning per cap
}

var _ core.Budget = (*Service)(nil)

func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Config == nil {
		return nil, errors.New("budget: Store and Config are required")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{
		store:   d.Store,
		cfg:     d.Config,
		bus:     d.Bus,
		audit:   d.Audit,
		clock:   d.Clock,
		log:     d.Log,
		senders: newSenderLimiter(senderKeys),
		warned:  map[warnKey]string{},
	}, nil
}
