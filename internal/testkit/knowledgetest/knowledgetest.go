// Package knowledgetest is an in-memory core.KnowledgeRepo for module tests.
// It passes conformance.Knowledge: FTS is replaced by case-insensitive word
// matching (AND, then OR), bm25 by the share of query words a chunk holds
// with title words counting double. Principals and sessions are checked
// against the core.Store it is given (the foreign keys of the SQLite schema);
// rows of a deleted principal are not cascaded.
package knowledgetest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	searchDefault = 8
	searchMax     = 50
	maxTerms      = 16
)

// New returns an empty repo whose rows reference principals and sessions of s.
func New(s core.Store) core.KnowledgeRepo {
	return &repo{s: s, repos: map[int64]*core.IndexedRepo{}, files: map[int64]map[string]core.KnowledgeFile{},
		chunks: map[int64][]core.KnowledgeChunk{}, touches: map[touchKey]*core.KnowledgeTouch{}}
}

type touchKey struct {
	s    core.SessionID
	repo int64
}

type repo struct {
	s core.Store

	mu      sync.Mutex
	nextID  int64
	nextCID int64
	repos   map[int64]*core.IndexedRepo
	files   map[int64]map[string]core.KnowledgeFile
	chunks  map[int64][]core.KnowledgeChunk
	touches map[touchKey]*core.KnowledgeTouch
}

func notFound(what string) error { return fmt.Errorf("knowledgetest: %s: %w", what, core.ErrNotFound) }
func invalid(what string) error  { return fmt.Errorf("knowledgetest: %s: %w", what, core.ErrInvalid) }

func msTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return time.UnixMilli(t.UnixMilli()).UTC()
}

func cp(r *core.IndexedRepo) *core.IndexedRepo {
	c := *r
	return &c
}

func (k *repo) Upsert(ctx context.Context, r *core.IndexedRepo) error {
	if _, err := k.s.Principals().Get(ctx, core.SystemViewer(), r.Principal); err != nil {
		return invalid("upsert: unknown principal")
	}
	owner, name, ok := strings.Cut(r.Repo, "/")
	if !ok || owner == "" || name == "" || len(r.Repo) > 201 {
		return invalid("upsert: repo must be owner/name")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	now := msTime(time.Now())
	for _, x := range k.repos {
		if x.Principal == r.Principal && x.Repo == r.Repo {
			x.Source, x.Dir, x.SHA, x.Status, x.Error, x.UpdatedAt = r.Source, r.Dir, r.SHA, r.Status, r.Error, now
			*r = *cp(x)
			return nil
		}
	}
	k.nextID++
	x := cp(r)
	x.ID, x.CreatedAt, x.UpdatedAt = k.nextID, now, now
	x.IndexedAt, x.Notes.UpdatedAt = msTime(x.IndexedAt), msTime(x.Notes.UpdatedAt)
	if x.Notes.Status == "" {
		x.Notes.Status = core.NotesNone
	}
	k.repos[x.ID] = x
	*r = *cp(x)
	return nil
}

func (k *repo) visible(v core.Viewer, id int64) (*core.IndexedRepo, error) {
	x, ok := k.repos[id]
	if !ok || !v.Can(x.Principal) {
		return nil, notFound("knowledge repo")
	}
	return x, nil
}

func (k *repo) Get(_ context.Context, v core.Viewer, id int64) (*core.IndexedRepo, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	x, err := k.visible(v, id)
	if err != nil {
		return nil, err
	}
	return cp(x), nil
}

func (k *repo) ByName(_ context.Context, v core.Viewer, p core.PrincipalID, name string) (*core.IndexedRepo, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !v.Can(p) {
		return nil, notFound("by name")
	}
	for _, x := range k.repos {
		if x.Principal == p && x.Repo == name {
			return cp(x), nil
		}
	}
	return nil, notFound("by name")
}

func (k *repo) List(_ context.Context, v core.Viewer, p core.PrincipalID) ([]*core.IndexedRepo, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []*core.IndexedRepo
	for _, x := range k.repos {
		if v.Can(x.Principal) && (p == "" || x.Principal == p) {
			out = append(out, cp(x))
		}
	}
	slices.SortFunc(out, func(a, b *core.IndexedRepo) int {
		return cmp.Or(strings.Compare(a.Repo, b.Repo), strings.Compare(string(a.Principal), string(b.Principal)))
	})
	return out, nil
}

func (k *repo) UpdateIndex(_ context.Context, r *core.IndexedRepo) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	x, ok := k.repos[r.ID]
	if !ok {
		return notFound("update index")
	}
	x.Status, x.Error, x.Files, x.Chunks, x.Bytes = r.Status, r.Error, r.Files, r.Chunks, r.Bytes
	x.Skipped, x.Truncated, x.IndexedAt, x.UpdatedAt = r.Skipped, r.Truncated, msTime(r.IndexedAt), msTime(time.Now())
	r.UpdatedAt = x.UpdatedAt
	return nil
}

func (k *repo) UpdateNotes(_ context.Context, id int64, n core.NotesState) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	x, ok := k.repos[id]
	if !ok {
		return notFound("update notes")
	}
	n.UpdatedAt = msTime(n.UpdatedAt)
	x.Notes = n
	return nil
}

func (k *repo) Delete(_ context.Context, v core.Viewer, id int64) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, err := k.visible(v, id); err != nil {
		return err
	}
	delete(k.repos, id)
	delete(k.files, id)
	delete(k.chunks, id)
	for key := range k.touches {
		if key.repo == id {
			delete(k.touches, key)
		}
	}
	return nil
}

func (k *repo) Files(_ context.Context, v core.Viewer, id int64) ([]core.KnowledgeFile, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, err := k.visible(v, id); err != nil {
		return nil, err
	}
	out := make([]core.KnowledgeFile, 0, len(k.files[id]))
	for _, f := range k.files[id] {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b core.KnowledgeFile) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

func (k *repo) Chunks(_ context.Context, v core.Viewer, id int64, path string) ([]core.KnowledgeChunk, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, err := k.visible(v, id); err != nil {
		return nil, err
	}
	var out []core.KnowledgeChunk
	for _, c := range k.chunks[id] {
		if path == "" || c.Path == path {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b core.KnowledgeChunk) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), a.StartLine-b.StartLine, cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (k *repo) Apply(_ context.Context, id int64, sha string, changes []core.KnowledgeFileChange) error {
	for _, c := range changes {
		p := c.File.Path
		if p == "" || len(p) > 1024 || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
			return invalid("apply: file path")
		}
		for _, ch := range c.Chunks {
			if ch.StartLine < 1 || ch.EndLine < ch.StartLine {
				return invalid("apply: line range")
			}
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.repos[id]; !ok {
		return invalid("apply: unknown repo")
	}
	if k.files[id] == nil {
		k.files[id] = map[string]core.KnowledgeFile{}
	}
	for _, c := range changes {
		path := c.File.Path
		k.chunks[id] = slices.DeleteFunc(k.chunks[id], func(x core.KnowledgeChunk) bool { return x.Path == path })
		if c.Delete {
			delete(k.files[id], path)
			continue
		}
		for _, ch := range c.Chunks {
			k.nextCID++
			ch.ID, ch.SHA, ch.Path = k.nextCID, sha, path
			k.chunks[id] = append(k.chunks[id], ch)
		}
		f := c.File
		f.Chunks = len(c.Chunks)
		k.files[id][path] = f
	}
	return nil
}

func (k *repo) SetSHA(_ context.Context, id int64, sha string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.chunks[id] {
		k.chunks[id][i].SHA = sha
	}
	return nil
}

// words tokenizes like unicode61: runs of letters, digits and marks, lowercased.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	})
}

func (k *repo) Search(_ context.Context, v core.Viewer, q core.KnowledgeQuery) ([]core.KnowledgeHit, error) {
	terms := words(q.Text)
	slices.Sort(terms)
	terms = slices.Compact(terms)
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}
	if len(terms) == 0 || q.Principal != "" && !v.Can(q.Principal) {
		return nil, nil
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = searchDefault
	case limit > searchMax:
		limit = searchMax
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	type scored struct {
		hit   core.KnowledgeHit
		id    int64
		all   bool
		score float64
	}
	var cands []scored
	for id, x := range k.repos {
		if !v.Can(x.Principal) || q.Principal != "" && x.Principal != q.Principal || q.Repo != 0 && q.Repo != id {
			continue
		}
		for _, c := range k.chunks[id] {
			text := map[string]bool{}
			for _, w := range words(c.Path + " " + c.Body + " " + c.Terms) {
				text[w] = true
			}
			title := map[string]bool{}
			for _, w := range words(c.Title) {
				title[w] = true
			}
			n, score := 0, 0.0
			for _, t := range terms {
				switch {
				case title[t]:
					n++
					score += 2
				case text[t]:
					n++
					score++
				}
			}
			if n == 0 {
				continue
			}
			cands = append(cands, scored{id: c.ID, all: n == len(terms), score: -score / float64(len(terms)), hit: core.KnowledgeHit{
				RepoID: id, Repo: x.Repo, Principal: x.Principal, SHA: c.SHA, Path: c.Path, StartLine: c.StartLine,
				EndLine: c.EndLine, Kind: c.Kind, Lang: c.Lang, Title: c.Title, Snippet: c.Body,
			}})
		}
	}
	if slices.ContainsFunc(cands, func(s scored) bool { return s.all }) {
		cands = slices.DeleteFunc(cands, func(s scored) bool { return !s.all })
	}
	slices.SortFunc(cands, func(a, b scored) int { return cmp.Or(cmp.Compare(a.score, b.score), cmp.Compare(a.id, b.id)) })
	out := make([]core.KnowledgeHit, 0, min(limit, len(cands)))
	for _, c := range cands[:min(limit, len(cands))] {
		c.hit.Rank = c.score
		out = append(out, c.hit)
	}
	return out, nil
}

func (k *repo) Touch(ctx context.Context, s core.SessionID, id int64, at time.Time) error {
	sess, err := k.s.Sessions().Get(ctx, core.SystemViewer(), s)
	if err != nil {
		return invalid("touch: unknown session")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	x, ok := k.repos[id]
	if !ok || x.Principal != sess.Principal {
		return invalid("touch: no such session and repo of one principal")
	}
	key := touchKey{s, id}
	if t, ok := k.touches[key]; ok {
		t.TouchedAt = msTime(at)
		return nil
	}
	k.touches[key] = &core.KnowledgeTouch{Repo: id, TouchedAt: msTime(at), InjectedEpoch: -1}
	return nil
}

func (k *repo) Touched(ctx context.Context, v core.Viewer, s core.SessionID) ([]core.KnowledgeTouch, error) {
	if _, err := k.s.Sessions().Get(ctx, v, s); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []core.KnowledgeTouch
	for key, t := range k.touches {
		if key.s == s {
			out = append(out, *t)
		}
	}
	slices.SortFunc(out, func(a, b core.KnowledgeTouch) int {
		return cmp.Or(b.TouchedAt.Compare(a.TouchedAt), cmp.Compare(a.Repo, b.Repo))
	})
	return out, nil
}

func (k *repo) MarkInjected(_ context.Context, s core.SessionID, id int64, epoch int, hash string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	t, ok := k.touches[touchKey{s, id}]
	if !ok {
		return notFound("mark injected")
	}
	t.InjectedEpoch, t.InjectedHash = epoch, hash
	return nil
}
