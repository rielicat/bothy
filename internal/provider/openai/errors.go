package openai

import (
	"encoding/json"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/wire"
)

// errorPayload is OpenAI's {"message","type","param","code"}; code may be a
// string or, on some compatible servers, a number.
type errorPayload struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

func (e *errorPayload) code() string {
	var s string
	if json.Unmarshal(e.Code, &s) == nil {
		return s
	}
	return strings.Trim(string(e.Code), `"`)
}

func (e *errorPayload) kind(status int) (core.ErrorKind, string) {
	code, typ := e.code(), e.Type
	switch {
	case code == "context_length_exceeded" || code == "string_above_max_length" || overflowStatus(status) && wire.IsContextOverflow(e.Message):
		return core.ErrKindContextLength, e.Message
	case code == "invalid_api_key" || typ == "authentication_error":
		return core.ErrKindAuth, e.Message
	case code == "content_filter" || code == "content_policy_violation":
		return core.ErrKindContentFilter, e.Message
	case code == "rate_limit_exceeded" || typ == "rate_limit_error" || code == "slow_down":
		return core.ErrKindRateLimit, e.Message
	case code == "server_is_overloaded" || typ == "service_unavailable_error":
		return core.ErrKindOverloaded, e.Message
	case typ == "server_error" && status < 500:
		return core.ErrKindServer, e.Message
	}
	return wire.StatusKind(status), e.Message
}

// classify reads {"error":{…}} and the looser shapes compatible servers use:
// {"error":"text"}, {"message":…} and {"detail":…}.
func classify(status int, body []byte) (core.ErrorKind, string) {
	var obj struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &obj) != nil {
		msg := strings.TrimSpace(string(body))
		if overflowStatus(status) && wire.IsContextOverflow(msg) {
			return core.ErrKindContextLength, msg
		}
		return wire.StatusKind(status), msg
	}
	var p errorPayload
	if len(obj.Error) > 0 && obj.Error[0] == '{' && json.Unmarshal(obj.Error, &p) == nil {
		return p.kind(status)
	}
	msg := obj.Message
	var s string
	if len(obj.Error) > 0 && json.Unmarshal(obj.Error, &s) == nil && s != "" {
		msg = s
	}
	if msg == "" && len(obj.Detail) > 0 {
		if json.Unmarshal(obj.Detail, &s) == nil {
			msg = s
		} else {
			msg = string(obj.Detail)
		}
	}
	p = errorPayload{Message: msg}
	return p.kind(status)
}

// overflowStatus limits message sniffing to the statuses servers use for an
// oversized prompt (200 for in-stream errors), so a 429 about "too many
// tokens" per minute is not mistaken for a context overflow.
func overflowStatus(status int) bool {
	return status == 200 || status == 400 || status == 413 || status == 422
}
