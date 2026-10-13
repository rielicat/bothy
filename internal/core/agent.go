package core

import (
	"context"
	"time"
)

// RunSink receives a run's events in order (channel streamer, OpenAI SSE
// writer). OnEvent must not block for long; the runner drops nothing but the
// sink must buffer.
type RunSink interface{ OnEvent(e Event) }

type RunRequest struct {
	Session    SessionID
	Principal  PrincipalID
	User       UserID // the acting/billing user
	Trigger    Trigger
	Schedule   ScheduleID
	Slot       Slot   // "" = the session's ModelSlot (schedules and /v1 bothy-fast set it)
	Input      []Part // attachments arrive as image/file parts whose Blob is already in the workspace
	Provenance Provenance
	Sender     string
	Chat       *ChatRef // originating chat (message tool target, card routing)
	Sink       RunSink  // may be nil (web runs use the bus)
	Limits     *RunLimits
	// Instructions are /v1 client "system" messages, rendered as untrusted
	// "client instructions" inside the turn-context; never part of S1/S2.
	Instructions string
	// API runs: OnApproval "deny" (default) or "wait" (up to ApprovalWait).
	OnApproval   string
	ApprovalWait time.Duration
	// Ephemeral carries client history for stateless /v1 requests. It is
	// appended to the hidden api session before Input.
	Ephemeral   []Message
	MaxOutput   int      // /v1 max_tokens cap per step; 0 = model default
	Temperature *float64 // /v1 client temperature; nil = model default
	Stop        []string // /v1 stop sequences
}

type SubmitResult struct {
	Run     RunID `json:"run_id,omitempty"`
	Queued  bool  `json:"queued"`  // waiting for the session's active run
	Steered bool  `json:"steered"` // injected into the active run at the next step
}

type RunInfo struct {
	Run       RunID       `json:"run_id"`
	Session   SessionID   `json:"session_id"`
	Principal PrincipalID `json:"principal_id"`
	Status    RunStatus   `json:"status"`
	Started   time.Time   `json:"started_at"`
	Queued    int         `json:"queued"`
}

type Runner interface {
	// Submit returns ErrBusy when the session mailbox is full and ErrPaused when
	// the kill switch is engaged.
	Submit(ctx context.Context, r RunRequest) (SubmitResult, error)
	// Stop cancels the active run, SIGKILLs the session box, cancels its pending
	// approvals and clears its queue. It never waits on the run goroutine.
	Stop(ctx context.Context, v Viewer, s SessionID, by UserID) (bool, error)
	StopAll(ctx context.Context, by UserID) (int, error)
	SetPaused(ctx context.Context, paused bool, by UserID) error // persisted in meta.paused
	Paused() bool
	// Resume continues a parked run after its approval resolved: allowed =>
	// execute the stored Action exactly once and continue the loop; denied =>
	// record the denial and continue the loop; expired/cancelled => run stopped
	// with a notice. It is a no-op when the approval's run is not parked.
	Resume(ctx context.Context, a *Approval) error
	Active() []RunInfo
	ResolveSession(ctx context.Context, p PrincipalID, chat ChatRef) (*Session, error) // open session by ChatRef.Key()
	NewSession(ctx context.Context, p PrincipalID, origin string, chat *ChatRef) (*Session, error)
	// Fork creates the clean fork of §5.3: no history, taint 0, a system note
	// linking the old session. For a channel session it archives the old one and
	// moves the channel key to the fork, so the chat continues in the fork.
	Fork(ctx context.Context, v Viewer, s SessionID, by UserID) (*Session, error)
	Compact(ctx context.Context, v Viewer, s SessionID) error
	ContextPreview(ctx context.Context, v Viewer, s SessionID) (*Breakdown, error)
	Continue(ctx context.Context, v Viewer, run RunID, by UserID) (SubmitResult, error) // "+10 steps" after a steps budget stop
	Constitution() (version, text string)                                               // embedded CONSTITUTION.md (full variant) for GET /api/constitution
}
