package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// 0015 files existing schedule results under their schedule (not a
// schedule's own target chat), stops listing past all-clear check-ins,
// gives result titles the check-in's name and a human date, and marks every
// existing account as welcomed.
func TestRelatedChatsMigration(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "bothy.db")
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 14), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	const u = "usr_01owner0000000000000000"
	session := func(id, origin, title string) sqlStep {
		return sqlStep{`INSERT INTO sessions(id, principal_id, origin, title, created_at, updated_at, last_message_at) VALUES (?, ?, ?, ?, 1, 1, 1)`,
			[]any{id, u, origin, title}}
	}
	run := func(id, sessionID, trigger, status, schedule string) sqlStep {
		return sqlStep{`INSERT INTO runs(id, session_id, principal_id, user_id, trigger, schedule_id, status, limits, created_at)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, '{}', 1)`, []any{id, sessionID, u, u, trigger, schedule, status}}
	}
	msg := func(sessionID string, seq int, role, kind, text string) sqlStep {
		return sqlStep{`INSERT INTO messages(session_id, seq, role, kind, provenance, parts, text, created_at) VALUES (?, ?, ?, ?, 'system', '[]', ?, 1)`,
			[]any{sessionID, seq, role, kind, text}}
	}
	schedule := func(id, name, kind, target string) sqlStep {
		return sqlStep{`INSERT INTO schedules(id, principal_id, name, kind, spec, prompt, session_id, limits, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, '0 9 * * *', 'x', NULLIF(?, ''), '{}', 'system', 1, 1)`, []any{id, u, name, kind, target}}
	}
	checkIn := func(id, title, status, reply string) []sqlStep {
		return []sqlStep{session(id, "schedule", title), run("run_"+id, id, "heartbeat", status, "sch_hb"),
			msg(id, 1, "user", "normal", "Here is HEARTBEAT.md."), msg(id, 2, "assistant", "normal", reply)}
	}
	steps := []sqlStep{
		{`INSERT INTO users(id, username, role, pw_hash, created_at, updated_at) VALUES (?, 'owner', 'owner', 'h', 1700000000000, 1)`, []any{u}},
		{`INSERT INTO users(id, username, role, pw_hash, created_at, updated_at) VALUES ('usr_01zero00000000000000000', 'zero', 'member', 'h', 0, 0)`, nil},
		{`INSERT INTO principals(id, kind, user_id, workspace_id, tool_profile, created_at) VALUES (?, 'user', ?, 'ws_o', 'standard', 1)`, []any{u, u}},
		session("ses_target", "schedule", "Standup · 2026-01-02"),
		schedule("sch_hb", "heartbeat", "heartbeat", ""),
		schedule("sch_pr", "Weekly PR digest", "cron", ""),
		schedule("sch_target", "Standup", "cron", "ses_target"),
		run("run_target", "ses_target", "schedule", "done", "sch_target"),
		session("ses_pr", "schedule", "Weekly PR digest · 2026-10-19"),
		run("run_pr", "ses_pr", "schedule", "done", "sch_pr"),
		session("ses_orphan", "schedule", "Gone · 2026-03-04"),
		run("run_orphan", "ses_orphan", "schedule", "done", ""),
		session("ses_web", "web", "heartbeat · 2026-10-05"),
		run("run_web", "ses_web", "message", "done", ""),
	}
	steps = append(steps, checkIn("ses_quiet", "heartbeat · 2026-10-05", "done", "HEARTBEAT_OK")...)
	steps = append(steps, checkIn("ses_emph", "heartbeat · 2026-10-06", "done", "**HEARTBEAT_OK**")...)
	steps = append(steps, checkIn("ses_found", "heartbeat · 2026-10-12", "done", "The deploy to staging failed an hour ago.")...)
	steps = append(steps, checkIn("ses_failed", "heartbeat · 2026-10-13", "failed", "HEARTBEAT_OK")...)
	steps = append(steps, checkIn("ses_asked", "heartbeat · 2026-10-14", "done", "HEARTBEAT_OK")...)
	steps = append(steps, sqlStep{`INSERT INTO approvals(id, run_id, session_id, principal_id, tool_call_id, action, decision, card, approver, status,
		on_timeout, created_at, expires_at) VALUES ('apr_asked', 'run_ses_asked', 'ses_asked', ?, 'tcl_x', '{}', '{}', '{}', '{}', 'allowed', 'park', 1, 2)`,
		[]any{u}})
	for _, s := range steps {
		if _, err := old.w.ExecContext(ctx, s.q, s.args...); err != nil {
			t.Fatalf("%s: %v", s.q, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, Options{Path: p, BackupDir: filepath.Join(filepath.Dir(p), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, c := range []struct {
		id       core.SessionID
		schedule core.ScheduleID
		title    string
		hidden   bool
	}{
		{"ses_target", "", "Standup · 2026-01-02", false},
		{"ses_pr", "sch_pr", "Weekly PR digest · 19 Oct", false},
		{"ses_orphan", "", "Gone · 2026-03-04", false},
		{"ses_web", "", "heartbeat · 2026-10-05", false},
		{"ses_quiet", "sch_hb", "Check-in · 5 Oct", true},
		{"ses_emph", "sch_hb", "Check-in · 6 Oct", true},
		{"ses_found", "sch_hb", "Check-in · 12 Oct", false},
		{"ses_failed", "sch_hb", "Check-in · 13 Oct", false},
		{"ses_asked", "sch_hb", "Check-in · 14 Oct", false},
	} {
		s, err := db.Sessions().Get(ctx, core.SystemViewer(), c.id)
		if err != nil {
			t.Fatal(err)
		}
		if s.ScheduleID != c.schedule || s.Title != c.title || s.Hidden != c.hidden || s.ParentID != "" || s.RootID != "" || s.TitleAuto {
			t.Errorf("%s after 0015 = schedule %q title %q hidden %v parent %q root %q auto %v; want %q, %q, hidden %v",
				c.id, s.ScheduleID, s.Title, s.Hidden, s.ParentID, s.RootID, s.TitleAuto, c.schedule, c.title, c.hidden)
		}
	}
	for id, want := range map[core.UserID]time.Time{u: time.UnixMilli(1700000000000).UTC(), "usr_01zero00000000000000000": time.UnixMilli(1).UTC()} {
		got, err := db.Users().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !got.WelcomedAt.Equal(want) {
			t.Errorf("%s welcomed_at = %v, want %v", id, got.WelcomedAt, want)
		}
		if won, err := db.Users().ClaimWelcome(ctx, id, t0); err != nil || won {
			t.Errorf("an account from before 0015 claimed a welcome chat: %v %v", won, err)
		}
	}
	var n int
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = 'sessions_hidden_api'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions_hidden_api kept (%d, %v)", n, err)
	}
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n); err != nil || n != 0 {
		t.Errorf("foreign key check: %d %v", n, err)
	}
}

// A check-in with nothing to report is archived after 7 days like a hidden
// API one-shot, its messages deleted; listed schedule results, newer quiet
// check-ins and hidden chats of other origins stay.
func TestQuietCheckInRetention(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	p := core.UserPrincipal(u.ID)
	old, recent := t0.Add(-8*24*time.Hour), t0.Add(-6*24*time.Hour)
	aged := func(when time.Time) func(*core.Session) {
		return func(s *core.Session) { s.CreatedAt, s.UpdatedAt, s.LastMessageAt = when, when, when }
	}
	quiet := w.session(p, aged(old), func(s *core.Session) { s.Origin, s.Hidden, s.ScheduleID = "schedule", true, "sch_hb" })
	fresh := w.session(p, aged(recent), func(s *core.Session) { s.Origin, s.Hidden, s.ScheduleID = "schedule", true, "sch_hb" })
	listed := w.session(p, aged(old), func(s *core.Session) { s.Origin, s.ScheduleID = "schedule", "sch_pr" })
	hiddenWeb := w.session(p, aged(old), func(s *core.Session) { s.Hidden = true })
	for _, s := range []*core.Session{quiet, fresh, listed, hiddenWeb} {
		w.message(s.ID, core.ProvUser, "Here is the list")
	}
	w.db.clock = fixedClock{t0}
	if err := w.db.retain(w.ctx, config.Default()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		s        *core.Session
		archived bool
	}{{quiet, true}, {fresh, false}, {listed, false}, {hiddenWeb, false}} {
		got, err := w.db.Sessions().Get(w.ctx, core.SystemViewer(), c.s.ID)
		if err != nil {
			t.Fatal(err)
		}
		msgs, _ := w.db.Messages().Range(w.ctx, core.SystemViewer(), c.s.ID, 1, 0)
		if got.Archived != c.archived || (len(msgs) == 0) != c.archived {
			t.Errorf("%s (%s, hidden %v): archived %v with %d messages, want archived %v", c.s.Title, c.s.Origin, c.s.Hidden, got.Archived, len(msgs), c.archived)
		}
	}
}
