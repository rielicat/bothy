package core

import (
	"context"
	"encoding/json"
	"time"
)

// Page is keyset pagination: Before is an exclusive upper bound on the row's
// sort key (seq, id or unix ms depending on the method); 0 = newest.
type Page struct {
	Before int64
	Limit  int // 0 = repository default (50), max 500
}

type User struct {
	ID           UserID    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	Role         Role      `json:"role"`
	PWHash       string    `json:"-"` // PHC $argon2id$v=19$m=19456,t=2,p=1$…
	Timezone     string    `json:"timezone"`
	Disabled     bool      `json:"disabled"`
	FailedLogins int       `json:"-"`
	LockedUntil  time.Time `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// MustChangePassword: the account has a one-time password an admin
	// handed out; its owner must choose their own before anything else.
	MustChangePassword bool `json:"must_change_password,omitempty"`
}

type Space struct {
	Principal   PrincipalID `json:"principal_id"`
	Kind        string      `json:"kind"` // "group" | "guest" (allowlisted guest DM)
	Chat        ChatRef     `json:"chat"`
	Title       string      `json:"title"`
	Owner       UserID      `json:"owner_user_id"`
	Enabled     bool        `json:"enabled"`
	MentionOnly bool        `json:"mention_only"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type IdentityStatus string

const (
	IdentityPending IdentityStatus = "pending" // unknown sender, approve_sender code issued
	IdentityLinked  IdentityStatus = "linked"  // linked to a user account
	IdentityGuest   IdentityStatus = "guest"   // allowlisted, talks to its own guest space
	IdentityBlocked IdentityStatus = "blocked"
)

type ChannelIdentity struct {
	Channel     string         `json:"channel"`
	ExternalID  string         `json:"external_id"`
	Username    string         `json:"username"`
	DisplayName string         `json:"display_name"`
	Status      IdentityStatus `json:"status"`
	User        UserID         `json:"user_id,omitempty"`      // linked
	Principal   PrincipalID    `json:"principal_id,omitempty"` // guest space
	DMChat      string         `json:"dm_chat_id,omitempty"`
	FirstSeen   time.Time      `json:"first_seen_at"`
	LastSeen    time.Time      `json:"last_seen_at"`
	LastNotice  time.Time      `json:"-"`
	// FirstText is the start of a pending sender's first message, shown to
	// admins in the approvals inbox; it is dropped once the status changes.
	FirstText string `json:"first_text,omitempty"`
}

type PairingPurpose string

const (
	PairLinkIdentity  PairingPurpose = "link_identity"  // user creates in web UI, sends /pair CODE (or /start CODE) from chat
	PairApproveSender PairingPurpose = "approve_sender" // unknown sender receives it; an admin enters it in the web UI
)

type Pairing struct {
	CodeHash   []byte         `json:"-"`
	Purpose    PairingPurpose `json:"purpose"`
	User       UserID         `json:"user_id,omitempty"`
	Channel    string         `json:"channel,omitempty"`
	ExternalID string         `json:"external_id,omitempty"`
	CreatedBy  UserID         `json:"created_by,omitempty"`
	ExpiresAt  time.Time      `json:"expires_at"`
	UsedAt     time.Time      `json:"used_at"`
	Attempts   int            `json:"attempts"`
}

type AuthSession struct {
	IDHash      []byte
	User        UserID
	CSRF        string
	IP, UA      string
	CreatedAt   time.Time
	LastSeen    time.Time
	IdleExpires time.Time
	Expires     time.Time
}

type TokenRecord struct {
	ID         string    `json:"id"` // tok_<26>, public part of bothy_pat_<id>_<secret>_<crc6>
	User       UserID    `json:"user_id"`
	Name       string    `json:"name"`
	SecretHash []byte    `json:"-"`
	Scopes     []string  `json:"scopes"`      // chat | llm | read | admin
	OnApproval string    `json:"on_approval"` // deny | wait
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsed   time.Time `json:"last_used_at"`
	Expires    time.Time `json:"expires_at"`
	Revoked    time.Time `json:"revoked_at"`
}

type Session struct {
	ID             SessionID   `json:"id"`
	Principal      PrincipalID `json:"principal_id"`
	Origin         string      `json:"origin"`      // web|telegram|discord|slack|api|schedule
	ChannelKey     string      `json:"channel_key"` // ChatRef.Key() for channel sessions; "" otherwise
	Chat           *ChatRef    `json:"chat,omitempty"`
	Title          string      `json:"title"`
	ModelSlot      Slot        `json:"model_slot"`
	ModelOverride  string      `json:"model_override,omitempty"` // "provider/model"
	SystemSnapshot string      `json:"-"`                        // frozen S2 block text
	ToolsSnapshot  []string    `json:"-"`                        // frozen core tool names
	SnapshotHash   string      `json:"-"`                        // sha256 of tools+system bytes at snapshot time
	Summary        string      `json:"-"`
	SummaryUpto    int64       `json:"summary_upto"` // last message seq folded into Summary
	Epoch          int         `json:"epoch"`        // incremented by each compaction
	MemorySeen     int64       `json:"-"`            // last memory_events id folded into the snapshot
	PinsSeen       int64       `json:"-"`            // last pin id folded into the snapshot
	Taint          Taint       `json:"taint"`
	Hidden         bool        `json:"hidden"` // ephemeral API sessions
	Archived       bool        `json:"archived"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	LastMessageAt  time.Time   `json:"last_message_at"`
}

type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunParked          RunStatus = "parked"
	RunDone            RunStatus = "done"
	RunStopped         RunStatus = "stopped"
	RunFailed          RunStatus = "failed"
	RunBudgetExceeded  RunStatus = "budget_exceeded"
	RunLoopDetected    RunStatus = "loop_detected"
	RunInterrupted     RunStatus = "interrupted" // process restarted mid-run
	RunSkipped         RunStatus = "skipped"     // schedule fired while target session queue was full
)

func (s RunStatus) Terminal() bool {
	switch s {
	case RunQueued, RunRunning, RunWaitingApproval, RunParked:
		return false
	}
	return true
}

type Run struct {
	ID         RunID       `json:"id"`
	Session    SessionID   `json:"session_id"`
	Principal  PrincipalID `json:"principal_id"`
	User       UserID      `json:"user_id"` // billing user (space owner for spaces)
	Trigger    Trigger     `json:"trigger"`
	Schedule   ScheduleID  `json:"schedule_id,omitempty"`
	Status     RunStatus   `json:"status"`
	Error      string      `json:"error,omitempty"`
	Steps      int         `json:"steps"`
	ToolCalls  int         `json:"tool_calls"`
	Usage      Usage       `json:"usage"`
	Cost       Micros      `json:"cost_micros"`
	Models     []string    `json:"models"`
	Limits     RunLimits   `json:"limits"`
	ParkedOn   ApprovalID  `json:"parked_on,omitempty"`
	StoppedBy  UserID      `json:"stopped_by,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt time.Time   `json:"finished_at"`
}

type RunFilter struct {
	Session  SessionID
	Schedule ScheduleID
	Status   RunStatus
	Page     Page
}

type ToolCallStatus string

const (
	TCPlanned  ToolCallStatus = "planned"
	TCWaiting  ToolCallStatus = "waiting"
	TCApproved ToolCallStatus = "approved"
	TCDenied   ToolCallStatus = "denied"
	TCRunning  ToolCallStatus = "running"
	TCOK       ToolCallStatus = "ok"
	TCError    ToolCallStatus = "error"
	TCTimeout  ToolCallStatus = "timeout"
	TCStopped  ToolCallStatus = "stopped"
	TCInvalid  ToolCallStatus = "invalid" // unknown tool or schema-invalid args
)

type ToolCallRecord struct {
	ID            string         `json:"id"` // tcl_<26>
	Run           RunID          `json:"run_id"`
	Session       SessionID      `json:"session_id"`
	Principal     PrincipalID    `json:"principal_id"`
	Step          int            `json:"step"`
	CallID        string         `json:"call_id"` // provider call id
	Tool          string         `json:"tool"`
	Source        string         `json:"source"`
	SideEffect    SideEffect     `json:"side_effect"`
	Action        *Action        `json:"action,omitempty"`
	Verdict       Verdict        `json:"verdict"`
	Rule          string         `json:"rule"`
	Approval      ApprovalID     `json:"approval_id,omitempty"`
	Status        ToolCallStatus `json:"status"`
	ResultPreview string         `json:"result_preview"` // <= 2KB, redacted
	ResultBytes   int            `json:"result_bytes"`
	FullRef       string         `json:"full_ref,omitempty"`
	Untrusted     bool           `json:"untrusted"`
	Duration      time.Duration  `json:"duration"`
	CreatedAt     time.Time      `json:"created_at"`
	FinishedAt    time.Time      `json:"finished_at"`
}

type ScheduleKind string

const (
	SchedCron      ScheduleKind = "cron"      // 5-field cron (gronx), timezone-aware
	SchedHeartbeat ScheduleKind = "heartbeat" // interval "30m", ±10% jitter, active hours
	SchedOnce      ScheduleKind = "once"      // RFC3339
)

type Schedule struct {
	ID          ScheduleID   `json:"id"`
	Principal   PrincipalID  `json:"principal_id"`
	Name        string       `json:"name"`
	Kind        ScheduleKind `json:"kind"`
	Spec        string       `json:"spec"`
	TZ          string       `json:"tz"`
	ActiveHours string       `json:"active_hours,omitempty"` // "08:00-22:00" in TZ
	Prompt      string       `json:"prompt"`
	Session     SessionID    `json:"session_id,omitempty"` // "" = dedicated schedule session
	Deliver     *ChatRef     `json:"deliver,omitempty"`    // nil = PreferredChat of the principal's user if any, otherwise web only; {channel:"web"} = web only
	Slot        Slot         `json:"slot"`
	Limits      RunLimits    `json:"limits"`
	MissedRun   string       `json:"missed_run"` // skip | once
	Enabled     bool         `json:"enabled"`
	CreatedBy   string       `json:"created_by"` // "user:<id>" | "run:<id>" (agent-created after admin approval) | "system" (heartbeat)
	Failures    int          `json:"failures"`   // consecutive; 3 => auto-disabled
	NextRunAt   time.Time    `json:"next_run_at"`
	LastRunAt   time.Time    `json:"last_run_at"`
	LastStatus  RunStatus    `json:"last_status,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type UsageQuery struct {
	User    UserID // "" = all users (admin)
	From    time.Time
	To      time.Time
	GroupBy string // day | model | user | schedule | session
}

type UsageRow struct {
	Key        string `json:"key"`
	Cost       Micros `json:"cost_micros"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cache_read"`
	CacheWrite int    `json:"cache_write"`
	Calls      int    `json:"calls"`
	PriceKnown bool   `json:"price_known"` // false if any call in the bucket had an unknown price
}

type AuditEntry struct {
	ID      int64           `json:"id"`
	At      time.Time       `json:"at"`
	Actor   string          `json:"actor"`  // user:<id> | agent:<run> | system | channel:<name>:<ext> | token:<id>
	Action  string          `json:"action"` // see §10 audit action list
	Target  string          `json:"target"`
	Outcome string          `json:"outcome"` // ok | denied | error
	Session SessionID       `json:"session_id,omitempty"`
	Run     RunID           `json:"run_id,omitempty"`
	IP      string          `json:"ip,omitempty"`
	Detail  json.RawMessage `json:"detail"` // redacted JSON; never message content
}

type AuditFilter struct {
	Actor        string
	ActionPrefix string
	From, To     time.Time
	Page         Page
}

type EgressRecord struct {
	ID        int64         `json:"id"`
	At        time.Time     `json:"at"`
	Dialer    string        `json:"dialer"` // box | public | system
	Session   SessionID     `json:"session_id,omitempty"`
	Principal PrincipalID   `json:"principal_id,omitempty"`
	Run       RunID         `json:"run_id,omitempty"`
	Dest      string        `json:"dest"` // SystemDest.Name for system dialer
	Host      string        `json:"host"`
	Port      int           `json:"port"`
	IP        string        `json:"ip,omitempty"`
	Decision  string        `json:"decision"` // allow | deny | held_allow | held_deny
	Reason    string        `json:"reason,omitempty"`
	Up        int64         `json:"bytes_up"`
	Down      int64         `json:"bytes_down"`
	Duration  time.Duration `json:"duration"`
}

type MCPPinStatus string

const (
	PinPending    MCPPinStatus = "pending"
	PinApproved   MCPPinStatus = "approved"
	PinSuperseded MCPPinStatus = "superseded"
	PinDisabled   MCPPinStatus = "disabled"
)

type MCPPin struct {
	Server     string            `json:"server"`
	Tool       string            `json:"tool"`
	Hash       string            `json:"hash"` // sha256 canonical {name,title,description,inputSchema,outputSchema,annotations}
	Definition json.RawMessage   `json:"definition"`
	Status     MCPPinStatus      `json:"status"`
	SideEffect SideEffect        `json:"side_effect"`
	FieldMap   map[string]string `json:"field_map,omitempty"` // {"recipient":"/to","url":"/url"}
	Private    bool              `json:"private"`             // output counts as private data
	ApprovedBy UserID            `json:"approved_by,omitempty"`
	FirstSeen  time.Time         `json:"first_seen_at"`
	ApprovedAt time.Time         `json:"approved_at"`
}

type MCPInstall struct {
	Server    string    `json:"server"`
	InstallID string    `json:"install_id"` // in_<26>; volume subpath mcp/<server>/<install_id>
	Version   string    `json:"version"`
	LockHash  string    `json:"lock_hash"` // sha256 of the recorded lockfile (TOFU)
	Lock      []byte    `json:"-"`         // package-lock.json or hashed requirements.txt
	TreeHash  string    `json:"tree_hash"`
	Status    string    `json:"status"` // installing | ready | failed | drifted
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type SkillStatus string

const (
	SkillPending  SkillStatus = "pending" // new: disabled until an admin enables
	SkillEnabled  SkillStatus = "enabled"
	SkillDisabled SkillStatus = "disabled"
	SkillChanged  SkillStatus = "changed" // on-disk hash != approved hash: excluded until re-approved
)

type SkillMeta struct {
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Path         string      `json:"path"`
	Hash         string      `json:"hash"`
	ApprovedHash string      `json:"approved_hash"`
	Status       SkillStatus `json:"status"`
	Tokens       int         `json:"tokens"`
	InstalledBy  UserID      `json:"installed_by,omitempty"`
	ApprovedBy   UserID      `json:"approved_by,omitempty"`
	InstalledAt  time.Time   `json:"installed_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

type MemoryFile string

const (
	FileMemory    MemoryFile = "MEMORY"
	FileUser      MemoryFile = "USER"
	FileHeartbeat MemoryFile = "HEARTBEAT"
)

type MemoryEvent struct {
	ID         int64       `json:"id"`
	Principal  PrincipalID `json:"principal_id"`
	File       MemoryFile  `json:"file"`
	Op         string      `json:"op"` // add | replace | remove | edit (web editor)
	Before     string      `json:"before"`
	After      string      `json:"after"`
	Run        RunID       `json:"run_id,omitempty"`
	Actor      string      `json:"actor"` // agent:<run> | user:<id>
	Provenance Provenance  `json:"provenance"`
	At         time.Time   `json:"at"`
}

type SearchHit struct {
	Session SessionID `json:"session_id"`
	Title   string    `json:"title"`
	Seq     int64     `json:"seq"`
	Snippet string    `json:"snippet"`
	At      time.Time `json:"at"`
	Rank    float64   `json:"rank"`
	Trusted bool      `json:"trusted"` // provenance user/system; untrusted hits taint the session
}

type Upload struct {
	ID        string      `json:"id"` // upl_<26>
	Principal PrincipalID `json:"principal_id"`
	Session   SessionID   `json:"session_id"`
	Name      string      `json:"name"`
	MIME      string      `json:"mime"`
	Size      int64       `json:"size"`
	Ref       string      `json:"ref"` // workspace-relative: ".bothy/in/<upl>/<name>"
	CreatedAt time.Time   `json:"created_at"`
}

type SecretMeta struct {
	Scope     Scope     `json:"scope"`
	Name      string    `json:"name"`
	Source    string    `json:"source"` // vault | env | aws-sm (a reference in the configuration; never a value)
	Hint      string    `json:"hint"`   // last 4 chars; always "" for aws-sm
	UpdatedAt time.Time `json:"updated_at"`
	LastUsed  time.Time `json:"last_used_at"`
}

// ---- repositories ----

type MetaRepo interface {
	Get(ctx context.Context, k string) (string, error) // ErrNotFound
	Set(ctx context.Context, k, v string) error
	Delete(ctx context.Context, k string) error
}

type UserRepo interface {
	Create(ctx context.Context, u *User) error // also creates the user principal + workspace id
	Get(ctx context.Context, id UserID) (*User, error)
	ByUsername(ctx context.Context, name string) (*User, error)
	List(ctx context.Context) ([]*User, error)
	Update(ctx context.Context, u *User) error
	RecordLogin(ctx context.Context, id UserID, ok bool, lockUntil time.Time) error
	Count(ctx context.Context) (int, error)
	// Delete removes the users row (user purge). Foreign keys cascade to its
	// principals, spaces, sessions (messages, pins, runs, approvals), grants,
	// schedules, memory events, uploads, pairing codes, auth sessions and
	// tokens. Channel identities linked to the user, or guests of its spaces,
	// must be deleted first (ErrInvalid otherwise). audit_log is untouched.
	Delete(ctx context.Context, id UserID) error
}

type PrincipalRepo interface {
	Get(ctx context.Context, v Viewer, id PrincipalID) (*Principal, error)
	ForUser(ctx context.Context, u UserID) ([]*Principal, error) // own principal first, then owned spaces
	Update(ctx context.Context, p *Principal) error
}

type SpaceRepo interface {
	Create(ctx context.Context, s *Space, p *Principal) error // creates the space principal too
	ByChat(ctx context.Context, channel, chatID string) (*Space, error)
	Get(ctx context.Context, v Viewer, id PrincipalID) (*Space, error)
	List(ctx context.Context, v Viewer) ([]*Space, error)
	Update(ctx context.Context, s *Space) error
}

type IdentityRepo interface {
	Touch(ctx context.Context, id *ChannelIdentity) (*ChannelIdentity, error) // upsert names/last_seen; returns stored row
	Get(ctx context.Context, channel, externalID string) (*ChannelIdentity, error)
	Update(ctx context.Context, id *ChannelIdentity) error // status, user, principal, dm chat, last notice, first text (kept only while pending)
	ForUser(ctx context.Context, u UserID) ([]*ChannelIdentity, error)
	List(ctx context.Context, status IdentityStatus) ([]*ChannelIdentity, error) // "" = all
	Delete(ctx context.Context, channel, externalID string) error
}

type PairingRepo interface {
	Create(ctx context.Context, p *Pairing) error
	// Redeem atomically marks an unexpired, unused code as used. Wrong codes
	// increment no row; callers rate-limit per identity.
	Redeem(ctx context.Context, codeHash []byte, purpose PairingPurpose, now time.Time) (*Pairing, error)
	ForIdentity(ctx context.Context, channel, externalID string, purpose PairingPurpose, now time.Time) (*Pairing, error)
}

type AuthSessionRepo interface {
	Create(ctx context.Context, s *AuthSession) error
	Get(ctx context.Context, idHash []byte) (*AuthSession, error)
	Touch(ctx context.Context, idHash []byte, lastSeen, idleExpires time.Time) error
	Delete(ctx context.Context, idHash []byte) error
	DeleteUser(ctx context.Context, u UserID) error
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

type TokenRepo interface {
	Create(ctx context.Context, t *TokenRecord) error
	Get(ctx context.Context, id string) (*TokenRecord, error)
	List(ctx context.Context, u UserID) ([]*TokenRecord, error)
	Revoke(ctx context.Context, u UserID, id string, at time.Time) error // ErrNotFound across users
	Touch(ctx context.Context, id string, at time.Time) error
}

type SessionFilter struct {
	Principal     PrincipalID // "" = all principals in the Viewer
	Archived      bool
	IncludeHidden bool
	NonEmpty      bool   // only sessions holding at least one message
	Query         string // title substring
	Page          Page   // Before = last_message_at unix ms
}

type SessionRepo interface {
	Create(ctx context.Context, s *Session) error
	Get(ctx context.Context, v Viewer, id SessionID) (*Session, error)
	ByChannelKey(ctx context.Context, p PrincipalID, key string) (*Session, error) // open (non-archived) only
	List(ctx context.Context, v Viewer, f SessionFilter) ([]*Session, error)
	Update(ctx context.Context, s *Session) error
	SetTaint(ctx context.Context, id SessionID, t Taint) error
	Archive(ctx context.Context, v Viewer, id SessionID) error
	AddURLs(ctx context.Context, id SessionID, urls []string, source string) error // URL provenance: search | user
	HasURL(ctx context.Context, id SessionID, url string) (bool, error)
}

type MessageRepo interface {
	Append(ctx context.Context, m *Message) error                                                   // assigns ID and Seq atomically; fills messages.text from PlainText
	Range(ctx context.Context, v Viewer, s SessionID, fromSeq int64, limit int) ([]*Message, error) // seq >= fromSeq ascending
	Page(ctx context.Context, v Viewer, s SessionID, p Page) ([]*Message, error)                    // newest first, Before = seq
	Search(ctx context.Context, v Viewer, query string, limit int) ([]SearchHit, error)             // FTS5 MATCH over Viewer's principals, bm25 order
}

type PinRepo interface {
	Add(ctx context.Context, p *Pin) error
	List(ctx context.Context, v Viewer, s SessionID) ([]*Pin, error)
	Delete(ctx context.Context, v Viewer, s SessionID, id int64) error
}

type RunRepo interface {
	Create(ctx context.Context, r *Run) error // runs_one_active index => ErrConflict
	Update(ctx context.Context, r *Run) error
	Get(ctx context.Context, v Viewer, id RunID) (*Run, error)
	List(ctx context.Context, v Viewer, f RunFilter) ([]*Run, error)
	MarkInterrupted(ctx context.Context, now time.Time) ([]*Run, error) // boot: queued|running|waiting_approval -> interrupted; returns the rows changed
	Parked(ctx context.Context) ([]*Run, error)
	AddLLMCall(ctx context.Context, run RunID, p PrincipalID, u UserID, c CallInfo) error
	LLMCalls(ctx context.Context, v Viewer, run RunID) ([]CallInfo, error)
	AddToolCall(ctx context.Context, tc *ToolCallRecord) error
	UpdateToolCall(ctx context.Context, tc *ToolCallRecord) error
	ToolCalls(ctx context.Context, v Viewer, run RunID) ([]*ToolCallRecord, error)
}

type ApprovalRepo interface {
	Create(ctx context.Context, a *Approval) error
	Get(ctx context.Context, v Viewer, id ApprovalID) (*Approval, error)
	// Decide is a CAS: it updates only when status is one of from. Returns false
	// when another resolver won.
	Decide(ctx context.Context, id ApprovalID, from []ApprovalStatus, to ApprovalStatus, c Choice, by UserID, via string, at time.Time) (bool, error)
	Park(ctx context.Context, id ApprovalID, until time.Time) error
	Pending(ctx context.Context, v Viewer) ([]*Approval, error)
	Recent(ctx context.Context, v Viewer, p Page) ([]*Approval, error) // not pending/parked, newest first; Before = created_at ms
	Due(ctx context.Context, now time.Time) ([]*Approval, error)       // pending past expires_at, parked past park_until
	ByRun(ctx context.Context, run RunID) ([]*Approval, error)
	// Seen reports whether an allowed approval of this principal targeted the
	// recipient (Action.Recipient) or host (Action.Host); used by the policy
	// first-contact step. Empty arguments never match.
	Seen(ctx context.Context, p PrincipalID, recipient, host string) (bool, error)
	AddMessage(ctx context.Context, id ApprovalID, m CardMessage) error
	Messages(ctx context.Context, id ApprovalID) ([]CardMessage, error)
	// AddVote records one vote on a multi-approver approval: ErrConflict when
	// the user already voted on it, ErrInvalid for an unknown approval or a
	// choice other than allow_once or deny.
	AddVote(ctx context.Context, v ApprovalVote) error
	Votes(ctx context.Context, id ApprovalID) ([]ApprovalVote, error) // oldest first
	// SetComment records the deciding approver's reason (Approval.Comment).
	SetComment(ctx context.Context, id ApprovalID, comment string) error
}

type GrantRepo interface {
	Add(ctx context.Context, g *Grant) error
	// Active returns unexpired, unrevoked grants for the principal that apply to
	// the session and/or schedule (plus principal-scoped grants).
	Active(ctx context.Context, p PrincipalID, s SessionID, sch ScheduleID, now time.Time) ([]*Grant, error)
	Use(ctx context.Context, id GrantID) error
	List(ctx context.Context, v Viewer, scope GrantScope, scopeID string) ([]*Grant, error)
	Revoke(ctx context.Context, v Viewer, id GrantID, at time.Time) error
}

type ScheduleRepo interface {
	Upsert(ctx context.Context, s *Schedule) error
	Get(ctx context.Context, v Viewer, id ScheduleID) (*Schedule, error)
	List(ctx context.Context, v Viewer) ([]*Schedule, error)
	Delete(ctx context.Context, v Viewer, id ScheduleID) error
	NextDue(ctx context.Context) (*Schedule, error) // enabled, min next_run_at; ErrNotFound when none
	SetRun(ctx context.Context, id ScheduleID, last, next time.Time, status RunStatus, failures int, enabled bool) error
}

type UsageRepo interface {
	AddSpend(ctx context.Context, u UserID, day string, c CallInfo) error // spend_daily UPSERT
	Spend(ctx context.Context, u UserID, day string) (Micros, error)      // u == "" => global total
	Report(ctx context.Context, q UsageQuery) ([]UsageRow, error)         // aggregates llm_calls
}

type AuditRepo interface {
	// Append is called only by the audit writer, which computes the chain.
	Append(ctx context.Context, e *AuditEntry, prevHash, hash []byte) (int64, error)
	Head(ctx context.Context) (id int64, hash []byte, err error)
	Range(ctx context.Context, fromID, toID int64, fn func(e *AuditEntry, prevHash, hash []byte) error) error
	Query(ctx context.Context, f AuditFilter) ([]*AuditEntry, error)
}

type EgressRepo interface {
	Add(ctx context.Context, rs []EgressRecord) error // batched by the egress module
	// Query returns rows of the Viewer's principals (s "" = all of them), plus
	// system-dialer rows (principal_id "") when v.Admin. Before = id.
	Query(ctx context.Context, v Viewer, s SessionID, p Page) ([]*EgressRecord, error)
	Prune(ctx context.Context, before time.Time) (int64, error)
}

type MCPRepo interface {
	UpsertPin(ctx context.Context, p *MCPPin) (MCPPinStatus, error) // unseen hash => pending; same as approved => approved
	ApprovePins(ctx context.Context, server string, approve map[string]MCPPin, by UserID, at time.Time) error
	Pins(ctx context.Context, server string) ([]*MCPPin, error) // latest per tool
	Approved(ctx context.Context, server string) (map[string]*MCPPin, error)
	DisableTool(ctx context.Context, server, tool string) error
	PutInstall(ctx context.Context, in *MCPInstall) error
	Install(ctx context.Context, server string) (*MCPInstall, error) // current ready (or latest) install
}

type SkillRepo interface {
	Upsert(ctx context.Context, s *SkillMeta) error
	Get(ctx context.Context, name string) (*SkillMeta, error)
	List(ctx context.Context) ([]*SkillMeta, error)
	Delete(ctx context.Context, name string) error
}

type MemoryEventRepo interface {
	Add(ctx context.Context, e *MemoryEvent) (int64, error)
	Since(ctx context.Context, p PrincipalID, afterID int64) ([]*MemoryEvent, error)
	List(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile, page Page) ([]*MemoryEvent, error)
}

type VaultRepo interface {
	ActiveKey(ctx context.Context) (id int64, wrapped, check []byte, source string, err error) // ErrNotFound on first boot
	PutKey(ctx context.Context, wrapped, check []byte, source string) (int64, error)           // deactivates the previous key
	PutSecret(ctx context.Context, sc Scope, name string, keyID int64, version int, nonce, ct []byte, hint string, by UserID) error
	GetSecret(ctx context.Context, sc Scope, name string) (keyID int64, version int, nonce, ct []byte, err error)
	DeleteSecret(ctx context.Context, sc Scope, name string) error
	ListSecrets(ctx context.Context, sc Scope) ([]SecretMeta, error)
	TouchSecret(ctx context.Context, sc Scope, name string, at time.Time) error
	// Rewrap runs fn for every secret in one transaction (KEK/DEK rotation).
	Rewrap(ctx context.Context, fn func(sc Scope, name string, keyID int64, version int, nonce, ct []byte) (newKeyID int64, newNonce, newCT []byte, err error)) error
}

type UploadRepo interface {
	Create(ctx context.Context, u *Upload) error
	Get(ctx context.Context, v Viewer, id string) (*Upload, error)
}

type DedupRepo interface {
	Seen(ctx context.Context, channel, msgID string, at time.Time) (bool, error) // insert-or-report
	Prune(ctx context.Context, before time.Time) (int64, error)
}

type Store interface {
	Meta() MetaRepo
	Users() UserRepo
	Principals() PrincipalRepo
	Spaces() SpaceRepo
	Identities() IdentityRepo
	Pairings() PairingRepo
	AuthSessions() AuthSessionRepo
	Tokens() TokenRepo
	Sessions() SessionRepo
	Messages() MessageRepo
	Pins() PinRepo
	Runs() RunRepo
	Approvals() ApprovalRepo
	Grants() GrantRepo
	Schedules() ScheduleRepo
	Usage() UsageRepo
	Audit() AuditRepo
	Egress() EgressRepo
	MCP() MCPRepo
	Skills() SkillRepo
	MemoryEvents() MemoryEventRepo
	Vault() VaultRepo
	Dedup() DedupRepo
	Uploads() UploadRepo
	// Tx runs fn on the single writer connection inside BEGIN IMMEDIATE.
	Tx(ctx context.Context, fn func(Store) error) error
	// Backup writes <dir>/bothy-<UTC yyyymmddThhmmss>.tar (0600) holding a
	// VACUUM INTO snapshot of the DB plus memory/**, knowledge/** (project
	// notes), SOUL.md, bothy.yaml and bothy.ui.yaml from the DB's directory.
	// It never includes the KEK (/keys) or workspaces. Returns the path.
	// Restore = stop, untar into /data.
	Backup(ctx context.Context, dir string) (string, error)
	SQLiteVersion() string
	Close() error
}
