package mockllm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type responsesRequest struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions"`
	Input        json.RawMessage `json:"input"` // a string or a list of items
	Tools        json.RawMessage `json:"tools"`
	Stream       bool            `json:"stream"`
}

type inputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

// items returns the input as a list; a plain string is one user message.
func (r *responsesRequest) items() ([]inputItem, error) {
	var s string
	if json.Unmarshal(r.Input, &s) == nil {
		c, _ := json.Marshal(s)
		return []inputItem{{Role: "user", Content: c}}, nil
	}
	var items []inputItem
	if len(r.Input) == 0 {
		return nil, nil
	}
	return items, json.Unmarshal(r.Input, &items)
}

func analyzeResponses(req *responsesRequest, items []inputItem) turn {
	var t turn
	system := []string{}
	if req.Instructions != "" {
		system = append(system, req.Instructions)
	}
	lastUser := -1
	for i, it := range items {
		if it.Type != "" && it.Type != "message" {
			continue
		}
		switch it.Role {
		case "system", "developer":
			system = append(system, contentText(it.Content))
		case "user":
			lastUser = i
		}
	}
	t.system = strings.Join(system, "\n")
	if lastUser >= 0 {
		t.user = contentText(items[lastUser].Content)
	}
	for _, it := range items[lastUser+1:] {
		if it.Type == "function_call_output" {
			t.toolResult, t.hasToolResult = contentText(it.Output), true
		}
	}
	return t
}

func estimateResponses(req *responsesRequest, items []inputItem) int {
	n := len(req.Tools) + len(req.Instructions)
	for _, it := range items {
		n += len(contentText(it.Content)) + len(it.Arguments) + len(contentText(it.Output))
	}
	return (n + 3) / 4
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "", "mockllm: request body is not valid JSON: "+err.Error())
		return
	}
	items, err := req.items()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_input", "input", "mockllm: input is neither a string nor a list of items: "+err.Error())
		return
	}
	info, ok := s.model(req.Model)
	if !ok {
		writeError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", "model", fmt.Sprintf("The model `%s` does not exist", req.Model))
		return
	}
	est := estimateResponses(&req, items)
	rule, ok := s.begin(w, r, info, est, analyzeResponses(&req, items))
	if !ok {
		return
	}
	p := s.plan(rule, req.Model, est)
	if req.Stream {
		s.streamResponses(w, r, p, rule)
	} else {
		s.completeResponse(w, r, p, rule)
	}
}

// response is the Responses API's response object, with the fields clients
// read.
type response struct {
	ID                string           `json:"id"`
	Object            string           `json:"object"`
	CreatedAt         int64            `json:"created_at"`
	Status            string           `json:"status"`
	Error             any              `json:"error"`
	IncompleteDetails any              `json:"incomplete_details"`
	Model             string           `json:"model"`
	Output            []map[string]any `json:"output"`
	Store             bool             `json:"store"`
	Usage             *responsesUsage  `json:"usage"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int `json:"total_tokens"`
}

func (p planned) responseObject(status string, output []map[string]any) response {
	res := response{ID: "resp_mock" + strconv.Itoa(p.seq), Object: "response", CreatedAt: createdAt, Status: status,
		Model: p.model, Output: output}
	if output == nil {
		res.Output = []map[string]any{}
	}
	if status == "completed" && p.u != nil {
		// input_tokens includes cached and cache-write tokens; output_tokens
		// includes reasoning.
		u := &responsesUsage{InputTokens: p.u.Input + p.u.CacheRead + p.u.CacheWrite, OutputTokens: p.u.Output}
		u.InputTokensDetails.CachedTokens, u.InputTokensDetails.CacheWriteTokens = p.u.CacheRead, p.u.CacheWrite
		u.OutputTokensDetails.ReasoningTokens = p.u.Reasoning
		u.TotalTokens = u.InputTokens + u.OutputTokens
		res.Usage = u
	}
	return res
}

// outputItems returns each output item as streamed (added, then the events
// for its content) and as finished.
func (p planned) outputItems() (added, done []map[string]any) {
	n := strconv.Itoa(p.seq)
	if p.reasoning != "" {
		added = append(added, map[string]any{"id": "rs_mock" + n, "type": "reasoning", "summary": []any{}})
		done = append(done, map[string]any{"id": "rs_mock" + n, "type": "reasoning",
			"summary":           []any{map[string]any{"type": "summary_text", "text": p.reasoning}},
			"encrypted_content": "mockenc_" + base64.RawURLEncoding.EncodeToString([]byte(p.reasoning))})
	}
	if text := strings.Join(p.text, ""); text != "" {
		phase := "final_answer"
		if len(p.calls) > 0 {
			phase = "commentary"
		}
		id := "msg_mock" + n
		added = append(added, map[string]any{"id": id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}, "phase": phase})
		done = append(done, map[string]any{"id": id, "type": "message", "status": "completed", "role": "assistant", "phase": phase,
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}}})
	}
	for i, c := range p.calls {
		id := "fc_mock" + n + "_" + strconv.Itoa(i)
		added = append(added, map[string]any{"id": id, "type": "function_call", "call_id": c.id, "name": c.name, "arguments": "", "status": "in_progress"})
		done = append(done, map[string]any{"id": id, "type": "function_call", "call_id": c.id, "name": c.name, "arguments": c.args, "status": "completed"})
	}
	return added, done
}

type sseEvent struct {
	typ  string
	data []byte
}

func (p planned) responseEvents() []sseEvent {
	var out []sseEvent
	add := func(fields map[string]any) {
		fields["sequence_number"] = len(out)
		b, _ := json.Marshal(fields)
		out = append(out, sseEvent{typ: fields["type"].(string), data: b})
	}
	add(map[string]any{"type": "response.created", "response": p.responseObject("in_progress", nil)})
	add(map[string]any{"type": "response.in_progress", "response": p.responseObject("in_progress", nil)})
	added, done := p.outputItems()
	for i := range added {
		id := added[i]["id"]
		add(map[string]any{"type": "response.output_item.added", "output_index": i, "item": added[i]})
		switch added[i]["type"] {
		case "reasoning":
			part := map[string]any{"type": "summary_text", "text": ""}
			add(map[string]any{"type": "response.reasoning_summary_part.added", "item_id": id, "output_index": i, "summary_index": 0, "part": part})
			add(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": id, "output_index": i, "summary_index": 0, "delta": p.reasoning})
			add(map[string]any{"type": "response.reasoning_summary_text.done", "item_id": id, "output_index": i, "summary_index": 0, "text": p.reasoning})
			add(map[string]any{"type": "response.reasoning_summary_part.done", "item_id": id, "output_index": i, "summary_index": 0,
				"part": map[string]any{"type": "summary_text", "text": p.reasoning}})
		case "message":
			text := strings.Join(p.text, "")
			add(map[string]any{"type": "response.content_part.added", "item_id": id, "output_index": i, "content_index": 0,
				"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}})
			for _, t := range p.text {
				add(map[string]any{"type": "response.output_text.delta", "item_id": id, "output_index": i, "content_index": 0, "delta": t, "logprobs": []any{}})
			}
			add(map[string]any{"type": "response.output_text.done", "item_id": id, "output_index": i, "content_index": 0, "text": text, "logprobs": []any{}})
			add(map[string]any{"type": "response.content_part.done", "item_id": id, "output_index": i, "content_index": 0,
				"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}})
		case "function_call":
			args := done[i]["arguments"].(string)
			for _, piece := range split3(args) {
				add(map[string]any{"type": "response.function_call_arguments.delta", "item_id": id, "output_index": i, "delta": piece})
			}
			add(map[string]any{"type": "response.function_call_arguments.done", "item_id": id, "output_index": i, "arguments": args})
		}
		add(map[string]any{"type": "response.output_item.done", "output_index": i, "item": done[i]})
	}
	add(map[string]any{"type": "response.completed", "response": p.responseObject("completed", done)})
	return out
}

func (s *Server) streamResponses(w http.ResponseWriter, r *http.Request, p planned, rule Rule) {
	events := p.responseEvents()
	cut, garble := len(events), -1
	switch rule.Fault {
	case FaultMidstream:
		cut = max(1, len(events)/2)
	case FaultMalformed:
		garble = len(events) / 2
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	for i, ev := range events[:cut] {
		if i > 0 && rule.Delay > 0 && !s.sleep(r, rule.Delay) {
			return
		}
		data := ev.data
		if i == garble {
			data = data[:len(data)/2]
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.typ, data); err != nil {
			return
		}
		_ = rc.Flush()
	}
	if rule.Fault == FaultMidstream {
		panic(http.ErrAbortHandler) // drops the connection before response.completed
	}
}

func (s *Server) completeResponse(w http.ResponseWriter, r *http.Request, p planned, rule Rule) {
	_, done := p.outputItems()
	body, _ := json.Marshal(p.responseObject("completed", done))
	if rule.Delay > 0 && !s.sleep(r, rule.Delay) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch rule.Fault {
	case FaultMalformed:
		body = body[:len(body)/2]
	case FaultMidstream:
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:len(body)/2])
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
