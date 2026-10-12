package vault

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
)

// NewSlogHandler wraps h so the message and every string attribute pass through
// r.Redact. Errors, Stringers, TextMarshalers and byte slices are rendered to
// text first; other values are redacted through their JSON form and replaced
// only when that changes anything.
func NewSlogHandler(h slog.Handler, r *Redactor) slog.Handler {
	if r == nil {
		return h
	}
	return &slogHandler{h: h, r: r}
}

type slogHandler struct {
	h slog.Handler
	r *Redactor
}

func (s *slogHandler) Enabled(ctx context.Context, l slog.Level) bool { return s.h.Enabled(ctx, l) }

func (s *slogHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, s.r.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(s.attr(a))
		return true
	})
	return s.h.Handle(ctx, out)
}

func (s *slogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(as))
	for i, a := range as {
		red[i] = s.attr(a)
	}
	return &slogHandler{h: s.h.WithAttrs(red), r: s.r}
}

func (s *slogHandler) WithGroup(name string) slog.Handler {
	return &slogHandler{h: s.h.WithGroup(name), r: s.r}
}

func (s *slogHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, s.r.Redact(v.String()))
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i := range g {
			out[i] = s.attr(g[i])
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		return slog.Attr{Key: a.Key, Value: s.anyValue(v.Any())}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func (s *slogHandler) anyValue(x any) slog.Value {
	switch t := x.(type) {
	case nil:
		return slog.AnyValue(nil)
	case error:
		return slog.StringValue(s.r.Redact(t.Error()))
	case fmt.Stringer:
		return slog.StringValue(s.r.Redact(t.String()))
	case encoding.TextMarshaler:
		b, err := t.MarshalText()
		if err != nil {
			return slog.StringValue(s.r.Redact(fmt.Sprintf("%+v", x)))
		}
		return slog.StringValue(s.r.Redact(string(b)))
	case []byte:
		return slog.StringValue(s.r.Redact(string(t)))
	}
	// The text handler prints %+v and the JSON handler marshals, so both forms
	// are checked; json:"-" fields show up only in the first.
	txt := fmt.Sprintf("%+v", x)
	if red := s.r.Redact(txt); red != txt {
		return slog.StringValue(red)
	}
	b, err := json.Marshal(x)
	if err != nil {
		return slog.StringValue(txt)
	}
	if red := s.r.RedactBytes(b); !bytes.Equal(red, b) {
		return slog.StringValue(string(red))
	}
	return slog.AnyValue(x)
}
