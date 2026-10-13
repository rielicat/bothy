package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config   config.View
	Store    core.Store
	Sandbox  core.Sandbox
	Egress   core.Egress
	Registry core.ToolRegistry
	Vault    core.Vault
	Bus      core.Bus
	Audit    core.Auditor
	WorkDir  string // resolved work dir: install trees under <WorkDir>/mcp/<server>/<install_id>
	Clock    core.Clock
	Log      *slog.Logger
}

// Manager implements core.MCP and core.CheckProvider.
type Manager struct {
	d      Deps
	log    *slog.Logger
	clock  core.Clock
	client *mcpsdk.Client

	// ctx bounds every background operation (discovery, installs, reaping,
	// stdio streams); Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// watchers counts session watch goroutines; Close waits for them so every
	// stdio box is destroyed before it returns.
	watchers sync.WaitGroup

	sandboxOK atomic.Bool
	idle      time.Duration // stdio instances and HTTP sessions unused this long are closed

	// regMu serialises registry updates: a reloaded server and its
	// predecessor share one registry source.
	regMu sync.Mutex

	mu          sync.Mutex
	servers     map[string]*server
	order       []string
	started     bool
	closed      bool
	unwatch     func()
	pinsBanner  string
	driftBanner string
}

var (
	_ core.MCP           = (*Manager)(nil)
	_ core.CheckProvider = (*Manager)(nil)
)

// New builds the server table from config. Nothing is dialled or started
// until Start.
func New(d Deps) (*Manager, error) {
	if d.Config == nil || d.Store == nil || d.Sandbox == nil || d.Egress == nil || d.Registry == nil || d.Vault == nil {
		return nil, fmt.Errorf("mcp: config, store, sandbox, egress, registry and vault are required: %w", core.ErrInvalid)
	}
	m := &Manager{d: d, log: d.Log, clock: d.Clock, idle: idleTimeout, servers: map[string]*server{}}
	if m.clock == nil {
		m.clock = core.SystemClock{}
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	m.log = m.log.With("module", "mcp")
	m.client = mcpsdk.NewClient(&mcpsdk.Implementation{Name: "bothy", Version: "0.1"},
		&mcpsdk.ClientOptions{Logger: m.log, Capabilities: &mcpsdk.ClientCapabilities{}})
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.applyConfig(d.Config.Current().MCP.Servers, false)
	return m, nil
}

func (m *Manager) Checks() []core.Check { return []core.Check{pendingCheck{m}, driftCheck{m}} }

// Start connects every enabled server once (stdio through a short-lived
// discovery box), records and pins their tools, registers the approved,
// hash-matching ones and then returns; idle reaping, config reloads and
// sandbox status changes are handled in the background until Close or until
// ctx ends.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.unwatch = m.d.Config.Watch(m.onConfig)
	m.mu.Unlock()
	// Picks up mcp.servers edits made between New and the watch above.
	m.applyConfig(m.d.Config.Current().MCP.Servers, false)
	context.AfterFunc(ctx, m.cancel)
	m.sandboxOK.Store(m.d.Sandbox.Status(ctx).Available)
	m.d.Sandbox.Watch(m.onSandbox)
	m.spawn(m.reaper)
	m.discoverAll()
	return nil
}

// Close stops background work, closes every session and destroys the boxes
// of running stdio instances.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	unwatch := m.unwatch
	m.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	m.cancel()
	m.wg.Wait()
	for _, s := range m.allServers() {
		m.closeInstances(s)
	}
	m.watchers.Wait()
	return nil
}
