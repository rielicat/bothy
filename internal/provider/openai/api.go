package openai

import "github.com/rielicat/bothy/internal/core"

// Values of providers[].api.
const (
	APIAuto      = "auto"
	APIResponses = "responses"
	APIChat      = "chat"
)

// SigResponses is the SigKind of parts written by the Responses client. It
// differs from KindOpenAI so the facade never sends encrypted reasoning or
// item ids to the Chat Completions client (or the reverse) when a
// provider's api changes.
const SigResponses core.ProviderKind = "openai_responses"

// ResolveAPI returns the wire API a provider uses: an explicit choice, else
// the Responses API for kind openai and Chat Completions for openai_compat,
// whose servers mostly implement only that.
func ResolveAPI(kind core.ProviderKind, api string) string {
	switch api {
	case APIResponses, APIChat:
		return api
	}
	if kind == core.KindOpenAI {
		return APIResponses
	}
	return APIChat
}
