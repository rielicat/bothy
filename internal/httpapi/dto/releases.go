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
	Approvers     string              `json:"approvers"` // who can still approve, the caller as "you", e.g. "you, Bob or any admin"
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
	User    core.UserID `json:"user_id"`
	Name    string      `json:"name"`
	Choice  core.Choice `json:"choice"` // allow_once | deny
	Via     string      `json:"via"`    // web | telegram | discord | slack
	At      time.Time   `json:"at"`
	Comment string      `json:"comment,omitempty"`
}

// Releases is GET /api/releases: the configured environments and the newest
// deploys the caller may see (their own; everyone's for admins).
type Releases struct {
	Environments []ReleaseEnvironment `json:"environments"`
	Deploys      []DeployView         `json:"deploys"`
}

// DeployView is a deploy record with names instead of user ids. Subject
// (core.Deploy) is the commit's subject line as its author wrote it: show
// it as quoted text, never as a heading, a question or a link.
type DeployView struct {
	core.Deploy
	RequestedByName string   `json:"requested_by_name"`
	ApprovedByNames []string `json:"approved_by_names"`
	ShortSHA        string   `json:"short_sha"` // the first 7 characters of SHA
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
	Name string `json:"name"` // display name ("" for a user that no longer exists), or the role
}
