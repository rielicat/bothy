package policy

import (
	"context"
	"fmt"
	"strings"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// Checks implements core.CheckProvider: policy.hands_off,
// policy.auto_external_rules and budgets.unlimited (SPEC §10).
func (e *Engine) Checks() []core.Check {
	return []core.Check{
		check{id: "policy.hands_off", cfg: e.cfg, run: handsOff},
		check{id: "policy.auto_external_rules", cfg: e.cfg, run: autoExternalRules},
		check{id: "budgets.unlimited", cfg: e.cfg, run: budgetsUnlimited},
	}
}

type check struct {
	id  string
	cfg config.View
	run func(c *config.Config) []core.Finding
}

func (c check) ID() string { return c.id }

func (c check) Run(context.Context) []core.Finding { return c.run(c.cfg.Current()) }

func (c check) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

func handsOff(c *config.Config) []core.Finding {
	if c.Policy.Preset != "hands_off" {
		return nil
	}
	return []core.Finding{{
		ID: "policy.hands_off", Area: "policy", Severity: core.SevMedium, Security: true,
		Title:  "Policy preset is hands_off",
		Detail: "External sends run without approval and without the first-contact check; only irreversible, admin and floor-protected actions still ask.",
		Fix:    "/settings/policy",
	}}
}

// autoExternalRules flags configuration that auto-allows sending data out:
// class overrides and rules with verdict auto for external_send or
// irreversible, and auto rules constrained by nothing at all.
func autoExternalRules(c *config.Config) []core.Finding {
	var hits []string
	for _, class := range []core.SideEffect{core.SEExternalSend, core.SEIrreversible} {
		if c.Policy.Defaults[class.String()] == string(core.VerdictAuto) {
			hits = append(hits, "policy.defaults."+class.String())
		}
	}
	for _, r := range c.Policy.Rules {
		if r.Verdict != string(core.VerdictAuto) {
			continue
		}
		external := r.SideEffect == core.SEExternalSend.String() || r.SideEffect == core.SEIrreversible.String()
		unconstrained := r.SideEffect == "" && trivialGlob(r.Tool) && len(r.Argv0) == 0 &&
			len(r.Domains) == 0 && len(r.Paths) == 0 && len(r.Match) == 0
		if external || unconstrained {
			hits = append(hits, "policy.rules."+r.ID)
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: "policy.auto_external_rules", Area: "policy", Severity: core.SevMedium, Security: true,
		Title:  "Policy auto-allows actions that can send data out",
		Detail: fmt.Sprintf("These settings let external or irreversible actions run without approval: %s.", strings.Join(hits, ", ")),
		Fix:    "/settings/policy",
	}}
}

func budgetsUnlimited(c *config.Config) []core.Finding {
	if c.Budgets.DailyUSD > 0 {
		return nil
	}
	return []core.Finding{{
		ID: "budgets.unlimited", Area: "budgets", Severity: core.SevMedium, Security: true,
		Title:  "No global daily spending cap",
		Detail: "budgets.daily_usd is 0, so model spend is unlimited; a runaway loop or abuse has no cost ceiling.",
		Fix:    "/settings/budgets",
	}}
}
