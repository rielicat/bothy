package channel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	// Membership lookups per adapter: a burst of new senders must not turn
	// into a burst of platform calls.
	lookupsPerMin   = 30
	lookupsInFlight = 4
	// failureMemo: after a failed lookup for a sender, the next minute's
	// messages use the last verdict instead of asking again.
	failureMemo = time.Minute
	// lookupFailedEvery bounds "can't check your access" per sender.
	lookupFailedEvery = time.Hour

	sweepEvery  = 15 * time.Minute
	sweepAge    = 45 * time.Minute
	sweepMax    = 100
	backfillMax = 50
	teamsTTL    = time.Minute
	// recheckWait bounds a synchronous recheck (TeamActive).
	recheckWait = 5 * time.Second
)

// lookupLimiter allows lookupsPerMin lookups a minute, lookupsInFlight at
// once.
type lookupLimiter struct {
	start    time.Time
	n        int
	inFlight int
}

// acquireLookup reserves a lookup slot on adapter name; release frees it.
func (h *Hub) acquireLookup(name string) (release func(), ok bool) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.lookups[name]
	if l == nil {
		l = &lookupLimiter{}
		h.lookups[name] = l
	}
	if now.Sub(l.start) >= time.Minute {
		l.start, l.n = now, 0
	}
	if l.n >= lookupsPerMin || l.inFlight >= lookupsInFlight {
		return nil, false
	}
	l.n++
	l.inFlight++
	done := false
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !done {
			done = true
			l.inFlight--
		}
	}, true
}

func (h *Hub) noteFailure(key string) {
	h.mu.Lock()
	h.failures[key] = time.Now()
	h.mu.Unlock()
}

func (h *Hub) recentFailure(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	at, ok := h.failures[key]
	return ok && time.Since(at) < failureMemo
}

func checkerOf(a *adapter) (core.MemberChecker, bool) {
	mc, ok := a.ch.(core.MemberChecker)
	return mc, ok
}

// fresh reports whether res carries a verdict younger than maxAge.
func (h *Hub) fresh(res core.IdentityResolution, maxAge time.Duration) bool {
	return !res.MemberAt.IsZero() && h.clock.Now().Sub(res.MemberAt) < maxAge
}

// usable: admitted, with a verdict younger than maxAge where the platform
// gives verdicts.
func (h *Hub) usable(a *adapter, res core.IdentityResolution, maxAge time.Duration) bool {
	if !res.Admitted() {
		return false
	}
	if _, ok := checkerOf(a); !ok {
		return true
	}
	return h.fresh(res, maxAge)
}

// check runs then with a membership verdict younger than MemberTTL,
// asking the platform when the stored one is older. The lookup runs off
// the dispatcher, in order per sender. When the platform doesn't answer
// (or lookups are throttled), a verdict younger than MemberStale still
// counts; otherwise failed runs.
func (h *Hub) check(ctx context.Context, a *adapter, ident core.ChannelIdentity, res core.IdentityResolution, teams []string,
	then func(context.Context, core.IdentityResolution), failed func(context.Context)) {
	mc, ok := checkerOf(a)
	if !ok {
		then(ctx, res)
		return
	}
	if h.syncScope(ctx, a, mc) {
		if r, err := h.d.Auth.ResolveIdentity(ctx, a.name, ident.ExternalID); err == nil {
			res = r
		}
	}
	if h.fresh(res, core.MemberTTL) {
		then(ctx, res)
		return
	}
	key := a.name + ":" + ident.ExternalID
	fallback := func(ctx context.Context, res core.IdentityResolution) {
		if res.Admitted() && h.fresh(res, core.MemberStale) {
			then(ctx, res)
			return
		}
		failed(ctx)
	}
	if h.recentFailure(key) {
		fallback(ctx, res)
		return
	}
	release, ok := h.acquireLookup(a.name)
	if !ok {
		fallback(ctx, res)
		return
	}
	h.enqueueChat(a.name+":member:"+ident.ExternalID, func(ctx context.Context) {
		defer release()
		// An earlier message of this sender may have asked already.
		if r, err := h.d.Auth.ResolveIdentity(ctx, a.name, ident.ExternalID); err == nil {
			res = r
		}
		if h.fresh(res, core.MemberTTL) {
			release()
			then(ctx, res)
			return
		}
		r, err := h.lookup(ctx, a, mc, ident, teams)
		release()
		if err != nil {
			h.noteFailure(key)
			h.warnLookup(a, err)
			fallback(ctx, res)
			return
		}
		then(ctx, r)
	})
}

// lookup asks the platform and records the verdict; a lost teammate's runs
// stop.
func (h *Hub) lookup(ctx context.Context, a *adapter, mc core.MemberChecker, ident core.ChannelIdentity, teams []string) (core.IdentityResolution, error) {
	m, err := mc.CheckMember(ctx, ident.ExternalID, teams)
	if err != nil {
		return core.IdentityResolution{}, err
	}
	res, err := h.d.Auth.Admit(ctx, ident, m)
	if err != nil {
		return core.IdentityResolution{}, err
	}
	if res.Lost {
		h.lost(ctx, a, ident, res)
	}
	return res, nil
}

// recheck renews res synchronously within wait (TeamActive, presses).
func (h *Hub) recheck(ctx context.Context, a *adapter, ident core.ChannelIdentity, wait time.Duration) (core.IdentityResolution, error) {
	mc, ok := checkerOf(a)
	if !ok {
		return h.d.Auth.ResolveIdentity(ctx, a.name, ident.ExternalID)
	}
	h.syncScope(ctx, a, mc)
	release, ok := h.acquireLookup(a.name)
	if !ok {
		return core.IdentityResolution{}, errors.New("membership lookups are throttled")
	}
	defer release()
	rctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	res, err := h.lookup(rctx, a, mc, ident, h.teamsOf(rctx, a))
	if err != nil {
		h.warnLookup(a, err)
	}
	return res, err
}

func (h *Hub) warnLookup(a *adapter, err error) {
	if h.noteOnce("lookup-warn:"+a.name, lookupFailedEvery) {
		h.log.Warn("channel: membership lookup failed", "channel", a.name, "err", err)
	}
}

// lost handles a teammate the platform now says is outside the team: their
// runs stop; their chats stay.
func (h *Hub) lost(ctx context.Context, a *adapter, ident core.ChannelIdentity, res core.IdentityResolution) {
	if res.Principal != "" {
		sctx := core.WithAuditOrigin(ctx, core.AuditOrigin{Actor: "system", Detail: map[string]any{"reason": "membership"}})
		if _, err := h.d.Runner.StopPrincipal(sctx, res.Principal); err != nil {
			h.log.Warn("channel: stopping a departed teammate's tasks failed", "channel", a.name, "err", err)
		}
	}
	h.inboxChanged("sender:" + a.name + ":" + ident.ExternalID)
}

// teamsOf lists the servers with a channel Bothy is on in, for adapters
// that have several (Discord): membership there is what counts.
func (h *Hub) teamsOf(ctx context.Context, a *adapter) []string {
	if _, ok := a.ch.(core.TeamLister); !ok {
		return nil
	}
	now := time.Now()
	h.mu.Lock()
	if e, ok := h.teams[a.name]; ok && now.Sub(e.at) < teamsTTL {
		h.mu.Unlock()
		return e.ids
	}
	h.mu.Unlock()
	spaces, err := h.d.Store.Spaces().List(ctx, core.SystemViewer())
	if err != nil {
		h.log.Warn("channel: listing channels failed", "err", err)
		return nil
	}
	var ids []string
	for _, sp := range spaces {
		if sp.Kind == core.SpaceGroup && sp.Enabled && sp.Chat.Channel == a.name && sp.Team != "" && !slices.Contains(ids, sp.Team) {
			ids = append(ids, sp.Team)
		}
	}
	slices.Sort(ids)
	h.mu.Lock()
	h.teams[a.name] = teamsEntry{ids: ids, at: now}
	h.mu.Unlock()
	return ids
}

type teamsEntry struct {
	ids []string
	at  time.Time
}

// InvalidateTeams drops the cached server list; httpapi calls it when an
// admin turns a channel on or off.
func (h *Hub) InvalidateTeams() {
	h.mu.Lock()
	clear(h.teams)
	h.mu.Unlock()
}

func scopeKey(name string) string { return "member_scope:" + name }

// syncScope makes every verdict on a stale at once when what membership
// means there changes: another Slack workspace's token, or the set of
// Discord servers with a channel on. It reports whether it did.
func (h *Hub) syncScope(ctx context.Context, a *adapter, mc core.MemberChecker) bool {
	// Before it connects, an adapter may not know its own workspace yet.
	if a.ch.Status().State != "connected" {
		return false
	}
	scope := mc.MemberScope() + "|" + strings.Join(h.teamsOf(ctx, a), ",")
	h.mu.Lock()
	last, known := h.scopes[a.name]
	h.mu.Unlock()
	stored := known
	if !known {
		v, err := h.d.Store.Meta().Get(ctx, scopeKey(a.name))
		switch {
		case err == nil:
			last, stored = v, true
		case !errors.Is(err, core.ErrNotFound):
			h.log.Warn("channel: reading the membership scope failed", "channel", a.name, "err", err)
			return false
		}
	}
	if known && last == scope {
		return false
	}
	// The first scope ever seen makes no verdict stale: none was given
	// under another one.
	reset := stored && last != scope
	if reset {
		if err := h.d.Store.Identities().ResetMembership(ctx, a.name); err != nil {
			h.log.Warn("channel: resetting membership verdicts failed", "channel", a.name, "err", err)
			return false
		}
	}
	if last != scope || !stored {
		if err := h.d.Store.Meta().Set(ctx, scopeKey(a.name), scope); err != nil {
			h.log.Warn("channel: saving the membership scope failed", "channel", a.name, "err", err)
		}
	}
	h.mu.Lock()
	h.scopes[a.name] = scope
	h.mu.Unlock()
	return reset
}

// memberSweep renews, every 15 minutes, the verdicts that matter when
// nobody is writing: approvers (their cards) and teammates with schedules.
func (h *Hub) memberSweep(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.contain("membership sweep", func() { h.sweepOnce(ctx) })
		}
	}
}

func (h *Hub) sweepOnce(ctx context.Context) {
	ids, err := h.d.Store.Identities().List(ctx, core.IdentityTeammate)
	if err != nil {
		h.log.Warn("channel: membership sweep failed", "err", err)
		return
	}
	scheduled, err := h.d.Store.Schedules().PrincipalsWithEnabled(ctx)
	if err != nil {
		h.log.Warn("channel: membership sweep failed", "err", err)
		return
	}
	now := h.clock.Now()
	n := 0
	for _, id := range ids {
		if n >= sweepMax || ctx.Err() != nil {
			return
		}
		a := h.live(id.Channel)
		if a == nil {
			continue
		}
		mc, ok := checkerOf(a)
		if !ok || (!id.CanApprove && !slices.Contains(scheduled, id.Principal)) {
			continue
		}
		if !id.MemberAt.IsZero() && now.Sub(id.MemberAt) < sweepAge {
			continue
		}
		h.syncScope(ctx, a, mc)
		release, ok := h.acquireLookup(a.name)
		if !ok {
			continue
		}
		n++
		lctx, cancel := context.WithTimeout(ctx, recheckWait)
		_, err := h.lookup(lctx, a, mc, *id, h.teamsOf(ctx, a))
		cancel()
		release()
		if err != nil {
			h.warnLookup(a, err)
		}
	}
}

// backfillTeams fills in the server of Discord channels recorded before
// Bothy knew it, so their servers count for membership.
func (h *Hub) backfillTeams(ctx context.Context, a *adapter) {
	insp, ok := a.ch.(core.ChatInspector)
	if _, lists := a.ch.(core.TeamLister); !ok || !lists {
		return
	}
	spaces, err := h.d.Store.Spaces().List(ctx, core.SystemViewer())
	if err != nil {
		h.log.Warn("channel: listing channels failed", "err", err)
		return
	}
	n := 0
	for _, sp := range spaces {
		if n >= backfillMax || ctx.Err() != nil {
			break
		}
		if sp.Kind != core.SpaceGroup || sp.Chat.Channel != a.name || sp.Team != "" {
			continue
		}
		release, ok := h.acquireLookup(a.name)
		if !ok {
			break
		}
		n++
		lctx, cancel := context.WithTimeout(ctx, recheckWait)
		info, err := insp.InspectChat(lctx, sp.Chat)
		cancel()
		release()
		if err != nil {
			h.log.Info("channel: describing a channel failed", "channel", a.name, "err", err)
			continue
		}
		h.applyChatInfo(ctx, a, sp.Principal, info, false)
	}
	h.InvalidateTeams()
}

// applyChatInfo stores what the platform says about a group. A title is
// taken only while the space has its fallback name.
func (h *Hub) applyChatInfo(ctx context.Context, a *adapter, p core.PrincipalID, info core.ChatInfo, withExternal bool) {
	err := h.d.Store.Tx(ctx, func(tx core.Store) error {
		sp, err := tx.Spaces().Get(ctx, core.SystemViewer(), p)
		if err != nil {
			return err
		}
		if info.TeamID != "" {
			sp.Team = info.TeamID
		}
		if info.TeamName != "" {
			sp.TeamName = info.TeamName
		}
		if info.Members > 0 {
			sp.Members = info.Members
		}
		if withExternal {
			sp.External = info.External
		}
		if t := spaceTitle(info.Title); t != "" && sp.Title == fallbackTitle(a.name, sp.Chat.ChatID) {
			sp.Title = t
			if pr, err := tx.Principals().Get(ctx, core.SystemViewer(), p); err == nil && pr.DisplayName != t {
				pr.DisplayName = t
				if err := tx.Principals().Update(ctx, pr); err != nil {
					return err
				}
			}
		}
		sp.UpdatedAt = h.clock.Now()
		return tx.Spaces().Update(ctx, sp)
	})
	if err != nil {
		h.log.Warn("channel: saving what the platform says about a group failed", "channel", a.name, "err", err)
	}
}

// idleServers is the Discord status detail when Bothy is in servers where
// no channel is on, so their members can't use it.
func (h *Hub) idleServers(ctx context.Context, a *adapter) string {
	tl, ok := a.ch.(core.TeamLister)
	if !ok {
		return ""
	}
	on := h.teamsOf(ctx, a)
	n := 0
	for _, t := range tl.Teams() {
		if !slices.Contains(on, t.ID) {
			n++
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
		return "Bothy is in 1 server where it isn't on in any channel."
	}
	return fmt.Sprintf("Bothy is in %d servers where it isn't on in any channel.", n)
}
