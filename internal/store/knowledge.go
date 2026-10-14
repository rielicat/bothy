package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// Knowledge is the v0.2 knowledge index (0003_knowledge.sql). It is not part
// of core.Store so the store interface every fake implements stays frozen.
func (h *handle) Knowledge() core.KnowledgeRepo { return knowledgeRepo{h} }

var _ core.KnowledgeRepo = knowledgeRepo{}

type knowledgeRepo struct{ h *handle }

const (
	knowledgeRepoCols = `id, principal_id, repo, source, dir, sha, status, error, files, chunks, bytes, skipped,
		truncated, indexed_at, notes_status, notes_error, notes_untrusted, notes_updated_at, created_at, updated_at, indexed_sha`
	knowledgeChunkCols = `id, sha, path, start_line, end_line, kind, lang, title, body, terms`

	knowledgeSearchDefault = 8
	knowledgeSearchMax     = 50
	knowledgeTermRunes     = 64
	knowledgePathMax       = 1024
)

func scanIndexedRepo(sc scanner) (*core.IndexedRepo, error) {
	var r core.IndexedRepo
	var indexed, notesAt, created, updated int64
	var truncated, untrusted bool
	if err := sc.Scan(&r.ID, &r.Principal, &r.Repo, &r.Source, &r.Dir, &r.SHA, &r.Status, &r.Error, &r.Files,
		&r.Chunks, &r.Bytes, &r.Skipped, &truncated, &indexed, &r.Notes.Status, &r.Notes.Error, &untrusted,
		&notesAt, &created, &updated, &r.IndexedSHA); err != nil {
		return nil, err
	}
	r.Truncated, r.Notes.Untrusted = truncated, untrusted
	r.IndexedAt, r.Notes.UpdatedAt, r.CreatedAt, r.UpdatedAt = fromMS(indexed), fromMS(notesAt), fromMS(created), fromMS(updated)
	return &r, nil
}

func scanKnowledgeChunk(sc scanner) (core.KnowledgeChunk, error) {
	var c core.KnowledgeChunk
	err := sc.Scan(&c.ID, &c.SHA, &c.Path, &c.StartLine, &c.EndLine, &c.Kind, &c.Lang, &c.Title, &c.Body, &c.Terms)
	return c, err
}

func (r knowledgeRepo) Upsert(ctx context.Context, x *core.IndexedRepo) error {
	now := ms(r.h.now())
	notes := x.Notes.Status
	if notes == "" {
		notes = core.NotesNone
	}
	got, err := queryOne(ctx, r.h.wq, "upsert knowledge repo", scanIndexedRepo, `INSERT INTO knowledge_repos(principal_id, repo,
		source, dir, sha, status, error, files, chunks, bytes, skipped, truncated, indexed_at, notes_status, notes_error,
		notes_untrusted, notes_updated_at, created_at, updated_at, indexed_sha)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (principal_id, repo) DO UPDATE SET source = excluded.source, dir = excluded.dir, sha = excluded.sha,
			status = excluded.status, error = excluded.error, updated_at = excluded.updated_at
		RETURNING `+knowledgeRepoCols,
		string(x.Principal), x.Repo, x.Source, x.Dir, x.SHA, x.Status, x.Error, x.Files, x.Chunks, x.Bytes, x.Skipped,
		b2i(x.Truncated), ms(x.IndexedAt), notes, x.Notes.Error, b2i(x.Notes.Untrusted), ms(x.Notes.UpdatedAt), now, now, x.IndexedSHA)
	if err != nil {
		return err
	}
	*x = *got
	return nil
}

func (r knowledgeRepo) Get(ctx context.Context, v core.Viewer, id int64) (*core.IndexedRepo, error) {
	x, err := queryOne(ctx, r.h.rq, "get knowledge repo", scanIndexedRepo,
		`SELECT `+knowledgeRepoCols+` FROM knowledge_repos WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if !v.Can(x.Principal) {
		return nil, notFound("get knowledge repo")
	}
	return x, nil
}

func (r knowledgeRepo) ByName(ctx context.Context, v core.Viewer, p core.PrincipalID, repo string) (*core.IndexedRepo, error) {
	if !v.Can(p) {
		return nil, notFound("knowledge repo by name")
	}
	return queryOne(ctx, r.h.rq, "knowledge repo by name", scanIndexedRepo,
		`SELECT `+knowledgeRepoCols+` FROM knowledge_repos WHERE principal_id = ? AND repo = ?`, string(p), repo)
}

func (r knowledgeRepo) List(ctx context.Context, v core.Viewer, p core.PrincipalID) ([]*core.IndexedRepo, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok || p != "" && !v.Can(p) {
		return nil, nil
	}
	if p != "" {
		cond += ` AND principal_id = ?`
		args = append(args, string(p))
	}
	return queryAll(ctx, r.h.rq, "list knowledge repos", scanIndexedRepo,
		`SELECT `+knowledgeRepoCols+` FROM knowledge_repos WHERE `+cond+` ORDER BY repo, principal_id`, args...)
}

func (r knowledgeRepo) UpdateIndex(ctx context.Context, x *core.IndexedRepo) error {
	now := r.h.now()
	res, err := r.h.wq.ExecContext(ctx, `UPDATE knowledge_repos SET status = ?, error = ?, files = ?, chunks = ?,
		bytes = ?, skipped = ?, truncated = ?, indexed_at = ?, indexed_sha = ?, updated_at = ? WHERE id = ?`,
		x.Status, x.Error, x.Files, x.Chunks, x.Bytes, x.Skipped, b2i(x.Truncated), ms(x.IndexedAt), x.IndexedSHA, ms(now), x.ID)
	if err := mustAffect("update knowledge index", res, err); err != nil {
		return err
	}
	x.UpdatedAt = fromMS(ms(now))
	return nil
}

func (r knowledgeRepo) UpdateNotes(ctx context.Context, id int64, n core.NotesState) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE knowledge_repos SET notes_status = ?, notes_error = ?, notes_untrusted = ?,
		notes_updated_at = ? WHERE id = ?`, n.Status, n.Error, b2i(n.Untrusted), ms(n.UpdatedAt), id)
	return mustAffect("update knowledge notes", res, err)
}

// visible is the by-ID rule every repo-scoped call starts with.
func (r knowledgeRepo) visible(ctx context.Context, q querier, v core.Viewer, id int64) error {
	var p core.PrincipalID
	if err := q.QueryRowContext(ctx, `SELECT principal_id FROM knowledge_repos WHERE id = ?`, id).Scan(&p); err != nil {
		return dbErr("knowledge repo", err)
	}
	if !v.Can(p) {
		return notFound("knowledge repo")
	}
	return nil
}

func (r knowledgeRepo) Delete(ctx context.Context, v core.Viewer, id int64) error {
	return r.h.write(ctx, func(q querier) error {
		if err := r.visible(ctx, q, v, id); err != nil {
			return err
		}
		res, err := q.ExecContext(ctx, `DELETE FROM knowledge_repos WHERE id = ?`, id)
		return mustAffect("delete knowledge repo", res, err)
	})
}

func (r knowledgeRepo) Files(ctx context.Context, v core.Viewer, repo int64) ([]core.KnowledgeFile, error) {
	if err := r.visible(ctx, r.h.rq, v, repo); err != nil {
		return nil, err
	}
	return queryAll(ctx, r.h.rq, "knowledge files", func(sc scanner) (core.KnowledgeFile, error) {
		var f core.KnowledgeFile
		err := sc.Scan(&f.Path, &f.Hash, &f.Size, &f.Lang, &f.Chunks)
		return f, err
	}, `SELECT path, hash, size, lang, chunks FROM knowledge_files WHERE repo_id = ? ORDER BY path`, repo)
}

func (r knowledgeRepo) Chunks(ctx context.Context, v core.Viewer, repo int64, path string) ([]core.KnowledgeChunk, error) {
	if err := r.visible(ctx, r.h.rq, v, repo); err != nil {
		return nil, err
	}
	q, args := `SELECT `+knowledgeChunkCols+` FROM knowledge_chunks WHERE repo_id = ?`, []any{repo}
	if path != "" {
		q += ` AND path = ?`
		args = append(args, path)
	}
	return queryAll(ctx, r.h.rq, "knowledge chunks", scanKnowledgeChunk, q+` ORDER BY path, start_line, id`, args...)
}

type preparer interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

func (r knowledgeRepo) Apply(ctx context.Context, repo int64, sha string, changes []core.KnowledgeFileChange) error {
	for _, c := range changes {
		if err := validKnowledgePath(c.File.Path); err != nil {
			return err
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return r.h.write(ctx, func(q querier) error {
		insert := func(ctx context.Context, args ...any) error {
			_, err := q.ExecContext(ctx, insertChunkSQL, args...)
			return err
		}
		if p, ok := q.(preparer); ok {
			stmt, err := p.PrepareContext(ctx, insertChunkSQL)
			if err != nil {
				return dbErr("prepare knowledge chunk", err)
			}
			defer stmt.Close()
			insert = func(ctx context.Context, args ...any) error {
				_, err := stmt.ExecContext(ctx, args...)
				return err
			}
		}
		for _, c := range changes {
			f := c.File
			if _, err := q.ExecContext(ctx, `DELETE FROM knowledge_chunks WHERE repo_id = ? AND path = ?`, repo, f.Path); err != nil {
				return dbErr("replace knowledge chunks", err)
			}
			if c.Delete {
				if _, err := q.ExecContext(ctx, `DELETE FROM knowledge_files WHERE repo_id = ? AND path = ?`, repo, f.Path); err != nil {
					return dbErr("delete knowledge file", err)
				}
				continue
			}
			for _, ch := range c.Chunks {
				if err := insert(ctx, repo, sha, f.Path, ch.StartLine, ch.EndLine, ch.Kind, ch.Lang, ch.Title, ch.Body, ch.Terms); err != nil {
					return dbErr("insert knowledge chunk", err)
				}
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO knowledge_files(repo_id, path, hash, size, lang, chunks) VALUES (?,?,?,?,?,?)
				ON CONFLICT (repo_id, path) DO UPDATE SET hash = excluded.hash, size = excluded.size, lang = excluded.lang,
				chunks = excluded.chunks`, repo, f.Path, f.Hash, f.Size, f.Lang, len(c.Chunks)); err != nil {
				return dbErr("upsert knowledge file", err)
			}
		}
		return nil
	})
}

const insertChunkSQL = `INSERT INTO knowledge_chunks(repo_id, sha, path, start_line, end_line, kind, lang, title, body, terms)
	VALUES (?,?,?,?,?,?,?,?,?,?)`

func validKnowledgePath(p string) error {
	if p == "" || len(p) > knowledgePathMax || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return invalid("knowledge file path")
	}
	return nil
}

func (r knowledgeRepo) SetSHA(ctx context.Context, repo int64, sha string) error {
	_, err := r.h.wq.ExecContext(ctx, `UPDATE knowledge_chunks SET sha = ? WHERE repo_id = ? AND sha <> ?`, sha, repo, sha)
	return dbErr("relabel knowledge chunks", err)
}

// knowledgeTerms splits input into runs of letters, digits and marks (what
// unicode61 tokenizes), so no FTS syntax survives: quotes, NEAR, column
// filters, prefixes and operators become separators or quoted plain words.
func knowledgeTerms(input string) []string {
	terms := strings.FieldsFunc(input, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	})
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, min(len(terms), maxSearchTerms))
	for _, t := range terms {
		if rs := []rune(t); len(rs) > knowledgeTermRunes {
			t = string(rs[:knowledgeTermRunes])
		}
		if k := strings.ToLower(t); !seen[k] {
			seen[k] = true
			out = append(out, `"`+t+`"`)
		}
		if len(out) == maxSearchTerms {
			break
		}
	}
	return out
}

func (r knowledgeRepo) Search(ctx context.Context, v core.Viewer, q core.KnowledgeQuery) ([]core.KnowledgeHit, error) {
	terms := knowledgeTerms(q.Text)
	if len(terms) == 0 {
		return nil, nil
	}
	cond, args, ok := viewerCond("r.principal_id", v)
	if !ok || q.Principal != "" && !v.Can(q.Principal) {
		return nil, nil
	}
	if q.Principal != "" {
		cond += ` AND r.principal_id = ?`
		args = append(args, string(q.Principal))
	}
	if q.Repo != 0 {
		cond += ` AND r.id = ?`
		args = append(args, q.Repo)
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = knowledgeSearchDefault
	case limit > knowledgeSearchMax:
		limit = knowledgeSearchMax
	}
	run := func(match string) ([]core.KnowledgeHit, error) {
		a := append([]any{match}, args...)
		a = append(a, limit)
		return queryAll(ctx, r.h.rq, "search knowledge", func(sc scanner) (core.KnowledgeHit, error) {
			var h core.KnowledgeHit
			err := sc.Scan(&h.RepoID, &h.Repo, &h.Principal, &h.SHA, &h.Path, &h.StartLine, &h.EndLine, &h.Kind, &h.Lang,
				&h.Title, &h.Snippet, &h.Rank)
			return h, err
		}, `SELECT r.id, r.repo, r.principal_id, c.sha, c.path, c.start_line, c.end_line, c.kind, c.lang, c.title, c.body,
			bm25(knowledge_fts, 3.0, 5.0, 1.0, 0.5) AS rank
			FROM knowledge_fts
			JOIN knowledge_chunks c ON c.id = knowledge_fts.rowid
			JOIN knowledge_repos r ON r.id = c.repo_id
			WHERE knowledge_fts MATCH ? AND `+cond+`
			ORDER BY rank, c.id LIMIT ?`, a...)
	}
	hits, err := run(strings.Join(terms, " "))
	if err != nil || len(hits) > 0 || len(terms) == 1 {
		return hits, err
	}
	return run(strings.Join(terms, " OR "))
}

func (r knowledgeRepo) Touch(ctx context.Context, s core.SessionID, repo int64, at time.Time) error {
	res, err := r.h.wq.ExecContext(ctx, `INSERT INTO knowledge_sessions(session_id, repo_id, touched_at)
		SELECT s.id, k.id, ? FROM sessions s JOIN knowledge_repos k ON k.principal_id = s.principal_id
		WHERE s.id = ? AND k.id = ?
		ON CONFLICT (session_id, repo_id) DO UPDATE SET touched_at = excluded.touched_at`, ms(at), string(s), repo)
	if err != nil {
		return dbErr("touch knowledge repo", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return dbErr("touch knowledge repo", err)
		}
		return invalid("touch knowledge repo: no such session and repo of one principal")
	}
	return nil
}

func (r knowledgeRepo) Touched(ctx context.Context, v core.Viewer, s core.SessionID) ([]core.KnowledgeTouch, error) {
	if err := r.h.visibleSession(ctx, r.h.rq, v, s); err != nil {
		return nil, err
	}
	return queryAll(ctx, r.h.rq, "knowledge touches", func(sc scanner) (core.KnowledgeTouch, error) {
		var t core.KnowledgeTouch
		var at int64
		err := sc.Scan(&t.Repo, &at, &t.InjectedEpoch, &t.InjectedHash)
		t.TouchedAt = fromMS(at)
		return t, err
	}, `SELECT repo_id, touched_at, injected_epoch, injected_hash FROM knowledge_sessions WHERE session_id = ?
		ORDER BY touched_at DESC, repo_id`, string(s))
}

func (r knowledgeRepo) MarkInjected(ctx context.Context, s core.SessionID, repo int64, epoch int, hash string) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE knowledge_sessions SET injected_epoch = ?, injected_hash = ?
		WHERE session_id = ? AND repo_id = ?`, epoch, hash, string(s), repo)
	return mustAffect("mark knowledge injected", res, err)
}
