package dto

import (
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// ---- approvals inbox (v0.2 UI, docs/design/ui.md §8.8) ----

// InboxType is the kind of an approvals-inbox item.
type InboxType string

const (
	InboxApproval       InboxType = "approval"        // an action card (Approval)
	InboxNoteSuggestion InboxType = "note_suggestion" // a project notes edit waiting for approval (Approval, card kind review)
	InboxMCPReview      InboxType = "mcp_review"      // admins: tools of an MCP server waiting for review (MCP)
	InboxSkillReview    InboxType = "skill_review"    // admins: a new or changed skill (Skill)
	InboxUnknownSender  InboxType = "unknown_sender"  // admins: someone new wrote to Bothy on a chat app (Sender)
	InboxGroupChat      InboxType = "group_chat"      // Bothy was added to a group chat (Group)
)

// Inbox is GET /api/inbox: what is waiting for a decision, the caller's
// items first ("For you"), then those waiting for others, oldest first.
// Each item is decided through the endpoint its type already has:
// POST /api/approvals/{id}; POST /api/mcp/{server}/approve; PATCH
// /api/skills/{name}; PATCH /api/identities/{channel}/{external}
// (guest, linked or blocked); PATCH /api/spaces/{id} (enabled true turns a
// group chat on, any other change records it as seen).
type Inbox struct {
	Items []InboxItem `json:"items"`
}

type InboxItem struct {
	// ID is the approval id, or "mcp:<server>", "skill:<name>",
	// "sender:<channel>:<external_id>", "group:<space principal id>".
	ID   string    `json:"id"`
	Type InboxType `json:"type"`
	// Question is the item's title in plain copy, e.g. "Review changes to
	// Linear tools"; Summary the facts beside it ("2 tools changed",
	// "Jordan K (@jordan_k)", "Added by Sam Patel").
	Question  string    `json:"question"`
	Summary   string    `json:"summary,omitempty"`
	ForYou    bool      `json:"for_you"` // the caller can decide it now
	CreatedAt time.Time `json:"created_at"`

	Approval *core.Approval        `json:"approval,omitempty"`
	MCP      *InboxMCP             `json:"mcp,omitempty"`
	Skill    *core.SkillMeta       `json:"skill,omitempty"`
	Sender   *core.ChannelIdentity `json:"sender,omitempty"` // first_text holds the start of their message
	Group    *InboxGroup           `json:"group,omitempty"`
}

type InboxMCP struct {
	Server string         `json:"server"`
	Tools  []*core.MCPPin `json:"tools"` // each changed tool's pending definition and, when it had one, its approved one
}

type InboxGroup struct {
	Space   core.Space `json:"space"`
	AddedBy string     `json:"added_by"` // display name of whoever added Bothy (else the owner)
}
