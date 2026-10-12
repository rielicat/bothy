package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/provider/ollama"
	"github.com/bothyhq/bothy/internal/provider/wire"
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
			return core.ProbeResult{Detail: "The vault is unavailable, so the key cannot be read."}, nil
		}
		s, err := f.d.Vault.Resolve(ctx, pc.SecretRef, "", "provider:probe")
		if err != nil {
			return core.ProbeResult{Detail: "Could not read the key from the vault: " + err.Error()}, nil
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
	cl := newClient(o)

	var res core.ProbeResult
	found, derr := cl.Discover(ctx)
	for _, m := range found {
		res.Models = append(res.Models, m.Ref.Model)
	}
	model := pc.Model
	if model == "" && len(res.Models) > 0 {
		model = res.Models[0]
	}
	if model == "" {
		if derr != nil {
			res.Detail = plain(derr, base, pc.Kind)
		} else {
			res.Detail = "The server lists no models; name one to test."
		}
		return res, nil
	}
	info := f.probeInfo(pc.Kind, base, model, found)

	t0 := f.clock.Now()
	resp, err := cl.Stream(ctx, &core.Request{
		Model: model, MaxTokens: 1,
		Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "Reply with the single word: ok"}}}},
	}, nil)
	res.Latency = f.clock.Now().Sub(t0)
	if err != nil {
		res.Detail = plain(err, base, pc.Kind)
		return res, nil
	}
	res.OK = true
	res.UsageReported = !resp.UsageEstimated
	res.Cost = info.Price.Cost(resp.Usage)

	var notes []string
	tresp, terr := cl.Stream(ctx, &core.Request{
		Model: model, MaxTokens: 256, ToolChoice: "required",
		Tools:    []core.ToolDef{{Name: probeTool, Description: "Echo a value back.", Schema: probeSchema}},
		Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: `Call the probe_echo tool with value "ok".`}}}},
	}, nil)
	if terr != nil {
		notes = append(notes, "Tool calling failed: "+plain(terr, base, pc.Kind))
	} else {
		res.ToolCalling = calledProbe(tresp)
		res.UsageReported = res.UsageReported && !tresp.UsageEstimated
		res.Cost += info.Price.Cost(tresp.Usage)
		if !res.ToolCalling {
			notes = append(notes, "The model answered without calling the test tool; tool use may be unreliable.")
		}
	}
	if !res.UsageReported {
		notes = append(notes, "The server does not report token usage; Bothy will estimate it.")
	}
	res.Context = info.Context
	if oc, ok := cl.(*ollama.Client); ok {
		if hint := ollamaContext(ctx, oc, model, &res); hint != "" {
			notes = append(notes, hint)
		}
	}
	res.Detail = strings.Join(notes, " ")
	return res, nil
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

// ollamaContext reports the window Ollama actually allocated (from /api/ps
// after the probe loaded the model) and explains a small one: requests
// without num_ctx get the server default, which is 4,096 on small GPUs.
func ollamaContext(ctx context.Context, oc *ollama.Client, model string, res *core.ProbeResult) string {
	show, err := oc.Show(ctx, model)
	if err == nil && show.Context > 0 {
		res.Context = show.Context
	}
	running, err := oc.Running(ctx)
	if err != nil {
		return ""
	}
	served := running[model]
	if served <= 0 {
		return ""
	}
	res.Context = served
	if served >= smallContext || show.Context > 0 && served >= show.Context {
		return ""
	}
	want := 32_768
	if show.Context > 0 {
		want = min(want, show.Context)
	}
	msg := fmt.Sprintf("Ollama is serving %s with a %s context", model, thousands(served))
	if show.Context > 0 {
		msg += fmt.Sprintf(" (the model supports %s)", thousands(show.Context))
	}
	return msg + fmt.Sprintf(". Set OLLAMA_CONTEXT_LENGTH=%d where Ollama runs, or set context: %d for this model in Bothy's provider settings so it is sent as num_ctx.", want, want)
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// plain turns a provider failure into a sentence for the setup wizard.
func plain(err error, base string, kind core.ProviderKind) string {
	var pe *core.ProviderError
	if !errors.As(err, &pe) {
		return err.Error()
	}
	host := base
	if u, perr := url.Parse(base); perr == nil && u.Host != "" {
		host = u.Host
	}
	switch pe.Kind {
	case core.ErrKindAuth:
		return "The provider rejected the API key: " + pe.Msg
	case core.ErrKindNetwork:
		msg := "Could not reach " + host + ": " + pe.Msg
		if kind == core.KindOllama {
			msg += " Is Ollama running? From the Bothy container use http://host.docker.internal:11434 and allow_private."
		}
		return msg
	case core.ErrKindRateLimit:
		return "The provider is rate limiting this key; try again shortly."
	case core.ErrKindInvalidRequest:
		return "The provider rejected the test request: " + pe.Msg
	default:
		return fmt.Sprintf("The provider failed (%s): %s", pe.Kind, pe.Msg)
	}
}
