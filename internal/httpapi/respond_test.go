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
