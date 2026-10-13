package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/events"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

func TestFallbackBeforeFirstEvent(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		primary := newServer(t, jsonReply(status, errBody("null", "server_error", "busy")))
		backup := newServer(t, sse(okStream))
		h := newHarness(t, []config.Provider{
			cfgProvider("a", core.KindOpenAICompat, primary.URL),
			cfgProvider("b", core.KindOpenAICompat, backup.URL),
		}, "a/m1", "b/m2")
		var built []core.ModelRef
		var ev []core.StreamEvent
		resp, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, func(m core.ModelInfo) (*core.Request, error) {
			built = append(built, m.Ref)
			return userReq(m)
		}, func(e core.StreamEvent) error { ev = append(ev, e); return nil })
		if err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		if resp.Message.PlainText() != "Hi" || ci.Ref.String() != "b/m2" {
			t.Fatalf("%d: %q via %s", status, resp.Message.PlainText(), ci.Ref)
		}
		// build is called once per candidate, with that candidate's info.
		if len(built) != 2 || built[0].String() != "a/m1" || built[1].String() != "b/m2" {
			t.Fatalf("%d: built %v", status, built)
		}
		// One retry on the same candidate, then the next candidate.
		if primary.count() != 2 || backup.count() != 1 || len(ci.Attempts) != 3 {
			t.Fatalf("%d: primary %d backup %d attempts %+v", status, primary.count(), backup.count(), ci.Attempts)
		}
		if ci.Attempts[0].Kind == "" || ci.Attempts[2].Err != "" || ci.Attempts[2].Ref.String() != "b/m2" {
			t.Fatalf("%d: attempts %+v", status, ci.Attempts)
		}
		if len(h.sleeps) != 1 || h.sleeps[0] != 250*time.Millisecond {
			t.Fatalf("%d: backoff %v", status, h.sleeps)
		}
		if ci.Usage != (core.Usage{Input: 600, Output: 200, CacheRead: 400}) || ci.UsageEstimated {
			t.Fatalf("%d: usage %+v", status, ci.Usage)
		}
	}
}

func TestRetryAfterHonouredAndCapped(t *testing.T) {
	primary := newServer(t, reply{status: 429, ctype: "application/json", header: map[string]string{"Retry-After": "3"}, body: errBody(`"rate_limit_exceeded"`, "requests", "slow down")}, sse(okStream))
	h := newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, primary.URL)}, "a/m")
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.sleeps) != 1 || h.sleeps[0] != 3*time.Second || primary.count() != 2 {
		t.Fatalf("sleeps %v requests %d", h.sleeps, primary.count())
	}
	// A Retry-After beyond 20s skips the same-candidate retry.
	slow := newServer(t, reply{status: 429, ctype: "application/json", header: map[string]string{"Retry-After": "120"}, body: errBody(`"rate_limit_exceeded"`, "requests", "slow down")})
	backup := newServer(t, sse(okStream))
	h = newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, slow.URL), cfgProvider("b", core.KindOpenAICompat, backup.URL)}, "a/m", "b/m")
	if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil || ci.Ref.Provider != "b" {
		t.Fatalf("%v %+v", err, ci)
	}
	if len(h.sleeps) != 0 || slow.count() != 1 {
		t.Fatalf("sleeps %v requests %d", h.sleeps, slow.count())
	}
}

func TestNoFallbackAfterFirstEvent(t *testing.T) {
	primary := newServer(t, sse(midStream))
	backup := newServer(t, sse(okStream))
	h := newHarness(t, []config.Provider{
		cfgProvider("a", core.KindOpenAICompat, primary.URL),
		cfgProvider("b", core.KindOpenAICompat, backup.URL),
	}, "a/m", "b/m")
	var text string
	_, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, func(e core.StreamEvent) error {
		if e.Type == core.StreamTextDelta {
			text += e.Text
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "mid-reply") {
		t.Fatalf("err %v", err)
	}
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindServer {
		t.Fatalf("provider error not wrapped: %v", err)
	}
	if text != "Partial" || backup.count() != 0 || primary.count() != 1 || len(ci.Attempts) != 1 {
		t.Fatalf("text %q backup %d primary %d attempts %d", text, backup.count(), primary.count(), len(ci.Attempts))
	}
}

func TestNonRetryableErrorsReturn(t *testing.T) {
	for _, tc := range []struct {
		body string
		kind core.ErrorKind
	}{
		{errBody(`"context_length_exceeded"`, "invalid_request_error", "This model's maximum context length is 8192 tokens."), core.ErrKindContextLength},
		{errBody("null", "invalid_request_error", "Invalid value for 'tool_choice'."), core.ErrKindInvalidRequest},
	} {
		primary := newServer(t, jsonReply(400, tc.body))
		backup := newServer(t, sse(okStream))
		h := newHarness(t, []config.Provider{
			cfgProvider("a", core.KindOpenAICompat, primary.URL),
			cfgProvider("b", core.KindOpenAICompat, backup.URL),
		}, "a/m", "b/m")
		_, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != tc.kind {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		// Surfaced to the runner: no retry, no fallback, no breaker penalty.
		if primary.count() != 1 || backup.count() != 0 || len(ci.Attempts) != 1 || ci.Error == "" {
			t.Fatalf("%s: primary %d backup %d", tc.kind, primary.count(), backup.count())
		}
		if hs := h.f.Health(); hs[0].State != stateOK {
			t.Fatalf("%s: health %+v", tc.kind, hs)
		}
	}
}

func TestCircuitBreaker(t *testing.T) {
	bad := newServer(t, jsonReply(500, errBody("null", "server_error", "boom")))
	h := newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, bad.URL)}, "a/m")
	for i := range breakerThreshold {
		if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err == nil {
			t.Fatalf("call %d succeeded", i)
		}
	}
	if bad.count() != 2*breakerThreshold {
		t.Fatalf("requests %d", bad.count())
	}
	if hs := h.f.Health(); hs[0].State != stateDown || hs[0].LastError == "" {
		t.Fatalf("health %+v", hs)
	}
	_, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil)
	if err == nil || bad.count() != 2*breakerThreshold || len(ci.Attempts) != 1 || ci.Attempts[0].Err != "circuit open" {
		t.Fatalf("open breaker still called: %v %d %+v", err, bad.count(), ci.Attempts)
	}
	// After 60s one probe call goes through; failing again re-opens at once.
	h.clock.Advance(breakerOpenFor + time.Second)
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err == nil || bad.count() != 2*breakerThreshold+2 {
		t.Fatalf("half-open: %v %d", err, bad.count())
	}
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err == nil || bad.count() != 2*breakerThreshold+2 {
		t.Fatalf("re-opened breaker still called: %d", bad.count())
	}
	// A success closes it.
	h.clock.Advance(breakerOpenFor + time.Second)
	good := newServer(t, sse(okStream))
	cfg := *h.view.Current()
	cfg.Providers = []config.Provider{cfgProvider("a", core.KindOpenAICompat, good.URL)}
	h.view.set(&cfg)
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
		t.Fatal(err)
	}
	if hs := h.f.Health(); hs[0].State != stateOK {
		t.Fatalf("health %+v", hs)
	}
}

func TestAuthFailureMovesOnAndRaisesBanner(t *testing.T) {
	denied := newServer(t, jsonReply(401, errBody(`"invalid_api_key"`, "invalid_request_error", "Incorrect API key provided: "+canary)))
	backup := newServer(t, sse(okStream))
	h := newHarness(t, []config.Provider{
		cfgProvider("a", core.KindOpenAICompat, denied.URL),
		cfgProvider("b", core.KindOpenAICompat, backup.URL),
	}, "a/m", "b/m")
	bus := events.New(events.Options{Clock: h.clock})
	h.f.d.Bus = bus
	for range 2 {
		if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil || ci.Ref.Provider != "b" {
			t.Fatalf("%v %+v", err, ci)
		}
	}
	// Auth errors are not retried on the same candidate.
	if denied.count() != 2 || len(h.sleeps) != 0 {
		t.Fatalf("denied %d sleeps %v", denied.count(), h.sleeps)
	}
	hs := h.f.Health()
	if hs[0].ID != "a" || hs[0].State != stateAuthFailed || strings.Contains(hs[0].LastError, canary) {
		t.Fatalf("health %+v", hs)
	}
	banners := bus.Banners(core.Filter{System: true})
	if len(banners) != 1 || banners[0].Data.(core.BannerData).Code != "provider_auth" {
		t.Fatalf("banners %+v", banners)
	}
	evs, _ := bus.Since(0, core.Filter{System: true})
	if len(evs) != 1 {
		t.Fatalf("banner published %d times", len(evs))
	}
	findings := h.f.Checks()[0].Run(context.Background())
	if len(findings) != 1 || findings[0].ID != "providers.auth_failed" || findings[0].Severity != core.SevHigh {
		t.Fatalf("findings %+v", findings)
	}
	// The key is re-resolved after an auth failure: a fixed key works at once.
	cfg := *h.view.Current()
	cfg.Providers = []config.Provider{cfgProvider("a", core.KindOpenAICompat, backup.URL)}
	h.view.set(&cfg)
	if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil || ci.Ref.Provider != "a" {
		t.Fatalf("%v %+v", err, ci)
	}
	if len(bus.Banners(core.Filter{System: true})) != 0 {
		t.Fatal("banner not cleared after recovery")
	}
	if len(h.f.Checks()[0].Run(context.Background())) != 0 {
		t.Fatal("finding not cleared")
	}
}

func TestHotReload(t *testing.T) {
	first := newServer(t, sse(okStream))
	h := newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, first.URL)}, "a/m")
	if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil || ci.Ref.String() != "a/m" {
		t.Fatalf("%v %+v", err, ci)
	}
	// The wizard adds a provider and makes it the main model: no restart.
	second := newServer(t, sse(okStream))
	cfg := *h.view.Current()
	cfg.Providers = append(append([]config.Provider(nil), cfg.Providers...), cfgProvider("wizard", core.KindOpenAICompat, second.URL))
	cfg.Models = config.Models{Main: []string{"wizard/new-model"}}
	h.view.set(&cfg)
	if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil || ci.Ref.String() != "wizard/new-model" || second.count() != 1 {
		t.Fatalf("%v %+v", err, ci)
	}
	// Disabling a provider removes it from chains.
	off := false
	cfg2 := cfg
	cfg2.Providers = []config.Provider{cfg.Providers[0], cfg.Providers[1]}
	cfg2.Providers[1].Enabled = &off
	h.view.set(&cfg2)
	if got := h.f.Chain(core.SlotMain, nil); len(got) != 0 {
		t.Fatalf("disabled provider still in chain: %+v", got)
	}
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("expected no model: %v", err)
	}
}

func TestToolLessCandidateSkipped(t *testing.T) {
	notools := newServer(t, sse(okStream))
	tools := newServer(t, sse(okStream))
	no := false
	h := newHarness(t, []config.Provider{
		cfgProvider("a", core.KindOpenAICompat, notools.URL, config.ProviderModel{ID: "small", Tools: &no}),
		cfgProvider("b", core.KindOpenAICompat, tools.URL),
	}, "a/small", "b/big")
	build := func(m core.ModelInfo) (*core.Request, error) {
		r, _ := userReq(m)
		r.Tools = []core.ToolDef{{Name: "shell", Schema: json.RawMessage(`{"type":"object"}`)}}
		return r, nil
	}
	_, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil)
	if err != nil || ci.Ref.String() != "b/big" || notools.count() != 0 {
		t.Fatalf("%v %+v", err, ci)
	}
	if ci.Attempts[0].Err != "model has no tool support" {
		t.Fatalf("attempts %+v", ci.Attempts)
	}
}

func TestPassthroughNoFallback(t *testing.T) {
	bad := newServer(t, jsonReply(500, errBody("null", "server_error", "boom")))
	backup := newServer(t, sse(okStream))
	h := newHarness(t, []config.Provider{
		cfgProvider("a", core.KindOpenAICompat, bad.URL),
		cfgProvider("b", core.KindOpenAICompat, backup.URL),
	}, "a/m", "b/m")
	req, _ := userReq(core.ModelInfo{Ref: core.ModelRef{Model: "m"}})
	_, ci, err := h.f.Passthrough(context.Background(), core.ModelRef{Provider: "a", Model: "m"}, req, nil)
	if err == nil || backup.count() != 0 || bad.count() != 2 || ci.Purpose != "passthrough" {
		t.Fatalf("%v backup %d bad %d %+v", err, backup.count(), bad.count(), ci)
	}
	if _, _, err := h.f.Passthrough(context.Background(), core.ModelRef{Provider: "nope", Model: "m"}, req, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown provider: %v", err)
	}
}

func TestMissingUsageEstimated(t *testing.T) {
	srv := newServer(t, sse(noUsageStream))
	h := newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, srv.URL)}, "a/m")
	var usage *core.Usage
	resp, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, func(e core.StreamEvent) error {
		if e.Type == core.StreamUsage {
			usage = e.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := core.Usage{Input: 2, Output: 4} // "hello" = 5 bytes, "Hello there" = 11 bytes, /3.6
	if !ci.UsageEstimated || ci.Usage != want || resp.Usage != want || usage == nil || *usage != want {
		t.Fatalf("ci %+v resp %+v event %+v", ci.Usage, resp.Usage, usage)
	}
}

func TestCancelledContextIsNotAProviderFailure(t *testing.T) {
	srv := newServer(t, sse(okStream))
	h := newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, srv.URL)}, "a/m")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(core.ErrStopped)
	_, _, err := h.f.Complete(ctx, core.SlotMain, nil, userReq, nil)
	if !errors.Is(err, core.ErrStopped) {
		t.Fatalf("err %v", err)
	}
	if hs := h.f.Health(); hs[0].State != stateOK {
		t.Fatalf("health %+v", hs)
	}
	// A callback error stops the call at once and is returned as is.
	stop := errors.New("sink closed")
	_, _, err = h.f.Complete(context.Background(), core.SlotMain, nil, userReq, func(core.StreamEvent) error { return stop })
	if !errors.Is(err, stop) || srv.count() != 1 {
		t.Fatalf("callback error: %v (requests %d)", err, srv.count())
	}
}

func TestPrepareStripsForeignSignatures(t *testing.T) {
	sig := func(k core.ProviderKind) []core.Part {
		return []core.Part{
			{Type: core.PartThinking, Text: "t-" + string(k), Sig: "s-" + string(k), SigKind: k},
			{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "c", Name: "x", Args: json.RawMessage(`{}`)}, Sig: "fc-" + string(k), SigKind: k},
		}
	}
	var parts []core.Part
	for _, k := range []core.ProviderKind{core.KindAnthropic, core.KindGemini, core.KindOllama} {
		parts = append(parts, sig(k)...)
	}
	parts = append(parts, core.Part{Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Ref: "a.png", Data: []byte{1}}})
	orig := &core.Request{Messages: []core.Message{{Role: core.MsgAssistant, Parts: parts}}}
	got := prepare(orig, core.ModelInfo{Ref: core.ModelRef{Model: "m"}, MaxOutput: 99, Context: 4096}, core.KindAnthropic)
	ps := got.Messages[0].Parts
	var thinking, signed int
	for _, p := range ps {
		if p.Type == core.PartThinking {
			thinking++
			if p.SigKind != core.KindAnthropic {
				t.Fatalf("foreign thinking kept: %+v", p)
			}
		}
		if p.Sig != "" {
			signed++
		}
	}
	if thinking != 1 || signed != 2 {
		t.Fatalf("thinking %d signed %d: %+v", thinking, signed, ps)
	}
	if last := ps[len(ps)-1]; last.Type != core.PartText || !strings.Contains(last.Text, "cannot read images") {
		t.Fatalf("image not replaced for a non-vision model: %+v", last)
	}
	if got.Model != "m" || got.MaxTokens != 99 || got.NumCtx != 0 {
		t.Fatalf("defaults %+v", got)
	}
	// The caller's history is untouched.
	op := orig.Messages[0].Parts
	if len(op) != len(parts) || op[2].Sig != "s-gemini" || op[3].Sig != "fc-gemini" || op[6].Type != core.PartImage {
		t.Fatalf("prepare mutated the caller's parts: %+v", op)
	}
	if prepare(orig, core.ModelInfo{Context: 16384}, core.KindOllama).NumCtx != 16384 {
		t.Fatal("ollama num_ctx default")
	}
}

func TestPrepareDropsNotices(t *testing.T) {
	text := func(s string) []core.Part { return []core.Part{{Type: core.PartText, Text: s}} }
	r := &core.Request{Messages: []core.Message{
		{Role: core.MsgUser, Kind: core.KindNormal, Parts: text("q")},
		{Role: core.MsgAssistant, Kind: core.KindNotice, Parts: text("Stopped by /stop.")},
		{Role: core.MsgUser, Kind: core.KindSteer, Parts: text("more")},
	}}
	got := prepare(r, core.ModelInfo{}, core.KindOpenAI).Messages
	if len(got) != 2 || got[0].Parts[0].Text != "q" || got[1].Parts[0].Text != "more" || len(r.Messages) != 3 {
		t.Fatalf("messages %+v", got)
	}
}

func TestSameRoleMessagesMergedOnTheWire(t *testing.T) {
	srv := newServer(t, sse(okStream))
	h := newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, srv.URL)}, "a/m")
	build := func(m core.ModelInfo) (*core.Request, error) {
		return &core.Request{Model: m.Ref.Model, Messages: []core.Message{
			{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "one"}}},
			{Role: core.MsgUser, Kind: core.KindSteer, Parts: []core.Part{{Type: core.PartText, Text: "two"}}},
			{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "a"}}},
			{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "b"}}},
		}}, nil
	}
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(srv.body(0)), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Content != "one\n\ntwo" || body.Messages[1].Content != "a\nb" {
		t.Fatalf("messages %+v", body.Messages)
	}
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("expected an error without Config and Egress")
	}
	f, err := New(Deps{Config: config.Static(config.Default()), Egress: &sandboxtest.Egress{}})
	if err != nil || f.clock == nil || f.log == nil {
		t.Fatalf("%v", err)
	}
	if hs := f.Health(); len(hs) != 0 {
		t.Fatalf("health %+v", hs)
	}
}

// Run with -race: calls, health, model listing, calibration and config
// reloads share the facade's state.
func TestConcurrentCallsAndReload(t *testing.T) {
	good := newServer(t, sse(okStream))
	denied := newServer(t, jsonReply(401, errBody(`"invalid_api_key"`, "invalid_request_error", "bad key")))
	h := newHarness(t, []config.Provider{
		cfgProvider("bad", core.KindOpenAICompat, denied.URL),
		cfgProvider("good", core.KindOpenAICompat, good.URL),
	}, "bad/m", "good/m")
	h.f.sleep = func(context.Context, time.Duration) error { return nil }
	h.f.d.Bus = events.New(events.Options{Clock: h.clock})
	base := h.view.Current()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 10 {
				if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil || ci.Ref.Provider != "good" {
					t.Errorf("%v %+v", err, ci)
					return
				}
				h.f.Calibrate(core.ModelRef{Provider: "good", Model: "m"}, 100, 90+i)
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			_ = h.f.Health()
			_ = h.f.Models(context.Background())
			_ = h.f.Chain(core.SlotMain, nil)
			_ = h.f.Estimate("some text", core.ModelRef{Provider: "good", Model: "m"})
		}
	})
	wg.Go(func() {
		for i := range 10 {
			cfg := *base
			cfg.Providers = append([]config.Provider(nil), base.Providers...)
			cfg.Providers[1].Models = []config.ProviderModel{{ID: "m", Context: 8192 + i}}
			h.view.set(&cfg)
		}
	})
	wg.Wait()
}
