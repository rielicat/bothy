package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const knowledgeSearchMax = 50

// knowledgeRoutes are the v0.2 knowledge endpoints; every one is scoped to
// the actor's Viewer, so foreign repos answer 404.
func (s *Server) knowledgeRoutes() []route {
	return []route{
		{pattern: "GET /api/knowledge/repos", access: accessUser, h: s.listKnowledgeRepos},
		{pattern: "POST /api/knowledge/repos", access: accessUser, h: s.indexKnowledgeRepo},
		{pattern: "GET /api/knowledge/repos/{id}", access: accessUser, h: s.getKnowledgeRepo},
		{pattern: "DELETE /api/knowledge/repos/{id}", access: accessUser, h: s.deleteKnowledgeRepo},
		{pattern: "POST /api/knowledge/repos/{id}/reindex", access: accessUser, h: s.reindexKnowledgeRepo},
		{pattern: "GET /api/knowledge/repos/{id}/notes", access: accessUser, h: s.getKnowledgeNotes},
		{pattern: "PUT /api/knowledge/repos/{id}/notes", access: accessUser, h: s.putKnowledgeNotes},
		{pattern: "POST /api/knowledge/repos/{id}/notes/draft", access: accessUser, h: s.draftKnowledgeNotes},
		{pattern: "GET /api/knowledge/search", access: accessUser, h: s.searchKnowledge},
	}
}

var errNoKnowledge = newError(http.StatusServiceUnavailable, "unavailable", "knowledge is not wired into the HTTP API in this build")

func (s *Server) knowledge() (core.Knowledge, error) {
	if s.d.Knowledge == nil {
		return nil, errNoKnowledge
	}
	return s.d.Knowledge, nil
}

// knowledgeID parses {id}; anything else is the same 404 as a foreign id.
func knowledgeID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errNotFound
	}
	return id, nil
}

// knowledgePrincipal reads an optional principal filter that must be one of
// the actor's principals.
func knowledgePrincipal(v string, a core.Actor) (core.PrincipalID, error) {
	p := core.PrincipalID(v)
	if p != "" && !a.Viewer.Can(p) {
		return "", errNotFound
	}
	return p, nil
}

func (s *Server) listKnowledgeRepos(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	p, err := knowledgePrincipal(r.URL.Query().Get("principal"), a)
	if err != nil {
		return err
	}
	repos, err := k.Repos(r.Context(), a.Viewer, p)
	if err != nil {
		return err
	}
	items := make([]core.IndexedRepo, 0, len(repos))
	for _, x := range repos {
		items = append(items, *x)
	}
	return ok(w, dto.List[core.IndexedRepo]{Items: items})
}

func (s *Server) indexKnowledgeRepo(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	var req dto.KnowledgeIndexReq
	if err := decode(r, &req); err != nil {
		return err
	}
	p := req.Principal
	if p == "" {
		p = core.UserPrincipal(a.User)
	}
	if !a.Viewer.Can(p) {
		return errNotFound
	}
	repo, err := k.Index(r.Context(), a.Viewer, p, core.KnowledgeIndexRequest{Dir: req.Dir, Repo: req.Repo, SHA: req.SHA}, a.User)
	if err != nil {
		return err
	}
	return accepted(w, repo)
}

func (s *Server) getKnowledgeRepo(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	repo, err := k.Repo(r.Context(), a.Viewer, id)
	if err != nil {
		return err
	}
	return ok(w, repo)
}

func (s *Server) deleteKnowledgeRepo(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	if err := k.Delete(r.Context(), a.Viewer, id, a.User); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) reindexKnowledgeRepo(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	repo, err := k.Reindex(r.Context(), a.Viewer, id, a.User)
	if err != nil {
		return err
	}
	return accepted(w, repo)
}

func (s *Server) getKnowledgeNotes(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	n, err := k.Notes(r.Context(), a.Viewer, id)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", quoteETag(n.ETag))
	return ok(w, n)
}

func (s *Server) putKnowledgeNotes(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	match, present := r.Header["If-Match"]
	if !present || len(match) == 0 {
		return newError(http.StatusPreconditionRequired, "bad_request", `If-Match with the notes' etag is required ("" while there are none)`)
	}
	var req dto.KnowledgeNotesPutReq
	if err := decode(r, &req); err != nil {
		return err
	}
	n, err := k.WriteNotes(r.Context(), a.Viewer, id, req.Content, unquoteETag(match[0]), a.User)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", quoteETag(n.ETag))
	return ok(w, n)
}

func (s *Server) draftKnowledgeNotes(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	id, err := knowledgeID(r)
	if err != nil {
		return err
	}
	repo, err := k.DraftNotes(r.Context(), a.Viewer, id, a.User)
	if err != nil {
		return err
	}
	return accepted(w, repo)
}

// purgeKnowledge removes each principal's project notes for a user purge;
// the index rows go with the principals (foreign keys).
func (s *Server) purgeKnowledge(ctx context.Context, ps []*core.Principal) error {
	if s.d.Knowledge == nil {
		return nil
	}
	var errs []error
	for _, p := range ps {
		if err := s.d.Knowledge.Purge(ctx, p.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// searchKnowledge is GET /api/knowledge/search?q=&repo_id=&principal=&limit=.
func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	k, err := s.knowledge()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	p, err := knowledgePrincipal(q.Get("principal"), a)
	if err != nil {
		return err
	}
	query := core.KnowledgeQuery{Text: q.Get("q"), Principal: p, Limit: 20}
	if v := q.Get("repo_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return badRequest("repo_id must be a positive integer")
		}
		if _, err := k.Repo(r.Context(), a.Viewer, id); err != nil {
			return err
		}
		query.Repo = id
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > knowledgeSearchMax {
			return badRequest("limit must be between 1 and %d", knowledgeSearchMax)
		}
		query.Limit = n
	}
	hits, err := k.Search(r.Context(), a.Viewer, query)
	if err != nil {
		return err
	}
	return ok(w, dto.KnowledgeSearchResp{Items: nonNil(hits)})
}
