package provider

import (
	"errors"
	"net/http"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/bedrock"
)

// Vendors' wording for an account without credit or quota, and for a key
// that works but may not use the model asked for. Bare "billing" and "quota
// exceeded" are left out: OpenAI's per-minute limit links its billing page
// and Google's names the exceeded per-minute quota, both mere rate limits.
var (
	creditWords = []string{"credit balance", "insufficient_quota", "exceeded your current quota", "insufficient credits",
		"insufficient balance", "insufficient funds", "payment required", "out of credits", "more credits", "purchase credits",
		"used all available credits", "spending limit", "spend limit", "enable billing"}
	modelWords = []string{"does not have access to model", "do not have access to", "don't have access to the model",
		"model_not_found", "model not found", "not found: model", "no such model", "unknown model", "invalid model",
		"model identifier is invalid", "not allowed to use", "is not supported for this"}
)

func hasAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// failureCode classifies a failed probe call for the setup screen.
func failureCode(err error, kind core.ProviderKind) core.FailureCode {
	var pe *core.ProviderError
	if !errors.As(err, &pe) {
		if c := core.TransportFailure(err); c != "" {
			return c
		}
		return core.FailUnknown
	}
	msg := strings.ToLower(pe.Msg)
	if kind == core.KindBedrock {
		if c := bedrockFailure(pe, msg); c != "" {
			return c
		}
	}
	switch pe.Kind {
	case core.ErrKindNetwork:
		if c := core.TransportFailure(err); c != "" {
			return c
		}
		return core.FailUnreachable
	case core.ErrKindAuth:
		switch {
		case strings.HasPrefix(pe.Msg, "credential unavailable: "):
			return core.FailUnknown
		// The OpenAI client reports its 429 billing codes (spend and usage
		// limits, exhausted credit) as auth errors; a rejected key is 401/403.
		case pe.Status == http.StatusPaymentRequired || pe.Status == http.StatusTooManyRequests || hasAny(msg, creditWords):
			return core.FailOutOfCredit
		case hasAny(msg, modelWords):
			return core.FailNoModelAccess
		}
		return core.FailKeyRejected
	case core.ErrKindRateLimit:
		if hasAny(msg, creditWords) {
			return core.FailOutOfCredit
		}
		return core.FailRateLimited
	case core.ErrKindOverloaded, core.ErrKindServer:
		if pe.Status == http.StatusOK && strings.HasPrefix(pe.Msg, "malformed stream") &&
			(kind == core.KindOpenAICompat || kind == core.KindOllama) {
			return core.FailNotAModelServer
		}
		return core.FailProviderError
	case core.ErrKindInvalidRequest:
		switch {
		case hasAny(msg, creditWords):
			return core.FailOutOfCredit
		case hasAny(msg, modelWords), pe.Status == http.StatusNotFound && strings.Contains(msg, "model"):
			return core.FailNoModelAccess
		case pe.Status == http.StatusNotFound || pe.Status == http.StatusMethodNotAllowed:
			return core.FailNotAModelServer
		}
	}
	return core.FailUnknown
}

// bedrockFailure names the AWS conditions: no credentials, an identity
// without Bedrock permissions, and models not turned on for the account.
func bedrockFailure(pe *core.ProviderError, msg string) core.FailureCode {
	switch {
	case strings.HasPrefix(pe.Msg, "AWS credentials unavailable: "):
		return core.FailAWSNoCredentials
	case strings.Contains(msg, "use case details"), strings.Contains(msg, "model access"):
		return core.FailAWSNoModels
	case strings.Contains(msg, "don't have access to the model"), strings.Contains(msg, "model identifier is invalid"):
		return core.FailNoModelAccess
	case pe.Kind == core.ErrKindAuth && (strings.Contains(msg, "accessdenied") || strings.Contains(msg, "not authorized")):
		return core.FailAWSDenied
	}
	return ""
}

// rawError is the message behind a failure, for the Details disclosure.
func rawError(err error) string {
	var pe *core.ProviderError
	if errors.As(err, &pe) {
		return bedrock.ScrubAccounts(pe.Msg)
	}
	return bedrock.ScrubAccounts(err.Error())
}
