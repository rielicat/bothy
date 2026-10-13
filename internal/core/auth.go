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

type PairingCode struct {
	Code      string            `json:"code"` // 8 Crockford chars shown as XXXX-XXXX
	ExpiresAt time.Time         `json:"expires_at"`
	DeepLinks map[string]string `json:"deep_links"` // channel -> URL (Telegram t.me/<bot>?start=<code>)
}

type IdentityResolution struct {
	Status    IdentityStatus // "" = unknown sender
	User      UserID         // linked: the user
	Role      Role
	Principal PrincipalID // linked: user principal; guest: guest space principal
}

type SenderNotice struct {
	Reply bool        // true at most once per 24h per identity
	Code  PairingCode // approve_sender code to show to the unknown sender
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
	ViewerFor(ctx context.Context, u UserID) (Viewer, error) // own principal + owned spaces; Admin = role is owner/admin

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

	NewPairingCode(ctx context.Context, by Actor) (PairingCode, error) // link_identity for by.User
	// RedeemPairing handles "/pair CODE" and "/start CODE" from a chat.
	RedeemPairing(ctx context.Context, ident ChannelIdentity, code string) (*ChannelIdentity, error)
	// SenderNotice records an unknown DM sender and returns whether to reply.
	SenderNotice(ctx context.Context, ident ChannelIdentity) (SenderNotice, error)
	// ApproveSender redeems an approve_sender code typed into the web UI by an
	// admin: as "guest" (creates a guest space) or "link" (to linkUser). Admins
	// link a chat identity only to themselves; only the owner links one to
	// another user. An identity linked to some user is never made a guest or
	// linked to anyone else (ErrForbidden); it can be blocked, unlinked or
	// linked back to that same user.
	ApproveSender(ctx context.Context, by Actor, code, as string, linkUser UserID) (*ChannelIdentity, error)
	ResolveIdentity(ctx context.Context, channel, externalID string) (IdentityResolution, error)
}
