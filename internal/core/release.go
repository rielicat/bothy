package core

import (
	"context"
	"time"
)

// PrefixDeploy is the id prefix of deploy records (dep_<26>).
const PrefixDeploy = "dep"

// WorkflowDispatcher starts GitHub Actions workflow runs and reads their
// status for release_deploy and release_status. It runs in the gateway with
// the GitHub token from the vault; the token never enters a box.
type WorkflowDispatcher interface {
	// Dispatch fires workflow_dispatch and returns the run it started. An
	// error means GitHub did not accept the dispatch; a run with ID 0 means it
	// did but the run could not be identified.
	Dispatch(ctx context.Context, d WorkflowDispatch) (*WorkflowRun, error)
	// RunStatus returns ErrNotFound for a run GitHub does not know.
	RunStatus(ctx context.Context, repo string, runID int64) (*WorkflowRun, error)
}

type WorkflowDispatch struct {
	Repo     string            // owner/name
	Workflow string            // workflow file name (deploy.yml) or numeric id
	Ref      string            // branch or tag
	Inputs   map[string]string // workflow_dispatch inputs
}

// WorkflowRun holds only structured fields of a GitHub Actions run: names and
// titles are authored by whoever can push to the repository, so they are left
// out to keep the result trusted.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Repo       string    `json:"repo"`
	Number     int       `json:"number,omitempty"`
	Status     string    `json:"status"`               // queued | in_progress | completed | waiting | requested | pending
	Conclusion string    `json:"conclusion,omitempty"` // success | failure | cancelled | timed_out | action_required | neutral | skipped | stale | startup_failure
	HeadSHA    string    `json:"head_sha,omitempty"`
	URL        string    `json:"url,omitempty"` // web page of the run
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Done reports whether the run has finished.
func (r *WorkflowRun) Done() bool { return r != nil && r.Status == "completed" }

type DeployStatus string

const (
	DeployDispatched DeployStatus = "dispatched" // GitHub accepted workflow_dispatch; the run is not identified yet
	DeployRunning    DeployStatus = "running"
	DeploySucceeded  DeployStatus = "succeeded"
	DeployFailed     DeployStatus = "failed"  // the run finished with a conclusion other than success
	DeployUnknown    DeployStatus = "unknown" // no longer followed: releases.follow_for elapsed or the run was not found
)

// Terminal reports whether Bothy has stopped following the deploy.
func (s DeployStatus) Terminal() bool {
	return s == DeploySucceeded || s == DeployFailed || s == DeployUnknown
}

// Deploy is one approved release_deploy and what became of its workflow run.
type Deploy struct {
	ID          string       `json:"id"` // dep_<26>
	Environment string       `json:"environment"`
	Repo        string       `json:"repo"`
	Workflow    string       `json:"workflow"`
	Ref         string       `json:"ref"`
	Principal   PrincipalID  `json:"principal_id"`
	Session     SessionID    `json:"session_id"`
	Run         RunID        `json:"run_id,omitempty"` // the agent run that dispatched it
	Approval    ApprovalID   `json:"approval_id,omitempty"`
	RequestedBy UserID       `json:"requested_by"`
	ApprovedBy  []UserID     `json:"approved_by"`
	Chat        *ChatRef     `json:"chat,omitempty"` // originating chat; the outcome is posted there
	Status      DeployStatus `json:"status"`
	WorkflowRun int64        `json:"workflow_run_id,omitempty"`
	RunURL      string       `json:"run_url,omitempty"`
	Conclusion  string       `json:"conclusion,omitempty"`
	Error       string       `json:"error,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	FinishedAt  time.Time    `json:"finished_at"`
	// Via is who dispatched the deploy, so the follower reads its run
	// through the same credentials whatever the allowlists say later. A
	// deploy recorded before Via was kept has "" and follows today's choice.
	Via string `json:"via,omitempty"` // DeployViaConnection | DeployViaToken
}

// Deploy.Via values.
const (
	DeployViaConnection = "connection" // the GitHub connection that allowlists the repository
	DeployViaToken      = "token"      // releases.github_token
)

type DeployFilter struct {
	Environment string // "" = all
	Open        bool   // only deploys still followed (dispatched or running)
	Page        Page   // Before = created_at unix ms
}

// ReleaseRepo stores deploy records. Get and List return rows of the
// Viewer's principals; viewers with Admin also see everyone's (deploys are
// operational records, never message content).
type ReleaseRepo interface {
	Create(ctx context.Context, d *Deploy) error // assigns ID when empty
	Update(ctx context.Context, d *Deploy) error // ErrNotFound for a missing row
	Get(ctx context.Context, v Viewer, id string) (*Deploy, error)
	List(ctx context.Context, v Viewer, f DeployFilter) ([]*Deploy, error) // newest first
}

// ReleaseStore is implemented by the stores that keep deploy records (the
// SQLite store and memstore). It stays outside Store so other Store
// implementations need not change.
type ReleaseStore interface {
	Releases() ReleaseRepo
}
