// Package core holds every cross-module type and interface. It imports only the
// standard library. It is frozen after the skeleton commit: changes go through
// the lead as an interface change request (ICR).
package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type (
	UserID      string // usr_<26>
	PrincipalID string // user principals reuse the UserID string; spaces are spc_<26>
	SessionID   string // ses_<26>
	RunID       string // run_<26>
	ApprovalID  string // apr_<26>
	ScheduleID  string // sch_<26>
	WorkspaceID string // ws_<26>
	GrantID     string // grt_<26>
)

// ID prefixes. Every generated id is "<prefix>_<26 lowercase Crockford base32>".
const (
	PrefixUser      = "usr"
	PrefixSpace     = "spc"
	PrefixSession   = "ses"
	PrefixRun       = "run"
	PrefixApproval  = "apr"
	PrefixSchedule  = "sch"
	PrefixWorkspace = "ws"
	PrefixGrant     = "grt"
	PrefixToolCall  = "tcl"
	PrefixLLMCall   = "llm"
	PrefixToken     = "tok"
	PrefixBox       = "box"
	PrefixInstall   = "in"
	PrefixUpload    = "upl"
)

const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewID returns prefix + "_" + 26 chars: 48-bit unix ms then 80 random bits.
// Ids sort by creation time.
func NewID(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	return prefix + "_" + encode128(b)
}

// DerivedID returns a stable id of the given prefix for a purpose string. Used
// for sandbox box keys so the same session or MCP instance maps to the same box.
func DerivedID(prefix, purpose string) string {
	sum := sha256.Sum256([]byte(purpose))
	var b [16]byte
	copy(b[:], sum[:16])
	return prefix + "_" + encode128(b)
}

func encode128(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[0:8])
	lo := binary.BigEndian.Uint64(b[8:16])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

// ValidID reports whether s is "<prefix>_" followed by 26 Crockford chars.
func ValidID(prefix, s string) bool {
	rest, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(rest) != 26 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if !strings.ContainsRune(crockford, rune(rest[i])) {
			return false
		}
	}
	return true
}

// Micros is millionths of a US dollar. All money math is integer.
type Micros int64

func (m Micros) USD() float64 { return float64(m) / 1e6 }

func (m Micros) String() string { return fmt.Sprintf("$%.4f", m.USD()) }

// MicrosFromUSD converts a config value such as 5.00 into Micros, rounding half up.
func MicrosFromUSD(usd float64) Micros { return Micros(math.Round(usd * 1e6)) }

// Clock is injected wherever "now" matters. Timer-heavy tests use testing/synctest.
type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (r Role) IsAdmin() bool { return r == RoleOwner || r == RoleAdmin }

type PrincipalKind string

const (
	PrincipalUser  PrincipalKind = "user"  // a human account's own data
	PrincipalSpace PrincipalKind = "space" // a group chat or an allowlisted guest DM; restricted
)

// Principal owns sessions, workspace, memory, grants and schedules. A space has
// no vault scope and no memory files, and gets read-only tools unless its
// ToolProfile is "standard".
type Principal struct {
	ID          PrincipalID   `json:"id"`
	Kind        PrincipalKind `json:"kind"`
	User        UserID        `json:"user_id"` // user principal: that user; space: the owning user
	DisplayName string        `json:"display_name"`
	Workspace   WorkspaceID   `json:"workspace_id"`
	ToolProfile string        `json:"tool_profile"` // "standard" | "restricted" (spaces default restricted)
	CreatedAt   time.Time     `json:"created_at"`
}

func (p Principal) Restricted() bool { return p.Kind == PrincipalSpace && p.ToolProfile != "standard" }

// UserPrincipal returns the principal id of a user's own data.
func UserPrincipal(u UserID) PrincipalID { return PrincipalID(u) }

// Viewer scopes repository reads. Repositories return ErrNotFound for any row
// whose principal is not in Principals, unless System is set. HTTP handlers
// build Viewers with Auth.ViewerFor and never set System.
//
// Admin is set by Auth.ViewerFor for owner/admin users. Repositories ignore it
// except in exactly two places: approvals whose approver is AdminOnly
// (ApprovalRepo.Get/Pending/Recent also return those), and egress rows of the
// system dialer (principal_id ""). It never grants access to message content.
type Viewer struct {
	User       UserID        `json:"user_id"`
	Principals []PrincipalID `json:"principals"`
	Admin      bool          `json:"admin"`
	System     bool          `json:"-"`
}

// SystemViewer is for internal callers only (scheduler, runner, egress, doctor).
func SystemViewer() Viewer { return Viewer{System: true} }

func (v Viewer) Can(p PrincipalID) bool {
	if v.System {
		return true
	}
	for _, x := range v.Principals {
		if x == p {
			return true
		}
	}
	return false
}

type Trigger string

const (
	TrigMessage   Trigger = "message" // web UI or channel message
	TrigSchedule  Trigger = "schedule"
	TrigHeartbeat Trigger = "heartbeat"
	TrigAPI       Trigger = "api" // OpenAI-compatible endpoint
	// TrigResume appears only in RunStartedData when a parked run continues.
	// Run.Trigger, PolicyInput.Trigger and ToolEnv.Trigger always keep the run's
	// original trigger, so floors such as F3 still apply after a resume.
	TrigResume Trigger = "resume"
)

func (t Trigger) Interactive() bool { return t == TrigMessage }

func (t Trigger) Scheduled() bool { return t == TrigSchedule || t == TrigHeartbeat }

var (
	ErrNotFound        = errors.New("not found")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid")
	ErrBusy            = errors.New("session queue full")
	ErrNoSandbox       = errors.New("sandbox unavailable")
	ErrStopped         = errors.New("run stopped")
	ErrPaused          = errors.New("kill switch engaged")
	ErrApprovalExpired = errors.New("approval expired")
	ErrParked          = errors.New("run parked awaiting approval")
	ErrSealed          = errors.New("vault sealed")
	ErrNotImplemented  = errors.New("not implemented")
)
