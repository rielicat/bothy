package egress

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config    config.View
	Store     core.Store
	Vault     core.Vault // search API keys
	Approvals core.Approvals
	Policy    core.Policy // net_allow grants
	Bus       core.Bus
	Audit     core.Auditor
	RunDir    string // /run/bothy
	Clock     core.Clock
	Log       *slog.Logger
}

var (
	_ core.Egress        = (*Service)(nil)
	_ core.ToolProvider  = (*Service)(nil)
	_ core.CheckProvider = (*Service)(nil)
)

// Service implements core.Egress, core.ToolProvider and core.CheckProvider.
type Service struct {
	view      config.View
	store     core.Store
	vault     core.Vault
	approvals core.Approvals
	policy    core.Policy
	bus       core.Bus
	auditor   core.Auditor
	runDir    string
	clock     core.Clock
	log       *slog.Logger

	// resolver and dialAddr are replaced by tests; nil resolver means the
	// configured system resolver.
	resolver resolver
	dialAddr func(ctx context.Context, network, addr string, allowPrivate bool) (net.Conn, error)

	dns   dnsCache
	logq  logQueue
	holds holdGroup

	mu       sync.Mutex
	boxes    map[string]*box
	closed   bool
	handlers sync.WaitGroup

	sysMu      sync.Mutex
	sysClients map[string]*http.Client

	tools []core.Tool
}

func New(d Deps) (*Service, error) {
	if d.Config == nil || d.Store == nil {
		return nil, fmt.Errorf("egress: %w: Config and Store are required", core.ErrInvalid)
	}
	s := &Service{
		view: d.Config, store: d.Store, vault: d.Vault, approvals: d.Approvals, policy: d.Policy,
		bus: d.Bus, auditor: d.Audit, runDir: d.RunDir, clock: d.Clock, log: d.Log,
		dialAddr:   netDial,
		boxes:      map[string]*box{},
		sysClients: map[string]*http.Client{},
	}
	if s.clock == nil {
		s.clock = core.SystemClock{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.logq.wake = make(chan struct{}, 1)
	s.tools = []core.Tool{&webFetch{s: s}, &webSearch{s: s}, &netAllow{s: s}}
	return s, nil
}

func (s *Service) cfg() *config.Config { return s.view.Current() }

// Run batches egress_log writes until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.closeAll()
			waitTimeout(&s.handlers, 2*time.Second)
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.flush(fctx)
			return nil
		case <-t.C:
			_ = s.flush(ctx)
		case <-s.logq.wake:
			_ = s.flush(ctx)
		}
	}
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

func (s *Service) Tools() []core.Tool { return s.tools }

func (s *Service) Checks() []core.Check {
	return []core.Check{
		&check{s: s, id: "egress.wildcards", run: s.checkWildcards},
		&check{s: s, id: "egress.shared_tenant_allowed", run: s.checkSharedTenant},
		&check{s: s, id: "egress.presets_enabled", run: s.checkPresets},
		&check{s: s, id: "mcp.http_plaintext", run: s.checkMCPPlaintext},
	}
}
