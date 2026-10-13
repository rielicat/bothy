package agent

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func deltaTexts(evs []core.Event, typ core.EventType) []string {
	var out []string
	for _, ev := range evs {
		if d, ok := ev.Data.(core.DeltaData); ok && ev.Type == typ {
			out = append(out, d.Text)
		}
	}
	return out
}

// Each flush is redacted on its own, so a secret split across two flushes
// used to reach the web (the bus) and the sink unmasked in two halves. A
// flush now publishes only text that no later delta can turn into a secret.
func TestStreamedSecretSplitAcrossFlushesStaysMasked(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		deltas       []string
		want         string
	}{
		{"passphrase", "correct horse battery staple",
			[]string{"The passphrase is correct hor", "se battery staple, keep it."}, "The passphrase is [redacted], keep it."},
		{"token", "sk-proj-abcdefghijklmnopqrstuvwxyz",
			[]string{"Your key: sk-proj-abcdefghij", "klmnopqrstuvwxyz is set."}, "Your key: [redacted] is set."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				block := make(chan struct{})
				h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: tc.deltas, Block: block}}})
				defer h.close()
				h.red.Add("secret", []byte(tc.secret))
				res := h.submit("tell me")
				time.Sleep(time.Second) // many flush intervals pass between the two deltas
				synctest.Wait()
				close(block)
				synctest.Wait()
				for name, chunks := range map[string][]string{
					"bus":  deltaTexts(h.busEvents(res.Run), core.EvRunDelta),
					"sink": deltaTexts(h.sink.All(), core.EvRunDelta),
				} {
					if got := strings.Join(chunks, ""); got != tc.want {
						t.Fatalf("%s text = %q (flushes %q), want %q", name, got, chunks, tc.want)
					}
					if len(chunks) < 2 {
						t.Fatalf("%s got %d flushes; the deltas must straddle a flush", name, len(chunks))
					}
				}
			})
		})
	}
}

func TestStreamCut(t *testing.T) {
	red := fakes.NewRedactor()
	red.Add("pw", []byte("correct horse battery"))
	r := &Runner{d: Deps{Redactor: red}}
	long := strings.Repeat("a", holdbackMax)
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"hello wor", len("hello ")},
		{"hello world ", len("hello world ")},
		{"nospace", 0},
		{"x correct horse ", len("x ")},
		{"x correct horse battery ", len("x correct horse battery ")},
		{long + "/rest", len(long) + 1},
		{long, 0},
		{long + "/correct horse", len(long) + 1},
	} {
		if got := r.streamCut(tc.in); got != tc.want {
			t.Errorf("streamCut(%.30q…) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Tool progress output is chunked by time as well; a secret printed across
// two chunks must not show up in halves either.
func TestToolOutputSecretSplitAcrossChunksStaysMasked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tool := &testTool{name: "cat", run: func(ctx context.Context, env *core.ToolEnv, _ *core.Action) (*core.ToolResult, error) {
			env.Emit(core.ToolOutputData{Stream: 1, Chunk: "password=correct hor"})
			time.Sleep(time.Second)
			env.Emit(core.ToolOutputData{Stream: 1, Chunk: "se battery staple\nok\n"})
			return &core.ToolResult{Content: "done"}, nil
		}}
		h := newHarness(t, opts{tools: []core.Tool{tool}, steps: []fakes.LLMStep{
			{ToolCalls: []core.ToolCall{call("cat", `{}`)}},
			{Text: []string{"Done."}},
		}})
		defer h.close()
		h.red.Add("pw", []byte("correct horse battery staple"))
		res := h.submit("show the file")
		time.Sleep(2 * time.Second) // past the pause inside the tool
		synctest.Wait()
		var chunks []string
		for _, ev := range h.busEvents(res.Run) {
			if d, ok := ev.Data.(core.ToolOutputData); ok {
				chunks = append(chunks, d.Chunk)
			}
		}
		if got := strings.Join(chunks, ""); got != "password=[redacted]\nok\n" {
			t.Fatalf("tool output = %q (chunks %q)", got, chunks)
		}
	})
}
