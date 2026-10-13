// Package openai is the Chat Completions client for OpenAI and every
// OpenAI-compatible server (openai_compat: LM Studio, vLLM, OpenRouter, Groq,
// xAI, DeepSeek…). It tolerates the compatible servers' quirks: missing
// usage, missing call ids, whole-argument tool calls, reasoning text in
// reasoning_content or reasoning, and tool calls written as
// <tool_call>{…}</tool_call> text. Owner: M2.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// DefaultBaseURL is used for kind openai when the config has no base_url.
const DefaultBaseURL = "https://api.openai.com/v1"

// Client implements core.Provider.
type Client struct{ o wire.Options }

var _ core.Provider = (*Client)(nil)

// New returns a client; o.Kind is KindOpenAI or KindOpenAICompat.
func New(o wire.Options) *Client {
	if o.Kind != core.KindOpenAICompat {
		o.Kind = core.KindOpenAI
	}
	if o.BaseURL == "" && o.Kind == core.KindOpenAI {
		o.BaseURL = DefaultBaseURL
	}
	return &Client{o: o}
}

func (c *Client) ID() string              { return c.o.ID }
func (c *Client) Kind() core.ProviderKind { return c.o.Kind }

func setAuth(h http.Header, key string) { h.Set("Authorization", "Bearer "+key) }

func (c *Client) Stream(ctx context.Context, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	req, err := c.o.NewRequest(ctx, http.MethodPost, c.o.URL("/chat/completions"), c.buildRequest(r), setAuth)
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

type chunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			Reasoning        *string `json:"reasoning"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageJSON    `json:"usage"`
	Error *errorPayload `json:"error"`
}

type usageJSON struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	// DeepSeek reports its cache this way instead.
	PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
}

// normalize converts to core.Usage, where Input excludes cache reads and
// writes (OpenAI counts them inside prompt_tokens) and Output includes
// reasoning. OpenAI counts reasoning inside completion_tokens; xAI reports
// it beside them (total = prompt + completion + reasoning) and bills it as
// output, recognisable by that total or by more reasoning than completion.
func (u *usageJSON) normalize() core.Usage {
	out := core.Usage{Output: u.CompletionTokens}
	if d := u.PromptTokensDetails; d != nil {
		out.CacheRead, out.CacheWrite = d.CachedTokens, d.CacheWriteTokens
	}
	if out.CacheRead == 0 && u.PromptCacheHitTokens != nil {
		out.CacheRead = *u.PromptCacheHitTokens
	}
	if d := u.CompletionTokensDetails; d != nil {
		out.Reasoning = d.ReasoningTokens
	}
	if r := out.Reasoning; r > 0 && (r > u.CompletionTokens || u.TotalTokens >= u.PromptTokens+u.CompletionTokens+r) {
		out.Output += r
	}
	out.Input = max(0, u.PromptTokens-out.CacheRead-out.CacheWrite)
	return out
}

type pending struct {
	call *wire.Call
	args strings.Builder
}

func (c *Client) read(ctx context.Context, body io.Reader, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	b := wire.NewBuilder(c.o.Kind, on)
	// OpenAI itself always answers with native tool calls; there a
	// <tool_call> tag can only be prose (an example, a quote) and must not
	// turn into an action.
	text := newTextFilter(b, c.o.Kind == core.KindOpenAICompat && len(r.Tools) > 0)
	sse := wire.NewSSE(body)
	model := r.Model
	var (
		calls    []*pending
		byIndex  = map[int]*pending{}
		usage    *usageJSON
		finish   string
		gotDone  bool
		gotChunk bool
	)
	finishCalls := func() error {
		for _, p := range calls {
			raw := p.args.String()
			// Arguments cut off by the output limit never become a call.
			if finish == "length" && !wire.ArgsComplete(raw) {
				continue
			}
			if err := b.FinishCall(p.call, raw, ""); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		ev, err := sse.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, c.o.NetError(ctx, err)
		}
		data := bytes.TrimSpace(ev.Data)
		if len(data) == 0 {
			continue
		}
		if string(data) == "[DONE]" {
			gotDone = true
			break
		}
		var ch chunk
		if err := json.Unmarshal(data, &ch); err != nil {
			return nil, c.o.Malformed(err)
		}
		if ch.Error != nil {
			kind, msg := ch.Error.kind(http.StatusOK)
			return nil, c.o.StreamError(kind, msg)
		}
		gotChunk = true
		if ch.Model != "" {
			model = ch.Model
		}
		if ch.Usage != nil {
			usage = ch.Usage // some servers repeat cumulative usage on every chunk
		}
		for _, choice := range ch.Choices {
			if choice.Index != 0 {
				continue
			}
			d := choice.Delta
			// Some servers send the same text under both names.
			reasoning, echo := d.ReasoningContent, c.echoSig()
			if reasoning == nil || *reasoning == "" {
				reasoning, echo = d.Reasoning, ""
			}
			if reasoning != nil {
				if err := b.Thinking(*reasoning); err != nil {
					return nil, err
				}
				if *reasoning != "" {
					b.Sign(echo)
				}
			}
			if d.Content != nil {
				if err := text.write(*d.Content); err != nil {
					return nil, err
				}
			}
			for _, tc := range d.ToolCalls {
				idx := len(calls)
				if tc.Index != nil {
					idx = *tc.Index
				}
				p := byIndex[idx]
				// Some servers number every call 0 and tell them apart by id.
				if p != nil && tc.ID != "" && p.call.ID != tc.ID && !wire.IsSynthesized(p.call.ID) {
					p = nil
				}
				if p == nil {
					if err := text.flush(); err != nil {
						return nil, err
					}
					call, err := b.StartCall(tc.ID, tc.Function.Name)
					if err != nil {
						return nil, err
					}
					p = &pending{call: call}
					byIndex[idx] = p
					calls = append(calls, p)
				}
				p.args.WriteString(argText(tc.Function.Arguments))
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
		}
	}
	if !gotDone && finish == "" {
		if !gotChunk {
			return nil, c.o.NetError(ctx, errors.New("stream ended without a chunk"))
		}
		return nil, c.o.NetError(ctx, errors.New("stream ended before finish_reason"))
	}
	if err := text.flush(); err != nil {
		return nil, err
	}
	if err := finishCalls(); err != nil {
		return nil, err
	}
	stop := mapFinish(finish)
	var u core.Usage
	if usage != nil {
		u = usage.normalize()
		if err := b.Usage(u); err != nil {
			return nil, err
		}
	}
	return b.Response(model, stop, u, usage == nil), nil
}

// echoSig is the signature of a thinking part whose text this server sent as
// reasoning_content. DeepSeek's thinking mode rejects a tool loop (HTTP 400)
// unless every assistant turn carries it back, so it is returned under that
// name, and only to the provider that wrote it: servers that use the
// "reasoning" field instead (Groq, OpenRouter) reject an unknown one.
func (c *Client) echoSig() string {
	if c.o.Kind != core.KindOpenAICompat {
		return ""
	}
	return "reasoning_content:" + c.o.ID
}

// argText accepts arguments as the specified JSON string or, from some
// compatible servers, as a raw JSON object.
func argText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

func mapFinish(s string) core.StopReason {
	switch s {
	case "tool_calls", "function_call":
		return core.StopTool
	case "length":
		return core.StopMaxTokens
	case "content_filter":
		return core.StopRefusal
	default:
		return core.StopEnd
	}
}
