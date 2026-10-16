package core

import (
	"context"
	"time"
)

type Verdict string

const (
	VerdictAuto Verdict = "auto"
	VerdictAsk  Verdict = "ask"
	VerdictDeny Verdict = "deny"
)

type TimeoutAction string

const (
	TimeoutDeny TimeoutAction = "deny"
	TimeoutPark TimeoutAction = "park"
)

type Choice string

const (
	AllowOnce    Choice = "allow_once"
	AllowSession Choice = "allow_session"
	// AllowAlways creates the same narrow grant as AllowSession but with
	// GrantPrincipal scope, so it persists across sessions until revoked.
	// Offered only where AllowSession is, and never for irreversible actions.
	AllowAlways Choice = "allow_always"
	Deny        Choice = "deny"
)

type PolicyInput struct {
	User       UserID // the principal's account; "" for team-owned principals
	Role       Role
	Principal  Principal
	Session    SessionID
	Schedule   ScheduleID
	Trigger    Trigger
	Action     *Action
	Spec       ToolSpec
	Taint      Taint
	Provenance Provenance // provenance of the message that started the run
	Chat       *ChatRef
}

// ApproverReq says who may resolve an approval.
//
// A multi-approver approval (Users or Roles set, User empty, AdminOnly false)
// is resolved by votes: it is allowed once Needed() distinct eligible users
// have voted allow_once, and denied by the first eligible deny. Requester's
// allow never counts; the requester may still deny (withdraw). Eligibility is
// evaluated when the vote is cast, so a disabled or demoted account loses it.
type ApproverReq struct {
	User      UserID `json:"user_id"`    // the principal's account; empty when AdminOnly, Team or multi-approver
	AdminOnly bool   `json:"admin_only"` // admin-class actions: any owner/admin
	// Team: a channel's or teammate's request. Any enabled owner or admin on
	// the web, or a teammate an admin marked "Can approve" pressing a recorded
	// chat card, may resolve it.
	Team      bool     `json:"team,omitempty"`
	Users     []UserID `json:"users,omitempty"`     // multi-approver: users named by id
	Roles     []Role   `json:"roles,omitempty"`     // multi-approver: any user holding at least one of these roles
	Required  int      `json:"required,omitempty"`  // multi-approver: distinct allow votes needed (0 means 1)
	Requester UserID   `json:"requester,omitempty"` // multi-approver: whose allow never counts; "" for a teammate's request
	// RequesterIdentity is "<channel>:<external id>" of the chat sender whose
	// message started a team-owned principal's run; set by Approvals.Request.
	// Only this sender may withdraw (deny) from chat without Can approve.
	RequesterIdentity string `json:"requester_identity,omitempty"`
}

// Multi reports whether the approval is resolved by votes.
func (r ApproverReq) Multi() bool {
	return !r.AdminOnly && !r.Team && (len(r.Users) > 0 || len(r.Roles) > 0)
}

// Decider is who resolved an approval: a web account or a chat teammate.
type Decider struct {
	User     UserID // web
	Identity string // chat: "<channel>:<external id>"
}

// Needed is the number of distinct allow votes a multi-approver approval needs.
func (r ApproverReq) Needed() int { return max(r.Required, 1) }

// Eligible reports whether a user with this role may vote allow on a
// multi-approver approval. Roles rank owner > admin > member: naming a role
// admits that role and every higher one. Account state (disabled) is the
// caller's to check.
func (r ApproverReq) Eligible(u UserID, role Role) bool {
	if !r.Multi() || u == "" || u == r.Requester {
		return false
	}
	for _, x := range r.Users {
		if x == u {
			return true
		}
	}
	for _, x := range r.Roles {
		if rank := roleRank(x); rank > 0 && roleRank(role) >= rank {
			return true
		}
	}
	return false
}

func roleRank(r Role) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleMember:
		return 1
	}
	return 0
}

// ApprovalVote is one approver's vote on a multi-approver approval. A user
// votes at most once per approval.
type ApprovalVote struct {
	Approval ApprovalID `json:"approval_id"`
	User     UserID     `json:"user_id"`
	Choice   Choice     `json:"choice"` // allow_once | deny
	Via      string     `json:"via"`    // web | telegram | discord | slack
	At       time.Time  `json:"at"`
	Comment  string     `json:"comment,omitempty"` // the voter's reason, e.g. for a deny
}

// AllowVotes counts the distinct allow votes that count towards Needed().
func AllowVotes(votes []ApprovalVote, r ApproverReq) int {
	seen := map[UserID]bool{}
	for _, v := range votes {
		if v.Choice == AllowOnce && v.User != r.Requester {
			seen[v.User] = true
		}
	}
	return len(seen)
}

type Decision struct {
	Verdict   Verdict       `json:"verdict"`
	Rule      string        `json:"rule"`   // "floor:financial" | "floor:trifecta" | "rule:<id>" | "grant:<id>" | "first_contact:domain" | "default:external_send"
	Reason    string        `json:"reason"` // safe to show the model and the user
	Floor     bool          `json:"floor"`
	Approver  ApproverReq   `json:"approver"`
	Timeout   time.Duration `json:"timeout"`
	OnTimeout TimeoutAction `json:"on_timeout"`
	Choices   []Choice      `json:"choices"` // AllowSession omitted when no narrow grant can be formed or when Exposed
	Warnings  []string      `json:"warnings,omitempty"`
}

type GrantScope string

const (
	GrantSession   GrantScope = "session"
	GrantSchedule  GrantScope = "schedule"
	GrantPrincipal GrantScope = "principal" // persistent; created by admins in the web UI, or narrow-only via an allow_always card choice
)

type GrantKind string

const (
	GrantTool      GrantKind = "tool"      // Tool + Pattern (JSON pointer -> glob); never a whole tool
	GrantDomain    GrantKind = "domain"    // Value "host:port"; "*.suffix:443" only for GrantPrincipal by admins
	GrantRecipient GrantKind = "recipient" // Value = ChatRef.Key() or MCP recipient field value
)

type Grant struct {
	ID               GrantID           `json:"id"`
	Principal        PrincipalID       `json:"principal_id"`
	Scope            GrantScope        `json:"scope"`
	ScopeID          string            `json:"scope_id"` // session or schedule id; "" for principal
	Kind             GrantKind         `json:"kind"`
	Tool             string            `json:"tool,omitempty"`
	Pattern          map[string]string `json:"pattern,omitempty"` // JSON pointer into Action.Payload -> glob
	Value            string            `json:"value,omitempty"`
	MaxSE            SideEffect        `json:"max_se"`
	AllowWhenTainted bool              `json:"allow_when_tainted"` // only valid with Kind recipient and an exact Value
	Uses             int               `json:"uses"`
	MaxUses          int               `json:"max_uses"` // 0 = unlimited
	CreatedBy        UserID            `json:"created_by"`
	// CreatedByIdentity: a chat approver created it from a card
	// ("<channel>:<external id>"); CreatedBy is then "".
	CreatedByIdentity string    `json:"created_by_identity,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"` // zero = never
}

type Policy interface {
	// Decide order: floors -> rules -> grants -> first contact -> class defaults.
	// No rule or grant overrides a floor (sole exception: AllowWhenTainted
	// recipient grant vs the trifecta floor).
	Decide(ctx context.Context, in PolicyInput) Decision
	// NarrowGrant builds the grant that "allow for session" creates for an Action,
	// or ok=false when no narrow grant exists (then AllowSession is not offered).
	NarrowGrant(a *Action, p PrincipalID, scope GrantScope, scopeID string, by Decider) (g Grant, ok bool)
	Grant(ctx context.Context, g Grant) (GrantID, error)
	Revoke(ctx context.Context, v Viewer, id GrantID, by UserID) error
	// NextTaint is pure. boxEgressed reports that the session box moved bytes
	// through the egress proxy during this call (output becomes untrusted).
	NextTaint(prev Taint, spec ToolSpec, a *Action, res *ToolResult, boxEgressed bool) Taint
}

// CardKind tells renderers which layout a card uses (v0.2 S2).
type CardKind string

const (
	CardAction      CardKind = "action"
	CardPullRequest CardKind = "pull_request" // repo_open_pr
	CardDeploy      CardKind = "deploy"       // release_deploy
	CardReview      CardKind = "review"       // a suggested text change (project notes)
)

// CardTone colours the primary button: destructive for irreversible and
// financial actions, except deploys, whose safeguard is their approvers.
type CardTone string

const (
	ToneDefault     CardTone = "default"
	ToneDestructive CardTone = "destructive"
)

type CardField struct {
	Label string `json:"label"` // plain, e.g. "File", "Command", "To"
	Value string `json:"value"` // verbatim; renderers escape and visualize bidi/zero-width chars
	Mono  bool   `json:"mono"`
}

// Card is rendered by the policy module from the Action only, never from model
// prose. Channel adapters format it; they never add content. Fields, Warnings
// and the plain-copy fields below (v0.2 S2) are the words every surface shows.
type Card struct {
	Title    string      `json:"title"` // the tool's title, e.g. "Delete file"
	Risk     SideEffect  `json:"risk"`
	Fields   []CardField `json:"fields"`
	Code     string      `json:"code,omitempty"` // exact argv / JSON args
	Diff     string      `json:"diff,omitempty"`
	Warnings []string    `json:"warnings,omitempty"`
	Context  string      `json:"context"` // "Telegram · DM · session “Q3 report”"
	Choices  []Choice    `json:"choices"`
	Expires  time.Time   `json:"expires"`
	WebURL   string      `json:"web_url"` // public_url + /approvals/<id>

	// Plain copy, set on every card rendered since v0.2; cards stored before
	// that lack it, hence omitempty.
	Question     string   `json:"question,omitempty"`      // "Delete notes/staging.md?"
	Consequence  string   `json:"consequence,omitempty"`   // "This can't be undone."
	PrimaryLabel string   `json:"primary_label,omitempty"` // allow_once's verb ("Delete file"), else "Allow for this chat"
	Ing          string   `json:"ing,omitempty"`           // "deleting notes/staging.md", as in "You approved …"
	Kind         CardKind `json:"kind,omitempty"`
	Tone         CardTone `json:"tone,omitempty"`
}

type ApprovalStatus string

const (
	ApprovalPending   ApprovalStatus = "pending"
	ApprovalAllowed   ApprovalStatus = "allowed"
	ApprovalDenied    ApprovalStatus = "denied"
	ApprovalExpired   ApprovalStatus = "expired"
	ApprovalParked    ApprovalStatus = "parked"
	ApprovalCancelled ApprovalStatus = "cancelled"
)

type Approval struct {
	ID         ApprovalID     `json:"id"`
	Run        RunID          `json:"run_id"`
	Session    SessionID      `json:"session_id"`
	Principal  PrincipalID    `json:"principal_id"`
	ToolCallID string         `json:"tool_call_id"`
	Action     *Action        `json:"action"`
	Decision   Decision       `json:"decision"`
	Card       Card           `json:"card"`
	Status     ApprovalStatus `json:"status"`
	Choice     Choice         `json:"choice,omitempty"`
	Approver   ApproverReq    `json:"approver"`
	Chat       *ChatRef       `json:"chat,omitempty"` // originating chat
	OnTimeout  TimeoutAction  `json:"on_timeout"`
	DecidedBy  UserID         `json:"decided_by,omitempty"`
	// DecidedByIdentity: a chat teammate decided ("<channel>:<external
	// id>"); DecidedBy is then "".
	DecidedByIdentity string    `json:"decided_by_identity,omitempty"`
	DecidedVia        string    `json:"decided_via,omitempty"` // web|telegram|discord|slack|timeout|stop (CancelRun)
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ParkUntil         time.Time `json:"park_until,omitempty"`
	DecidedAt         time.Time `json:"decided_at,omitempty"`
	// DecidedByName is DecidedBy's display name; the HTTP API fills it on reads.
	DecidedByName string `json:"decided_by_name,omitempty"`
	// Votes of a multi-approver approval, oldest first. Approvals.Get, Pending,
	// Recent and Resolve fill it; repositories leave it empty.
	Votes []ApprovalVote `json:"votes,omitempty"`
	// Comment is the deciding approver's reason (Resolution.Comment).
	Comment string `json:"comment,omitempty"`
	// Approvals (multi-approver only) and Requester carry display names for
	// cards (v0.2 S4); they are filled like Votes.
	Approvals *ApprovalTally  `json:"approvals,omitempty"`
	Requester *ApprovalPerson `json:"requester,omitempty"`
}

// ApprovalPerson names who asked on an approval card: an account, or a
// person or channel in a chat app.
type ApprovalPerson struct {
	User     UserID `json:"user"`
	Name     string `json:"name"`               // display name, else username (chat: display name, else handle)
	App      string `json:"app,omitempty"`      // the chat app of a teammate or channel requester
	Chat     string `json:"chat,omitempty"`     // a channel's request, whoever asked: the channel's title ("#eng")
	Identity string `json:"identity,omitempty"` // "<channel>:<external id>", so chat cards can mention the real profile
	Handle   string `json:"handle,omitempty"`   // the chat requester's @handle, when they have one
	// Kind is whose request it is: RequesterTeammate (a teammate's own
	// chat) or RequesterChannel; "" for an account's.
	Kind string `json:"kind,omitempty"`
	// Scheduled: nobody asked; the teammate's or channel's schedule
	// started the run, and Name is that teammate or channel.
	Scheduled bool `json:"scheduled,omitempty"`
}

// ApprovalPerson kinds.
const (
	RequesterTeammate = "teammate"
	RequesterChannel  = "channel"
)

// ApprovalTally is where a multi-approver approval stands.
type ApprovalTally struct {
	Required int              `json:"required"` // distinct approvals needed, the requester's never counting
	Votes    []ApprovalBallot `json:"votes"`    // oldest first
	// Eligible lists the enabled users other than the requester who may
	// approve, leaving out admins and owners when AnyAdmin already covers them.
	Eligible []ApprovalPerson `json:"eligible"`
	AnyAdmin bool             `json:"any_admin"` // every admin may approve through a role
}

// ApprovalBallot is one vote with the voter's name.
type ApprovalBallot struct {
	User    UserID    `json:"user"`
	Name    string    `json:"name"`
	Choice  Choice    `json:"choice"` // allow_once | deny
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
}

type ApprovalRequest struct {
	Run        RunID
	Session    SessionID
	Principal  PrincipalID
	ToolCallID string
	Action     *Action
	Spec       ToolSpec
	Decision   Decision // from Policy.Decide (Verdict Ask)
	Chat       *ChatRef
	Context    string // card context line, built by the caller from session/channel
	Sender     string // RunRequest.Sender of the run ("" for schedules)
}

type Resolution struct {
	ID         ApprovalID
	Choice     Choice
	By         UserID
	ByIdentity string // a chat press by a teammate ("<channel>:<external id>"); By is then ""
	ByRole     Role
	Via        string  // web | telegram | discord | slack
	ViaMsg     *MsgRef // required for chat channels: must be a recorded card message
	Comment    string
}

type Approvals interface {
	// Request persists, renders the Card and publishes approval.requested with
	// Principal = r.Principal; for AdminOnly approvals it also publishes a copy
	// with Principal "" (admin broadcast) so every admin's inbox sees it, and
	// for multi-approver approvals a copy to each eligible approver's own
	// principal. approval.resolved and approval.voted are published the same way.
	Request(ctx context.Context, r ApprovalRequest) (*Approval, error)
	// Await blocks until resolved, timed out, or ctx done. Timeout: deny =>
	// ErrApprovalExpired; park => ErrParked (status parked, ParkUntil set).
	Await(ctx context.Context, id ApprovalID) (Choice, error)
	// Resolve is a CAS from pending|parked. It checks approver authority, that a
	// chat press comes from a recorded card message, and that a truncated card is
	// only ever denied from chat. Publishes approval.resolved. On a
	// multi-approver approval it records r.By's vote (approval.voted) and
	// resolves only on a deny or the Needed()th distinct allow; the returned
	// approval may still be pending.
	Resolve(ctx context.Context, r Resolution) (*Approval, error)
	CancelRun(ctx context.Context, run RunID) error
	// RenderCard renders without persisting (policy preview, tests).
	RenderCard(r ApprovalRequest) Card
	Get(ctx context.Context, v Viewer, id ApprovalID) (*Approval, error)
	Pending(ctx context.Context, v Viewer) ([]*Approval, error)        // pending + parked
	Recent(ctx context.Context, v Viewer, p Page) ([]*Approval, error) // decided, newest first; Before = created_at ms
	// ButtonData returns "a:<id>:<o|s|a|d>:<mac8>" (<= 64 bytes, Telegram limit);
	// the mac is HMAC-SHA256(server key, id|choice) truncated to 8 base32 chars.
	ButtonData(id ApprovalID, c Choice) string
	ParseButtonData(data string) (ApprovalID, Choice, error)                            // verifies the mac
	AddMessageRef(ctx context.Context, id ApprovalID, ref MsgRef, truncated bool) error // a card (kind card)
	// AddMessage records a chat message of any kind: a link-only card, or
	// the requester's waiting line (kind notice, which only withdraws).
	AddMessage(ctx context.Context, id ApprovalID, m CardMessage) error
	MessageRefs(ctx context.Context, id ApprovalID) ([]CardMessage, error)
	// OnResolved registers a callback; the runner uses it to resume parked runs.
	OnResolved(fn func(a *Approval))
}

type CardMessage struct {
	Ref       MsgRef `json:"ref"`
	Truncated bool   `json:"truncated"`
	Kind      string `json:"kind"` // card | link (a link-only card) | notice (the requester's waiting line)
}

// CardMessage kinds; Messages lists cards, then links, then notices.
const (
	CardMsgCard   = "card"
	CardMsgLink   = "link"
	CardMsgNotice = "notice"
)
