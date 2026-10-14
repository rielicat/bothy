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
