package config

import (
	"encoding/json"
	"reflect"
	"slices"

	"go.yaml.in/yaml/v3"
)

// buildSchema renders the Config schema as JSON Schema draft 2020-12 with
// defaults from def. Non-standard annotations for the UI: x-reload
// ("hot"|"restart"), x-locked, x-secret and, on keyed lists, x-key.
func buildSchema(def *Config) json.RawMessage {
	var n yaml.Node
	_ = n.Encode(def)
	s := schemaOf(rootField(), &n, "")
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = "Bothy configuration"
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`)
	}
	return b
}

func schemaOf(f *field, def *yaml.Node, pattern string) map[string]any {
	s := map[string]any{}
	switch f.kind {
	case kStruct:
		props := map[string]any{}
		for _, c := range f.fields {
			var cd *yaml.Node
			if def != nil && def.Kind == yaml.MappingNode {
				_, cd = lookup(def, c.name)
			}
			props[c.name] = schemaOf(c, cd, join(pattern, c.name))
		}
		s["type"] = "object"
		if f.isPtrStruct() {
			s["type"] = []string{"object", "null"}
		}
		s["properties"] = props
		s["additionalProperties"] = false
	case kKeyedList:
		item := schemaOf(f.elem, nil, join(pattern, "*"))
		item["required"] = []string{f.key}
		s["type"] = "array"
		s["items"] = item
		s["x-key"] = f.key
	case kMap:
		s["type"] = "object"
		s["additionalProperties"] = schemaOf(f.elem, nil, join(pattern, "*"))
	case kScalarList:
		s["type"] = "array"
		items := scalarSchema(f.typ.Elem())
		if e := enums[pattern]; e != nil {
			items["enum"] = e
		}
		s["items"] = items
	default:
		s = scalarSchema(f.typ)
		switch {
		case f.embed:
			s["pattern"] = secretRefAny.String()
		case f.secret:
			s["pattern"] = "^$|" + secretRefExact.String()
		}
		if e := enums[pattern]; e != nil {
			s["enum"] = e
		}
	}
	if def != nil && f.kind != kStruct {
		s["default"] = leafJSON(f, def)
	}
	s["x-reload"] = "restart"
	if f.hot {
		s["x-reload"] = "hot"
	}
	if f.locked {
		s["x-locked"] = true
	}
	if f.secret {
		s["x-secret"] = true
	}
	return s
}

func scalarSchema(t reflect.Type) map[string]any {
	nullable := t.Kind() == reflect.Pointer
	if nullable {
		t = t.Elem()
	}
	var typ string
	s := map[string]any{}
	switch {
	case t == durationType:
		typ = "string"
		s["format"] = "duration"
	case t.Kind() == reflect.Bool:
		typ = "boolean"
	case t.Kind() == reflect.String:
		typ = "string"
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		typ = "number"
	case slices.Contains([]reflect.Kind{reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64}, t.Kind()):
		typ = "integer"
		s["minimum"] = 0
		if t.Kind() == reflect.Uint8 {
			s["maximum"] = 255
		}
	default:
		typ = "integer"
	}
	if nullable {
		s["type"] = []string{typ, "null"}
	} else {
		s["type"] = typ
	}
	return s
}

func join(pattern, seg string) string {
	if pattern == "" {
		return seg
	}
	return pattern + "." + seg
}
