// Package anthropic is the Messages API client (streaming SSE, tools,
// thinking with signature round-trip, prompt caching breakpoints). Owner: M2.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
)

// DefaultBaseURL is used when the provider config has no base_url.
const DefaultBaseURL = "https://api.anthropic.com/v1"

// apiVersion is the anthropic-version header the request and stream shapes
// in this package were written against.
const apiVersion = "2023-06-01"

// Client implements core.Provider.
type Client struct{ o wire.Options }

var _ core.Provider = (*Client)(nil)

func New(o wire.Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	o.Kind = core.KindAnthropic
	return &Client{o: o}
}

func (c *Client) ID() string              { return c.o.ID }
func (c *Client) Kind() core.ProviderKind { return core.KindAnthropic }

func setAuth(h http.Header, key string) { h.Set("x-api-key", key) }

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	req, err := c.o.NewRequest(ctx, method, c.o.URL(path), body, setAuth)
	if err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", apiVersion)
	return req, nil
}

func (c *Client) Stream(ctx context.Context, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/messages", buildRequest(r))
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

type usage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	OutputTokensDetails      *struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Model string `json:"model"`
		Usage *usage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		Data     string `json:"data"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// tracker accumulates usage: message_start carries the input side and
// message_delta the cumulative totals, either of which may be missing.
type tracker struct {
	u    core.Usage
	seen bool
}

func (t *tracker) apply(u *usage) {
	if u == nil {
		return
	}
	set := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
			t.seen = true
		}
	}
	set(&t.u.Input, u.InputTokens)
	set(&t.u.Output, u.OutputTokens)
	set(&t.u.CacheWrite, u.CacheCreationInputTokens)
	set(&t.u.CacheRead, u.CacheReadInputTokens)
	if u.OutputTokensDetails != nil {
		t.u.Reasoning = u.OutputTokensDetails.ThinkingTokens
	}
}

type openBlock struct {
	kind string // text | thinking | tool_use
	call *wire.Call
	args strings.Builder
}

func (c *Client) read(ctx context.Context, body io.Reader, model string, on func(core.StreamEvent) error) (*core.Response, error) {
	b := wire.NewBuilder(core.KindAnthropic, on)
	sse := wire.NewSSE(body)
	blocks := map[int]*openBlock{}
	var tr tracker
	stop := core.StopReason("")
	finished := false
	for {
		ev, err := sse.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, c.o.NetError(ctx, err)
		}
		if len(ev.Data) == 0 {
			continue
		}
		var se streamEvent
		if err := json.Unmarshal(ev.Data, &se); err != nil {
			return nil, c.o.Malformed(err)
		}
		switch se.Type {
		case "message_start":
			if se.Message != nil {
				if se.Message.Model != "" {
					model = se.Message.Model
				}
				tr.apply(se.Message.Usage)
			}
		case "content_block_start":
			if se.ContentBlock == nil {
				continue
			}
			ob := &openBlock{kind: se.ContentBlock.Type}
			switch ob.kind {
			case "text":
				b.Close()
				if err := b.Text(se.ContentBlock.Text); err != nil {
					return nil, err
				}
			case "thinking":
				b.OpenThinking()
				if err := b.Thinking(se.ContentBlock.Thinking); err != nil {
					return nil, err
				}
			case "redacted_thinking":
				b.OpenThinking()
				b.Sign(RedactedPrefix + se.ContentBlock.Data)
			case "tool_use":
				call, err := b.StartCall(se.ContentBlock.ID, se.ContentBlock.Name)
				if err != nil {
					return nil, err
				}
				ob.call = call
			}
			blocks[se.Index] = ob
		case "content_block_delta":
			ob := blocks[se.Index]
			if ob == nil || se.Delta == nil {
				continue
			}
			switch se.Delta.Type {
			case "text_delta":
				err = b.Text(se.Delta.Text)
			case "thinking_delta":
				err = b.Thinking(se.Delta.Thinking)
			case "signature_delta":
				b.Sign(se.Delta.Signature)
			case "input_json_delta":
				ob.args.WriteString(se.Delta.PartialJSON)
			}
			if err != nil {
				return nil, err
			}
		case "content_block_stop":
			ob := blocks[se.Index]
			if ob == nil {
				continue
			}
			delete(blocks, se.Index)
			if ob.call != nil {
				if err := b.FinishCall(ob.call, ob.args.String(), ""); err != nil {
					return nil, err
				}
			}
			b.Close()
		case "message_delta":
			if se.Delta != nil && se.Delta.StopReason != "" {
				stop = mapStop(se.Delta.StopReason)
			}
			tr.apply(se.Usage)
		case "message_stop":
			finished = true
		case "error":
			if se.Error == nil {
				return nil, c.o.StreamError(core.ErrKindServer, "error event without details")
			}
			return nil, c.o.StreamError(typeKind(se.Error.Type, se.Error.Message), se.Error.Message)
		}
		if finished {
			break
		}
	}
	if !finished {
		return nil, c.o.NetError(ctx, fmt.Errorf("stream ended before message_stop"))
	}
	if stop == "" {
		stop = core.StopEnd
	}
	if tr.seen {
		if err := b.Usage(tr.u); err != nil {
			return nil, err
		}
	}
	return b.Response(model, stop, tr.u, !tr.seen), nil
}

func mapStop(s string) core.StopReason {
	switch s {
	case "tool_use":
		return core.StopTool
	case "max_tokens", "model_context_window_exceeded":
		return core.StopMaxTokens
	case "refusal":
		return core.StopRefusal
	default: // end_turn, stop_sequence, pause_turn
		return core.StopEnd
	}
}
