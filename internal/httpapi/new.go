package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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

type Server struct{ d Deps }

func New(d Deps) (*Server, error) { return &Server{d: d}, nil }

// Handler is the full middleware-wrapped mux (used by tests and Serve). The
// skeleton serves only the health endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return mux
}

// Serve listens on server.listen (TLS when configured) until ctx is done, then
// shuts down gracefully within 10s.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{Addr: s.d.Config.Current().Server.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
