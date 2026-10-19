package agent

import (
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
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

	interruptedNotice  = "Bothy restarted before it finished this task. Send “retry” to continue."
	stopNotice         = "You stopped Bothy."
	overrideGoneNotice = "The model chosen for this chat isn't available any more, so Bothy used the main model."
	noModelSetNotice   = "No AI model is connected yet. Connect one in Settings → Models."
	textOnlyNotice     = "This AI model can only keep a short conversation in mind, so Bothy can chat with it but not take actions."
)

// budgetNotice says which limit stopped a task, with amounts as money.
func (r *Runner) budgetNotice(be *core.BudgetError) string {
	switch be.Kind {
	case core.BudgetSteps:
		return fmt.Sprintf("Bothy paused after %d steps to check in.", be.Limit)
	case core.BudgetTokens:
		return "Bothy stopped because this task reached its length limit."
	case core.BudgetTime:
		return "Bothy stopped because this task ran out of time."
	case core.BudgetRunCost:
		return fmt.Sprintf("Bothy stopped because this task reached its %s limit.", money(core.Micros(be.Limit)))
	case core.BudgetDailyCost:
		return fmt.Sprintf("Bothy has reached today's %s spending limit. It can start again at midnight (%s).",
			money(core.Micros(be.Limit)), r.resetTime().Location())
	}
	return "Bothy stopped because this task reached a limit."
}

// money formats an amount the way the UI does: "<$0.01", "$12.40", "$1,240".
func money(m core.Micros) string {
	switch {
	case m > 0 && m < 10_000:
		return "<$0.01"
	case m < 1000*1_000_000:
		return fmt.Sprintf("$%.2f", m.USD())
	}
	s := strconv.FormatInt(int64(math.Round(m.USD())), 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return "$" + s
}

// autoPinNotice tells which standing instructions compaction kept pinned.
func autoPinNotice(pinned []string) string {
	quoted := make([]string, len(pinned))
	for i, p := range pinned {
		quoted[i] = "“" + truncateRunes(strings.Join(strings.Fields(p), " "), 200) + "”"
	}
	if len(quoted) == 1 {
		return "Pinned " + quoted[0] + " so Bothy keeps it in mind."
	}
	return "Pinned " + strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1] + " so Bothy keeps them in mind."
}

// forkNotice opens a clean chat continued from another one. It names no
// chat: the line above the transcript says which one it came from.
const forkNotice = "This clean chat starts without the messages, private information or outside content of the chat it came " +
	"from. Bothy's files are shared, so files from outside your team still count as outside content here."

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
	// unread: the key itself was never sent; Bothy could not read it from
	// where it is kept (an aws-sm reference), so replacing it won't help.
	// That holds whatever went wrong reading it, a timeout included: the
	// provider was never contacted, so it isn't the one to blame.
	unread := auth
	for _, a := range ci.Attempts {
		if a.Err == "" {
			continue
		}
		auth = auth && a.Kind == core.ErrKindAuth
		unread = unread && strings.Contains(a.Err, "credential unavailable: ")
		if !slices.Contains(providers, a.Ref.Provider) {
			providers = append(providers, a.Ref.Provider)
		}
	}
	if len(providers) == 0 && pe != nil {
		providers, auth = []string{pe.Provider}, pe.Kind == core.ErrKindAuth
		unread = strings.HasPrefix(pe.Msg, "credential unavailable: ")
	}
	switch {
	case len(providers) == 0:
		return noModelNotice
	case (auth || unread) && !rs.role.IsAdmin():
		return memberAuthNotice
	case unread:
		if n := r.providerName(providers[0]); n != "The AI model" {
			return "Bothy couldn't read its key for " + n + ". Check where the key is kept in Settings → Models."
		}
		return "Bothy couldn't read its AI model's key. Check where the key is kept in Settings → Models."
	case auth:
		return r.providerName(providers[0]) + " stopped accepting Bothy's key."
	case len(providers) > 1:
		return noModelNotice
	}
	return r.providerName(providers[0]) + " had a problem answering."
}
