// Package ollama is the native Ollama client: /api/chat streamed as NDJSON
// with options.num_ctx (the OpenAI-compatible endpoint ignores the context
// size), /api/tags and /api/show for discovery and /api/ps for the context
// the server actually allocated. Owner: M2.
package ollama

import (
	"bufio"
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

// DefaultBaseURL is used when the provider config has no base_url.
const DefaultBaseURL = "http://localhost:11434"

// Client implements core.Provider.
type Client struct{ o wire.Options }

var _ core.Provider = (*Client)(nil)

func New(o wire.Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	o.Kind = core.KindOllama
	return &Client{o: o}
}

func (c *Client) ID() string              { return c.o.ID }
func (c *Client) Kind() core.ProviderKind { return core.KindOllama }

// setAuth serves Ollama behind an authenticating proxy or ollama.com.
func setAuth(h http.Header, key string) { h.Set("Authorization", "Bearer "+key) }

func (c *Client) Stream(ctx context.Context, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	req, err := c.o.NewRequest(ctx, http.MethodPost, c.o.URL("/api/chat"), buildRequest(r), setAuth)
	if err != nil {
		return nil, err
	}
	resp, err := c.o.Do(ctx, req, classify)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return c.read(ctx, resp.Body, r.Model, on)
}

type chunk struct {
	Model   string `json:"model"`
	Message *struct {
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []toolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount *int   `json:"prompt_eval_count"`
	EvalCount       *int   `json:"eval_count"`
	Error           string `json:"error"`
}

func (c *Client) read(ctx context.Context, body io.Reader, model string, on func(core.StreamEvent) error) (*core.Response, error) {
	b := wire.NewBuilder(core.KindOllama, on)
	br := bufio.NewReaderSize(body, 32<<10)
	var (
		final *chunk
		lines int
	)
	for final == nil {
		line, err := wire.ReadLine(br)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, c.o.NetError(ctx, err)
		}
		eof := errors.Is(err, io.EOF)
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var ch chunk
			if err := json.Unmarshal(line, &ch); err != nil {
				return nil, c.o.Malformed(err)
			}
			if ch.Error != "" {
				return nil, c.o.StreamError(messageKind(ch.Error), ch.Error)
			}
			lines++
			if ch.Model != "" {
				model = ch.Model
			}
			if m := ch.Message; m != nil {
				if err := b.Thinking(m.Thinking); err != nil {
					return nil, err
				}
				if err := b.Text(m.Content); err != nil {
					return nil, err
				}
				for _, tc := range m.ToolCalls {
					call, err := b.StartCall(tc.ID, tc.Function.Name)
					if err != nil {
						return nil, err
					}
					if err := b.FinishCall(call, string(tc.Function.Arguments), ""); err != nil {
						return nil, err
					}
				}
			}
			if ch.Done {
				final = &ch
			}
		}
		if eof {
			break
		}
	}
	if final == nil {
		if lines == 0 {
			return nil, c.o.NetError(ctx, errors.New("stream ended without a response line"))
		}
		return nil, c.o.NetError(ctx, errors.New("stream ended before done"))
	}
	stop := core.StopEnd
	if final.DoneReason == "length" {
		stop = core.StopMaxTokens
	}
	// A fully cached prompt can come back without prompt_eval_count; the
	// facade then estimates instead of billing zero input.
	reported := final.PromptEvalCount != nil && final.EvalCount != nil
	var u core.Usage
	if reported {
		u = core.Usage{Input: *final.PromptEvalCount, Output: *final.EvalCount}
		if err := b.Usage(u); err != nil {
			return nil, err
		}
	}
	return b.Response(model, stop, u, !reported), nil
}

func messageKind(msg string) core.ErrorKind {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "not found"):
		return core.ErrKindInvalidRequest
	case wire.IsContextOverflow(msg):
		return core.ErrKindContextLength
	}
	return core.ErrKindServer
}

// classify reads Ollama's {"error":"…"} body.
func classify(status int, body []byte) (core.ErrorKind, string) {
	var e struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	kind := wire.StatusKind(status)
	if kind == core.ErrKindInvalidRequest && wire.IsContextOverflow(msg) {
		kind = core.ErrKindContextLength
	}
	return kind, msg
}
