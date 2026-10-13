package anthropic

import (
	"encoding/json"
	"regexp"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// creditRe matches how the API reports exhausted prepaid credit: an
// invalid_request_error ("Your credit balance is too low to access the
// Anthropic API. Please go to Plans & Billing to upgrade or purchase
// credits."), not the documented billing_error.
var creditRe = regexp.MustCompile(`(?i)credit balance|purchase credits|plans (&|and) billing`)

// classify reads the documented error shape
// {"type":"error","error":{"type":…,"message":…},"request_id":…}.
func classify(status int, body []byte) (core.ErrorKind, string) {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Type == "" {
		return wire.StatusKind(status), string(body)
	}
	kind := typeKind(e.Error.Type, e.Error.Message)
	if kind == core.ErrKindInvalidRequest && status >= 500 {
		kind = wire.StatusKind(status)
	}
	return kind, e.Error.Message
}

// typeKind maps the API's error types (also used by in-stream error events).
func typeKind(typ, msg string) core.ErrorKind {
	switch typ {
	case "authentication_error", "permission_error", "billing_error":
		return core.ErrKindAuth
	case "rate_limit_error":
		return core.ErrKindRateLimit
	case "overloaded_error":
		return core.ErrKindOverloaded
	case "api_error", "timeout_error":
		return core.ErrKindServer
	case "request_too_large":
		return core.ErrKindContextLength
	case "invalid_request_error":
		switch {
		case creditRe.MatchString(msg):
			return core.ErrKindAuth
		case wire.IsContextOverflow(msg):
			return core.ErrKindContextLength
		}
	}
	return core.ErrKindInvalidRequest
}
