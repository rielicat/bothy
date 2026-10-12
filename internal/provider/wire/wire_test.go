package wire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

func readAll(t *testing.T, s string) []Event {
	t.Helper()
	r := NewSSE(strings.NewReader(s))
	var out []Event
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
}

func TestSSE(t *testing.T) {
	evs := readAll(t, ": comment\r\nevent: a\r\ndata: one\r\ndata:two\r\n\r\nid: 7\nretry: 10\ndata: {\"x\":1}\n\n\n\ndata: tail")
	if len(evs) != 3 {
		t.Fatalf("%+v", evs)
	}
	if evs[0].Name != "a" || string(evs[0].Data) != "one\ntwo" {
		t.Fatalf("first %+v", evs[0])
	}
	if evs[1].Name != "" || string(evs[1].Data) != `{"x":1}` {
		t.Fatalf("second %+v", evs[1])
	}
	if string(evs[2].Data) != "tail" {
		t.Fatalf("an event without its blank line at EOF must still dispatch: %+v", evs[2])
	}
	if evs := readAll(t, "event: ping\n\n"); len(evs) != 1 || evs[0].Name != "ping" || len(evs[0].Data) != 0 {
		t.Fatalf("name-only event %+v", evs)
	}
	if evs := readAll(t, ""); len(evs) != 0 {
		t.Fatal("empty stream")
	}
}

func TestSSEBounded(t *testing.T) {
	r := NewSSE(strings.NewReader("data: " + strings.Repeat("x", MaxEventBytes+1) + "\n\n"))
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("err %v", err)
	}
	many := strings.Repeat("data: "+strings.Repeat("y", 1<<20)+"\n", 9)
	r = NewSSE(strings.NewReader(many + "\n"))
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("multi-line err %v", err)
	}
}

func TestRepairArgs(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"", "{}", true},
		{`{"a": 1, "b": [1, 2]}`, `{"a":1,"b":[1,2]}`, true},
		{`{"n": 1.50}`, `{"n":1.50}`, true},
		{"```json\n{\"a\":1}\n```", `{"a":1}`, true},
		{`"{\"city\":\"Paris\"}"`, `{"city":"Paris"}`, true},
		{`{"a":1,}`, `{"a":1}`, true},
		{`{"list":[1,2,],"s":"x,}"}`, `{"list":[1,2],"s":"x,}"}`, true},
		{`{"text":"unterminated`, `{"text":"unterminated"}`, true},
		{`{"a":{"b":[1,2`, `{"a":{"b":[1,2]}}`, true},
		{`{"a":`, `{"a":null}`, true},
		{`[1,2]`, `{}`, false},
		{`not json`, `{}`, false},
		{`{"a":1} {"b":2}`, `{}`, false},
	} {
		got, ok := RepairArgs(tc.in)
		if string(got) != tc.want || ok != tc.ok {
			t.Errorf("%q: got %s %v, want %s %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	h := http.Header{}
	if RetryAfter(h) != 0 {
		t.Fatal("absent")
	}
	h.Set("Retry-After", "2")
	if RetryAfter(h) != 2*time.Second {
		t.Fatal("seconds")
	}
	h.Set("Retry-After-Ms", "1500")
	if RetryAfter(h) != 1500*time.Millisecond {
		t.Fatal("milliseconds win")
	}
	h = http.Header{}
	h.Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	if d := RetryAfter(h); d < 59*time.Minute || d > time.Hour {
		t.Fatalf("date %v", d)
	}
	h.Set("Retry-After", "soon")
	if RetryAfter(h) != 0 {
		t.Fatal("garbage")
	}
}

func TestStatusKindAndOverflow(t *testing.T) {
	for status, want := range map[int]core.ErrorKind{
		400: core.ErrKindInvalidRequest, 401: core.ErrKindAuth, 402: core.ErrKindAuth, 403: core.ErrKindAuth,
		404: core.ErrKindInvalidRequest, 408: core.ErrKindNetwork, 413: core.ErrKindContextLength,
		429: core.ErrKindRateLimit, 500: core.ErrKindServer, 502: core.ErrKindServer, 503: core.ErrKindOverloaded,
		504: core.ErrKindServer, 529: core.ErrKindOverloaded,
	} {
		if got := StatusKind(status); got != want {
			t.Errorf("%d: %s, want %s", status, got, want)
		}
	}
	for _, msg := range []string{
		"prompt is too long: 208310 tokens > 200000 maximum",
		"This model's maximum context length is 8192 tokens.",
		"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).",
		"context_length_exceeded",
	} {
		if !IsContextOverflow(msg) {
			t.Errorf("not recognised: %q", msg)
		}
	}
	if IsContextOverflow("Invalid value for 'tool_choice'.") {
		t.Error("false positive")
	}
}

func TestBuilderOrderAndDrops(t *testing.T) {
	var evs []core.StreamEvent
	b := NewBuilder(core.KindAnthropic, func(e core.StreamEvent) error { evs = append(evs, e); return nil })
	_ = b.Thinking("think")
	b.Sign("sig")
	b.Close()
	_ = b.Text("a")
	_ = b.Text("b")
	c1, _ := b.StartCall("", "one")
	c2, _ := b.StartCall("id2", "two")
	_ = b.FinishCall(c2, `{"x":1}`, "")
	_ = b.FinishCall(c2, `{"x":2}`, "") // finishing twice is a no-op
	_ = b.Text("after")
	_ = c1 // never finished: truncated output
	resp := b.Response("m", core.StopEnd, core.Usage{Output: 3}, false)
	ps := resp.Message.Parts
	if len(ps) != 4 || ps[0].Type != core.PartThinking || ps[0].Sig != "sig" || ps[0].SigKind != core.KindAnthropic ||
		ps[1].Text != "ab" || ps[2].ToolCall.ID != "id2" || string(ps[2].ToolCall.Args) != `{"x":1}` || ps[3].Text != "after" {
		t.Fatalf("parts %+v", ps)
	}
	if resp.Stop != core.StopTool || resp.Message.Role != core.MsgAssistant || resp.Message.Tokens != 3 {
		t.Fatalf("resp %+v", resp)
	}
	if !IsSynthesized(c1.ID) || IsSynthesized("toolu_01") {
		t.Fatal("synthesized ids")
	}
	if a, b := SynthesizedID(), SynthesizedID(); a == b {
		t.Fatal("ids repeat")
	}
	var starts int
	for _, e := range evs {
		if e.Type == core.StreamToolCallStart {
			starts++
		}
	}
	if starts != 2 {
		t.Fatalf("starts %d", starts)
	}
	// A signature with no open part gets an empty thinking part, never a call.
	b = NewBuilder(core.KindGemini, nil)
	c, _ := b.StartCall("", "f")
	_ = b.FinishCall(c, "{}", "")
	b.Sign("late")
	ps = b.Response("m", core.StopEnd, core.Usage{}, true).Message.Parts
	if len(ps) != 2 || ps[0].Sig != "" || ps[1].Type != core.PartThinking || ps[1].Sig != "late" {
		t.Fatalf("late signature %+v", ps)
	}
}

func TestOptionsErrors(t *testing.T) {
	o := &Options{ID: "p", Redact: func(s string) string { return strings.ReplaceAll(s, "SECRET", "[redacted]") }}
	err := o.credentialError(errors.New("bad SECRET"))
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindAuth || strings.Contains(pe.Msg, "SECRET") {
		t.Fatalf("%v", err)
	}
	if errors.As(o.credentialError(core.ErrSealed), &pe); pe.Kind != core.ErrKindServer {
		t.Fatalf("sealed kind %s", pe.Kind)
	}
	long := o.redact(strings.Repeat("é", 400))
	if len(long) > 520 || !strings.HasSuffix(long, "…") {
		t.Fatalf("truncation %d", len(long))
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(core.ErrStopped)
	if err := o.NetError(ctx, errors.New("reset")); !errors.Is(err, core.ErrStopped) {
		t.Fatalf("cancelled context: %v", err)
	}
	if err := o.NetError(context.Background(), errors.New("reset")); !errors.As(err, &pe) || pe.Kind != core.ErrKindNetwork {
		t.Fatalf("network: %v", err)
	}
}
