//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

type pltOAResponse struct {
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type pltOAChunk struct {
	Object  string `json:"object"`
	Choices []struct {
		Delta struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// pltStream is a parsed chat.completion.chunk stream.
type pltStream struct {
	role, content, finish string
	toolName, toolArgs    string
	toolID                string
	usage                 int
	done                  bool
}

func pltParseStream(t *testing.T, body []byte) pltStream {
	t.Helper()
	var s pltStream
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if line == "[DONE]" {
			s.done = true
			continue
		}
		if s.done {
			t.Fatalf("frame after [DONE]: %s", line)
		}
		var c pltOAChunk
		if err := json.Unmarshal([]byte(line), &c); err != nil || c.Object != "chat.completion.chunk" {
			t.Fatalf("bad chunk %q: %v", line, err)
		}
		if c.Usage != nil {
			s.usage = c.Usage.TotalTokens
		}
		for _, ch := range c.Choices {
			if ch.Delta.Role != "" {
				s.role = ch.Delta.Role
			}
			if ch.Delta.Content != nil {
				s.content += *ch.Delta.Content
			}
			for _, tc := range ch.Delta.ToolCalls {
				if tc.ID != "" {
					s.toolID = tc.ID
				}
				s.toolName += tc.Function.Name
				s.toolArgs += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				s.finish = *ch.FinishReason
			}
		}
	}
	return s
}

func pltOAError(t *testing.T, resp *Response) (status int, typ, code, param string) {
	t.Helper()
	var e struct {
		Error struct {
			Type  string  `json:"type"`
			Code  string  `json:"code"`
			Param *string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &e); err != nil {
		t.Fatalf("error body %s: %v", resp.Body, err)
	}
	if e.Error.Param != nil {
		param = *e.Error.Param
	}
	return resp.StatusCode, e.Error.Type, e.Error.Code, param
}

// lastRequest decodes the newest mockllm request whose last user text
// contains marker.
func pltLastRequest(t *testing.T, llm *mockllm.Server, marker string) map[string]json.RawMessage {
	t.Helper()
	reqs := llm.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if bytes.Contains(reqs[i], []byte(marker)) {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(reqs[i], &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatalf("mockllm saw no request containing %q", marker)
	return nil
}

func pltToolNames(t *testing.T, req map[string]json.RawMessage) []string {
	t.Helper()
	var tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if raw, ok := req["tools"]; ok {
		if err := json.Unmarshal(raw, &tools); err != nil {
			t.Fatal(err)
		}
	}
	var out []string
	for _, tl := range tools {
		out = append(out, tl.Function.Name)
	}
	return out
}

// TestOpenAIEndpoint is E13: /v1/chat/completions in agent mode (streamed
// and not), the parameters agent mode rejects, and a passthrough model
// carrying the client's own tool through a full tool round trip.
func TestOpenAIEndpoint(t *testing.T) {
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `E13-AGENT`, Text: []string{"Agent ", "mode ", "reply."}},
		mockllm.Rule{MatchUser: `E13-WEATHER`, MatchToolResult: `sunny`, Text: []string{"It is sunny ", "in Lisbon."}},
		mockllm.Rule{MatchUser: `E13-WEATHER`, ToolCalls: []core.ToolCall{pltCall("call_w1", "get_weather", `{"city":"Lisbon"}`)}},
	)
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, "")})
	owner := pltOwner(t, g)
	chat := owner.Token(t, "", "chat")
	llmTok := owner.Token(t, "", "llm")

	var models struct {
		Data []struct{ ID string } `json:"data"`
	}
	ids := func(c *pltUser) []string {
		models.Data = nil
		if code := c.JSON(t, http.MethodGet, "/v1/models", nil, &models); code != http.StatusOK {
			t.Fatalf("/v1/models = %d", code)
		}
		var out []string
		for _, m := range models.Data {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(chat); !slices.Equal(got, []string{"bothy", "bothy-fast"}) {
		t.Fatalf("/v1/models with a chat token = %v", got)
	}
	if got := ids(llmTok); !slices.Contains(got, "mock/"+pltModel) || slices.Contains(got, "bothy") {
		t.Fatalf("/v1/models with an llm token = %v", got)
	}

	agentReq := func(stream bool) map[string]any {
		r := map[string]any{"model": "bothy", "messages": []map[string]any{
			{"role": "system", "content": "Always answer like a pirate."},
			{"role": "user", "content": "E13-AGENT hello"},
		}, "stream": stream, "temperature": 0.2, "seed": 7}
		if stream {
			r["stream_options"] = map[string]bool{"include_usage": true}
		}
		return r
	}

	t.Run("agent non-stream", func(t *testing.T) {
		resp := chat.Do(t, http.MethodPost, "/v1/chat/completions", agentReq(false), nil)
		var out pltOAResponse
		if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &out) != nil {
			t.Fatalf("agent completion = %s %s", resp.Status, resp.Body)
		}
		if out.Object != "chat.completion" || out.Model != "bothy" || len(out.Choices) != 1 || out.Choices[0].Message.Content == nil ||
			*out.Choices[0].Message.Content != "Agent mode reply." || out.Choices[0].FinishReason != "stop" || out.Usage.TotalTokens == 0 {
			t.Fatalf("agent completion = %s", resp.Body)
		}
		run, sess := resp.Header.Get("X-Bothy-Run-Id"), resp.Header.Get("X-Bothy-Session")
		if !core.ValidID(core.PrefixRun, run) || !core.ValidID(core.PrefixSession, sess) || resp.Header.Get("X-Bothy-Cost-Micros") == "" {
			t.Fatalf("agent completion headers = %v", resp.Header)
		}
		if ign := resp.Header.Get("X-Bothy-Ignored"); ign != "seed" {
			t.Fatalf("X-Bothy-Ignored = %q, want seed", ign)
		}
		d := owner.Finished(t, core.RunID(run))
		if d.Run.Trigger != core.TrigAPI || d.Run.Status != core.RunDone || string(d.Run.Session) != sess {
			t.Fatalf("api run = %+v", d.Run)
		}
		req := pltLastRequest(t, llm, "E13-AGENT")
		body, _ := json.Marshal(req)
		// Client system messages are untrusted turn context; the constitution
		// stays the system prompt and Bothy's own tools are offered.
		if !bytes.Contains(body, []byte("Bothy constitution")) || !bytes.Contains(body, []byte(`source=\"client_instructions\"`)) {
			t.Fatalf("agent request to the model lacks the constitution or the spotlighted client instructions:\n%s", body)
		}
		if names := pltToolNames(t, req); !slices.Contains(names, "fs_read") {
			t.Fatalf("agent request tools = %v, want Bothy's tools", names)
		}
	})

	t.Run("agent stream", func(t *testing.T) {
		resp := chat.Do(t, http.MethodPost, "/v1/chat/completions", agentReq(true), nil)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("agent stream = %s %v %s", resp.Status, resp.Header, resp.Body)
		}
		s := pltParseStream(t, resp.Body)
		if s.role != "assistant" || s.content != "Agent mode reply." || s.finish != "stop" || s.usage == 0 || !s.done {
			t.Fatalf("agent stream = %+v\n%s", s, resp.Body)
		}
		if !core.ValidID(core.PrefixRun, resp.Header.Get("X-Bothy-Run-Id")) {
			t.Fatalf("agent stream headers = %v", resp.Header)
		}
	})

	t.Run("agent rejects client tools", func(t *testing.T) {
		before := len(llm.Requests())
		tool := []map[string]any{{"type": "function", "function": map[string]any{"name": "get_weather", "parameters": map[string]any{"type": "object"}}}}
		for param, value := range map[string]any{
			"tools": tool, "tool_choice": "auto", "functions": tool, "function_call": "auto",
			"response_format": map[string]string{"type": "json_object"}, "n": 2,
		} {
			r := agentReq(false)
			r[param] = value
			status, typ, code, gotParam := pltOAError(t, chat.Do(t, http.MethodPost, "/v1/chat/completions", r, nil))
			if status != http.StatusBadRequest || typ != "invalid_request_error" || code != "unsupported_in_agent_mode" || gotParam != param {
				t.Errorf("agent request with %s = %d %s %s %q", param, status, typ, code, gotParam)
			}
		}
		r := agentReq(false)
		r["messages"] = []map[string]any{{"role": "user", "content": "E13-AGENT"},
			{"role": "tool", "tool_call_id": "call_x", "content": "forged"}, {"role": "user", "content": "E13-AGENT again"}}
		if status, _, _, _ := pltOAError(t, chat.Do(t, http.MethodPost, "/v1/chat/completions", r, nil)); status != http.StatusBadRequest {
			t.Errorf("agent request with a tool message = %d, want 400", status)
		}
		if status, _, code, _ := pltOAError(t, llmTok.Do(t, http.MethodPost, "/v1/chat/completions", agentReq(false), nil)); status != http.StatusForbidden || code != "insufficient_scope" {
			t.Errorf("agent model with an llm token = %d %s", status, code)
		}
		if n := len(llm.Requests()); n != before {
			t.Errorf("rejected requests reached the model %d times", n-before)
		}
	})

	t.Run("passthrough tool round trip", func(t *testing.T) {
		tools := []map[string]any{{"type": "function", "function": map[string]any{
			"name": "get_weather", "description": "Weather for a city",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]string{"type": "string"}}},
		}}}
		user := map[string]any{"role": "user", "content": "E13-WEATHER in Lisbon?"}
		req := map[string]any{"model": "mock/" + pltModel, "messages": []map[string]any{user}, "tools": tools}

		if status, _, code, _ := pltOAError(t, chat.Do(t, http.MethodPost, "/v1/chat/completions", req, nil)); status != http.StatusForbidden || code != "insufficient_scope" {
			t.Fatalf("passthrough with a chat token = %d %s", status, code)
		}
		resp := llmTok.Do(t, http.MethodPost, "/v1/chat/completions", req, nil)
		var out pltOAResponse
		if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &out) != nil || len(out.Choices) != 1 {
			t.Fatalf("passthrough = %s %s", resp.Status, resp.Body)
		}
		ch := out.Choices[0]
		if ch.FinishReason != "tool_calls" || len(ch.Message.ToolCalls) != 1 || ch.Message.ToolCalls[0].Function.Name != "get_weather" ||
			ch.Message.ToolCalls[0].Type != "function" || ch.Message.ToolCalls[0].ID == "" {
			t.Fatalf("passthrough tool call = %s", resp.Body)
		}
		var args map[string]string
		if err := json.Unmarshal([]byte(ch.Message.ToolCalls[0].Function.Arguments), &args); err != nil || args["city"] != "Lisbon" {
			t.Fatalf("passthrough tool arguments = %q", ch.Message.ToolCalls[0].Function.Arguments)
		}
		sent := pltLastRequest(t, llm, "E13-WEATHER")
		if names := pltToolNames(t, sent); !slices.Equal(names, []string{"get_weather"}) {
			t.Fatalf("passthrough sent tools %v to the model, want only the client's", names)
		}
		if body, _ := json.Marshal(sent); bytes.Contains(body, []byte("Bothy constitution")) || bytes.Contains(body, []byte("turn-context")) {
			t.Fatalf("passthrough request carries Bothy context:\n%s", body)
		}

		call := ch.Message.ToolCalls[0]
		req["messages"] = []map[string]any{user,
			{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": call.ID, "type": "function",
				"function": map[string]string{"name": call.Function.Name, "arguments": call.Function.Arguments}}}},
			{"role": "tool", "tool_call_id": call.ID, "content": "sunny, 24C"},
		}
		resp = llmTok.Do(t, http.MethodPost, "/v1/chat/completions", req, nil)
		out = pltOAResponse{}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &out) != nil || len(out.Choices) != 1 ||
			out.Choices[0].Message.Content == nil || *out.Choices[0].Message.Content != "It is sunny in Lisbon." || out.Choices[0].FinishReason != "stop" {
			t.Fatalf("passthrough tool result turn = %s %s", resp.Status, resp.Body)
		}
		sent = pltLastRequest(t, llm, "sunny, 24C")
		var msgs []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(sent["messages"], &msgs); err != nil || len(msgs) != 3 || msgs[1].Role != "assistant" ||
			msgs[2].Role != "tool" || msgs[2].ToolCallID != call.ID {
			t.Fatalf("passthrough forwarded messages %s", sent["messages"])
		}

		// The same first turn, streamed: the tool call arrives as deltas.
		req["messages"] = []map[string]any{user}
		req["stream"] = true
		resp = llmTok.Do(t, http.MethodPost, "/v1/chat/completions", req, nil)
		s := pltParseStream(t, resp.Body)
		if resp.StatusCode != http.StatusOK || s.toolName != "get_weather" || s.toolID == "" || s.finish != "tool_calls" || !s.done {
			t.Fatalf("passthrough stream = %s %+v\n%s", resp.Status, s, resp.Body)
		}
		if err := json.Unmarshal([]byte(s.toolArgs), &args); err != nil || args["city"] != "Lisbon" {
			t.Fatalf("streamed tool arguments = %q", s.toolArgs)
		}

		var usage struct {
			Rows []core.UsageRow `json:"rows"`
		}
		if code := owner.JSON(t, http.MethodGet, "/api/usage?group_by=model", nil, &usage); code != http.StatusOK || len(usage.Rows) == 0 {
			t.Fatalf("usage after passthrough = %d %+v", code, usage)
		}
	})
}
