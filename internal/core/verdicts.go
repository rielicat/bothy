package core

import "time"

// How long membership verdicts count (SPEC §5.1 step 3).
const (
	MemberTTL     = time.Hour        // a verdict older than this is checked again before use
	MemberStale   = 4 * time.Hour    // a member verdict this young still counts for chats and schedules when the platform doesn't answer
	ApproverFresh = 2 * time.Hour    // full approver cards need a verdict this young; older gets a link-only card
	ApproverStale = 15 * time.Minute // a press falls back to a verdict this young when its recheck fails
)
