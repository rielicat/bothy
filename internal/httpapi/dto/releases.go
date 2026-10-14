package dto

import (
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// ---- multi-approver approvals and releases (v0.2 F5) ----

// ApprovalVotes is GET /api/approvals/{id}/votes: the vote state of a
// multi-approver approval as the caller sees it ("1 of 2 approvals").
type ApprovalVotes struct {
	Approval      core.ApprovalID     `json:"approval_id"`
	Status        core.ApprovalStatus `json:"status"`
	Required      int                 `json:"required"`  // distinct approvals needed
	Approvals     int                 `json:"approvals"` // allow votes so far
	Votes         []ApprovalVoteView  `json:"votes"`     // oldest first
	Approvers     string              `json:"approvers"` // who may approve, e.g. "Alice, Bob or any admin"
	Requester     core.UserID         `json:"requester"`
	RequesterName string              `json:"requester_name"`
	// CanVote: the caller may still allow or deny (eligible approver, not the
	// requester, not voted yet, approval open). CanWithdraw: the caller asked
	// and may still deny.
	CanVote     bool        `json:"can_vote"`
	CanWithdraw bool        `json:"can_withdraw"`
	YourVote    core.Choice `json:"your_vote,omitempty"`
}

type ApprovalVoteView struct {
	User   core.UserID `json:"user_id"`
	Name   string      `json:"name"`
	Choice core.Choice `json:"choice"` // allow_once | deny
	Via    string      `json:"via"`    // web | telegram | discord | slack
	At     time.Time   `json:"at"`
}

// Releases is GET /api/releases: the configured environments and the newest
// deploys the caller may see (their own; everyone's for admins).
type Releases struct {
	Environments []ReleaseEnvironment `json:"environments"`
	Deploys      []*core.Deploy       `json:"deploys"`
}

type ReleaseEnvironment struct {
	Name       string            `json:"name"`
	Repo       string            `json:"repo"`
	Workflow   string            `json:"workflow"`
	RefPattern string            `json:"ref_pattern"`
	Inputs     map[string]string `json:"inputs"`
	Required   int               `json:"required"` // approvers besides the requester
	Approvers  []ReleaseApprover `json:"approvers"`
}

type ReleaseApprover struct {
	Kind string `json:"kind"` // user | role
	ID   string `json:"id"`   // user id or role name
	Name string `json:"name"` // display name, or the role
}
