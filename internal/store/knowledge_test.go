package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func (w *world) knowledgeRepo(p core.PrincipalID, name string) *core.IndexedRepo {
	w.t.Helper()
	r := &core.IndexedRepo{Principal: p, Repo: name, Source: core.KnowledgeWorkspace, Dir: ".", Status: core.IndexQueued}
	w.must(w.db.Knowledge().Upsert(w.ctx, r))
	return r
}

func (w *world) knowledgeFile(repo int64, path string, chunks ...core.KnowledgeChunk) {
	w.t.Helper()
	w.must(w.db.Knowledge().Apply(w.ctx, repo, "sha1", []core.KnowledgeFileChange{{
		File: core.KnowledgeFile{Path: path, Hash: "v1:x", Size: 1}, Chunks: chunks}}))
}

// ftsIntegrity runs FTS5's own consistency check of the external-content
// index against knowledge_chunks.
func (w *world) ftsIntegrity() {
	w.t.Helper()
	if _, err := w.db.w.ExecContext(w.ctx, `INSERT INTO knowledge_fts(knowledge_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		w.t.Fatalf("knowledge_fts integrity-check: %v", err)
	}
}

func (w *world) ftsRows() int {
	w.t.Helper()
	var n int
	w.must(w.db.w.QueryRowContext(w.ctx, `SELECT count(*) FROM knowledge_fts`).Scan(&n))
	return n
}

func kchunk(start, end int, title, body string) core.KnowledgeChunk {
	return core.KnowledgeChunk{StartLine: start, EndLine: end, Kind: "func", Lang: "go", Title: title, Body: body}
}

func TestKnowledgeFTSStaysConsistent(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleOwner)
	p := core.UserPrincipal(u.ID)
	r := w.knowledgeRepo(p, "acme/api")
	for i := range 20 {
		w.knowledgeFile(r.ID, fmt.Sprintf("f%02d.go", i), kchunk(1, 3, "Alpha", "alpha beta"), kchunk(4, 9, "Gamma", "gamma delta"))
	}
	w.ftsIntegrity()
	if n := w.ftsRows(); n != 40 {
		t.Fatalf("fts rows = %d, want 40", n)
	}
	w.knowledgeFile(r.ID, "f00.go", kchunk(1, 2, "Epsilon", "epsilon"))
	w.must(w.db.Knowledge().Apply(w.ctx, r.ID, "sha2", []core.KnowledgeFileChange{{File: core.KnowledgeFile{Path: "f01.go"}, Delete: true}}))
	w.must(w.db.Knowledge().SetSHA(w.ctx, r.ID, "sha3"))
	w.ftsIntegrity()
	if n := w.ftsRows(); n != 37 {
		t.Fatalf("fts rows after replace and delete = %d, want 37", n)
	}

	// VACUUM renumbers implicit rowids; INTEGER PRIMARY KEY chunk ids must
	// keep the FTS rows pointing at the right chunks.
	if _, err := w.db.w.ExecContext(w.ctx, `VACUUM`); err != nil {
		t.Fatal(err)
	}
	w.ftsIntegrity()
	hits, err := w.db.Knowledge().Search(w.ctx, viewerOf(u), core.KnowledgeQuery{Text: "epsilon"})
	if err != nil || len(hits) != 1 || hits[0].Path != "f00.go" || hits[0].SHA != "sha3" {
		t.Fatalf("search after VACUUM = %+v, %v", hits, err)
	}
}

func TestKnowledgeCascadesFromPrincipal(t *testing.T) {
	w := newWorld(t)
	keep, gone := w.user(core.RoleOwner), w.user(core.RoleMember)
	rk := w.knowledgeRepo(core.UserPrincipal(keep.ID), "acme/kept")
	rg := w.knowledgeRepo(core.UserPrincipal(gone.ID), "acme/gone")
	w.knowledgeFile(rk.ID, "a.go", kchunk(1, 1, "a", "wombat"))
	w.knowledgeFile(rg.ID, "b.go", kchunk(1, 1, "b", "wombat"), kchunk(2, 2, "c", "wombat again"))
	s := w.session(core.UserPrincipal(gone.ID))
	w.must(w.db.Knowledge().Touch(w.ctx, s.ID, rg.ID, t0))

	w.must(w.db.Users().Delete(w.ctx, gone.ID))
	w.ftsIntegrity()
	if n := w.ftsRows(); n != 1 {
		t.Fatalf("fts rows after the user purge = %d, want only the kept user's chunk", n)
	}
	for _, table := range []string{"knowledge_repos", "knowledge_files", "knowledge_chunks", "knowledge_sessions"} {
		var n int
		w.must(w.db.w.QueryRowContext(w.ctx, `SELECT count(*) FROM `+table+` WHERE `+map[string]string{
			"knowledge_repos": "id = ?", "knowledge_files": "repo_id = ?", "knowledge_chunks": "repo_id = ?",
			"knowledge_sessions": "repo_id = ?"}[table], rg.ID).Scan(&n))
		if n != 0 {
			t.Errorf("%s keeps %d rows of the purged user", table, n)
		}
	}
	hits, err := w.db.Knowledge().Search(w.ctx, core.SystemViewer(), core.KnowledgeQuery{Text: "wombat"})
	if err != nil || len(hits) != 1 || hits[0].RepoID != rk.ID {
		t.Fatalf("search after purge = %+v, %v", hits, err)
	}
}

func TestKnowledgeRanking(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleOwner)
	r := w.knowledgeRepo(core.UserPrincipal(u.ID), "acme/api")
	w.knowledgeFile(r.ID, "body.go", kchunk(1, 5, "Other", "the scheduler runs jobs; other words to dilute"))
	w.knowledgeFile(r.ID, "title.go", kchunk(1, 5, "Scheduler", "runs jobs"))
	camel := kchunk(1, 3, "NewIndexer", "func NewIndexer() *Indexer")
	camel.Terms = "new indexer"
	w.knowledgeFile(r.ID, "camel.go", camel)

	hits, err := w.db.Knowledge().Search(w.ctx, viewerOf(u), core.KnowledgeQuery{Text: "scheduler"})
	if err != nil || len(hits) != 2 {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	if hits[0].Path != "title.go" || hits[0].Rank > hits[1].Rank {
		t.Errorf("a title match should rank first: %+v", hits)
	}
	hits, err = w.db.Knowledge().Search(w.ctx, viewerOf(u), core.KnowledgeQuery{Text: "indexer"})
	if err != nil || len(hits) != 1 || hits[0].Path != "camel.go" {
		t.Errorf("identifier parts were not searchable: %+v, %v", hits, err)
	}
}

func TestKnowledgeMigratesExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	ctx := context.Background()
	old, err := open(ctx, Options{Path: p}, initOnlyFS(t), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	// Raw SQL: the repository writes columns later migrations add.
	u := &core.User{ID: core.UserID(core.NewID(core.PrefixUser))}
	if _, err := old.w.ExecContext(ctx, `INSERT INTO users(id, username, role, pw_hash, created_at, updated_at) VALUES (?, 'owner', 'owner', 'h', 1, 1)`, string(u.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.w.ExecContext(ctx, `INSERT INTO principals(id, kind, user_id, display_name, workspace_id, tool_profile, created_at)
		VALUES (?, 'user', ?, 'owner', ?, 'standard', 1)`, string(u.ID), string(u.ID), core.NewID(core.PrefixWorkspace)); err != nil {
		t.Fatal(err)
	}
	call := core.CallInfo{ID: core.NewID(core.PrefixLLMCall), Ref: core.ModelRef{Provider: "p", Model: "m"}, Purpose: "title",
		Usage: core.Usage{Input: 7}, Cost: 11}
	// Rows deleted and added again leave the rowid out of insertion order;
	// the rebuild must keep each row's rowid, since listings break ties on it.
	var rowids []int64
	for i := range 3 {
		c := call
		if i > 0 {
			c.ID = core.NewID(core.PrefixLLMCall)
		}
		if err := old.Runs().AddLLMCall(ctx, "", core.UserPrincipal(u.ID), u.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.w.ExecContext(ctx, `UPDATE llm_calls SET rowid = rowid + 100 WHERE id = ?`, call.ID); err != nil {
		t.Fatal(err)
	}
	rowidsOf := func(d *DB) []int64 {
		t.Helper()
		var out []int64
		rows, err := d.w.QueryContext(ctx, `SELECT rowid FROM llm_calls ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	rowids = rowidsOf(old)
	old.Close()
	db, err := Open(ctx, Options{Path: p, BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := rowidsOf(db); !slices.Equal(got, rowids) {
		t.Fatalf("llm_calls rowids %v after the rebuild, want %v", got, rowids)
	}
	r := &core.IndexedRepo{Principal: core.UserPrincipal(u.ID), Repo: "acme/api", Source: core.KnowledgeWorkspace, Dir: ".",
		Status: core.IndexQueued}
	if err := db.Knowledge().Upsert(ctx, r); err != nil || r.ID == 0 {
		t.Fatalf("upsert after upgrade: %+v %v", r, err)
	}
	rows, err := db.Usage().Report(ctx, core.UsageQuery{GroupBy: "user", From: time.UnixMilli(0), To: time.Now().Add(time.Hour)})
	if err != nil || len(rows) != 1 || rows[0].Input != 21 || rows[0].Cost != 33 {
		t.Fatalf("llm_calls rows lost in the rebuild: %+v %v", rows, err)
	}
	notes := core.CallInfo{ID: core.NewID(core.PrefixLLMCall), Ref: core.ModelRef{Provider: "p", Model: "m"}, Purpose: "notes"}
	if err := db.Runs().AddLLMCall(ctx, "", core.UserPrincipal(u.ID), u.ID, notes); err != nil {
		t.Fatalf("purpose notes: %v", err)
	}
	if err := db.Runs().AddLLMCall(ctx, "", core.UserPrincipal(u.ID), u.ID, core.CallInfo{ID: core.NewID(core.PrefixLLMCall),
		Ref: core.ModelRef{Provider: "p", Model: "m"}, Purpose: "chat"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("purpose CHECK lost in the rebuild: %v", err)
	}
	var idx int
	if err := db.w.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND tbl_name = 'llm_calls' AND name LIKE 'llm_calls_%'`).Scan(&idx); err != nil || idx != 3 {
		t.Fatalf("llm_calls indexes = %d %v", idx, err)
	}
}

// 0010 stems knowledge search: a database indexed before it finds
// "redirect" for "redirects" once upgraded, and new chunks keep reaching the
// rebuilt index through the old triggers.
func TestKnowledgeStemmingMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bothy.db")
	ctx := context.Background()
	old, err := open(ctx, Options{Path: p}, migrationsUpTo(t, 9), queryVersion)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, ctx: ctx, db: old}
	u := w.user(core.RoleOwner)
	r := w.knowledgeRepo(core.UserPrincipal(u.ID), "acme/web")
	w.knowledgeFile(r.ID, "src/auth/callback.ts", kchunk(1, 3, "callback", "return Response.redirect(next, 302);"))
	search := func(q string) []core.KnowledgeHit {
		t.Helper()
		hits, err := w.db.Knowledge().Search(ctx, viewerOf(u), core.KnowledgeQuery{Text: q})
		w.must(err)
		return hits
	}
	if hits := search("redirects"); len(hits) != 0 {
		t.Fatalf("before 0010 a plural matched: %+v", hits)
	}
	old.Close()

	db, err := Open(ctx, Options{Path: p, BackupDir: filepath.Join(dir, "backups")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w.db = db
	w.ftsIntegrity()
	for _, q := range []string{"redirect", "redirects", "redirecting", "Redirected"} {
		if hits := search(q); len(hits) != 1 || hits[0].Path != "src/auth/callback.ts" {
			t.Errorf("search %q after 0010 = %+v", q, hits)
		}
	}
	w.knowledgeFile(r.ID, "src/auth/session.ts", kchunk(1, 2, "session", "res.cookie(name, value)"))
	w.ftsIntegrity()
	if hits := search("cookies"); len(hits) != 1 || hits[0].Path != "src/auth/session.ts" {
		t.Errorf("a chunk added after 0010 is not stemmed: %+v", hits)
	}
	w.must(db.Knowledge().Apply(ctx, r.ID, "sha2", []core.KnowledgeFileChange{{File: core.KnowledgeFile{Path: "src/auth/callback.ts"}, Delete: true}}))
	w.ftsIntegrity()
	if n := w.ftsRows(); n != 1 {
		t.Fatalf("fts rows after a delete = %d, want 1", n)
	}
}
