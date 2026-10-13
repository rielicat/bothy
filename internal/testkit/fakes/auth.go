package fakes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.Auth = (*Auth)(nil)

// PairingTTL is how long codes from NewPairingCode and SenderNotice last.
const PairingTTL = 15 * time.Minute

// Auth resolves identities from Identities (key "<channel>:<external id>"),
// builds Viewers from Viewers, and records pairing and sender-notice calls.
//
// An unknown identity resolves to the zero IdentityResolution; a user without
// a Viewers entry gets a Viewer of their own principal. SenderNotice replies
// once per identity per 24 h (by Clock, or the system clock when nil).
// RedeemPairing accepts a code from Codes (normalized: upper case without
// dashes or spaces) once, then resolves that identity as a linked member.
// NewPairingCode registers a fresh code in Codes.
type Auth struct {
	core.Auth
	Identities map[string]core.IdentityResolution
	Viewers    map[core.UserID]core.Viewer
	Notices    []core.ChannelIdentity

	mu       sync.Mutex
	Codes    map[string]core.UserID
	Redeemed []string // "<channel>:<external id>|<code>" per RedeemPairing call
	Clock    core.Clock
	notified map[string]time.Time
}

func (a *Auth) now() time.Time {
	if a.Clock != nil {
		return a.Clock.Now()
	}
	return time.Now()
}

func (a *Auth) ResolveIdentity(_ context.Context, channel, externalID string) (core.IdentityResolution, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Identities[channel+":"+externalID], nil
}

func (a *Auth) ViewerFor(_ context.Context, u core.UserID) (core.Viewer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if v, ok := a.Viewers[u]; ok {
		return v, nil
	}
	return core.Viewer{User: u, Principals: []core.PrincipalID{core.UserPrincipal(u)}}, nil
}

func (a *Auth) SenderNotice(_ context.Context, ident core.ChannelIdentity) (core.SenderNotice, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Notices = append(a.Notices, ident)
	key := ident.Channel + ":" + ident.ExternalID
	now := a.now()
	last, seen := a.notified[key]
	reply := !seen || now.Sub(last) >= 24*time.Hour
	if reply {
		if a.notified == nil {
			a.notified = map[string]time.Time{}
		}
		a.notified[key] = now
	}
	sum := sha256.Sum256([]byte(key))
	return core.SenderNotice{Reply: reply, Code: core.PairingCode{Code: formatCode(crockfordUpper(sum[:5])), ExpiresAt: now.Add(PairingTTL)}}, nil
}

func (a *Auth) RedeemPairing(_ context.Context, ident core.ChannelIdentity, code string) (*core.ChannelIdentity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := ident.Channel + ":" + ident.ExternalID
	a.Redeemed = append(a.Redeemed, key+"|"+code)
	norm := normalizeCode(code)
	u, ok := a.Codes[norm]
	if !ok {
		return nil, fmt.Errorf("pairing code: %w", core.ErrNotFound)
	}
	delete(a.Codes, norm)
	if a.Identities == nil {
		a.Identities = map[string]core.IdentityResolution{}
	}
	a.Identities[key] = core.IdentityResolution{Status: core.IdentityLinked, User: u, Role: core.RoleMember, Principal: core.UserPrincipal(u)}
	linked := ident
	linked.Status, linked.User = core.IdentityLinked, u
	return &linked, nil
}

func (a *Auth) NewPairingCode(_ context.Context, by core.Actor) (core.PairingCode, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return core.PairingCode{}, err
	}
	code := crockfordUpper(b[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Codes == nil {
		a.Codes = map[string]core.UserID{}
	}
	a.Codes[code] = by.User
	return core.PairingCode{Code: formatCode(code), ExpiresAt: a.now().Add(PairingTTL)}, nil
}

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// crockfordUpper encodes 5 bytes as 8 Crockford base32 characters.
func crockfordUpper(b []byte) string {
	var v uint64
	for _, x := range b[:5] {
		v = v<<8 | uint64(x)
	}
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = crockfordAlphabet[v&31]
		v >>= 5
	}
	return string(out)
}

func formatCode(c string) string { return c[:4] + "-" + c[4:] }

func normalizeCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(c))
}
