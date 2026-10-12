package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// validateArgs checks tool arguments against the tool's JSON Schema before
// Plan. It covers the keywords tool schemas use (type, properties, required,
// additionalProperties, items, enum, const, bounds, pattern, allOf/anyOf/
// oneOf); unknown keywords pass, since Plan validates fully. The registry
// already rejected schemas that do not compile.
func validateArgs(schema, args json.RawMessage) error {
	var v any
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return errors.New("arguments must be a JSON object")
	}
	s, ok := parseSchema(schema)
	if !ok {
		return nil
	}
	return checkSchema(s, v, "")
}

func parseSchema(schema json.RawMessage) (any, bool) {
	if len(bytes.TrimSpace(schema)) == 0 {
		return nil, false
	}
	var s any
	return s, json.Unmarshal(schema, &s) == nil
}

func checkSchema(s, v any, at string) error {
	sm, ok := s.(map[string]any)
	if !ok {
		if b, isBool := s.(bool); isBool && !b {
			return fmt.Errorf("%s: no value is allowed here", where(at))
		}
		return nil
	}
	if t, ok := sm["type"]; ok && !typeMatches(t, v) {
		return fmt.Errorf("%s: must be %s", where(at), typeName(t))
	}
	if enum, ok := sm["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return jsonEqual(e, v) }) {
		return fmt.Errorf("%s: must be one of %s", where(at), compact(enum))
	}
	if c, ok := sm["const"]; ok && !jsonEqual(c, v) {
		return fmt.Errorf("%s: must be %s", where(at), compact(c))
	}
	switch x := v.(type) {
	case map[string]any:
		if err := checkObject(sm, x, at); err != nil {
			return err
		}
	case []any:
		if err := checkArray(sm, x, at); err != nil {
			return err
		}
	case string:
		n := utf8.RuneCountInString(x)
		if m, ok := num(sm["minLength"]); ok && float64(n) < m {
			return fmt.Errorf("%s: must be at least %v characters", where(at), m)
		}
		if m, ok := num(sm["maxLength"]); ok && float64(n) > m {
			return fmt.Errorf("%s: must be at most %v characters", where(at), m)
		}
		if p, ok := sm["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(x) {
				return fmt.Errorf("%s: must match %q", where(at), p)
			}
		}
	case json.Number:
		f, _ := x.Float64()
		if m, ok := num(sm["minimum"]); ok && f < m {
			return fmt.Errorf("%s: must be >= %v", where(at), m)
		}
		if m, ok := num(sm["maximum"]); ok && f > m {
			return fmt.Errorf("%s: must be <= %v", where(at), m)
		}
		if m, ok := num(sm["exclusiveMinimum"]); ok && f <= m {
			return fmt.Errorf("%s: must be > %v", where(at), m)
		}
		if m, ok := num(sm["exclusiveMaximum"]); ok && f >= m {
			return fmt.Errorf("%s: must be < %v", where(at), m)
		}
	}
	if all, ok := sm["allOf"].([]any); ok {
		for _, sub := range all {
			if err := checkSchema(sub, v, at); err != nil {
				return err
			}
		}
	}
	if anyOf, ok := sm["anyOf"].([]any); ok {
		var first error
		matched := false
		for _, sub := range anyOf {
			err := checkSchema(sub, v, at)
			if err == nil {
				matched = true
				break
			}
			if first == nil {
				first = err
			}
		}
		if !matched && first != nil {
			return first
		}
	}
	if oneOf, ok := sm["oneOf"].([]any); ok {
		n := 0
		for _, sub := range oneOf {
			if checkSchema(sub, v, at) == nil {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("%s: must match exactly one of the allowed shapes", where(at))
		}
	}
	return nil
}

func checkObject(sm map[string]any, x map[string]any, at string) error {
	if req, ok := sm["required"].([]any); ok {
		for _, k := range req {
			if name, ok := k.(string); ok {
				if _, present := x[name]; !present {
					return fmt.Errorf("%s: missing required property %q", where(at), name)
				}
			}
		}
	}
	props, _ := sm["properties"].(map[string]any)
	keys := make([]string, 0, len(x))
	for k := range x {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ps, ok := props[k]; ok {
			if err := checkSchema(ps, x[k], at+"/"+k); err != nil {
				return err
			}
			continue
		}
		switch ap := sm["additionalProperties"].(type) {
		case bool:
			if !ap {
				return fmt.Errorf("%s: unknown property %q", where(at), k)
			}
		case map[string]any:
			if err := checkSchema(ap, x[k], at+"/"+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkArray(sm map[string]any, x []any, at string) error {
	if m, ok := num(sm["minItems"]); ok && float64(len(x)) < m {
		return fmt.Errorf("%s: needs at least %v items", where(at), m)
	}
	if m, ok := num(sm["maxItems"]); ok && float64(len(x)) > m {
		return fmt.Errorf("%s: allows at most %v items", where(at), m)
	}
	if items, ok := sm["items"]; ok {
		for i, e := range x {
			if err := checkSchema(items, e, fmt.Sprintf("%s/%d", at, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeMatches(t, v any) bool {
	switch tt := t.(type) {
	case string:
		return isType(tt, v)
	case []any:
		for _, x := range tt {
			if s, ok := x.(string); ok && isType(s, v) {
				return true
			}
		}
		return false
	}
	return true
}

func isType(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f)
	}
	return true
}

func typeName(t any) string {
	switch tt := t.(type) {
	case string:
		return "a value of type " + tt
	case []any:
		names := make([]string, 0, len(tt))
		for _, x := range tt {
			names = append(names, fmt.Sprint(x))
		}
		return "one of the types " + strings.Join(names, ", ")
	}
	return "valid"
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(normalizeNumbers(a))
	bb, err2 := json.Marshal(normalizeNumbers(b))
	return err1 == nil && err2 == nil && bytes.Equal(ab, bb)
}

// normalizeNumbers maps json.Number and float64 to float64 so schema
// constants compare equal to decoded arguments.
func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return x.String()
		}
		return f
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeNumbers(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalizeNumbers(e)
		}
		return out
	}
	return v
}

func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return truncateRunes(string(b), 200)
}

func where(at string) string {
	if at == "" {
		return "arguments"
	}
	return at
}
