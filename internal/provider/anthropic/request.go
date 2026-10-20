package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// RedactedPrefix marks a Part.Sig that holds a redacted_thinking block's data
// rather than a thinking signature. Signatures are base64, so ':' never
// starts one.
const RedactedPrefix = "redacted_thinking:"

// maxBreakpoints is the API's limit on cache_control markers per request.
const maxBreakpoints = 4

type request struct {
	Model         string      `json:"model"`
	MaxTokens     int         `json:"max_tokens"`
	System        []block     `json:"system,omitempty"`
	Messages      []message   `json:"messages"`
	Tools         []tool      `json:"tools,omitempty"`
	ToolChoice    *toolChoice `json:"tool_choice,omitempty"`
	StopSequences []string    `json:"stop_sequences,omitempty"`
	Temperature   *float64    `json:"temperature,omitempty"`
	Stream        bool        `json:"stream"`
}

type cacheControl struct {
	Type string `json:"type"`
}

var ephemeral = &cacheControl{Type: "ephemeral"}

type block struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Source       *imageSource    `json:"source,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      *string         `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Thinking     *string         `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

type tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type toolChoice struct {
	Type string `json:"type"`
}

func buildRequest(r *core.Request) request {
	out := request{
		Model:         r.Model,
		MaxTokens:     r.MaxTokens,
		StopSequences: r.Stop,
		Temperature:   r.Temperature,
		Stream:        true,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = 4096
	}
	for _, s := range r.System {
		if s.Text == "" {
			continue
		}
		b := block{Type: "text", Text: s.Text}
		if s.CacheBreak {
			b.CacheControl = ephemeral
		}
		out.System = append(out.System, b)
	}
	for _, t := range r.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out.Tools = append(out.Tools, tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	if r.ToolsCache && len(out.Tools) > 0 {
		out.Tools[len(out.Tools)-1].CacheControl = ephemeral
	}
	switch r.ToolChoice {
	case "none":
		out.ToolChoice = &toolChoice{Type: "none"}
	case "required":
		out.ToolChoice = &toolChoice{Type: "any"}
	}
	shared := r.Messages
	if r.CacheTailSkipLast && len(shared) > 0 {
		shared = shared[:len(shared)-1]
	}
	out.Messages = appendMessages(nil, shared)
	if r.CacheTail && len(out.Messages) > 0 {
		last := &out.Messages[len(out.Messages)-1]
		if n := len(last.Content); n > 0 {
			last.Content[n-1].CacheControl = ephemeral
		}
	}
	out.Messages = appendMessages(out.Messages, r.Messages[len(shared):])
	limitBreakpoints(&out)
	return out
}

// limitBreakpoints drops the earliest system breakpoints when the request
// would exceed the API's limit; the tools and rolling-tail markers save the
// most input on later calls.
func limitBreakpoints(r *request) {
	n := 0
	for _, t := range r.Tools {
		if t.CacheControl != nil {
			n++
		}
	}
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	for i := range r.System {
		if r.System[i].CacheControl != nil {
			n++
		}
	}
	for i := 0; n > maxBreakpoints && i < len(r.System); i++ {
		if r.System[i].CacheControl != nil {
			r.System[i].CacheControl = nil
			n--
		}
	}
}

// appendMessages adds msgs to out, merging consecutive same-role messages
// into one turn. Building in two passes gives the same turns as one, so the
// rolling breakpoint can go on the shared messages before a call's own.
func appendMessages(out []message, msgs []core.Message) []message {
	for _, m := range msgs {
		role := "user"
		if m.Role == core.MsgAssistant {
			role = "assistant"
		}
		var blocks []block
		for _, p := range m.Parts {
			if b, ok := partBlock(p, role); ok {
				blocks = append(blocks, b)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			continue
		}
		out = append(out, message{Role: role, Content: blocks})
	}
	for i := range out {
		if out[i].Role == "user" {
			toolResultsFirst(out[i].Content)
		}
	}
	return out
}

// toolResultsFirst moves tool_result blocks ahead of other content: the API
// requires them first in the user message that answers a tool call.
func toolResultsFirst(bs []block) {
	results := make([]block, 0, len(bs))
	rest := make([]block, 0, len(bs))
	for _, b := range bs {
		if b.Type == "tool_result" {
			results = append(results, b)
		} else {
			rest = append(rest, b)
		}
	}
	copy(bs, append(results, rest...))
}

func partBlock(p core.Part, role string) (block, bool) {
	switch p.Type {
	case core.PartText, core.PartContext:
		if strings.TrimSpace(p.Text) == "" {
			return block{}, false
		}
		return block{Type: "text", Text: p.Text}, true
	case core.PartImage:
		if role == "user" && wire.IsImage(p) {
			return block{Type: "image", Source: &imageSource{Type: "base64", MediaType: p.Blob.MIME, Data: base64.StdEncoding.EncodeToString(p.Blob.Data)}}, true
		}
		return block{Type: "text", Text: wire.FileNote(p.Blob)}, true
	case core.PartFile:
		return block{Type: "text", Text: wire.FileNote(p.Blob)}, true
	case core.PartThinking:
		if role != "assistant" || p.SigKind != core.KindAnthropic || p.Sig == "" {
			return block{}, false
		}
		if data, ok := strings.CutPrefix(p.Sig, RedactedPrefix); ok {
			return block{Type: "redacted_thinking", Data: data}, true
		}
		text := p.Text
		return block{Type: "thinking", Thinking: &text, Signature: p.Sig}, true
	case core.PartToolCall:
		if role != "assistant" || p.ToolCall == nil {
			return block{}, false
		}
		input := p.ToolCall.Args
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		return block{Type: "tool_use", ID: p.ToolCall.ID, Name: p.ToolCall.Name, Input: input}, true
	case core.PartToolResult:
		if p.ToolResult == nil {
			return block{}, false
		}
		content := p.ToolResult.Content
		return block{Type: "tool_result", ToolUseID: p.ToolResult.CallID, Content: &content, IsError: p.ToolResult.IsError}, true
	}
	return block{}, false
}
