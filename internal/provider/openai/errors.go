package openai

import (
	"encoding/json"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// errorPayload is OpenAI's {"message","type","param","code"}; code may be a
// string or, on some compatible servers, a number.
type errorPayload struct {
	Message  string          `json:"message"`
	Type     string          `json:"type"`
	Code     json.RawMessage `json:"code"`
	Metadata *struct {
		Raw          json.RawMessage `json:"raw"`
		ProviderName string          `json:"provider_name"`
	} `json:"metadata"`
}

// upstream adds the routed provider's own message to a router's generic one
// (OpenRouter: "Provider returned error" with the reason in metadata.raw).
func (e *errorPayload) upstream() string {
	if e.Metadata == nil || len(e.Metadata.Raw) == 0 {
		return e.Message
	}
	raw := e.Metadata.Raw
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s)
	}
	var inner struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &inner) == nil {
		var p errorPayload
		switch {
		case len(inner.Error) > 0 && inner.Error[0] == '{' && json.Unmarshal(inner.Error, &p) == nil && p.Message != "":
			msg = p.Message
		case len(inner.Error) > 0 && json.Unmarshal(inner.Error, &s) == nil && s != "":
			msg = s
		case inner.Message != "":
			msg = inner.Message
		}
	}
	if msg == "" || strings.Contains(e.Message, msg) {
		return e.Message
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if e.Metadata.ProviderName != "" {
		msg = e.Metadata.ProviderName + ": " + msg
	}
	return e.Message + " (" + msg + ")"
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
	// An exhausted quota arrives as a 429, but retrying cannot help: like
	// Anthropic's billing_error it is a credential problem for the admin.
	case code == "invalid_api_key" || typ == "authentication_error" || billingCodes[code] || typ == "insufficient_quota":
		return core.ErrKindAuth, e.Message
	case code == "content_filter" || code == "content_policy_violation" || policyCodes[code]:
		return core.ErrKindContentFilter, e.Message
	case code == "rate_limit_exceeded" || typ == "rate_limit_error" || code == "slow_down":
		return core.ErrKindRateLimit, e.Message
	case code == "server_is_overloaded" || typ == "service_unavailable_error":
		return core.ErrKindOverloaded, e.Message
	// The Responses API reports in-stream failures (response.failed) with a
	// code and no type.
	case (typ == "server_error" || code == "server_error" || code == "vector_store_timeout") && status < 500:
		return core.ErrKindServer, e.Message
	}
	return wire.StatusKind(status), e.Message
}

// billingCodes are the 429 codes of an account that cannot pay; the type
// may still say insufficient_quota (error-codes guide).
var billingCodes = map[string]bool{
	"insufficient_quota":                true,
	"credit_balance_exhausted":          true,
	"organization_spend_limit_exceeded": true,
	"project_spend_limit_exceeded":      true,
	"organization_usage_limit_exceeded": true,
}

// policyCodes end a Responses stream (response.failed) for a policy
// violation; repeating the request cannot succeed.
var policyCodes = map[string]bool{
	"bio_policy":                     true,
	"cyber_policy":                   true,
	"misalignment_policy_violation":  true,
	"image_content_policy_violation": true,
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
		p.Message = p.upstream()
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
