package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/events"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
	"github.com/bothyhq/bothy/internal/testkit/memstore"
)

// view is a config.View whose Set notifies watchers, for hot-reload tests.
type view struct {
	mu       sync.Mutex
	cur      *config.Config
	watchers map[int]func(old, cur *config.Config)
	next     int
}

func (v *view) Current() *config.Config {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cur
}

func (v *view) Watch(fn func(old, cur *config.Config)) func() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.watchers == nil {
		v.watchers = map[int]func(old, cur *config.Config){}
	}
	id := v.next
	v.next++
	v.watchers[id] = fn
	return func() {
		v.mu.Lock()
		delete(v.watchers, id)
		v.mu.Unlock()
	}
}

func (v *view) Set(f func(c *config.Config)) {
	v.mu.Lock()
	old := v.cur
	c := *old
	f(&c)
	v.cur = &c
	fns := make([]func(old, cur *config.Config), 0, len(v.watchers))
	for _, fn := range v.watchers {
		fns = append(fns, fn)
	}
	v.mu.Unlock()
	for _, fn := range fns {
		fn(old, &c)
	}
}

type fired struct {
	at  time.Time
	req core.RunRequest
	run core.RunID
}

// runner creates a real run row per Submit (the scheduler reads run outcomes
// back through the store) and records when each run was submitted.
type runner struct {
	*fakes.Runner
	store core.Store

	mu    sync.Mutex
	err   error
	fires []fired
}

func (r *runner) Submit(ctx context.Context, req core.RunRequest) (core.SubmitResult, error) {
	if r.Paused() {
		return core.SubmitResult{}, core.ErrPaused
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return core.SubmitResult{}, r.err
	}
	run := &core.Run{Session: req.Session, Principal: req.Principal, User: req.User, Trigger: req.Trigger,
		Schedule: req.Schedule, Status: core.RunQueued, CreatedAt: time.Now()}
	if err := r.store.Runs().Create(ctx, run); err != nil {
		return core.SubmitResult{}, err
	}
	r.fires = append(r.fires, fired{at: time.Now(), req: req, run: run.ID})
	return core.SubmitResult{Run: run.ID}, nil
}

func (r *runner) all() []fired {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fired(nil), r.fires...)
}

func (r *runner) fail(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

type harness struct {
	t      *testing.T
	svc    *Service
	store  core.Store
	cfg    *view
	runner *runner
	hub    *fakes.Hub
	memory *fakes.Memory
	bus    *events.Bus
	audit  *fakes.Auditor
	owner  core.UserID
	p      core.PrincipalID
	dm     core.ChatRef
}

func newHarness(t *testing.T, tweak ...func(*config.Config)) *harness {
	t.Helper()
	c := config.Default()
	c.Timezone = "Europe/London"
	for _, f := range tweak {
		f(c)
	}
	st := memstore.New()
	ctx := context.Background()
	u := &core.User{Username: "owner", Role: core.RoleOwner, Timezone: "Europe/London", CreatedAt: time.Now()}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, store: st, cfg: &view{cur: c}, owner: u.ID, p: core.UserPrincipal(u.ID),
		dm:    core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM},
		bus:   events.New(events.Options{Ring: 256}),
		audit: &fakes.Auditor{}, memory: &fakes.Memory{}}
	h.runner = &runner{Runner: &fakes.Runner{Store: st}, store: st}
	h.hub = &fakes.Hub{PreferredChats: map[core.UserID]core.ChatRef{u.ID: h.dm}}
	if _, err := st.Identities().Touch(ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "42", Status: core.IdentityLinked,
		User: u.ID, DMChat: "42", FirstSeen: time.Now(), LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Deps{Store: st, Config: h.cfg, Runner: h.runner, Hub: h.hub, Memory: h.memory, Bus: h.bus, Audit: h.audit})
	if err != nil {
		t.Fatal(err)
	}
	svc.jitter = func() float64 { return 0 }
	h.svc = svc
	return h
}

func (h *harness) viewer() core.Viewer {
	return core.Viewer{User: h.owner, Principals: []core.PrincipalID{h.p}}
}

func (h *harness) upsert(sc *core.Schedule) *core.Schedule {
	h.t.Helper()
	if sc.Principal == "" {
		sc.Principal = h.p
	}
	if err := h.svc.Upsert(context.Background(), h.viewer(), sc, "user:"+string(h.owner)); err != nil {
		h.t.Fatalf("Upsert(%+v): %v", sc, err)
	}
	return sc
}

func (h *harness) get(id core.ScheduleID) *core.Schedule {
	h.t.Helper()
	sc, err := h.store.Schedules().Get(context.Background(), core.SystemViewer(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return sc
}

// start runs the scheduler loop until the returned stop is called. Use only
// inside a synctest bubble.
func (h *harness) start() (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.svc.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			h.t.Errorf("Run: %v", err)
		}
	}
}

// finish marks a run finished and publishes run.finished, as the runner does.
func (h *harness) finish(run core.RunID, status core.RunStatus) {
	h.t.Helper()
	ctx := context.Background()
	r, err := h.store.Runs().Get(ctx, core.SystemViewer(), run)
	if err != nil {
		h.t.Fatal(err)
	}
	r.Status, r.FinishedAt = status, time.Now()
	if err := h.store.Runs().Update(ctx, r); err != nil {
		h.t.Fatal(err)
	}
	h.bus.Publish(core.Event{Type: core.EvRunFinished, Principal: r.Principal, Session: r.Session, Run: run,
		Data: core.RunFinishedData{Status: status}})
}

func sleepUntil(t time.Time) { time.Sleep(time.Until(t)) }
