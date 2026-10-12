package config

import (
	"encoding"
	"reflect"
	"strconv"
	"strings"
)

func envMap(kv []string) map[string]string {
	m := make(map[string]string, len(kv))
	for _, e := range kv {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

// applyEnv sets scalar leaves from BOTHY_<PATH> variables (path segments joined
// by "__"). TEMPORARY: lists, maps, id-keyed items and validation belong to the
// full loader; unparsable values are ignored.
func applyEnv(c *Config, env map[string]string) {
	walkEnv(reflect.ValueOf(c).Elem(), nil, env)
}

var textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()

func walkEnv(v reflect.Value, path []string, env map[string]string) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if key == "" || key == "-" {
			continue
		}
		p := append(path[:len(path):len(path)], strings.ToUpper(key))
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			walkEnv(fv, p, env)
			continue
		}
		if raw, ok := env["BOTHY_"+strings.Join(p, "__")]; ok {
			setScalar(fv, raw)
		}
	}
}

func setScalar(fv reflect.Value, raw string) {
	if reflect.PointerTo(fv.Type()).Implements(textUnmarshaler) {
		tmp := reflect.New(fv.Type())
		if tmp.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(raw)) == nil {
			fv.Set(tmp.Elem())
		}
		return
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		if b, err := strconv.ParseBool(raw); err == nil {
			fv.SetBool(b)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, err := strconv.ParseInt(raw, 10, fv.Type().Bits()); err == nil {
			fv.SetInt(n)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, err := strconv.ParseUint(raw, 10, fv.Type().Bits()); err == nil {
			fv.SetUint(n)
		}
	case reflect.Float32, reflect.Float64:
		if x, err := strconv.ParseFloat(raw, fv.Type().Bits()); err == nil {
			fv.SetFloat(x)
		}
	}
}
