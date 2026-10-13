// Package dto holds the JSON shapes of the REST API that are not core types.
// Frozen in the skeleton; web/src/lib/api/types.gen.ts is generated from it and
// from internal/core by tygo (make gen).
package dto

import (
	"encoding/json"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// Error is the envelope for every non-2xx /api response.
type Error struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"` // bad_request | unauthorized | forbidden | not_found | conflict | busy | paused | budget_exceeded | rate_limited | sealed | no_sandbox | unavailable | internal
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

type List[T any] struct {
	Items []T   `json:"items"`
	Next  int64 `json:"next,omitempty"` // pass as ?before= for the next page; 0 = end
}

// ---- setup / auth ----

type SetupState struct {
	NeedsSetup bool   `json:"needs_setup"`
	Claimed    bool   `json:"claimed"` // a setup grant cookie is present and valid
	OwnerSet   bool   `json:"owner_set"`
	Sandbox    bool   `json:"sandbox_available"`
	Version    string `json:"-"` // never disclosed pre-auth
}

type SetupClaimReq struct {
	Token string `json:"token"`
}

type SetupClaimResp struct {
	CSRF string `json:"csrf"`
}

type SetupOwnerReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Timezone string `json:"timezone"`
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
	Sandbox    bool              `json:"sandbox_available"`
	Banners    []core.BannerData `json:"banners"`
}

type PasswordReq struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type TokenCreateResp struct {
	Token string            `json:"token"` // plaintext, shown once
	Meta  *core.TokenRecord `json:"meta"`
}

type UserCreateReq struct {
	Username    string    `json:"username"`
	Password    string    `json:"password"`
	DisplayName string    `json:"display_name"`
	Role        core.Role `json:"role"`
}

type UserPatchReq struct {
	DisplayName *string    `json:"display_name,omitempty"`
	Role        *core.Role `json:"role,omitempty"`
	Disabled    *bool      `json:"disabled,omitempty"`
}

type PairingApproveReq struct {
	Code   string      `json:"code"`
	As     string      `json:"as"` // guest | link
	UserID core.UserID `json:"user_id,omitempty"`
}

type IdentityPatchReq struct {
	Status core.IdentityStatus `json:"status"` // blocked | guest | linked
	UserID core.UserID         `json:"user_id,omitempty"`
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
}

type SessionPatchReq struct {
	Title         *string `json:"title,omitempty"`
	ModelOverride *string `json:"model_override,omitempty"` // "" clears
}

type SessionDetail struct {
	Session core.Session     `json:"session"`
	Pins    []*core.Pin      `json:"pins"`
	Active  *core.RunInfo    `json:"active,omitempty"`
	Pending []*core.Approval `json:"pending_approvals"`
}

type MessagePage struct {
	Messages []*core.Message `json:"messages"` // oldest first within the page
	HasMore  bool            `json:"has_more"`
}

type SendReq struct {
	Text    string   `json:"text"`
	Uploads []string `json:"uploads,omitempty"` // upl_<26> ids from POST /api/uploads
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
	Run       *core.Run              `json:"run"`
	LLMCalls  []core.CallInfo        `json:"llm_calls"`
	ToolCalls []*core.ToolCallRecord `json:"tool_calls"`
	Approvals []*core.Approval       `json:"approvals"`
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
	File     core.MemoryFile `json:"file"`
	Content  string          `json:"content"`
	ETag     string          `json:"etag"`
	CapBytes int             `json:"cap_bytes"`
}

type MemoryPutReq struct {
	Content string `json:"content"` // If-Match header carries the etag
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
}

type ChannelTestResp struct {
	Identity string `json:"identity"`
}

type DiscordInvite struct {
	URL string `json:"url"`
}

type MCPApproveReq struct {
	Tools map[string]core.MCPToolApproval `json:"tools"`
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
	Rows   []core.UsageRow `json:"rows"`
	Totals core.UsageRow   `json:"totals"`
}

type UsageToday struct {
	Spent       core.Micros `json:"spent_micros"`
	Limit       core.Micros `json:"limit_micros"`
	GlobalSpent core.Micros `json:"global_spent_micros"` // admins only
	GlobalLimit core.Micros `json:"global_limit_micros"`
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
