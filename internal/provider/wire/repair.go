package wire

import (
	"bytes"
	"encoding/json"
	"strings"
)

// RepairArgs turns model-produced tool arguments into a compact JSON object.
// It accepts what models actually emit: empty input, code fences, an object
// encoded as a JSON string, trailing commas and unclosed strings or brackets
// at the end. ok is false when nothing usable remains; args is then "{}" so
// the call still fails schema validation with a message the model can act on.
func RepairArgs(raw string) (args json.RawMessage, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return json.RawMessage("{}"), true
	}
	s = stripFence(s)
	for _, cand := range []string{s, dropTrailingCommas(s), closeOpen(dropTrailingCommas(s))} {
		if obj, ok := asObject([]byte(cand)); ok {
			return obj, true
		}
	}
	return json.RawMessage("{}"), false
}

func asObject(b []byte) (json.RawMessage, bool) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	switch x := v.(type) {
	case map[string]any:
		var buf bytes.Buffer
		if err := json.Compact(&buf, bytes.TrimSpace(b)); err != nil {
			return nil, false
		}
		return buf.Bytes(), true
	case string:
		// Some models double-encode: "{\"city\":\"Paris\"}".
		var inner map[string]any
		if json.Unmarshal([]byte(x), &inner) == nil {
			var buf bytes.Buffer
			if json.Compact(&buf, []byte(strings.TrimSpace(x))) == nil {
				return buf.Bytes(), true
			}
		}
	}
	return nil, false
}

func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 && !strings.ContainsAny(s[:i], "{[") {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// dropTrailingCommas removes commas directly before a closing bracket,
// outside strings.
func dropTrailingCommas(s string) string {
	var out strings.Builder
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			out.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\r' || s[j] == '\t') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.String()
}

// closeOpen appends the closers for strings, objects and arrays left open at
// the end of s (output cut off mid-argument).
func closeOpen(s string) string {
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	var b strings.Builder
	b.WriteString(s)
	if esc {
		b.WriteByte('\\')
	}
	if inStr {
		b.WriteByte('"')
	}
	t := strings.TrimRight(b.String(), " \n\r\t")
	t = strings.TrimSuffix(t, ",")
	if strings.HasSuffix(t, ":") {
		t += "null"
	}
	b.Reset()
	b.WriteString(t)
	for i := len(stack) - 1; i >= 0; i-- {
		b.WriteByte(stack[i])
	}
	return b.String()
}
