package provider

import (
	"context"

	"github.com/bothyhq/bothy/internal/core"
)

func (f *Facade) Checks() []core.Check { return []core.Check{authCheck{f}} }

// authCheck is the doctor's providers.auth_failed: a provider whose last call
// was refused for its credentials.
type authCheck struct{ f *Facade }

func (authCheck) ID() string { return "providers.auth_failed" }

func (c authCheck) Run(context.Context) []core.Finding {
	var out []core.Finding
	for _, h := range c.f.Health() {
		if h.State != stateAuthFailed {
			continue
		}
		out = append(out, core.Finding{
			ID:       "providers.auth_failed",
			Area:     "providers",
			Title:    "Provider " + h.ID + " rejects its API key",
			Detail:   h.LastError,
			Fix:      "/settings/providers",
			Severity: core.SevHigh,
		})
	}
	return out
}

func (authCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }
