package core

import (
	"context"
	"io"
	"time"
)

// ---- memory ----

type MemoryOp struct {
	File MemoryFile `json:"file"`
	Op   string     `json:"op"`            // add | replace | remove
	Old  string     `json:"old,omitempty"` // exact existing entry text (replace/remove)
	New  string     `json:"new,omitempty"`
}

type MemorySnapshot struct {
	Memory  string `json:"memory"`
	User    string `json:"user"`
	LastID  int64  `json:"last_id"` // last memory_events id folded in
	Private bool   `json:"private"` // non-empty => session starts with TaintPrivate
}

// Memory files live in <data>/memory/<principal>/{MEMORY,USER,HEARTBEAT}.md.
// Caps: memory.memory_max_bytes, user_max_bytes, heartbeat_max_bytes. Space
// principals have no memory: Snapshot returns an empty snapshot and
// Apply/Read/Write/History return ErrForbidden for them.
//
// TEAM.md, remembered for everyone, is one file at <data>/memory/team/TEAM.md
// (cap memory.team_max_bytes) that every run's S1 carries, whatever the
// principal. Only an admin's edit (WriteTeam) or a memory tool call with file
// TEAM that an admin approved changes it; Apply refuses it.
type Memory interface {
	ToolProvider // memory, session_search
	Snapshot(ctx context.Context, p PrincipalID) (MemorySnapshot, error)
	// Apply enforces byte caps (error text tells the model the remaining bytes),
	// writes atomically (temp+rename, 0600), records memory_events, audits.
	Apply(ctx context.Context, p PrincipalID, run RunID, op MemoryOp, prov Provenance) (*MemoryEvent, error)
	Read(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile) (content, etag string, err error)
	Write(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile, content, etag string, by UserID) (string, error) // ErrConflict on etag mismatch
	DeltasSince(ctx context.Context, p PrincipalID, afterID int64) (text string, lastID int64, err error)
	History(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile, page Page) ([]*MemoryEvent, error)
	ReadTeam(ctx context.Context) (content, etag string, err error)
	// WriteTeam: ErrForbidden unless by is an enabled admin; ErrConflict on
	// etag mismatch. Audited memory.edit with scope team.
	WriteTeam(ctx context.Context, content, etag string, by UserID) (string, error)
	TeamHistory(ctx context.Context, page Page) ([]*MemoryEvent, error) // newest first
	Search(ctx context.Context, v Viewer, query string, limit int) ([]SearchHit, error)
	// Purge deletes every memory file of a principal (DELETE /api/users/{id}?purge=true);
	// its memory_events rows go with the principal (ON DELETE CASCADE).
	Purge(ctx context.Context, p PrincipalID) error
}

// ---- skills ----

type Skills interface {
	ToolProvider                           // skill_load, skill_read
	Index(ctx context.Context) []SkillMeta // enabled and hash-matching only, by name
	Load(ctx context.Context, name string) (body string, files []string, err error)
	ReadFile(ctx context.Context, name, rel string) ([]byte, error) // os.Root confined, 256KB cap
	List(ctx context.Context) ([]*SkillMeta, error)                 // all, for admins
	// SetEnabled with on approves the last scanned tree; hash, when non-empty,
	// must equal the SkillMeta.Hash the admin reviewed, otherwise ErrConflict.
	SetEnabled(ctx context.Context, name string, on bool, hash string, by UserID) error
	Install(ctx context.Context, zip io.ReaderAt, size int64, by UserID) (*SkillMeta, error) // status pending; never executes anything
	Remove(ctx context.Context, name string, by UserID) error
	Rescan(ctx context.Context) error // hash drift => status changed
}

// ---- MCP ----

type MCPStatus struct {
	Name      string    `json:"name"`
	Transport string    `json:"transport"` // stdio | http
	State     string    `json:"state"`     // disabled | installing | connecting | ready | error | no_sandbox | pins_pending
	Protocol  string    `json:"protocol"`  // negotiated spec revision
	Err       string    `json:"error,omitempty"`
	Tools     int       `json:"tools"`
	Pending   int       `json:"pending"`
	Since     time.Time `json:"since"`
}

type MCPToolApproval struct {
	SideEffect SideEffect        `json:"side_effect"`
	FieldMap   map[string]string `json:"field_map,omitempty"`
	Private    bool              `json:"private"`
	Disable    bool              `json:"disable,omitempty"` // true => pin status disabled (MCPRepo.DisableTool); other fields ignored
	// Hash is the definition hash the admin reviewed. When set, Approve fails
	// with ErrConflict unless it is still the tool's latest pin.
	Hash string `json:"hash,omitempty"`
}

type MCP interface {
	// Start connects enabled servers and registers approved, hash-matching
	// tools. Discovery has no billing user: HTTP header secret refs resolve in
	// the global vault scope only, so each needs a global value (§2.11).
	Start(ctx context.Context) error
	Status() []MCPStatus
	Pins(ctx context.Context, server string) ([]*MCPPin, error) // latest definition per tool incl. pending diffs
	Approve(ctx context.Context, server string, tools map[string]MCPToolApproval, by UserID) error
	Install(ctx context.Context, server string, by UserID) error // stdio only, installer box
	Reload(ctx context.Context, server string) error
	Close() error
}

// ---- scheduler ----

type Scheduler interface {
	ToolProvider // schedule_list, schedule_create, schedule_delete (Triggers: message only)
	// Run fires due schedules and keeps one heartbeat schedule (name
	// "heartbeat", kind heartbeat, created_by "system") for the owner's principal
	// in sync with scheduler.heartbeat (hot reload enables/disables it).
	Run(ctx context.Context) error
	Upsert(ctx context.Context, v Viewer, s *Schedule, by string) error // validates spec+tz (empty tz = v.User's own), computes NextRunAt, wakes loop
	Delete(ctx context.Context, v Viewer, id ScheduleID, by UserID) error
	RunNow(ctx context.Context, v Viewer, id ScheduleID, by UserID) (RunID, error)
	Preview(kind ScheduleKind, spec, tz string, n int) ([]time.Time, error)
	// Describe renders a spec in plain English for dto.SchedulePreviewResp.Human,
	// e.g. "Weekdays at 08:00 (Europe/London)".
	Describe(kind ScheduleKind, spec, tz string) (string, error)
	// SetPaused is a scheduler-only pause, independent of the kill switch:
	// the scheduler stops firing while Runner.Paused() is true whatever this
	// says, and nothing in the gateway calls it.
	SetPaused(paused bool)
}

// ---- audit ----

type Auditor interface {
	// Record redacts Detail, chains sha256(prev_hash || canonical row) and appends.
	// auth.*, config.*, approval.* and secret.* are written synchronously; others
	// are batched (flush <= 250ms) and flushed on shutdown.
	Record(ctx context.Context, e AuditEntry) error
	Verify(ctx context.Context) (badID int64, err error) // 0 = chain intact
	Flush(ctx context.Context) error
}

// ---- doctor ----

type Severity string

const (
	SevInfo     Severity = "info"
	SevLow      Severity = "low"
	SevMedium   Severity = "medium"
	SevHigh     Severity = "high"
	SevCritical Severity = "critical"
)

type Finding struct {
	ID       string   `json:"id"` // stable: "vault.kek_colocated", "sandbox.unavailable"
	Area     string   `json:"area"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Fix      string   `json:"fix,omitempty"` // copyable command or UI route
	Severity Severity `json:"severity"`
	Security bool     `json:"security"` // included in security-audit
	AutoFix  bool     `json:"auto_fix"`
}

type Check interface {
	ID() string
	Run(ctx context.Context) []Finding
	Fix(ctx context.Context, f Finding) error // ErrNotImplemented when !AutoFix
}

type CheckProvider interface{ Checks() []Check }
