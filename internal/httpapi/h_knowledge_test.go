package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// fakeKnowledge answers like the real service: rows outside the Viewer are
// ErrNotFound, notes writes check the etag.
type fakeKnowledge struct {
	core.Knowledge
	mu        sync.Mutex
	next      int64
	repos     map[int64]*core.IndexedRepo
	notes     map[int64]string
	lastIndex core.KnowledgeIndexRequest
	lastQuery core.KnowledgeQuery
	purged    []core.PrincipalID
	drafted   []int64
}

func newFakeKnowledge() *fakeKnowledge {
	return &fakeKnowledge{repos: map[int64]*core.IndexedRepo{}, notes: map[int64]string{}}
}

func (f *fakeKnowledge) add(p core.PrincipalID, name string) *core.IndexedRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	r := &core.IndexedRepo{ID: f.next, Principal: p, Repo: name, Source: core.KnowledgeWorkspace, Dir: ".", Status: core.IndexReady}
	f.repos[r.ID] = r
	return r
}

func (f *fakeKnowledge) get(v core.Viewer, id int64) (*core.IndexedRepo, error) {
	r, ok := f.repos[id]
	if !ok || !v.Can(r.Principal) {
		return nil, core.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *fakeKnowledge) Repos(_ context.Context, v core.Viewer, p core.PrincipalID) ([]*core.IndexedRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*core.IndexedRepo
	for _, r := range f.repos {
		if v.Can(r.Principal) && (p == "" || p == r.Principal) {
			cp := *r
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *core.IndexedRepo) int { return int(a.ID - b.ID) })
	return out, nil
}

func (f *fakeKnowledge) Repo(_ context.Context, v core.Viewer, id int64) (*core.IndexedRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.get(v, id)
}

func (f *fakeKnowledge) Index(_ context.Context, v core.Viewer, p core.PrincipalID, req core.KnowledgeIndexRequest, _ core.UserID) (*core.IndexedRepo, error) {
	if !v.Can(p) {
		return nil, core.ErrNotFound
	}
	if req.Dir == "../x" {
		return nil, fmt.Errorf("%w: directory is outside the workspace", core.ErrInvalid)
	}
	f.mu.Lock()
	f.lastIndex = req
	f.mu.Unlock()
	r := f.add(p, req.Repo)
	r.Status = core.IndexQueued
	return r, nil
}

func (f *fakeKnowledge) Reindex(_ context.Context, v core.Viewer, id int64, _ core.UserID) (*core.IndexedRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(v, id)
	if err != nil {
		return nil, err
	}
	r.Status = core.IndexQueued
	return r, nil
}

func (f *fakeKnowledge) Search(_ context.Context, v core.Viewer, q core.KnowledgeQuery) ([]core.KnowledgeHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastQuery = q
	if q.Text == "" {
		return nil, fmt.Errorf("%w: query is empty", core.ErrInvalid)
	}
	var out []core.KnowledgeHit
	for _, r := range f.repos {
		if v.Can(r.Principal) && (q.Repo == 0 || q.Repo == r.ID) {
			out = append(out, core.KnowledgeHit{RepoID: r.ID, Repo: r.Repo, Principal: r.Principal, Path: "a.go", StartLine: 1,
				EndLine: 2, Citation: r.Repo + ":a.go#L1-L2"})
		}
	}
	return out, nil
}

func (f *fakeKnowledge) notesOf(r *core.IndexedRepo) *core.KnowledgeNotes {
	c := f.notes[r.ID]
	etag := ""
	if c != "" {
		etag = "e" + strconv.Itoa(len(c))
	}
	return &core.KnowledgeNotes{RepoID: r.ID, Repo: r.Repo, Content: c, ETag: etag, CapBytes: 16384}
}

func (f *fakeKnowledge) Notes(_ context.Context, v core.Viewer, id int64) (*core.KnowledgeNotes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(v, id)
	if err != nil {
		return nil, err
	}
	return f.notesOf(r), nil
}

func (f *fakeKnowledge) WriteNotes(_ context.Context, v core.Viewer, id int64, content, etag string, _ core.UserID) (*core.KnowledgeNotes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(v, id)
	if err != nil {
		return nil, err
	}
	if f.notesOf(r).ETag != etag {
		return nil, fmt.Errorf("%w: stale", core.ErrConflict)
	}
	f.notes[id] = content
	return f.notesOf(r), nil
}

func (f *fakeKnowledge) DraftNotes(_ context.Context, v core.Viewer, id int64, _ core.UserID) (*core.IndexedRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(v, id)
	if err != nil {
		return nil, err
	}
	f.drafted = append(f.drafted, id)
	r.Notes.Status = core.NotesDrafting
	return r, nil
}

func (f *fakeKnowledge) Delete(_ context.Context, v core.Viewer, id int64, _ core.UserID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(v, id); err != nil {
		return err
	}
	delete(f.repos, id)
	return nil
}

func (f *fakeKnowledge) Purge(_ context.Context, p core.PrincipalID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purged = append(f.purged, p)
	return nil
}

func TestKnowledgeRoutesUnwired(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do(http.MethodGet, "/api/knowledge/repos", nil, h.owner)
	expect(t, rec, http.StatusServiceUnavailable)
	if errCode(t, rec) != "unavailable" {
		t.Fatalf("code = %s", errCode(t, rec))
	}
}

func TestKnowledgeRoutes(t *testing.T) {
	h := newHarness(t).setup()
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	ownerP := core.UserPrincipal(h.owner.user)
	r := k.add(ownerP, "acme/api")
	id := strconv.FormatInt(r.ID, 10)

	list := decodeAs[dto.List[core.IndexedRepo]](t, h.do(http.MethodGet, "/api/knowledge/repos", nil, h.owner))
	if len(list.Items) != 1 || list.Items[0].Repo != "acme/api" {
		t.Fatalf("list = %+v", list)
	}
	rec := h.do(http.MethodPost, "/api/knowledge/repos", dto.KnowledgeIndexReq{Dir: "repos/acme/web", Repo: "acme/web", SHA: "abc1234"}, h.owner)
	expect(t, rec, http.StatusAccepted)
	if got := decodeAs[core.IndexedRepo](t, rec); got.Principal != ownerP || got.Status != core.IndexQueued || k.lastIndex.Dir != "repos/acme/web" || k.lastIndex.SHA != "abc1234" {
		t.Fatalf("index = %+v (req %+v)", got, k.lastIndex)
	}
	expect(t, h.do(http.MethodPost, "/api/knowledge/repos", dto.KnowledgeIndexReq{Dir: "../x"}, h.owner), http.StatusBadRequest)
	expect(t, h.do(http.MethodGet, "/api/knowledge/repos/"+id, nil, h.owner), http.StatusOK)
	expect(t, h.do(http.MethodPost, "/api/knowledge/repos/"+id+"/reindex", nil, h.owner), http.StatusAccepted)

	rec = h.do(http.MethodGet, "/api/knowledge/repos/"+id+"/notes", nil, h.owner)
	expect(t, rec, http.StatusOK)
	if rec.Header().Get("ETag") != `""` {
		t.Fatalf("etag of empty notes = %q", rec.Header().Get("ETag"))
	}
	expect(t, h.do(http.MethodPut, "/api/knowledge/repos/"+id+"/notes", dto.KnowledgeNotesPutReq{Content: "x"}, h.owner), http.StatusPreconditionRequired)
	rec = h.do(http.MethodPut, "/api/knowledge/repos/"+id+"/notes", dto.KnowledgeNotesPutReq{Content: "# acme/api\n"}, h.owner, withHeader("If-Match", `""`))
	expect(t, rec, http.StatusOK)
	etag := rec.Header().Get("ETag")
	if n := decodeAs[core.KnowledgeNotes](t, rec); n.Content != "# acme/api\n" || etag != `"`+n.ETag+`"` {
		t.Fatalf("put notes = %+v etag %s", n, etag)
	}
	expect(t, h.do(http.MethodPut, "/api/knowledge/repos/"+id+"/notes", dto.KnowledgeNotesPutReq{Content: "y"}, h.owner, withHeader("If-Match", `"stale"`)), http.StatusConflict)
	expect(t, h.do(http.MethodPut, "/api/knowledge/repos/"+id+"/notes", dto.KnowledgeNotesPutReq{Content: "y"}, h.owner, withHeader("If-Match", etag)), http.StatusOK)
	expect(t, h.do(http.MethodPost, "/api/knowledge/repos/"+id+"/notes/draft", nil, h.owner), http.StatusAccepted)
	if !slices.Equal(k.drafted, []int64{r.ID}) {
		t.Fatalf("drafted = %v", k.drafted)
	}

	res := decodeAs[dto.KnowledgeSearchResp](t, h.do(http.MethodGet, "/api/knowledge/search?q=rate+limiter&repo_id="+id+"&limit=5", nil, h.owner))
	if len(res.Items) != 1 || k.lastQuery.Text != "rate limiter" || k.lastQuery.Repo != r.ID || k.lastQuery.Limit != 5 {
		t.Fatalf("search = %+v (query %+v)", res, k.lastQuery)
	}
	expect(t, h.do(http.MethodGet, "/api/knowledge/search?q=x&limit=99", nil, h.owner), http.StatusBadRequest)
	expect(t, h.do(http.MethodGet, "/api/knowledge/search?q=x&repo_id=abc", nil, h.owner), http.StatusBadRequest)
	expect(t, h.do(http.MethodGet, "/api/knowledge/search?q=", nil, h.owner), http.StatusBadRequest)
	if res := decodeAs[dto.KnowledgeSearchResp](t, h.do(http.MethodGet, "/api/knowledge/search?q=x&repo_id=999", nil, h.owner)); res.Items != nil {
		t.Fatalf("unknown repo_id = %+v", res)
	}

	for _, bad := range []string{"abc", "0", "-1", "99999999999999999999"} {
		expect(t, h.do(http.MethodGet, "/api/knowledge/repos/"+bad, nil, h.owner), http.StatusNotFound)
	}
	expect(t, h.do(http.MethodDelete, "/api/knowledge/repos/"+id, nil, h.owner), http.StatusNoContent)
	expect(t, h.do(http.MethodGet, "/api/knowledge/repos/"+id, nil, h.owner), http.StatusNotFound)
}

// TestKnowledgeIDOR: every by-ID knowledge route answers another user's
// repo with the same 404 as a missing one, through cookies and tokens.
func TestKnowledgeIDOR(t *testing.T) {
	h := newHarness(t).setup()
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	ownerP := core.UserPrincipal(h.owner.user)
	r := k.add(ownerP, "acme/secret")
	id := strconv.FormatInt(r.ID, 10)
	if _, err := k.WriteNotes(context.Background(), core.Viewer{Principals: []core.PrincipalID{ownerP}}, r.ID, "secret notes", "", h.owner.user); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*client{h.member, h.admin, h.tokenFor(h.member, "chat"), h.tokenFor(h.admin, "chat", "admin")} {
		for _, probe := range []struct {
			method, path string
			body         any
			hdr          []reqOpt
		}{
			{http.MethodGet, "/api/knowledge/repos/" + id, nil, nil},
			{http.MethodDelete, "/api/knowledge/repos/" + id, nil, nil},
			{http.MethodPost, "/api/knowledge/repos/" + id + "/reindex", nil, nil},
			{http.MethodGet, "/api/knowledge/repos/" + id + "/notes", nil, nil},
			{http.MethodPut, "/api/knowledge/repos/" + id + "/notes", dto.KnowledgeNotesPutReq{Content: "pwned"}, []reqOpt{withHeader("If-Match", `"e12"`)}},
			{http.MethodPost, "/api/knowledge/repos/" + id + "/notes/draft", nil, nil},
			{http.MethodGet, "/api/knowledge/search?q=x&repo_id=" + id, nil, nil},
			{http.MethodGet, "/api/knowledge/search?q=x&principal=" + string(ownerP), nil, nil},
			{http.MethodGet, "/api/knowledge/repos?principal=" + string(ownerP), nil, nil},
			{http.MethodPost, "/api/knowledge/repos", dto.KnowledgeIndexReq{Principal: ownerP, Dir: "."}, nil},
		} {
			rec := h.do(probe.method, probe.path, probe.body, c, probe.hdr...)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s %s = %d %s, want 404", c.name, probe.method, probe.path, rec.Code, rec.Body.String())
			}
		}
		if list := decodeAs[dto.List[core.IndexedRepo]](t, h.do(http.MethodGet, "/api/knowledge/repos", nil, c)); len(list.Items) != 0 {
			t.Errorf("%s lists %+v", c.name, list.Items)
		}
		if res := decodeAs[dto.KnowledgeSearchResp](t, h.do(http.MethodGet, "/api/knowledge/search?q=x", nil, c)); len(res.Items) != 0 {
			t.Errorf("%s search sees %+v", c.name, res.Items)
		}
	}
	if k.notes[r.ID] != "secret notes" || len(k.drafted) != 0 || k.repos[r.ID] == nil {
		t.Fatalf("a probe changed the owner's repo: notes %q drafted %v", k.notes[r.ID], k.drafted)
	}
}

func TestKnowledgePurgedWithUser(t *testing.T) {
	h := newHarness(t).setup()
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	expect(t, h.do(http.MethodDelete, "/api/users/"+string(h.member.user)+"?purge=true", nil, h.owner), http.StatusNoContent)
	if !slices.Contains(k.purged, core.UserPrincipal(h.member.user)) {
		t.Fatalf("purged = %v", k.purged)
	}
}
