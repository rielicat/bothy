package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// discovered is one tool as a server listed it.
type discovered struct {
	def   *toolDefinition
	canon []byte
	hash  string
}

// conn is an initialised client session plus the tool list it reported.
type conn struct {
	sess     *mcpsdk.ClientSession
	tools    map[string]discovered
	protocol string
	box      string             // stdio box destroyed with the session; "" for HTTP
	cancel   context.CancelFunc // ends the transport's lifetime context
}

// instance is the lazily started runtime session for one (stdio server,
// principal) or (HTTP server, billing user).
type instance struct {
	gate chan struct{} // held while (re)connecting so concurrent first calls share one start

	mu       sync.Mutex
	c        *conn
	lastUsed time.Time
	inflight int
	failures int
	retryAt  time.Time
}

// backoff is the restart delay after consecutive failures: 1s doubling to 60s.
func backoff(failures int) time.Duration {
	return min(time.Second<<min(failures-1, 6), 60*time.Second)
}

func (s *server) instance(key string) *instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst := s.instances[key]
	if inst == nil {
		inst = &instance{gate: make(chan struct{}, 1)}
		s.instances[key] = inst
	}
	return inst
}

func runtimeBox(server string, p core.PrincipalID) string {
	return core.DerivedID(core.PrefixBox, "mcp:"+server+":"+string(p))
}

func discoverBox(server string) string { return core.DerivedID(core.PrefixBox, "mcp-discover:"+server) }

func installBox(server string) string { return core.DerivedID(core.PrefixBox, "mcp-install:"+server) }

// vaultUser is whose vault scope resolves an HTTP server's headers for a run:
// the billing user, or the global scope only for space principals, which have
// no vault scope.
func vaultUser(env *core.ToolEnv) core.UserID {
	if env.Principal.Kind == core.PrincipalSpace {
		return ""
	}
	return env.User
}

// acquire returns the caller's runtime session, starting it when needed, and
// a release func the caller must run when its call is done.
func (m *Manager) acquire(ctx context.Context, s *server, env *core.ToolEnv) (*conn, *instance, func(), error) {
	if m.ctx.Err() != nil {
		return nil, nil, nil, fmt.Errorf("mcp server %s: manager closed: %w", s.name, core.ErrConflict)
	}
	var key string
	var connect func(context.Context) (*conn, error)
	switch s.spec.transport {
	case "stdio":
		p := env.Principal.ID
		ws := core.WorkspaceID("")
		if s.spec.workspace {
			ws = env.Principal.Workspace
		}
		key = "p:" + string(p)
		connect = func(ctx context.Context) (*conn, error) { return m.connectStdio(ctx, s, runtimeBox(s.name, p), ws, p) }
	default:
		u := vaultUser(env)
		key = "u:" + string(u)
		connect = func(ctx context.Context) (*conn, error) { return m.connectHTTP(ctx, s, u) }
	}
	inst := s.instance(key)
	select {
	case inst.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, nil, ctx.Err()
	}
	defer func() { <-inst.gate }()
	release := func() {
		inst.mu.Lock()
		inst.inflight--
		inst.lastUsed = m.clock.Now()
		inst.mu.Unlock()
	}
	inst.mu.Lock()
	if c := inst.c; c != nil {
		inst.inflight++
		inst.mu.Unlock()
		return c, inst, release, nil
	}
	wait := inst.retryAt.Sub(m.clock.Now())
	inst.mu.Unlock()
	if wait > 0 {
		return nil, nil, nil, fmt.Errorf("mcp server %s is restarting after a failure; try again in %s", s.name, wait.Round(time.Second))
	}
	c, err := connect(ctx)
	if err == nil {
		err = m.adopt(s, inst, c)
	}
	if err != nil {
		inst.mu.Lock()
		if ctx.Err() == nil {
			inst.failures++
			inst.retryAt = m.clock.Now().Add(backoff(inst.failures))
		}
		inst.mu.Unlock()
		return nil, nil, nil, err
	}
	return c, inst, release, nil
}

// adopt makes c the instance's session for one caller, unless the manager
// closed or the server was removed while it connected. Holding m.mu while
// storing it means Close either sees the session or the caller sees closed.
func (m *Manager) adopt(s *server, inst *instance, c *conn) error {
	m.mu.Lock()
	if m.closed || s.isRemoved() {
		m.mu.Unlock()
		m.closeConn(c)
		return fmt.Errorf("mcp server %s was shut down", s.name)
	}
	m.watchers.Add(1)
	inst.mu.Lock()
	inst.c, inst.failures, inst.lastUsed = c, 0, m.clock.Now()
	inst.inflight++
	inst.mu.Unlock()
	m.mu.Unlock()
	go m.watch(s, inst, c)
	return nil
}

// watch notices a session ending on its own (server exit, box killed, HTTP
// session lost) and schedules a restart with backoff.
func (m *Manager) watch(s *server, inst *instance, c *conn) {
	defer m.watchers.Done()
	_ = c.sess.Wait()
	inst.mu.Lock()
	unexpected := inst.c == c
	if unexpected {
		inst.c = nil
		inst.failures++
		inst.retryAt = m.clock.Now().Add(backoff(inst.failures))
	}
	inst.mu.Unlock()
	if unexpected {
		if m.ctx.Err() == nil {
			m.log.Warn("mcp session ended unexpectedly", "server", s.name)
		}
		m.closeConn(c)
	}
}

// drop discards a session a call found broken; like a crash noticed by
// watch, it delays the restart.
func (m *Manager) drop(inst *instance, c *conn) {
	inst.mu.Lock()
	mine := inst.c == c
	if mine {
		inst.c = nil
		inst.failures++
		inst.retryAt = m.clock.Now().Add(backoff(inst.failures))
	}
	inst.mu.Unlock()
	if mine {
		m.closeConn(c)
	}
}

func (m *Manager) closeConn(c *conn) {
	_ = c.sess.Close()
	c.cancel()
	if c.box != "" {
		m.destroyBox(c.box)
	}
}

func (m *Manager) destroyBox(box string) {
	if err := m.removeBox(box); err != nil && !errors.Is(err, core.ErrNoSandbox) {
		m.log.Warn("destroy mcp box", "box", box, "err", err)
	}
}

// freshBox removes whatever box is left under key (after a failed Destroy or
// a crash) before Ensure creates it. Ensure is idempotent per key, so it would
// otherwise reuse a box whose /opt/mcp is an older or unverified tree.
func (m *Manager) freshBox(ctx context.Context, key string) error {
	if err := m.d.Sandbox.Destroy(ctx, key); err != nil && !errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("remove stale box %s: %w", key, err)
	}
	return nil
}

func (m *Manager) removeBox(box string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.d.Sandbox.Destroy(ctx, box); err != nil {
		return fmt.Errorf("destroy box %s: %w", box, err)
	}
	return nil
}

func (m *Manager) closeInstances(s *server) {
	s.mu.Lock()
	insts := slices.Collect(maps.Values(s.instances))
	s.mu.Unlock()
	for _, inst := range insts {
		inst.mu.Lock()
		c := inst.c
		inst.c = nil
		inst.mu.Unlock()
		if c != nil {
			m.closeConn(c)
		}
	}
}

func (m *Manager) reaper(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := m.clock.Now()
			m.reapIdle(now)
			m.retryDiscovery(now)
		}
	}
}

// reapIdle closes runtime sessions (and stdio boxes) idle for m.idle.
func (m *Manager) reapIdle(now time.Time) {
	for _, s := range m.allServers() {
		s.mu.Lock()
		insts := slices.Collect(maps.Values(s.instances))
		s.mu.Unlock()
		for _, inst := range insts {
			inst.mu.Lock()
			c := inst.c
			if c != nil && inst.inflight == 0 && now.Sub(inst.lastUsed) >= m.idle {
				inst.c = nil
			} else {
				c = nil
			}
			inst.mu.Unlock()
			if c != nil {
				m.log.Debug("closing idle mcp session", "server", s.name)
				m.closeConn(c)
			}
		}
	}
}

// dial establishes a session over the transport mk builds. The transport gets
// a context that lives as long as the session, because an SSE stream or a
// stdio upgrade dies with the context it was opened with; setup itself is
// bounded by ctx and connectTimeout.
func (m *Manager) dial(ctx context.Context, mk func(life context.Context) (mcpsdk.Transport, error)) (*conn, error) {
	life, cancel := context.WithCancel(m.ctx)
	ctx, stop := context.WithTimeout(ctx, connectTimeout)
	defer stop()
	detach := context.AfterFunc(ctx, cancel)
	t, err := mk(life)
	if err != nil {
		cancel()
		return nil, err
	}
	cs, err := m.client.Connect(ctx, lifeTransport{t, life}, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	tools, err := m.listTools(ctx, cs)
	if err == nil && !detach() {
		err = ctx.Err()
	}
	if err != nil {
		_ = cs.Close()
		cancel()
		return nil, err
	}
	return &conn{sess: cs, tools: tools, protocol: cs.InitializeResult().ProtocolVersion, cancel: cancel}, nil
}

// lifeTransport hands the session's lifetime context to the inner
// transport's Connect instead of the setup context Client.Connect passes.
type lifeTransport struct {
	mcpsdk.Transport
	life context.Context
}

func (t lifeTransport) Connect(context.Context) (mcpsdk.Connection, error) {
	return t.Transport.Connect(t.life)
}

func (m *Manager) listTools(ctx context.Context, cs *mcpsdk.ClientSession) (map[string]discovered, error) {
	out := map[string]discovered{}
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		if len(out) == maxToolsPerServer {
			m.log.Warn("mcp server lists too many tools; ignoring the rest", "max", maxToolsPerServer)
			break
		}
		def, canon, hash, err := definitionOf(t)
		if err != nil {
			m.log.Warn("ignoring mcp tool", "tool", t.Name, "err", err)
			continue
		}
		// Every definition is stored as a pin and may reach a model through
		// tool_search, so an oversized one is dropped rather than recorded.
		if len(canon) > maxDefinitionBytes {
			m.log.Warn("ignoring mcp tool with an oversized definition", "tool", t.Name, "bytes", len(canon), "max", maxDefinitionBytes)
			continue
		}
		out[t.Name] = discovered{def: def, canon: canon, hash: hash}
	}
	return out, nil
}

var errNotInstalled = errors.New("not installed")

// connectStdio starts the server's command in box (creating the box first)
// and runs the MCP handshake over the sandbox stdio stream. The command runs
// from /opt/mcp, the read-only install tree, so the relative paths in
// mcp.servers.*.command resolve there; it gets no environment beyond the
// box's own and, in particular, no secrets.
func (m *Manager) connectStdio(ctx context.Context, s *server, box string, ws core.WorkspaceID, p core.PrincipalID) (*conn, error) {
	if !m.d.Sandbox.Status(ctx).Available {
		return nil, fmt.Errorf("mcp server %s: %w", s.name, core.ErrNoSandbox)
	}
	s.mu.Lock()
	in := s.install
	s.mu.Unlock()
	if in == nil {
		return nil, fmt.Errorf("mcp server %s: %w", s.name, errNotInstalled)
	}
	spec := core.BoxSpec{Key: box, Profile: s.spec.profile, Workspace: ws, MCPTree: s.spec.installTree(in.InstallID),
		Limits: m.limits(), Egress: core.BoxEgress{Principal: p, Extra: s.spec.egress, NoGrants: true}}
	if err := m.freshBox(ctx, box); err != nil {
		return nil, fmt.Errorf("mcp server %s: %w", s.name, err)
	}
	if err := m.d.Sandbox.Ensure(ctx, spec); err != nil {
		return nil, fmt.Errorf("mcp server %s: box: %w", s.name, err)
	}
	c, err := m.dial(ctx, func(life context.Context) (mcpsdk.Transport, error) {
		rwc, err := m.d.Sandbox.Stdio(life, box, s.spec.argv(), map[string]string{})
		if err != nil {
			return nil, err
		}
		oc := &onceCloser{ReadWriteCloser: rwc}
		context.AfterFunc(life, func() { _ = oc.Close() })
		return &mcpsdk.IOTransport{Reader: oc, Writer: oc}, nil
	})
	if err != nil {
		m.destroyBox(box)
		return nil, fmt.Errorf("mcp server %s: %w", s.name, err)
	}
	c.box = box
	return c, nil
}

// argv wraps the configured command so it runs from /opt/mcp; pypi installs
// (uv pip install --target) also need the tree on PYTHONPATH, which the
// gateway may not pass as exec env.
func (s *serverSpec) argv() []string {
	script := `cd /opt/mcp && exec "$@"`
	if s.pkg.Ecosystem == "pypi" {
		script = `cd /opt/mcp && export PYTHONPATH=/opt/mcp && exec "$@"`
	}
	return append([]string{"/bin/sh", "-c", script, "mcp-" + s.name}, s.command...)
}

func (m *Manager) limits() core.BoxLimits {
	l := m.d.Config.Current().Sandbox.Limits
	return core.BoxLimits{MemoryMB: l.MemoryMB, CPUs: l.CPUs, Pids: l.Pids}
}

// connectHTTP opens a Streamable HTTP session, falling back to the legacy
// HTTP+SSE transport for servers that predate it. Requests go only through
// the egress system client for the server's exact host:port; header
// references resolve in u's vault scope, then the global one (u == "" means
// global only).
func (m *Manager) connectHTTP(ctx context.Context, s *server, u core.UserID) (*conn, error) {
	if err := checkHost(s.spec.hostPort, s.spec.allowPrivate); err != nil {
		return nil, fmt.Errorf("mcp server %s: %w", s.name, err)
	}
	sys := m.d.Egress.SystemClient(core.SystemDest{Name: source(s.name), Hosts: []string{s.spec.hostPort}, AllowPrivate: s.spec.allowPrivate})
	if sys == nil || sys.Transport == nil {
		// A nil transport would mean http.DefaultTransport, outside every egress rule.
		return nil, fmt.Errorf("mcp server %s: egress returned no client transport", s.name)
	}
	headers := make(map[string]core.Secret, len(s.spec.headers))
	for k, ref := range s.spec.headers {
		if !config.HasSecretRef(ref) {
			headers[k] = core.NewSecret([]byte(ref))
			continue
		}
		v, err := m.d.Vault.Resolve(ctx, ref, u, source(s.name))
		if err != nil {
			return nil, fmt.Errorf("mcp server %s: header %s: %w", s.name, k, err)
		}
		headers[k] = v
	}
	hc := &http.Client{Transport: &headerTransport{base: sys.Transport, headers: headers}, CheckRedirect: sys.CheckRedirect}
	c, err := m.dial(ctx, func(context.Context) (mcpsdk.Transport, error) {
		return &mcpsdk.StreamableClientTransport{Endpoint: s.spec.url, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil
	})
	if err == nil || ctx.Err() != nil {
		return c, wrapServer(s.name, err)
	}
	c, sseErr := m.dial(ctx, func(context.Context) (mcpsdk.Transport, error) {
		return &mcpsdk.SSEClientTransport{Endpoint: s.spec.url, HTTPClient: hc}, nil
	})
	if sseErr != nil {
		return nil, fmt.Errorf("mcp server %s: %w (legacy SSE fallback: %w)", s.name, err, sseErr)
	}
	return c, nil
}

func wrapServer(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("mcp server %s: %w", name, err)
}
