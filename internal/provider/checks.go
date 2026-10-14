package provider

import (
	"context"
	"fmt"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/ollama"
	"github.com/rielicat/bothy/internal/provider/openai"
)

func (f *Facade) Checks() []core.Check {
	return []core.Check{authCheck{f}, ollamaContextCheck{f}, chatToolsCheck{f}}
}

// chatToolsCheck is the doctor's providers.chat_tools: a model that Chat
// Completions refused function tools while reasoning, as remembered since
// the last restart. Bothy then sends its tool calls with reasoning off, or,
// when even that fails, the model cannot call tools over that API at all.
type chatToolsCheck struct{ f *Facade }

func (chatToolsCheck) ID() string { return "providers.chat_tools" }

func (c chatToolsCheck) Run(context.Context) []core.Finding {
	// Resolving the entries first drops what a changed provider config made
	// stale (entry forgets it on rebuild) before the memory is read.
	ents := map[string]*entry{}
	for _, e := range c.f.effort.Entries() {
		if _, ok := ents[e.Provider]; !ok {
			ents[e.Provider], _ = c.f.entry(e.Provider)
		}
	}
	var out []core.Finding
	for _, e := range c.f.effort.Entries() {
		ent := ents[e.Provider]
		if ent == nil || ent.api != openai.APIChat {
			continue
		}
		ref := core.ModelRef{Provider: e.Provider, Model: e.Model}.String()
		fix := "Set api: responses on provider " + e.Provider + " if the server offers the Responses API."
		if ent.kind == core.KindOpenAI {
			fix = "Set api: responses (or auto) on provider " + e.Provider + "."
		}
		f := core.Finding{ID: "providers.chat_tools", Area: "providers", Fix: "/settings/providers"}
		switch e.Mode {
		case openai.EffortOff:
			f.Title = ref + " calls tools with reasoning turned off"
			f.Detail = `Chat Completions refuses function tools while this model reasons, so Bothy sends tool requests with reasoning_effort "none", which lowers the quality of multi-step tool use. ` + fix
			f.Severity = core.SevLow
		case openai.EffortNoTools:
			f.Title = ref + " cannot call tools over Chat Completions"
			f.Detail = `Chat Completions refuses function tools for this model, with reasoning on and with reasoning_effort "none". ` + fix
			f.Severity = core.SevMedium
		default:
			continue
		}
		out = append(out, f)
	}
	return out
}

func (chatToolsCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

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
