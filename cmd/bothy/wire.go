package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rielicat/bothy/internal/agent"
	"github.com/rielicat/bothy/internal/audit"
	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/budget"
	"github.com/rielicat/bothy/internal/channel"
	"github.com/rielicat/bothy/internal/channel/discord"
	"github.com/rielicat/bothy/internal/channel/slack"
	"github.com/rielicat/bothy/internal/channel/telegram"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/doctor"
	"github.com/rielicat/bothy/internal/egress"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/github"
	"github.com/rielicat/bothy/internal/httpapi"
	"github.com/rielicat/bothy/internal/knowledge"
	"github.com/rielicat/bothy/internal/mcp"
	"github.com/rielicat/bothy/internal/memory"
	"github.com/rielicat/bothy/internal/policy"
	"github.com/rielicat/bothy/internal/provider"
	"github.com/rielicat/bothy/internal/release"
	"github.com/rielicat/bothy/internal/sandbox"
	"github.com/rielicat/bothy/internal/scheduler"
	"github.com/rielicat/bothy/internal/skills"
	"github.com/rielicat/bothy/internal/store"
	"github.com/rielicat/bothy/internal/tools"
	"github.com/rielicat/bothy/internal/vault"
	"github.com/rielicat/bothy/internal/webui"
)

// The release module finds a failed run's step through an optional
// interface; this keeps the GitHub connections offering it.
var _ release.StepReader = (*github.Service)(nil)

// app holds every constructed component; built in dependency order.
type app struct {
	cfg       *config.Manager
	dirs      config.Dirs // resolved once at boot (§2.1)
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
	knowledge *knowledge.Service
	skills    *skills.Library
	registry  *tools.Registry
	mcp       *mcp.Manager
	github    *github.Service
	runner    *agent.Runner
	hub       *channel.Hub
	scheduler *scheduler.Service
	release   *release.Service
	doctor    *doctor.Runner
	api       *httpapi.Server
	restart   chan struct{} // POST /api/admin/restart
}

var errRestart = errors.New("restart requested")

func build(ctx context.Context) (*app, error) {
	a, err := buildBase(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.buildRest(ctx); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

// buildBase opens what the offline CLI commands need: config, logging, the
// store, the audit writer, the vault and auth.
func buildBase(ctx context.Context) (*app, error) {
	setUmask() // 007: /data files are created 0600/0700 explicitly; workspace files 0660/2770
	a := &app{clock: core.SystemClock{}, restart: make(chan struct{}, 1)}
	var err error
	dataDir, defaulted := dataDir()
	if a.cfg, err = config.Load(ctx, config.Options{DataDir: dataDir, File: os.Getenv("BOTHY_CONFIG"), Env: os.Environ}); err != nil {
		return nil, dataDirErr(err, dataDir, defaulted)
	}
	c := a.cfg.Current()
	a.dirs = config.ResolveDirs(c) // "auto" work_dir and keyfile resolved once (§2.1)
	dirs := a.dirs
	a.redactor = vault.NewRedactor()
	a.log = slog.New(vault.NewSlogHandler(newLogHandler(c.Log), a.redactor))
	if moved, err := config.MoveLegacyWork(dirs); err != nil {
		a.log.Error("could not move the workspaces to the work volume; copy them by hand", "from", dirs.LegacyWork, "to", dirs.Work, "err", err)
	} else if moved {
		a.log.Warn("moved the workspaces to the work volume", "from", dirs.LegacyWork, "to", dirs.Work)
	}
	a.bus = events.New(events.Options{Ring: 4096, Clock: a.clock})
	if a.db, err = store.Open(ctx, store.Options{Path: filepath.Join(dirs.Data, "bothy.db"), WorkDir: dirs.Work,
		BackupDir: filepath.Join(dirs.Data, "backups"), BackupsKeep: c.Storage.BackupsKeep, Clock: a.clock, Log: a.log}); err != nil {
		return nil, dataDirErr(err, dataDir, defaulted)
	}
	if a.audit, err = audit.New(audit.Deps{Store: a.db, Redactor: a.redactor, Clock: a.clock, Log: a.log}); err != nil {
		a.close()
		return nil, err
	}
	a.cfg.SetOnSet(func(r config.ChangeRecord) { auditConfig(ctx, a.audit, r) })
	a.cfg.Watch(func(old, cur *config.Config) { publishConfigChanged(a.bus, a.cfg, old, cur) })
	publishConfigBanners(a.bus, a.cfg.Effective())
	if a.vault, err = vault.New(vault.Deps{Store: a.db, Config: a.cfg, Redactor: a.redactor, Audit: a.audit, Env: os.Environ, Clock: a.clock, Log: a.log}); err != nil {
		a.close()
		return nil, err
	}
	if a.auth, err = auth.New(auth.Deps{Store: a.db, Config: a.cfg, Audit: a.audit, Bus: a.bus, Redactor: a.redactor, DataDir: dirs.Data, Clock: a.clock, Log: a.log}); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

// close flushes pending audit rows and closes the store; serve's own
// shutdown does the same after its loops end.
func (a *app) close() {
	if a.audit != nil {
		_ = a.audit.Flush(context.Background())
	}
	if a.db != nil {
		_ = a.db.Close()
	}
}

// buildRest constructs the services serve and doctor need on top of the base.
func (a *app) buildRest(ctx context.Context) error {
	c, dirs := a.cfg.Current(), a.dirs
	var err error
	if a.policy, a.approvals, err = policy.New(policy.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	if a.egress, err = egress.New(egress.Deps{Config: a.cfg, Store: a.db, Vault: a.vault, Approvals: a.approvals, Policy: a.policy,
		Bus: a.bus, Audit: a.audit, RunDir: dirs.Run, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	if a.sandbox, err = sandbox.New(ctx, sandbox.Deps{Config: a.cfg, Egress: a.egress, Store: a.db, Bus: a.bus, Audit: a.audit,
		WorkDir: dirs.Work, RunDir: dirs.Run, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	if a.llm, err = provider.New(provider.Deps{Config: a.cfg, Vault: a.vault, Egress: a.egress, Env: os.Environ, Clock: a.clock, Log: a.log, Bus: a.bus,
		Credentials: a.egress.CredentialsClient}); err != nil {
		return err
	}
	if a.budget, err = budget.New(budget.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	if a.memory, err = memory.New(memory.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, DataDir: dirs.Data, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	repos := a.db.GitHub()
	indexed := func(ctx context.Context, repo, sha string) {
		owner, name, _ := strings.Cut(repo, "/")
		if err := repos.SetIndexed(ctx, owner, name, sha); err != nil && !errors.Is(err, core.ErrNotFound) {
			a.log.Warn("knowledge: recording the indexed commit failed", "repo", repo, "err", err)
		}
	}
	if a.knowledge, err = knowledge.New(knowledge.Deps{Repo: a.db.Knowledge(), Store: a.db, Config: a.cfg, LLM: a.llm, Budget: a.budget,
		Redactor: a.redactor, Bus: a.bus, Audit: a.audit, DataDir: dirs.Data, WorkDir: dirs.Work, Clock: a.clock, Log: a.log,
		Indexed: indexed}); err != nil {
		return err
	}
	if a.skills, err = skills.New(skills.Deps{Store: a.db, Config: a.cfg, Audit: a.audit, Bus: a.bus, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	a.registry = tools.NewRegistry(tools.RegistryDeps{Config: a.cfg, Log: a.log})
	if a.mcp, err = mcp.New(mcp.Deps{Config: a.cfg, Store: a.db, Sandbox: a.sandbox, Egress: a.egress, Registry: a.registry,
		Vault: a.vault, Bus: a.bus, Audit: a.audit, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	if a.github, err = github.New(github.Deps{Config: a.cfg, Store: repos, Vault: a.vault, Egress: a.egress, Bus: a.bus,
		Audit: a.audit, DataDir: dirs.Data, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	if a.runner, err = agent.New(agent.Deps{Store: a.db, Config: a.cfg, LLM: a.llm, Tools: a.registry, Policy: a.policy,
		Approvals: a.approvals, Budget: a.budget, Memory: a.memory, Skills: a.skills, Sandbox: a.sandbox, Egress: a.egress,
		Redactor: a.redactor, Bus: a.bus, Audit: a.audit, DataDir: dirs.Data, WorkDir: dirs.Work, Clock: a.clock, Log: a.log,
		TurnContext: []core.TurnContextHook{a.knowledge}}); err != nil {
		return err
	}
	if a.hub, err = channel.New(channel.Deps{Config: a.cfg, Auth: a.auth, Runner: a.runner, Approvals: a.approvals, Budget: a.budget,
		Sandbox: a.sandbox, Store: a.db, Bus: a.bus, Audit: a.audit, Redactor: a.redactor, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return err
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
			return err
		}
		a.hub.Add(ch) // disabled channels report state "disabled"
	}
	if a.scheduler, err = scheduler.New(scheduler.Deps{Store: a.db, Config: a.cfg, Runner: a.runner, Hub: a.hub, Memory: a.memory,
		Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	// Deploys of repositories on a GitHub connection's allowlist use that
	// connection; releases.github_token serves the others.
	if a.release, err = release.New(release.Deps{Config: a.cfg, Store: a.db, Deploys: a.db.Releases(), Vault: a.vault,
		HTTP: a.egress.SystemClient, Connections: a.github, Messenger: a.hub, Bus: a.bus, Audit: a.audit, Redactor: a.redactor, Clock: a.clock, Log: a.log}); err != nil {
		return err
	}
	// Late tool registration: every provider's tools, then builtins. Snapshots are
	// frozen per session, so registration must finish before the API serves.
	// MCP tools register themselves later through Deps.Registry (lazy tools).
	var all []core.Tool
	all = append(all, tools.Builtins(tools.BuiltinDeps{Config: a.cfg, Messenger: a.hub, Sandbox: a.sandbox, Clock: a.clock})...)
	for _, p := range []core.ToolProvider{a.sandbox, a.egress, a.memory, a.skills, a.scheduler, a.github, a.knowledge, a.release} {
		all = append(all, p.Tools()...)
	}
	for _, t := range all {
		if err := a.registry.Register(t); err != nil {
			return err
		}
	}
	checks := doctor.ConfigChecks(a.cfg) // config.errors, config.safe_mode
	for _, p := range []core.CheckProvider{a.db, a.vault, a.auth, a.policy, a.egress, a.sandbox, a.llm, a.memory, a.skills, a.mcp, a.hub, a.github, a.release} {
		checks = append(checks, p.Checks()...)
	}
	a.doctor = doctor.New(checks)
	a.api, err = httpapi.New(httpapi.Deps{Config: a.cfg, Store: a.db, Auth: a.auth, Vault: a.vault, Bus: a.bus, Runner: a.runner,
		Approvals: a.approvals, Policy: a.policy, Tools: a.registry, LLM: a.llm, Budget: a.budget, Memory: a.memory, Skills: a.skills, MCP: a.mcp, GitHub: a.github,
		Scheduler: a.scheduler, Hub: a.hub, Sandbox: a.sandbox, Egress: a.egress, Doctor: a.doctor, Audit: a.audit, Deploys: a.db.Releases(),
		WebUI: webui.Handler(), Version: version, DataDir: dirs.Data, WorkDir: dirs.Work, Restart: a.requestRestart,
		Clock: a.clock, Log: a.log, Knowledge: a.knowledge})
	return err
}

func (a *app) requestRestart() {
	select {
	case a.restart <- struct{}{}:
	default: // one pending request is enough
	}
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: bothy serve\n\nRuns the gateway. Settings come from <data>/bothy.yaml and BOTHY_* variables;\nthe data directory is $BOTHY_STORAGE__DATA_DIR (default /data).\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errUsage
	}
	setMemoryLimit()
	a, err := build(ctx)
	if err != nil {
		return err
	}
	c := a.cfg.Current()
	switch tok, created, err := a.auth.EnsureSetupToken(ctx); {
	case err != nil:
		a.log.Error("setup token", "err", err)
	case created:
		printSetupBanner(os.Stderr, c, tok)
	default:
		if pending, err := a.auth.NeedsSetup(ctx); err == nil && pending {
			printSetupPending(os.Stderr, c)
		}
	}
	a.log.Info("gateway starting", "listen", c.Server.Listen, "url", baseURL(c), "version", version)
	// Every resolution is forwarded; Resume is a no-op unless the run is parked.
	a.approvals.OnResolved(func(ap *core.Approval) { _ = a.runner.Resume(context.WithoutCancel(ctx), ap) })
	// Before any intake: a run submitted while recovery is pending would be
	// marked interrupted as a leftover of the previous process.
	if err := a.runner.Recover(ctx); err != nil {
		a.log.Error("boot recovery", "err", err)
	}
	loops := []func(context.Context) error{
		a.audit.Run, a.approvals.Run, a.egress.Run, a.sandbox.Run, a.runner.Run,
		func(ctx context.Context) error { return a.db.RunRetention(ctx, a.cfg) }, a.api.Serve, a.knowledge.Run,
	}
	if !a.cfg.Effective().SafeMode { // safe mode: no chat intake, schedules or MCP (§7.2)
		loops = append(loops, a.hub.Run, a.scheduler.Run, a.mcp.Start, a.release.Run)
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
