package fakes

import (
	"context"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.Auth = (*Auth)(nil)

// Auth resolves identities from Identities (key "<channel>:<external id>"),
// builds Viewers from Viewers, and records Admit and NoticeSender calls.
//
// An unknown identity resolves to the zero IdentityResolution; a user without
// a Viewers entry gets a Viewer of their own principal. NoticeSender replies
// once per identity per 24 h (by Clock, or the system clock when nil). Admit
// records the verdict and, for a member, resolves the identity as an admitted
// teammate (Principal from Principals, when set).
type Auth struct {
	core.Auth
	Identities map[string]core.IdentityResolution
	Viewers    map[core.UserID]core.Viewer
	Notices    []core.ChannelIdentity
	// Principals gives a teammate Admit lets in their person space.
	Principals map[string]core.PrincipalID

	mu       sync.Mutex
	Admitted []string // "<channel>:<external id>|<verdict>" per Admit call
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

func (a *Auth) NoticeSender(_ context.Context, ident core.ChannelIdentity) (bool, error) {
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
	return reply, nil
}

func (a *Auth) Admit(_ context.Context, ident core.ChannelIdentity, m core.Membership) (core.IdentityResolution, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := ident.Channel + ":" + ident.ExternalID
	a.Admitted = append(a.Admitted, key+"|"+string(m.Status))
	if a.Identities == nil {
		a.Identities = map[string]core.IdentityResolution{}
	}
	res := a.Identities[key]
	was := res.Admitted()
	if m.Status != core.MemberUnchecked {
		res.Member, res.MemberAt = m.Status, a.now()
	}
	if m.Status == core.MemberYes && (res.Status == "" || res.Status == core.IdentityPending) {
		res.Status, res.Via, res.SpaceOn = core.IdentityTeammate, core.ViaMembership, true
		res.Principal = a.Principals[key]
	}
	if res.Status == "" {
		res.Status = core.IdentityPending
	}
	if res.Member == core.MemberOutsider {
		res.CanApprove = false
	}
	res.Lost = was && !res.Admitted() && res.Member == core.MemberOutsider
	stored := res
	stored.Lost = false
	a.Identities[key] = stored
	return res, nil
}
