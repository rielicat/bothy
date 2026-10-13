package gemini

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

type request struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Tools             []toolSet         `json:"tools,omitempty"`
	ToolConfig        *toolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
}

type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type toolSet struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

type functionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type toolConfig struct {
	FunctionCallingConfig struct {
		Mode string `json:"mode"`
	} `json:"functionCallingConfig"`
}

type generationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

func buildRequest(r *core.Request) request {
	out := request{}
	if sys := wire.SystemText(r.System); sys != "" {
		out.SystemInstruction = &content{Parts: []part{{Text: sys}}}
	}
	if len(r.Tools) > 0 {
		var decls []functionDeclaration
		for _, t := range r.Tools {
			decls = append(decls, functionDeclaration{Name: t.Name, Description: t.Description, ParametersJSONSchema: t.Schema})
		}
		out.Tools = []toolSet{{FunctionDeclarations: decls}}
		mode := "AUTO"
		switch r.ToolChoice {
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		}
		out.ToolConfig = &toolConfig{}
		out.ToolConfig.FunctionCallingConfig.Mode = mode
	}
	if r.MaxTokens > 0 || r.Temperature != nil || len(r.Stop) > 0 {
		out.GenerationConfig = &generationConfig{MaxOutputTokens: r.MaxTokens, Temperature: r.Temperature, StopSequences: r.Stop}
	}
	out.Contents = buildContents(r.Messages)
	return out
}

func buildContents(msgs []core.Message) []content {
	var out []content
	for _, m := range msgs {
		role := "user"
		if m.Role == core.MsgAssistant {
			role = "model"
		}
		var parts []part
		for _, p := range m.Parts {
			if gp, ok := toPart(p, role); ok {
				parts = append(parts, gp)
			}
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			continue
		}
		out = append(out, content{Role: role, Parts: parts})
	}
	markForeignCalls(out)
	return out
}

// markForeignCalls gives the first functionCall of every step since the last
// user message the skip sentinel when it has no signature: Gemini 3 rejects
// such a call there, and calls another vendor made before a fallback never
// have one. Earlier turns are not validated and are left alone.
func markForeignCalls(cs []content) {
	turn := 0
	for i, c := range cs {
		if c.Role == "user" && slices.ContainsFunc(c.Parts, func(p part) bool { return p.FunctionResponse == nil }) {
			turn = i + 1
		}
	}
	for _, c := range cs[turn:] {
		if c.Role != "model" {
			continue
		}
		for j := range c.Parts {
			if p := &c.Parts[j]; p.FunctionCall != nil {
				if p.ThoughtSignature == "" {
					p.ThoughtSignature = skipSignature
				}
				break
			}
		}
	}
}

// skipSignature is the thought signature Google documents for function
// calls the API did not generate (history from another model): Gemini 3
// then skips its validation instead of rejecting the request.
const skipSignature = "skip_thought_signature_validator"

// sig returns the part's thought signature when it came from Gemini; others
// were stripped by the facade, but a stray one must never be forwarded.
func sig(p core.Part) string {
	if p.SigKind == core.KindGemini {
		return p.Sig
	}
	return ""
}

// callID returns an id worth echoing: ids Bothy synthesized for calls that
// arrived without one would not match anything on Gemini's side.
func callID(id string) string {
	if wire.IsSynthesized(id) {
		return ""
	}
	return id
}

func toPart(p core.Part, role string) (part, bool) {
	switch p.Type {
	case core.PartText, core.PartContext:
		if strings.TrimSpace(p.Text) == "" && sig(p) == "" {
			return part{}, false
		}
		return part{Text: p.Text, ThoughtSignature: sig(p)}, true
	case core.PartThinking:
		if role != "model" || p.SigKind != core.KindGemini || (p.Text == "" && p.Sig == "") {
			return part{}, false
		}
		return part{Text: p.Text, Thought: p.Text != "", ThoughtSignature: p.Sig}, true
	case core.PartImage:
		if wire.IsImage(p) {
			return part{InlineData: &inlineData{MimeType: p.Blob.MIME, Data: base64.StdEncoding.EncodeToString(p.Blob.Data)}}, true
		}
		return part{Text: wire.FileNote(p.Blob)}, true
	case core.PartFile:
		return part{Text: wire.FileNote(p.Blob)}, true
	case core.PartToolCall:
		if role != "model" || p.ToolCall == nil {
			return part{}, false
		}
		args := p.ToolCall.Args
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		return part{FunctionCall: &functionCall{ID: callID(p.ToolCall.ID), Name: p.ToolCall.Name, Args: args}, ThoughtSignature: sig(p)}, true
	case core.PartToolResult:
		if p.ToolResult == nil {
			return part{}, false
		}
		key := "output"
		if p.ToolResult.IsError {
			key = "error"
		}
		return part{FunctionResponse: &functionResponse{
			ID: callID(p.ToolResult.CallID), Name: p.ToolResult.Name,
			Response: map[string]any{key: p.ToolResult.Content},
		}}, true
	}
	return part{}, false
}
