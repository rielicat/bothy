package core

import "time"

type EventType string

const (
	EvRunQueued         EventType = "run.queued"
	EvRunStarted        EventType = "run.started"
	EvRunStatus         EventType = "run.status"
	EvRunDelta          EventType = "run.delta"    // coalesced by the runner to <= 20/s per run
	EvRunThinking       EventType = "run.thinking" // coalesced like run.delta
	EvToolCall          EventType = "run.tool_call"
	EvToolOutput        EventType = "run.tool_output" // <= 8KB chunk, <= 10/s per call
	EvToolResult        EventType = "run.tool_result"
	EvRunUsage          EventType = "run.usage"
	EvRunParked         EventType = "run.parked"
	EvRunFinished       EventType = "run.finished"
	EvApprovalRequested EventType = "approval.requested"
	EvApprovalResolved  EventType = "approval.resolved"
	EvApprovalVoted     EventType = "approval.voted" // ApprovalData: a vote that did not resolve a multi-approver approval
	EvReleaseDispatched EventType = "release.dispatched"
	EvReleaseFinished   EventType = "release.finished"
	EvInboxChanged      EventType = "inbox.changed" // a new unknown sender or group chat item; refetch GET /api/inbox
	EvMessageCreated    EventType = "message.created"
	EvSessionUpdated    EventType = "session.updated"
	EvMemoryUpdated     EventType = "memory.updated"
	EvEgressBlocked     EventType = "egress.blocked"
	EvScheduleRun       EventType = "schedule.run"
	EvBudgetWarning     EventType = "budget.warning"
	EvPairingLinked     EventType = "pairing.linked"
	EvChannelStatus     EventType = "channel.status"  // admin broadcast
	EvSandboxStatus     EventType = "sandbox.status"  // admin broadcast
	EvMCPPinPending     EventType = "mcp.pin_pending" // admin broadcast
	EvConfigChanged     EventType = "config.changed"  // admin broadcast
	EvBanner            EventType = "banner"          // admin broadcast or per principal
)

// EventData is implemented only by the payload types below; each EventType has
// exactly one payload type (see the table in SPEC §8.2).
type EventData interface{ eventData() }

type Event struct {
	Seq       uint64      `json:"seq"`
	Type      EventType   `json:"type"`
	Principal PrincipalID `json:"principal_id,omitempty"` // "" = admin broadcast
	Session   SessionID   `json:"session_id,omitempty"`
	Run       RunID       `json:"run_id,omitempty"`
	At        time.Time   `json:"at"`
	Data      EventData   `json:"data"`
}

type RunQueuedData struct {
	Position int `json:"position"`
}
type RunStartedData struct {
	Trigger Trigger `json:"trigger"`
	Model   string  `json:"model"`
}
type RunStatusData struct {
	Status string `json:"status"` // thinking | tool | waiting_approval | compacting | queued
	Detail string `json:"detail,omitempty"`
}
type DeltaData struct {
	Text string `json:"text"`
}
type ToolCallData struct {
	CallID  string  `json:"call_id"` // tcl_<26>
	Tool    string  `json:"tool"`
	Title   string  `json:"title"`
	Action  *Action `json:"action"`
	Verdict Verdict `json:"verdict"`
	Rule    string  `json:"rule"`
	// ProviderID is the model's own id of the call (the persisted
	// tool_call part's id), so a client matches live rows to the
	// transcript exactly.
	ProviderID string `json:"provider_call_id,omitempty"`
}
type ToolOutputData struct {
	CallID string `json:"call_id"`
	Stream int    `json:"stream"` // 1 stdout, 2 stderr
	Chunk  string `json:"chunk"`  // redacted
}
type ToolResultData struct {
	CallID    string         `json:"call_id"`
	Status    ToolCallStatus `json:"status"`
	Preview   string         `json:"preview"` // <= 2KB, redacted
	Bytes     int            `json:"bytes"`
	Ms        int64          `json:"ms"`
	Truncated bool           `json:"truncated"`
	Untrusted bool           `json:"untrusted"`
}
type UsageData struct {
	Call     CallInfo `json:"call"`
	RunCost  Micros   `json:"run_cost_micros"`
	DayCost  Micros   `json:"day_cost_micros"`
	DayLimit Micros   `json:"day_limit_micros"`
}
type RunParkedData struct {
	Approval ApprovalID `json:"approval_id"`
}
type RunFinishedData struct {
	Status RunStatus  `json:"status"`
	Error  string     `json:"error,omitempty"`
	Budget BudgetKind `json:"budget,omitempty"` // which limit stopped the run when Status is budget_exceeded
	Steps  int        `json:"steps"`
	Usage  Usage      `json:"usage"`
	Cost   Micros     `json:"cost_micros"`
}
type ApprovalData struct {
	Approval *Approval `json:"approval"`
}
type ApprovalResolvedData struct {
	Approval ApprovalID     `json:"approval_id"`
	Status   ApprovalStatus `json:"status"`
	Choice   Choice         `json:"choice,omitempty"`
	By       UserID         `json:"by,omitempty"`
	Via      string         `json:"via"`
	Comment  string         `json:"comment,omitempty"` // the deciding approver's reason
}

// ReleaseDispatchedData: GitHub accepted a deploy's workflow_dispatch.
type ReleaseDispatchedData struct {
	Deploy      string `json:"deploy_id"`
	Environment string `json:"environment"`
	Repo        string `json:"repo"`
	Ref         string `json:"ref"`
	URL         string `json:"url,omitempty"` // the workflow run's page, when identified
}

// ReleaseFinishedData: Bothy stopped following a deploy, or GitHub refused
// to start it.
type ReleaseFinishedData struct {
	Deploy      string        `json:"deploy_id"`
	Environment string        `json:"environment"`
	Repo        string        `json:"repo"`
	Ref         string        `json:"ref"`
	Status      DeployStatus  `json:"status"` // succeeded | failed | unknown
	OK          bool          `json:"ok"`
	URL         string        `json:"url,omitempty"`
	Duration    time.Duration `json:"duration"` // the run's own start to finish when known
	// FailedStep names the workflow step a failed run stopped at, as written
	// in the repository's workflow file (show it verbatim, never as markup).
	FailedStep string `json:"failed_step,omitempty"`
}

// InboxChangedData names the approvals-inbox item that appeared, e.g.
// "sender:telegram:42" or "group:spc_…" (dto.InboxItem.ID).
type InboxChangedData struct {
	Item string `json:"item"`
}
type MessageData struct {
	Message *Message `json:"message"`
}
type SessionData struct {
	Title string `json:"title"`
	Taint Taint  `json:"taint"`
	Epoch int    `json:"epoch"`
}
type MemoryData struct {
	File    MemoryFile `json:"file"`
	EventID int64      `json:"event_id"`
}
type EgressBlockedData struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Reason string `json:"reason"`
}
type ScheduleRunData struct {
	Schedule ScheduleID `json:"schedule_id"`
	Status   RunStatus  `json:"status"`
}
type BudgetWarningData struct {
	User  UserID `json:"user_id"`
	Spent Micros `json:"spent_micros"`
	Limit Micros `json:"limit_micros"`
}
type PairingLinkedData struct {
	Channel    string `json:"channel"`
	ExternalID string `json:"external_id"`
	Display    string `json:"display"`
}
type ChannelStatusData struct {
	Status ChannelStatus `json:"status"`
}
type SandboxStatusData struct {
	Status SandboxStatus `json:"status"`
}
type MCPPinData struct {
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
}
type ConfigChangedData struct {
	Paths           []string `json:"paths"`
	RestartRequired []string `json:"restart_required"`
}
type BannerData struct {
	Level  string `json:"level"` // info | warn | error
	Code   string `json:"code"`  // no_sandbox | sandbox_not_isolated | config_error | provider_auth | provider_key_unread | paused | mcp_pins | mcp_drifted | budget_80 | vault_sealed | safe_mode | restart_required | unknown_sender | skills_changed
	Text   string `json:"text"`
	Action string `json:"action,omitempty"` // SPA route, e.g. "/settings/sandbox"
	Clear  bool   `json:"clear,omitempty"`  // true removes the banner with this Code
	// By, ByName and At say who raised it and when (paused: who paused).
	By     UserID    `json:"by,omitempty"`
	ByName string    `json:"by_name,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// PausedBanner is the paused banner, naming who paused and when.
func PausedBanner(by UserID, name string, at time.Time) BannerData {
	text := "Bothy is paused by an admin."
	if name != "" {
		text = "Bothy is paused by " + name + "."
	}
	return BannerData{Level: "error", Code: "paused", Text: text, Action: "/settings/security", By: by, ByName: name, At: at}
}

func (RunQueuedData) eventData()        {}
func (RunStartedData) eventData()       {}
func (RunStatusData) eventData()        {}
func (DeltaData) eventData()            {}
func (ToolCallData) eventData()         {}
func (ToolOutputData) eventData()       {}
func (ToolResultData) eventData()       {}
func (UsageData) eventData()            {}
func (RunParkedData) eventData()        {}
func (RunFinishedData) eventData()      {}
func (ApprovalData) eventData()         {}
func (ApprovalResolvedData) eventData() {}
func (MessageData) eventData()          {}
func (SessionData) eventData()          {}
func (MemoryData) eventData()           {}
func (EgressBlockedData) eventData()    {}
func (ScheduleRunData) eventData()      {}
func (BudgetWarningData) eventData()    {}
func (PairingLinkedData) eventData()    {}
func (ChannelStatusData) eventData()    {}
func (SandboxStatusData) eventData()    {}
func (MCPPinData) eventData()           {}
func (ConfigChangedData) eventData()    {}
func (BannerData) eventData()           {}

func (ReleaseDispatchedData) eventData() {}
func (ReleaseFinishedData) eventData()   {}
func (InboxChangedData) eventData()      {}

type Filter struct {
	Principals []PrincipalID // events for these principals
	Admin      bool          // also admin broadcasts (Principal == "")
	Session    SessionID     // optional narrowing
	Types      []EventType   // optional narrowing
	System     bool          // everything (internal subscribers only)
}

type Subscription interface {
	C() <-chan Event
	Dropped() uint64 // > 0 => the consumer fell behind and must resync
	Close()
}

type Bus interface {
	Publish(e Event) Event // assigns Seq and At; never blocks
	Subscribe(f Filter, buf int) Subscription
	// Since replays events with Seq > after from the 4096-entry ring.
	// complete=false means the gap is outside the ring: the client must resync.
	Since(after uint64, f Filter) (evs []Event, complete bool)
	BootID() string // random per process; SSE ids are "<boot>-<seq>"
	// Banners returns the active banner events matching f: the bus keeps the
	// latest EvBanner per (Principal, Code) and drops it on Clear. dto.Me and the
	// SSE hello use it, so banners survive page reloads.
	Banners(f Filter) []Event
}
