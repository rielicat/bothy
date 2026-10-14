package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/knowledgetest"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

type harness struct {
	t      *testing.T
	ctx    context.Context
	cfg    *config.Config
	store  core.Store
	repo   *countingRepo
	svc    *Service
	bus    *events.Bus
	audit  *fakes.Auditor
	llm    *fakes.LLM
	budget *fakes.Budget
	clock  *fakes.Clock
	data   string
	work   string
	user   *core.User
	p      *core.Principal
	ws     string
}

// countingRepo records the file changes the indexer applies.
type countingRepo struct {
	core.KnowledgeRepo
	mu      sync.Mutex
	applied []string // "path" or "-path" for deletions
}

func (c *countingRepo) Apply(ctx context.Context, repo int64, sha string, changes []core.KnowledgeFileChange) error {
	c.mu.Lock()
	for _, ch := range changes {
		if ch.Delete {
			c.applied = append(c.applied, "-"+ch.File.Path)
		} else {
			c.applied = append(c.applied, ch.File.Path)
		}
	}
	c.mu.Unlock()
	return c.KnowledgeRepo.Apply(ctx, repo, sha, changes)
}

func (c *countingRepo) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.applied
	c.applied = nil
	return out
}

func newHarness(t *testing.T, tweak ...func(*config.Config)) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.Knowledge.AutoNotes = false
	for _, f := range tweak {
		f(cfg)
	}
	st := memstore.New()
	h := &harness{t: t, ctx: context.Background(), cfg: cfg, store: st, repo: &countingRepo{KnowledgeRepo: knowledgetest.New(st)},
		bus: events.New(events.Options{Ring: 256}), audit: &fakes.Auditor{}, llm: &fakes.LLM{}, budget: &fakes.Budget{},
		clock: fakes.NewClock(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)), data: t.TempDir(), work: t.TempDir()}
	h.user = h.newUser("owner", core.RoleOwner)
	h.p = h.principal(h.user)
	h.ws = workspaceDir(h.work, h.p)
	if err := os.MkdirAll(h.ws, 0o750); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Deps{Repo: h.repo, Store: st, Config: config.Static(cfg), LLM: h.llm, Budget: h.budget, Bus: h.bus,
		Audit: h.audit, DataDir: h.data, WorkDir: h.work, Clock: h.clock})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	t.Cleanup(func() {
		svc.bgCancel()
		svc.bgWG.Wait()
		// The real audit writer refuses any other outcome.
		for _, e := range h.audit.All() {
			if e.Outcome != "ok" && e.Outcome != "denied" && e.Outcome != "error" {
				t.Errorf("audit %s has outcome %q", e.Action, e.Outcome)
			}
		}
	})
	return h
}

func (h *harness) newUser(name string, role core.Role) *core.User {
	h.t.Helper()
	u := &core.User{Username: name, Role: role, PWHash: "x", Timezone: "UTC", CreatedAt: time.Now()}
	if err := h.store.Users().Create(h.ctx, u); err != nil {
		h.t.Fatal(err)
	}
	return u
}

func (h *harness) principal(u *core.User) *core.Principal {
	h.t.Helper()
	p, err := h.store.Principals().Get(h.ctx, core.SystemViewer(), core.UserPrincipal(u.ID))
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) viewer() core.Viewer {
	return core.Viewer{User: h.user.ID, Principals: []core.PrincipalID{h.p.ID}}
}

// write puts a file into the owner's workspace.
func (h *harness) write(rel, content string) {
	h.t.Helper()
	p := filepath.Join(h.ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		h.t.Fatal(err)
	}
}

// index queues dir as name and runs the job on the test goroutine.
func (h *harness) index(dir, name, sha string) *core.IndexedRepo {
	h.t.Helper()
	r, err := h.svc.Index(h.ctx, h.viewer(), h.p.ID, core.KnowledgeIndexRequest{Dir: dir, Repo: name, SHA: sha}, h.user.ID)
	if err != nil {
		h.t.Fatalf("Index(%s): %v", dir, err)
	}
	return h.runJob(r.ID)
}

func (h *harness) runJob(id int64) *core.IndexedRepo {
	h.t.Helper()
	h.svc.mu.Lock()
	h.svc.queue, h.svc.queued = nil, map[int64]bool{}
	h.svc.mu.Unlock()
	h.svc.runJob(h.ctx, id)
	r, err := h.repo.Get(h.ctx, core.SystemViewer(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) session(p core.PrincipalID) *core.Session {
	h.t.Helper()
	s := &core.Session{Principal: p, Origin: "web", Title: "t", CreatedAt: time.Now(), UpdatedAt: time.Now(), LastMessageAt: time.Now()}
	if err := h.store.Sessions().Create(h.ctx, s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) env(s *core.Session) *core.ToolEnv {
	return &core.ToolEnv{User: h.user.ID, Role: core.RoleOwner, Principal: *h.p, Session: s.ID, Run: "run_1",
		Trigger: core.TrigMessage, Workspace: h.ws, Interactive: true}
}

// snapshotTree maps every file below dir to its content.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[strings.TrimPrefix(p, dir)] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
