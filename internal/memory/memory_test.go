package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

type harness struct {
	svc   *Service
	store core.Store
	bus   *events.Bus
	audit *fakes.Auditor
	user  core.UserID
	p     core.PrincipalID
	dir   string
}

func newHarness(t *testing.T, tweak ...func(*config.Config)) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.Memory.MemoryMaxBytes = 256
	cfg.Memory.UserMaxBytes = 256
	cfg.Memory.HeartbeatMaxBytes = 256
	for _, f := range tweak {
		f(cfg)
	}
	st := memstore.New()
	u := &core.User{Username: "owner", Role: core.RoleOwner, CreatedAt: time.Now()}
	if err := st.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	h := &harness{store: st, bus: events.New(events.Options{Ring: 64}), audit: &fakes.Auditor{}, user: u.ID,
		p: core.UserPrincipal(u.ID), dir: t.TempDir()}
	svc, err := New(Deps{Store: st, Config: config.Static(cfg), Bus: h.bus, Audit: h.audit, DataDir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

func (h *harness) viewer() core.Viewer {
	return core.Viewer{User: h.user, Principals: []core.PrincipalID{h.p}}
}

func (h *harness) apply(t *testing.T, f core.MemoryFile, op, old, new string) *core.MemoryEvent {
	t.Helper()
	e, err := h.svc.Apply(context.Background(), h.p, "run_1", core.MemoryOp{File: f, Op: op, Old: old, New: new}, core.ProvUser)
	if err != nil {
		t.Fatalf("Apply(%s %s): %v", op, f, err)
	}
	return e
}

func (h *harness) read(t *testing.T, f core.MemoryFile) string {
	t.Helper()
	c, _, err := h.svc.Read(context.Background(), h.viewer(), h.p, f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRequiresDeps(t *testing.T) {
	cfg := config.Static(config.Default())
	for name, d := range map[string]Deps{
		"store":   {Config: cfg, DataDir: "x"},
		"config":  {Store: memstore.New(), DataDir: "x"},
		"datadir": {Store: memstore.New(), Config: cfg},
	} {
		if _, err := New(d); err == nil {
			t.Errorf("%s: New accepted missing dependency", name)
		}
	}
}

func TestApplyOps(t *testing.T) {
	h := newHarness(t)
	h.apply(t, core.FileMemory, "add", "", "  Lives in Leeds  ")
	h.apply(t, core.FileMemory, "add", "", "Prefers metric units")
	if got, want := h.read(t, core.FileMemory), "Lives in Leeds\nPrefers metric units\n"; got != want {
		t.Fatalf("after adds = %q, want %q", got, want)
	}
	h.apply(t, core.FileMemory, "replace", "Lives in Leeds", "Lives in York")
	h.apply(t, core.FileMemory, "remove", "Prefers metric units", "")
	if got, want := h.read(t, core.FileMemory), "Lives in York\n"; got != want {
		t.Fatalf("after replace+remove = %q, want %q", got, want)
	}
	h.apply(t, core.FileMemory, "remove", "Lives in York", "")
	if got := h.read(t, core.FileMemory); got != "" {
		t.Fatalf("after removing the last entry = %q, want empty", got)
	}

	ctx := context.Background()
	for _, c := range []struct {
		name string
		op   core.MemoryOp
		want string
	}{
		{"unknown file", core.MemoryOp{File: "SOUL", Op: "add", New: "x"}, "file must be"},
		{"unknown op", core.MemoryOp{File: core.FileUser, Op: "append", New: "x"}, "op must be"},
		{"add without new", core.MemoryOp{File: core.FileUser, Op: "add"}, "needs new"},
		{"multi-line", core.MemoryOp{File: core.FileUser, Op: "add", New: "a\nb"}, "single line"},
		{"replace missing old", core.MemoryOp{File: core.FileUser, Op: "replace", New: "x"}, "needs old"},
		{"replace same", core.MemoryOp{File: core.FileUser, Op: "replace", Old: "x", New: "x"}, "identical"},
		{"remove absent", core.MemoryOp{File: core.FileUser, Op: "remove", Old: "nope"}, "no entry exactly equal"},
		{"nul byte", core.MemoryOp{File: core.FileUser, Op: "add", New: "a\x00b"}, "NUL"},
	} {
		if _, err := h.svc.Apply(ctx, h.p, "", c.op, core.ProvUser); !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalid containing %q", c.name, err, c.want)
		}
	}
}

func TestApplyDuplicateAndHints(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, core.FileMemory, "add", "", "- Project Bothy ships in October")
	_, err := h.svc.Apply(ctx, h.p, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: "- Project Bothy ships in October"}, core.ProvUser)
	if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("duplicate add: %v", err)
	}
	h.apply(t, core.FileHeartbeat, "add", "", "Check the bank inbox")

	// Hints quote file content, so only a session already tainted private gets them.
	tl := tool(t, h, "memory")
	private := h.env(t)
	private.Taint = core.TaintPrivate
	_, err = tl.Plan(ctx, private, json.RawMessage(`{"file":"MEMORY","op":"remove","old":"project bothy"}`))
	if err == nil || !strings.Contains(err.Error(), `"- Project Bothy ships in October"`) {
		t.Fatalf("near-miss remove should name the candidate entry: %v", err)
	}
	_, err = tl.Plan(ctx, private, json.RawMessage(`{"file":"HEARTBEAT","op":"remove","old":"check calendar"}`))
	if err == nil || !strings.Contains(err.Error(), "Current HEARTBEAT.md:\nCheck the bank inbox") {
		t.Fatalf("HEARTBEAT miss should include the file: %v", err)
	}
	for _, taint := range []core.Taint{0, core.TaintUntrusted} {
		env := h.env(t)
		env.Taint = taint
		for _, args := range []string{`{"file":"HEARTBEAT","op":"remove","old":"bank"}`, `{"file":"MEMORY","op":"remove","old":"bothy"}`} {
			_, err := tl.Plan(ctx, env, json.RawMessage(args))
			if !errors.Is(err, core.ErrInvalid) || strings.Contains(err.Error(), "inbox") || strings.Contains(err.Error(), "October") {
				t.Fatalf("taint %b: a miss leaked file content into a session not tainted private: %v", taint, err)
			}
		}
	}
	_, err = h.svc.Apply(ctx, h.p, "run_1", core.MemoryOp{File: core.FileHeartbeat, Op: "remove", Old: "bank"}, core.ProvUser)
	if !errors.Is(err, core.ErrInvalid) || strings.Contains(err.Error(), "inbox") {
		t.Fatalf("Apply miss leaked file content: %v", err)
	}
}

func TestApplyCapRemainingBytes(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Memory.UserMaxBytes = 300 })
	entry := strings.Repeat("a", 199) // 200 bytes with its newline
	h.apply(t, core.FileUser, "add", "", entry)
	_, err := h.svc.Apply(context.Background(), h.p, "run_1", core.MemoryOp{File: core.FileUser, Op: "add", New: strings.Repeat("b", 149)}, core.ProvUser)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("over-cap add: err = %v, want ErrInvalid", err)
	}
	for _, want := range []string{"USER.md is capped at 300 bytes", "would make it 350 bytes", "100 bytes are free now"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("cap error %q lacks %q", err, want)
		}
	}
	if got := h.read(t, core.FileUser); got != entry+"\n" {
		t.Fatalf("file changed by a rejected op: %q", got)
	}
	// Exactly at the cap is allowed.
	h.apply(t, core.FileUser, "add", "", strings.Repeat("c", 99))
	if n := len(h.read(t, core.FileUser)); n != 300 {
		t.Fatalf("file is %d bytes, want 300", n)
	}
	_, err = h.svc.Write(context.Background(), h.viewer(), h.p, core.FileUser, strings.Repeat("x", 301), "", h.user)
	if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "capped at 300 bytes") {
		t.Fatalf("over-cap Write: %v", err)
	}
}

func TestApplyConcurrentSerialized(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Memory.MemoryMaxBytes = 1 << 16 })
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.svc.Apply(context.Background(), h.p, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: fmt.Sprintf("entry %02d", i)}, core.ProvUser)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	lines := splitLines(h.read(t, core.FileMemory))
	if len(lines) != n {
		t.Fatalf("file has %d entries, want %d (a concurrent write was lost)", len(lines), n)
	}
	evs, err := h.store.MemoryEvents().Since(context.Background(), h.p, 0)
	if err != nil || len(evs) != n {
		t.Fatalf("events = %d, %v; want %d", len(evs), err, n)
	}
	// Serialized: each op started from exactly the previous op's result.
	for i := 1; i < len(evs); i++ {
		if evs[i].Before != evs[i-1].After {
			t.Fatalf("event %d did not start from event %d's content", evs[i].ID, evs[i-1].ID)
		}
	}
}

func TestWriteETag(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, tag, err := h.svc.Read(ctx, h.viewer(), h.p, core.FileHeartbeat)
	if err != nil || c != "" || tag != etag("") {
		t.Fatalf("empty file: %q %q %v", c, tag, err)
	}
	tag2, err := h.svc.Write(ctx, h.viewer(), h.p, core.FileHeartbeat, "Check the inbox\n", tag, h.user)
	if err != nil || tag2 != etag("Check the inbox\n") {
		t.Fatalf("Write with current etag: %q %v", tag2, err)
	}
	if _, err := h.svc.Write(ctx, h.viewer(), h.p, core.FileHeartbeat, "stale\n", tag, h.user); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Write with stale etag: err = %v, want ErrConflict", err)
	}
	if got := h.read(t, core.FileHeartbeat); got != "Check the inbox\n" {
		t.Fatalf("conflicting write changed the file: %q", got)
	}
	// An agent op in between also invalidates the editor's etag.
	h.apply(t, core.FileHeartbeat, "add", "", "Check the calendar")
	if _, err := h.svc.Write(ctx, h.viewer(), h.p, core.FileHeartbeat, "x\n", tag2, h.user); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Write after Apply: err = %v, want ErrConflict", err)
	}
	if _, err := h.svc.Write(ctx, h.viewer(), h.p, core.FileHeartbeat, "forced\n", "", h.user); err != nil {
		t.Fatalf("Write without precondition: %v", err)
	}
	if _, err := h.svc.Write(ctx, h.viewer(), h.p, core.FileHeartbeat, "\xff", "", h.user); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
	hist, err := h.svc.History(ctx, h.viewer(), h.p, core.FileHeartbeat, core.Page{})
	if err != nil || len(hist) != 3 || hist[0].Op != "edit" || hist[0].Actor != "user:"+string(h.user) || hist[1].Op != "add" {
		t.Fatalf("history = %+v, %v", hist, err)
	}
}

func TestDeltasSince(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	snap, err := h.svc.Snapshot(ctx, h.p)
	if err != nil || snap.LastID != 0 || snap.Private {
		t.Fatalf("empty snapshot = %+v, %v", snap, err)
	}
	text, last, err := h.svc.DeltasSince(ctx, h.p, snap.LastID)
	if err != nil || text != "" || last != 0 {
		t.Fatalf("no events: %q %d %v", text, last, err)
	}

	add := h.apply(t, core.FileMemory, "add", "", "Lives in Leeds")
	text, last, _ = h.svc.DeltasSince(ctx, h.p, 0)
	if want := "MEMORY.md changed:\n+ Lives in Leeds\n"; text != want || last != add.ID {
		t.Fatalf("after add: %q (last %d), want %q (last %d)", text, last, want, add.ID)
	}

	rep := h.apply(t, core.FileMemory, "replace", "Lives in Leeds", "Lives in York")
	text, last, _ = h.svc.DeltasSince(ctx, h.p, add.ID)
	if want := "MEMORY.md changed:\n- Lives in Leeds\n+ Lives in York\n"; text != want || last != rep.ID {
		t.Fatalf("after replace: %q, want %q", text, want)
	}

	h.apply(t, core.FileUser, "add", "", "Name: Sam")
	h.apply(t, core.FileHeartbeat, "add", "", "Check the inbox")
	rem := h.apply(t, core.FileMemory, "remove", "Lives in York", "")
	text, last, _ = h.svc.DeltasSince(ctx, h.p, rep.ID)
	if want := "MEMORY.md changed:\n- Lives in York\nUSER.md changed:\n+ Name: Sam\n"; text != want || last != rem.ID {
		t.Fatalf("after remove: %q (last %d), want %q (last %d)", text, last, want, rem.ID)
	}

	// Net change since the start: the add and remove of MEMORY cancel out.
	text, _, _ = h.svc.DeltasSince(ctx, h.p, 0)
	if want := "USER.md changed:\n+ Name: Sam\n"; text != want {
		t.Fatalf("net deltas: %q, want %q", text, want)
	}

	snap, err = h.svc.Snapshot(ctx, h.p)
	if err != nil || snap.LastID != rem.ID || snap.User != "Name: Sam\n" || snap.Memory != "" || !snap.Private {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	if text, last, _ := h.svc.DeltasSince(ctx, h.p, snap.LastID); text != "" || last != snap.LastID {
		t.Fatalf("deltas after a fresh snapshot: %q %d", text, last)
	}
}

func TestSpacesHaveNoMemory(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	space := core.PrincipalID(core.NewID(core.PrefixSpace))
	v := core.Viewer{User: h.user, Principals: []core.PrincipalID{h.p, space}}
	snap, err := h.svc.Snapshot(ctx, space)
	if err != nil || snap != (core.MemorySnapshot{}) {
		t.Fatalf("space snapshot = %+v, %v", snap, err)
	}
	if text, last, err := h.svc.DeltasSince(ctx, space, 7); err != nil || text != "" || last != 7 {
		t.Fatalf("space deltas: %q %d %v", text, last, err)
	}
	if _, err := h.svc.Apply(ctx, space, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: "x"}, core.ProvUser); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("Apply: %v", err)
	}
	if _, _, err := h.svc.Read(ctx, v, space, core.FileMemory); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("Read: %v", err)
	}
	if _, err := h.svc.Write(ctx, v, space, core.FileMemory, "x", "", h.user); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("Write: %v", err)
	}
	if _, err := h.svc.History(ctx, v, space, core.FileMemory, core.Page{}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("History: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "memory", string(space))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a space memory dir exists: %v", err)
	}
}

func TestViewerScoping(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.apply(t, core.FileMemory, "add", "", "secret plan")
	other := core.Viewer{User: "usr_other", Principals: []core.PrincipalID{"usr_other"}}
	if _, _, err := h.svc.Read(ctx, other, h.p, core.FileMemory); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Read across principals: %v", err)
	}
	if _, err := h.svc.Write(ctx, other, h.p, core.FileMemory, "", "", "usr_other"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Write across principals: %v", err)
	}
	if _, err := h.svc.History(ctx, other, h.p, core.FileMemory, core.Page{}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("History across principals: %v", err)
	}
	if _, _, err := h.svc.Read(ctx, h.viewer(), h.p, "SOUL"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("unknown file: %v", err)
	}
	for _, bad := range []core.PrincipalID{"../etc", "usr_short", ""} {
		if _, err := h.svc.Apply(ctx, bad, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: "x"}, core.ProvUser); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Apply(%q): %v", bad, err)
		}
		if _, err := h.svc.Snapshot(ctx, bad); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Snapshot(%q): %v", bad, err)
		}
	}
}

func TestFilesAtomicAndPrivate(t *testing.T) {
	h := newHarness(t)
	h.apply(t, core.FileMemory, "add", "", "x")
	dir := filepath.Join(h.dir, "memory", string(h.p))
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"MEMORY.md"}) {
		t.Fatalf("dir holds %v, want only MEMORY.md (no temp files left)", names)
	}
	if runtime.GOOS == "windows" {
		return
	}
	fi, _ := os.Stat(filepath.Join(dir, "MEMORY.md"))
	di, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("modes: file %v dir %v, want 0600 and 0700", fi.Mode().Perm(), di.Mode().Perm())
	}
}

func TestAuditAndBus(t *testing.T) {
	h := newHarness(t)
	sub := h.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{h.p}}, 8)
	defer sub.Close()
	e := h.apply(t, core.FileMemory, "add", "", "x")
	if _, err := h.svc.Write(context.Background(), h.viewer(), h.p, core.FileUser, "y\n", "", h.user); err != nil {
		t.Fatal(err)
	}
	for _, want := range []core.MemoryData{{File: core.FileMemory, EventID: e.ID}, {File: core.FileUser, EventID: e.ID + 1}} {
		ev := <-sub.C()
		if ev.Type != core.EvMemoryUpdated || ev.Data != want {
			t.Fatalf("event %+v, want memory.updated %+v", ev, want)
		}
	}
	got := h.audit.All()
	if len(got) != 2 || got[0].Action != "memory.write" || got[0].Actor != "agent:run_1" || got[0].Run != "run_1" ||
		got[1].Action != "memory.edit" || got[1].Actor != "user:"+string(h.user) {
		t.Fatalf("audit = %+v", got)
	}
	for _, a := range got {
		if strings.Contains(string(a.Detail), `"x`) || strings.Contains(string(a.Detail), `"y`) {
			t.Fatalf("audit detail leaks memory content: %s", a.Detail)
		}
	}
	if e.Provenance != core.ProvUser || e.Before != "" || e.After != "x\n" {
		t.Fatalf("event = %+v", e)
	}
}

type failingEvents struct {
	core.MemoryEventRepo
}

func (failingEvents) Add(context.Context, *core.MemoryEvent) (int64, error) {
	return 0, errors.New("disk full")
}

type failingStore struct {
	core.Store
}

func (f failingStore) MemoryEvents() core.MemoryEventRepo {
	return failingEvents{f.Store.MemoryEvents()}
}

func TestEventFailureRollsBackFile(t *testing.T) {
	h := newHarness(t)
	h.apply(t, core.FileMemory, "add", "", "kept")
	h.svc.store = failingStore{h.store}
	if _, err := h.svc.Apply(context.Background(), h.p, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: "lost"}, core.ProvUser); err == nil {
		t.Fatal("Apply succeeded without its event row")
	}
	if got := h.read(t, core.FileMemory); got != "kept\n" {
		t.Fatalf("file = %q, want the content before the failed op", got)
	}
}

func TestSearchAndPurge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	s := &core.Session{Principal: h.p, Origin: "web", Title: "Q3 report", CreatedAt: time.Now()}
	if err := h.store.Sessions().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	m := &core.Message{Session: s.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUser,
		Parts: []core.Part{{Type: core.PartText, Text: "the invoice is overdue"}}, CreatedAt: time.Now()}
	if err := h.store.Messages().Append(ctx, m); err != nil {
		t.Fatal(err)
	}
	hits, err := h.svc.Search(ctx, h.viewer(), "invoice", 0)
	if err != nil || len(hits) != 1 || hits[0].Session != s.ID {
		t.Fatalf("Search = %+v, %v", hits, err)
	}
	if _, err := h.svc.Search(ctx, h.viewer(), "   ", 0); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("empty query: %v", err)
	}
	other := core.Viewer{User: "usr_x", Principals: []core.PrincipalID{"usr_x"}}
	if hits, _ := h.svc.Search(ctx, other, "invoice", 5); len(hits) != 0 {
		t.Fatalf("search leaked across principals: %+v", hits)
	}

	h.apply(t, core.FileMemory, "add", "", "x")
	if err := h.svc.Purge(ctx, h.p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "memory", string(h.p))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purge left files: %v", err)
	}
	if err := h.svc.Purge(ctx, "../.."); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Purge(../..): %v", err)
	}
}

func TestOverCapCheck(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chk := h.svc.Checks()[0]
	if chk.ID() != "memory.over_cap" || len(chk.Run(ctx)) != 0 {
		t.Fatal("fresh install should have no findings")
	}
	h.apply(t, core.FileMemory, "add", "", strings.Repeat("a", 200))
	if err := os.WriteFile(h.svc.path(h.p, core.FileUser), []byte(strings.Repeat("b", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := chk.Run(ctx)
	if len(fs) != 1 || fs[0].Severity != core.SevLow || !strings.Contains(fs[0].Detail, "USER.md: 300 of 256 bytes") ||
		strings.Contains(fs[0].Detail, "MEMORY.md") {
		t.Fatalf("findings = %+v", fs)
	}
	if err := chk.Fix(ctx, fs[0]); !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("Fix: %v", err)
	}
}

// copyingEvents returns the new id without writing it back into the caller's
// event, which the MemoryEventRepo contract allows.
type copyingEvents struct {
	core.MemoryEventRepo
}

func (r copyingEvents) Add(ctx context.Context, e *core.MemoryEvent) (int64, error) {
	cp := *e
	return r.MemoryEventRepo.Add(ctx, &cp)
}

type copyingStore struct {
	core.Store
}

func (s copyingStore) MemoryEvents() core.MemoryEventRepo {
	return copyingEvents{s.Store.MemoryEvents()}
}

func TestEventIDComesFromRepo(t *testing.T) {
	h := newHarness(t)
	h.apply(t, core.FileUser, "add", "", "first")
	h.svc.store = copyingStore{h.store}
	sub := h.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{h.p}}, 8)
	defer sub.Close()
	e := h.apply(t, core.FileMemory, "add", "", "x")
	if e.ID != 2 {
		t.Fatalf("event id = %d, want the id the repository returned (2)", e.ID)
	}
	if ev := <-sub.C(); ev.Data != (core.MemoryData{File: core.FileMemory, EventID: 2}) {
		t.Fatalf("memory.updated = %+v", ev.Data)
	}
	audit := h.audit.All()
	if !strings.Contains(string(audit[len(audit)-1].Detail), `"event_id":2`) {
		t.Fatalf("audit detail = %s", audit[len(audit)-1].Detail)
	}
	if tag, err := h.svc.Write(context.Background(), h.viewer(), h.p, core.FileMemory, "y\n", "", h.user); err != nil || tag == "" {
		t.Fatalf("Write: %q %v", tag, err)
	}
	if ev := <-sub.C(); ev.Data != (core.MemoryData{File: core.FileMemory, EventID: 3}) {
		t.Fatalf("memory.updated after Write = %+v", ev.Data)
	}
}

func TestApplyShrinksOverCapFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The cap (256) was lowered below what the file already holds.
	content := strings.Repeat("a", 150) + "\n" + strings.Repeat("b", 150) + "\n"
	if err := os.MkdirAll(filepath.Dir(h.svc.path(h.p, core.FileMemory)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.svc.path(h.p, core.FileMemory), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Apply(ctx, h.p, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: "c"}, core.ProvUser); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("growing an over-cap file: %v", err)
	}
	tl := tool(t, h, "memory")
	if _, err := tl.Plan(ctx, h.env(t), json.RawMessage(`{"file":"MEMORY","op":"replace","old":"`+strings.Repeat("a", 150)+`","new":"`+strings.Repeat("a", 140)+`"}`)); err != nil {
		t.Fatalf("Plan of a shrinking replace: %v", err)
	}
	h.apply(t, core.FileMemory, "replace", strings.Repeat("a", 150), strings.Repeat("a", 140))
	h.apply(t, core.FileMemory, "remove", strings.Repeat("b", 150), "")
	if got := h.read(t, core.FileMemory); got != strings.Repeat("a", 140)+"\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEventFailureLeavesNoNewFile(t *testing.T) {
	h := newHarness(t)
	h.svc.store = failingStore{h.store}
	if _, err := h.svc.Apply(context.Background(), h.p, "", core.MemoryOp{File: core.FileUser, Op: "add", New: "x"}, core.ProvUser); err == nil {
		t.Fatal("Apply succeeded without its event row")
	}
	if _, err := os.Stat(h.svc.path(h.p, core.FileUser)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed first write left a file behind: %v", err)
	}
}
