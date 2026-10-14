package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/ollama"
	"github.com/rielicat/bothy/internal/provider/openai"
	"github.com/rielicat/bothy/internal/provider/wire"
)

const (
	probeTimeout = 60 * time.Second
	// smallContext is where a served Ollama window starts to cripple an
	// agent (SPEC §5.6 degraded profile and below).
	smallContext = 16_384
	probeTool    = "probe_echo"
)

var probeSchema = json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)

// Probe tests a provider before it is saved: it lists models, makes a
// one-token completion and a forced one-tool call, and reports latency, tool
// calling, whether usage was reported, the effective window and the cost.
// Provider failures are reported in the result in plain language; the error
// is only for invalid input. The key is used for these calls and dropped.
func (f *Facade) Probe(ctx context.Context, pc core.ProbeConfig) (core.ProbeResult, error) {
	if !validKind(pc.Kind) {
		return core.ProbeResult{}, fmt.Errorf("provider: unknown kind %q: %w", pc.Kind, core.ErrInvalid)
	}
	switch pc.API {
	case "", openai.APIAuto, openai.APIResponses, openai.APIChat:
	default:
		return core.ProbeResult{}, fmt.Errorf("provider: unknown api %q: %w", pc.API, core.ErrInvalid)
	}
	if pc.Kind == core.KindBedrock {
		return f.probeBedrock(ctx, pc)
	}
	base := strings.TrimRight(pc.BaseURL, "/")
	if base == "" {
		base = defaultBase(pc.Kind)
	}
	if base == "" {
		return core.ProbeResult{}, fmt.Errorf("provider: base_url is required for %s: %w", pc.Kind, core.ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	key := pc.APIKey
	if key.IsZero() && pc.SecretRef != "" {
		if f.d.Vault == nil {
			return core.ProbeResult{Detail: "The vault is unavailable, so the key cannot be read.", Code: core.FailUnknown,
				Error: "no vault"}, nil
		}
		s, err := f.d.Vault.Resolve(ctx, pc.SecretRef, "", "provider:probe")
		if err != nil {
			return core.ProbeResult{Detail: "Could not read the key from the vault: " + err.Error(), Code: cmp.Or(core.CodeOf(err), core.FailUnknown),
				Error: err.Error()}, nil
		}
		key = s
	}
	hc, err := f.httpClient("provider:probe", base, pc.AllowPrivate)
	if err != nil {
		return core.ProbeResult{}, err
	}
	defer hc.CloseIdleConnections()
	creds := &credentials{f: f, id: "probe", secret: key, expires: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)}
	o := wire.Options{ID: "probe", Kind: pc.Kind, BaseURL: base, HTTP: hc, Redact: creds.redact}
	if !key.IsZero() {
		o.Key = creds.key
	}
	api := apiFor(pc.Kind, pc.API)
	// A private memory: what this probe learns about tools and reasoning is
	// reported in its result, and the provider is not saved yet.
	effort := openai.NewEffortMemory(1, nil)
	cl := f.newClient(o, api, effort)

	res := core.ProbeResult{API: api}
	found, derr := cl.Discover(ctx)
	for _, m := range found {
		res.Models = append(res.Models, m.Ref.Model)
	}
	f.annotate(endpoint{kind: pc.Kind, base: base}, found)
	res.Choices = choicesFor(found)
	model := pc.Model
	if i := slices.IndexFunc(found, func(m core.ModelInfo) bool { return m.Recommended }); model == "" && i >= 0 {
		// The probe loads the model setup will suggest, so its window and
		// tool notes are about that one.
		model = found[i].Ref.Model
	}
	if model == "" {
		if derr != nil {
			res.Detail, res.Code, res.Error = plain(derr, base, pc.Kind, api), failureCode(derr, pc.Kind), rawError(derr)
		} else {
			res.Detail, res.Code = "The server lists no models; name one to test.", core.FailNoModels
		}
		return res, nil
	}
	res.Model = model
	info := f.probeInfo(pc.Kind, base, model, found)
	oc, isOllama := cl.(*ollama.Client)
	trained, numCtx := 0, 0
	if isOllama {
		// Ollama loads the model with the window Bothy will send, so the
		// probe also tests that the machine can hold it.
		trained = ollamaWindow(ctx, oc, model, &info)
		numCtx = info.Context
	}

	notes, ok := f.exercise(ctx, probeCall{cl: cl, effort: effort, model: model, info: info, numCtx: numCtx, firstMax: 1,
		base: base, kind: pc.Kind, asked: pc.API, api: api}, &res)
	if !ok {
		return res, nil
	}
	if isOllama {
		notes = append(notes, ollamaContext(ctx, oc, model, trained, &res)...)
	}
	res.Detail = strings.Join(notes, " ")
	return res, nil
}

// probeCall is what exercise needs to test one model.
type probeCall struct {
	cl         client
	effort     *openai.EffortMemory // nil for clients without an OpenAI API choice
	model      string
	info       core.ModelInfo
	numCtx     int
	firstMax   int // output tokens of the first completion
	base       string
	kind       core.ProviderKind
	asked, api string // providers[].api as asked and as resolved
}

// exercise makes the probe's completion and forced tool call. ok is false
// when the completion failed; res.Detail then says why.
func (f *Facade) exercise(ctx context.Context, p probeCall, res *core.ProbeResult) (notes []string, ok bool) {
	t0 := f.clock.Now()
	resp, err := p.cl.Stream(ctx, &core.Request{
		Model: p.model, MaxTokens: p.firstMax, NumCtx: p.numCtx,
		Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "Reply with the single word: ok"}}}},
	}, nil)
	res.Latency = f.clock.Now().Sub(t0)
	if err != nil {
		res.Detail, res.Code, res.Error = plain(err, p.base, p.kind, p.api), failureCode(err, p.kind), rawError(err)
		return nil, false
	}
	res.OK = true
	res.UsageReported = !resp.UsageEstimated
	// A reply cut off at the one-token limit may carry no usage (a Responses
	// incomplete event can have none); the tool call then decides.
	cutShort := resp.UsageEstimated && resp.Stop == core.StopMaxTokens
	res.Cost = p.info.Price.Cost(resp.Usage)

	if n := apiNote(p.kind, p.asked, p.api); n != "" {
		notes = append(notes, n)
	}
	toolTokens := 256
	if p.api == openai.APIResponses {
		// Reasoning counts against max_output_tokens there, and a
		// reasoning model can spend 256 before it calls the tool.
		toolTokens = 1024
	}
	toolReq := &core.Request{
		Model: p.model, MaxTokens: toolTokens, ToolChoice: "required", NumCtx: p.numCtx,
		Tools:    []core.ToolDef{{Name: probeTool, Description: "Echo a value back.", Schema: probeSchema}},
		Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: `Call the probe_echo tool with value "ok".`}}}},
	}
	tresp, terr := p.cl.Stream(ctx, toolReq, nil)
	if terr != nil && forcedChoiceRefused(terr) {
		// Some models (Claude 5.5 on Bedrock, thinking models) refuse a
		// forced tool_choice; the agent never forces one, so ask normally.
		toolReq.ToolChoice = ""
		tresp, terr = p.cl.Stream(ctx, toolReq, nil)
	}
	mode := p.effort.Mode("probe", p.model)
	switch {
	case terr != nil && mode == openai.EffortNoTools:
		notes = append(notes, noToolsOverChat(p.kind, terr))
	case terr != nil:
		notes = append(notes, "Tool calling failed: "+plain(terr, p.base, p.kind, p.api))
	default:
		res.ToolCalling = calledProbe(tresp)
		res.UsageReported = (res.UsageReported || cutShort) && !tresp.UsageEstimated
		res.Cost += p.info.Price.Cost(tresp.Usage)
		if mode == openai.EffortOff {
			notes = append(notes, reasoningOffNote(p.kind))
		}
		switch {
		case res.ToolCalling:
		case tresp.Stop == core.StopMaxTokens:
			// A reasoning model can spend the whole budget thinking; that
			// says nothing about its tool use.
			notes = append(notes, fmt.Sprintf("The model used the test's %d-token output limit before calling the test tool, probably while reasoning, so tool calling could not be confirmed.", toolTokens))
		default:
			notes = append(notes, "The model answered without calling the test tool; tool use may be unreliable.")
		}
	}
	if !res.UsageReported {
		notes = append(notes, "The server does not report token usage; Bothy will estimate it.")
	}
	res.Context = p.info.Context
	return notes, true
}

// probeInfo describes the probed model from the catalog and discovery.
func (f *Facade) probeInfo(kind core.ProviderKind, base, model string, found []core.ModelInfo) core.ModelInfo {
	info := core.ModelInfo{Ref: core.ModelRef{Provider: "probe", Model: model}, Tools: true}
	for _, m := range found {
		if m.Ref.Model == model {
			info = m
		}
	}
	if cm, ok := f.cat.Find(kind, base).Model(model); ok {
		ci := cm.Info(info.Ref)
		if ci.Context == 0 {
			ci.Context = info.Context
		}
		info = ci
	}
	if info.Context == 0 {
		info.Context = defaultContext[kind]
	}
	if kind == core.KindOllama {
		info.Price = core.Price{Known: true}
	}
	return info
}

func calledProbe(r *core.Response) bool {
	for _, p := range r.Message.Parts {
		if p.Type != core.PartToolCall || p.ToolCall == nil || p.ToolCall.Name != probeTool {
			continue
		}
		var args struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(p.ToolCall.Args, &args) == nil && args.Value != "" {
			return true
		}
	}
	return false
}

// ollamaWindow sets the window the facade would send for a model config does
// not size (ollama.Window of the trained and served contexts) and returns the
// trained maximum.
func ollamaWindow(ctx context.Context, oc *ollama.Client, model string, info *core.ModelInfo) int {
	show, err := oc.Show(ctx, model)
	if err != nil {
		return 0
	}
	running, _ := oc.Running(ctx)
	if w := ollama.Window(show.Context, running[model]); w > 0 {
		info.Context = w
	}
	return show.Context
}

// ollamaContext explains the window sent as num_ctx when it is below what
// the model supports, and checks with /api/ps (after the probe loaded the
// model with that num_ctx) that the server really allocated it.
func ollamaContext(ctx context.Context, oc *ollama.Client, model string, trained int, res *core.ProbeResult) []string {
	var notes []string
	window := res.Context
	if trained > window {
		notes = append(notes, fmt.Sprintf("Bothy sends num_ctx %s for %s, less than the %s the model supports, because a larger window needs much more memory. To use more, set context for this model in Bothy's provider settings.",
			thousands(window), model, thousands(trained)))
	}
	running, err := oc.Running(ctx)
	if err != nil {
		return notes
	}
	served := running[model]
	if served <= 0 || served >= window || served >= smallContext {
		return notes
	}
	res.Context = served
	msg := fmt.Sprintf("Ollama is serving %s with a %s context although Bothy asked for %s", model, thousands(served), thousands(window))
	if trained > 0 {
		msg += fmt.Sprintf(" (the model supports %s)", thousands(trained))
	}
	return append(notes, msg+fmt.Sprintf(". Set OLLAMA_CONTEXT_LENGTH=%d where Ollama runs, and check that nothing between Bothy and Ollama drops num_ctx.", window))
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// localHost reports whether a base URL's host is this machine, the Docker
// host or a private address: where a self-hosted model server would run.
func localHost(h string) bool {
	switch strings.ToLower(h) {
	case "localhost", "host.docker.internal":
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// apiNote says which OpenAI API the probe used, for type openai (where the
// choice is automatic) and whenever api was set.
func apiNote(kind core.ProviderKind, asked, api string) string {
	if kind != core.KindOpenAI && (asked == "" || asked == openai.APIAuto) {
		return ""
	}
	switch api {
	case openai.APIResponses:
		return "Tested with the Responses API."
	case openai.APIChat:
		return "Tested with the Chat Completions API."
	}
	return ""
}

// reasoningOffNote explains a Chat Completions endpoint that accepted the
// test tool only with reasoning off.
func reasoningOffNote(kind core.ProviderKind) string {
	msg := `Tool calling works over Chat Completions only with reasoning turned off: the server refuses tools while this model reasons, so Bothy sends them with reasoning_effort "none".`
	if kind == core.KindOpenAI {
		return msg + " To keep reasoning during tool use, set api to responses or auto for this provider."
	}
	return msg + " If this server also offers the Responses API, set api: responses for this provider to keep reasoning during tool use."
}

// noToolsOverChat explains a model that cannot call tools over Chat
// Completions at all, with the provider's own words last.
func noToolsOverChat(kind core.ProviderKind, err error) string {
	msg := "Tool calling failed: this model cannot call tools over Chat Completions, not even with reasoning turned off."
	if kind == core.KindOpenAI {
		msg += " Set api to responses or auto for this provider to use the Responses API, which supports it."
	} else {
		msg += " If this server also offers the Responses API, set api: responses for this provider."
	}
	var pe *core.ProviderError
	if errors.As(err, &pe) {
		msg += " The provider said: " + pe.Msg
	}
	return msg
}

// plain turns a provider failure into a sentence for the setup wizard.
func plain(err error, base string, kind core.ProviderKind, api string) string {
	var pe *core.ProviderError
	if !errors.As(err, &pe) {
		return err.Error()
	}
	host, hostname := base, ""
	if u, perr := url.Parse(base); perr == nil && u.Host != "" {
		host, hostname = u.Host, u.Hostname()
	}
	switch pe.Kind {
	case core.ErrKindAuth:
		if kind == core.KindBedrock {
			return bedrockAuthText(pe.Msg)
		}
		return "The provider rejected the API key: " + pe.Msg
	case core.ErrKindNetwork:
		msg := "Could not reach " + host + ": " + pe.Msg
		// On a Linux host, host.docker.internal is the bridge gateway, and a
		// stock install listens on the host's loopback only.
		switch {
		case kind == core.KindOllama:
			msg += " Is Ollama running? From the Bothy container use http://host.docker.internal:11434 and allow_private;" +
				" on a Linux host, also start Ollama with OLLAMA_HOST=0.0.0.0 so the container can reach it."
		case kind == core.KindOpenAICompat && localHost(hostname):
			msg += " Is the server running? From the Bothy container use http://host.docker.internal:<port>/v1 and allow_private;" +
				" on a Linux host the server must listen on the local network (LM Studio: Serve on Local Network; vLLM: --host 0.0.0.0)."
		}
		return msg
	case core.ErrKindRateLimit:
		return "The provider is rate limiting this key; try again shortly."
	case core.ErrKindInvalidRequest:
		// A 404 that does not mention a model is a missing path: a server
		// without the Responses API.
		if api == openai.APIResponses && pe.Status == http.StatusNotFound && !strings.Contains(strings.ToLower(pe.Msg), "model") {
			return "This server has no Responses API at " + base + "/responses (" + pe.Msg + "). Set api: chat for this provider to use Chat Completions."
		}
		return "The provider rejected the test request: " + pe.Msg
	default:
		return fmt.Sprintf("The provider failed (%s): %s", pe.Kind, pe.Msg)
	}
}

// forcedChoiceRefused reports a request refused only because it forced the
// tool choice.
func forcedChoiceRefused(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "tool_choice") && (strings.Contains(m, "not supported") || strings.Contains(m, "may not be") ||
		strings.Contains(m, "not allowed") || strings.Contains(m, "unsupported") || strings.Contains(m, "forces tool use"))
}
