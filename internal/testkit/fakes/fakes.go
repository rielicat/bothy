// Package fakes holds in-memory implementations of core interfaces for unit tests.
package fakes

import (
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

// Clock is a manually advanced core.Clock (use testing/synctest for timers).
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(t time.Time) *Clock        { return &Clock{now: t} }
func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// Auditor records entries; Verify always reports an intact chain.
type Auditor struct {
	mu      sync.Mutex
	Entries []core.AuditEntry
}

// Vault is an in-memory core.Vault with an exact-match Redactor. Seal() makes
// Get/Resolve return core.ErrSealed.
type Vault struct{ core.Vault }

func NewVault(secrets map[string]string) *Vault { return &Vault{} }

// Policy returns Verdicts[tool] (default Default) with Rule "fake".
type Policy struct {
	core.Policy
	Verdicts map[string]core.Verdict
	Default  core.Verdict
}

// Approvals resolves every request immediately with Choice (or blocks until
// Resolve when Manual is true). Records requests.
type Approvals struct {
	core.Approvals
	Choice   core.Choice
	Manual   bool
	Requests []core.ApprovalRequest
}

// LLM replays scripted turns: each Step streams Text and/or ToolCalls and
// returns Usage; Err fails the call. Requests records every BuildFunc result.
type LLM struct {
	core.LLM
	Steps    []LLMStep
	Requests []*core.Request
	Info     core.ModelInfo
}

type LLMStep struct {
	Text      []string // streamed as deltas
	ToolCalls []core.ToolCall
	Usage     core.Usage
	Err       error
	Block     chan struct{} // if set, blocks after the first delta until closed or ctx done
}

// Sink records run events.
type Sink struct {
	mu     sync.Mutex
	Events []core.Event
}

func (s *Sink) OnEvent(e core.Event) { s.mu.Lock(); s.Events = append(s.Events, e); s.mu.Unlock() }

// Runner records Submit/Stop/StopAll/Resume calls and returns Result (or Err)
// from Submit; Paused/SetPaused toggle a flag; ResolveSession/NewSession/Fork
// create sessions in Store (a memstore) when set.
type Runner struct {
	core.Runner
	Store   core.Store
	Result  core.SubmitResult
	Err     error
	Submits []core.RunRequest
	Stops   []core.SessionID
	Resumed []core.ApprovalID
	PausedV bool
}

// Hub records Messenger sends and StreamTo calls; StreamTo returns a *Sink.
// PreferredChats maps users to their preferred DM.
type Hub struct {
	core.ChannelHub
	Sent           []string // "<chat key>|<markdown>"
	Streams        map[string]*Sink
	PreferredChats map[core.UserID]core.ChatRef
}

// Auth resolves identities from Identities (key "<channel>:<external id>"),
// builds Viewers from Viewers, and records pairing and sender-notice calls.
type Auth struct {
	core.Auth
	Identities map[string]core.IdentityResolution
	Viewers    map[core.UserID]core.Viewer
	Notices    []core.ChannelIdentity
}

// Budget allows everything unless Deny is set; StartRun returns a meter that
// enforces only the given RunLimits steps and records AfterCall infos.
type Budget struct {
	core.Budget
	Deny  *core.BudgetError
	Calls []core.CallInfo
}

// Registry is a map-backed core.ToolRegistry with the real filtering rules
// reduced to: trigger, principal kind (SpaceOK), sandbox (Exec), Gated.
type Registry struct {
	core.ToolRegistry
	Tools map[string]core.Tool
}

// Memory, Skills, MCP and Scheduler embed their interface and implement the
// read paths over in-memory maps (files, skill bodies, statuses, schedules).
type Memory struct {
	core.Memory
	Files map[core.PrincipalID]map[core.MemoryFile]string
}

type Skills struct {
	core.Skills
	Metas  []core.SkillMeta
	Bodies map[string]string
}

type MCP struct {
	core.MCP
	StatusV []core.MCPStatus
}

type Scheduler struct {
	core.Scheduler
	Schedules map[core.ScheduleID]*core.Schedule
}
