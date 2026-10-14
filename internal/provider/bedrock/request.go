package bedrock

import (
	"encoding/json"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// RedactedPrefix marks a Part.Sig holding a reasoningContent.redactedContent
// blob (base64) rather than a reasoning signature. Signatures are base64, so
// ':' never starts one.
const RedactedPrefix = "redacted:"

// maxCachePoints is Bedrock's limit on cachePoint blocks per request.
const maxCachePoints = 4

// emptyToolResult stands in for a tool result without output: Converse
// rejects a blank text block.
const emptyToolResult = "(no output)"

type request struct {
	Messages        []message        `json:"messages"`
	System          []systemBlock    `json:"system,omitempty"`
	InferenceConfig *inferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *toolConfig      `json:"toolConfig,omitempty"`
	// AdditionalModelRequestFields passes model-native parameters (Claude's
	// thinking configuration); nil leaves the model's defaults.
	AdditionalModelRequestFields json.RawMessage `json:"additionalModelRequestFields,omitempty"`
}

type cachePoint struct {
	Type string `json:"type"`
}

var defaultCachePoint = &cachePoint{Type: "default"}

type systemBlock struct {
	Text       string      `json:"text,omitempty"`
	CachePoint *cachePoint `json:"cachePoint,omitempty"`
}

type inferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type toolConfig struct {
	Tools      []toolEntry `json:"tools"`
	ToolChoice *toolChoice `json:"toolChoice,omitempty"`
}

type toolEntry struct {
	ToolSpec   *toolSpec   `json:"toolSpec,omitempty"`
	CachePoint *cachePoint `json:"cachePoint,omitempty"`
}

type toolSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	JSON json.RawMessage `json:"json"`
}

type toolChoice struct {
	Any *struct{} `json:"any,omitempty"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

type block struct {
	Text             string            `json:"text,omitempty"`
	Image            *imageBlock       `json:"image,omitempty"`
	ToolUse          *toolUse          `json:"toolUse,omitempty"`
	ToolResult       *toolResult       `json:"toolResult,omitempty"`
	ReasoningContent *reasoningContent `json:"reasoningContent,omitempty"`
	CachePoint       *cachePoint       `json:"cachePoint,omitempty"`
}

type imageBlock struct {
	Format string      `json:"format"`
	Source imageSource `json:"source"`
}

type imageSource struct {
	Bytes []byte `json:"bytes"` // base64 in JSON, as the REST protocol encodes blobs
}

type toolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type toolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Content   []toolResultContent `json:"content"`
	Status    string              `json:"status,omitempty"`
}

type toolResultContent struct {
	Text string `json:"text"`
}

type reasoningContent struct {
	ReasoningText   *reasoningText `json:"reasoningText,omitempty"`
	RedactedContent string         `json:"redactedContent,omitempty"`
}

type reasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

func buildRequest(r *core.Request) request {
	cache := SupportsCache(r.Model)
	toolHistory := hasToolBlocks(r.Messages)
	out := request{}
	for _, s := range r.System {
		if strings.TrimSpace(s.Text) == "" {
			continue
		}
		out.System = append(out.System, systemBlock{Text: s.Text})
		if s.CacheBreak && cache {
			out.System = append(out.System, systemBlock{CachePoint: defaultCachePoint})
		}
	}
	ic := inferenceConfig{MaxTokens: r.MaxTokens, Temperature: r.Temperature, StopSequences: r.Stop}
	if ic.MaxTokens > 0 || ic.Temperature != nil || len(ic.StopSequences) > 0 {
		out.InferenceConfig = &ic
	}

	// Converse has no "none" choice; leaving the tools out says the same when
	// the history needs no tool definitions to be valid.
	withTools := len(r.Tools) > 0 && (r.ToolChoice != "none" || toolHistory)
	if withTools {
		tc := &toolConfig{}
		for _, t := range r.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tc.Tools = append(tc.Tools, toolEntry{ToolSpec: &toolSpec{Name: t.Name, Description: t.Description, InputSchema: inputSchema{JSON: schema}}})
		}
		if r.ToolsCache && supportsToolCache(r.Model) {
			tc.Tools = append(tc.Tools, toolEntry{CachePoint: defaultCachePoint})
		}
		if r.ToolChoice == "required" && !rejectsForcedTool(r.Model) {
			tc.ToolChoice = &toolChoice{Any: &struct{}{}}
		}
		out.ToolConfig = tc
	}
	out.Messages = buildMessages(r.Messages, withTools)
	if r.CacheTail && cache && len(out.Messages) > 0 {
		last := &out.Messages[len(out.Messages)-1]
		last.Content = append(last.Content, block{CachePoint: defaultCachePoint})
	}
	limitCachePoints(&out)
	return out
}

func hasToolBlocks(msgs []core.Message) bool {
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == core.PartToolCall || p.Type == core.PartToolResult {
				return true
			}
		}
	}
	return false
}

// limitCachePoints drops the earliest system cache points beyond Bedrock's
// limit; the tools and rolling-tail points save the most on later calls.
func limitCachePoints(r *request) {
	n := 0
	if r.ToolConfig != nil {
		for _, t := range r.ToolConfig.Tools {
			if t.CachePoint != nil {
				n++
			}
		}
	}
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b.CachePoint != nil {
				n++
			}
		}
	}
	for _, s := range r.System {
		if s.CachePoint != nil {
			n++
		}
	}
	if n <= maxCachePoints {
		return
	}
	kept := r.System[:0]
	for _, s := range r.System {
		if s.CachePoint != nil && n > maxCachePoints {
			n--
			continue
		}
		kept = append(kept, s)
	}
	r.System = kept
}

// buildMessages maps the history. Converse wants alternating roles, so
// consecutive same-role messages merge, and tool results lead the user
// message that answers a tool call. Without tool definitions (withTools
// false) tool calls and results become text, since Converse refuses toolUse
// blocks in a request that defines no tools.
func buildMessages(msgs []core.Message, withTools bool) []message {
	var out []message
	for _, m := range msgs {
		role := "user"
		if m.Role == core.MsgAssistant {
			role = "assistant"
		}
		var blocks []block
		for _, p := range m.Parts {
			if b, ok := partBlock(p, role, withTools); ok {
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

func toolResultsFirst(bs []block) {
	results := make([]block, 0, len(bs))
	rest := make([]block, 0, len(bs))
	for _, b := range bs {
		if b.ToolResult != nil {
			results = append(results, b)
		} else {
			rest = append(rest, b)
		}
	}
	copy(bs, append(results, rest...))
}

// imageFormats maps MIME types to Converse image formats.
var imageFormats = map[string]string{
	"image/png": "png", "image/jpeg": "jpeg", "image/jpg": "jpeg", "image/gif": "gif", "image/webp": "webp",
}

func partBlock(p core.Part, role string, withTools bool) (block, bool) {
	switch p.Type {
	case core.PartText, core.PartContext:
		if strings.TrimSpace(p.Text) == "" {
			return block{}, false
		}
		return block{Text: p.Text}, true
	case core.PartImage:
		if format, ok := imageFormats[strings.ToLower(mimeOf(p))]; ok && role == "user" && wire.IsImage(p) {
			return block{Image: &imageBlock{Format: format, Source: imageSource{Bytes: p.Blob.Data}}}, true
		}
		return block{Text: wire.FileNote(p.Blob)}, true
	case core.PartFile:
		return block{Text: wire.FileNote(p.Blob)}, true
	case core.PartThinking:
		if role != "assistant" || p.SigKind != core.KindBedrock || p.Sig == "" {
			return block{}, false
		}
		if data, ok := strings.CutPrefix(p.Sig, RedactedPrefix); ok {
			return block{ReasoningContent: &reasoningContent{RedactedContent: data}}, true
		}
		return block{ReasoningContent: &reasoningContent{ReasoningText: &reasoningText{Text: p.Text, Signature: p.Sig}}}, true
	case core.PartToolCall:
		if role != "assistant" || p.ToolCall == nil {
			return block{}, false
		}
		input := p.ToolCall.Args
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		if !withTools {
			return block{Text: "[called tool " + p.ToolCall.Name + " with " + string(input) + "]"}, true
		}
		return block{ToolUse: &toolUse{ToolUseID: p.ToolCall.ID, Name: p.ToolCall.Name, Input: input}}, true
	case core.PartToolResult:
		if p.ToolResult == nil {
			return block{}, false
		}
		content := p.ToolResult.Content
		if strings.TrimSpace(content) == "" {
			content = emptyToolResult
		}
		if !withTools {
			return block{Text: "[result of tool " + p.ToolResult.Name + "]\n" + content}, true
		}
		tr := &toolResult{ToolUseID: p.ToolResult.CallID, Content: []toolResultContent{{Text: content}}}
		if p.ToolResult.IsError {
			tr.Status = "error"
		}
		return block{ToolResult: tr}, true
	}
	return block{}, false
}

func mimeOf(p core.Part) string {
	if p.Blob == nil {
		return ""
	}
	return p.Blob.MIME
}
