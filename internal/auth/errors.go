package auth

import (
	"fmt"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// AuthError is returned when credentials (password, cookie, token, setup
// token or grant) are missing, wrong, expired or revoked. The message is safe
// to show and never says which part was wrong. Callers detect it through the
// Unauthenticated method rather than the type.
type AuthError struct{ msg string }

func (e *AuthError) Error() string         { return e.msg }
func (e *AuthError) Unauthenticated() bool { return true }

func unauthenticated(msg string) error { return &AuthError{msg: msg} }

var (
	errCredentials = unauthenticated("invalid username or password")
	errSession     = unauthenticated("not signed in")
	errToken       = unauthenticated("invalid, expired or revoked API token")
	errSetupToken  = unauthenticated("invalid or expired setup token")
	errSetupGrant  = unauthenticated("setup session expired: claim the setup token again")
)

// refusal is an ErrInvalid whose message is a whole sentence an admin reads
// as written (docs/design/ui.md §14), so it carries no sentinel prefix.
type refusal struct{ msg string }

func (e *refusal) Error() string { return e.msg }
func (e *refusal) Unwrap() error { return core.ErrInvalid }

func refuse(format string, args ...any) error { return &refusal{msg: fmt.Sprintf(format, args...)} }

// LimitError reports a lockout or attempt limit; RetryAfter says when the
// caller may try again.
type LimitError struct {
	msg   string
	after time.Duration
}

func (e *LimitError) Error() string             { return e.msg }
func (e *LimitError) RetryAfter() time.Duration { return e.after }

func limited(what string, after time.Duration) error {
	after = max(after.Round(time.Second), time.Second)
	return &LimitError{msg: fmt.Sprintf("%s: try again in %s", what, after), after: after}
}
