package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

// passthrough calls exactly one model with the client's own tools: no Bothy
// tools, memory, constitution or fallback. It is metered against the token
// user's daily cap and audited with token counts only.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, a core.Actor, req oaRequest, raw map[string]json.RawMessage) error {
	ref, err := core.ParseModelRef(req.Model)
	if err != nil {
		return invalidParam("model", "%v", err)
	}
	if req.N != nil && *req.N > 1 {
		return invalidParam("n", "only n=1 is supported")
	}
	creq, err := translateRequest(req)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.d.Budget.CheckDaily(ctx, a.User); err != nil {
		return s.v1RunError(err)
	}
	if ign := ignoredParams(raw, passthroughHonoured); len(ign) > 0 {
		w.Header().Set("X-Bothy-Ignored", strings.Join(ign, ","))
	}
	id := "chatcmpl-" + requestID() + requestID()
	created := s.now().Unix()
	var cs *chunkStream
	var on func(core.StreamEvent) error
	if req.Stream {
		cs = newChunkStream(w, id, req.Model, created, nil)
		calls := map[string]int{}
		on = func(ev core.StreamEvent) error {
			switch ev.Type {
			case core.StreamTextDelta:
				if ev.Text == "" {
					return nil
				}
				t := ev.Text
				return cs.delta(oaDelta{Content: &t})
			case core.StreamToolCallStart:
				if ev.Call == nil {
					return nil
				}
				idx := len(calls)
				calls[ev.Call.ID] = idx
				return cs.delta(oaDelta{ToolCalls: []oaToolCall{{Index: &idx, ID: ev.Call.ID, Type: "function",
					Function: oaFunction{Name: ev.Call.Name}}}})
			case core.StreamToolCallDone:
				if ev.Call == nil {
					return nil
				}
				idx, ok := calls[ev.Call.ID]
				if !ok {
					idx = len(calls)
					calls[ev.Call.ID] = idx
					if err := cs.delta(oaDelta{ToolCalls: []oaToolCall{{Index: &idx, ID: ev.Call.ID, Type: "function",
						Function: oaFunction{Name: ev.Call.Name}}}}); err != nil {
						return err
					}
				}
				return cs.delta(oaDelta{ToolCalls: []oaToolCall{{Index: &idx, Function: oaFunction{Arguments: string(ev.Call.Args)}}}})
			}
			return nil
		}
	}
	resp, ci, err := s.d.LLM.Passthrough(ctx, ref, creq, on)
	ci.Purpose = "passthrough"
	if ci.Ref == (core.ModelRef{}) {
		ci.Ref = ref
	}
	if resp != nil || ci.Usage != (core.Usage{}) {
		if rerr := s.d.Budget.RecordSpend(ctx, a.User, ci); rerr != nil {
			s.log.Error("record passthrough spend", "err", rerr)
		}
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	s.audit(r, a, "api.passthrough", "model:"+ref.String(), outcome, map[string]any{"input": ci.Usage.TotalInput(), "output": ci.Usage.Output,
		"cost_micros": ci.Cost, "stream": req.Stream})
	if err != nil {
		if cs != nil && cs.started {
			_ = cs.fail(s.redact(err.Error()))
			return nil
		}
		return err
	}
	usage := usageOf(resp.Usage)
	reason := stopReason(resp.Stop)
	if cs != nil {
		var u *oaUsage
		if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
			u = &usage
		}
		_ = cs.finish(reason, u)
		return nil
	}
	w.Header().Set("X-Bothy-Cost-Micros", strconv.FormatInt(int64(ci.Cost), 10))
	return ok(w, oaResponse{ID: id, Object: "chat.completion", Created: created, Model: req.Model, Usage: usage,
		Choices: []oaChoice{{Message: outMessage(resp.Message), FinishReason: reason}}})
}

func stopReason(s core.StopReason) string {
	switch s {
	case core.StopTool:
		return "tool_calls"
	case core.StopMaxTokens:
		return "length"
	case core.StopRefusal:
		return "content_filter"
	}
	return "stop"
}

func outMessage(m core.Message) oaOutMessage {
	out := oaOutMessage{Role: "assistant"}
	var text strings.Builder
	for _, p := range m.Parts {
		switch {
		case p.Type == core.PartText:
			text.WriteString(p.Text)
		case p.Type == core.PartToolCall && p.ToolCall != nil:
			args := string(p.ToolCall.Args)
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, oaToolCall{ID: p.ToolCall.ID, Type: "function", Function: oaFunction{Name: p.ToolCall.Name, Arguments: args}})
		}
	}
	if text.Len() > 0 || len(out.ToolCalls) == 0 {
		t := text.String()
		out.Content = &t
	}
	return out
}

// translateRequest maps an OpenAI request onto core.Request so any provider
// kind can serve it.
func translateRequest(req oaRequest) (*core.Request, error) {
	out := &core.Request{Model: "", Temperature: req.Temperature, ToolChoice: "auto"}
	ref, _ := core.ParseModelRef(req.Model)
	out.Model = ref.Model
	n, err := maxOutput(req)
	if err != nil {
		return nil, err
	}
	out.MaxTokens = n
	if stop, err := parseStop(req.Stop); err != nil {
		return nil, err
	} else {
		out.Stop = stop
	}
	for _, t := range req.Tools {
		if t.Type != "function" || t.Function.Name == "" {
			return nil, invalidParam("tools", "only function tools with a name are supported")
		}
		schema := t.Function.Parameters
		if len(bytes.TrimSpace(schema)) == 0 || bytes.Equal(bytes.TrimSpace(schema), []byte("null")) {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, core.ToolDef{Name: t.Function.Name, Description: t.Function.Description, Schema: schema})
	}
	if tc := bytes.TrimSpace(req.ToolChoice); len(tc) > 0 && !bytes.Equal(tc, []byte("null")) {
		var choice string
		if err := json.Unmarshal(tc, &choice); err != nil || (choice != "auto" && choice != "none") {
			return nil, invalidParam("tool_choice", `tool_choice must be "auto" or "none"`)
		}
		out.ToolChoice = choice
	}
	names := map[string]string{} // tool call id -> function name, for tool results
	for _, m := range req.Messages {
		mc, err := parseContent(m.Content)
		if err != nil {
			return nil, err
		}
		text := strings.Join(mc.text, "\n")
		switch m.Role {
		case "system", "developer":
			out.System = append(out.System, core.SystemBlock{Text: text})
		case "user":
			msg := core.Message{Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUser}
			if text != "" {
				msg.Parts = append(msg.Parts, core.Part{Type: core.PartText, Text: text})
			}
			for _, img := range mc.images {
				msg.Parts = append(msg.Parts, core.Part{Type: core.PartImage, Blob: &core.Blob{MIME: img.mime, Size: int64(len(img.data)), Data: img.data}})
			}
			out.Messages = append(out.Messages, msg)
		case "assistant":
			msg := core.Message{Role: core.MsgAssistant, Kind: core.KindNormal, Provenance: core.ProvUser}
			if text != "" {
				msg.Parts = append(msg.Parts, core.Part{Type: core.PartText, Text: text})
			}
			for _, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Function.Arguments)
				if len(bytes.TrimSpace(args)) == 0 {
					args = json.RawMessage("{}")
				}
				if !json.Valid(args) {
					return nil, invalidParam("messages", "tool call %s has arguments that are not JSON", tc.ID)
				}
				names[tc.ID] = tc.Function.Name
				msg.Parts = append(msg.Parts, core.Part{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args}})
			}
			out.Messages = append(out.Messages, msg)
		case "tool":
			if m.ToolCallID == "" {
				return nil, invalidParam("messages", "tool messages need tool_call_id")
			}
			out.Messages = append(out.Messages, core.Message{Role: core.MsgTool, Kind: core.KindNormal, Provenance: core.ProvUser,
				Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: m.ToolCallID, Name: names[m.ToolCallID], Content: text}}}})
		default:
			return nil, invalidParam("messages", "unknown role %q", m.Role)
		}
	}
	return out, nil
}

func parseStop(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil || len(many) > 4 {
		return nil, invalidParam("stop", "stop must be a string or up to 4 strings")
	}
	return many, nil
}
