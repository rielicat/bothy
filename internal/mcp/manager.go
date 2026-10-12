package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

const (
	stDisabled    = "disabled"
	stInstalling  = "installing"
	stConnecting  = "connecting"
	stReady       = "ready"
	stError       = "error"
	stNoSandbox   = "no_sandbox"
	stPinsPending = "pins_pending"
)

const (
	idleTimeout       = 10 * time.Minute
	discoverTimeout   = 90 * time.Second
	connectTimeout    = 60 * time.Second
	installTimeout    = 10 * time.Minute
	maxToolsPerServer = 512
)

// server is one configured MCP server and everything known about it.
type server struct {
	name    string
	raw     config.MCPServer
	spec    *serverSpec // nil when the config entry was rejected
	specErr error

	// op serialises discovery and installation of this server.
	op sync.Mutex

	mu         sync.Mutex
	state      string
	err        string
	protocol   string
	since      time.Time
	live       map[string]string // tool -> definition hash at the last discovery
	install    *core.MCPInstall  // verified install (stdio)
	tools      int
	pending    int
	regKey     string // registrationKey of the registered tools
	instances  map[string]*instance
	installing bool
	drifted    bool
	removed    bool
}

func source(name string) string { return "mcp:" + name }

func (s *server) setState(now time.Time, state, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != state {
		s.since = now
	}
	s.state, s.err = state, msg
}

func (s *server) status() core.MCPStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return core.MCPStatus{Name: s.name, Transport: s.raw.Transport, State: s.state, Protocol: s.protocol, Err: s.err,
		Tools: s.tools, Pending: s.pending, Since: s.since}
}

func (s *server) isRemoved() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removed
}

// usable reports whether the server's tools may be called right now.
func (s *server) usable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.removed && (s.state == stReady || s.state == stPinsPending)
}

func (m *Manager) newServer(c config.MCPServer) *server {
	s := &server{name: c.Name, raw: c, since: m.clock.Now(), instances: map[string]*instance{}}
	s.spec, s.specErr = newServerSpec(c)
	switch {
	case s.specErr != nil:
		s.state, s.err = stError, "invalid config: "+s.specErr.Error()
	case !s.spec.enabled:
		s.state = stDisabled
	default:
		s.state = stConnecting
	}
	return s
}

// spawn runs fn in the background unless the manager is closed.
func (m *Manager) spawn(fn func(ctx context.Context)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn(m.ctx)
	}()
	return true
}

func (m *Manager) server(name string) *server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[name]
}

// serversLocked returns the servers in config order; callers hold m.mu.
func (m *Manager) serversLocked() []*server {
	out := make([]*server, 0, len(m.order))
	for _, n := range m.order {
		out = append(out, m.servers[n])
	}
	return out
}

func (m *Manager) allServers() []*server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.serversLocked()
}

// applyConfig reconciles the server table with mcp.servers: unchanged entries
// keep their state, changed and removed ones are shut down (sessions closed,
// boxes destroyed, tools unregistered) and, when discover is set, new and
// changed ones are discovered in the background.
func (m *Manager) applyConfig(list []config.MCPServer, discover bool) {
	m.mu.Lock()
	next := make(map[string]*server, len(list))
	order := make([]string, 0, len(list))
	var stop, start []*server
	for _, c := range list {
		if _, dup := next[c.Name]; dup {
			m.log.Warn("duplicate mcp server name ignored", "server", c.Name)
			continue
		}
		order = append(order, c.Name)
		if old := m.servers[c.Name]; old != nil && reflect.DeepEqual(old.raw, c) {
			next[c.Name] = old
			continue
		} else if old != nil {
			stop = append(stop, old)
		}
		s := m.newServer(c)
		next[c.Name] = s
		if discover && s.state == stConnecting {
			start = append(start, s)
		}
	}
	for name, old := range m.servers {
		if next[name] == nil {
			stop = append(stop, old)
		}
	}
	m.servers, m.order = next, order
	m.mu.Unlock()
	for _, s := range stop {
		s.mu.Lock()
		s.removed = true
		s.mu.Unlock()
		m.closeInstances(s)
		m.unregister(s.name)
	}
	for _, s := range start {
		m.spawn(func(ctx context.Context) { m.discover(ctx, s) })
	}
	m.refreshBanners()
}

func (m *Manager) onConfig(old, cur *config.Config) {
	if reflect.DeepEqual(old.MCP, cur.MCP) {
		return
	}
	servers := slices.Clone(cur.MCP.Servers)
	m.spawn(func(context.Context) { m.applyConfig(servers, true) })
}

func (m *Manager) onSandbox(st core.SandboxStatus) {
	if prev := m.sandboxOK.Swap(st.Available); prev == st.Available {
		return
	}
	for _, s := range m.allServers() {
		if s.spec == nil || s.spec.transport != "stdio" || !s.spec.enabled {
			continue
		}
		if st.Available {
			s.mu.Lock()
			again := s.state == stNoSandbox
			s.mu.Unlock()
			if again {
				m.spawn(func(ctx context.Context) { m.discover(ctx, s) })
			}
			continue
		}
		m.closeInstances(s)
		s.setState(m.clock.Now(), stNoSandbox, noSandboxMsg)
	}
}

const noSandboxMsg = "no sandbox: MCP stdio servers need sandboxd or bwrap"

// discoverAll discovers every enabled server concurrently and waits.
func (m *Manager) discoverAll() {
	var wg sync.WaitGroup
	for _, s := range m.allServers() {
		s.mu.Lock()
		want := s.state == stConnecting
		s.mu.Unlock()
		if !want {
			continue
		}
		wg.Add(1)
		if !m.spawn(func(ctx context.Context) { defer wg.Done(); m.discover(ctx, s) }) {
			wg.Done()
		}
	}
	wg.Wait()
}

func (m *Manager) Status() []core.MCPStatus {
	servers := m.allServers()
	out := make([]core.MCPStatus, len(servers))
	for i, s := range servers {
		out[i] = s.status()
	}
	return out
}

// Pins returns the latest definition of every tool the server has offered,
// plus, for each tool whose latest definition is not the approved one, the
// approved definition as well, so the review UI can diff the two.
func (m *Manager) Pins(ctx context.Context, name string) ([]*core.MCPPin, error) {
	latest, err := m.d.Store.MCP().Pins(ctx, name)
	if err != nil {
		return nil, err
	}
	approved, err := m.d.Store.MCP().Approved(ctx, name)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(latest)
	for _, p := range latest {
		if a := approved[p.Tool]; p.Status != core.PinApproved && a != nil && a.Hash != p.Hash {
			out = append(out, a)
		}
	}
	slices.SortStableFunc(out, func(a, b *core.MCPPin) int { return cmp.Compare(a.Tool, b.Tool) })
	return out, nil
}

var fieldMapKeys = []string{"recipient", "url", "host"}

func checkFieldMap(fm map[string]string) error {
	for k, ptr := range fm {
		if !slices.Contains(fieldMapKeys, k) {
			return fmt.Errorf("%w: field_map key %q (want one of %s)", core.ErrInvalid, k, strings.Join(fieldMapKeys, ", "))
		}
		if ptr != "" && !strings.HasPrefix(ptr, "/") {
			return fmt.Errorf("%w: field_map.%s %q is not a JSON pointer", core.ErrInvalid, k, ptr)
		}
	}
	return nil
}

// Approve approves each tool's latest definition with the given class, field
// map and privacy flag (or disables the tool), then re-registers the server's
// tools. Everything is validated before anything is written.
func (m *Manager) Approve(ctx context.Context, name string, tools map[string]core.MCPToolApproval, by core.UserID) error {
	s := m.server(name)
	if s == nil {
		return fmt.Errorf("mcp server %q: %w", name, core.ErrNotFound)
	}
	if len(tools) == 0 {
		return fmt.Errorf("%w: no tools to approve", core.ErrInvalid)
	}
	latest, err := m.d.Store.MCP().Pins(ctx, name)
	if err != nil {
		return err
	}
	byTool := make(map[string]*core.MCPPin, len(latest))
	for _, p := range latest {
		byTool[p.Tool] = p
	}
	approve := map[string]core.MCPPin{}
	var disable []string
	detail := map[string]any{}
	for _, tool := range slices.Sorted(maps.Keys(tools)) {
		a := tools[tool]
		p := byTool[tool]
		if p == nil {
			return fmt.Errorf("mcp tool %s/%s: %w", name, tool, core.ErrNotFound)
		}
		if a.Disable {
			disable = append(disable, tool)
			detail[tool] = map[string]any{"disable": true, "hash": p.Hash}
			continue
		}
		se := a.SideEffect
		if se == 0 {
			se = p.SideEffect
		}
		if !se.Valid() {
			return fmt.Errorf("%w: tool %s: invalid side effect %d", core.ErrInvalid, tool, se)
		}
		def, err := parseDefinition(p.Definition)
		if err != nil {
			return fmt.Errorf("tool %s: stored definition: %w", tool, err)
		}
		if err := checkFieldMap(a.FieldMap); err != nil {
			return fmt.Errorf("tool %s: %w", tool, err)
		}
		se = max(se, def.annotationFloor())
		approve[tool] = core.MCPPin{Hash: p.Hash, SideEffect: se, FieldMap: a.FieldMap, Private: a.Private}
		detail[tool] = map[string]any{"hash": p.Hash, "side_effect": se.String(), "private": a.Private, "field_map": a.FieldMap}
	}
	for _, tool := range disable {
		if err := m.d.Store.MCP().DisableTool(ctx, name, tool); err != nil {
			return err
		}
	}
	if len(approve) > 0 {
		if err := m.d.Store.MCP().ApprovePins(ctx, name, approve, by, m.clock.Now()); err != nil {
			return err
		}
	}
	m.record(ctx, "user:"+string(by), "mcp.approve", name, "ok", detail)
	m.syncTools(ctx, s)
	return nil
}

// Reload re-reads the server's config entry, closes its sessions and
// instances and rediscovers it in the background.
func (m *Manager) Reload(ctx context.Context, name string) error {
	m.applyConfig(m.d.Config.Current().MCP.Servers, false)
	s := m.server(name)
	if s == nil {
		return fmt.Errorf("mcp server %q: %w", name, core.ErrNotFound)
	}
	m.closeInstances(s)
	if !m.spawn(func(ctx context.Context) { m.discover(ctx, s) }) {
		return fmt.Errorf("mcp: manager closed: %w", core.ErrConflict)
	}
	return nil
}

func (m *Manager) unregister(name string) {
	m.regMu.Lock()
	defer m.regMu.Unlock()
	m.d.Registry.UnregisterSource(source(name))
}

// refreshBanners raises or clears the mcp_pins and mcp_drifted banners.
func (m *Manager) refreshBanners() {
	var pending int
	var drifted []string
	for _, s := range m.allServers() {
		s.mu.Lock()
		if !s.removed {
			pending += s.pending
			if s.drifted {
				drifted = append(drifted, s.name)
			}
		}
		s.mu.Unlock()
	}
	pins := ""
	if pending == 1 {
		pins = "1 MCP tool is waiting for review."
	} else if pending > 1 {
		pins = fmt.Sprintf("%d MCP tools are waiting for review.", pending)
	}
	drift := ""
	if len(drifted) > 0 {
		drift = fmt.Sprintf("Installed files of MCP server %s changed on disk; it is disabled until reinstalled.", strings.Join(drifted, ", "))
	}
	m.mu.Lock()
	pubPins, pubDrift := pins != m.pinsBanner, drift != m.driftBanner
	m.pinsBanner, m.driftBanner = pins, drift
	m.mu.Unlock()
	if pubPins {
		m.publish(core.BannerData{Level: "warn", Code: "mcp_pins", Text: pins, Action: "/settings/mcp", Clear: pins == ""})
	}
	if pubDrift {
		m.publish(core.BannerData{Level: "error", Code: "mcp_drifted", Text: drift, Action: "/settings/mcp", Clear: drift == ""})
	}
}

func (m *Manager) publish(d core.EventData) {
	if m.d.Bus == nil {
		return
	}
	typ := core.EvBanner
	if _, ok := d.(core.MCPPinData); ok {
		typ = core.EvMCPPinPending
	}
	m.d.Bus.Publish(core.Event{Type: typ, Data: d})
}

func (m *Manager) record(ctx context.Context, actor, action, target, outcome string, detail any) {
	if m.d.Audit == nil {
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		m.log.Error("audit detail", "action", action, "err", err)
		return
	}
	if err := m.d.Audit.Record(ctx, core.AuditEntry{At: m.clock.Now(), Actor: actor, Action: action,
		Target: "mcp:" + target, Outcome: outcome, Detail: raw}); err != nil {
		m.log.Error("audit", "action", action, "err", err)
	}
}
