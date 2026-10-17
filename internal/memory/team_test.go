package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

// newApproval gives each change its own approval, as the store allows one
// change per approval.
func newApproval() core.ApprovalID { return core.ApprovalID(core.NewID(core.PrefixApproval)) }

func (h *harness) applyTeam(t *testing.T, op, old, new string) *core.MemoryEvent {
	t.Helper()
	e, err := h.svc.applyTeam(context.Background(), teamChange{run: "run_1", approval: newApproval(), approvedBy: h.user,
		op: core.MemoryOp{File: core.FileTeam, Op: op, Old: old, New: new}, prov: core.ProvUser})
	if err != nil {
		t.Fatalf("applyTeam(%s): %v", op, err)
	}
	return e
}

func (h *harness) team(t *testing.T) string {
	t.Helper()
	c, tag, err := h.svc.ReadTeam(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tag != etag(c) {
		t.Fatalf("ReadTeam etag %q does not match its content", tag)
	}
	return c
}

func (h *harness) addUser(t *testing.T, role core.Role, disabled bool) core.UserID {
	t.Helper()
	u := &core.User{Username: fmt.Sprintf("%s%d", role, time.Now().UnixNano()), Role: role, Disabled: disabled, CreatedAt: time.Now()}
	if err := h.store.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func teamHarness(t *testing.T) *harness {
	return newHarness(t, func(c *config.Config) { c.Memory.TeamMaxBytes = 256 })
}

func TestApplyTeamOps(t *testing.T) {
	h := teamHarness(t)
	h.applyTeam(t, "add", "", "  Deploy day is Thursday  ")
	h.applyTeam(t, "add", "", "Standup is at 9:30")
	if got, want := h.team(t), "Deploy day is Thursday\nStandup is at 9:30\n"; got != want {
		t.Fatalf("after adds = %q, want %q", got, want)
	}
	e := h.applyTeam(t, "replace", "Deploy day is Thursday", "Deploy day is Friday")
	if e.File != core.FileTeam || e.Principal != "" || !strings.HasPrefix(string(e.Approval), core.PrefixApproval+"_") || e.Run != "run_1" || e.Actor != "agent:run_1" {
		t.Fatalf("event = %+v", e)
	}
	h.applyTeam(t, "remove", "Standup is at 9:30", "")
	if got, want := h.team(t), "Deploy day is Friday\n"; got != want {
		t.Fatalf("after replace+remove = %q, want %q", got, want)
	}
	// TEAM.md is in every prompt, so a near miss may name entries.
	_, err := h.svc.applyTeam(context.Background(), teamChange{run: "run_1", approval: "apr_1",
		op: core.MemoryOp{File: core.FileTeam, Op: "remove", Old: "Deploy"}})
	if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), `"Deploy day is Friday"`) {
		t.Fatalf("near miss: %v", err)
	}
	for name, c := range map[string]teamChange{
		"personal file": {run: "run_1", approval: "apr_1", op: core.MemoryOp{File: core.FileMemory, Op: "add", New: "x"}},
		"no approval":   {run: "run_1", op: core.MemoryOp{File: core.FileTeam, Op: "add", New: "x"}},
		"no run":        {approval: "apr_1", op: core.MemoryOp{File: core.FileTeam, Op: "add", New: "x"}},
	} {
		if _, err := h.svc.applyTeam(context.Background(), c); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got, want := h.team(t), "Deploy day is Friday\n"; got != want {
		t.Fatalf("refused changes wrote: %q", got)
	}
}

func TestApplyRefusesTeam(t *testing.T) {
	h := teamHarness(t)
	_, err := h.svc.Apply(context.Background(), h.p, "run_1", core.MemoryOp{File: core.FileTeam, Op: "add", New: "x"}, core.ProvUser)
	if !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("Apply(TEAM) = %v, want ErrForbidden", err)
	}
	if h.team(t) != "" {
		t.Fatal("Apply wrote TEAM.md")
	}
	if _, _, err := h.svc.Read(context.Background(), h.viewer(), h.p, core.FileTeam); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Read(TEAM) through the personal path = %v", err)
	}
}

func TestTeamCap(t *testing.T) {
	h := teamHarness(t)
	ctx := context.Background()
	h.applyTeam(t, "add", "", strings.Repeat("a", 200))
	_, err := h.svc.applyTeam(ctx, teamChange{run: "run_1", approval: "apr_1",
		op: core.MemoryOp{File: core.FileTeam, Op: "add", New: strings.Repeat("b", 100)}})
	if !errors.Is(err, core.ErrInvalid) || !strings.Contains(err.Error(), "TEAM.md is capped at 256 bytes") ||
		!strings.Contains(err.Error(), "55 bytes are free") {
		t.Fatalf("over cap: %v", err)
	}
	// After the cap is lowered a shrinking op still passes.
	if err := os.WriteFile(h.svc.path(teamDir, core.FileTeam), []byte(strings.Repeat("c", 300)+"\nshort\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.applyTeam(t, "remove", "short", "")
	if _, err := h.svc.WriteTeam(ctx, strings.Repeat("d", 257), "", h.user); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("WriteTeam over the cap: %v", err)
	}
}

func TestTeamFileModeAndPlace(t *testing.T) {
	h := teamHarness(t)
	h.applyTeam(t, "add", "", "x")
	p := filepath.Join(h.dir, "memory", "team", "TEAM.md")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		di, _ := os.Stat(filepath.Dir(p))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("modes = %v %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("team dir holds %d entries (temp files left?)", len(entries))
	}
}

type failingTeamEvents struct {
	core.TeamMemoryEventRepo
}

func (failingTeamEvents) Add(context.Context, *core.MemoryEvent) (int64, error) {
	return 0, errors.New("disk full")
}

type failingTeamStore struct {
	core.Store
}

func (f failingTeamStore) TeamMemoryEvents() core.TeamMemoryEventRepo {
	return failingTeamEvents{f.Store.TeamMemoryEvents()}
}

func TestTeamEventFailureRollsBack(t *testing.T) {
	h := teamHarness(t)
	ctx := context.Background()
	h.svc.store = failingTeamStore{h.store}
	if _, err := h.svc.applyTeam(ctx, teamChange{run: "run_1", approval: "apr_1",
		op: core.MemoryOp{File: core.FileTeam, Op: "add", New: "lost"}}); err == nil {
		t.Fatal("applyTeam succeeded without its event row")
	}
	if _, err := os.Stat(h.svc.path(teamDir, core.FileTeam)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed first change left TEAM.md behind: %v", err)
	}
	h.svc.store = h.store
	h.applyTeam(t, "add", "", "kept")
	h.svc.store = failingTeamStore{h.store}
	if _, err := h.svc.WriteTeam(ctx, "lost\n", "", h.user); err == nil {
		t.Fatal("WriteTeam succeeded without its event row")
	}
	if got := h.team(t); got != "kept\n" {
		t.Fatalf("TEAM.md = %q, want the content before the failed edit", got)
	}
}

func TestTeamConcurrentWrites(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Memory.TeamMaxBytes = 4096 })
	ctx := context.Background()
	var wg sync.WaitGroup
	const n = 12
	errs := make(chan error, n+1)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.svc.applyTeam(ctx, teamChange{run: "run_1", approval: newApproval(),
				op: core.MemoryOp{File: core.FileTeam, Op: "add", New: fmt.Sprintf("note %d", i)}})
			errs <- err
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		// An admin's whole-file edit with no precondition races the adds; the
		// mutex keeps every change that commits.
		_, err := h.svc.WriteTeam(ctx, "", "", h.user)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	evs, err := h.svc.TeamHistory(ctx, core.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	// Replaying the events in order must give the file: no change was lost.
	cur := ""
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Before != cur {
			t.Fatalf("event %d starts from %q, want %q", evs[i].ID, evs[i].Before, cur)
		}
		cur = evs[i].After
	}
	if got := h.team(t); got != cur {
		t.Fatalf("TEAM.md = %q, events end at %q", got, cur)
	}
}

func TestWriteTeam(t *testing.T) {
	h := teamHarness(t)
	ctx := context.Background()
	sub := h.bus.Subscribe(core.Filter{Admin: true}, 8)
	defer sub.Close()
	tag, err := h.svc.WriteTeam(ctx, "Deploy day is Thursday\n", "", h.user)
	if err != nil || tag != etag("Deploy day is Thursday\n") {
		t.Fatalf("WriteTeam = %q, %v", tag, err)
	}
	if ev := <-sub.C(); ev.Principal != "" || ev.Run != "" || ev.Data != (core.MemoryData{File: core.FileTeam, EventID: 1}) {
		t.Fatalf("memory.updated = %+v", ev)
	}
	a := h.audit.All()[len(h.audit.All())-1]
	if a.Action != "memory.edit" || a.Target != "memory:team/TEAM" || a.Actor != "user:"+string(h.user) ||
		!strings.Contains(string(a.Detail), `"scope":"team"`) || strings.Contains(string(a.Detail), "Thursday") {
		t.Fatalf("audit = %+v %s", a, a.Detail)
	}
	if _, err := h.svc.WriteTeam(ctx, "x\n", "stale", h.user); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale etag: %v", err)
	}
	if same, err := h.svc.WriteTeam(ctx, "Deploy day is Thursday\n", tag, h.user); err != nil || same != tag {
		t.Fatalf("unchanged write: %q %v", same, err)
	}
	for name, by := range map[string]core.UserID{
		"member":         h.addUser(t, core.RoleMember, false),
		"disabled admin": h.addUser(t, core.RoleAdmin, true),
		"unknown":        "usr_nobody",
		"nobody":         "",
	} {
		if _, err := h.svc.WriteTeam(ctx, "planted\n", "", by); !errors.Is(err, core.ErrForbidden) {
			t.Errorf("%s: %v", name, err)
		}
	}
	admin := h.addUser(t, core.RoleAdmin, false)
	if _, err := h.svc.WriteTeam(ctx, "Deploy day is Friday\n", tag, admin); err != nil {
		t.Fatalf("admin: %v", err)
	}
	evs, err := h.svc.TeamHistory(ctx, core.Page{})
	if err != nil || len(evs) != 2 || evs[0].Actor != "user:"+string(admin) || evs[0].Op != "edit" || evs[1].After != "Deploy day is Thursday\n" {
		t.Fatalf("history = %+v, %v", evs, err)
	}
}

func TestApplyTeamRecords(t *testing.T) {
	h := teamHarness(t)
	sub := h.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{h.p}, Admin: true}, 8)
	defer sub.Close()
	e, err := h.svc.applyTeam(context.Background(), teamChange{run: "run_9", approval: "apr_9", approvedBy: h.user,
		op: core.MemoryOp{File: core.FileTeam, Op: "add", New: "Deploy day is Thursday"}, prov: core.ProvUntrusted})
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-sub.C(); ev.Principal != "" || ev.Run != "" || ev.Data != (core.MemoryData{File: core.FileTeam, EventID: e.ID}) {
		t.Fatalf("memory.updated = %+v", ev)
	}
	a := h.audit.All()[len(h.audit.All())-1]
	d := string(a.Detail)
	if a.Action != "memory.write" || a.Actor != "agent:run_9" || a.Run != "run_9" || a.Target != "memory:team/TEAM" ||
		!strings.Contains(d, `"scope":"team"`) || !strings.Contains(d, `"approval_id":"apr_9"`) ||
		!strings.Contains(d, `"approved_by":"`+string(h.user)+`"`) || !strings.Contains(d, `"provenance":"untrusted"`) ||
		strings.Contains(d, "Thursday") || strings.Contains(d, "principal_id") {
		t.Fatalf("audit = %+v %s", a, d)
	}
	if e.Provenance != core.ProvUntrusted {
		t.Fatalf("provenance = %s", e.Provenance)
	}
}

func TestTeamOverCapAndPurge(t *testing.T) {
	h := teamHarness(t)
	ctx := context.Background()
	h.applyTeam(t, "add", "", "kept for everyone")
	h.apply(t, core.FileMemory, "add", "", "mine")
	if err := h.svc.Purge(ctx, h.p); err != nil {
		t.Fatal(err)
	}
	if got := h.team(t); got != "kept for everyone\n" {
		t.Fatalf("purge touched TEAM.md: %q", got)
	}
	if err := h.svc.Purge(ctx, teamDir); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Purge(team) = %v", err)
	}
	if err := os.WriteFile(h.svc.path(teamDir, core.FileTeam), []byte(strings.Repeat("t", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := h.svc.Checks()[0].Run(ctx)
	if len(fs) != 1 || !strings.Contains(fs[0].Detail, "team/TEAM.md: 300 of 256 bytes") {
		t.Fatalf("findings = %+v", fs)
	}
}

// approvedRun stores a session and a run of the harness's user and an
// approval of act in it; mod adjusts the approval before it is stored.
func (h *harness) approvedRun(t *testing.T, act *core.Action, mod func(*core.Approval)) (core.RunID, core.ApprovalID) {
	t.Helper()
	ctx := context.Background()
	s := &core.Session{Principal: h.p, Origin: "web", CreatedAt: time.Now()}
	if err := h.store.Sessions().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	r := &core.Run{Session: s.ID, Principal: h.p, User: h.user, Trigger: core.TrigMessage, Status: core.RunRunning, CreatedAt: time.Now()}
	if err := h.store.Runs().Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := &core.Approval{Run: r.ID, Session: s.ID, Principal: h.p, ToolCallID: core.NewID(core.PrefixToolCall), Action: act,
		Decision: core.Decision{Verdict: core.VerdictAsk, Rule: "floor:admin", Approver: core.ApproverReq{AdminOnly: true}},
		Approver: core.ApproverReq{AdminOnly: true}, Status: core.ApprovalAllowed, Choice: core.AllowOnce, OnTimeout: core.TimeoutDeny,
		Card:      core.Card{Title: "Update memory", Risk: core.SEAdmin},
		DecidedBy: h.user, DecidedVia: "web", CreatedAt: now, ExpiresAt: now.Add(time.Minute), DecidedAt: now}
	if mod != nil {
		mod(a)
	}
	if err := h.store.Approvals().Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	return r.ID, a.ID
}

func TestMemoryToolProposesTeam(t *testing.T) {
	h := teamHarness(t)
	h.applyTeam(t, "add", "", "Standup is at 9:30")
	tl := tool(t, h, "memory")
	env := h.env(t)
	conformance.Tool(t, tl, []conformance.ToolCase{
		{Name: "team add", Env: env, Args: json.RawMessage(`{"file":"TEAM","op":"add","new":"Deploy day is Thursday"}`)},
		{Name: "team remove", Env: env, Args: json.RawMessage(`{"file":"TEAM","op":"remove","old":"Standup is at 9:30"}`)},
		{Name: "team missing entry", Env: env, Args: json.RawMessage(`{"file":"TEAM","op":"remove","old":"Lunch is at noon"}`), WantErr: true},
		{Name: "team over cap", Env: env, Args: json.RawMessage(`{"file":"TEAM","op":"add","new":"` + strings.Repeat("z", 250) + `"}`), WantErr: true},
	})
	a, err := tl.Plan(context.Background(), env, json.RawMessage(`{"file":"TEAM","op":"add","new":"Deploy day is Thursday"}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.SideEffect != core.SEAdmin || a.Kind != core.ActMemory || a.Diff != "--- TEAM.md\n+++ TEAM.md\n+Deploy day is Thursday\n" ||
		string(a.Payload) != `{"file":"TEAM","new":"Deploy day is Thursday","op":"add"}` {
		t.Fatalf("action = %+v (payload %s)", a, a.Payload)
	}
	if got := h.team(t); got != "Standup is at 9:30\n" {
		t.Fatalf("Plan wrote TEAM.md: %q", got)
	}
	restricted := *env
	restricted.Principal = core.Principal{ID: core.PrincipalID(core.NewID(core.PrefixSpace)), Kind: core.PrincipalSpace, ToolProfile: "restricted"}
	if _, err := tl.Plan(context.Background(), &restricted, a.Payload); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("a restricted channel's plan = %v", err)
	}
}

// TestMemoryToolTeamNeedsItsApproval: Exec writes TEAM.md only under an
// allowed, admin-only approval of exactly this call in this run.
func TestMemoryToolTeamNeedsItsApproval(t *testing.T) {
	h := teamHarness(t)
	tl := tool(t, h, "memory")
	ctx := context.Background()
	plan := func(args string) *core.Action {
		t.Helper()
		a, err := tl.Plan(ctx, h.env(t), json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	act := plan(`{"file":"TEAM","op":"add","new":"Deploy day is Thursday"}`)
	other := plan(`{"file":"TEAM","op":"add","new":"Deploy day is Friday"}`)
	member := h.addUser(t, core.RoleMember, false)
	refused := map[string]func(env *core.ToolEnv){
		"no approval": func(env *core.ToolEnv) { env.Approval = "" },
		"unknown approval": func(env *core.ToolEnv) {
			env.Approval = core.ApprovalID(core.NewID(core.PrefixApproval))
		},
	}
	for name, mod := range map[string]func(*core.Approval){
		"pending":      func(a *core.Approval) { a.Status, a.Choice, a.DecidedBy = core.ApprovalPending, "", "" },
		"denied":       func(a *core.Approval) { a.Status, a.Choice = core.ApprovalDenied, core.Deny },
		"another call": func(a *core.Approval) { a.Action = other },
		"not admin-only": func(a *core.Approval) {
			a.Approver, a.Decision.Approver = core.ApproverReq{User: h.user}, core.ApproverReq{User: h.user}
		},
		"for the session": func(a *core.Approval) { a.Choice = core.AllowSession },
		"by a member":     func(a *core.Approval) { a.DecidedBy = member },
		"by a teammate":   func(a *core.Approval) { a.DecidedBy, a.DecidedByIdentity = "", "slack:U1" },
	} {
		run, id := h.approvedRun(t, act, mod)
		refused[name] = func(env *core.ToolEnv) { env.Run, env.Approval = run, id }
	}
	otherRun, _ := h.approvedRun(t, act, nil)
	_, id := h.approvedRun(t, act, nil)
	refused["another run"] = func(env *core.ToolEnv) { env.Run, env.Approval = otherRun, id }
	for name, mod := range refused {
		env := h.env(t)
		mod(env)
		res, err := tl.Exec(ctx, env, act)
		if err != nil || !res.IsError || res.Content != noTeamApproval {
			t.Errorf("%s: %+v, %v", name, res, err)
		}
	}
	if got := h.team(t); got != "" {
		t.Fatalf("a refused change wrote TEAM.md: %q", got)
	}
	if evs, _ := h.svc.TeamHistory(ctx, core.Page{}); len(evs) != 0 {
		t.Fatalf("a refused change was recorded: %+v", evs)
	}

	run, id := h.approvedRun(t, act, nil)
	env := h.env(t)
	env.Run, env.Approval, env.Taint = run, id, core.TaintUntrusted
	res, err := tl.Exec(ctx, env, act)
	if err != nil || res.IsError || res.Content != "Remembered for everyone (an admin approved it; TEAM.md 23/256 bytes used)." {
		t.Fatalf("approved Exec = %+v, %v", res, err)
	}
	if got := h.team(t); got != "Deploy day is Thursday\n" {
		t.Fatalf("TEAM.md = %q", got)
	}
	evs, _ := h.svc.TeamHistory(ctx, core.Page{})
	if len(evs) != 1 || evs[0].Approval != id || evs[0].Run != run || evs[0].Actor != "agent:"+string(run) || evs[0].Provenance != core.ProvUntrusted {
		t.Fatalf("history = %+v", evs)
	}
	// The same approval replayed adds nothing: the entry is already there.
	added := *env
	if res, err := tl.Exec(ctx, &added, act); err != nil || !res.IsError ||
		res.Content != `TEAM.md already has the entry "Deploy day is Thursday"; nothing changed` {
		t.Fatalf("replayed Exec = %+v, %v", res, err)
	}
	rm := plan(`{"file":"TEAM","op":"remove","old":"Deploy day is Thursday"}`)
	run, id = h.approvedRun(t, rm, nil)
	env.Run, env.Approval, env.Taint = run, id, 0
	if res, err := tl.Exec(ctx, env, rm); err != nil || res.Content != "Forgot for everyone (an admin approved it; TEAM.md 0/256 bytes used)." {
		t.Fatalf("remove = %+v, %v", res, err)
	}
	// Nor once the note is gone: an approval is good for one change.
	if res, err := tl.Exec(ctx, &added, act); err != nil || !res.IsError || res.Content != noTeamApproval {
		t.Fatalf("Exec replayed after the note was removed = %+v, %v", res, err)
	}
	if got := h.team(t); got != "" {
		t.Fatalf("a replayed approval put the note back: %q", got)
	}
	if evs, _ := h.svc.TeamHistory(ctx, core.Page{}); len(evs) != 2 {
		t.Fatalf("history = %+v", evs)
	}
}
