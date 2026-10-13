package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestErrorMapping(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleOwner)
	cases := []struct {
		name string
		stmt string
		want error
	}{
		{"primary key", `INSERT INTO meta(k, v) VALUES ('a', '1'), ('a', '2')`, core.ErrConflict},
		{"unique", fmt.Sprintf(`INSERT INTO users(id, username, role, pw_hash, created_at, updated_at) VALUES ('usr_x', '%s', 'member', '', 0, 0)`, strings.ToUpper(u.Username)), core.ErrConflict},
		{"check", `INSERT INTO users(id, username, role, pw_hash, created_at, updated_at) VALUES ('usr_y', 'y', 'root', '', 0, 0)`, core.ErrInvalid},
		{"foreign key", `INSERT INTO sessions(id, principal_id, origin, created_at, updated_at) VALUES ('ses_x', 'nope', 'web', 0, 0)`, core.ErrInvalid},
		{"not null", `INSERT INTO meta(k, v) VALUES ('b', NULL)`, core.ErrInvalid},
		{"strict datatype", `INSERT INTO meta(k, v) VALUES ('c', x'00')`, core.ErrInvalid},
		{"trigger", `DELETE FROM audit_log`, nil},
	}
	for _, c := range cases {
		_, err := w.db.w.ExecContext(w.ctx, c.stmt)
		got := dbErr(c.name, err)
		if c.want == nil {
			if err != nil && !errors.Is(got, core.ErrInvalid) {
				t.Errorf("%s: %v", c.name, got)
			}
			continue
		}
		if !errors.Is(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if err := dbErr("x", sql.ErrNoRows); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("no rows = %v", err)
	}
	if err := dbErr("x", context.Canceled); !errors.Is(err, context.Canceled) || errors.Is(err, core.ErrInvalid) {
		t.Errorf("context errors pass through: %v", err)
	}
	if dbErr("x", nil) != nil {
		t.Error("dbErr(nil) != nil")
	}
}

// TestTxSavepoints: a multi-statement write that fails inside Tx (here the
// space insert after its principal was inserted) leaves no partial rows, and
// the rest of the transaction still commits.
func TestTxSavepoints(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	err := w.db.Tx(w.ctx, func(tx core.Store) error {
		if err := tx.Meta().Set(w.ctx, "kept", "1"); err != nil {
			return err
		}
		bad := &core.Space{Kind: "channel", Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, Owner: u.ID}
		if err := tx.Spaces().Create(w.ctx, bad, &core.Principal{}); !errors.Is(err, core.ErrInvalid) {
			return fmt.Errorf("bad space: %w", err)
		}
		return tx.Tx(w.ctx, func(inner core.Store) error { return inner.Meta().Set(w.ctx, "nested", "1") })
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := w.db.Principals().ForUser(w.ctx, u.ID)
	if err != nil || len(ps) != 1 {
		t.Errorf("the failed space left %d principals behind (%v)", len(ps), err)
	}
	for _, k := range []string{"kept", "nested"} {
		if _, err := w.db.Meta().Get(w.ctx, k); err != nil {
			t.Errorf("meta %s: %v", k, err)
		}
	}
}

func TestTxPanicRollsBack(t *testing.T) {
	w := newWorld(t)
	func() {
		defer func() { _ = recover() }()
		_ = w.db.Tx(w.ctx, func(tx core.Store) error {
			if err := tx.Meta().Set(w.ctx, "p", "1"); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	if _, err := w.db.Meta().Get(w.ctx, "p"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a panicking Tx committed: %v", err)
	}
	if err := w.db.Meta().Set(w.ctx, "after", "1"); err != nil {
		t.Fatalf("the writer is stuck after a panicking Tx: %v", err)
	}
	err := w.db.Tx(w.ctx, func(tx core.Store) error {
		if _, err := tx.Backup(w.ctx, t.TempDir()); !errors.Is(err, core.ErrInvalid) {
			return fmt.Errorf("Backup inside Tx = %w", err)
		}
		if err := tx.Close(); !errors.Is(err, core.ErrInvalid) {
			return fmt.Errorf("Close inside Tx = %w", err)
		}
		if tx.SQLiteVersion() != w.db.SQLiteVersion() {
			return errors.New("SQLiteVersion differs inside Tx")
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

func TestRunsOneActiveAndDecideCAS(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	s := w.session(core.UserPrincipal(u.ID))
	w.run(s, core.RunRunning)
	second := &core.Run{Session: s.ID, Principal: s.Principal, User: u.ID, Trigger: core.TrigMessage, Status: core.RunWaitingApproval, CreatedAt: t0}
	if err := w.db.Runs().Create(w.ctx, second); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("runs_one_active: %v", err)
	}

	run := w.run(w.session(s.Principal), core.RunWaitingApproval)
	a := &core.Approval{Run: run.ID, Session: run.Session, Principal: run.Principal, Status: core.ApprovalPending, OnTimeout: core.TimeoutDeny, Card: core.Card{Risk: core.SERead}}
	w.must(w.db.Approvals().Create(w.ctx, a))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 50 {
		wg.Go(func() {
			ok, err := w.db.Approvals().Decide(w.ctx, a.ID, []core.ApprovalStatus{core.ApprovalPending, core.ApprovalParked},
				core.ApprovalAllowed, core.AllowOnce, u.ID, fmt.Sprintf("web-%d", i), t0)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d deciders won, want exactly 1", wins)
	}
	if ok, err := w.db.Approvals().Decide(w.ctx, a.ID, nil, core.ApprovalDenied, core.Deny, u.ID, "web", t0); ok || err != nil {
		t.Errorf("Decide with no from states = %t, %v", ok, err)
	}
}

func TestScheduleUpsertKeepsPrincipal(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.user(core.RoleMember), w.user(core.RoleMember)
	sch := &core.Schedule{Principal: core.UserPrincipal(alice.ID), Name: "a", Kind: core.SchedCron, Spec: "* * * * *", TZ: "UTC",
		Prompt: "x", MissedRun: "skip", CreatedBy: "system"}
	w.must(w.db.Schedules().Upsert(w.ctx, sch))
	hijack := *sch
	hijack.Principal = core.UserPrincipal(bob.ID)
	hijack.Prompt = "exfiltrate"
	if err := w.db.Schedules().Upsert(w.ctx, &hijack); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Upsert onto another principal's schedule = %v", err)
	}
	got, err := w.db.Schedules().Get(w.ctx, core.SystemViewer(), sch.ID)
	if err != nil || got.Principal != sch.Principal || got.Prompt != "x" {
		t.Errorf("schedule after a rejected Upsert = %+v, %v", got, err)
	}
}

func TestSessionURLNormalisation(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	s := w.session(core.UserPrincipal(u.ID))
	w.must(w.db.Sessions().AddURLs(w.ctx, s.ID, []string{"HTTPS://Example.COM:443/path?q=1#frag", "http://a.example"}, "search"))
	for url, want := range map[string]bool{
		"https://example.com/path?q=1":       true,
		"https://example.com:443/path?q=1#x": true,
		"https://example.com/path?q=2":       false,
		"https://example.com/PATH?q=1":       false,
		"http://a.example/":                  true,
		"http://a.example:8080/":             false,
	} {
		ok, err := w.db.Sessions().HasURL(w.ctx, s.ID, url)
		if err != nil || ok != want {
			t.Errorf("HasURL(%s) = %t, %v; want %t", url, ok, err, want)
		}
	}
}
