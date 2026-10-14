//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

type responsesItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Role             string          `json:"role"`
	Phase            string          `json:"phase"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	EncryptedContent string          `json:"encrypted_content"`
	Output           json.RawMessage `json:"output"`
}

type responsesBody struct {
	Instructions string            `json:"instructions"`
	Input        []json.RawMessage `json:"input"`
	Tools        json.RawMessage   `json:"tools"`
	Store        *bool             `json:"store"`
	Include      []string          `json:"include"`
}

// TestFirstRunOpenAIResponses is the first-run flow with a provider of type
// openai: the wizard's probe and every agent call use the Responses API
// (api auto), and a tool loop carries the encrypted reasoning, the message
// and the call back in their original order.
func TestFirstRunOpenAIResponses(t *testing.T) {
	const model = "gpt-mock"
	g := Start(t, Options{
		MockLLM: []mockllm.Rule{
			{MatchSystem: `^Write a title`, Text: []string{"Cat facts"}},
			{MatchToolResult: `(?s)^`, Text: []string{"Noted: ", "Miso."}},
			{MatchUser: `probe_echo`, ToolCalls: []core.ToolCall{{Name: "probe_echo", Args: json.RawMessage(`{"value":"ok"}`)}}},
			{MatchUser: `(?i)remember that my cat`, Reasoning: "The user wants this saved to memory.", Text: []string{"Saving that."},
				ToolCalls: []core.ToolCall{{Name: "memory", Args: json.RawMessage(`{"file":"MEMORY","op":"add","new":"The user's cat is called Miso."}`)}}},
			{Text: []string{"OK"}},
		},
		Models: []core.ModelInfo{{Ref: core.ModelRef{Provider: "openai", Model: model}, Context: 32768, MaxOutput: 2048, Tools: true}},
	})

	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: g.SetupToken(t)}, &claim); code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	g.SetCSRF(claim.CSRF)
	var me dto.Me
	owner := dto.SetupOwnerReq{Username: "owner", Password: "Granite-River-42-Lantern", Timezone: "Europe/London"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK {
		t.Fatalf("owner = %d", code)
	}
	g.SetCSRF(me.CSRF)

	var probe core.ProbeResult
	test := dto.ProviderTestReq{Kind: core.KindOpenAI, BaseURL: g.LLM.URL(), Model: model, AllowPrivate: true}
	if code := g.JSON(t, http.MethodPost, "/api/providers/test", test, &probe); code != http.StatusOK || !probe.OK || !probe.ToolCalling ||
		probe.API != "responses" || !strings.Contains(probe.Detail, "Responses API") {
		t.Fatalf("provider test = %d %+v", code, probe)
	}

	provider, _ := json.Marshal(map[string]any{
		"id": "openai", "type": "openai", "base_url": g.LLM.URL(), "allow_private": true,
		"models": []map[string]any{{"id": model, "context": 32768, "max_output": 2048, "tools": true}},
	})
	set := dto.ConfigPatchReq{Set: map[string]json.RawMessage{
		"providers.openai": provider,
		"models.main":      json.RawMessage(`["openai/` + model + `"]`),
		"models.fast":      json.RawMessage(`["openai/` + model + `"]`),
	}}
	if code := g.JSON(t, http.MethodPatch, "/api/config", set, nil); code != http.StatusOK {
		t.Fatalf("config patch = %d", code)
	}
	if code := g.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("setup complete = %d", code)
	}

	var sess core.Session
	if code := g.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &sess); code/100 != 2 || sess.ID == "" {
		t.Fatalf("create session = %d %+v", code, sess)
	}
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Text: "Please remember that my cat is called Miso."}, nil); code/100 != 2 {
		t.Fatalf("send message = %d", code)
	}
	var runs dto.List[core.Run]
	chEventually(t, 30*time.Second, "the run to finish", func() bool {
		runs = dto.List[core.Run]{}
		return g.JSON(t, http.MethodGet, "/api/runs?limit=10&session="+string(sess.ID), nil, &runs) == http.StatusOK &&
			len(runs.Items) == 1 && runs.Items[0].Status.Terminal()
	})
	if r := runs.Items[0]; r.Status != core.RunDone || r.ToolCalls != 1 {
		t.Fatalf("run = %+v\nlogs:\n%s", r, g.Logs())
	}
	var mem dto.MemoryDoc
	if code := g.JSON(t, http.MethodGet, "/api/memory/MEMORY", nil, &mem); code != http.StatusOK || !strings.Contains(mem.Content, "The user's cat is called Miso.") {
		t.Fatalf("MEMORY.md = %d %q", code, mem.Content)
	}

	for _, r := range g.LLM.History() {
		if r.Method == http.MethodPost && r.Path != "/v1/responses" {
			t.Fatalf("a call went to %s", r.Path)
		}
	}
	// The two agent calls of the tool loop are the ones with tools.
	var agent []responsesBody
	for _, b := range g.LLM.Requests() {
		var body responsesBody
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body.Tools), `"name":"memory"`) {
			agent = append(agent, body)
		}
	}
	if len(agent) != 2 {
		t.Fatalf("%d agent calls", len(agent))
	}
	first, second := agent[0], agent[1]
	if first.Store == nil || *first.Store || len(first.Include) != 1 || first.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("first call: store %v include %v", first.Store, first.Include)
	}
	if first.Instructions == "" || first.Instructions != second.Instructions || !bytes.Equal(first.Tools, second.Tools) {
		t.Fatal("instructions or tools changed within the tool loop")
	}
	for i := range first.Input {
		if !bytes.Equal(first.Input[i], second.Input[i]) {
			t.Fatalf("input item %d changed:\n%s\n%s", i, first.Input[i], second.Input[i])
		}
	}
	var tail []responsesItem
	for _, raw := range second.Input[len(first.Input):] {
		var it responsesItem
		if err := json.Unmarshal(raw, &it); err != nil {
			t.Fatal(err)
		}
		tail = append(tail, it)
	}
	got := make([]string, len(tail))
	for i, it := range tail {
		got[i] = it.Type
	}
	if strings.Join(got, " ") != "reasoning message function_call function_call_output" {
		t.Fatalf("replayed items %v", got)
	}
	// mockllm numbers the items of one reply alike: rs_mock<n>, msg_mock<n>
	// and fc_mock<n>_0.
	n := strings.TrimPrefix(tail[0].ID, "rs_mock")
	if tail[1].ID != "msg_mock"+n || tail[2].ID != fmt.Sprintf("fc_mock%s_0", n) || !strings.HasPrefix(tail[0].EncryptedContent, "mockenc_") ||
		tail[1].Phase != "commentary" || tail[2].Name != "memory" || tail[2].CallID == "" || tail[3].CallID != tail[2].CallID {
		t.Fatalf("replayed items %+v", tail)
	}
}
