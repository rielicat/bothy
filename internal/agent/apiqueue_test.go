package agent

import (
	"errors"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// An /v1 request streams its own run to the caller, which cannot follow a
// run queued behind another one: Submit refuses it with ErrBusy (a retryable
// 429) instead of queueing a run nobody reads.
func TestAPIChatRequestIsNeverQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"one"}, Block: block}, {Text: []string{"two"}}}})
		defer h.close()
		first := h.submit("first")
		synctest.Wait()
		api := h.req("from the api")
		api.Trigger, api.Sender = core.TrigAPI, "api:tok_x"
		api.Chat = &core.ChatRef{Channel: "api", ChatID: string(h.sess.ID), Kind: core.ChatAPI}
		res, err := h.r.Submit(h.ctx, api)
		if !errors.Is(err, core.ErrBusy) {
			t.Fatalf("api submit on a busy session = %+v, %v", res, err)
		}
		close(block)
		synctest.Wait()
		if runs := h.runs(); len(runs) != 1 || runs[0].ID != first.Run {
			t.Fatalf("runs = %+v", runs)
		}
		if res, err := h.r.Submit(h.ctx, api); err != nil || res.Run == "" || res.Queued {
			t.Fatalf("api submit on an idle session = %+v, %v", res, err)
		}
		synctest.Wait()
	})
}
