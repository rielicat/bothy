package sandbox

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Config  config.View
	Egress  core.Egress
	Store   core.Store
	Bus     core.Bus
	Audit   core.Auditor
	WorkDir string // /work
	RunDir  string // /run/bothy
	Clock   core.Clock
	Log     *slog.Logger
}

// Client implements core.Sandbox, core.ToolProvider and core.CheckProvider.
type Client struct {
	core.Sandbox
	svc   *service
	tools []core.Tool
}

// New probes backends in order sandboxd -> bwrap -> none (per sandbox.backend).
func New(ctx context.Context, d Deps) (*Client, error) {
	s := newService(d)
	s.be = s.selectBackend(ctx)
	s.refresh(ctx)
	return &Client{Sandbox: s, svc: s, tools: []core.Tool{newShell(d.Config), newPython(d.Config)}}, nil
}

func (s *service) selectBackend(ctx context.Context) backend {
	c := s.cfg.Current().Sandbox
	sandboxd := func() backend { return newSandboxdBackend(c.Socket) }
	switch c.Backend {
	case "none":
		return noneBackend{reason: "sandbox.backend is none. " + noSandboxText, fix: "set sandbox.backend: auto"}
	case "sandboxd":
		return sandboxd()
	case "bwrap":
		return newBwrapBackend(ctx, s.work, s.run)
	}
	// auto: a sandboxd socket wins; then bwrap on Linux; else none, while Run
	// keeps watching for the socket (compose may start sandboxd later).
	s.auto, s.newBackend = true, sandboxd
	if isSocket(c.Socket) {
		return sandboxd()
	}
	if runtime.GOOS == "linux" {
		if b := newBwrapBackend(ctx, s.work, s.run); b.st.Available {
			return b
		}
	}
	return noneBackend{reason: noSandboxText, fix: composeFix}
}

func isSocket(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// Run watches sandboxd health, reaps idle boxes and scans workspace quotas.
func (c *Client) Run(ctx context.Context) error { return c.svc.loop(ctx) }

func (c *Client) Tools() []core.Tool { return c.tools }

func (c *Client) Checks() []core.Check { return checks(c.svc) }

func (s *service) loop(ctx context.Context) error {
	s.reconcile(ctx)
	s.scanQuota()
	health := time.NewTimer(s.nextHealth())
	defer health.Stop()
	reap := time.NewTicker(s.reapEvery)
	defer reap.Stop()
	quota := time.NewTicker(s.quotaEvery)
	defer quota.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-health.C:
			s.maybeSwitch()
			s.refresh(ctx)
			s.syncMemberBanners(ctx)
			health.Reset(s.nextHealth())
		case <-reap.C:
			s.reapIdle(ctx)
		case <-quota.C:
			s.scanQuota()
		}
	}
}

func (s *service) nextHealth() time.Duration {
	if _, st := s.current(); st.Available {
		return s.healthEvery
	}
	return s.retryEvery
}

// maybeSwitch moves an auto-mode gateway from none to sandboxd once the
// sidecar's socket appears.
func (s *service) maybeSwitch() {
	if !s.auto {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.be.name() == "none" && isSocket(s.cfg.Current().Sandbox.Socket) {
		s.be = s.newBackend()
		s.log.Info("sandboxd socket appeared; using the sandboxd backend")
	}
}
