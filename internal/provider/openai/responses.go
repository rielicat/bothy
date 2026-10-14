package openai

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// Responses is the Responses API client (POST /responses, streamed); it
// implements core.Provider. Requests are stateless (store: false): every
// call carries the whole history, and reasoning survives a tool loop as the
// encrypted reasoning items the previous replies returned, replayed in their
// original order with the items that followed them.
type Responses struct {
	o           wire.Options
	noReasoning func(model string) bool

	mu sync.Mutex
	// staleBefore is when the server last refused to verify replayed
	// encrypted reasoning: it is bound to the organization that wrote it,
	// so a key from another organization or another endpoint rejects every
	// request that carries it. Reasoning in messages created before then
	// is not replayed again.
	staleBefore time.Time
	// plain holds models that refused encrypted content because they do not
	// reason (and that the catalog did not know).
	plain map[string]bool
}

var _ core.Provider = (*Responses)(nil)

// NewResponses returns a Responses client; o.Kind is KindOpenAI or
// KindOpenAICompat. noReasoning (optional) reports models known not to
// reason, which are not asked for encrypted reasoning.
func NewResponses(o wire.Options, noReasoning func(model string) bool) *Responses {
	return &Responses{o: withDefaults(o), noReasoning: noReasoning, plain: map[string]bool{}}
}

// maxPlainModels bounds Responses.plain; model names come from config and
// discovery.
const maxPlainModels = 64

func (c *Responses) ID() string              { return c.o.ID }
func (c *Responses) Kind() core.ProviderKind { return c.o.Kind }

// SigKind is the kind recorded on the parts this client writes.
func (c *Responses) SigKind() core.ProviderKind { return SigResponses }

func (c *Responses) Models(ctx context.Context) ([]core.ModelInfo, error) { return models(ctx, &c.o) }

func (c *Responses) Discover(ctx context.Context) ([]core.ModelInfo, error) {
	return discover(ctx, &c.o)
}

// Stream makes one Responses call. A request the server refuses, before any
// event, for the encrypted reasoning it asked for or carried back is sent
// once more without it, and the client remembers why (encryptedRefusal):
// like the Chat Completions reasoning fallback this is a capability
// downgrade of an unbilled 400, not a retry of a failed call.
func (c *Responses) Stream(ctx context.Context, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	started := false
	track := func(ev core.StreamEvent) error {
		started = true
		if on == nil {
			return nil
		}
		return on(ev)
	}
	body := c.buildRequest(r)
	resp, err := c.send(ctx, body, r, track)
	if err == nil || started {
		return resp, err
	}
	switch encryptedRefusal(err) {
	case refusedByModel:
		if len(body.Include) == 0 && !replaysReasoning(body) {
			return resp, err
		}
		c.markPlain(r.Model)
	case refusedStale:
		if !replaysReasoning(body) {
			return resp, err
		}
		c.markStale()
	default:
		return resp, err
	}
	return c.send(ctx, c.buildRequest(r), r, track)
}

func (c *Responses) send(ctx context.Context, body respRequest, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	req, err := c.o.NewRequest(ctx, http.MethodPost, c.o.URL("/responses"), body, setAuth)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.o.Do(ctx, req, classify)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return c.read(ctx, resp.Body, r, on)
}

// respRequest has no stop sequences: the Responses API has none, so
// Request.Stop is not sent.
type respRequest struct {
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions,omitempty"`
	Input           []any      `json:"input"`
	Tools           []respTool `json:"tools,omitempty"`
	ToolChoice      string     `json:"tool_choice,omitempty"`
	MaxOutputTokens int        `json:"max_output_tokens,omitempty"`
	Temperature     *float64   `json:"temperature,omitempty"`
	Store           bool       `json:"store"`
	Include         []string   `json:"include,omitempty"`
	Stream          bool       `json:"stream"`
}

// respTool always sends "strict": false. When it is omitted the API tries to
// coerce the schema into strict mode (optional fields become required and
// nullable), which Chat Completions never did and tool schemas are not
// written for.
type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

// easyMessage is the documented {"role","content"} input form.
type easyMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // []any of input parts, or a string for assistant text
	Phase   string `json:"phase,omitempty"`
}

type inputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type inputImage struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
	Detail   string `json:"detail"`
}

// outputMessage replays an assistant message item under its own id.
type outputMessage struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Role    string `json:"role"`
	Status  string `json:"status"`
	Content []any  `json:"content"`
	Phase   string `json:"phase,omitempty"`
}

type outputText struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
	Logprobs    []any  `json:"logprobs"`
}

type outputRefusal struct {
	Type    string `json:"type"`
	Refusal string `json:"refusal"`
}

type functionCallItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type functionOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// messageMeta is what a text part's Sig keeps of its message item; the text
// itself stays in Part.Text, which the agent redacts before every call.
type messageMeta struct {
	ID      string `json:"id,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Refusal bool   `json:"refusal,omitempty"`
}

const minOutputTokens = 16 // the API's minimum for max_output_tokens

// buildRequest renders the request so that everything before the newest
// turn is byte-identical to the previous call: instructions and tools come
// from the frozen system and tools blocks, and every history item is
// rendered from that message alone.
func (c *Responses) buildRequest(r *core.Request) respRequest {
	out := respRequest{
		Model:        r.Model,
		Instructions: wire.SystemText(r.System),
		Input:        []any{},
		Temperature:  r.Temperature,
		Stream:       true,
	}
	if r.MaxTokens > 0 {
		out.MaxOutputTokens = max(minOutputTokens, r.MaxTokens)
	}
	c.mu.Lock()
	plain, stale := c.plain[r.Model], c.staleBefore
	c.mu.Unlock()
	if !plain && (c.noReasoning == nil || !c.noReasoning(r.Model)) {
		out.Include = []string{"reasoning.encrypted_content"}
	}
	for _, t := range r.Tools {
		params := t.Schema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object"}`)
		}
		out.Tools = append(out.Tools, respTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params})
	}
	if len(out.Tools) > 0 {
		switch r.ToolChoice {
		case "none", "required":
			out.ToolChoice = r.ToolChoice
		default:
			out.ToolChoice = "auto"
		}
	}
	for _, m := range r.Messages {
		switch m.Role {
		case core.MsgTool:
			for _, p := range m.Parts {
				if p.Type == core.PartToolResult && p.ToolResult != nil {
					out.Input = append(out.Input, functionOutputItem{Type: "function_call_output", CallID: callID(p.ToolResult.CallID), Output: p.ToolResult.Content})
				}
			}
		case core.MsgAssistant:
			// A zero CreatedAt cannot be shown to be newer, so it counts as
			// stale: leaving reasoning out is always accepted.
			replay := !plain && (stale.IsZero() || m.CreatedAt.After(stale))
			out.Input = append(out.Input, c.assistantItems(m.Parts, replay)...)
		default:
			if parts := inputParts(m.Parts); len(parts) > 0 {
				out.Input = append(out.Input, easyMessage{Role: "user", Content: parts})
			}
		}
	}
	return out
}

// assistantItems turns one assistant message back into the items it came
// from, in their original order; replay false leaves its reasoning out.
func (c *Responses) assistantItems(parts []core.Part, replay bool) []any {
	// Under store: false an item id refers to nothing stored; it only pairs
	// an item with the reasoning item before it. When a reasoning item
	// cannot be replayed (the server sent no encrypted content) the ids are
	// left out, since the API rejects a reasoning item without its
	// following item and is assumed to reject the reverse too.
	ids := true
	for _, p := range parts {
		if p.Type == core.PartThinking && p.SigKind == SigResponses {
			if _, ok := c.reasoningItem(p); !ok || !replay {
				ids = false
			}
		}
	}
	var out []any
	for _, p := range parts {
		switch p.Type {
		case core.PartThinking:
			if item, ok := c.reasoningItem(p); ok && replay {
				out = append(out, item)
			}
		case core.PartText:
			if p.Text == "" {
				continue
			}
			var meta messageMeta
			if v, ok := c.ownSig(p); ok {
				_ = json.Unmarshal([]byte(v), &meta)
			}
			if meta.ID == "" || !ids {
				out = append(out, easyMessage{Role: "assistant", Content: p.Text, Phase: meta.Phase})
				continue
			}
			var content any = outputText{Type: "output_text", Text: p.Text, Annotations: []any{}, Logprobs: []any{}}
			if meta.Refusal {
				content = outputRefusal{Type: "refusal", Refusal: p.Text}
			}
			out = append(out, outputMessage{Type: "message", ID: meta.ID, Role: "assistant", Status: "completed", Content: []any{content}, Phase: meta.Phase})
		case core.PartToolCall:
			if p.ToolCall == nil {
				continue
			}
			args := string(p.ToolCall.Args)
			if args == "" {
				args = "{}"
			}
			item := functionCallItem{Type: "function_call", CallID: callID(p.ToolCall.ID), Name: p.ToolCall.Name, Arguments: args}
			if v, ok := c.ownSig(p); ok && ids {
				item.ID = v
			}
			out = append(out, item)
		}
	}
	// Reasoning that nothing of its own reply follows was cut off with the
	// item after it (a call dropped at the output limit, an empty message).
	// The API rejects a reasoning item sent without its following item
	// ("provided without its required following item"), and since the whole
	// history goes back every turn, that would fail every later call of the
	// session; reasoning that led nowhere is not needed.
	for len(out) > 0 {
		if _, ok := out[len(out)-1].(json.RawMessage); !ok {
			break
		}
		out = out[:len(out)-1]
	}
	return out
}

// replaysReasoning reports whether a request carries reasoning items back
// (they are the only raw items in the input).
func replaysReasoning(body respRequest) bool {
	for _, it := range body.Input {
		if _, ok := it.(json.RawMessage); ok {
			return true
		}
	}
	return false
}

type encryptedReason uint8

const (
	refusedByModel encryptedReason = iota + 1
	refusedStale
)

// encryptedRefusal recognises the 400s about encrypted reasoning:
//
//	Encrypted content is not supported with this model.
//
// for a model that does not reason, and (code invalid_encrypted_content)
//
//	The encrypted content for item rs_… could not be verified. Reason:
//	Encrypted content organization_id did not match the target organization.
//
// for reasoning written under another organization or endpoint, or no
// longer readable. Only the key terms are matched; the wording is not
// documented.
func encryptedRefusal(err error) encryptedReason {
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest {
		return 0
	}
	m := strings.ToLower(pe.Msg)
	if !strings.Contains(m, "encrypted content") && !strings.Contains(m, "encrypted_content") {
		return 0
	}
	if strings.Contains(m, "not supported") || strings.Contains(m, "unsupported") {
		return refusedByModel
	}
	return refusedStale
}

func (c *Responses) markPlain(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.plain) >= maxPlainModels {
		clear(c.plain)
	}
	c.plain[model] = true
}

func (c *Responses) markStale() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.staleBefore = time.Now()
}

// reasoningItem returns the stored reasoning item of a thinking part this
// provider wrote; it is sent back verbatim.
func (c *Responses) reasoningItem(p core.Part) (json.RawMessage, bool) {
	v, ok := c.ownSig(p)
	if !ok || !strings.HasPrefix(v, "{") || !json.Valid([]byte(v)) {
		return nil, false
	}
	return json.RawMessage(v), true
}

// sign scopes a signature to this provider: item ids and encrypted
// reasoning mean nothing to another endpoint, even one with the same API.
func (c *Responses) sign(v string) string { return c.o.ID + ":" + v }

// ownSig returns a part's signature when this provider wrote it. Provider
// ids never contain ':'.
func (c *Responses) ownSig(p core.Part) (string, bool) {
	if p.SigKind != SigResponses || p.Sig == "" {
		return "", false
	}
	id, v, ok := strings.Cut(p.Sig, ":")
	if !ok || id != c.o.ID || v == "" {
		return "", false
	}
	return v, true
}

func inputParts(ps []core.Part) []any {
	var out []any
	for _, p := range ps {
		switch p.Type {
		case core.PartText, core.PartContext:
			if p.Text != "" {
				out = append(out, inputText{Type: "input_text", Text: p.Text})
			}
		case core.PartImage:
			if wire.IsImage(p) {
				out = append(out, inputImage{Type: "input_image", ImageURL: "data:" + p.Blob.MIME + ";base64," + base64.StdEncoding.EncodeToString(p.Blob.Data), Detail: "auto"})
			} else {
				out = append(out, inputText{Type: "input_text", Text: wire.FileNote(p.Blob)})
			}
		case core.PartFile:
			out = append(out, inputText{Type: "input_text", Text: wire.FileNote(p.Blob)})
		}
	}
	return out
}

// maxCallID is the API's limit on call_id.
const maxCallID = 64

// callID keeps a tool call id within the API's limit. Ids minted by other
// vendors (after a fallback) can be longer; the same hash is applied to the
// call and to its output, so they stay paired and every turn renders the
// same bytes.
func callID(id string) string {
	if len(id) <= maxCallID {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "call_" + hex.EncodeToString(sum[:20])
}
