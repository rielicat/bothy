package ollama

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
)

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
	Options  *options  `json:"options,omitempty"`
}

type options struct {
	NumCtx      int      `json:"num_ctx,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"`
	Images    []string   `json:"images,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

type toolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func buildRequest(r *core.Request) chatRequest {
	out := chatRequest{Model: r.Model, Stream: true}
	if r.NumCtx > 0 || r.MaxTokens > 0 || r.Temperature != nil || len(r.Stop) > 0 {
		out.Options = &options{NumCtx: r.NumCtx, NumPredict: r.MaxTokens, Temperature: r.Temperature, Stop: r.Stop}
	}
	// Ollama has no tool_choice; "none" is honoured by not offering tools.
	if r.ToolChoice != "none" {
		for _, t := range r.Tools {
			var tl tool
			tl.Type = "function"
			tl.Function.Name, tl.Function.Description, tl.Function.Parameters = t.Name, t.Description, t.Schema
			if len(tl.Function.Parameters) == 0 {
				tl.Function.Parameters = json.RawMessage(`{"type":"object"}`)
			}
			out.Tools = append(out.Tools, tl)
		}
	}
	if sys := wire.SystemText(r.System); sys != "" {
		out.Messages = append(out.Messages, message{Role: "system", Content: sys})
	}
	out.Messages = append(out.Messages, buildMessages(r.Messages)...)
	return out
}

func buildMessages(msgs []core.Message) []message {
	var out []message
	for _, m := range msgs {
		switch m.Role {
		case core.MsgTool:
			for _, p := range m.Parts {
				if p.Type == core.PartToolResult && p.ToolResult != nil {
					out = append(out, message{Role: "tool", Content: p.ToolResult.Content, ToolName: p.ToolResult.Name})
				}
			}
		case core.MsgAssistant:
			am := message{Role: "assistant"}
			var text []string
			for _, p := range m.Parts {
				switch p.Type {
				case core.PartText:
					if p.Text != "" {
						text = append(text, p.Text)
					}
				case core.PartThinking:
					if p.SigKind == core.KindOllama {
						am.Thinking += p.Text
					}
				case core.PartToolCall:
					if p.ToolCall == nil {
						continue
					}
					var tc toolCall
					if !wire.IsSynthesized(p.ToolCall.ID) {
						tc.ID = p.ToolCall.ID
					}
					tc.Function.Name, tc.Function.Arguments = p.ToolCall.Name, p.ToolCall.Args
					if len(tc.Function.Arguments) == 0 {
						tc.Function.Arguments = json.RawMessage("{}")
					}
					am.ToolCalls = append(am.ToolCalls, tc)
				}
			}
			am.Content = strings.Join(text, "\n")
			if am.Content == "" && am.Thinking == "" && len(am.ToolCalls) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == "assistant" && len(out[n-1].ToolCalls) == 0 {
				prev := &out[n-1]
				prev.Content = joinText(prev.Content, am.Content)
				prev.Thinking += am.Thinking
				prev.ToolCalls = am.ToolCalls
				continue
			}
			out = append(out, am)
		default:
			um := message{Role: "user"}
			var text []string
			for _, p := range m.Parts {
				switch p.Type {
				case core.PartText, core.PartContext:
					if p.Text != "" {
						text = append(text, p.Text)
					}
				case core.PartImage:
					if wire.IsImage(p) {
						um.Images = append(um.Images, base64.StdEncoding.EncodeToString(p.Blob.Data))
					} else {
						text = append(text, wire.FileNote(p.Blob))
					}
				case core.PartFile:
					text = append(text, wire.FileNote(p.Blob))
				}
			}
			um.Content = strings.Join(text, "\n\n")
			if um.Content == "" && len(um.Images) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == "user" {
				out[n-1].Content = joinText(out[n-1].Content, um.Content)
				out[n-1].Images = append(out[n-1].Images, um.Images...)
				continue
			}
			out = append(out, um)
		}
	}
	return out
}

func joinText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}
