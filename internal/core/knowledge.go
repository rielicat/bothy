package core

import (
	"context"
	"time"
)

// ---- knowledge (v0.2 F6): repository index and project notes ----

// EvKnowledgeUpdated reports an index or notes state change of one repo. The
// knowledge module indexes the checkouts EvRepoCheckout announces.
const EvKnowledgeUpdated EventType = "knowledge.updated"

type KnowledgeUpdatedData struct {
	Repo    IndexedRepo `json:"repo"`
	Deleted bool        `json:"deleted,omitempty"`
}

func (KnowledgeUpdatedData) eventData() {}

// Sources of an indexed directory.
const (
	KnowledgeGitHub    = "github"    // a repository checkout (repo.checkout)
	KnowledgeWorkspace = "workspace" // any directory of the principal's workspace
)

// IndexedRepo.Status values.
const (
	IndexQueued  = "queued"
	IndexRunning = "indexing"
	IndexReady   = "ready"
	IndexFailed  = "error"
)

// NotesState.Status values.
const (
	NotesNone     = "none"
	NotesDrafting = "drafting"
	NotesReady    = "ready"
	NotesFailed   = "error"
)

// IndexedRepo is one indexed directory of a principal's workspace, named like
// a repository. Its project notes live outside every repository, in
// <data>/knowledge/<principal>/<owner>/<repo>/NOTES.md.
type IndexedRepo struct {
	ID        int64       `json:"id"`
	Principal PrincipalID `json:"principal_id"`
	Repo      string      `json:"repo"`   // "owner/name", lowercase
	Source    string      `json:"source"` // github | workspace
	Dir       string      `json:"dir"`    // workspace-relative directory of the indexed copy
	// SHA is the commit of the latest requested index ("" for a plain
	// directory, and for a GitHub repository read again, which reads its
	// branch head); chunks carry the commit they were indexed at.
	SHA       string     `json:"sha"`
	Status    string     `json:"status"` // queued | indexing | ready | error
	Error     string     `json:"error,omitempty"`
	Files     int        `json:"files"`
	Chunks    int        `json:"chunks"`
	Bytes     int64      `json:"bytes"`
	Skipped   int        `json:"skipped"`   // text files left out by a size or count cap
	Truncated bool       `json:"truncated"` // a cap stopped the walk before the end
	IndexedAt time.Time  `json:"indexed_at"`
	Notes     NotesState `json:"notes"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// IndexedSHA is the commit the latest finished index read ("" until one
	// finished); UpdateIndex writes it.
	IndexedSHA string `json:"indexed_sha"`

	// Filled on read, never stored. Progress counts the files read so far
	// while the repo is queued or indexing; Summary is the first sentence
	// of the notes as plain text ("" without notes); Stale says the
	// repository's default branch has moved past IndexedSHA on GitHub.
	Progress *IndexProgress `json:"progress,omitempty"`
	Summary  string         `json:"summary"`
	Stale    bool           `json:"stale,omitempty"`
}

// IndexProgress is how far a running index got. Total is an estimate: the
// previous index's file count, else a count of the directory's files.
type IndexProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

type NotesState struct {
	Status string `json:"status"` // none | drafting | ready | error
	Error  string `json:"error,omitempty"`
	// Untrusted: part of the notes came from repository content or from an
	// agent run holding untrusted content, and no user has saved them since.
	Untrusted bool      `json:"untrusted"`
	UpdatedAt time.Time `json:"updated_at"`
}

// KnowledgeChunk is one indexed slice of a file: a markdown section, a
// top-level declaration or a window of plain text.
type KnowledgeChunk struct {
	ID        int64  `json:"id"`
	SHA       string `json:"sha"`
	Path      string `json:"path"` // slash-separated, relative to IndexedRepo.Dir
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Kind      string `json:"kind"` // heading | func | method | type | class | header | text | …
	Lang      string `json:"lang"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Terms     string `json:"-"` // extra search words (identifier parts); indexed, never shown
}

type KnowledgeFile struct {
	Path   string `json:"path"`
	Hash   string `json:"hash"` // chunker version + sha256 of the content
	Size   int64  `json:"size"`
	Lang   string `json:"lang"`
	Chunks int    `json:"chunks"`
}

// KnowledgeFileChange replaces one file's chunks (Chunks may be empty) or,
// with Delete, removes the file from the index.
type KnowledgeFileChange struct {
	File   KnowledgeFile
	Chunks []KnowledgeChunk
	Delete bool
}

type KnowledgeQuery struct {
	Text      string      `json:"text"`
	Principal PrincipalID `json:"principal_id,omitempty"` // "" = every principal of the Viewer
	Repo      int64       `json:"repo_id,omitempty"`      // 0 = every repo
	Limit     int         `json:"limit,omitempty"`        // 0 = 8, at most 50
}

// KnowledgeHit is a ranked search result. KnowledgeRepo.Search returns the
// whole chunk in Snippet; core.Knowledge narrows it to the lines around the
// match and sets Citation ("owner/repo@sha:path#L10-L42").
type KnowledgeHit struct {
	RepoID    int64       `json:"repo_id"`
	Repo      string      `json:"repo"`
	Principal PrincipalID `json:"principal_id"`
	SHA       string      `json:"sha"`
	Path      string      `json:"path"`
	StartLine int         `json:"start_line"`
	EndLine   int         `json:"end_line"`
	Kind      string      `json:"kind"`
	Lang      string      `json:"lang"`
	Title     string      `json:"title"`
	Snippet   string      `json:"snippet"`
	Citation  string      `json:"citation"`
	Rank      float64     `json:"rank"` // bm25: lower is better
}

// KnowledgeTouch records that a session worked on a repo and which notes
// version its turn-context already carries.
type KnowledgeTouch struct {
	Repo          int64     `json:"repo_id"`
	TouchedAt     time.Time `json:"touched_at"`
	InjectedEpoch int       `json:"injected_epoch"` // -1 = never injected
	InjectedHash  string    `json:"injected_hash"`
}

type KnowledgeNotes struct {
	RepoID    int64     `json:"repo_id"`
	Repo      string    `json:"repo"`
	Content   string    `json:"content"`
	ETag      string    `json:"etag"` // sha256 of Content; "" when there are no notes yet
	CapBytes  int       `json:"cap_bytes"`
	Status    string    `json:"status"`
	Untrusted bool      `json:"untrusted"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NotesVersion is one change to a repo's project notes: the text before
// and after it, who made it and, for Bothy's edits, the chat and run it was
// made in.
type NotesVersion struct {
	ID      int64     `json:"id"`
	Actor   string    `json:"actor"` // NotesByBothy | NotesByUser
	User    UserID    `json:"user_id,omitempty"`
	Session SessionID `json:"session_id,omitempty"`
	Run     RunID     `json:"run_id,omitempty"`
	Before  string    `json:"before"`
	After   string    `json:"after"`
	At      time.Time `json:"at"`
}

// NotesVersion.Actor values.
const (
	NotesByBothy = "bothy"
	NotesByUser  = "user"
)

type KnowledgeIndexRequest struct {
	Dir  string `json:"dir"`  // workspace-relative directory; "" or "." = the whole workspace
	Repo string `json:"repo"` // "owner/name"; "" = "local/<directory name>"
	SHA  string `json:"sha"`  // optional commit id shown in citations
}

// KnowledgeRepo stores the index (SQLite FTS5 in internal/store). Reads take
// a Viewer and answer ErrNotFound outside its principals; writes by id are
// for the knowledge service, which checks access first.
type KnowledgeRepo interface {
	// Upsert inserts the row of (Principal, Repo), or updates only Source,
	// Dir, SHA, Status and Error of an existing one, and writes the stored
	// row back into r.
	Upsert(ctx context.Context, r *IndexedRepo) error
	Get(ctx context.Context, v Viewer, id int64) (*IndexedRepo, error)
	ByName(ctx context.Context, v Viewer, p PrincipalID, repo string) (*IndexedRepo, error)
	List(ctx context.Context, v Viewer, p PrincipalID) ([]*IndexedRepo, error) // p "" = all of the Viewer's; by repo name
	// UpdateIndex writes the index state of r.ID (Status, Error, Files,
	// Chunks, Bytes, Skipped, Truncated, IndexedAt, IndexedSHA) and
	// r.UpdatedAt back; the requested SHA changes only through Upsert, and
	// UpdateNotes writes only the notes state, so concurrent writers never
	// undo each other.
	UpdateIndex(ctx context.Context, r *IndexedRepo) error
	UpdateNotes(ctx context.Context, id int64, n NotesState) error
	Delete(ctx context.Context, v Viewer, id int64) error                     // with its files, chunks and touches
	Files(ctx context.Context, v Viewer, repo int64) ([]KnowledgeFile, error) // by path
	Chunks(ctx context.Context, v Viewer, repo int64, path string) ([]KnowledgeChunk, error)
	// Apply runs every change in one transaction; new chunks get sha.
	Apply(ctx context.Context, repo int64, sha string, changes []KnowledgeFileChange) error
	SetSHA(ctx context.Context, repo int64, sha string) error // relabels every chunk of the repo
	// Search ANDs the words of q.Text and falls back to ORing them when
	// nothing matches all; hostile input never fails. Best rank first.
	Search(ctx context.Context, v Viewer, q KnowledgeQuery) ([]KnowledgeHit, error)
	Touch(ctx context.Context, s SessionID, repo int64, at time.Time) error
	Touched(ctx context.Context, v Viewer, s SessionID) ([]KnowledgeTouch, error) // most recent first
	MarkInjected(ctx context.Context, s SessionID, repo int64, epoch int, hash string) error
}

// TurnContextHook adds a block to the <turn-context> of a user turn
// (SPEC §5.6). The turn-context is persisted with the turn, so a block stays
// byte-identical in later prompts; the runner calls Commit once the turn is
// stored, which is when the block counts as delivered.
type TurnContextHook interface {
	TurnContext(ctx context.Context, in TurnContextInput) (TurnContextBlock, error)
}

type TurnContextInput struct {
	Principal Principal
	Session   SessionID
	Epoch     int
}

type TurnContextBlock struct {
	Text   string
	Taint  Taint // legs the block adds to the session
	Commit func(ctx context.Context) error
}

// Knowledge indexes repository checkouts (and any workspace directory) and
// keeps per-repo project notes.
type Knowledge interface {
	ToolProvider // knowledge_search, notes
	TurnContextHook
	Repos(ctx context.Context, v Viewer, p PrincipalID) ([]*IndexedRepo, error)
	Repo(ctx context.Context, v Viewer, id int64) (*IndexedRepo, error)
	Files(ctx context.Context, v Viewer, id int64) ([]KnowledgeFile, error) // what the index holds, by path
	// Index queues an index of a directory of p's workspace and returns the
	// queued row; indexing runs in the background (knowledge.updated).
	Index(ctx context.Context, v Viewer, p PrincipalID, req KnowledgeIndexRequest, by UserID) (*IndexedRepo, error)
	Reindex(ctx context.Context, v Viewer, id int64, by UserID) (*IndexedRepo, error)
	Search(ctx context.Context, v Viewer, q KnowledgeQuery) ([]KnowledgeHit, error)
	Notes(ctx context.Context, v Viewer, id int64) (*KnowledgeNotes, error)
	// WriteNotes saves notes a user wrote (ErrConflict when etag is stale);
	// they count as trusted from then on.
	WriteNotes(ctx context.Context, v Viewer, id int64, content, etag string, by UserID) (*KnowledgeNotes, error)
	// DraftNotes regenerates the notes with the fast model in the background.
	DraftNotes(ctx context.Context, v Viewer, id int64, by UserID) (*IndexedRepo, error)
	// NotesHistory lists the notes' changes, newest first; NotesVersion
	// reads one (ErrNotFound for an id the repo's history does not hold).
	NotesHistory(ctx context.Context, v Viewer, id int64) ([]NotesVersion, error)
	NotesVersion(ctx context.Context, v Viewer, id, version int64) (*NotesVersion, error)
	Delete(ctx context.Context, v Viewer, id int64, by UserID) error // index and notes
	Purge(ctx context.Context, p PrincipalID) error                  // every index row and notes file of a principal (user purge)
}
