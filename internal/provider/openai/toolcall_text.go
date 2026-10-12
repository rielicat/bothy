package openai

import (
	"encoding/json"
	"strings"

	"github.com/bothyhq/bothy/internal/provider/wire"
)

const (
	openTag  = "<tool_call>"
	closeTag = "</tool_call>"
)

// textFilter forwards content deltas and, for an OpenAI-compatible server
// when the request offered tools, turns
// <tool_call>{"name":…,"arguments":…}</tool_call> spans (the Hermes / Qwen
// convention, emitted as plain text by servers without a tool parser) into
// real tool calls. Text that might be the start of a tag is held back
// until it can be decided, so a tag never leaks to the user half-written.
type textFilter struct {
	b       *wire.Builder
	enabled bool
	buf     strings.Builder // undecided text, or the body of an open tag
	inTag   bool
}

func newTextFilter(b *wire.Builder, enabled bool) *textFilter {
	return &textFilter{b: b, enabled: enabled}
}

func (f *textFilter) write(s string) error {
	if !f.enabled {
		return f.b.Text(s)
	}
	f.buf.WriteString(s)
	for {
		cur := f.buf.String()
		if f.inTag {
			end := strings.Index(cur, closeTag)
			if end < 0 {
				return nil
			}
			f.buf.Reset()
			f.buf.WriteString(cur[end+len(closeTag):])
			f.inTag = false
			if err := f.emitCall(cur[:end]); err != nil {
				return err
			}
			continue
		}
		start := strings.Index(cur, openTag)
		if start >= 0 {
			if err := f.b.Text(cur[:start]); err != nil {
				return err
			}
			f.buf.Reset()
			f.buf.WriteString(cur[start+len(openTag):])
			f.inTag = true
			continue
		}
		keep := partialPrefix(cur, openTag)
		if err := f.b.Text(cur[:len(cur)-keep]); err != nil {
			return err
		}
		f.buf.Reset()
		f.buf.WriteString(cur[len(cur)-keep:])
		return nil
	}
}

// flush releases held-back text at the end of the stream (or before a native
// tool call). An unterminated tag that holds a complete call still counts.
func (f *textFilter) flush() error {
	if !f.enabled || f.buf.Len() == 0 {
		return nil
	}
	rest := f.buf.String()
	f.buf.Reset()
	if f.inTag {
		f.inTag = false
		if _, ok := parseTextCall(rest); ok {
			return f.emitCall(rest)
		}
		return f.b.Text(openTag + rest)
	}
	return f.b.Text(rest)
}

func (f *textFilter) emitCall(body string) error {
	tc, ok := parseTextCall(body)
	if !ok {
		return f.b.Text(openTag + body + closeTag)
	}
	call, err := f.b.StartCall("", tc.Name)
	if err != nil {
		return err
	}
	return f.b.FinishCall(call, string(tc.Arguments), "")
}

type textCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func parseTextCall(body string) (textCall, bool) {
	var tc textCall
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &tc); err != nil || tc.Name == "" {
		return textCall{}, false
	}
	if len(tc.Arguments) > 0 && tc.Arguments[0] == '"' {
		var s string
		if json.Unmarshal(tc.Arguments, &s) == nil {
			tc.Arguments = json.RawMessage(s)
		}
	}
	return tc, true
}

// partialPrefix returns the length of the longest suffix of s that is a
// proper prefix of tag.
func partialPrefix(s, tag string) int {
	for n := min(len(s), len(tag)-1); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
