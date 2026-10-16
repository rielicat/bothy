package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rielicat/bothy/internal/core"
)

// rebuildParent is a synthetic migration that rebuilds a table other tables
// reference, the way 0013 rebuilds principals, without any marker telling
// the migrator foreign keys must be off.
const rebuildParent = `
CREATE TABLE parent_new (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '') STRICT;
INSERT INTO parent_new(id) SELECT id FROM parent;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;`

const parentAndChild = `
CREATE TABLE parent (id TEXT PRIMARY KEY) STRICT;
CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES parent(id) ON DELETE CASCADE) STRICT;
INSERT INTO parent(id) VALUES ('p1'), ('p2');
INSERT INTO child(parent_id) VALUES ('p1'), ('p1'), ('p2');`

func fkOn(t *testing.T, db *sql.DB) int {
	t.Helper()
	var on int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatal(err)
	}
	return on
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestMigrationsRunWithForeignKeysOff(t *testing.T) {
	ctx := context.Background()
	openInit := func(t *testing.T) *DB {
		t.Helper()
		db, err := open(ctx, Options{Path: filepath.Join(t.TempDir(), "bothy.db")}, initOnlyFS(t), queryVersion)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}

	t.Run("rebuilding a parent keeps its children", func(t *testing.T) {
		db := openInit(t)
		ms := testMigrations(t, map[string]string{"0002_tables.sql": parentAndChild, "0003_rebuild.sql": rebuildParent})
		if _, err := migrate(ctx, db.w, ms, nil); err != nil {
			t.Fatal(err)
		}
		if n := count(t, db.w, "child"); n != 3 {
			t.Fatalf("children after the rebuild = %d, want 3 (it cascaded)", n)
		}
		if on := fkOn(t, db.w); on != 1 {
			t.Fatalf("foreign_keys on the writer after migrating = %d, want 1", on)
		}
		if _, err := db.w.Exec(`INSERT INTO child(parent_id) VALUES ('nobody')`); err == nil {
			t.Fatal("the writer accepts a dangling child: foreign keys stayed off")
		}
	})

	t.Run("a new dangling key rolls the migration back", func(t *testing.T) {
		db := openInit(t)
		ms := testMigrations(t, map[string]string{"0002_tables.sql": parentAndChild,
			"0003_orphan.sql": `DELETE FROM parent WHERE id = 'p2';`})
		if _, err := migrate(ctx, db.w, ms, nil); err == nil || !strings.Contains(err.Error(), "foreign key violation") {
			t.Fatalf("migrate = %v, want a foreign key violation", err)
		}
		if v := userVersion(t, db.w); v != 2 {
			t.Fatalf("user_version = %d after the failed migration, want 2", v)
		}
		if n := count(t, db.w, "parent"); n != 2 {
			t.Fatalf("parents = %d, the failed migration was not rolled back", n)
		}
		if on := fkOn(t, db.w); on != 1 {
			t.Fatalf("foreign_keys on the next writer connection = %d, want 1", on)
		}
	})

	t.Run("an old violation doesn't block later migrations", func(t *testing.T) {
		db := openInit(t)
		ms := testMigrations(t, map[string]string{"0002_tables.sql": parentAndChild})
		if _, err := migrate(ctx, db.w, ms, nil); err != nil {
			t.Fatal(err)
		}
		conn, err := db.w.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF; INSERT INTO child(parent_id) VALUES ('ghost'); PRAGMA foreign_keys = ON;`); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		ms = testMigrations(t, map[string]string{"0002_tables.sql": parentAndChild, "0003_rebuild.sql": rebuildParent})
		if _, err := migrate(ctx, db.w, ms, nil); err != nil {
			t.Fatalf("migrate over an old violation = %v", err)
		}
		if n := count(t, db.w, "child"); n != 4 {
			t.Fatalf("children = %d, want 4", n)
		}
	})

	t.Run("a failed restore discards the connection", func(t *testing.T) {
		db := openInit(t)
		prev := restoreFK
		restoreFK = func(context.Context, *sql.Conn) error { return errors.New("injected") }
		t.Cleanup(func() { restoreFK = prev })
		ms := testMigrations(t, map[string]string{"0002_tables.sql": parentAndChild})
		if _, err := migrate(ctx, db.w, ms, nil); err == nil || !strings.Contains(err.Error(), "injected") {
			t.Fatalf("migrate = %v, want the injected restore failure", err)
		}
		restoreFK = prev
		if on := fkOn(t, db.w); on != 1 {
			t.Fatalf("foreign_keys on the next writer connection = %d, want 1: the connection went back to the pool", on)
		}
	})
}

// team-access fixture ids: users, principals, spaces.
const (
	taOwner    = "usr_01owner0000000000000000000"
	taAdmin    = "usr_01admin0000000000000000000"
	taMember   = "usr_01member000000000000000000"
	taDisabled = "usr_01disabled0000000000000000"

	taOwnerGuest = "spc_01ownerguest00000000000000" // a guest space on the owner's Telegram DM before linking
	taGuest      = "spc_01guest7001000000000000000" // enabled guest 7001, owned by the admin
	taGuestOff   = "spc_01guest7003000000000000000" // a guest space an admin turned off
	taGuestBlk   = "spc_01guest7002000000000000000" // the blocked guest's space
	taGroup      = "spc_01group000000000000000000"  // enabled standard group, owned by the owner
	taGroupNew   = "spc_01groupnew000000000000000"  // disabled, never changed: still in Approvals
	taGroupOnce  = "spc_01grouponce00000000000000"  // turned on once, off again
)

type sqlStep struct {
	q    string
	args []any
}

func teamAccessFixtures() []sqlStep {
	user := func(id, name, role string, disabled int) sqlStep {
		return sqlStep{`INSERT INTO users(id, username, display_name, role, pw_hash, disabled, created_at, updated_at, preferred_chat, approvals_web_only)
			VALUES (?, ?, ?, ?, 'h', ?, 1, 1, 'telegram', 1)`, []any{id, name, strings.ToUpper(name[:1]) + name[1:], role, disabled}}
	}
	principal := func(id, kind, userID, profile string) sqlStep {
		return sqlStep{`INSERT INTO principals(id, kind, user_id, display_name, workspace_id, tool_profile, created_at) VALUES (?, ?, ?, ?, ?, ?, 1)`,
			[]any{id, kind, userID, id, "ws_" + id[4:], profile}}
	}
	space := func(id, kind, channel, chat, chatKind, owner string, enabled, created, updated int) sqlStep {
		return sqlStep{`INSERT INTO spaces(principal_id, kind, channel, chat_id, chat_kind, title, owner_user_id, enabled, mention_only, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`, []any{id, kind, channel, chat, chatKind, "title " + id[4:12], owner, enabled, created, updated}}
	}
	ident := func(channel, ext, status, userID, principalID, dm, firstText string) sqlStep {
		return sqlStep{`INSERT INTO channel_identities(channel, external_id, username, display_name, status, user_id, principal_id, dm_chat_id,
			first_seen_at, last_seen_at, last_notice_at, first_text) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, 1, 2, 3, ?)`,
			[]any{channel, ext, "h" + ext, "Person " + ext, status, userID, principalID, dm, firstText}}
	}
	session := func(id, principalID, origin, key, chat string) sqlStep {
		return sqlStep{`INSERT INTO sessions(id, principal_id, origin, channel_key, chat, created_at, updated_at) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 1, 1)`,
			[]any{id, principalID, origin, key, chat}}
	}
	run := func(id, sessionID, principalID, userID, status string) sqlStep {
		return sqlStep{`INSERT INTO runs(id, session_id, principal_id, user_id, trigger, status, limits, created_at) VALUES (?, ?, ?, ?, 'message', ?, '{}', 1)`,
			[]any{id, sessionID, principalID, userID, status}}
	}
	approval := func(id, runID, sessionID, principalID, status, approver string) sqlStep {
		decision := `{"verdict":"ask","approver":` + approver + `}`
		return sqlStep{`INSERT INTO approvals(id, run_id, session_id, principal_id, tool_call_id, action, decision, card, approver, status, on_timeout,
			created_at, expires_at) VALUES (?, ?, ?, ?, 'tcl_x', '{}', ?, '{}', ?, ?, 'park', 1, 2)`,
			[]any{id, runID, sessionID, principalID, decision, approver, status}}
	}
	schedule := func(id, principalID, deliver string, enabled int) sqlStep {
		return sqlStep{`INSERT INTO schedules(id, principal_id, name, kind, spec, prompt, deliver, limits, enabled, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'cron', '0 9 * * *', 'x', NULLIF(?, ''), '{}', ?, 'system', 1, 1)`, []any{id, principalID, id, deliver, enabled}}
	}
	grant := func(id, principalID, by string) sqlStep {
		return sqlStep{`INSERT INTO grants(id, principal_id, scope, kind, value, max_se, created_by, created_at) VALUES (?, ?, 'principal', 'domain', 'a.example:443', 'external_send', ?, 1)`,
			[]any{id, principalID, by}}
	}
	return []sqlStep{
		user(taOwner, "owner", "owner", 0), user(taAdmin, "admin", "admin", 0), user(taMember, "member", "member", 0), user(taDisabled, "gone", "member", 1),
		principal(taOwner, "user", taOwner, "standard"), principal(taAdmin, "user", taAdmin, "standard"),
		principal(taMember, "user", taMember, "standard"), principal(taDisabled, "user", taDisabled, "standard"),
		principal(taOwnerGuest, "space", taOwner, "restricted"), principal(taGuest, "space", taAdmin, "restricted"),
		principal(taGuestOff, "space", taAdmin, "restricted"), principal(taGuestBlk, "space", taAdmin, "restricted"),
		principal(taGroup, "space", taOwner, "standard"), principal(taGroupNew, "space", taOwner, "restricted"),
		principal(taGroupOnce, "space", taOwner, "restricted"),
		space(taOwnerGuest, "guest", "telegram", "5001", "dm", taOwner, 0, 1, 5),
		space(taGuest, "guest", "telegram", "7001", "dm", taAdmin, 1, 1, 5),
		space(taGuestOff, "guest", "telegram", "7003", "dm", taAdmin, 0, 1, 9),
		space(taGuestBlk, "guest", "telegram", "7002", "dm", taAdmin, 1, 1, 5),
		space(taGroup, "group", "telegram", "-100", "group", taOwner, 1, 1, 50),
		space(taGroupNew, "group", "slack", "C9", "group", taOwner, 0, 7, 7),
		space(taGroupOnce, "group", "discord", "300", "group", taOwner, 0, 7, 70),
		ident("telegram", "5001", "linked", taOwner, "", "5001", ""),
		ident("slack", "U1", "linked", taAdmin, "", "D1", ""),
		ident("discord", "700", "linked", taMember, "", "800", ""),
		ident("slack", "U9", "linked", taDisabled, "", "D9", ""),
		ident("telegram", "7001", "guest", "", taGuest, "7001", ""),
		ident("telegram", "7003", "guest", "", taGuestOff, "7003", ""),
		ident("telegram", "7002", "blocked", "", taGuestBlk, "7002", ""),
		ident("telegram", "8001", "pending", "", "", "8001", "hello there"),
		ident("slack", "U50", "pending", "", "", "D50", "hi from slack"),
		ident("telegram", "8002", "blocked", "", "", "8002", ""),
		// The guest's chat, run, grant, memory and a schedule delivering to the web.
		session("ses_guest", taGuest, "telegram", "telegram:7001:", `{"channel":"telegram","chat_id":"7001","kind":"dm"}`),
		{`INSERT INTO messages(session_id, seq, role, provenance, parts, created_at) VALUES ('ses_guest', 1, 'user', 'untrusted', '[]', 1)`, nil},
		run("run_guest", "ses_guest", taGuest, taAdmin, "done"),
		grant("grt_guest", taGuest, taAdmin),
		{`INSERT INTO memory_events(principal_id, file, op, actor, created_at) VALUES (?, 'MEMORY', 'add', 'agent:run_guest', 1)`, []any{taGuest}},
		schedule("sch_guest", taGuest, `{"channel":"web","chat_id":"","kind":""}`, 1),
		{`INSERT INTO uploads(id, principal_id, session_id, name, mime, size, ref, created_at) VALUES ('upl_guest', ?, 'ses_guest', 'a.txt', 'text/plain', 1, 'a', 1)`, []any{taGuest}},
		// The group's chat, parked run with its approvals, card message, schedules and grant.
		session("ses_group", taGroup, "telegram", "telegram:-100:", `{"channel":"telegram","chat_id":"-100","kind":"group"}`),
		run("run_group", "ses_group", taGroup, taOwner, "parked"),
		approval("apr_parked", "run_group", "ses_group", taGroup, "parked", `{"user_id":"`+taOwner+`","admin_only":false}`),
		approval("apr_admin", "run_group", "ses_group", taGroup, "pending", `{"user_id":"","admin_only":true}`),
		{`INSERT INTO approval_messages(approval_id, channel, chat_id, message_id) VALUES ('apr_parked', 'telegram', '5001', 'm1')`, nil},
		schedule("sch_group_dm", taGroup, "", 1),
		schedule("sch_group_chat", taGroup, `{"channel":"telegram","chat_id":"-100","kind":"group"}`, 1),
		grant("grt_group", taGroup, taOwner),
		// An account's own chat in a chat app and a schedule posting to its DM.
		session("ses_user_slack", taAdmin, "slack", "slack:D1:", `{"channel":"slack","chat_id":"D1","kind":"dm"}`),
		schedule("sch_user", taOwner, `{"channel":"telegram","chat_id":"5001","kind":"dm"}`, 1),
		{`INSERT INTO pairing_codes(code_hash, purpose, user_id, created_at, expires_at) VALUES (x'01', 'link_identity', ?, 1, 2)`, []any{taOwner}},
		{`INSERT INTO spend_daily(user_id, day, cost_micros) VALUES (?, '2026-10-15', 100), (?, '2026-10-15', 7)`, []any{taOwner, taAdmin}},
		{`INSERT INTO llm_calls(id, run_id, principal_id, user_id, purpose, provider, model, created_at) VALUES
			('llm_group', 'run_group', ?, ?, 'agent', 'p', 'm', 1), ('llm_pass', NULL, ?, ?, 'passthrough', 'p', 'm', 1)`,
			[]any{taGroup, taOwner, taAdmin, taAdmin}},
	}
}

// openAt12 builds a database at schema version 12 holding fs, closed again.
func openAt12(t *testing.T, steps []sqlStep) string {
	t.Helper()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "bothy.db")
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 12), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if _, err := old.w.ExecContext(ctx, s.q, s.args...); err != nil {
			t.Fatalf("%s: %v", s.q, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTeamAccessMigration(t *testing.T) {
	ctx := context.Background()
	p := openAt12(t, teamAccessFixtures())
	tables := []string{"sessions", "messages", "runs", "grants", "schedules", "memory_events", "approvals", "uploads", "llm_calls",
		"principals", "spaces", "channel_identities", "approval_messages"}
	before := map[string]int{}
	{
		raw, err := sql.Open("sqlite", dsn(p, true))
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range tables {
			before[tb] = count(t, raw, tb)
		}
		_ = raw.Close()
	}

	db, err := Open(ctx, Options{Path: p, BackupDir: filepath.Join(filepath.Dir(p), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sys := core.SystemViewer()

	for _, tb := range tables {
		want := before[tb]
		if tb == "principals" || tb == "spaces" {
			want += 2 // the admin and the member get fresh person spaces; the owner reuses its guest space
		}
		if got := count(t, db.w, tb); got != want {
			t.Errorf("%s rows = %d, want %d", tb, got, want)
		}
	}
	var violations int
	if err := db.w.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign_key_check after the migration = %d, %v", violations, err)
	}

	ident := func(channel, ext string) *core.ChannelIdentity {
		t.Helper()
		id, err := db.Identities().Get(ctx, channel, ext)
		if err != nil {
			t.Fatalf("identity %s:%s: %v", channel, ext, err)
		}
		return id
	}
	principal := func(id core.PrincipalID) *core.Principal {
		t.Helper()
		pr, err := db.Principals().Get(ctx, sys, id)
		if err != nil {
			t.Fatalf("principal %s: %v", id, err)
		}
		return pr
	}
	space := func(id core.PrincipalID) *core.Space {
		t.Helper()
		sp, err := db.Spaces().Get(ctx, sys, id)
		if err != nil {
			t.Fatalf("space %s: %v", id, err)
		}
		return sp
	}

	owner, admin, member := ident("telegram", "5001"), ident("slack", "U1"), ident("discord", "700")
	for _, id := range []*core.ChannelIdentity{owner, admin} {
		if id.Status != core.IdentityTeammate || !id.CanApprove || id.Via != core.ViaAdmin || id.Greet != core.GreetMoved {
			t.Errorf("owner or admin identity = %+v, want a teammate who can approve, let in by an admin, owed the moved note", id)
		}
	}
	if member.Status != core.IdentityTeammate || member.CanApprove || member.Greet != core.GreetMoved {
		t.Errorf("member identity = %+v, want a teammate who can't approve", member)
	}
	if owner.Member != core.MemberUnknown || admin.Member != core.MemberUnchecked || member.Member != core.MemberUnchecked {
		t.Errorf("membership: telegram %q, slack %q, discord %q; want unknown, never checked, never checked", owner.Member, admin.Member, member.Member)
	}
	if owner.Principal != taOwnerGuest {
		t.Errorf("owner's person space = %s, want the reused guest space %s", owner.Principal, taOwnerGuest)
	}
	if sp, pr := space(owner.Principal), principal(owner.Principal); sp.Kind != core.SpacePerson || !sp.Enabled ||
		pr.Kind != core.PrincipalPerson || pr.ToolProfile != "standard" || pr.User != "" {
		t.Errorf("owner's reused space = %+v / %+v, want an enabled, standard person space owned by the team", sp, pr)
	}
	ap := principal(admin.Principal)
	if !core.ValidID(core.PrefixSpace, string(ap.ID)) || ap.Kind != core.PrincipalPerson || ap.User != "" || ap.ToolProfile != "standard" ||
		ap.Workspace == "" || ap.DisplayName != "Person U1" {
		t.Errorf("admin's fresh person principal = %+v", ap)
	}
	if as := space(admin.Principal); as.Chat.ChatID != "U1" || as.Chat.Kind != core.ChatDM || !as.Enabled {
		t.Errorf("admin's fresh person space = %+v", as)
	}
	if principal(member.Principal).Workspace == ap.Workspace {
		t.Error("fresh person principals share a workspace")
	}

	if gone := ident("slack", "U9"); gone.Status != core.IdentityBlocked || gone.Principal != "" || gone.CanApprove {
		t.Errorf("disabled account's identity = %+v, want blocked with no principal", gone)
	}
	guest := ident("telegram", "7001")
	if guest.Status != core.IdentityTeammate || guest.Principal != taGuest || guest.Greet != "" || guest.CanApprove || guest.Member != core.MemberUnknown {
		t.Errorf("enabled guest = %+v, want a teammate on its old space with no greeting due", guest)
	}
	if pr := principal(taGuest); pr.Kind != core.PrincipalPerson || pr.ToolProfile != "standard" || pr.User != "" {
		t.Errorf("guest principal = %+v, want person, standard, owned by the team", pr)
	}
	if off := ident("telegram", "7003"); off.Status != core.IdentityBlocked {
		t.Errorf("guest an admin had turned off = %+v, want blocked", off)
	}
	if space(taGuestOff).Enabled || space(taGuestBlk).Enabled {
		t.Error("a blocked person's space is enabled")
	}
	if pend := ident("telegram", "8001"); pend.Status != core.IdentityPending || pend.FirstText != "hello there" || pend.Greet != core.GreetHello {
		t.Errorf("pending identity = %+v, want pending with its first words", pend)
	}
	if pend := ident("slack", "U50"); pend.Status != core.IdentityPending || pend.FirstText != "hi from slack" {
		t.Errorf("pending Slack identity = %+v", pend)
	}
	if blk := ident("telegram", "8002"); blk.Status != core.IdentityBlocked || blk.Principal != "" {
		t.Errorf("blocked pending identity = %+v, want unchanged", blk)
	}

	g, gp := space(taGroup), principal(taGroup)
	if g.Kind != core.SpaceGroup || gp.User != "" || gp.Kind != core.PrincipalSpace || gp.ToolProfile != "standard" || g.ReviewedAt.IsZero() {
		t.Errorf("group = %+v / %+v, want an owner-less standard channel, reviewed", g, gp)
	}
	if !space(taGroupNew).ReviewedAt.IsZero() {
		t.Error("a group never turned on left Approvals")
	}
	if once := space(taGroupOnce); once.ReviewedAt.UnixMilli() != 70 {
		t.Errorf("group once turned on: reviewed_at = %d, want its updated_at 70", once.ReviewedAt.UnixMilli())
	}

	sched := func(id core.ScheduleID) *core.Schedule {
		t.Helper()
		s, err := db.Schedules().Get(ctx, sys, id)
		if err != nil {
			t.Fatalf("schedule %s: %v", id, err)
		}
		return s
	}
	if s := sched("sch_group_dm"); s.Enabled || s.Deliver != nil {
		t.Errorf("group schedule that posted to the owner's DM = %+v, want paused with no delivery", s)
	}
	if s := sched("sch_group_chat"); !s.Enabled || s.Deliver == nil || s.Deliver.ChatID != "-100" {
		t.Errorf("group schedule posting in the group = %+v, want unchanged", s)
	}
	if s := sched("sch_guest"); s.Deliver != nil || !s.Enabled {
		t.Errorf("teammate's schedule = %+v, want it to post in their DM (deliver NULL)", s)
	}
	if s := sched("sch_user"); s.Deliver == nil || s.Deliver.Channel != "web" {
		t.Errorf("account's schedule = %+v, want web delivery", s)
	}

	parked, err := db.Approvals().Get(ctx, sys, "apr_parked")
	if err != nil {
		t.Fatal(err)
	}
	if !parked.Approver.Team || parked.Approver.User != "" || !parked.Decision.Approver.Team || parked.Decision.Approver.User != "" {
		t.Errorf("parked group approval approver = %+v / %+v, want the team", parked.Approver, parked.Decision.Approver)
	}
	adminOnly, err := db.Approvals().Get(ctx, sys, "apr_admin")
	if err != nil {
		t.Fatal(err)
	}
	if adminOnly.Approver.Team || !adminOnly.Approver.AdminOnly {
		t.Errorf("admin-only approval approver = %+v, want unchanged", adminOnly.Approver)
	}
	msgs, err := db.Approvals().Messages(ctx, "apr_parked")
	if err != nil || len(msgs) != 1 || msgs[0].Kind != core.CardMsgCard {
		t.Errorf("card message after the migration = %+v, %v", msgs, err)
	}

	runUser := func(id string) string {
		t.Helper()
		var u string
		if err := db.w.QueryRow(`SELECT user_id FROM runs WHERE id = ?`, id).Scan(&u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	if runUser("run_group") != "" || runUser("run_guest") != "" {
		t.Error("runs of team-owned principals still bill a user")
	}
	var groupCall, passCall string
	if err := db.w.QueryRow(`SELECT (SELECT user_id FROM llm_calls WHERE id = 'llm_group'), (SELECT user_id FROM llm_calls WHERE id = 'llm_pass')`).
		Scan(&groupCall, &passCall); err != nil {
		t.Fatal(err)
	}
	if groupCall != "" || passCall != taAdmin {
		t.Errorf("llm_calls user_id: group %q, passthrough %q; want '' and the admin", groupCall, passCall)
	}

	us, err := db.Sessions().Get(ctx, sys, "ses_user_slack")
	if err != nil || us.ChannelKey != "" || us.Chat != nil {
		t.Errorf("account's chat-app session = %+v, %v; want it off every chat", us, err)
	}
	if gs, err := db.Sessions().Get(ctx, sys, "ses_group"); err != nil || gs.ChannelKey != "telegram:-100:" {
		t.Errorf("group session = %+v, %v; want its chat kept", gs, err)
	}

	var cols int
	if err := db.w.QueryRow(`SELECT count(*) FROM pragma_table_info('users') WHERE name IN ('preferred_chat', 'approvals_web_only')`).Scan(&cols); err != nil || cols != 0 {
		t.Errorf("users still has the preference columns (%d, %v)", cols, err)
	}
	if err := db.w.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name = 'pairing_codes'`).Scan(&cols); err != nil || cols != 0 {
		t.Errorf("pairing_codes is still there (%d, %v)", cols, err)
	}
	if spent, err := db.Usage().Spend(ctx, taOwner, "2026-10-15"); err != nil || spent != 100 {
		t.Errorf("spend after the column rename = %d, %v", spent, err)
	}

	raw, err := db.Meta().Get(ctx, "team_access_report")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]int64
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]int64{"linked": 3, "approvers": 2, "disabled_accounts": 1, "guests": 1, "telegram_guests": 1,
		"guests_blocked": 1, "schedules_paused": 1} {
		if report[k] != want {
			t.Errorf("team_access_report.%s = %d, want %d (report %s)", k, report[k], want, raw)
		}
	}
	if report["at"] <= 0 {
		t.Errorf("team_access_report.at = %d", report["at"])
	}

	team, err := db.Principals().Team(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var teamIDs []core.PrincipalID
	for _, p := range team {
		teamIDs = append(teamIDs, p.ID)
	}
	for _, want := range []core.PrincipalID{taGroup, taGuest, owner.Principal, admin.Principal, member.Principal} {
		if !slices.Contains(teamIDs, want) {
			t.Errorf("Team() = %v, missing %s", teamIDs, want)
		}
	}
	if byP, err := db.Identities().ByPrincipal(ctx, admin.Principal); err != nil || byP.ExternalID != "U1" {
		t.Errorf("ByPrincipal(admin's space) = %+v, %v", byP, err)
	}
}

// 0013 fails, and changes nothing, when any rebuild loses a row.
func TestTeamAccessMigrationRowGuard(t *testing.T) {
	ctx := context.Background()
	p := openAt12(t, teamAccessFixtures())
	ms, err := loadMigrations(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for _, m := range ms[:13] {
		body := m.sql
		if m.version == 13 {
			const mark = "UPDATE m_rows SET after = CASE name"
			if !strings.Contains(body, mark) {
				t.Fatal("0013 lost its row-count check")
			}
			body = strings.Replace(body, mark, "DELETE FROM sessions WHERE id = 'ses_guest';\n"+mark, 1)
		}
		fsys["migrations/"+m.name] = &fstest.MapFile{Data: []byte(body)}
	}
	if db, err := open(ctx, Options{Path: p}, fsys, queryVersion); err == nil {
		_ = db.Close()
		t.Fatal("0013 migrated although a session went missing")
	}
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 12), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if v := userVersion(t, old.w); v != 12 {
		t.Fatalf("user_version = %d after the guard fired, want 12", v)
	}
	if n := count(t, old.w, "pairing_codes"); n != 1 {
		t.Fatalf("pairing_codes rows = %d, the failed migration changed the database", n)
	}
	var sessions int
	if err := old.w.QueryRow(`SELECT count(*) FROM sessions WHERE id = 'ses_guest'`).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("the deleted session is gone (%d, %v)", sessions, err)
	}
}
