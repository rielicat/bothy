package agent

import (
	"strings"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestModelErrorIsSaidOnceInPlainWords(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := &core.ProviderError{Provider: "fake", Kind: core.ErrKindServer, Status: 529, Msg: "Overloaded (try later)."}
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Err: raw}}})
		defer h.close()
		id := h.submit("hello")
		synctest.Wait()
		run := h.run(id.Run)
		if run.Status != core.RunFailed || !strings.Contains(run.Error, "Overloaded (try later).") {
			t.Fatalf("run %s %q: the raw message belongs in the run error", run.Status, run.Error)
		}
		notices := h.notices()
		if len(notices) != 1 || notices[0] != "The AI model had a problem answering." {
			t.Fatalf("notices = %q", notices)
		}
		if strings.Contains(notices[0], ").") || strings.Contains(notices[0], "Overloaded") {
			t.Fatalf("the notice repeats the raw error: %q", notices[0])
		}
	})
}

func TestNoticeMoneyAndLimits(t *testing.T) {
	for m, want := range map[core.Micros]string{0: "$0.00", 4_000: "<$0.01", 70_000: "$0.07", 12_400_000: "$12.40",
		999_994_999: "$999.99", 1_240_000_000: "$1,240", 1_234_567_000_000: "$1,234,567"} {
		if got := money(m); got != want {
			t.Errorf("money(%d) = %q, want %q", m, got, want)
		}
	}
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{cfg: func(c *config.Config) { c.Timezone = "Europe/London" }})
		defer h.close()
		for be, want := range map[core.BudgetError]string{
			{Kind: core.BudgetSteps, Limit: 10}:          "Bothy paused after 10 steps to check in.",
			{Kind: core.BudgetRunCost, Limit: 500_000}:   "Bothy stopped because this task reached its $0.50 limit.",
			{Kind: core.BudgetDailyCost, Limit: 5000000}: "Bothy has reached today's $5.00 spending limit. It can start again at midnight (Europe/London).",
		} {
			if got := h.r.budgetNotice(&be); got != want {
				t.Errorf("notice = %q, want %q", got, want)
			}
		}
	})
	if got := autoPinNotice([]string{"Answer in French.", "Use  metric\nunits."}); got != "Pinned “Answer in French.” and “Use metric units.” so Bothy keeps them in mind." {
		t.Errorf("auto-pin notice = %q", got)
	}
}

func TestModelErrorNotice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{cfg: func(c *config.Config) {
			c.Providers = []config.Provider{
				{ID: "claude", Type: "anthropic"},
				{ID: "router", Type: "openai_compat", BaseURL: "https://openrouter.ai/api/v1"},
				{ID: "lab", Type: "openai_compat", BaseURL: "http://10.0.0.5:8000/v1"},
			}
		}})
		defer h.close()
		failed := func(kind core.ErrorKind, providers ...string) core.CallInfo {
			var ci core.CallInfo
			for _, p := range providers {
				ci.Attempts = append(ci.Attempts, core.Attempt{Ref: core.ModelRef{Provider: p, Model: "m"}, Err: "x", Kind: kind})
			}
			return ci
		}
		admin := &runState{user: &core.User{Role: core.RoleAdmin}}
		member := &runState{user: &core.User{Role: core.RoleMember}}
		for _, c := range []struct {
			rs   *runState
			ci   core.CallInfo
			pe   *core.ProviderError
			want string
		}{
			{member, failed(core.ErrKindServer, "claude"), nil, "Anthropic had a problem answering."},
			{member, failed(core.ErrKindRateLimit, "router", "router"), nil, "OpenRouter had a problem answering."},
			{member, failed(core.ErrKindNetwork, "lab"), nil, "The AI model had a problem answering."},
			{member, failed(core.ErrKindServer, "claude", "router"), nil, "No AI model could answer right now."},
			{admin, failed(core.ErrKindAuth, "claude"), nil, "Anthropic stopped accepting Bothy's key."},
			{admin, core.CallInfo{Attempts: []core.Attempt{{Ref: core.ModelRef{Provider: "router", Model: "m"}, Kind: core.ErrKindAuth,
				Err: "router: auth: credential unavailable: vault: AWS refused this server's identity access"}}}, nil,
				"Bothy couldn't read its key for OpenRouter. Check where the key is kept in Settings → Models."},
			{admin, core.CallInfo{}, &core.ProviderError{Provider: "lab", Kind: core.ErrKindAuth, Msg: "credential unavailable: x"},
				"Bothy couldn't read its AI model's key. Check where the key is kept in Settings → Models."},
			{member, failed(core.ErrKindAuth, "claude", "router"), nil, "Bothy can't reach its AI model right now. An admin has been told."},
			{admin, core.CallInfo{}, &core.ProviderError{Provider: "router", Kind: core.ErrKindServer}, "OpenRouter had a problem answering."},
			{admin, core.CallInfo{}, nil, "No AI model could answer right now."},
		} {
			if got := h.r.modelErrorNotice(c.rs, c.ci, c.pe); got != c.want {
				t.Errorf("notice for %+v = %q, want %q", c.ci.Attempts, got, c.want)
			}
		}
	})
}
