//go:build e2e && budgets

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/discord/discordtest"
	"github.com/rielicat/bothy/internal/channel/slack/slacktest"
	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// SPEC §11.3 footprint budgets (E18). scripts/budgets/footprint.sh runs these.

func TestColdStart(t *testing.T) {
	const budget = 500 * time.Millisecond
	var boots []time.Duration
	for range 5 {
		g := Start(t, Options{})
		boots = append(boots, g.Boot)
		if err := g.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(boots, func(i, j int) bool { return boots[i] < boots[j] })
	median := boots[len(boots)/2]
	t.Logf("cold start to /healthz: median %s (all %v), budget %s", median, boots, budget)
	if median > budget {
		t.Errorf("cold start %s exceeds %s", median, budget)
	}
}

// budgetTelegram, budgetDiscord and budgetSlack wire the channel fakes in
// through the locked api_base / gateway_url keys and env secrets.
type budgetTelegram struct{ *telegramtest.Server }

func (p budgetTelegram) Env() map[string]string {
	return map[string]string{"BOTHY_CHANNELS__TELEGRAM__ENABLED": "true", "BOTHY_CHANNELS__TELEGRAM__API_BASE": p.URL,
		"BOTHY_SECRET_TELEGRAM_BOT_TOKEN": p.Token}
}

type budgetDiscord struct{ *discordtest.Server }

func (p budgetDiscord) Env() map[string]string {
	return map[string]string{"BOTHY_CHANNELS__DISCORD__ENABLED": "true", "BOTHY_CHANNELS__DISCORD__API_BASE": p.URL(),
		"BOTHY_CHANNELS__DISCORD__GATEWAY_URL": p.GatewayURL(), "BOTHY_CHANNELS__DISCORD__APPLICATION_ID": p.AppID,
		"BOTHY_SECRET_DISCORD_BOT_TOKEN": p.Token}
}

type budgetSlack struct{ *slacktest.Server }

func (p budgetSlack) Env() map[string]string {
	return map[string]string{"BOTHY_CHANNELS__SLACK__ENABLED": "true", "BOTHY_CHANNELS__SLACK__API_BASE": p.URL(),
		"BOTHY_SECRET_SLACK_APP_TOKEN": p.AppToken, "BOTHY_SECRET_SLACK_BOT_TOKEN": p.BotToken}
}

func TestIdleRSS(t *testing.T) {
	const budget = 60 << 20
	wait := 10 * time.Minute
	if v := os.Getenv("BOTHY_RSS_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("BOTHY_RSS_WAIT: %v", err)
		}
		wait = d
	}
	tg, dc, sl := telegramtest.New(telegramtest.Config{}), discordtest.New(), slacktest.New()
	// Same memory limit as deploy/compose.yaml.
	g := Start(t, Options{Env: map[string]string{"GOMEMLIMIT": "48MiB"},
		Platforms: []Platform{budgetTelegram{tg}, budgetDiscord{dc}, budgetSlack{sl}}})
	connected := func() bool { return tg.Calls("getUpdates") > 0 && dc.Connected() && sl.Connected() }
	for deadline := time.Now().Add(30 * time.Second); !connected(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("channels did not connect: telegram polls %d, discord %v, slack %v", tg.Calls("getUpdates"), dc.Connected(), sl.Connected())
		}
	}
	start, err := g.RSS()
	if err != nil {
		t.Fatal(err)
	}
	var peak, last int64
	for end := time.Now().Add(wait); ; {
		if last, err = g.RSS(); err != nil {
			t.Fatal(err)
		}
		peak = max(peak, last)
		if !time.Now().Before(end) {
			break
		}
		time.Sleep(min(10*time.Second, time.Until(end)))
	}
	if !connected() || dc.Identifies() != 1 || sl.Connections() != 1 {
		t.Errorf("channels did not stay connected: discord %v (%d identifies), slack %v (%d connections)",
			dc.Connected(), dc.Identifies(), sl.Connected(), sl.Connections())
	}
	mib := func(b int64) float64 { return float64(b) / (1 << 20) }
	t.Logf("idle RSS with telegram, discord and slack connected: %.1f MiB after connecting, %.1f MiB after %s, peak %.1f MiB, budget %d MiB",
		mib(start), mib(last), wait, mib(peak), budget>>20)
	if peak > budget {
		t.Errorf("idle RSS %.1f MiB exceeds %d MiB", mib(peak), budget>>20)
	}
}

// TestTokenOverhead measures S1 plus the core tool schemas (standard
// profile) in the first agent request mockllm records. Without a sandbox the
// exec tools are absent; TestComposeShell measures the set with them.
func TestTokenOverhead(t *testing.T) {
	const model = "mock-64k"
	g := Start(t, Options{
		MockLLM: []mockllm.Rule{{Text: []string{"OK"}}},
		Models:  []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: model}, Context: 65536, MaxOutput: 2048, Tools: true}},
	})
	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: g.SetupToken(t)}, &claim); code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	g.SetCSRF(claim.CSRF)
	var me dto.Me
	owner := dto.SetupOwnerReq{Username: "owner", Password: "Granite-River-42-Lantern", Timezone: "UTC"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK {
		t.Fatalf("owner = %d", code)
	}
	g.SetCSRF(me.CSRF)
	provider, _ := json.Marshal(map[string]any{
		"id": "mock", "type": "openai_compat", "base_url": g.LLM.URL(), "allow_private": true,
		"models": []map[string]any{{"id": model, "context": 65536, "max_output": 2048, "tools": true}},
	})
	set := dto.ConfigPatchReq{Set: map[string]json.RawMessage{
		"providers.mock": provider, "models.main": json.RawMessage(`["mock/` + model + `"]`),
	}}
	if code := g.JSON(t, http.MethodPatch, "/api/config", set, nil); code != http.StatusOK {
		t.Fatalf("config patch = %d", code)
	}
	events := g.Events(t, "", nil)
	events.WaitFor(t, "hello", 5*time.Second)
	var sess core.Session
	if code := g.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &sess); code/100 != 2 {
		t.Fatalf("create session = %d", code)
	}
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Text: "hello"}, nil); code/100 != 2 {
		t.Fatalf("send = %d", code)
	}
	events.WaitFor(t, string(core.EvRunFinished), 20*time.Second)
	var cons struct {
		Content string `json:"content"`
	}
	if code := g.JSON(t, http.MethodGet, "/api/constitution", nil, &cons); code != http.StatusOK {
		t.Fatalf("GET /api/constitution = %d", code)
	}
	o := MeasureTokenOverhead(t, g.LLM.Requests(), cons.Content)
	t.Logf("fixed token overhead: S1 %d + tools %d = %d estimated tokens (budget 2500; tools budget 1500); whole system prompt %d; tools %v",
		o.S1, o.Tools, o.Fixed(), o.System, o.ToolNames)
	if o.Fixed() > 2500 || o.Tools > 1500 {
		t.Errorf("fixed token overhead over budget: S1 %d, tools %d", o.S1, o.Tools)
	}
}
