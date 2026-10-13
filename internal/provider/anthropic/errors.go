package anthropic

import (
	"encoding/json"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

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
		if wire.IsContextOverflow(msg) {
			return core.ErrKindContextLength
		}
	}
	return core.ErrKindInvalidRequest
}
