package bedrock

import (
	"regexp"
	"strings"
)

// geoPrefixes are the cross-region inference profile prefixes AWS uses.
var geoPrefixes = map[string]bool{
	"us": true, "eu": true, "apac": true, "jp": true, "au": true, "ca": true, "us-gov": true, "global": true,
}

// modelProviders are the vendor segments of Bedrock model ids; a first
// segment that is none of these but precedes one is a geography.
var modelProviders = map[string]bool{
	"anthropic": true, "amazon": true, "meta": true, "mistral": true, "cohere": true, "ai21": true,
	"deepseek": true, "openai": true, "qwen": true, "writer": true, "twelvelabs": true, "stability": true,
	"luma": true, "minimax": true, "moonshot": true, "google": true, "nvidia": true,
}

var arnModelRe = regexp.MustCompile(`^arn:aws[a-z-]*:bedrock:[a-z0-9-]*:[0-9]*:(?:inference-profile|foundation-model|application-inference-profile)/(.+)$`)

// SplitModel separates a model or inference-profile id, or its ARN, into
// the cross-region geography ("us", "global", "" for an in-region model)
// and the base model id ("anthropic.claude-sonnet-4-5-20250929-v1:0").
// Application inference profiles hide their model behind an opaque id, so
// base is that id.
func SplitModel(id string) (geo, base string) {
	if m := arnModelRe.FindStringSubmatch(id); m != nil {
		id = m[1]
	}
	first, rest, ok := strings.Cut(id, ".")
	if !ok {
		return "", id
	}
	vendor, _, _ := strings.Cut(rest, ".")
	if geoPrefixes[first] || !modelProviders[first] && modelProviders[vendor] {
		return first, rest
	}
	return "", id
}

func isClaude(base string) bool { return strings.HasPrefix(base, "anthropic.claude") }
func isNova(base string) bool   { return strings.HasPrefix(base, "amazon.nova") }

// otherVendor reports a model of a known vendor other than Anthropic; an
// application inference profile hides its model, so it is not one.
func otherVendor(model string) bool {
	_, base := SplitModel(model)
	vendor, _, ok := strings.Cut(base, ".")
	return ok && modelProviders[vendor] && vendor != "anthropic"
}

// claudeNoCache are the Claude generations Bedrock serves without prompt
// caching; a cachePoint block makes their requests fail validation.
var claudeNoCache = regexp.MustCompile(`^anthropic\.claude-(instant|v2|3-haiku|3-sonnet|3-opus|3-5-sonnet)`)

// novaCache are the Nova understanding models with prompt caching (system
// and messages only, never tools).
var novaCache = regexp.MustCompile(`^amazon\.nova-(micro|lite|pro|premier|2-lite)-`)

// SupportsCache reports whether the model takes cachePoint blocks in system
// and messages.
func SupportsCache(model string) bool {
	_, base := SplitModel(model)
	return isClaude(base) && !claudeNoCache.MatchString(base) || novaCache.MatchString(base)
}

// supportsToolCache reports whether a cachePoint may follow the tools.
func supportsToolCache(model string) bool {
	_, base := SplitModel(model)
	return isClaude(base) && !claudeNoCache.MatchString(base)
}

// noForcedToolChoice matches the Claude models that refuse a forced tool
// choice ("any") with a validation error; for them "required" degrades to
// auto and the prompt has to ask for the tool.
var noForcedToolChoice = regexp.MustCompile(`^anthropic\.claude-(opus-5-5|sonnet-5-5|fable-5-1|mythos-5-1)(-|$)`)

func rejectsForcedTool(model string) bool {
	_, base := SplitModel(model)
	return noForcedToolChoice.MatchString(base)
}

// SupportsVision reports image input for the Claude and Nova models whose id
// says so; Nova Micro is text-only.
func SupportsVision(model string) bool {
	_, base := SplitModel(model)
	return isClaude(base) || isNova(base) && !strings.HasPrefix(base, "amazon.nova-micro")
}
