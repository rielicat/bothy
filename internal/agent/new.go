package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store     core.Store
	Config    config.View
	LLM       core.LLM
	Tools     core.ToolRegistry
	Policy    core.Policy
	Approvals core.Approvals
	Budget    core.Budget
	Memory    core.Memory
	Skills    core.Skills
	Sandbox   core.Sandbox
	Egress    core.Egress
	Redactor  core.Redactor
	Bus       core.Bus
	Audit     core.Auditor
	DataDir   string // SOUL.md
	WorkDir   string // workspaces
	Clock     core.Clock
	Log       *slog.Logger
	// TurnContext hooks add blocks (knowledge notes) to each user turn's
	// <turn-context>, after the pins and in this order.
	TurnContext []core.TurnContextHook
}

// Meta keys owned by the runner.
const (
	metaPaused       = "paused"
	metaConstitution = "constitution_version"
	metaSpotlightKey = "agent.spotlight_key"
	// metaWorkspaceUntrusted + workspace id is "1" once untrusted content
	// may have been written to that workspace.
	metaWorkspaceUntrusted = "agent.workspace_untrusted."
)

const (
	workerIdle     = 5 * time.Minute
	abandonAfter   = 5 * time.Second
	drainTimeout   = 10 * time.Second
	shutdownWait   = 5 * time.Second
	stopKillWithin = 2 * time.Second
	// chatNoticeWithin bounds a notice sent straight to a chat through the
	// hub's Messenger, which works without the hub's intake (as at boot).
	chatNoticeWithin = 5 * time.Second
)

var (
	errShutdown     = errors.New("gateway shutting down")
	errShuttingDown = fmt.Errorf("agent: %w: %w", core.ErrPaused, errShutdown)
)

// Runner implements core.Runner.
type Runner struct {
	d     Deps
	log   *slog.Logger
	clock core.Clock

	hubMu sync.RWMutex
	hub   core.ChannelHub

	initMu  sync.Mutex
	inited  bool
	spotKey []byte

	recoverMu sync.Mutex
	recovered bool

	paused  atomic.Bool
	closing atomic.Bool

	mu      sync.Mutex
	workers map[core.SessionID]*worker
	users   map[core.UserID]*semaphore
	prefix  map[core.SessionID]prefixMark
	wsMarks map[core.WorkspaceID]bool // workspaces known to hold untrusted content
	// drafts holds the titles still made of a chat's first words, so a task
	// that ends before the generated title (parked, failed, over budget) leaves
	// the naming to the next one. Lost on restart: the draft then stays.
	drafts map[core.SessionID]string
	global  *semaphore
	loops   sync.WaitGroup // worker goroutines
	bg      sync.WaitGroup // titles, notices
	bgCtx   context.Context
	bgStop  context.CancelFunc // cancels bg work when shutdown stops waiting

	// beforeIdle runs on a worker between finishing a job and clearing its
	// state; tests use it to widen that window.
	beforeIdle func()
	// refineParallel bounds a refinement's calls in flight (0 = the refine
	// package default); tests set 1 to make the call order deterministic.
	refineParallel int
}

type prefixMark struct {
	epoch int
	hash  string
}

var _ core.Runner = (*Runner)(nil)

// New validates the dependencies and writes the default persona file when it
// is missing. It does no database I/O: meta state is loaded on first use.
func New(d Deps) (*Runner, error) {
	switch {
	case d.Store == nil, d.Config == nil, d.LLM == nil, d.Tools == nil, d.Policy == nil,
		d.Approvals == nil, d.Budget == nil, d.Bus == nil:
		return nil, errors.New("agent: Store, Config, LLM, Tools, Policy, Approvals, Budget and Bus are required")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	r := &Runner{
		d:       d,
		log:     d.Log,
		clock:   d.Clock,
		workers: map[core.SessionID]*worker{},
		users:   map[core.UserID]*semaphore{},
		prefix:  map[core.SessionID]prefixMark{},
		wsMarks: map[core.WorkspaceID]bool{},
		drafts:  map[core.SessionID]string{},
	}
	r.bgCtx, r.bgStop = context.WithCancel(context.Background())
	r.global = newSemaphore(func() int { return r.cfg().Agent.MaxConcurrentRuns })
	// Without the file the embedded default persona is used, so boot goes on.
	if err := r.ensurePersona(); err != nil {
		r.log.Warn("default persona not written", "err", err)
	}
	return r, nil
}

// SetHub is called once by cmd/bothy after the hub exists (the hub depends on
// the runner). The runner uses it for sinks of resumed parked runs
// (Hub.StreamTo) and for restart notices (Messenger.Send); before SetHub, or
// with a nil hub in tests, those deliveries are skipped.
func (r *Runner) SetHub(h core.ChannelHub) {
	r.hubMu.Lock()
	r.hub = h
	r.hubMu.Unlock()
}

func (r *Runner) hubOrNil() core.ChannelHub {
	r.hubMu.RLock()
	defer r.hubMu.RUnlock()
	return r.hub
}

func (r *Runner) cfg() *config.Config { return r.d.Config.Current() }

func (r *Runner) now() time.Time { return r.clock.Now() }

func (r *Runner) redact(s string) string {
	if r.d.Redactor == nil || s == "" {
		return s
	}
	return r.d.Redactor.Redact(s)
}

// ensureInit loads the kill switch, records the constitution version and
// creates the spotlight key. It retries on the next call after a failure.
func (r *Runner) ensureInit(ctx context.Context) error {
	r.initMu.Lock()
	defer r.initMu.Unlock()
	if r.inited {
		return nil
	}
	meta := r.d.Store.Meta()
	v, err := meta.Get(ctx, metaPaused)
	switch {
	case err == nil:
		r.paused.Store(v == "1")
	case !errors.Is(err, core.ErrNotFound):
		return fmt.Errorf("agent: read meta.paused: %w", err)
	}
	key, err := meta.Get(ctx, metaSpotlightKey)
	switch {
	case errors.Is(err, core.ErrNotFound):
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("agent: spotlight key: %w", err)
		}
		key = hex.EncodeToString(b)
		if err := meta.Set(ctx, metaSpotlightKey, key); err != nil {
			return fmt.Errorf("agent: store spotlight key: %w", err)
		}
	case err != nil:
		return fmt.Errorf("agent: read spotlight key: %w", err)
	}
	if r.spotKey, err = hex.DecodeString(key); err != nil || len(r.spotKey) < 16 {
		return fmt.Errorf("agent: malformed meta.%s", metaSpotlightKey)
	}
	prev, err := meta.Get(ctx, metaConstitution)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("agent: read constitution version: %w", err)
	}
	if prev != constitutionVersion {
		if err := meta.Set(ctx, metaConstitution, constitutionVersion); err != nil {
			return fmt.Errorf("agent: store constitution version: %w", err)
		}
		if prev != "" {
			r.log.Info("constitution updated", "from", prev, "to", constitutionVersion)
		}
	}
	r.inited = true
	return nil
}

// personaName resolves agent.persona_file inside DataDir; it refuses paths
// that would leave the data directory.
func (r *Runner) personaName() (string, bool) {
	name := r.cfg().Agent.PersonaFile
	if name == "" {
		name = "SOUL.md"
	}
	if r.d.DataDir == "" || !filepath.IsLocal(name) {
		return "", false
	}
	return name, true
}

func (r *Runner) ensurePersona() error {
	name, ok := r.personaName()
	if !ok {
		return nil
	}
	root, err := os.OpenRoot(r.d.DataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: open data dir: %w", err)
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: create %s: %w", name, err)
	}
	if _, err := f.WriteString(defaultSoul); err != nil {
		f.Close()
		return fmt.Errorf("agent: write %s: %w", name, err)
	}
	return f.Close()
}

const personaMax = 16 << 10

// persona reads the persona file, falling back to the embedded default.
func (r *Runner) persona() string {
	name, ok := r.personaName()
	if !ok {
		return defaultSoul
	}
	root, err := os.OpenRoot(r.d.DataDir)
	if err != nil {
		return defaultSoul
	}
	defer root.Close()
	b, err := root.ReadFile(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Warn("persona unreadable, using the default", "file", name, "err", err)
		}
		return defaultSoul
	}
	if len(b) > personaMax {
		b = b[:personaMax]
	}
	return string(b)
}

func (r *Runner) auditf(ctx context.Context, actor, action, target, outcome string, sess core.SessionID, run core.RunID, detail any) {
	if r.d.Audit == nil {
		return
	}
	var raw json.RawMessage
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			r.log.Error("audit detail", "action", action, "err", err)
			return
		}
		raw = b
	}
	e := core.AuditEntry{At: r.now(), Actor: actor, Action: action, Target: target, Outcome: outcome, Session: sess, Run: run, Detail: raw}
	if err := r.d.Audit.Record(ctx, e); err != nil {
		r.log.Error("audit", "action", action, "err", err)
	}
}

// Recover performs boot recovery once: runs left open by the previous process
// become interrupted and resolved parked runs resume. cmd/bothy calls it
// before any intake starts, because a run submitted first would be taken for
// a leftover and interrupted; Run calls it too.
func (r *Runner) Recover(ctx context.Context) error {
	if err := r.ensureInit(ctx); err != nil {
		return err
	}
	r.recoverMu.Lock()
	defer r.recoverMu.Unlock()
	if r.recovered {
		return nil
	}
	r.recovered = true
	return r.recover(ctx)
}

// Run performs boot recovery (see Recover) and reaps idle session workers
// until ctx is done; then drains for up to 10s.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.ensureInit(ctx); err != nil {
		return err
	}
	if err := r.Recover(ctx); err != nil {
		r.log.Error("boot recovery", "err", err)
	}
	<-ctx.Done()
	r.shutdown()
	return nil
}

// shutdown stops intake, lets runs reach a step boundary for drainTimeout,
// then cancels the rest; their status becomes interrupted.
func (r *Runner) shutdown() {
	defer r.bgStop()
	r.mu.Lock()
	r.closing.Store(true)
	for _, w := range r.workers {
		w.signal()
	}
	r.mu.Unlock()
	deadline := time.NewTimer(drainTimeout)
	defer deadline.Stop()
	idle := make(chan struct{})
	go func() {
		r.loops.Wait()
		r.bg.Wait()
		close(idle)
	}()
	select {
	case <-idle:
		return
	case <-deadline.C:
	}
	for _, rs := range r.activeRuns() {
		rs.cancel(errShutdown)
		rs.stopPostRun()
	}
	r.bgStop()
	wait := time.NewTimer(shutdownWait)
	defer wait.Stop()
	select {
	case <-idle:
	case <-wait.C:
		r.log.Warn("runs did not stop in time; abandoning them")
	}
}

func (r *Runner) recover(ctx context.Context) error {
	runs, err := r.d.Store.Runs().MarkInterrupted(ctx, r.now())
	if err != nil {
		return fmt.Errorf("mark interrupted: %w", err)
	}
	for _, run := range runs {
		if err := r.d.Approvals.CancelRun(ctx, run.ID); err != nil {
			r.log.Warn("cancel approvals of interrupted run", "run", run.ID, "err", err)
		}
		r.notice(ctx, run.Session, run.Principal, run.ID, interruptedNotice, nil)
		sess, err := r.d.Store.Sessions().Get(ctx, core.SystemViewer(), run.Session)
		if err != nil || sess.Chat == nil {
			continue
		}
		if h := r.hubOrNil(); h != nil {
			if _, err := h.Send(ctx, *sess.Chat, interruptedNotice); err != nil {
				r.log.Warn("restart notice", "session", run.Session, "err", err)
			}
		}
	}
	parked, err := r.d.Store.Runs().Parked(ctx)
	if err != nil {
		return fmt.Errorf("parked runs: %w", err)
	}
	for _, run := range parked {
		if run.ParkedOn == "" {
			continue
		}
		a, err := r.d.Approvals.Get(ctx, core.SystemViewer(), run.ParkedOn)
		if err != nil {
			r.log.Warn("approval of parked run", "run", run.ID, "err", err)
			continue
		}
		if a.Status != core.ApprovalPending && a.Status != core.ApprovalParked {
			if err := r.Resume(ctx, a); err != nil {
				r.log.Warn("resume parked run at boot", "run", run.ID, "err", err)
			}
		}
	}
	return nil
}
