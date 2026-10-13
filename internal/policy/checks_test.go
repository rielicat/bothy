package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func runChecks(t *testing.T, mod func(*config.Config)) map[string]core.Finding {
	t.Helper()
	h := newHarness(t, mod)
	out := map[string]core.Finding{}
	for _, c := range h.engine.Checks() {
		for _, f := range c.Run(context.Background()) {
			if f.ID != c.ID() {
				t.Errorf("check %s produced finding %s", c.ID(), f.ID)
			}
			if !f.Security || f.Severity != core.SevMedium || f.Title == "" || f.Detail == "" {
				t.Errorf("finding %+v", f)
			}
			out[f.ID] = f
		}
		if err := c.Fix(context.Background(), core.Finding{ID: c.ID()}); !errors.Is(err, core.ErrNotImplemented) {
			t.Errorf("%s: Fix = %v", c.ID(), err)
		}
	}
	return out
}

func TestChecks(t *testing.T) {
	h := newHarness(t, nil)
	ids := []string{}
	for _, c := range h.engine.Checks() {
		ids = append(ids, c.ID())
	}
	if strings.Join(ids, ",") != "policy.hands_off,policy.auto_external_rules,budgets.unlimited" {
		t.Errorf("check ids %v", ids)
	}
	if got := runChecks(t, nil); len(got) != 0 {
		t.Errorf("defaults produce findings: %v", got)
	}
	got := runChecks(t, func(c *config.Config) {
		c.Policy.Preset = "hands_off"
		c.Budgets.DailyUSD = 0
		c.Policy.Defaults = map[string]string{"external_send": "auto"}
		c.Policy.Rules = append(c.Policy.Rules,
			config.Rule{ID: "mail", Tool: "send_mail", SideEffect: "external_send", Verdict: "auto"},
			config.Rule{ID: "everything", Verdict: "auto"},
			config.Rule{ID: "fine", Tool: "fs_read", Verdict: "auto"},
		)
	})
	for _, id := range []string{"policy.hands_off", "policy.auto_external_rules", "budgets.unlimited"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing %s", id)
		}
	}
	detail := got["policy.auto_external_rules"].Detail
	for _, want := range []string{"policy.defaults.external_send", "policy.rules.mail", "policy.rules.everything"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %s", detail, want)
		}
	}
	if strings.Contains(detail, "policy.rules.fine") || strings.Contains(detail, "readonly-shell") {
		t.Errorf("detail flags harmless rules: %q", detail)
	}
}
