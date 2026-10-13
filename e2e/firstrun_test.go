//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/httpapi/dto"
	"github.com/bothyhq/bothy/internal/testkit/mockllm"
)

// TestFirstRun is E1: setup token -> owner -> provider test against mockllm
// -> first chat streamed over SSE with usage and breakdown.
func TestFirstRun(t *testing.T) {
	const model = "mock-model"
	g := Start(t, Options{
		MockLLM: []mockllm.Rule{
			{MatchUser: `(?i)hello`, Text: []string{"Hello ", "from ", "the mock."}},
			{Text: []string{"OK"}}, // the provider probe and anything else
		},
		Models: []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: model}, Context: 32768, MaxOutput: 2048, Tools: true}},
	})

	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: g.SetupToken(t)}, &claim); code != http.StatusOK || claim.CSRF == "" {
		t.Fatalf("claim = %d %+v", code, claim)
	}
	g.SetCSRF(claim.CSRF)
	var st dto.SetupState
	if code := g.JSON(t, http.MethodGet, "/api/setup/state", nil, &st); code != http.StatusOK || !st.Claimed || st.CSRF != claim.CSRF {
		t.Fatalf("setup state after claim = %d %+v", code, st)
	}

	var me dto.Me
	owner := dto.SetupOwnerReq{Username: "owner", Password: "Granite-River-42-Lantern", Timezone: "Europe/London"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK || me.CSRF == "" || me.User.Role != core.RoleOwner {
		t.Fatalf("owner = %d %+v", code, me)
	}
	g.SetCSRF(me.CSRF)

	var probe core.ProbeResult
	test := dto.ProviderTestReq{Kind: core.KindOpenAICompat, BaseURL: g.LLM.URL(), Model: model, AllowPrivate: true}
	if code := g.JSON(t, http.MethodPost, "/api/providers/test", test, &probe); code != http.StatusOK || !probe.OK {
		t.Fatalf("provider test = %d %+v", code, probe)
	}

	provider, _ := json.Marshal(map[string]any{
		"id": "mock", "type": "openai_compat", "base_url": g.LLM.URL(), "allow_private": true,
		"models": []map[string]any{{"id": model, "context": 32768, "max_output": 2048, "tools": true}},
	})
	set := dto.ConfigPatchReq{Set: map[string]json.RawMessage{
		"providers.mock": provider,
		"models.main":    json.RawMessage(`["mock/` + model + `"]`),
		"models.fast":    json.RawMessage(`["mock/` + model + `"]`),
	}}
	if code := g.JSON(t, http.MethodPatch, "/api/config", set, nil); code != http.StatusOK {
		t.Fatalf("config patch = %d", code)
	}
	if code := g.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("setup complete = %d", code)
	}

	events := g.Events(t, "", nil)
	if events.Status != http.StatusOK {
		t.Fatalf("GET /api/events = %d", events.Status)
	}
	events.WaitFor(t, "hello", 5*time.Second)

	var sess core.Session
	if code := g.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &sess); code/100 != 2 || sess.ID == "" {
		t.Fatalf("create session = %d %+v", code, sess)
	}
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Text: "hello there"}, nil); code/100 != 2 {
		t.Fatalf("send message = %d", code)
	}

	var text strings.Builder
	var usage *core.UsageData
	var finished *core.RunFinishedData
	deadline := time.Now().Add(20 * time.Second)
	for finished == nil {
		f, err := events.Next(time.Until(deadline))
		if err != nil {
			t.Fatalf("sse: %v (text so far %q)", err, text.String())
		}
		var ev struct {
			Session core.SessionID  `json:"session_id"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(f.Data, &ev) != nil || ev.Session != sess.ID {
			continue
		}
		switch f.Event {
		case string(core.EvRunDelta):
			var d core.DeltaData
			if err := json.Unmarshal(ev.Data, &d); err != nil {
				t.Fatal(err)
			}
			text.WriteString(d.Text)
		case string(core.EvRunUsage):
			usage = new(core.UsageData)
			if err := json.Unmarshal(ev.Data, usage); err != nil {
				t.Fatal(err)
			}
		case string(core.EvRunFinished):
			finished = new(core.RunFinishedData)
			if err := json.Unmarshal(ev.Data, finished); err != nil {
				t.Fatal(err)
			}
		}
	}
	if finished.Status != core.RunDone {
		t.Fatalf("run.finished = %+v\nlogs:\n%s", finished, g.Logs())
	}
	if got := text.String(); got != "Hello from the mock." {
		t.Fatalf("streamed text = %q", got)
	}
	if usage == nil || usage.Call.Usage.Output == 0 || usage.Call.Breakdown.Total == 0 || usage.Call.Breakdown.Constitution == 0 ||
		usage.Call.Breakdown.Window != 32768 {
		t.Fatalf("run.usage = %+v", usage)
	}
	if n := len(g.LLM.Requests()); n == 0 {
		t.Fatal("mockllm saw no request")
	}
}
