// Package dto holds the JSON shapes of the REST API that are not core types.
// Frozen in the skeleton; web/src/lib/api/types.gen.ts is generated from it and
// from internal/core by tygo (make gen).
package dto

import (
	"encoding/json"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// Error is the envelope for every non-2xx /api response.
type Error struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"` // bad_request | unauthorized | forbidden | password_change_required | not_found | conflict | busy | paused | budget_exceeded | rate_limited | sealed | no_sandbox | unavailable | internal; a failed POST /api/channels/{name}/test (502) carries a core.FailureCode
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

type List[T any] struct {
	Items []T   `json:"items"`
	Next  int64 `json:"next,omitempty"` // pass as ?before= for the next page; 0 = end
}

// ---- setup / auth ----

type SetupState struct {
	NeedsSetup bool        `json:"needs_setup"`
	Claimed    bool        `json:"claimed"`        // a setup grant cookie is present and valid
	CSRF       string      `json:"csrf,omitempty"` // the claimed grant's CSRF token (like dto.Me.CSRF); empty unless Claimed
	OwnerSet   bool        `json:"owner_set"`
	Sandbox    bool        `json:"sandbox_available"`
	Install    InstallKind `json:"install,omitempty"` // always set; picks the command that prints a new setup code
	Version    string      `json:"-"`                 // never disclosed pre-auth
}

type SetupClaimReq struct {
	Token string `json:"token"`
}

type SetupClaimResp struct {
	CSRF string `json:"csrf"`
}

type SetupOwnerReq struct {
	DisplayName string `json:"display_name,omitempty"` // "" = the username
	Username    string `json:"username"`
	Password    string `json:"password"`
	Timezone    string `json:"timezone"`
}

type LoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Me struct {
	User       core.User         `json:"user"`
	CSRF       string            `json:"csrf"`
	Mode       string            `json:"mode"` // single | multi
	Principals []*core.Principal `json:"principals"`
	Paused     bool              `json:"paused"`
	// PausedBy, PausedByName and PausedAt say who paused Bothy and when,
	// while it is paused (empty for a pause recorded before they were kept).
	// PausedBy is a user id, or "channel:<app>:<id>" for an approver's
	// /stopall in a chat app.
	PausedBy     string            `json:"paused_by,omitempty"`
	PausedByName string            `json:"paused_by_name,omitempty"`
	PausedAt     time.Time         `json:"paused_at,omitzero"`
	Sandbox      bool              `json:"sandbox_available"`
	Banners      []core.BannerData `json:"banners"`
	Access       Access            `json:"access"` // how this request reached the server
	// WelcomePending: a signed-in web account that hasn't had its welcome
	// chat; the app asks for it once (POST /api/me/welcome).
	WelcomePending bool `json:"welcome_pending,omitempty"`
}

// Profile is GET and PATCH /api/me: what people set about themselves.
type Profile struct {
	User        core.UserID `json:"user_id"`
	Username    string      `json:"username"`
	DisplayName string      `json:"display_name"`
	// Timezone is the person's IANA time zone, which their new schedules
	// and their chats with Bothy on the web use.
	Timezone string `json:"timezone"`
}

// ProfilePatch changes the caller's own profile; absent fields stay.
type ProfilePatch struct {
	DisplayName *string `json:"display_name,omitempty"`
	Timezone    *string `json:"timezone,omitempty"`
}

// Access is the caller's remote-access status for Settings > General.
type Access struct {
	Peer string `json:"peer"` // local | lan | public (exposure-gate class of the client address)
	TLS  bool   `json:"tls"`  // native TLS or X-Forwarded-Proto https from a trusted proxy
}

type PasswordReq struct {
	Old string `json:"old"` // may be "" while the user's must_change_password is set
	New string `json:"new"`
}

type TokenCreateResp struct {
	Token string            `json:"token"` // plaintext, shown once
	Meta  *core.TokenRecord `json:"meta"`
}

type UserCreateReq struct {
	Username    string    `json:"username"`
	Password    string    `json:"password,omitempty"` // "" = generate a one-time password (UserCreateResp)
	DisplayName string    `json:"display_name"`
	Role        core.Role `json:"role"`
}

type UserPatchReq struct {
	DisplayName *string    `json:"display_name,omitempty"`
	Role        *core.Role `json:"role,omitempty"`
	Disabled    *bool      `json:"disabled,omitempty"`
}

// IdentityPatchReq is an admin's change to a person in a chat app; absent
// fields stay.
type IdentityPatchReq struct {
	Status     *core.IdentityStatus `json:"status,omitempty"` // teammate (let in, unblock) | blocked
	CanApprove *bool                `json:"can_approve,omitempty"`
}

// Teammate is a person in a chat app as the People page lists them.
type Teammate struct {
	Channel    string              `json:"channel"`
	ExternalID string              `json:"external_id"` // also the budget key "<channel>:<external_id>"
	Name       string              `json:"name"`        // display name, else @handle, else "Someone"
	Handle     string              `json:"handle,omitempty"`
	Status     core.IdentityStatus `json:"status"` // teammate | blocked
	CanApprove bool                `json:"can_approve"`
	Left       bool                `json:"left"` // the chat app says they're no longer in the team
	LastSeen   time.Time           `json:"last_seen_at"`
	SpentToday core.Micros         `json:"spent_today_micros"`
	Principal  core.PrincipalID    `json:"principal_id,omitempty"`
}

// SpaceView is a group chat with its tool profile.
type SpaceView struct {
	core.Space
	ToolProfile string `json:"tool_profile"`
}

// TeamSchedule is a teammate's or a channel's schedule in the admins' list.
type TeamSchedule struct {
	ID         core.ScheduleID `json:"id"`
	Name       string          `json:"name"`
	Owner      string          `json:"owner"`      // teammate name or channel title
	OwnerKind  string          `json:"owner_kind"` // teammate | channel
	App        string          `json:"app"`
	Kind       string          `json:"kind"` // cron | once
	Spec       string          `json:"spec"`
	TZ         string          `json:"tz"`
	NextRun    time.Time       `json:"next_run_at,omitzero"`
	LastStatus core.RunStatus  `json:"last_status,omitempty"`
	Enabled    bool            `json:"enabled"`
}

// GrantView is a grant with the names of whom it covers and who made it.
type GrantView struct {
	core.Grant
	PrincipalName string `json:"principal_name"`
	CreatedByName string `json:"created_by_name"`
}

type SpacePatchReq struct {
	Title       *string `json:"title,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	MentionOnly *bool   `json:"mention_only,omitempty"`
	ToolProfile *string `json:"tool_profile,omitempty"` // restricted | standard (admin)
}

// ---- sessions / messages / runs ----

type SessionCreateReq struct {
	Principal core.PrincipalID `json:"principal_id,omitempty"` // default: caller's own
	Title     string           `json:"title,omitempty"`
	// Message, when set, is sent as the first message in the same request, so
	// a chat is stored only together with its first message (202 SessionCreated).
	Message *SendReq `json:"message,omitempty"`
}

type SessionPatchReq struct {
	Title         *string `json:"title,omitempty"`
	ModelOverride *string `json:"model_override,omitempty"` // "" clears
	Refine        *string `json:"refine,omitempty"`         // "on" | "off"; "" clears (follow agent.refine.enabled)
}

type SessionDetail struct {
	Session core.Session     `json:"session"`
	Pins    []*core.Pin      `json:"pins"`
	Active  *core.RunInfo    `json:"active,omitempty"`
	Pending []*core.Approval `json:"pending_approvals"`
	Refine  string           `json:"refine"` // the session's refine setting: "on" | "off" | "" (follows agent.refine.enabled)
	From    *SessionFrom     `json:"from,omitempty"`
}

// SessionPage is GET /api/sessions: a page of chats, and the chats their
// families hang from that aren't on the page (omitted with ?q=).
type SessionPage struct {
	Items   []core.Session `json:"items"`
	Next    int64          `json:"next,omitempty"`
	Parents []core.Session `json:"parents,omitempty"`
}

// SessionFrom says where a chat came from, for the line at its top.
type SessionFrom struct {
	Kind  string `json:"kind"`            // chat | schedule | checkin
	ID    string `json:"id,omitempty"`    // empty when that chat was deleted or the schedule is gone
	Title string `json:"title,omitempty"` // the chat's title or the schedule's name ("Check-in" for the check-in)
}

// StarterRow is one row of GET /api/starters: a prompt that fills the
// message box (Fill, else Text) or, with Href, a link to set something up.
type StarterRow struct {
	Text string `json:"text"`
	Fill string `json:"fill,omitempty"`
	Href string `json:"href,omitempty"`
}

// WelcomeResp answers POST /api/me/welcome: the new chat the first time,
// nothing after.
type WelcomeResp struct {
	ChatID core.SessionID `json:"chat_id,omitempty"`
}

type MessagePage struct {
	Messages []*core.Message `json:"messages"` // oldest first within the page; tool_call parts carry state and approval_id
	HasMore  bool            `json:"has_more"`
	// Approvals the page's tool calls asked for, pending or decided, so a
	// decided card stays in the transcript after a reload.
	Approvals []*core.Approval `json:"approvals"`
}

type SendReq struct {
	Text    string   `json:"text"`
	Uploads []string `json:"uploads,omitempty"` // upl_<26> ids from POST /api/uploads
	Refine  *bool    `json:"refine,omitempty"`  // refine this turn's answer; omitted = the session's setting
}

type Upload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}

type PinReq struct {
	Text   string `json:"text,omitempty"`
	MsgSeq int64  `json:"msg_seq,omitempty"`
}

type StopResp struct {
	Stopped bool `json:"stopped"`
}

type RunDetail struct {
	Run       *RunView               `json:"run"`
	LLMCalls  []core.CallInfo        `json:"llm_calls"`
	ToolCalls []*core.ToolCallRecord `json:"tool_calls"`
	Approvals []*core.Approval       `json:"approvals"`
	Refine    *core.RefineTrace      `json:"refine,omitempty"` // the run's last refined answer; entries name their llm_calls
}

// RefineInfo is what the composer needs to offer refine mode and say what it
// costs: the defaults, and the most model calls one refined answer takes.
type RefineInfo struct {
	Enabled    bool      `json:"enabled"` // agent.refine.enabled: on for messages and sessions that do not say
	Drafts     int       `json:"drafts"`
	Depth      int       `json:"depth"`
	EarlyStop  bool      `json:"early_stop"`
	CriticSlot core.Slot `json:"critic_slot"`
	MaxCalls   int       `json:"max_calls"` // drafts × (1 + 3 × depth), the answer step included
	// Multiplier is about how many normal replies a refined one costs, early
	// stop considered: the refine estimate of a chat with no priced answer
	// yet, or of a new chat not stored until its first message.
	Multiplier float64 `json:"multiplier"`
}

type ApprovalResolveReq struct {
	Choice  core.Choice `json:"choice"`
	Comment string      `json:"comment,omitempty"`
}

type GrantCreateReq struct { // admin, persistent principal grants only
	Principal core.PrincipalID  `json:"principal_id"`
	Kind      core.GrantKind    `json:"kind"`
	Tool      string            `json:"tool,omitempty"`
	Pattern   map[string]string `json:"pattern,omitempty"`
	Value     string            `json:"value,omitempty"`
	MaxSE     core.SideEffect   `json:"max_se"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
}

type PolicyPreviewReq struct {
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
	Session core.SessionID  `json:"session_id,omitempty"` // taint and grants context; default: a clean session
	Trigger core.Trigger    `json:"trigger,omitempty"`
}

type PolicyPreviewResp struct {
	Action   *core.Action  `json:"action"`
	Decision core.Decision `json:"decision"`
	Card     *core.Card    `json:"card,omitempty"` // when the verdict is ask
}

// ---- memory / skills / schedules ----

type MemoryDoc struct {
	File     core.MemoryFile `json:"file"` // MEMORY | USER | HEARTBEAT; TEAM from /api/memory/team
	Content  string          `json:"content"`
	ETag     string          `json:"etag"`
	CapBytes int             `json:"cap_bytes"`
}

type MemoryPutReq struct {
	Content string `json:"content"` // If-Match header carries the etag
}

// MemoryChange is one change in a memory file's history
// (GET /api/memory/{file}/history), with names for the person and chat.
type MemoryChange struct {
	core.MemoryEvent
	ActorName string    `json:"actor_name,omitempty"` // the person's name, for actor user:<id>
	Chat      *ChatLink `json:"chat,omitempty"`       // the chat Bothy made the change in, when the caller can open it
}

type SkillDetail struct {
	Meta  *core.SkillMeta `json:"meta"`
	Body  string          `json:"body"`
	Files []string        `json:"files"`
}

type SkillPatchReq struct {
	Enabled bool   `json:"enabled"`
	Hash    string `json:"hash,omitempty"` // SkillMeta.Hash of the reviewed files
}

type SchedulePreviewReq struct {
	Kind core.ScheduleKind `json:"kind"`
	Spec string            `json:"spec"`
	TZ   string            `json:"tz"`
}

type SchedulePreviewResp struct {
	Next  []time.Time `json:"next"`  // 5 occurrences
	Human string      `json:"human"` // "Weekdays at 08:00 (Europe/London)"
}

// ScheduleExample is a schedule people can start from (GET
// /api/schedules/examples); creating one goes through the usual form.
type ScheduleExample struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Kind   string    `json:"kind"` // cron
	Spec   string    `json:"spec"`
	Slot   core.Slot `json:"slot"`
	Prompt string    `json:"prompt"`
	Needs  string    `json:"needs"` // github | web_search
}

// CheckinSuggestions are lines for an empty check-in list; Empty says the
// viewer's list is blank.
type CheckinSuggestions struct {
	Empty bool     `json:"empty"`
	Lines []string `json:"lines"`
}

type ScheduleExamples struct {
	Examples []ScheduleExample  `json:"examples"`
	Checkin  CheckinSuggestions `json:"checkin"`
}

// RunScheduleResp answers POST /api/schedules/{id}/run; SessionID is the
// chat the run writes in.
type RunScheduleResp struct {
	RunID     core.RunID     `json:"run_id"`
	SessionID core.SessionID `json:"session_id,omitempty"`
}

type ScheduleUpsertReq struct {
	Schedule core.Schedule `json:"schedule"`
	Grants   []core.Grant  `json:"grants"` // pre-authorized scopes (scope=schedule); narrow only
}

// ---- providers / channels / mcp / sandbox ----

type ProviderTestReq struct {
	Kind         core.ProviderKind `json:"kind"`
	BaseURL      string            `json:"base_url,omitempty"`
	APIKey       string            `json:"api_key,omitempty"` // write-only, used for this probe only; the wizard stores it with PUT /api/vault/global/<name>
	SecretRef    string            `json:"secret_ref,omitempty"`
	Model        string            `json:"model"`
	AllowPrivate bool              `json:"allow_private"`
	API          string            `json:"api,omitempty"`     // openai, openai_compat: auto | responses | chat, as providers[].api
	Region       string            `json:"region,omitempty"`  // bedrock: AWS region; "" = the server's AWS config
	Profile      string            `json:"profile,omitempty"` // bedrock: shared-config profile; "" = AWS_PROFILE or the default chain
}

type ChannelTestResp struct {
	Identity string `json:"identity"`
}

// SearchTestReq is POST /api/search/test: one search with these settings
// before they are saved. Without api_key the saved key is used; a private
// SearXNG address needs search.searxng_allow_private, as for the tool.
type SearchTestReq struct {
	Provider   string `json:"provider"`              // searxng | brave | tavily
	SearxngURL string `json:"searxng_url,omitempty"` // searxng
	APIKey     string `json:"api_key,omitempty"`     // brave, tavily; write-only, used for this check only
}

// SearchTestResp says whether the search worked; a failure carries why
// (Code) and the raw message (Error).
type SearchTestResp struct {
	OK    bool             `json:"ok"`
	Code  core.FailureCode `json:"code,omitempty"`
	Error string           `json:"error,omitempty"`
}

type DiscordInvite struct {
	URL string `json:"url"`
}

type MCPApproveReq struct {
	Tools map[string]core.MCPToolApproval `json:"tools"`
}

// MCPResolveReq names an npm package to pin; Version "" means the latest.
type MCPResolveReq struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// MCPResolveResp is the exact version and the registry's integrity hash to
// pin, so the admin never types a hash, and the command that runs the
// package's own program from /opt/mcp (none when it has no "bin").
type MCPResolveResp struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Integrity string   `json:"integrity"`
	Command   []string `json:"command,omitempty"`
}

type SandboxInfo struct {
	Status  core.SandboxStatus `json:"status"`
	Compose string             `json:"compose_hint,omitempty"` // shown when unavailable
	QuotaMB int                `json:"quota_mb"`
	UsedMB  int                `json:"used_mb"`
}

// ---- config / vault / usage / audit / admin ----

type ConfigView struct {
	config.Effective
	Schema json.RawMessage `json:"schema,omitempty"`
}

type ConfigPatchReq struct {
	Set map[string]json.RawMessage `json:"set"` // dotted path -> JSON value
}

type ConfigPatchResp struct {
	RestartRequired []string `json:"restart_required"`
}

type VaultPutReq struct {
	Value string `json:"value"` // write-only
}

type VaultImportReq struct {
	EnvVar string `json:"env_var"` // one of the well-known names in SPEC §7.2; anything else => 400
	Name   string `json:"name"`    // vault name in the global scope, e.g. "anthropic_api_key"
}

type UnsealReq struct {
	Passphrase string `json:"passphrase"`
}

type UsageResp struct {
	Rows   []UsageRowView `json:"rows"`
	Totals core.UsageRow  `json:"totals"`
}

type UsageToday struct {
	Spent       core.Micros `json:"spent_micros"`
	Limit       core.Micros `json:"limit_micros"`
	GlobalSpent core.Micros `json:"global_spent_micros"` // admins only
	GlobalLimit core.Micros `json:"global_limit_micros"`
	// ChatSpent and ChatLimit (admins, when chat apps have a share of the
	// daily limit): what people and channels in chat apps spent today
	// together, and their share.
	ChatSpent core.Micros `json:"chat_spent_micros,omitempty"`
	ChatLimit core.Micros `json:"chat_limit_micros,omitempty"`
	Timezone  string      `json:"timezone"` // the configured zone, whose midnight resets the daily limits
}

type AuditVerify struct {
	OK    bool  `json:"ok"`
	BadID int64 `json:"bad_id,omitempty"`
}

type PauseReq struct {
	Paused bool `json:"paused"`
}

type SecurityAuditReq struct {
	Fix bool `json:"fix"`
}

type DoctorReport struct {
	Findings []core.Finding `json:"findings"`
	Fixed    []string       `json:"fixed,omitempty"`
}
