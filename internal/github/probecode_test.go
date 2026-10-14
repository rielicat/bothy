package github

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestProbeCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want core.FailureCode
	}{
		{&apiError{status: 401, msg: "GitHub rejected the credentials"}, core.FailKeyRejected},
		{&rateLimitError{apiError: apiError{status: 403, msg: "GitHub rate limit reached"}}, core.FailRateLimited},
		{fmt.Errorf("probe: %w", &apiError{status: 502, msg: "GitHub is having trouble"}), core.FailProviderError},
		{&apiError{status: 404, msg: "GitHub found nothing"}, core.FailUnknown},
		{fmt.Errorf("github: %w", context.DeadlineExceeded), core.FailTimeout},
		{errors.New("dial tcp: lookup github.example.com: no such host"), core.FailUnreachable},
		{errors.New("the App is not installed on any account yet"), core.FailUnknown},
	} {
		if got := probeCode(tc.err); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.err, got, tc.want)
		}
	}
}
