package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

const (
	// maxExec caps a single exec, matching sandboxd's default
	// BOTHY_SBX_MAX_EXEC.
	maxExec = 30 * time.Minute
	// execGrace is kept free before the caller's deadline so sbx-init's own
	// timeout, its kill-after and the final frame all land first.
	execGrace = 6 * time.Second
)

// service implements core.Sandbox in front of one backend.
type service struct {
	cfg    config.View
	egress core.Egress
	bus    core.Bus
	audit  core.Auditor
	work   string
	run    string
	clock  core.Clock
	log    *slog.Logger

	// auto keeps looking for the sandboxd socket while the backend is none.
	auto       bool
	newBackend func() backend

	healthEvery, retryEvery, reapEvery, quotaEvery time.Duration

	mu       sync.Mutex
	be       backend
	st       core.SandboxStatus
	stSet    bool
	watchers []func(core.SandboxStatus)
	boxes    map[string]*boxState
	usage    map[core.WorkspaceID]int64

	locks keyedMutex
}

type boxState struct {
	spec     *core.BoxSpec // nil for boxes adopted from an earlier gateway process
	lastUsed time.Time
	active   int
}

func newService(d Deps) *service {
	clock := d.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &service{
		cfg: d.Config, egress: d.Egress, bus: d.Bus, audit: d.Audit, work: d.WorkDir, run: d.RunDir,
		clock: clock, log: log.With("component", "sandbox"),
		healthEvery: 15 * time.Second, retryEvery: 3 * time.Second, reapEvery: time.Minute, quotaEvery: 5 * time.Minute,
		boxes: map[string]*boxState{}, usage: map[core.WorkspaceID]int64{},
	}
}

func (s *service) current() (backend, core.SandboxStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.be, s.st
}

func (s *service) Status(context.Context) core.SandboxStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.SelfTest = slices.Clone(s.st.SelfTest)
	return st
}

func (s *service) Watch(fn func(core.SandboxStatus)) {
	s.mu.Lock()
	s.watchers = append(s.watchers, fn)
	s.mu.Unlock()
}

// refresh probes the backend and publishes a changed status.
func (s *service) refresh(ctx context.Context) core.SandboxStatus {
	be, _ := s.current()
	st := be.probe(ctx)
	s.setStatus(st)
	return st
}

func (s *service) setStatus(st core.SandboxStatus) {
	st.CheckedAt = s.clock.Now()
	if st.SelfTest == nil {
		st.SelfTest = []core.SelfTestCheck{}
	}
	s.mu.Lock()
	prev, first := s.st, !s.stSet
	s.st, s.stSet = st, true
	fns := slices.Clone(s.watchers)
	s.mu.Unlock()
	if !first && sameStatus(prev, st) {
		return
	}
	s.publish(prev, st)
	for _, fn := range fns {
		fn(st)
	}
}

func sameStatus(a, b core.SandboxStatus) bool {
	a.CheckedAt, b.CheckedAt = time.Time{}, time.Time{}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

const settingsRoute = "/settings/sandbox"

func (s *service) publish(prev, st core.SandboxStatus) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(core.Event{Type: core.EvSandboxStatus, Data: core.SandboxStatusData{Status: st}})
	banner := func(level, code, text string, clear bool) {
		s.bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: level, Code: code, Text: text,
			Action: settingsRoute, Clear: clear}})
	}
	bad := isolationFailure(st)
	switch {
	case st.Available:
		banner("warn", "no_sandbox", "", true)
		banner("error", "sandbox_not_isolated", "", true)
	case len(bad) > 0:
		banner("warn", "no_sandbox", "", true)
		banner("error", "sandbox_not_isolated", fmt.Sprintf("Sandbox not isolated: exec refused (failed checks: %v).", bad), false)
		if len(isolationFailure(prev)) == 0 && s.audit != nil {
			detail, _ := json.Marshal(map[string]any{"backend": st.Backend, "runtime": st.Runtime, "failed": bad})
			if err := s.audit.Record(context.Background(), core.AuditEntry{Actor: "system", Action: "sandbox.selftest_failed",
				Target: st.Backend, Outcome: "error", Detail: detail}); err != nil {
				s.log.Warn("audit sandbox.selftest_failed", "err", err)
			}
		}
	default:
		banner("error", "sandbox_not_isolated", "", true)
		text := noSandboxText
		if st.Backend != "none" && st.Reason != "" {
			text = "No sandbox: shell, python and MCP stdio are off (" + st.Reason + ")."
		}
		banner("warn", "no_sandbox", text, false)
	}
}

func (s *service) Ensure(ctx context.Context, spec core.BoxSpec) error {
	be, st := s.current()
	if !st.Available {
		return unavailable(st)
	}
	if err := s.validateSpec(&spec); err != nil {
		return err
	}
	if s.egress == nil {
		return fmt.Errorf("sandbox: no egress service: %w", core.ErrNoSandbox)
	}
	unlock := s.locks.lock(spec.Key)
	defer unlock()
	if err := boxDirs(s.work, s.run, spec, be.gid()); err != nil {
		return fmt.Errorf("sandbox: preparing box directories: %w", err)
	}
	sub, err := s.egress.OpenBox(ctx, spec.Key, spec.Egress)
	if err != nil {
		return fmt.Errorf("sandbox: opening the egress socket: %w", err)
	}
	if want := proto.EgressSubpath(spec.Key); sub != want {
		// The box mounts the directory sandboxd derives; a socket anywhere else
		// would leave it without egress.
		s.egress.CloseBox(spec.Key)
		return fmt.Errorf("sandbox: egress socket at %q, boxes mount %q", sub, want)
	}
	if err := be.create(ctx, spec); err != nil {
		return err
	}
	s.mu.Lock()
	b := s.boxes[spec.Key]
	if b == nil {
		b = &boxState{}
		s.boxes[spec.Key] = b
	}
	sp := spec
	b.spec, b.lastUsed = &sp, s.clock.Now()
	s.mu.Unlock()
	return nil
}

func (s *service) validateSpec(spec *core.BoxSpec) error {
	c := s.cfg.Current().Sandbox
	if spec.Profile == "" {
		spec.Profile = core.BoxProfile(c.Profile)
	}
	if spec.Limits == (core.BoxLimits{}) {
		spec.Limits = core.BoxLimits{MemoryMB: c.Limits.MemoryMB, CPUs: c.Limits.CPUs, Pids: c.Limits.Pids}
	}
	switch {
	case !proto.ValidKey(spec.Key):
		return fmt.Errorf("sandbox: %w: box key %q", core.ErrInvalid, spec.Key)
	case !proto.ValidWorkspace(string(spec.Workspace)):
		return fmt.Errorf("sandbox: %w: workspace %q", core.ErrInvalid, spec.Workspace)
	case !proto.ValidMCPTree(spec.MCPTree):
		return fmt.Errorf("sandbox: %w: mcp tree %q", core.ErrInvalid, spec.MCPTree)
	case spec.MCPTreeRW && spec.MCPTree == "":
		return fmt.Errorf("sandbox: %w: MCPTreeRW without MCPTree", core.ErrInvalid)
	case !proto.ValidProfile(string(spec.Profile)):
		return fmt.Errorf("sandbox: %w: profile %q", core.ErrInvalid, spec.Profile)
	}
	return nil
}

// begin marks key busy (not idle-reaped) and returns the release func.
func (s *service) begin(key string) func() {
	s.mu.Lock()
	b := s.boxes[key]
	if b == nil {
		b = &boxState{}
		s.boxes[key] = b
	}
	b.active++
	b.lastUsed = s.clock.Now()
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		b.active--
		b.lastUsed = s.clock.Now()
		s.mu.Unlock()
	}
}

// recreate brings back a box the backend lost, from the spec of its last
// Ensure.
func (s *service) recreate(ctx context.Context, be backend, key string) error {
	s.mu.Lock()
	var spec *core.BoxSpec
	if b := s.boxes[key]; b != nil {
		spec = b.spec
	}
	s.mu.Unlock()
	if spec == nil {
		return fmt.Errorf("sandbox: box %s was never ensured: %w", key, core.ErrNotFound)
	}
	unlock := s.locks.lock(key)
	defer unlock()
	return be.create(ctx, *spec)
}

func (s *service) Exec(ctx context.Context, key string, req core.ExecRequest, onOut func(stream int, b []byte)) (*core.ExecResult, error) {
	be, st := s.current()
	if !st.Available {
		return nil, unavailable(st)
	}
	if !proto.ValidKey(key) {
		return nil, fmt.Errorf("sandbox: %w: box key %q", core.ErrInvalid, key)
	}
	req, err := s.normalizeExec(ctx, req)
	if err != nil {
		return nil, err
	}
	defer s.begin(key)()
	res, err := be.exec(ctx, key, req, onOut)
	if isGone(err) {
		if err := s.recreate(ctx, be, key); err != nil {
			return nil, err
		}
		res, err = be.exec(ctx, key, req, onOut)
	}
	if err != nil && ctx.Err() != nil {
		if res == nil {
			res = &core.ExecResult{}
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.Exit, res.TimedOut = 124, true
			return res, nil
		}
		res.Exit, res.Killed = 137, true
		return res, context.Cause(ctx)
	}
	return res, err
}

func (s *service) normalizeExec(ctx context.Context, req core.ExecRequest) (core.ExecRequest, error) {
	c := s.cfg.Current().Sandbox
	if err := proto.ValidateArgv(req.Argv); err != nil {
		return req, invalid(err)
	}
	cwd, err := proto.CleanCwd(req.Cwd)
	if err != nil {
		return req, invalid(err)
	}
	req.Cwd = cwd
	if err := proto.ValidateEnv(req.Env); err != nil {
		return req, invalid(err)
	}
	if len(req.Stdin) > proto.MaxStdin {
		return req, fmt.Errorf("sandbox: %w: stdin exceeds %d bytes", core.ErrInvalid, proto.MaxStdin)
	}
	if req.Timeout <= 0 {
		req.Timeout = c.ExecTimeout.D()
	}
	req.Timeout = min(req.Timeout, maxExec)
	if dl, ok := ctx.Deadline(); ok {
		if room := time.Until(dl) - execGrace; room < req.Timeout {
			req.Timeout = max(room, time.Second)
		}
	}
	if req.MaxOutput <= 0 {
		req.MaxOutput = c.OutputKB << 10
	}
	req.MaxOutput = min(req.MaxOutput, proto.MaxOutputCap)
	return req, nil
}

func (s *service) Stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error) {
	be, st := s.current()
	if !st.Available {
		return nil, unavailable(st)
	}
	if !proto.ValidKey(key) {
		return nil, fmt.Errorf("sandbox: %w: box key %q", core.ErrInvalid, key)
	}
	if err := proto.ValidateArgv(argv); err != nil {
		return nil, invalid(err)
	}
	if err := proto.ValidateEnv(env); err != nil {
		return nil, invalid(err)
	}
	release := s.begin(key)
	rwc, err := be.stdio(ctx, key, argv, env)
	if isGone(err) {
		if err = s.recreate(ctx, be, key); err == nil {
			rwc, err = be.stdio(ctx, key, argv, env)
		}
	}
	if err != nil {
		release()
		return nil, err
	}
	return &releasingRWC{ReadWriteCloser: rwc, release: release}, nil
}

// releasingRWC keeps the box busy while a stdio stream is open.
type releasingRWC struct {
	io.ReadWriteCloser
	once    sync.Once
	release func()
}

func (r *releasingRWC) Close() error {
	err := r.ReadWriteCloser.Close()
	r.once.Do(r.release)
	return err
}

func (s *service) Kill(ctx context.Context, key string) error {
	be, _ := s.current()
	if be.name() == "none" {
		return core.ErrNoSandbox
	}
	if !proto.ValidKey(key) {
		return fmt.Errorf("sandbox: %w: box key %q", core.ErrInvalid, key)
	}
	return be.kill(ctx, key)
}

func (s *service) Destroy(ctx context.Context, key string) error {
	be, _ := s.current()
	if be.name() == "none" {
		return core.ErrNoSandbox
	}
	if !proto.ValidKey(key) {
		return fmt.Errorf("sandbox: %w: box key %q", core.ErrInvalid, key)
	}
	unlock := s.locks.lock(key)
	defer unlock()
	err := be.destroy(ctx, key)
	if s.egress != nil {
		s.egress.CloseBox(key)
	}
	s.mu.Lock()
	delete(s.boxes, key)
	s.mu.Unlock()
	return err
}

// reapIdle destroys boxes unused for longer than sandbox.idle_timeout, which
// also closes their egress sockets.
func (s *service) reapIdle(ctx context.Context) {
	idle := s.cfg.Current().Sandbox.IdleTimeout.D()
	now := s.clock.Now()
	var keys []string
	s.mu.Lock()
	for k, b := range s.boxes {
		if b.active == 0 && now.Sub(b.lastUsed) > idle {
			keys = append(keys, k)
		}
	}
	s.mu.Unlock()
	for _, k := range keys {
		s.mu.Lock()
		b := s.boxes[k]
		still := b != nil && b.active == 0 && now.Sub(b.lastUsed) > idle // an exec may have started meanwhile
		s.mu.Unlock()
		if !still {
			continue
		}
		if err := s.Destroy(ctx, k); err != nil && !errors.Is(err, core.ErrNoSandbox) {
			s.log.Warn("reaping idle box failed", "key", k, "err", err)
		}
	}
}

// reconcile adopts boxes that an earlier gateway process left running, so
// the idle reaper eventually removes them.
func (s *service) reconcile(ctx context.Context) {
	be, _ := s.current()
	infos, err := be.list(ctx)
	if err != nil {
		s.log.Warn("listing boxes failed", "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bi := range infos {
		if _, ok := s.boxes[bi.Key]; !ok && proto.ValidKey(bi.Key) {
			s.boxes[bi.Key] = &boxState{lastUsed: time.UnixMilli(bi.LastUsed)}
		}
	}
}

// invalidErr marks a proto validation error as core.ErrInvalid without
// repeating "invalid" in the message.
type invalidErr struct{ err error }

func (e invalidErr) Error() string   { return "sandbox: " + e.err.Error() }
func (e invalidErr) Unwrap() []error { return []error{core.ErrInvalid, e.err} }

func invalid(err error) error { return invalidErr{err} }

// keyedMutex serializes work per box key.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyLock{}
	}
	l := k.m[key]
	if l == nil {
		l = &keyLock{}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
