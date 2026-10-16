package core

import (
	"context"
	"time"
)

// Actor is the authenticated caller of an HTTP request.
type Actor struct {
	User    UserID   `json:"user_id"`
	Role    Role     `json:"role"`
	Via     string   `json:"via"` // cookie | token
	TokenID string   `json:"token_id,omitempty"`
	Scopes  []string `json:"scopes,omitempty"` // token scopes: chat | llm | read | admin; cookies have all of the role
	CSRF    string   `json:"-"`
	Viewer  Viewer   `json:"-"`
	// OnApproval and RatePerMin come from the token (cookie actors: "deny", 0).
	OnApproval string `json:"-"`
	RatePerMin int    `json:"-"`
	// MustChangePassword (cookie actors): until the one-time password is
	// replaced, only signing out and choosing a new password are allowed.
	MustChangePassword bool `json:"-"`
}

func (a Actor) HasScope(s string) bool {
	if a.Via == "cookie" {
		return s != "admin" || a.Role.IsAdmin()
	}
	for _, x := range a.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

type TokenSpec struct {
	Name       string        `json:"name"`
	Scopes     []string      `json:"scopes"`
	TTL        time.Duration `json:"ttl"`         // default 90 days; 0 rejected
	OnApproval string        `json:"on_approval"` // deny (default) | wait
	RatePerMin int           `json:"rate_per_min"`
}

// IdentityResolution is what Bothy knows about a chat sender.
type IdentityResolution struct {
	Status     IdentityStatus // "" unknown | pending | teammate | blocked
	Principal  PrincipalID    // teammate: their person space
	Via        AdmittedVia
	CanApprove bool
	Member     MemberStatus
	MemberAt   time.Time
	SpaceOn    bool // teammate: their person space is enabled
	Lost       bool // set by Admit: this verdict made an admitted person an outsider
}

// Admitted: a teammate whose space is on and whose platform said member, or
// whom an admin let in where the platform can't tell.
func (r IdentityResolution) Admitted() bool {
	if r.Status != IdentityTeammate || !r.SpaceOn {
		return false
	}
	switch r.Member {
	case MemberYes:
		return true
	case MemberUnknown:
		return r.Via == ViaAdmin
	}
	return false
}

type Auth interface {
	NeedsSetup(ctx context.Context) (bool, error)
	// EnsureSetupToken creates the one-time token when no owner exists; written to
	// <data>/setup-token (0600) and printed once in the boot banner.
	EnsureSetupToken(ctx context.Context) (plain string, created bool, err error)
	RotateSetupToken(ctx context.Context) (string, error)
	ClaimSetup(ctx context.Context, token string) (grantCookie string, err error) // constant-time; 30 min grant
	CreateOwner(ctx context.Context, grantCookie, username, password, timezone string) (cookie string, a Actor, err error)
	CompleteSetup(ctx context.Context, a Actor) error // deletes token file; setup routes then 404

	Login(ctx context.Context, username, password, ip string) (cookie string, a Actor, err error)
	FromCookie(ctx context.Context, cookie string) (Actor, error)
	FromBearer(ctx context.Context, token string) (Actor, error)
	Logout(ctx context.Context, cookie string) error
	ViewerFor(ctx context.Context, u UserID) (Viewer, error) // own principal; Admin = role is owner/admin

	// Passwords (CreateOwner, CreateUser, SetPassword, ResetPassword) must be at
	// least auth.password_min_length and not in the embedded list of 10k common
	// passwords (internal/auth, server-side only) => ErrInvalid with a message.
	CreateUser(ctx context.Context, by Actor, username, password, displayName string, role Role) (*User, error)
	UpdateUser(ctx context.Context, by Actor, u *User) error // role/disabled/display name; disabling revokes sessions and tokens
	SetPassword(ctx context.Context, u UserID, old, new string) error
	// ResetPassword is for the offline CLI (`bothy user reset-password`): no old
	// password, revokes the user's sessions, audited auth.password_changed.
	ResetPassword(ctx context.Context, u UserID, new string) error
	CreateToken(ctx context.Context, by Actor, spec TokenSpec) (plain string, rec *TokenRecord, err error)
	RevokeToken(ctx context.Context, by Actor, id string) error

	// Admit records the platform's verdict on a sender (SPEC §5.1 step 3)
	// and makes a member who is pending a teammate, creating their person
	// space the first time (audited identity.teammate). An outsider keeps
	// their status and chats but loses Can approve. Blocked stays blocked.
	Admit(ctx context.Context, ident ChannelIdentity, m Membership) (IdentityResolution, error)
	// NoticeSender reports whether to send ident a refusal or waiting line:
	// at most once per 24 h per identity (records last_notice_at).
	NoticeSender(ctx context.Context, ident ChannelIdentity) (bool, error)
	ResolveIdentity(ctx context.Context, channel, externalID string) (IdentityResolution, error)
}
