package openai

import (
	"net/url"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// IsOpenRouter reports whether an OpenAI-compatible base URL is OpenRouter's.
func IsOpenRouter(kind core.ProviderKind, baseURL string) bool {
	if kind != core.KindOpenAICompat {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "openrouter.ai" || strings.HasSuffix(h, ".openrouter.ai")
}

// ExplicitCache reports whether a model behind an OpenAI-compatible server
// caches only at cache_control breakpoints: Anthropic models on OpenRouter
// ("anthropic/…", or the "~anthropic/…-latest" aliases). Without them
// OpenRouter bills every call's whole prompt at the full input price.
func ExplicitCache(kind core.ProviderKind, baseURL, model string) bool {
	return IsOpenRouter(kind, baseURL) && anthropicModel(model)
}

func anthropicModel(model string) bool {
	return strings.HasPrefix(strings.TrimPrefix(strings.ToLower(model), "~"), "anthropic/")
}

func (c *Client) explicitCache(model string) bool { return c.openRouter && anthropicModel(model) }
