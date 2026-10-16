package oauth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

var (
	// ErrNotAllowed: the account is not one the rules admit (domain or
	// organization, pending membership).
	ErrNotAllowed = errors.New("oauth: account not allowed")
	// ErrUnverified: Google has not verified the account's address, whether
	// or not it is on the list, so the refusal says nothing about the list.
	ErrUnverified = errors.New("oauth: email address not verified")
	// ErrOrgAccess: GitHub answered 403 for a membership, which is OAuth App
	// access restrictions or SAML SSO the person has not authorized.
	ErrOrgAccess = errors.New("oauth: organization restricts this app")
	// ErrRejected: the provider refused the code (used, expired, or for
	// another client); the person starts again.
	ErrRejected = errors.New("oauth: provider refused the code")
	// ErrUnknownOrg: GitHub has no organization of that name.
	ErrUnknownOrg = errors.New("oauth: no such GitHub organization")
)

// ProviderError is a transport failure or an answer Bothy can't use. Desc
// is the provider's own error code or a short reason, never a token.
type ProviderError struct {
	Op     string
	Status int
	Code   core.FailureCode
	Desc   string
	Err    error
}

func (e *ProviderError) Error() string {
	var b strings.Builder
	b.WriteString("oauth: " + e.Op)
	if e.Status != 0 {
		fmt.Fprintf(&b, ": status %d", e.Status)
	}
	if e.Desc != "" {
		b.WriteString(": " + e.Desc)
	}
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	return b.String()
}

func (e *ProviderError) Unwrap() error { return e.Err }

func (e *ProviderError) FailureCode() core.FailureCode {
	if e.Code != "" {
		return e.Code
	}
	if c := core.TransportFailure(e.Err); c != "" {
		return c
	}
	return core.FailProviderError
}

// clip keeps a provider's description to one short printable line.
func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "")
	}
	return s
}
