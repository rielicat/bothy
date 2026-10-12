package provider

import (
	"cmp"
	"slices"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	breakerThreshold = 3
	breakerOpenFor   = 60 * time.Second
)

// Health states reported by core.ProviderHealth.
const (
	stateOK         = "ok"
	stateDegraded   = "degraded"
	stateDown       = "down"
	stateAuthFailed = "auth_failed"
)

// health is one provider's circuit breaker and reported state. Three
// consecutive failures open the breaker for 60s; the first attempt after that
// is a probe: success closes it, failure re-opens it at once.
type health struct {
	kind      core.ProviderKind
	state     string
	lastErr   string
	since     time.Time
	fails     int
	openUntil time.Time
}

func (f *Facade) healthOf(e *entry) *health {
	h := f.health[e.id]
	if h == nil {
		h = &health{kind: e.kind, state: stateOK, since: f.boot}
		f.health[e.id] = h
	}
	h.kind = e.kind
	return h
}

func (h *health) set(state string, now time.Time) {
	if h.state != state {
		h.state, h.since = state, now
	}
}

// open reports whether the breaker currently skips the provider.
func (f *Facade) open(e *entry) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock.Now().Before(f.healthOf(e).openUntil)
}

func (f *Facade) succeeded(e *entry) {
	f.mu.Lock()
	h := f.healthOf(e)
	wasAuth := h.state == stateAuthFailed
	h.fails, h.openUntil, h.lastErr = 0, time.Time{}, ""
	h.set(stateOK, f.clock.Now())
	f.mu.Unlock()
	if wasAuth {
		f.updateBanner()
	}
}

func (f *Facade) failed(e *entry, err error) {
	f.mu.Lock()
	now := f.clock.Now()
	h := f.healthOf(e)
	h.fails++
	h.lastErr = errText(err)
	if h.fails >= breakerThreshold {
		h.openUntil = now.Add(breakerOpenFor)
		h.set(stateDown, now)
	} else if h.state != stateAuthFailed {
		h.set(stateDegraded, now)
	}
	f.mu.Unlock()
}

// authFailed marks the provider auth_failed and drops its cached secret so a
// key fixed in the vault is used on the next attempt.
func (f *Facade) authFailed(e *entry, err error) {
	e.creds.invalidate()
	f.mu.Lock()
	now := f.clock.Now()
	h := f.healthOf(e)
	h.fails++
	h.lastErr = errText(err)
	if h.fails >= breakerThreshold {
		h.openUntil = now.Add(breakerOpenFor)
	}
	h.set(stateAuthFailed, now)
	f.mu.Unlock()
	f.updateBanner()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// updateBanner raises provider_auth once while any configured provider is
// auth_failed and clears it when none is.
func (f *Facade) updateBanner() {
	if f.d.Bus == nil {
		return
	}
	f.bannerMu.Lock()
	defer f.bannerMu.Unlock()
	var failed []string
	for _, h := range f.Health() {
		if h.State == stateAuthFailed {
			failed = append(failed, h.ID)
		}
	}
	f.mu.Lock()
	raise := len(failed) > 0 && !f.banner
	clear := len(failed) == 0 && f.banner
	f.banner = len(failed) > 0
	f.mu.Unlock()
	switch {
	case raise:
		f.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{
			Level: "error", Code: "provider_auth", Action: "/settings/providers",
			Text: "The " + failed[0] + " provider rejected its API key. Update the key in Settings → Vault.",
		}})
	case clear:
		f.d.Bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Code: "provider_auth", Clear: true}})
	}
}

// Health reports every enabled provider; providers not called yet are ok.
func (f *Facade) Health() []core.ProviderHealth {
	cfg := f.d.Config.Current()
	if cfg == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.ProviderHealth
	for _, p := range cfg.Providers {
		if !enabled(p) || !validKind(core.ProviderKind(p.Type)) {
			continue
		}
		ph := core.ProviderHealth{ID: p.ID, Kind: core.ProviderKind(p.Type), State: stateOK, Since: f.boot}
		if h := f.health[p.ID]; h != nil {
			ph.State, ph.LastError, ph.Since = h.state, h.lastErr, h.since
		}
		out = append(out, ph)
	}
	slices.SortFunc(out, func(a, b core.ProviderHealth) int { return cmp.Compare(a.ID, b.ID) })
	return out
}
