package openai

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// respEvent holds the fields of the stream events the reader handles. Every
// event names its type in the JSON; "event:" lines are optional (SDK
// fixtures omit them) and unknown events are skipped.
type respEvent struct {
	Type         string          `json:"type"`
	OutputIndex  *int            `json:"output_index"`
	ItemID       string          `json:"item_id"`
	SummaryIndex int             `json:"summary_index"`
	Item         json.RawMessage `json:"item"`
	Delta        json.RawMessage `json:"delta"`
	Arguments    json.RawMessage `json:"arguments"`
	Response     *respObject     `json:"response"`
	// error events: flat per the spec, nested in some SDKs and servers.
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Error   *errorPayload   `json:"error"`
}

type respObject struct {
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             *respUsage        `json:"usage"`
	Error             *errorPayload     `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type respItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Status           string          `json:"status"`
	Phase            string          `json:"phase"`
	Content          json.RawMessage `json:"content"`
	Summary          json.RawMessage `json:"summary"`
	EncryptedContent string          `json:"encrypted_content"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
}

type respContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

// respUsage: input_tokens includes cached and cache-write tokens, and
// output_tokens includes reasoning (prompt-caching guide), so Input is what
// remains and Output is billed whole.
type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *respUsage) normalize() core.Usage {
	out := core.Usage{Output: u.OutputTokens}
	if d := u.InputTokensDetails; d != nil {
		out.CacheRead, out.CacheWrite = d.CachedTokens, d.CacheWriteTokens
	}
	if d := u.OutputTokensDetails; d != nil {
		out.Reasoning = d.ReasoningTokens
	}
	out.Input = max(0, u.InputTokens-out.CacheRead-out.CacheWrite)
	return out
}

// reasoningSig is the replayable form of a reasoning item: everything the
// API needs and nothing output-only (status, created_by).
type reasoningSig struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Summary          json.RawMessage `json:"summary"`
	Content          json.RawMessage `json:"content,omitempty"`
	EncryptedContent string          `json:"encrypted_content"`
}

// item is one output item while it streams. Deltas are keyed by output
// index, never by "the current item": the API does not promise that items
// do not interleave.
type item struct {
	typ     string
	id      string
	final   bool // output_item.done, or the terminal response, gave the whole item
	dropped bool // a call that must not run

	text     strings.Builder
	streamed bool
	phase    string
	refusal  bool

	summary int // last streamed summary index, -1 before any
	sig     string

	callID, name string
	args         strings.Builder
	argsDone     *string
	started      bool
	call         *core.ToolCall
}

type respReader struct {
	c     *Responses
	on    func(core.StreamEvent) error
	items map[int]*item
	byID  map[string]int
	model string
}

func (c *Responses) read(ctx context.Context, body io.Reader, r *core.Request, on func(core.StreamEvent) error) (*core.Response, error) {
	if on == nil {
		on = func(core.StreamEvent) error { return nil }
	}
	rd := &respReader{c: c, on: on, items: map[int]*item{}, byID: map[string]int{}, model: r.Model}
	sse := wire.NewSSE(body)
	gotEvent := false
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
			break
		}
		var e respEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, c.o.Malformed(err)
		}
		gotEvent = true
		resp, done, err := rd.handle(&e)
		if err != nil {
			return nil, err
		}
		if done {
			return resp, nil
		}
	}
	if !gotEvent {
		return nil, c.o.NetError(ctx, errors.New("stream ended without an event"))
	}
	return nil, c.o.NetError(ctx, errors.New("stream ended before response.completed"))
}

func (rd *respReader) handle(e *respEvent) (*core.Response, bool, error) {
	switch e.Type {
	case "response.created", "response.in_progress", "response.queued":
		if e.Response != nil && e.Response.Model != "" {
			rd.model = e.Response.Model
		}
	case "response.output_item.added":
		var it respItem
		if err := json.Unmarshal(e.Item, &it); err != nil {
			return nil, false, rd.c.o.Malformed(err)
		}
		x := rd.get(rd.index(e, it.ID), it.Type)
		x.typ, x.id, x.phase = it.Type, it.ID, it.Phase
		if it.Type == "function_call" {
			x.callID, x.name = it.CallID, it.Name
			x.args.WriteString(argText(it.Arguments))
			if err := rd.start(x); err != nil {
				return nil, false, err
			}
		}
	case "response.output_text.delta", "response.refusal.delta":
		x := rd.get(rd.index(e, e.ItemID), "message")
		x.refusal = x.refusal || e.Type == "response.refusal.delta"
		if d := str(e.Delta); d != "" {
			x.text.WriteString(d)
			x.streamed = true
			if err := rd.on(core.StreamEvent{Type: core.StreamTextDelta, Text: d}); err != nil {
				return nil, false, err
			}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		x := rd.get(rd.index(e, e.ItemID), "reasoning")
		d := str(e.Delta)
		if d == "" {
			break
		}
		if e.Type == "response.reasoning_summary_text.delta" {
			if x.summary >= 0 && e.SummaryIndex != x.summary {
				d = "\n\n" + d
			}
			x.summary = e.SummaryIndex
		}
		x.text.WriteString(d)
		x.streamed = true
		if err := rd.on(core.StreamEvent{Type: core.StreamThinkingDelta, Text: d}); err != nil {
			return nil, false, err
		}
	case "response.function_call_arguments.delta":
		rd.get(rd.index(e, e.ItemID), "function_call").args.WriteString(str(e.Delta))
	case "response.function_call_arguments.done":
		a := argText(e.Arguments)
		rd.get(rd.index(e, e.ItemID), "function_call").argsDone = &a
	case "response.output_item.done":
		var it respItem
		if err := json.Unmarshal(e.Item, &it); err != nil {
			return nil, false, rd.c.o.Malformed(err)
		}
		if err := rd.finalize(rd.index(e, it.ID), it); err != nil {
			return nil, false, err
		}
	case "response.completed", "response.incomplete":
		resp, err := rd.finish(e)
		return resp, true, err
	case "response.failed":
		p := errorPayload{Message: "the response failed"}
		if e.Response != nil && e.Response.Error != nil {
			p = *e.Response.Error
		}
		return nil, false, rd.streamError(p)
	case "error":
		p := errorPayload{Message: e.Message, Code: e.Code}
		if e.Error != nil {
			p = *e.Error
		}
		return nil, false, rd.streamError(p)
	}
	return nil, false, nil
}

// index returns the event's output index, falling back to the item id for
// servers that send only that, and to a fresh slot when neither is known.
func (rd *respReader) index(e *respEvent, id string) int {
	if e.OutputIndex != nil {
		if id != "" {
			rd.byID[id] = *e.OutputIndex
		}
		return *e.OutputIndex
	}
	if i, ok := rd.byID[id]; ok && id != "" {
		return i
	}
	i := len(rd.items)
	for rd.items[i] != nil {
		i++
	}
	if id != "" {
		rd.byID[id] = i
	}
	return i
}

func (rd *respReader) get(i int, typ string) *item {
	x := rd.items[i]
	if x == nil {
		x = &item{typ: typ, summary: -1}
		rd.items[i] = x
	}
	return x
}

func (rd *respReader) start(x *item) error {
	if x.started || x.name == "" {
		return nil
	}
	if x.callID == "" {
		x.callID = wire.SynthesizedID()
	}
	x.started = true
	return rd.on(core.StreamEvent{Type: core.StreamToolCallStart, Call: &core.ToolCall{ID: x.callID, Name: x.name}})
}

// finalize takes a whole item (output_item.done, or the terminal response's
// output for an item that never got one). It is authoritative over the
// deltas; text that was never streamed goes out now.
func (rd *respReader) finalize(i int, it respItem) error {
	x := rd.get(i, it.Type)
	if x.final {
		return nil
	}
	x.final = true
	x.typ = it.Type
	if it.ID != "" {
		x.id = it.ID
	}
	switch it.Type {
	case "message":
		text, refusal := messageText(it.Content)
		x.phase = cmp.Or(it.Phase, x.phase)
		x.refusal = x.refusal || refusal
		if !x.streamed && text != "" {
			if err := rd.on(core.StreamEvent{Type: core.StreamTextDelta, Text: text}); err != nil {
				return err
			}
		}
		x.setText(text, x.streamed)
	case "reasoning":
		text := summaryText(it.Summary)
		if text == "" {
			text = reasoningText(it.Content)
		}
		if !x.streamed && text != "" {
			if err := rd.on(core.StreamEvent{Type: core.StreamThinkingDelta, Text: text}); err != nil {
				return err
			}
		}
		x.setText(text, x.streamed)
		if it.EncryptedContent != "" {
			summary := compact(it.Summary)
			if summary == nil || string(summary) == "null" {
				summary = json.RawMessage("[]")
			}
			content := compact(it.Content)
			if string(content) == "null" || string(content) == "[]" {
				content = nil
			}
			x.sig = rd.c.sign(marshal(reasoningSig{Type: "reasoning", ID: it.ID, Summary: summary, Content: content, EncryptedContent: it.EncryptedContent}))
		}
	case "function_call":
		x.callID = cmp.Or(it.CallID, x.callID)
		x.name = cmp.Or(it.Name, x.name)
		if it.Status == "incomplete" || it.Status == "in_progress" {
			x.dropped = true
			return nil
		}
		raw := argText(it.Arguments)
		if raw == "" {
			raw = x.streamedArgs()
		}
		return rd.finishCall(x, raw)
	}
	return nil
}

// streamedArgs is what the argument events carried.
func (x *item) streamedArgs() string {
	if x.argsDone != nil {
		return *x.argsDone
	}
	return x.args.String()
}

// setText replaces the streamed text with the final one unless deltas were
// streamed and the final item carries none (some servers send it empty).
func (x *item) setText(final string, streamed bool) {
	if final == "" && streamed {
		return
	}
	x.text.Reset()
	x.text.WriteString(final)
}

func (rd *respReader) finishCall(x *item, raw string) error {
	if x.call != nil || x.dropped {
		return nil
	}
	if x.name == "" {
		x.dropped = true
		return nil
	}
	if err := rd.start(x); err != nil {
		return err
	}
	args, _ := wire.RepairArgs(raw)
	x.call = &core.ToolCall{ID: x.callID, Name: x.name, Args: args}
	done := *x.call
	return rd.on(core.StreamEvent{Type: core.StreamToolCallDone, Call: &done})
}

func (rd *respReader) finish(e *respEvent) (*core.Response, error) {
	var res respObject
	if e.Response != nil {
		res = *e.Response
	}
	if res.Model != "" {
		rd.model = res.Model
	}
	stop := core.StopEnd
	if e.Type == "response.incomplete" {
		stop = core.StopMaxTokens
		if d := res.IncompleteDetails; d != nil && d.Reason == "content_filter" {
			stop = core.StopRefusal
		}
	}
	// Some streams (the openai-node fixtures) never send output_item.done;
	// the terminal response still lists every item.
	for i, raw := range res.Output {
		if x := rd.items[i]; x != nil && x.final {
			continue
		}
		var it respItem
		if err := json.Unmarshal(raw, &it); err != nil {
			return nil, rd.c.o.Malformed(err)
		}
		if err := rd.finalize(i, it); err != nil {
			return nil, err
		}
	}
	idx := make([]int, 0, len(rd.items))
	for i := range rd.items {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	var parts []core.Part
	calls, refused := 0, false
	for _, i := range idx {
		x := rd.items[i]
		switch x.typ {
		case "message":
			text := x.text.String()
			if text == "" {
				continue
			}
			refused = refused || x.refusal
			p := core.Part{Type: core.PartText, Text: text}
			if meta := (messageMeta{ID: x.id, Phase: x.phase, Refusal: x.refusal}); meta != (messageMeta{}) {
				p.Sig, p.SigKind = rd.c.sign(marshal(meta)), SigResponses
			}
			parts = append(parts, p)
		case "reasoning":
			if x.text.Len() == 0 && x.sig == "" {
				continue
			}
			parts = append(parts, core.Part{Type: core.PartThinking, Text: x.text.String(), Sig: x.sig, SigKind: SigResponses})
		case "function_call":
			// A call whose item never completed is cut off: running half an
			// argument (a path or command cut short) could do harm.
			if x.call == nil && !x.dropped && stop == core.StopEnd {
				if raw := x.streamedArgs(); wire.ArgsComplete(raw) {
					if err := rd.finishCall(x, raw); err != nil {
						return nil, err
					}
				}
			}
			if x.call == nil {
				continue
			}
			calls++
			p := core.Part{Type: core.PartToolCall, ToolCall: x.call}
			if x.id != "" {
				p.Sig, p.SigKind = rd.c.sign(x.id), SigResponses
			}
			parts = append(parts, p)
		}
	}
	switch {
	case stop != core.StopEnd:
	case calls > 0:
		stop = core.StopTool
	case refused:
		stop = core.StopRefusal
	}
	var u core.Usage
	if res.Usage != nil {
		u = res.Usage.normalize()
		uu := u
		if err := rd.on(core.StreamEvent{Type: core.StreamUsage, Usage: &uu}); err != nil {
			return nil, err
		}
	}
	return &core.Response{
		Message:        core.Message{Role: core.MsgAssistant, Kind: core.KindNormal, Parts: parts, Tokens: u.Output},
		Usage:          u,
		UsageEstimated: res.Usage == nil,
		Stop:           stop,
		Model:          rd.model,
	}, nil
}

var tryAgainRe = regexp.MustCompile(`(?i)try again in ([0-9]+(?:\.[0-9]+)?)\s*(ms|s)\b`)

// streamError maps a response.failed or error event. These arrive on an
// HTTP 200, so a rate limit carries its wait only in the message.
func (rd *respReader) streamError(p errorPayload) error {
	kind, msg := p.kind(200)
	if strings.TrimSpace(msg) == "" {
		msg = "the provider reported an error (" + cmp.Or(p.code(), "no code") + ")"
	}
	err := rd.c.o.StreamError(kind, msg)
	var pe *core.ProviderError
	if kind == core.ErrKindRateLimit && errors.As(err, &pe) {
		if m := tryAgainRe.FindStringSubmatch(p.Message); m != nil {
			if f, perr := strconv.ParseFloat(m[1], 64); perr == nil {
				unit := time.Second
				if strings.EqualFold(m[2], "ms") {
					unit = time.Millisecond
				}
				pe.RetryAfter = time.Duration(f * float64(unit))
			}
		}
	}
	return err
}

func messageText(raw json.RawMessage) (string, bool) {
	var cs []respContent
	if json.Unmarshal(raw, &cs) != nil {
		return "", false
	}
	var b strings.Builder
	refusal := false
	for _, c := range cs {
		switch c.Type {
		case "output_text":
			b.WriteString(c.Text)
		case "refusal":
			b.WriteString(c.Refusal)
			refusal = true
		}
	}
	return b.String(), refusal
}

func summaryText(raw json.RawMessage) string {
	var cs []respContent
	if json.Unmarshal(raw, &cs) != nil {
		return ""
	}
	var texts []string
	for _, c := range cs {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

func reasoningText(raw json.RawMessage) string {
	var cs []respContent
	if json.Unmarshal(raw, &cs) != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range cs {
		if c.Type == "reasoning_text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// str decodes a JSON string; anything else is "".
func str(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func compact(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return nil
	}
	return b.Bytes()
}

// marshal encodes without HTML escaping, so stored signatures keep the
// bytes the server sent.
func marshal(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}
