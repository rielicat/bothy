package core

import (
	"context"
	"time"
)

// Signing in with Google or GitHub next to passwords (SPEC §15).

type SignInProvider string

const (
	SignInGoogle SignInProvider = "google"
	SignInGitHub SignInProvider = "github"
)

// SignInProviders lists the providers in the order the sign-in page offers
// them.
var SignInProviders = []SignInProvider{SignInGoogle, SignInGitHub}

// Name is the provider as people know it ("Google", "GitHub").
func (p SignInProvider) Name() string {
	switch p {
	case SignInGoogle:
		return "Google"
	case SignInGitHub:
		return "GitHub"
	}
	return string(p)
}

// SignIn links a Google or GitHub account to a Bothy account. Only who the
// provider says the person is is kept, never a token.
type SignIn struct {
	Provider SignInProvider `json:"provider"`
	Subject  string         `json:"-"` // Google "sub", GitHub numeric id; never shown
	User     UserID         `json:"user_id"`
	// Email is Google's verified address; for GitHub, the verified address
	// that matched the sign-in list, else the primary verified one, or "".
	Email     string    `json:"email,omitempty"`
	Login     string    `json:"login,omitempty"` // GitHub only
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used_at"`
}

// SignInRepo is not Viewer-scoped: like UserRepo and IdentityRepo it serves
// authentication, before any Viewer exists, and ForUser and Delete take the
// caller's own user id.
type SignInRepo interface {
	// Create: ErrConflict when (provider, subject) is linked to anyone or the
	// user already has a link for provider; ErrInvalid for an unknown user,
	// provider, empty subject or over-long fields.
	Create(ctx context.Context, s *SignIn) error
	Get(ctx context.Context, p SignInProvider, subject string) (*SignIn, error) // ErrNotFound
	ForUser(ctx context.Context, u UserID) ([]*SignIn, error)                   // ordered by provider; never nil
	// Touch sets last_used_at and refreshes email and login. ErrNotFound.
	Touch(ctx context.Context, p SignInProvider, subject string, at time.Time, email, login string) error
	Delete(ctx context.Context, u UserID, p SignInProvider) error // ErrNotFound, also for another user's link
}

// SignInStore is implemented by the SQLite store and memstore. It stays
// outside Store so other Store implementations need not change.
type SignInStore interface {
	SignIns() SignInRepo
}

// SignInOption is a provider people can sign in with right now.
type SignInOption struct {
	Provider SignInProvider `json:"provider"`
	Name     string         `json:"name"` // "Google", "GitHub"
}

// SignInProviderStatus is the admin view of one provider (GET /api/sign-in).
type SignInProviderStatus struct {
	Provider     SignInProvider `json:"provider"`
	Name         string         `json:"name"`
	Enabled      bool           `json:"enabled"`           // effective config
	Ready        bool           `json:"ready"`             // enabled and usable now (the sign-in page offers it)
	Problem      string         `json:"problem,omitempty"` // no_address | incomplete | no_secret | sealed | unavailable
	RedirectURI  string         `json:"redirect_uri"`      // "" when public_url can't build one
	ClientID     string         `json:"client_id"`
	SecretSaved  bool           `json:"secret_saved"`
	SecretSource string         `json:"secret_source,omitempty"` // vault | env | aws-sm
	Orgs         []string       `json:"orgs"`                    // github; [] for google
}
