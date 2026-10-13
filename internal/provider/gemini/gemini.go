// Package gemini is the Gemini API client: streamGenerateContent with
// alt=sse, the key in the x-goog-api-key header (never the URL), function
// calls and thought signatures. Owner: M2.
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// DefaultBaseURL is used when the provider config has no base_url.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// Client implements core.Provider.
type Client struct{ o wire.Options }

var _ core.Provider = (*Client)(nil)

func New(o wire.Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	o.Kind = core.KindGemini
	return &Client{o: o}
}

func (c *Client) ID() string              { return c.o.ID }
func (c *Client) Kind() core.ProviderKind { return core.KindGemini }

func setAuth(h http.Header, key string) { h.Set("x-goog-api-key", key) }

// modelName is one model id segment; ':' separates the method in the URL.
var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// modelPath accepts "gemini-x", "models/gemini-x" and "tunedModels/x". The
// model is part of the URL path, which carries the key header, so anything
// else ("../files", "x?y", "x:method") is refused rather than escaped: a
// passthrough client must not reach other endpoints with the operator's key.
func modelPath(model string) (string, bool) {
	coll, name, ok := strings.Cut(model, "/")
	if !ok {
		coll, name = "models", model
	}
	if coll != "models" && coll != "tunedModels" || !modelName.MatchString(name) {
		return "", false
	}
	return "/" + coll + "/" + name, true
}

func (c *Client) Stream(ctx context.Context, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	path, ok := modelPath(r.Model)
	if !ok {
		return nil, &core.ProviderError{Provider: c.o.ID, Kind: core.ErrKindInvalidRequest, Msg: "invalid Gemini model name"}
	}
	u := c.o.URL(path + ":streamGenerateContent?alt=sse")
	req, err := c.o.NewRequest(ctx, http.MethodPost, u, buildRequest(r), setAuth)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.o.Do(ctx, req, classify)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return c.read(ctx, resp.Body, r.Model, on)
}

type response struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string `json:"text"`
				Thought      bool   `json:"thought"`
				FunctionCall *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
				ThoughtSignature string `json:"thoughtSignature"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		ToolUsePromptTokenCount int `json:"toolUsePromptTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string        `json:"modelVersion"`
	Error        *errorPayload `json:"error"`
}

func (c *Client) read(ctx context.Context, body io.Reader, model string, on func(core.StreamEvent) error) (*core.Response, error) {
	b := wire.NewBuilder(core.KindGemini, on)
	sse := wire.NewSSE(body)
	var (
		u      core.Usage
		seen   bool
		finish string
		chunks int
	)
	for {
		ev, err := sse.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, c.o.NetError(ctx, err)
		}
		if len(strings.TrimSpace(string(ev.Data))) == 0 {
			continue
		}
		var r response
		if err := json.Unmarshal(ev.Data, &r); err != nil {
			return nil, c.o.Malformed(err)
		}
		if r.Error != nil {
			kind, msg := r.Error.kind(r.Error.Code)
			return nil, c.o.StreamError(kind, msg)
		}
		chunks++
		if r.ModelVersion != "" {
			model = r.ModelVersion
		}
		if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
			return nil, c.o.StreamError(core.ErrKindContentFilter, "prompt blocked: "+r.PromptFeedback.BlockReason)
		}
		// usageMetadata is cumulative; the last chunk's counts are the totals.
		if m := r.UsageMetadata; m != nil {
			seen = true
			u = core.Usage{
				Input:     max(0, m.PromptTokenCount-m.CachedContentTokenCount) + m.ToolUsePromptTokenCount,
				CacheRead: m.CachedContentTokenCount,
				Output:    m.CandidatesTokenCount + m.ThoughtsTokenCount,
				Reasoning: m.ThoughtsTokenCount,
			}
		}
		if len(r.Candidates) == 0 {
			continue
		}
		cand := r.Candidates[0]
		for _, p := range cand.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				call, err := b.StartCall(p.FunctionCall.ID, p.FunctionCall.Name)
				if err != nil {
					return nil, err
				}
				if err := b.FinishCall(call, string(p.FunctionCall.Args), p.ThoughtSignature); err != nil {
					return nil, err
				}
			case p.Thought:
				if err := b.Thinking(p.Text); err != nil {
					return nil, err
				}
				b.Sign(p.ThoughtSignature)
			default:
				if err := b.Text(p.Text); err != nil {
					return nil, err
				}
				b.Sign(p.ThoughtSignature)
			}
		}
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
	}
	if chunks == 0 {
		return nil, c.o.NetError(ctx, errors.New("stream ended without a response chunk"))
	}
	if finish == "" {
		return nil, c.o.NetError(ctx, errors.New("stream ended before finishReason"))
	}
	if seen {
		if err := b.Usage(u); err != nil {
			return nil, err
		}
	}
	return b.Response(model, mapFinish(finish), u, !seen), nil
}

func mapFinish(s string) core.StopReason {
	switch s {
	case "MAX_TOKENS":
		return core.StopMaxTokens
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII", "IMAGE_SAFETY":
		return core.StopRefusal
	default: // STOP, MALFORMED_FUNCTION_CALL, OTHER, …
		return core.StopEnd
	}
}
