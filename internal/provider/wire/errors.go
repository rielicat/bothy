package wire

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// RetryAfter parses Retry-After (seconds or an HTTP date) and the
// retry-after-ms variant some APIs send. Zero means absent or unparsable.
func RetryAfter(h http.Header) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After-Ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		if s <= 0 {
			return 0
		}
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// contextRe matches the context-overflow wording of the vendors and of
// OpenAI-compatible servers (vLLM, llama.cpp, LM Studio), which often reuse
// a plain 400 for it.
var contextRe = regexp.MustCompile(`(?i)context[ _-]?(length|window)|maximum context|prompt is too long|too many tokens|input token count|exceeds the maximum number of tokens|reduce the length of the messages`)

// IsContextOverflow reports whether a provider message describes a prompt
// that does not fit the model's window.
func IsContextOverflow(msg string) bool { return contextRe.MatchString(msg) }

// StatusKind is the default status mapping shared by every vendor; clients
// refine it with what their error bodies say.
func StatusKind(status int) core.ErrorKind {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusPaymentRequired:
		return core.ErrKindAuth
	case status == http.StatusTooManyRequests:
		return core.ErrKindRateLimit
	case status == http.StatusRequestTimeout:
		return core.ErrKindNetwork
	case status == http.StatusRequestEntityTooLarge:
		return core.ErrKindContextLength
	case status == http.StatusServiceUnavailable, status == 529:
		return core.ErrKindOverloaded
	case status >= 500:
		return core.ErrKindServer
	default:
		return core.ErrKindInvalidRequest
	}
}
