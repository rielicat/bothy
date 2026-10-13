package httpapi

import (
	"net/http"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// A busy session answers a retryable 429 with Retry-After; a submit that was
// queued must never be reported as "steered … added to it", which tells the
// client the message is handled when it is not.
func TestV1BusyAndQueuedAreNotSteered(t *testing.T) {
	h := newHarness(t).setup()
	tok := h.tokenFor(h.member, "chat")
	body := map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": "hi"}}}

	h.runner.Err = core.ErrBusy
	rec := h.do("POST", "/v1/chat/completions", body, tok)
	expect(t, rec, http.StatusTooManyRequests)
	if e := decodeAs[oaError](t, rec); e.Error.Code != "busy" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("busy = %+v %v", e, rec.Header())
	}

	h.runner.Err = nil
	h.runner.Result = core.SubmitResult{Queued: true}
	rec = h.do("POST", "/v1/chat/completions", body, tok)
	if e := decodeAs[oaError](t, rec); rec.Code == http.StatusOK || e.Error.Code == "steered" {
		t.Fatalf("queued submit = %d %+v", rec.Code, e)
	}
	h.runner.Result = core.SubmitResult{Steered: true}
	rec = h.do("POST", "/v1/chat/completions", body, tok)
	if e := decodeAs[oaError](t, rec); rec.Code != http.StatusConflict || e.Error.Code != "steered" {
		t.Fatalf("steered submit = %d %+v", rec.Code, e)
	}
}
