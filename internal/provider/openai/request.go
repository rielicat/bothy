package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

type request struct {
	Model               string         `json:"model"`
	Messages            []message      `json:"messages"`
	Tools               []tool         `json:"tools,omitempty"`
	ToolChoice          string         `json:"tool_choice,omitempty"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	Stop                []string       `json:"stop,omitempty"`
	Stream              bool           `json:"stream"`
	StreamOptions       *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // string, []contentPart or nil
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type tool struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (c *Client) buildRequest(r *core.Request) request {
	out := request{
		Model:         r.Model,
		Temperature:   r.Temperature,
		Stop:          r.Stop,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	// OpenAI deprecated max_tokens and rejects it on reasoning models;
	// OpenAI-compatible servers mostly still only know max_tokens.
	if c.o.Kind == core.KindOpenAI {
		out.MaxCompletionTokens = r.MaxTokens
	} else {
		out.MaxTokens = r.MaxTokens
	}
	for _, t := range r.Tools {
		params := t.Schema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object"}`)
		}
		out.Tools = append(out.Tools, tool{Type: "function", Function: functionDef{Name: t.Name, Description: t.Description, Parameters: params}})
	}
	if len(out.Tools) > 0 {
		switch r.ToolChoice {
		case "none", "required":
			out.ToolChoice = r.ToolChoice
		default:
			out.ToolChoice = "auto"
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
					out = append(out, message{Role: "tool", ToolCallID: p.ToolResult.CallID, Content: p.ToolResult.Content})
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
				case core.PartToolCall:
					if p.ToolCall == nil {
						continue
					}
					args := string(p.ToolCall.Args)
					if args == "" {
						args = "{}"
					}
					am.ToolCalls = append(am.ToolCalls, toolCall{ID: p.ToolCall.ID, Type: "function", Function: functionCall{Name: p.ToolCall.Name, Arguments: args}})
				}
			}
			if len(text) > 0 {
				am.Content = strings.Join(text, "\n")
			}
			if am.Content == nil && len(am.ToolCalls) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == "assistant" && len(out[n-1].ToolCalls) == 0 {
				prev := &out[n-1]
				prev.Content = joinContent(prev.Content, am.Content)
				prev.ToolCalls = am.ToolCalls
				continue
			}
			out = append(out, am)
		default:
			parts := userParts(m.Parts)
			if len(parts) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == "user" {
				out[n-1].Content = mergeUser(out[n-1].Content, parts)
				continue
			}
			out = append(out, message{Role: "user", Content: userContent(parts)})
		}
	}
	return out
}

func userParts(ps []core.Part) []contentPart {
	var out []contentPart
	for _, p := range ps {
		switch p.Type {
		case core.PartText, core.PartContext:
			if p.Text != "" {
				out = append(out, contentPart{Type: "text", Text: p.Text})
			}
		case core.PartImage:
			if wire.IsImage(p) {
				out = append(out, contentPart{Type: "image_url", ImageURL: &imageURL{URL: "data:" + p.Blob.MIME + ";base64," + base64.StdEncoding.EncodeToString(p.Blob.Data)}})
			} else {
				out = append(out, contentPart{Type: "text", Text: wire.FileNote(p.Blob)})
			}
		case core.PartFile:
			out = append(out, contentPart{Type: "text", Text: wire.FileNote(p.Blob)})
		}
	}
	return out
}

// userContent uses the plain string form unless an image needs parts: many
// OpenAI-compatible servers accept only strings.
func userContent(ps []contentPart) any {
	for _, p := range ps {
		if p.Type != "text" {
			return ps
		}
	}
	texts := make([]string, len(ps))
	for i, p := range ps {
		texts[i] = p.Text
	}
	return strings.Join(texts, "\n\n")
}

func mergeUser(prev any, add []contentPart) any {
	var ps []contentPart
	switch x := prev.(type) {
	case string:
		ps = append(ps, contentPart{Type: "text", Text: x})
	case []contentPart:
		ps = append(ps, x...)
	}
	return userContent(append(ps, add...))
}

func joinContent(a, b any) any {
	as, _ := a.(string)
	bs, _ := b.(string)
	switch {
	case as == "":
		return b
	case bs == "":
		return a
	}
	return as + "\n" + bs
}
