package agent

import (
	"net/url"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// Run-end notices follow the UI's error formula: what happened, said once in
// plain words. The raw provider message stays in the run's error, which the
// UI shows under Details, so it is never repeated in the notice.
const (
	noModelNotice    = "No AI model could answer right now."
	memberAuthNotice = "Bothy can't reach its AI model right now. An admin has been told."
	failedNotice     = "Bothy couldn't finish this task."
	loopNotice       = "Bothy stopped because it kept repeating the same step. Try asking in a different way."
)

// providerNames name the companies behind provider types and well-known
// OpenAI-compatible hosts; other servers are "The AI model".
var (
	providerNames = map[string]string{
		string(core.KindAnthropic): "Anthropic", string(core.KindOpenAI): "OpenAI", string(core.KindGemini): "Google Gemini",
		string(core.KindOllama): "Ollama", string(core.KindBedrock): "Amazon Bedrock",
	}
	compatHosts = map[string]string{
		"openrouter.ai": "OpenRouter", "api.groq.com": "Groq", "api.x.ai": "xAI", "api.deepseek.com": "DeepSeek",
		"api.mistral.ai": "Mistral", "api.together.xyz": "Together AI", "api.fireworks.ai": "Fireworks AI",
	}
)

// providerName is how a notice names the provider configured under id.
func (r *Runner) providerName(id string) string {
	for _, p := range r.cfg().Providers {
		if p.ID != id {
			continue
		}
		if n, ok := providerNames[p.Type]; ok {
			return n
		}
		if u, err := url.Parse(p.BaseURL); err == nil {
			if n, ok := compatHosts[strings.ToLower(u.Hostname())]; ok {
				return n
			}
		}
		break
	}
	return "The AI model"
}

// modelErrorNotice says which party failed to answer: the one provider that
// was tried, or every model when the fallback chain ran out.
func (r *Runner) modelErrorNotice(rs *runState, ci core.CallInfo, pe *core.ProviderError) string {
	var providers []string
	auth := len(ci.Attempts) > 0
	for _, a := range ci.Attempts {
		if a.Err == "" {
			continue
		}
		auth = auth && a.Kind == core.ErrKindAuth
		if !slices.Contains(providers, a.Ref.Provider) {
			providers = append(providers, a.Ref.Provider)
		}
	}
	if len(providers) == 0 && pe != nil {
		providers, auth = []string{pe.Provider}, pe.Kind == core.ErrKindAuth
	}
	switch {
	case len(providers) == 0:
		return noModelNotice
	case auth && (rs.user == nil || !rs.user.Role.IsAdmin()):
		return memberAuthNotice
	case auth:
		return r.providerName(providers[0]) + " stopped accepting Bothy's key."
	case len(providers) > 1:
		return noModelNotice
	}
	return r.providerName(providers[0]) + " had a problem answering."
}
