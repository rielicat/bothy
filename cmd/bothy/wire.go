package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/bothyhq/bothy/internal/agent"
	"github.com/bothyhq/bothy/internal/audit"
	"github.com/bothyhq/bothy/internal/auth"
	"github.com/bothyhq/bothy/internal/budget"
	"github.com/bothyhq/bothy/internal/channel"
	"github.com/bothyhq/bothy/internal/channel/discord"
	"github.com/bothyhq/bothy/internal/channel/slack"
	"github.com/bothyhq/bothy/internal/channel/telegram"
	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/doctor"
	"github.com/bothyhq/bothy/internal/egress"
	"github.com/bothyhq/bothy/internal/events"
	"github.com/bothyhq/bothy/internal/httpapi"
	"github.com/bothyhq/bothy/internal/mcp"
	"github.com/bothyhq/bothy/internal/memory"
	"github.com/bothyhq/bothy/internal/policy"
	"github.com/bothyhq/bothy/internal/provider"
	"github.com/bothyhq/bothy/internal/sandbox"
	"github.com/bothyhq/bothy/internal/scheduler"
	"github.com/bothyhq/bothy/internal/skills"
	"github.com/bothyhq/bothy/internal/store"
	"github.com/bothyhq/bothy/internal/tools"
	"github.com/bothyhq/bothy/internal/vault"
	"github.com/bothyhq/bothy/internal/webui"
)

// app holds every constructed component; built in dependency order.
type app struct {
	cfg       *config.Manager
	log       *slog.Logger
	clock     core.Clock
	db        *store.DB
	redactor  *vault.Redactor
	audit     *audit.Writer
	vault     *vault.Vault
	bus       *events.Bus
	auth      *auth.Service
	policy    *policy.Engine
	approvals *policy.Approvals
	egress    *egress.Service
	sandbox   *sandbox.Client
	llm       *provider.Facade
	budget    *budget.Service
	memory    *memory.Service
	skills    *skills.Library
	registry  *tools.Registry
	mcp       *mcp.Manager
	runner    *agent.Runner
	hub       *channel.Hub
	scheduler *scheduler.Service
	doctor    *doctor.Runner
	api       *httpapi.Server
	restart   chan struct{} // POST /api/admin/restart
}

var errRestart = errors.New("restart requested")

func build(ctx context.Context) (*app, error) {
	setUmask() // 007: /data files are created 0600/0700 explicitly; workspace files 0660/2770
	a := &app{clock: core.SystemClock{}, restart: make(chan struct{}, 1)}
	var err error
	dataDir := envOr("BOTHY_STORAGE__DATA_DIR", "/data")
	if a.cfg, err = config.Load(ctx, config.Options{DataDir: dataDir, File: os.Getenv("BOTHY_CONFIG"), Env: os.Environ}); err != nil {
		return nil, err
	}
	c := a.cfg.Current()
	dirs := config.ResolveDirs(c) // "auto" work_dir and keyfile resolved once (§2.1)
	a.redactor = vault.NewRedactor()
	a.log = slog.New(vault.NewSlogHandler(newLogHandler(c.Log), a.redactor))
	a.bus = events.New(events.Options{Ring: 4096, Clock: a.clock})
	if a.db, err = store.Open(ctx, store.Options{Path: filepath.Join(dirs.Data, "bothy.db"), WorkDir: dirs.Work,
		BackupDir: filepath.Join(dirs.Data, "backups"), BackupsKeep: c.Storage.BackupsKeep, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.audit, err = audit.New(audit.Deps{Store: a.db, Redactor: a.redactor, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	a.cfg.SetOnSet(func(r config.ChangeRecord) { auditConfig(ctx, a.audit, r) })
	a.cfg.Watch(func(old, cur *config.Config) { publishConfigChanged(a.bus, a.cfg, old, cur) })
	publishConfigBanners(a.bus, a.cfg.Effective())
	if a.vault, err = vault.New(vault.Deps{Store: a.db, Config: a.cfg, Redactor: a.redactor, Audit: a.audit, Env: os.Environ, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.auth, err = auth.New(auth.Deps{Store: a.db, Config: a.cfg, Audit: a.audit, Bus: a.bus, Redactor: a.redactor, DataDir: dirs.Data, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.policy, a.approvals, err = policy.New(policy.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.egress, err = egress.New(egress.Deps{Config: a.cfg, Store: a.db, Vault: a.vault, Approvals: a.approvals, Policy: a.policy,
		Bus: a.bus, Audit: a.audit, RunDir: dirs.Run, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.sandbox, err = sandbox.New(ctx, sandbox.Deps{Config: a.cfg, Egress: a.egress, Store: a.db, Bus: a.bus, Audit: a.audit,
		WorkDir: dirs.Work, RunDir: dirs.Run, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.llm, err = provider.New(provider.Deps{Config: a.cfg, Vault: a.vault, Egress: a.egress, Env: os.Environ, Clock: a.clock, Log: a.log, Bus: a.bus}); err != nil {
		return nil, err
	}
	if a.budget, err = budget.New(budget.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.memory, err = memory.New(memory.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, DataDir: dirs.Data, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.skills, err = skills.New(skills.Deps{Store: a.db, Config: a.cfg, Audit: a.audit, Bus: a.bus, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	a.registry = tools.NewRegistry(tools.RegistryDeps{Config: a.cfg, Log: a.log})
	if a.mcp, err = mcp.New(mcp.Deps{Config: a.cfg, Store: a.db, Sandbox: a.sandbox, Egress: a.egress, Registry: a.registry,
		Vault: a.vault, Bus: a.bus, Audit: a.audit, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.runner, err = agent.New(agent.Deps{Store: a.db, Config: a.cfg, LLM: a.llm, Tools: a.registry, Policy: a.policy,
		Approvals: a.approvals, Budget: a.budget, Memory: a.memory, Skills: a.skills, Sandbox: a.sandbox, Egress: a.egress,
		Redactor: a.redactor, Bus: a.bus, Audit: a.audit, DataDir: dirs.Data, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.hub, err = channel.New(channel.Deps{Config: a.cfg, Auth: a.auth, Runner: a.runner, Approvals: a.approvals, Budget: a.budget,
		Sandbox: a.sandbox, Store: a.db, Bus: a.bus, Audit: a.audit, Redactor: a.redactor, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	a.runner.SetHub(a.hub) // breaks the runner <-> hub cycle: resumed-run sinks and restart notices
	adapterDeps := func(name string) core.AdapterDeps {
		return core.AdapterDeps{
			Secret: func(ctx context.Context, ref string) (core.Secret, error) {
				return a.vault.Resolve(ctx, ref, "", "channel:"+name) // empty user: global scope only
			},
			HTTP:     func(d core.SystemDest) *http.Client { return a.egress.SystemClient(d) },
			Meta:     a.db.Meta(),
			StateDir: filepath.Join(dirs.Data, "channels", name), // created by adapters that need it (P1)
			Clock:    a.clock,
			Log:      a.log.With("channel", name),
		}
	}
	for _, mk := range []func() (core.Channel, error){
		func() (core.Channel, error) { return telegram.New(c.Channels.Telegram, adapterDeps("telegram")) },
		func() (core.Channel, error) { return discord.New(c.Channels.Discord, adapterDeps("discord")) },
		func() (core.Channel, error) { return slack.New(c.Channels.Slack, adapterDeps("slack")) },
	} {
		ch, err := mk()
		if errors.Is(err, core.ErrNotImplemented) {
			continue // skeleton stubs
		}
		if err != nil {
			return nil, err
		}
		a.hub.Add(ch) // disabled channels report state "disabled"
	}
	if a.scheduler, err = scheduler.New(scheduler.Deps{Store: a.db, Config: a.cfg, Runner: a.runner, Hub: a.hub, Memory: a.memory,
		Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	// Late tool registration: every provider's tools, then builtins. Snapshots are
	// frozen per session, so registration must finish before the API serves.
	// MCP tools register themselves later through Deps.Registry (lazy tools).
	var all []core.Tool
	all = append(all, tools.Builtins(tools.BuiltinDeps{Config: a.cfg, Messenger: a.hub, Sandbox: a.sandbox, Clock: a.clock})...)
	for _, p := range []core.ToolProvider{a.sandbox, a.egress, a.memory, a.skills, a.scheduler} {
		all = append(all, p.Tools()...)
	}
	for _, t := range all {
		if err := a.registry.Register(t); err != nil {
			return nil, err
		}
	}
	checks := doctor.ConfigChecks(a.cfg) // config.errors, config.safe_mode
	for _, p := range []core.CheckProvider{a.db, a.vault, a.auth, a.policy, a.egress, a.sandbox, a.llm, a.memory, a.skills, a.mcp, a.hub} {
		checks = append(checks, p.Checks()...)
	}
	a.doctor = doctor.New(checks)
	a.api, err = httpapi.New(httpapi.Deps{Config: a.cfg, Store: a.db, Auth: a.auth, Vault: a.vault, Bus: a.bus, Runner: a.runner,
		Approvals: a.approvals, Policy: a.policy, LLM: a.llm, Budget: a.budget, Memory: a.memory, Skills: a.skills, MCP: a.mcp,
		Scheduler: a.scheduler, Hub: a.hub, Sandbox: a.sandbox, Egress: a.egress, Doctor: a.doctor, Audit: a.audit,
		WebUI: webui.Handler(), Version: version, DataDir: dirs.Data, WorkDir: dirs.Work, Restart: a.requestRestart,
		Clock: a.clock, Log: a.log})
	return a, err
}

func (a *app) requestRestart() {
	select {
	case a.restart <- struct{}{}:
	default: // one pending request is enough
	}
}

func serve(ctx context.Context, args []string) error {
	a, err := build(ctx)
	if err != nil {
		return err
	}
	if tok, created, err := a.auth.EnsureSetupToken(ctx); err == nil && created {
		printSetupBanner(a.cfg.Current(), tok)
	}
	// Every resolution is forwarded; Resume is a no-op unless the run is parked.
	a.approvals.OnResolved(func(ap *core.Approval) { _ = a.runner.Resume(context.WithoutCancel(ctx), ap) })
	loops := []func(context.Context) error{
		a.audit.Run, a.approvals.Run, a.egress.Run, a.sandbox.Run, a.runner.Run,
		func(ctx context.Context) error { return a.db.RunRetention(ctx, a.cfg) }, a.api.Serve,
	}
	if !a.cfg.Effective().SafeMode { // safe mode: no chat intake, schedules or MCP (§7.2)
		loops = append(loops, a.hub.Run, a.scheduler.Run, a.mcp.Start)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case <-a.restart:
			cancel(errRestart)
		case <-ctx.Done():
		}
	}()
	var wg sync.WaitGroup
	for _, run := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				cancel(err)
			}
		}()
	}
	wg.Wait()
	_ = a.mcp.Close()
	_ = a.audit.Flush(context.Background())
	_ = a.db.Close()
	cause := context.Cause(ctx)
	if errors.Is(cause, errRestart) {
		return reexec() // syscall.Exec of os.Executable(); works in distroless
	}
	if cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}
