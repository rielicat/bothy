package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/doctor"
)

type Deps struct {
	Config    config.Admin
	Store     core.Store
	Auth      core.Auth
	Vault     core.Vault
	Bus       core.Bus
	Runner    core.Runner
	Approvals core.Approvals
	Policy    core.Policy
	LLM       core.LLM
	Budget    core.Budget
	Memory    core.Memory
	Skills    core.Skills
	MCP       core.MCP
	Scheduler core.Scheduler
	Hub       core.ChannelHub
	Sandbox   core.Sandbox
	Egress    core.Egress
	Doctor    *doctor.Runner
	Audit     core.Auditor
	WebUI     http.Handler
	Version   string
	DataDir   string // persona file (agent.persona_file), backups
	WorkDir   string // uploads and workspace downloads (os.Root under <WorkDir>/ws/<id>)
	Restart   func() // POST /api/admin/restart: asks cmd/bothy to drain and re-exec
	Clock     core.Clock
	Log       *slog.Logger
}

type Server struct {
	d       Deps
	log     *slog.Logger
	clock   core.Clock
	boot    time.Time
	getenv  func(string) string
	handler http.Handler
	routes  []route

	// tools plans actions for POST /api/policy/preview. Deps has no tool
	// registry yet (ICR httpapi.Deps.Tools); until it is wired the route
	// answers 503.
	tools core.ToolRegistry

	cop      *http.CrossOriginProtection
	net      netCache
	gateways func() []netip.Addr
	tokens   *limiter // per-token requests per minute
	logins   *limiter // per client address
	claims   *limiter // global: setup claims
	sse      *sseTracker
	// ssePing and sseRecheck pace keep-alives and credential re-checks of
	// open streams; tests shorten them.
	ssePing    time.Duration
	sseRecheck time.Duration
	jobs       *jobs // background compactions and MCP installs, one per key

	shutdownOnce sync.Once
	shutdown     chan struct{} // closed when Serve begins shutting down; ends SSE streams
}

func New(d Deps) (*Server, error) {
	if d.Config == nil {
		d.Config = staticAdmin{config.Static(config.Default())}
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		d:          d,
		log:        d.Log,
		clock:      d.Clock,
		boot:       d.Clock.Now(),
		getenv:     os.Getenv,
		cop:        http.NewCrossOriginProtection(),
		gateways:   defaultGateways,
		tokens:     newLimiter(d.Clock),
		logins:     newLimiter(d.Clock),
		claims:     newLimiter(d.Clock),
		sse:        newSSETracker(),
		ssePing:    15 * time.Second,
		sseRecheck: time.Minute,
		jobs:       newJobs(),
		shutdown:   make(chan struct{}),
	}
	s.routes = s.routeTable()
	s.handler = s.build()
	return s, nil
}

// Handler is the full middleware-wrapped mux (used by tests and Serve).
func (s *Server) Handler() http.Handler { return s.handler }

// Serve listens on server.listen (TLS when configured) until ctx is done, then
// shuts down gracefully within 10s.
func (s *Server) Serve(ctx context.Context) error {
	c := s.cfg().Server
	srv := &http.Server{
		Addr:              c.Listen,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(s.stopStreams)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", c.Listen)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		if c.TLS.CertFile != "" && c.TLS.KeyFile != "" {
			errc <- srv.ServeTLS(ln, c.TLS.CertFile, c.TLS.KeyFile)
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) stopStreams() { s.shutdownOnce.Do(func() { close(s.shutdown) }) }

func (s *Server) cfg() *config.Config { return s.d.Config.Current() }

func (s *Server) now() time.Time { return s.clock.Now() }

func (s *Server) safeMode() bool { return s.d.Config.Effective().SafeMode }

// audit records an entry for the acting user; failures are logged. The
// action has already happened, so a client hanging up must not cancel its
// record.
func (s *Server) audit(r *http.Request, a core.Actor, action, target, outcome string, detail map[string]any) {
	if s.d.Audit == nil {
		return
	}
	e := core.AuditEntry{At: s.now(), Actor: actorString(a), Action: action, Target: target, Outcome: outcome,
		IP: clientIP(r), Detail: json.RawMessage("{}")}
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			e.Detail = b
		}
	}
	if err := s.d.Audit.Record(context.WithoutCancel(r.Context()), e); err != nil {
		s.log.Error("audit record failed", "action", action, "err", err)
	}
}

func actorString(a core.Actor) string {
	switch {
	case a.Via == "token" && a.TokenID != "":
		return "token:" + a.TokenID
	case a.User != "":
		return "user:" + string(a.User)
	}
	return "system"
}

func byString(a core.Actor) string { return "user:" + string(a.User) }

// staticAdmin adapts a fixed config to config.Admin for servers built
// without a manager (tests, tools).
type staticAdmin struct{ config.View }

func (staticAdmin) Effective() config.Effective { return config.Effective{} }
func (staticAdmin) Set(context.Context, map[string]json.RawMessage, string) ([]string, error) {
	return nil, core.ErrNotImplemented
}
func (staticAdmin) Reset(context.Context, string, string) error { return core.ErrNotImplemented }
func (staticAdmin) Schema() json.RawMessage                     { return json.RawMessage("{}") }
