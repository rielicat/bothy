package gemini

import (
	"encoding/json"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// errorPayload is the documented google.rpc error body.
type errorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
	Details []struct {
		Reason string `json:"reason"`
	} `json:"details"`
}

func (e *errorPayload) kind(status int) (core.ErrorKind, string) {
	for _, d := range e.Details {
		// An invalid key is a 400 INVALID_ARGUMENT, not a 401.
		if d.Reason == "API_KEY_INVALID" || d.Reason == "API_KEY_SERVICE_BLOCKED" {
			return core.ErrKindAuth, e.Message
		}
	}
	switch e.Status {
	case "UNAUTHENTICATED", "PERMISSION_DENIED":
		return core.ErrKindAuth, e.Message
	case "RESOURCE_EXHAUSTED":
		if status == 402 {
			return core.ErrKindAuth, e.Message // prepay credit depleted: retrying cannot help
		}
		return core.ErrKindRateLimit, e.Message
	case "UNAVAILABLE":
		return core.ErrKindOverloaded, e.Message
	case "INTERNAL", "DEADLINE_EXCEEDED":
		return core.ErrKindServer, e.Message
	case "CANCELLED":
		return core.ErrKindNetwork, e.Message
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "NOT_FOUND":
		if wire.IsContextOverflow(e.Message) {
			return core.ErrKindContextLength, e.Message
		}
		return core.ErrKindInvalidRequest, e.Message
	}
	return wire.StatusKind(status), e.Message
}

func classify(status int, body []byte) (core.ErrorKind, string) {
	// streamGenerateContent errors may arrive as a one-element array.
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &arr) == nil && len(arr) > 0 {
			body = arr[0]
		}
	}
	var e struct {
		Error *errorPayload `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error == nil {
		return wire.StatusKind(status), trimmed
	}
	return e.Error.kind(status)
}
