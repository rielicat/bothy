package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// Knowledge runs the core.KnowledgeRepo contract against a fresh store and
// repo per subtest; open returns both, the store holding the users and
// sessions the rows point at. internal/store and knowledgetest both run it.
//
// It pins what the §4.8-style comments leave open:
//   - Upsert assigns ID, stamps CreatedAt/UpdatedAt (2000..3000), defaults
//     Notes.Status to "none", and on a (principal, repo) conflict changes only
//     Source, Dir, SHA, Status and Error; an unknown principal or a repo name
//     without "owner/name" shape => ErrInvalid. UpdateIndex never writes SHA
//     and is the only writer of IndexedSHA once the row exists.
//   - Every read of a foreign repo or session => ErrNotFound; List and Search
//     outside the Viewer return nothing; an empty Viewer sees nothing.
//   - Apply replaces a file's chunks wholesale and sets KnowledgeFile.Chunks;
//     new chunks carry the sha given, SetSHA relabels them all.
//   - Search ANDs the query's words, falls back to OR when no chunk has all,
//     narrows by principal and repo, honours Limit, and never fails on
//     hostile input (FTS operators, quotes, column filters, huge input).
//   - Touch needs a session and a repo of the same principal (else
//     ErrInvalid); Touched lists most recent first with InjectedEpoch -1 until
//     MarkInjected, which answers ErrNotFound for an untouched pair.
//   - Delete removes the repo with its files, chunks and touches.
func Knowledge(t *testing.T, open func(t *testing.T) (core.Store, core.KnowledgeRepo)) {
	t.Helper()
	for _, c := range knowledgeSuite {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, k := open(t)
			c.run(&kfx{fx: &fx{t: t, ctx: t.Context(), s: s}, k: k})
		})
	}
}

type kfx struct {
	*fx
	k core.KnowledgeRepo
}

var knowledgeSuite = []struct {
	name string
	run  func(f *kfx)
}{
	{"Repos", testKnowledgeRepos},
	{"IDOR", testKnowledgeIDOR},
	{"Files", testKnowledgeFiles},
	{"Search", testKnowledgeSearch},
	{"HostileQueries", testKnowledgeHostile},
	{"Touches", testKnowledgeTouches},
	{"Delete", testKnowledgeDelete},
}

func (f *kfx) repo(p core.PrincipalID, name string) *core.IndexedRepo {
	f.t.Helper()
	r := &core.IndexedRepo{Principal: p, Repo: name, Source: core.KnowledgeWorkspace, Dir: "src/" + name,
		Status: core.IndexQueued}
	f.must(f.k.Upsert(f.ctx, r), "upsert "+name)
	return r
}

func chunk(path string, start, end int, title, body string) core.KnowledgeChunk {
	return core.KnowledgeChunk{Path: path, StartLine: start, EndLine: end, Kind: "text", Lang: "text", Title: title, Body: body}
}

func (f *kfx) file(repo int64, sha, path string, chunks ...core.KnowledgeChunk) {
	f.t.Helper()
	for i := range chunks {
		chunks[i].Path = path
	}
	f.must(f.k.Apply(f.ctx, repo, sha, []core.KnowledgeFileChange{{
		File: core.KnowledgeFile{Path: path, Hash: "v1:" + path, Size: int64(len(path)), Lang: "text"}, Chunks: chunks,
	}}), "apply "+path)
}

func stamped(tm time.Time) bool {
	return tm.Year() >= 2000 && tm.Year() < 3000
}

func testKnowledgeRepos(f *kfx) {
	u := f.user(core.RoleOwner)
	p := core.UserPrincipal(u.ID)
	v := viewer(u)
	r := f.repo(p, "acme/api")
	f.check(r.ID != 0 && stamped(r.CreatedAt) && stamped(r.UpdatedAt), "upsert did not assign id and times: %+v", r)
	f.eq("default notes status", r.Notes.Status, core.NotesNone)

	got, err := f.k.Get(f.ctx, v, r.ID)
	f.must(err, "get")
	f.eq("get", got, r)
	byName, err := f.k.ByName(f.ctx, v, p, "acme/api")
	f.must(err, "by name")
	f.eq("by name", byName.ID, r.ID)
	_, err = f.k.ByName(f.ctx, v, p, "acme/none")
	f.want(err, core.ErrNotFound, "by unknown name")

	f.eq("no indexed sha before an index", r.IndexedSHA, "")
	r.SHA, r.Status, r.Files, r.Chunks, r.Bytes, r.Skipped, r.Truncated = "ignored", core.IndexReady, 3, 7, 900, 2, true
	r.IndexedAt, r.IndexedSHA = at(5), "abc123"
	f.must(f.k.UpdateIndex(f.ctx, r), "update index")
	notes := core.NotesState{Status: core.NotesReady, Untrusted: true, UpdatedAt: at(6)}
	f.must(f.k.UpdateNotes(f.ctx, r.ID, notes), "update notes")
	got, err = f.k.Get(f.ctx, v, r.ID)
	f.must(err, "get after update")
	f.check(got.SHA == "" && got.Status == core.IndexReady && got.Files == 3 && got.Chunks == 7 && got.Bytes == 900 &&
		got.Skipped == 2 && got.Truncated && got.IndexedAt.Equal(at(5)) && got.IndexedSHA == "abc123", "index fields not stored: %+v", got)
	f.eq("notes", got.Notes, notes)

	again := &core.IndexedRepo{Principal: p, Repo: "acme/api", Source: core.KnowledgeGitHub, Dir: "repos/acme/api",
		SHA: "def456", Status: core.IndexQueued, Files: 99, IndexedSHA: "zzz", Notes: core.NotesState{Status: core.NotesNone}}
	f.must(f.k.Upsert(f.ctx, again), "re-upsert")
	f.eq("re-upsert keeps the id", again.ID, r.ID)
	f.check(again.Source == core.KnowledgeGitHub && again.Dir == "repos/acme/api" && again.SHA == "def456" &&
		again.Status == core.IndexQueued, "conflict did not update source/dir/sha/status: %+v", again)
	f.check(again.Files == 3 && again.Chunks == 7 && again.Notes == notes && again.IndexedSHA == "abc123",
		"conflict overwrote counters, notes or the indexed sha: %+v", again)

	f.repo(p, "acme/web")
	list, err := f.k.List(f.ctx, v, "")
	f.must(err, "list")
	f.need(len(list) == 2, "list = %d rows, want 2", len(list))
	f.check(list[0].Repo == "acme/api" && list[1].Repo == "acme/web", "list not by name: %s, %s", list[0].Repo, list[1].Repo)
	only, err := f.k.List(f.ctx, v, p)
	f.must(err, "list principal")
	f.eq("list principal", len(only), 2)

	f.want(f.k.UpdateIndex(f.ctx, &core.IndexedRepo{ID: 987654, Status: core.IndexReady}), core.ErrNotFound, "update missing index")
	f.want(f.k.UpdateNotes(f.ctx, 987654, notes), core.ErrNotFound, "update missing notes")
	f.want(f.k.Upsert(f.ctx, &core.IndexedRepo{Principal: "usr_nobody", Repo: "a/b", Source: core.KnowledgeWorkspace,
		Dir: ".", Status: core.IndexQueued}), core.ErrInvalid, "unknown principal")
	f.want(f.k.Upsert(f.ctx, &core.IndexedRepo{Principal: p, Repo: "noslash", Source: core.KnowledgeWorkspace,
		Dir: ".", Status: core.IndexQueued}), core.ErrInvalid, "repo without owner/name")
}

func testKnowledgeIDOR(f *kfx) {
	a, b := f.user(core.RoleOwner), f.user(core.RoleMember)
	pa := core.UserPrincipal(a.ID)
	r := f.repo(pa, "acme/secret")
	f.file(r.ID, "s1", "main.go", chunk("", 1, 3, "main", "func main() { zebracorn() }"))
	sa := f.session(pa)
	f.must(f.k.Touch(f.ctx, sa.ID, r.ID, at(1)), "touch")

	for name, v := range map[string]core.Viewer{"foreign": viewer(b), "empty": {User: b.ID}} {
		_, err := f.k.Get(f.ctx, v, r.ID)
		f.want(err, core.ErrNotFound, name+" get")
		_, err = f.k.ByName(f.ctx, v, pa, "acme/secret")
		f.want(err, core.ErrNotFound, name+" by name")
		_, err = f.k.Files(f.ctx, v, r.ID)
		f.want(err, core.ErrNotFound, name+" files")
		_, err = f.k.Chunks(f.ctx, v, r.ID, "")
		f.want(err, core.ErrNotFound, name+" chunks")
		_, err = f.k.Touched(f.ctx, v, sa.ID)
		f.want(err, core.ErrNotFound, name+" touched")
		f.want(f.k.Delete(f.ctx, v, r.ID), core.ErrNotFound, name+" delete")
		list, err := f.k.List(f.ctx, v, "")
		f.must(err, name+" list")
		f.eq(name+" list", len(list), 0)
		list, err = f.k.List(f.ctx, v, pa)
		f.must(err, name+" list foreign principal")
		f.eq(name+" list foreign principal", len(list), 0)
		hits, err := f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "zebracorn"})
		f.must(err, name+" search")
		f.eq(name+" search", len(hits), 0)
		hits, err = f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "zebracorn", Principal: pa, Repo: r.ID})
		f.must(err, name+" search narrowed to the foreign repo")
		f.eq(name+" search narrowed to the foreign repo", len(hits), 0)
	}
	hits, err := f.k.Search(f.ctx, viewer(a), core.KnowledgeQuery{Text: "zebracorn"})
	f.must(err, "own search")
	f.eq("own search", len(hits), 1)
	if _, err := f.k.Get(f.ctx, viewer(a), r.ID); err != nil {
		f.t.Errorf("the owner lost access after the probes: %v", err)
	}
	hits, err = f.k.Search(f.ctx, core.SystemViewer(), core.KnowledgeQuery{Text: "zebracorn"})
	f.must(err, "system search")
	f.eq("system search", len(hits), 1)
}

func testKnowledgeFiles(f *kfx) {
	u := f.user(core.RoleOwner)
	v := viewer(u)
	r := f.repo(core.UserPrincipal(u.ID), "acme/api")
	f.file(r.ID, "s1", "b.go", chunk("", 1, 4, "B", "package b"), chunk("", 5, 9, "Run", "func Run() {}"))
	f.file(r.ID, "s1", "a.md", chunk("", 1, 2, "Intro", "# Intro"))
	files, err := f.k.Files(f.ctx, v, r.ID)
	f.must(err, "files")
	f.need(len(files) == 2, "files = %+v", files)
	f.eq("files by path", files[0].Path+","+files[1].Path, "a.md,b.go")
	f.eq("chunk count", files[1].Chunks, 2)
	f.eq("hash kept", files[1].Hash, "v1:b.go")

	cs, err := f.k.Chunks(f.ctx, v, r.ID, "b.go")
	f.must(err, "chunks")
	f.need(len(cs) == 2, "chunks = %+v", cs)
	f.check(cs[0].StartLine == 1 && cs[1].StartLine == 5 && cs[1].Title == "Run" && cs[1].Body == "func Run() {}" &&
		cs[0].SHA == "s1" && cs[0].ID != 0, "chunks not stored: %+v", cs)

	f.file(r.ID, "s2", "b.go", chunk("", 1, 2, "B2", "package b // v2"))
	cs, err = f.k.Chunks(f.ctx, v, r.ID, "b.go")
	f.must(err, "chunks after replace")
	f.need(len(cs) == 1, "replace kept old chunks: %+v", cs)
	f.check(cs[0].Title == "B2" && cs[0].SHA == "s2", "replaced chunk = %+v", cs[0])
	all, err := f.k.Chunks(f.ctx, v, r.ID, "")
	f.must(err, "all chunks")
	f.eq("all chunks", len(all), 2)

	f.must(f.k.SetSHA(f.ctx, r.ID, "s3"), "set sha")
	all, err = f.k.Chunks(f.ctx, v, r.ID, "")
	f.must(err, "chunks after set sha")
	for _, c := range all {
		f.check(c.SHA == "s3", "chunk %s:%d sha = %q after SetSHA", c.Path, c.StartLine, c.SHA)
	}

	f.must(f.k.Apply(f.ctx, r.ID, "s3", []core.KnowledgeFileChange{{File: core.KnowledgeFile{Path: "a.md"}, Delete: true}}), "delete file")
	files, err = f.k.Files(f.ctx, v, r.ID)
	f.must(err, "files after delete")
	f.check(len(files) == 1 && files[0].Path == "b.go", "files after delete = %+v", files)
	all, err = f.k.Chunks(f.ctx, v, r.ID, "")
	f.must(err, "chunks after delete")
	f.check(len(all) == 1 && all[0].Path == "b.go", "chunks after delete = %+v", all)
	hits, err := f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "Intro"})
	f.must(err, "search deleted")
	f.eq("search finds a deleted file", len(hits), 0)

	f.want(f.k.Apply(f.ctx, r.ID, "s3", []core.KnowledgeFileChange{{File: core.KnowledgeFile{Path: ""}}}), core.ErrInvalid, "empty path")
	f.want(f.k.Apply(f.ctx, r.ID, "s3", []core.KnowledgeFileChange{{File: core.KnowledgeFile{Path: "x"},
		Chunks: []core.KnowledgeChunk{chunk("x", 0, 1, "", "zero line")}}}), core.ErrInvalid, "line 0")
	f.want(f.k.Apply(f.ctx, r.ID, "s3", []core.KnowledgeFileChange{{File: core.KnowledgeFile{Path: "x"},
		Chunks: []core.KnowledgeChunk{chunk("x", 5, 4, "", "end before start")}}}), core.ErrInvalid, "end before start")
}

func testKnowledgeSearch(f *kfx) {
	u := f.user(core.RoleOwner)
	sp := f.space(u)
	p := core.UserPrincipal(u.ID)
	v := viewer(u, sp.Principal)
	r1 := f.repo(p, "acme/api")
	r2 := f.repo(p, "acme/web")
	r3 := f.repo(sp.Principal, "acme/space")
	f.file(r1.ID, "c1", "limit.go", chunk("", 10, 20, "Allow", "func Allow() bool { return bucket.tokens > 0 } // rate limiter"))
	f.file(r1.ID, "c1", "docs.md", chunk("", 1, 5, "Rate", "Rate limits protect the API."))
	f.file(r2.ID, "c2", "app.ts", chunk("", 3, 4, "limiter", "export const limiter = makeLimiter()"))
	f.file(r3.ID, "c3", "notes.txt", chunk("", 1, 1, "x", "rate limiter for the space"))

	hits, err := f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "rate limiter", Principal: p})
	f.must(err, "and search")
	f.need(len(hits) == 1, "AND search = %+v, want only the chunk with both words", hits)
	h := hits[0]
	f.check(h.RepoID == r1.ID && h.Repo == "acme/api" && h.Principal == p && h.SHA == "c1" && h.Path == "limit.go" &&
		h.StartLine == 10 && h.EndLine == 20 && h.Title == "Allow" && strings.Contains(h.Snippet, "bucket.tokens"),
		"hit fields = %+v", h)

	hits, err = f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "rate limiter"})
	f.must(err, "all principals")
	f.eq("search over every principal of the Viewer", len(hits), 2)

	hits, err = f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "protect makeLimiter"})
	f.must(err, "or fallback")
	f.eq("OR fallback when no chunk has every word", len(hits), 2)

	hits, err = f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "limiter", Repo: r2.ID})
	f.must(err, "repo filter")
	f.check(len(hits) == 1 && hits[0].Path == "app.ts", "repo filter = %+v", hits)

	hits, err = f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "RATE", Limit: 1})
	f.must(err, "limit")
	f.eq("limit and case-insensitive match", len(hits), 1)

	hits, err = f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "nothingmatchesthis"})
	f.must(err, "no match")
	f.eq("no match", len(hits), 0)
}

func testKnowledgeHostile(f *kfx) {
	u := f.user(core.RoleOwner)
	v := viewer(u)
	r := f.repo(core.UserPrincipal(u.ID), "acme/api")
	f.file(r.ID, "c1", "a.go", chunk("", 1, 1, "near", `func near() { title := "AND OR NOT" }`))
	inputs := []string{
		"", " ", `"`, `""`, `"""`, `*`, `a*`, `^near`, `-near`, `+near`, `near:`, `title:near`, `{title}: near`,
		`NEAR(a b)`, `near AND`, `OR`, `NOT near`, `(near`, `near)`, `'; DROP TABLE knowledge_chunks; --`,
		"\x00near\x01", "näar ñear 近い", "near\u202e", strings.Repeat("near ", 5000), strings.Repeat(`"(`, 10000),
		strings.Repeat("a", 100000),
	}
	for _, in := range inputs {
		if _, err := f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: in}); err != nil {
			f.t.Errorf("Search(%.40q) failed: %v", in, err)
		}
	}
	hits, err := f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: `title:near`})
	f.must(err, "column filter as words")
	f.check(len(hits) == 1, "column filter text did not match as plain words: %+v", hits)
	hits, err = f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: `AND OR NOT`})
	f.must(err, "operators as words")
	f.check(len(hits) == 1, "operator words did not match as plain words: %+v", hits)
}

func testKnowledgeTouches(f *kfx) {
	a, b := f.user(core.RoleOwner), f.user(core.RoleMember)
	pa, pb := core.UserPrincipal(a.ID), core.UserPrincipal(b.ID)
	r1, r2 := f.repo(pa, "acme/one"), f.repo(pa, "acme/two")
	rb := f.repo(pb, "bob/repo")
	s := f.session(pa)
	f.must(f.k.Touch(f.ctx, s.ID, r1.ID, at(1)), "touch one")
	f.must(f.k.Touch(f.ctx, s.ID, r2.ID, at(2)), "touch two")
	f.want(f.k.Touch(f.ctx, s.ID, rb.ID, at(3)), core.ErrInvalid, "touch another principal's repo")
	f.want(f.k.Touch(f.ctx, "ses_missing", r1.ID, at(3)), core.ErrInvalid, "touch from a missing session")

	ts, err := f.k.Touched(f.ctx, viewer(a), s.ID)
	f.must(err, "touched")
	f.need(len(ts) == 2, "touched = %+v", ts)
	f.check(ts[0].Repo == r2.ID && ts[1].Repo == r1.ID, "touched not most recent first: %+v", ts)
	f.check(ts[0].InjectedEpoch == -1 && ts[0].InjectedHash == "" && ts[0].TouchedAt.Equal(at(2)), "fresh touch = %+v", ts[0])

	f.must(f.k.Touch(f.ctx, s.ID, r1.ID, at(4)), "touch one again")
	f.must(f.k.MarkInjected(f.ctx, s.ID, r1.ID, 3, "h1"), "mark injected")
	ts, err = f.k.Touched(f.ctx, viewer(a), s.ID)
	f.must(err, "touched again")
	f.need(len(ts) == 2, "touched again = %+v", ts)
	f.check(ts[0].Repo == r1.ID && ts[0].InjectedEpoch == 3 && ts[0].InjectedHash == "h1", "re-touch or mark lost: %+v", ts[0])
	f.want(f.k.MarkInjected(f.ctx, s.ID, rb.ID, 1, "x"), core.ErrNotFound, "mark an untouched pair")

	other := f.session(pa)
	ts, err = f.k.Touched(f.ctx, viewer(a), other.ID)
	f.must(err, "touched of a fresh session")
	f.eq("fresh session touches", len(ts), 0)
}

func testKnowledgeDelete(f *kfx) {
	u := f.user(core.RoleOwner)
	p := core.UserPrincipal(u.ID)
	v := viewer(u)
	r := f.repo(p, "acme/gone")
	keep := f.repo(p, "acme/kept")
	f.file(r.ID, "c1", "x.go", chunk("", 1, 1, "x", "quokka"))
	f.file(keep.ID, "c1", "y.go", chunk("", 1, 1, "y", "quokka"))
	s := f.session(p)
	f.must(f.k.Touch(f.ctx, s.ID, r.ID, at(1)), "touch")
	f.must(f.k.Delete(f.ctx, v, r.ID), "delete")
	_, err := f.k.Get(f.ctx, v, r.ID)
	f.want(err, core.ErrNotFound, "get after delete")
	_, err = f.k.Files(f.ctx, v, r.ID)
	f.want(err, core.ErrNotFound, "files after delete")
	f.want(f.k.Delete(f.ctx, v, r.ID), core.ErrNotFound, "second delete")
	hits, err := f.k.Search(f.ctx, v, core.KnowledgeQuery{Text: "quokka"})
	f.must(err, "search after delete")
	f.check(len(hits) == 1 && hits[0].RepoID == keep.ID, "search after delete = %+v", hits)
	ts, err := f.k.Touched(f.ctx, v, s.ID)
	f.must(err, "touched after delete")
	f.eq("touches after delete", len(ts), 0)
}
