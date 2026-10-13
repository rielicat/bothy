package store

import (
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// queryPlan returns SQLite's EXPLAIN QUERY PLAN details, one per line.
func queryPlan(t *testing.T, db *DB, q string, args ...any) string {
	t.Helper()
	rows, err := db.r.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

func noScan(t *testing.T, what, plan string, tables ...string) {
	t.Helper()
	for _, tb := range tables {
		for _, line := range strings.Split(plan, "\n") {
			if strings.HasPrefix(line, "SCAN "+tb) && !strings.Contains(line, "USING") {
				t.Errorf("%s scans %s:\n%s", what, tb, plan)
			}
		}
	}
}

// Approvals, llm_calls and hidden api sessions are kept indefinitely by
// default, and these queries run on timers or on every first contact: each
// must stay on an index instead of scanning the table.
func TestTimerAndHotPathQueriesUseIndexes(t *testing.T) {
	w := newWorld(t)
	now := ms(t0)
	noScan(t, "approval sweeper", queryPlan(t, w.db, dueApprovals, now), "approvals")
	for _, q := range []string{seenRecipient, seenHost} {
		p := queryPlan(t, w.db, q, "usr_x", "x")
		noScan(t, "approvals seen", p, "approvals")
		if !strings.Contains(p, "approvals_seen_") {
			t.Errorf("seen plan without its expression index:\n%s", p)
		}
	}
	for _, q := range []string{pruneHiddenMessages, archiveHidden} {
		p := queryPlan(t, w.db, q, now)
		noScan(t, "hidden session retention", p, "sessions", "messages")
		if !strings.Contains(p, "sessions_hidden_api") {
			t.Errorf("retention plan without sessions_hidden_api:\n%s", p)
		}
	}
	for _, uq := range []core.UsageQuery{
		{From: t0, GroupBy: "schedule"},
		{From: t0, GroupBy: "day"},
		{User: "usr_x", GroupBy: "model"},
		{User: "usr_x", From: t0, To: t0.Add(time.Hour), GroupBy: "session"},
	} {
		q, args, err := usageQuery(uq)
		if err != nil {
			t.Fatal(err)
		}
		p := queryPlan(t, w.db, q, args...)
		noScan(t, "usage "+uq.GroupBy, p, "c", "llm_calls")
		if needRuns := uq.GroupBy == "schedule" || uq.GroupBy == "session"; needRuns != strings.Contains(q, "JOIN runs") {
			t.Errorf("usage %s joins runs = %v", uq.GroupBy, !needRuns)
		}
	}
}

// The sidebar list must not walk a principal's hidden api sessions to find
// the visible ones.
func TestSessionListSkipsHiddenThroughTheIndex(t *testing.T) {
	w := newWorld(t)
	q := `SELECT ` + sessionCols + ` FROM sessions WHERE principal_id IN (?) AND archived = ? AND hidden = 0
		ORDER BY last_message_at DESC, id DESC LIMIT ?`
	if p := queryPlan(t, w.db, q, "usr_x", 0, 50); !strings.Contains(p, "hidden=?") {
		t.Errorf("list plan does not seek on hidden:\n%s", p)
	}
}

func TestHiddenSessionRetentionDropsSnapshots(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	p := core.UserPrincipal(u.ID)
	old := t0.Add(-8 * 24 * time.Hour)
	snap := func(s *core.Session) {
		s.SystemSnapshot, s.Summary, s.SnapshotHash = "persona and memory", "summary", "h"
		s.CreatedAt, s.UpdatedAt, s.LastMessageAt = old, old, old
	}
	hidden := w.session(p, snap, func(s *core.Session) { s.Origin, s.Hidden = "api", true })
	visible := w.session(p, snap)
	w.message(hidden.ID, core.ProvUser, "one-shot")
	w.db.clock = fixedClock{t0}
	if err := w.db.retain(w.ctx, config.Default()); err != nil {
		t.Fatal(err)
	}
	h, err := w.db.Sessions().Get(w.ctx, core.SystemViewer(), hidden.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Archived || h.SystemSnapshot != "" || h.Summary != "" || h.SnapshotHash != "" {
		t.Fatalf("archived hidden session = %+v", h)
	}
	if msgs, _ := w.db.Messages().Range(w.ctx, core.SystemViewer(), hidden.ID, 1, 0); len(msgs) != 0 {
		t.Fatalf("messages kept: %d", len(msgs))
	}
	v, _ := w.db.Sessions().Get(w.ctx, core.SystemViewer(), visible.ID)
	if v.Archived || v.SystemSnapshot == "" {
		t.Fatalf("visible session touched: %+v", v)
	}
}

func TestDueAndSeenStillAnswer(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	s := w.session(core.UserPrincipal(u.ID))
	r := w.run(s, core.RunRunning)
	mk := func(status core.ApprovalStatus, created, expires, park time.Time, act *core.Action) {
		a := &core.Approval{Run: r.ID, Session: s.ID, Principal: s.Principal, Action: act, Status: status,
			OnTimeout: core.TimeoutPark, Approver: core.ApproverReq{User: u.ID}, Card: core.Card{Risk: core.SERead}, CreatedAt: created, ExpiresAt: expires, ParkUntil: park}
		w.must(w.db.Approvals().Create(w.ctx, a))
	}
	mk(core.ApprovalParked, t0.Add(-3*time.Hour), t0.Add(-2*time.Hour), t0.Add(-time.Minute), nil)
	mk(core.ApprovalPending, t0.Add(-2*time.Hour), t0.Add(-time.Minute), time.Time{}, nil)
	mk(core.ApprovalPending, t0.Add(-time.Hour), t0.Add(time.Hour), time.Time{}, nil)
	mk(core.ApprovalAllowed, t0.Add(-time.Hour), t0, time.Time{}, &core.Action{Tool: "send", SideEffect: core.SEExternalSend, Recipient: "bob@example.com", Host: "api.example.com"})
	due, err := w.db.Approvals().Due(w.ctx, t0)
	if err != nil || len(due) != 2 || due[0].Status != core.ApprovalParked || due[1].Status != core.ApprovalPending {
		t.Fatalf("due = %+v, %v", due, err)
	}
	for _, c := range []struct {
		recipient, host string
		want            bool
	}{{"bob@example.com", "", true}, {"", "api.example.com", true}, {"x@example.com", "api.example.com", true},
		{"x@example.com", "other.example.com", false}, {"", "", false}} {
		if got, err := w.db.Approvals().Seen(w.ctx, s.Principal, c.recipient, c.host); err != nil || got != c.want {
			t.Errorf("Seen(%q, %q) = %v, %v", c.recipient, c.host, got, err)
		}
	}
}
