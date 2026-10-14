package dto

import (
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// ---- names, activity and search (v0.2 UI, docs/design/ui.md §17) ----

// SessionCreated answers POST /api/sessions: the session, plus the submitted
// run when the request carried the first message.
type SessionCreated struct {
	core.Session
	Run *core.SubmitResult `json:"run,omitempty"`
}

// RunView is a run with the names shown instead of its ids.
type RunView struct {
	core.Run
	RequestedByName string `json:"requested_by_name"` // who asked; "" for schedules and check-ins
	ChatTitle       string `json:"chat_title"`        // as the chat list shows it
	ScheduleName    string `json:"schedule_name"`     // "" unless a schedule started it; the heartbeat is "Check-in"
}

// UsageRowView is a usage bucket with its name: the person (group_by user),
// schedule (schedule) or chat (session). Day and model buckets, and keys
// that no longer resolve, have none.
type UsageRowView struct {
	core.UsageRow
	Name string `json:"name"`
}

// AuditRow is an audit entry as the log lists it.
type AuditRow struct {
	core.AuditEntry
	ActorName string `json:"actor_name"` // "Maya Chen", "Bothy" (agent and system)
	Summary   string `json:"summary"`    // one plain line: "Approved deleting notes/staging.md"
}

// EgressOutcome is a website access row's decision and connection result in
// one word.
type EgressOutcome string

const (
	EgressReached            EgressOutcome = "reached"
	EgressAllowedUnreachable EgressOutcome = "allowed_unreachable" // allowed, but the site did not answer
	EgressBlocked            EgressOutcome = "blocked"
)

// EgressRow is a website access row with its merged outcome.
type EgressRow struct {
	core.EgressRecord
	Outcome EgressOutcome `json:"outcome"`
}

// RefineEstimate is GET /api/sessions/{id}/refine-estimate: what refining the
// next reply of the chat would cost, for the composer's cost line.
type RefineEstimate struct {
	// USD is the estimated cost of one refined reply, from the chat's recent
	// answer steps; absent when the chat has no priced answer yet.
	USD        *float64 `json:"usd,omitempty"`
	Multiplier float64  `json:"multiplier"` // a refined reply costs about this many normal ones
	Drafts     int      `json:"drafts"`
}

// TextRange marks a match in a snippet: [Start, End) in UTF-16 code units,
// the indices of a JavaScript string.
type TextRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SearchResp is GET /api/search: plain-text snippets with their matches,
// grouped by where they were found.
type SearchResp struct {
	Chats []ChatSearchHit `json:"chats"`
	Notes []NoteSearchHit `json:"notes"`
	Code  []CodeSearchHit `json:"code"`
}

// ChatSearchHit is the best-matching message of one chat.
type ChatSearchHit struct {
	Session core.SessionID `json:"session_id"`
	Title   string         `json:"title"` // as the chat list shows it
	Seq     int64          `json:"seq"`
	At      time.Time      `json:"at"`
	Snippet string         `json:"snippet"`
	Ranges  []TextRange    `json:"ranges"`
	Trusted bool           `json:"trusted"` // written by the person or Bothy, not by others or tools
}

// NoteSearchHit is a matching section of a repository's project notes.
type NoteSearchHit struct {
	RepoID  int64       `json:"repo_id"`
	Repo    string      `json:"repo"`  // owner/name
	Title   string      `json:"title"` // the section's heading; "" before the first heading
	Snippet string      `json:"snippet"`
	Ranges  []TextRange `json:"ranges"`
}

// CodeSearchHit is a matching chunk of an indexed repository; Snippet is its
// first matching line.
type CodeSearchHit struct {
	RepoID    int64       `json:"repo_id"`
	Repo      string      `json:"repo"`
	Path      string      `json:"path"`
	StartLine int         `json:"start_line"`
	EndLine   int         `json:"end_line"`
	Citation  string      `json:"citation"` // owner/repo@sha:path#La-Lb
	Snippet   string      `json:"snippet"`
	Ranges    []TextRange `json:"ranges"`
}
