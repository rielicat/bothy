package provider

import (
	"context"
	"fmt"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/ollama"
)

func (f *Facade) Checks() []core.Check {
	return []core.Check{authCheck{f}, ollamaContextCheck{f}, bedrockCredentialsCheck{f}}
}

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
		what := "its API key"
		if h.Kind == core.KindBedrock {
			what = "its AWS credentials"
		}
		out = append(out, core.Finding{
			ID:       "providers.auth_failed",
			Area:     "providers",
			Title:    "Provider " + h.ID + " rejects " + what,
			Detail:   h.LastError,
			Fix:      "/settings/providers",
			Severity: core.SevHigh,
		})
	}
	return out
}

func (authCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

// ollamaContextCheck is the doctor's providers.ollama_context: an Ollama
// model in the main or fast chain whose window Bothy capped below what the
// model supports (ollama.Window), because config does not set its context.
type ollamaContextCheck struct{ f *Facade }

func (ollamaContextCheck) ID() string { return "providers.ollama_context" }

func (c ollamaContextCheck) Run(ctx context.Context) []core.Finding {
	var out []core.Finding
	seen := map[string]bool{}
	for _, slot := range []core.Slot{core.SlotMain, core.SlotFast} {
		for _, m := range c.f.Chain(slot, nil) {
			ref := m.Ref.String()
			if seen[ref] {
				continue
			}
			seen[ref] = true
			e, err := c.f.entry(m.Ref.Provider)
			if err != nil || e.kind != core.KindOllama || configured(e.cfg, m.Ref.Model).Context > 0 {
				continue
			}
			oc, ok := e.cl.(*ollama.Client)
			if !ok {
				continue
			}
			c.f.ensureContext(ctx, e, &m)
			show, err := oc.Show(ctx, m.Ref.Model)
			if err != nil || show.Context <= m.Context {
				continue
			}
			out = append(out, core.Finding{
				ID:    "providers.ollama_context",
				Area:  "providers",
				Title: fmt.Sprintf("%s runs with a %s-token window", ref, thousands(m.Context)),
				Detail: fmt.Sprintf("Bothy sends num_ctx %s because this model's context is not set in its provider settings; the model supports %s. "+
					"Sending the maximum would make Ollama reserve far more memory. To use a larger window, set context for this model.",
					thousands(m.Context), thousands(show.Context)),
				Fix:      "/settings/providers",
				Severity: core.SevInfo,
			})
		}
	}
	return out
}

func (ollamaContextCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }
