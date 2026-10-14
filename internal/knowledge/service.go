package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Repo    core.KnowledgeRepo
	Store   core.Store
	Config  config.View
	LLM     core.LLM    // notes drafts; nil disables them
	Budget  core.Budget // meters drafts
	Bus     core.Bus
	Audit   core.Auditor
	DataDir string // notes under <data>/knowledge
	WorkDir string // workspaces under <work>/ws/<id>
	Clock   core.Clock
	Log     *slog.Logger
}

const (
	indexWorkers = 2
	draftSlots   = 2
	eventBuffer  = 64
)

var _ core.Knowledge = (*Service)(nil)

// Service implements core.Knowledge: a background index queue fed by
// repo.checked_out events and the HTTP API, the knowledge_search and notes
// tools, notes drafting and the turn-context hook.
type Service struct {
	d     Deps
	log   *slog.Logger
	clock core.Clock

	mu       sync.Mutex
	queue    []int64
	queued   map[int64]bool
	running  map[int64]bool
	again    map[int64]bool // asked for again while running
	drafting map[int64]bool
	has      map[core.PrincipalID]bool // principal has an indexed repo (tool gating)
	wake     chan struct{}
	stopping bool // no new drafts: bgWG is being waited on

	// notesMu serialises every notes file change with its notes state row.
	notesMu sync.Mutex

	bg       context.Context
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup
	draftSem chan struct{}

	tools []core.Tool
}

func New(d Deps) (*Service, error) {
	switch {
	case d.Repo == nil, d.Store == nil, d.Config == nil, d.Bus == nil:
		return nil, errors.New("knowledge: Repo, Store, Config and Bus are required")
	case d.DataDir == "":
		return nil, errors.New("knowledge: DataDir is required")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Service{d: d, log: d.Log, clock: d.Clock, queued: map[int64]bool{}, running: map[int64]bool{},
		again: map[int64]bool{}, drafting: map[int64]bool{}, has: map[core.PrincipalID]bool{},
		wake: make(chan struct{}, 1), draftSem: make(chan struct{}, draftSlots)}
	s.bg, s.bgCancel = context.WithCancel(context.Background())
	s.tools = []core.Tool{newSearchTool(s), newNotesTool(s)}
	return s, nil
}

// Tools returns knowledge_search and notes.
func (s *Service) Tools() []core.Tool { return s.tools }

func (s *Service) cfg() config.KnowledgeCfg { return s.d.Config.Current().Knowledge }

func (s *Service) caps() caps {
	c := s.cfg()
	return caps{fileBytes: int64(c.MaxFileKB) << 10, files: c.MaxFiles, totalBytes: int64(c.MaxRepoMB) << 20}
}

var errDisabled = fmt.Errorf("%w: knowledge is turned off (knowledge.enabled)", core.ErrInvalid)

// Run serves the index queue and repo.checked_out events until ctx ends,
// then waits for its workers and notes drafts.
func (s *Service) Run(ctx context.Context) error {
	sub := s.d.Bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvRepoCheckedOut}}, eventBuffer)
	defer sub.Close()
	s.recover(ctx)
	var wg sync.WaitGroup
	for range indexWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx)
		}()
	}
	defer func() {
		wg.Wait()
		s.stop()
	}()
	var dropped uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub.C():
			if !ok {
				return nil
			}
			if n := sub.Dropped(); n > dropped {
				s.log.Warn("knowledge: missed repo.checked_out events; reindex the affected repos", "dropped", n-dropped)
				dropped = n
			}
			s.onCheckout(ctx, e)
		}
	}
}

// stop cancels running drafts and waits for them; drafts asked for later
// are refused, so bgWG.Add never races its Wait.
func (s *Service) stop() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	s.bgCancel()
	s.bgWG.Wait()
}

// recover requeues indexes a restart interrupted and fails drafts it cut off.
func (s *Service) recover(ctx context.Context) {
	repos, err := s.d.Repo.List(ctx, core.SystemViewer(), "")
	if err != nil {
		s.log.Error("knowledge: list repos at boot", "err", err)
		return
	}
	for _, r := range repos {
		if r.Status == core.IndexQueued || r.Status == core.IndexRunning {
			s.schedule(r.ID)
		}
		if r.Notes.Status == core.NotesDrafting {
			n := r.Notes
			n.Status, n.Error = core.NotesFailed, "Drafting was interrupted by a restart; draft again."
			if err := s.d.Repo.UpdateNotes(ctx, r.ID, n); err != nil {
				s.log.Warn("knowledge: reset interrupted draft", "repo", r.ID, "err", err)
			}
		}
	}
}

func (s *Service) schedule(id int64) {
	s.mu.Lock()
	switch {
	case s.running[id]:
		s.again[id] = true
	case s.queued[id]:
	default:
		s.queued[id] = true
		s.queue = append(s.queue, id)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) next(ctx context.Context) (int64, bool) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			id := s.queue[0]
			s.queue = s.queue[1:]
			delete(s.queued, id)
			s.running[id] = true
			more := len(s.queue) > 0
			s.mu.Unlock()
			if more {
				s.signal()
			}
			return id, true
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, false
		case <-s.wake:
		}
	}
}

func (s *Service) worker(ctx context.Context) {
	for {
		id, ok := s.next(ctx)
		if !ok {
			return
		}
		s.runJob(ctx, id)
		s.mu.Lock()
		delete(s.running, id)
		requeue := s.again[id]
		delete(s.again, id)
		s.mu.Unlock()
		if requeue && ctx.Err() == nil {
			s.schedule(id)
		}
	}
}

func (s *Service) wantedAgain(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.again[id]
}

// runJob indexes one repo. A shutdown leaves the row "indexing" so the next
// boot requeues it.
func (s *Service) runJob(ctx context.Context, id int64) {
	r, err := s.d.Repo.Get(ctx, core.SystemViewer(), id)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			s.log.Error("knowledge: load repo", "repo", id, "err", err)
		}
		return
	}
	if !s.cfg().Enabled {
		s.fail(ctx, r, &errUser{"Knowledge is turned off."})
		return
	}
	p, err := s.d.Store.Principals().Get(ctx, core.SystemViewer(), r.Principal)
	if err != nil {
		s.fail(ctx, r, err)
		return
	}
	r.Status, r.Error = core.IndexRunning, ""
	if err := s.d.Repo.UpdateIndex(ctx, r); err != nil {
		s.log.Warn("knowledge: mark indexing", "repo", id, "err", err)
		return
	}
	s.publish(r, false)
	st, err := indexDir(ctx, s.d.Repo, r.ID, r.SHA, workspaceDir(s.d.WorkDir, p), r.Dir, s.caps())
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.fail(ctx, r, err)
		return
	}
	r.Status, r.Error = core.IndexReady, ""
	if s.wantedAgain(id) {
		r.Status = core.IndexQueued
	}
	r.Files, r.Chunks, r.Bytes, r.Skipped, r.Truncated = st.files, st.chunks, st.bytes, st.skipped, st.truncated
	r.IndexedAt = s.clock.Now()
	if err := s.d.Repo.UpdateIndex(ctx, r); err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			s.log.Error("knowledge: store index state", "repo", id, "err", err)
		}
		return
	}
	s.setHas(r.Principal, true)
	s.publish(r, false)
	s.audit(ctx, "system", "knowledge.index", r, "ok", map[string]any{"state": "indexed", "files": st.files, "chunks": st.chunks,
		"skipped": st.skipped, "truncated": st.truncated, "sha": r.SHA})
	if r.Notes.Status == core.NotesNone && s.cfg().AutoNotes {
		if _, err := s.startDraft(ctx, r, p, "system"); err != nil && !errors.Is(err, core.ErrConflict) {
			s.log.Warn("knowledge: start notes draft", "repo", id, "err", err)
		}
	}
}

func userMessage(err error, fallback string) string {
	var ue *errUser
	if errors.As(err, &ue) {
		return ue.msg
	}
	var be *core.BudgetError
	if errors.As(err, &be) {
		return "Today's spending limit is reached."
	}
	if errors.Is(err, errNoModel) {
		return "No model is set up for drafting notes."
	}
	return fallback
}

// fail records an index failure; a repo deleted mid-index fails quietly.
func (s *Service) fail(ctx context.Context, r *core.IndexedRepo, err error) {
	r.Status, r.Error = core.IndexFailed, userMessage(err, "Indexing failed; the server log has the details.")
	if uerr := s.d.Repo.UpdateIndex(ctx, r); uerr != nil {
		if !errors.Is(uerr, core.ErrNotFound) {
			s.log.Error("knowledge: store index failure", "repo", r.ID, "err", uerr, "cause", err)
		}
		return
	}
	var ue *errUser
	if !errors.As(err, &ue) {
		s.log.Error("knowledge: index failed", "repo", r.ID, "err", err)
	}
	s.publish(r, false)
	s.audit(ctx, "system", "knowledge.index", r, "error", map[string]any{"error": r.Error})
}

func (s *Service) publish(r *core.IndexedRepo, deleted bool) {
	cp := *r
	s.d.Bus.Publish(core.Event{Type: core.EvKnowledgeUpdated, Principal: r.Principal,
		Data: core.KnowledgeUpdatedData{Repo: &cp, Deleted: deleted}})
}

func (s *Service) audit(ctx context.Context, actor, action string, r *core.IndexedRepo, outcome string, detail map[string]any) {
	s.auditRun(ctx, actor, action, r, outcome, detail, "", "")
}

func (s *Service) auditRun(ctx context.Context, actor, action string, r *core.IndexedRepo, outcome string, detail map[string]any,
	sess core.SessionID, run core.RunID) {
	if s.d.Audit == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["repo_id"] = r.ID
	b, err := json.Marshal(detail)
	if err != nil {
		b = []byte("{}")
	}
	if err := s.d.Audit.Record(ctx, core.AuditEntry{At: s.clock.Now(), Actor: actor, Action: action,
		Target: "knowledge:" + string(r.Principal) + "/" + r.Repo, Outcome: outcome, Session: sess, Run: run, Detail: b}); err != nil {
		s.log.Error("knowledge: audit", "action", action, "err", err)
	}
}

func userActor(u core.UserID) string { return "user:" + string(u) }

// onCheckout queues the index of a checkout another module wrote.
func (s *Service) onCheckout(ctx context.Context, e core.Event) {
	d, ok := e.Data.(core.RepoCheckedOutData)
	if !ok || e.Principal == "" {
		s.log.Warn("knowledge: ignoring malformed repo.checked_out event", "principal", e.Principal)
		return
	}
	if !s.cfg().Enabled {
		return
	}
	p, err := s.d.Store.Principals().Get(ctx, core.SystemViewer(), e.Principal)
	if err != nil {
		s.log.Warn("knowledge: repo.checked_out for an unknown principal", "principal", e.Principal, "err", err)
		return
	}
	name, err := normalizeRepo(d.Repo)
	if err == nil {
		var sha, dir string
		if sha, err = normalizeSHA(d.SHA); err == nil {
			dir, err = relDir(workspaceDir(s.d.WorkDir, p), d.Dir)
			if err == nil {
				actor := "system"
				if e.Run != "" {
					actor = "agent:" + string(e.Run)
				}
				var r *core.IndexedRepo
				if r, err = s.enqueue(ctx, p, name, dir, sha, core.KnowledgeGitHub); err == nil {
					s.auditRun(ctx, actor, "knowledge.index", r, "ok", map[string]any{"state": "queued", "source": r.Source,
						"dir": dir, "sha": sha}, e.Session, e.Run)
					if e.Session != "" {
						s.touch(ctx, e.Session, r.ID)
					}
					return
				}
			}
		}
	}
	s.log.Warn("knowledge: cannot index checkout", "principal", e.Principal, "repo", d.Repo, "err", err)
}

func (s *Service) enqueue(ctx context.Context, p *core.Principal, name, dir, sha, source string) (*core.IndexedRepo, error) {
	r := &core.IndexedRepo{Principal: p.ID, Repo: name, Source: source, Dir: dir, SHA: sha, Status: core.IndexQueued}
	if err := s.d.Repo.Upsert(ctx, r); err != nil {
		return nil, err
	}
	s.setHas(p.ID, true)
	s.schedule(r.ID)
	s.publish(r, false)
	return r, nil
}

func (s *Service) touch(ctx context.Context, sess core.SessionID, repo int64) {
	if sess == "" {
		return
	}
	if err := s.d.Repo.Touch(ctx, sess, repo, s.clock.Now()); err != nil {
		s.log.Debug("knowledge: touch", "session", sess, "repo", repo, "err", err)
	}
}

func (s *Service) setHas(p core.PrincipalID, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v {
		s.has[p] = true
	} else {
		delete(s.has, p)
	}
}

// hasRepos gates the tools; a miss asks the store once.
func (s *Service) hasRepos(ctx context.Context, p core.PrincipalID) bool {
	s.mu.Lock()
	v, ok := s.has[p]
	s.mu.Unlock()
	if ok {
		return v
	}
	repos, err := s.d.Repo.List(ctx, core.Viewer{Principals: []core.PrincipalID{p}}, p)
	if err != nil {
		return false
	}
	s.mu.Lock()
	s.has[p] = len(repos) > 0
	s.mu.Unlock()
	return len(repos) > 0
}

// ---- core.Knowledge ----

func (s *Service) Repos(ctx context.Context, v core.Viewer, p core.PrincipalID) ([]*core.IndexedRepo, error) {
	return s.d.Repo.List(ctx, v, p)
}

func (s *Service) Repo(ctx context.Context, v core.Viewer, id int64) (*core.IndexedRepo, error) {
	return s.d.Repo.Get(ctx, v, id)
}

func (s *Service) Index(ctx context.Context, v core.Viewer, p core.PrincipalID, req core.KnowledgeIndexRequest, by core.UserID) (*core.IndexedRepo, error) {
	if !s.cfg().Enabled {
		return nil, errDisabled
	}
	prin, err := s.d.Store.Principals().Get(ctx, v, p)
	if err != nil {
		return nil, err
	}
	ws := workspaceDir(s.d.WorkDir, prin)
	if ws == "" {
		return nil, invalidf("this principal has no workspace")
	}
	if filepath.IsAbs(req.Dir) {
		return nil, invalidf("dir must be relative to the workspace")
	}
	dir, err := relDir(ws, req.Dir)
	if err != nil {
		return nil, err
	}
	if err := checkDir(ws, dir); err != nil {
		return nil, err
	}
	name := defaultRepoName(dir)
	if req.Repo != "" {
		if name, err = normalizeRepo(req.Repo); err != nil {
			return nil, err
		}
	} else if cur, err := s.d.Repo.ByName(ctx, v, prin.ID, name); err == nil && cur.Dir != dir {
		// Two folders with the same base name would otherwise share one
		// index and one set of notes, the second silently replacing the first.
		return nil, fmt.Errorf("%w: %s already names the folder %s; give %s its own repo name (owner/name)",
			core.ErrConflict, name, cur.Dir, dir)
	} else if err != nil && !errors.Is(err, core.ErrNotFound) {
		return nil, err
	}
	sha, err := normalizeSHA(req.SHA)
	if err != nil {
		return nil, err
	}
	r, err := s.enqueue(ctx, prin, name, dir, sha, core.KnowledgeWorkspace)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, userActor(by), "knowledge.index", r, "ok", map[string]any{"state": "queued", "source": r.Source, "dir": dir, "sha": sha})
	return r, nil
}

// checkDir reports a missing directory before anything is queued.
func checkDir(ws, dir string) error {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return invalidf("the workspace does not exist yet")
	}
	defer root.Close()
	fi, err := root.Stat(filepath.FromSlash(dir))
	if err != nil || !fi.IsDir() {
		return invalidf("%s is not a directory in the workspace", dir)
	}
	return nil
}

func (s *Service) Reindex(ctx context.Context, v core.Viewer, id int64, by core.UserID) (*core.IndexedRepo, error) {
	if !s.cfg().Enabled {
		return nil, errDisabled
	}
	r, err := s.d.Repo.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	r.Status, r.Error = core.IndexQueued, ""
	if err := s.d.Repo.Upsert(ctx, r); err != nil {
		return nil, err
	}
	s.schedule(r.ID)
	s.publish(r, false)
	s.audit(ctx, userActor(by), "knowledge.index", r, "ok", map[string]any{"state": "queued", "source": r.Source, "dir": r.Dir, "sha": r.SHA})
	return r, nil
}

func (s *Service) Search(ctx context.Context, v core.Viewer, q core.KnowledgeQuery) ([]core.KnowledgeHit, error) {
	text, err := normalizeQuery(q.Text)
	if err != nil {
		return nil, err
	}
	q.Text = text
	hits, err := s.d.Repo.Search(ctx, v, q)
	if err != nil {
		return nil, err
	}
	words := queryWords(text)
	for i := range hits {
		hits[i] = refine(hits[i], words)
	}
	return hits, nil
}

func (s *Service) notesOf(r *core.IndexedRepo, content string) *core.KnowledgeNotes {
	return &core.KnowledgeNotes{RepoID: r.ID, Repo: r.Repo, Content: content, ETag: etagOf(content),
		CapBytes: s.cfg().NotesMaxBytes, Status: r.Notes.Status, Untrusted: r.Notes.Untrusted, UpdatedAt: r.Notes.UpdatedAt}
}

func (s *Service) Notes(ctx context.Context, v core.Viewer, id int64) (*core.KnowledgeNotes, error) {
	r, err := s.d.Repo.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	content, err := s.readNotes(r)
	if err != nil {
		return nil, err
	}
	return s.notesOf(r, content), nil
}

func (s *Service) WriteNotes(ctx context.Context, v core.Viewer, id int64, content, etag string, by core.UserID) (*core.KnowledgeNotes, error) {
	r, err := s.d.Repo.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	if !utf8.ValidString(content) {
		return nil, invalidf("notes must be UTF-8")
	}
	content = cleanNotes(content)
	if c := s.cfg().NotesMaxBytes; len(content) > c {
		return nil, invalidf("notes are limited to %d bytes (%d given)", c, len(content))
	}
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	cur, err := s.readNotes(r)
	if err != nil {
		return nil, err
	}
	if etagOf(cur) != etag {
		return nil, fmt.Errorf("%w: the notes changed since they were loaded", core.ErrConflict)
	}
	if err := s.writeNotesFile(r, content); err != nil {
		return nil, err
	}
	r.Notes = core.NotesState{Status: core.NotesReady, UpdatedAt: s.clock.Now()}
	if content == "" {
		r.Notes.Status = core.NotesNone
	}
	if err := s.d.Repo.UpdateNotes(ctx, r.ID, r.Notes); err != nil {
		return nil, err
	}
	s.publish(r, false)
	s.audit(ctx, userActor(by), "knowledge.notes_edit", r, "ok", map[string]any{"bytes": len(content)})
	return s.notesOf(r, content), nil
}

func (s *Service) DraftNotes(ctx context.Context, v core.Viewer, id int64, by core.UserID) (*core.IndexedRepo, error) {
	if !s.cfg().Enabled {
		return nil, errDisabled
	}
	r, err := s.d.Repo.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	p, err := s.d.Store.Principals().Get(ctx, v, r.Principal)
	if err != nil {
		return nil, err
	}
	return s.startDraft(ctx, r, p, userActor(by))
}

// startDraft marks the notes drafting and runs the draft in the background;
// ErrConflict when one is already running for the repo.
func (s *Service) startDraft(ctx context.Context, r *core.IndexedRepo, p *core.Principal, actor string) (*core.IndexedRepo, error) {
	s.mu.Lock()
	switch {
	case s.stopping:
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: the server is shutting down", core.ErrConflict)
	case s.drafting[r.ID]:
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: notes are already being drafted", core.ErrConflict)
	}
	s.drafting[r.ID] = true
	s.bgWG.Add(1)
	s.mu.Unlock()
	s.notesMu.Lock()
	base, err := s.readNotes(r)
	if err == nil {
		r.Notes.Status, r.Notes.Error = core.NotesDrafting, ""
		err = s.d.Repo.UpdateNotes(ctx, r.ID, r.Notes)
	}
	s.notesMu.Unlock()
	if err != nil {
		s.endDraft(r.ID)
		s.bgWG.Done()
		return nil, err
	}
	s.publish(r, false)
	baseTag := etagOf(base)
	go func(r core.IndexedRepo, p core.Principal) {
		defer s.bgWG.Done()
		defer s.endDraft(r.ID)
		select {
		case s.draftSem <- struct{}{}:
			defer func() { <-s.draftSem }()
		case <-s.bg.Done():
			return
		}
		ctx, cancel := context.WithTimeout(s.bg, draftTimeout)
		defer cancel()
		s.runDraft(ctx, &r, &p, actor, baseTag)
	}(*r, *p)
	return r, nil
}

func (s *Service) endDraft(id int64) {
	s.mu.Lock()
	delete(s.drafting, id)
	s.mu.Unlock()
}

// runDraft writes the draft only over the notes it started from: edits a
// user or the agent saved meanwhile are kept and the draft is dropped.
func (s *Service) runDraft(ctx context.Context, r *core.IndexedRepo, p *core.Principal, actor, baseTag string) {
	notes, err := s.draft(ctx, r, p)
	bg := context.WithoutCancel(ctx)
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	cur, rerr := s.d.Repo.Get(bg, core.SystemViewer(), r.ID)
	if rerr != nil {
		return // deleted meanwhile
	}
	var now string
	if now, rerr = s.readNotes(cur); err == nil && rerr == nil && etagOf(now) != baseTag {
		cur.Notes.Status, cur.Notes.Error = core.NotesReady, "The notes changed while the draft was written, so the draft was dropped."
		if now == "" {
			cur.Notes.Status = core.NotesNone
		}
		if uerr := s.d.Repo.UpdateNotes(bg, cur.ID, cur.Notes); uerr != nil {
			s.log.Warn("knowledge: store dropped draft", "repo", r.ID, "err", uerr)
		}
		s.publish(cur, false)
		s.audit(bg, actor, "knowledge.notes_draft", cur, "ok", map[string]any{"dropped": true})
		return
	}
	if err == nil {
		err = rerr
	}
	if err == nil {
		err = s.writeNotesFile(cur, notes)
	}
	if err != nil {
		var ue *errUser
		if !errors.As(err, &ue) {
			s.log.Warn("knowledge: notes draft failed", "repo", r.ID, "err", err)
		}
		cur.Notes.Status, cur.Notes.Error = core.NotesFailed, userMessage(err, "Drafting failed; the server log has the details.")
		if uerr := s.d.Repo.UpdateNotes(bg, cur.ID, cur.Notes); uerr != nil {
			s.log.Warn("knowledge: store draft failure", "repo", r.ID, "err", uerr)
		}
		s.publish(cur, false)
		s.audit(bg, actor, "knowledge.notes_draft", cur, "error", map[string]any{"error": cur.Notes.Error})
		return
	}
	cur.Notes = core.NotesState{Status: core.NotesReady, Untrusted: true, UpdatedAt: s.clock.Now()}
	if err := s.d.Repo.UpdateNotes(bg, cur.ID, cur.Notes); err != nil {
		s.log.Warn("knowledge: store draft state", "repo", r.ID, "err", err)
		return
	}
	s.publish(cur, false)
	s.audit(bg, actor, "knowledge.notes_draft", cur, "ok", map[string]any{"bytes": len(notes)})
}

func (s *Service) Delete(ctx context.Context, v core.Viewer, id int64, by core.UserID) error {
	r, err := s.d.Repo.Get(ctx, v, id)
	if err != nil {
		return err
	}
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	if err := s.d.Repo.Delete(ctx, v, id); err != nil {
		return err
	}
	s.forget(r)
	if dir, err := notesDir(s.d.DataDir, r.Principal, r.Repo); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			s.log.Warn("knowledge: remove notes", "repo", id, "err", err)
		}
	}
	s.publish(r, true)
	s.audit(ctx, userActor(by), "knowledge.delete", r, "ok", nil)
	return nil
}

// forget drops a deleted repo from the queue and the tool-gating cache.
func (s *Service) forget(r *core.IndexedRepo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued[r.ID] {
		delete(s.queued, r.ID)
		for i, q := range s.queue {
			if q == r.ID {
				s.queue = append(s.queue[:i], s.queue[i+1:]...)
				break
			}
		}
	}
	delete(s.again, r.ID)
	delete(s.has, r.Principal)
}

// Purge removes a principal's index rows and notes. The rows would also go
// with the principal (FK), but deleting them here, under notesMu, is what
// stops a draft that finishes before the principal row is gone from
// writing the notes back after the purge.
func (s *Service) Purge(ctx context.Context, p core.PrincipalID) error {
	if !core.ValidID(core.PrefixUser, string(p)) && !core.ValidID(core.PrefixSpace, string(p)) {
		return invalidf("bad principal id")
	}
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	repos, err := s.d.Repo.List(ctx, core.SystemViewer(), p)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range repos {
		if err := s.d.Repo.Delete(ctx, core.SystemViewer(), r.ID); err != nil && !errors.Is(err, core.ErrNotFound) {
			errs = append(errs, err)
			continue
		}
		s.forget(r)
	}
	s.setHas(p, false)
	return errors.Join(append(errs, os.RemoveAll(filepath.Join(s.d.DataDir, "knowledge", string(p))))...)
}
