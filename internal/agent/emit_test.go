package agent

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// A secret the model streams in two deltas that land in different flushes
// must not reach the bus or the sink: each flush is redacted on its own.
func TestSecretSplitAcrossFlushesStaysRedacted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const secret = "CANARY-0123456789abcdef"
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"The key is CANARY-01234", "56789abcdef, keep it."}, Block: block}}})
		defer h.close()
		h.red.Add("vault:global/canary", []byte(secret))
		res := h.submit("what is the key?")
		time.Sleep(time.Second) // the first delta's flush timer fires while the model blocks
		close(block)
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("run = %s", st)
		}
		var bus strings.Builder
		for _, ev := range h.busEvents(res.Run) {
			if d, ok := ev.Data.(core.DeltaData); ok && ev.Type == core.EvRunDelta {
				bus.WriteString(d.Text)
			}
		}
		for where, text := range map[string]string{"bus": bus.String(), "sink": h.sink.Text()} {
			if strings.Contains(text, "0123456789abcdef") || !strings.Contains(text, "The key is") || !strings.Contains(text, "keep it.") {
				t.Errorf("%s text = %q, want the reply with the secret masked", where, text)
			}
		}
	})
}
