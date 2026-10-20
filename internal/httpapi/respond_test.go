package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// Error messages reach people as they are: without the sentinel's own word,
// in plain copy, with fixes that are current routes (web/src/lib/routes.ts).
func TestMapErrorPlainMessages(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		err       error
		status    int
		code, msg string
		fix       string
	}{
		{fmt.Errorf("%w: you asked for this, so someone else has to approve it", core.ErrForbidden), http.StatusForbidden, "forbidden",
			"you asked for this, so someone else has to approve it", ""},
		{fmt.Errorf("vault: wrong passphrase: %w", core.ErrForbidden), http.StatusForbidden, "forbidden", "vault: wrong passphrase", ""},
		{core.ErrForbidden, http.StatusForbidden, "forbidden", "forbidden", ""},
		{fmt.Errorf("%w: you already voted", core.ErrConflict), http.StatusConflict, "conflict", "you already voted", ""},
		{fmt.Errorf("%w: allow_session was not offered for this approval", core.ErrInvalid), http.StatusBadRequest, "bad_request",
			"allow_session was not offered for this approval", ""},
		{&core.BudgetError{Kind: core.BudgetDailyCost}, http.StatusTooManyRequests, "budget_exceeded",
			"Bothy has reached today's spending limit. It can start again at midnight.", "/settings/general"},
		{&core.BudgetError{Kind: core.BudgetRunCost}, http.StatusTooManyRequests, "budget_exceeded",
			"Bothy stopped because this task reached its limit.", "/settings/advanced"},
		{fmt.Errorf("resolve secret:x: %w", core.ErrSealed), http.StatusServiceUnavailable, "sealed", "Bothy's saved keys are locked.", "/settings/security/keys"},
		{core.ErrNoSandbox, http.StatusServiceUnavailable, "no_sandbox", "Bothy can't run code on this server.", "/settings/security"},
	} {
		status, env, _ := h.srv.mapError(c.err)
		if b := env.Error.Error; status != c.status || b.Code != c.code || b.Message != c.msg || b.Fix != c.fix {
			t.Errorf("%v: %d %+v, want %d %s %q %q", c.err, status, b, c.status, c.code, c.msg, c.fix)
		}
	}
}

type upstreamErr struct{}

func (upstreamErr) Error() string {
	return "GitHub rejected the credentials (wrong, expired or revoked token): Bad credentials"
}
func (upstreamErr) UpstreamStatus() int { return http.StatusUnauthorized }

// Another service refusing (GitHub rejecting the token) is a 502 with its
// reason, not an internal error; a sentinel still decides first.
func TestUpstreamRefusalIsBadGateway(t *testing.T) {
	h := newHarness(t)
	// Only GitHub's own reason: what wraps it can name local paths.
	st, env, _ := h.srv.mapError(fmt.Errorf("github: reading /data/github/checkouts/ws_1/acme/web: %w", upstreamErr{}))
	if st != http.StatusBadGateway || env.Error.Error.Code != "provider_error" || env.Error.Error.Message != (upstreamErr{}).Error() {
		t.Fatalf("upstream refusal = %d %+v", st, env.Error.Error)
	}
	if st, _, _ := h.srv.mapError(fmt.Errorf("%w: %w", core.ErrNotFound, upstreamErr{})); st != http.StatusNotFound {
		t.Fatalf("a sentinel with an upstream cause = %d", st)
	}
}
