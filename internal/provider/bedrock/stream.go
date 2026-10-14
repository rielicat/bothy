package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// streamEvent is the union of ConverseStream event payloads; the
// :event-type header says which members are set.
type streamEvent struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Start             *struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse"`
	} `json:"start"`
	Delta *struct {
		Text    *string `json:"text"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse"`
		ReasoningContent *struct {
			Text            *string `json:"text"`
			Signature       string  `json:"signature"`
			RedactedContent string  `json:"redactedContent"`
		} `json:"reasoningContent"`
	} `json:"delta"`
	StopReason string `json:"stopReason"`
	Usage      *usage `json:"usage"`
	Message    string `json:"message"`
}

type usage struct {
	InputTokens           int `json:"inputTokens"`
	OutputTokens          int `json:"outputTokens"`
	TotalTokens           int `json:"totalTokens"`
	CacheReadInputTokens  int `json:"cacheReadInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
}

// openBlock is one content block between its first event and
// contentBlockStop.
type openBlock struct {
	kind string // text | reasoning | tool
	call *wire.Call
	args strings.Builder
	nova *novaThinking // a Nova text block's <thinking> preamble
}

func headerString(m eventstream.Message, name string) string {
	if v := m.Headers.Get(name); v != nil {
		return v.String()
	}
	return ""
}

// read decodes the event stream into the builder. The stream must reach
// messageStop; usage arrives in the metadata event after it.
func (c *Client) read(ctx context.Context, body io.Reader, model string, on func(core.StreamEvent) error) (*core.Response, error) {
	b := wire.NewBuilder(core.KindBedrock, on)
	_, base := SplitModel(model)
	nova := isNova(base)
	dec := eventstream.NewDecoder()
	blocks := map[int]*openBlock{}
	var payload []byte
	var u core.Usage
	usageSeen, stopped := false, false
	stop := core.StopEnd
	for {
		msg, err := dec.Decode(body, payload)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var ce eventstream.ChecksumError
			var le eventstream.LengthError
			if errors.As(err, &ce) || errors.As(err, &le) {
				return nil, c.o.Malformed(err)
			}
			return nil, c.o.NetError(ctx, err)
		}
		payload = msg.Payload[:0]
		switch headerString(msg, ":message-type") {
		case "exception":
			typ := headerString(msg, ":exception-type")
			var ev streamEvent
			_ = json.Unmarshal(msg.Payload, &ev)
			text := typ
			if ev.Message != "" {
				text = typ + ": " + ev.Message
			}
			return nil, c.o.StreamError(codeKind(typ, text, 200), text)
		case "error":
			code, text := headerString(msg, ":error-code"), headerString(msg, ":error-message")
			return nil, c.o.StreamError(codeKind(code, text, 200), strings.TrimPrefix(code+": "+text, ": "))
		case "event":
		default:
			continue
		}
		var ev streamEvent
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &ev); err != nil {
				return nil, c.o.Malformed(err)
			}
		}
		switch headerString(msg, ":event-type") {
		case "contentBlockStart":
			if ev.Start == nil || ev.Start.ToolUse == nil {
				continue
			}
			if err := c.closeOthers(b, blocks, ev.ContentBlockIndex); err != nil {
				return nil, err
			}
			call, err := b.StartCall(ev.Start.ToolUse.ToolUseID, ev.Start.ToolUse.Name)
			if err != nil {
				return nil, err
			}
			blocks[ev.ContentBlockIndex] = &openBlock{kind: "tool", call: call}
		case "contentBlockDelta":
			if ev.Delta == nil {
				continue
			}
			if err := c.delta(b, blocks, ev, nova); err != nil {
				return nil, err
			}
		case "contentBlockStop":
			ob := blocks[ev.ContentBlockIndex]
			if ob == nil {
				continue
			}
			delete(blocks, ev.ContentBlockIndex)
			if ob.call != nil {
				if err := b.FinishCall(ob.call, ob.args.String(), ""); err != nil {
					return nil, err
				}
			}
			if err := endBlock(b, ob); err != nil {
				return nil, err
			}
		case "messageStop":
			stopped = true
			stop = mapStop(ev.StopReason)
		case "metadata":
			if ev.Usage != nil {
				usageSeen = true
				u = core.Usage{Input: ev.Usage.InputTokens, Output: ev.Usage.OutputTokens,
					CacheRead: ev.Usage.CacheReadInputTokens, CacheWrite: ev.Usage.CacheWriteInputTokens}
			}
		}
	}
	if !stopped {
		return nil, c.o.NetError(ctx, fmt.Errorf("stream ended before messageStop"))
	}
	if err := c.closeOthers(b, blocks, -1); err != nil {
		return nil, err
	}
	if usageSeen {
		if err := b.Usage(u); err != nil {
			return nil, err
		}
	}
	return b.Response(model, stop, u, !usageSeen), nil
}

// closeOthers ends an open text or reasoning block when a new block starts
// without its predecessor's contentBlockStop (the builder holds one open
// part at a time), and every such block at the end of the stream (idx -1).
func (c *Client) closeOthers(b *wire.Builder, blocks map[int]*openBlock, idx int) error {
	for i, ob := range blocks {
		if i != idx && ob.call == nil {
			delete(blocks, i)
			if err := endBlock(b, ob); err != nil {
				return err
			}
		}
	}
	return nil
}

// endBlock closes a block's open part, after what a Nova preamble still
// holds back.
func endBlock(b *wire.Builder, ob *openBlock) error {
	if ob.nova != nil {
		if err := ob.nova.flush(b.Thinking, b.Text); err != nil {
			return err
		}
	}
	b.Close()
	return nil
}

func (c *Client) delta(b *wire.Builder, blocks map[int]*openBlock, ev streamEvent, nova bool) error {
	d := ev.Delta
	ob := blocks[ev.ContentBlockIndex]
	switch {
	case d.Text != nil:
		if ob == nil {
			if err := c.closeOthers(b, blocks, ev.ContentBlockIndex); err != nil {
				return err
			}
			b.Close()
			ob = &openBlock{kind: "text"}
			if nova {
				ob.nova = &novaThinking{}
			}
			blocks[ev.ContentBlockIndex] = ob
		}
		if ob.nova != nil {
			return ob.nova.feed(*d.Text, b.Thinking, b.Text)
		}
		return b.Text(*d.Text)
	case d.ToolUse != nil:
		if ob != nil && ob.call != nil {
			ob.args.WriteString(d.ToolUse.Input)
		}
	case d.ReasoningContent != nil:
		rc := d.ReasoningContent
		if ob == nil {
			if err := c.closeOthers(b, blocks, ev.ContentBlockIndex); err != nil {
				return err
			}
			b.OpenThinking()
			blocks[ev.ContentBlockIndex] = &openBlock{kind: "reasoning"}
		}
		switch {
		case rc.Text != nil:
			return b.Thinking(*rc.Text)
		case rc.Signature != "":
			b.Sign(rc.Signature)
		case rc.RedactedContent != "":
			b.Sign(RedactedPrefix + rc.RedactedContent)
		}
	}
	return nil
}

func mapStop(s string) core.StopReason {
	switch s {
	case "tool_use":
		return core.StopTool
	case "max_tokens", "model_context_window_exceeded":
		return core.StopMaxTokens
	case "guardrail_intervened", "content_filtered":
		return core.StopRefusal
	default: // end_turn, stop_sequence, malformed_model_output, malformed_tool_use
		return core.StopEnd
	}
}
