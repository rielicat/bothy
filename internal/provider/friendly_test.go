package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestPrettyID(t *testing.T) {
	for id, want := range map[string]string{
		"qwen3:14b":                   "Qwen3 14B",
		"deepseek-r1:8b-q4_K_M":       "DeepSeek R1 8B",
		"llama3.1:8b-instruct-q4_K_M": "Llama3.1 8B Instruct",
		"gpt-oss:20b":                 "GPT OSS 20B",
		"qwen/qwen3-coder-30b":        "Qwen3 Coder 30B",
		"mistral:latest":              "Mistral",
		"o3":                          "o3",
	} {
		if got := prettyID(id); got != want {
			t.Errorf("prettyID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestProviderNames(t *testing.T) {
	for _, tc := range []struct {
		e    endpoint
		want string
	}{
		{endpoint{kind: core.KindAnthropic}, "Anthropic"},
		{endpoint{kind: core.KindOpenAI}, "OpenAI"},
		{endpoint{kind: core.KindGemini}, "Google Gemini"},
		{endpoint{kind: core.KindBedrock, base: "https://bedrock-runtime.us-east-1.amazonaws.com"}, "Amazon Bedrock"},
		{endpoint{kind: core.KindOllama, base: "http://host.docker.internal:11434"}, "Ollama"},
		{endpoint{kind: core.KindOpenAICompat, base: "https://openrouter.ai/api/v1"}, "OpenRouter"},
		{endpoint{kind: core.KindOpenAICompat, base: "https://api.x.ai/v1"}, "xAI"},
		{endpoint{kind: core.KindOpenAICompat, base: "http://host.docker.internal:1234/v1"}, "LM Studio"},
		{endpoint{kind: core.KindOpenAICompat, base: "http://localhost:9000/v1"}, "Model server"},
		{endpoint{kind: core.KindOpenAICompat, base: "https://models.example.com/v1"}, "models.example.com"},
	} {
		if got := tc.e.providerName(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.e, got, tc.want)
		}
	}
}

func models(ids ...string) []core.ModelInfo {
	var out []core.ModelInfo
	for _, id := range ids {
		display, rest, _ := strings.Cut(id, "=")
		if rest != "" {
			id, display = display, rest
		} else {
			display = ""
		}
		out = append(out, core.ModelInfo{Ref: core.ModelRef{Provider: "p", Model: id}, Display: display, Tools: true})
	}
	return out
}

// suggested returns the ids marked recommended and quick tasks default and
// checks that exactly one of each is marked.
func suggested(t *testing.T, ms []core.ModelInfo) (main, quick string) {
	t.Helper()
	var mains, quicks int
	for _, m := range ms {
		if m.Recommended {
			main = m.Ref.Model
			mains++
		}
		if m.QuickTasksDefault {
			quick = m.Ref.Model
			quicks++
		}
		if m.DisplayName == "" || m.ShortName == "" || m.ProviderName == "" {
			t.Errorf("unnamed model %+v", m)
		}
	}
	if mains != 1 || quicks != 1 {
		t.Fatalf("%d recommended and %d quick tasks defaults in %+v", mains, quicks, ms)
	}
	return main, quick
}

func TestSuggestions(t *testing.T) {
	f := newHarness(t, nil).f
	for _, tc := range []struct {
		name        string
		e           endpoint
		ms          []core.ModelInfo
		main, quick string
	}{
		{"anthropic", endpoint{kind: core.KindAnthropic},
			models("claude-sonnet-4-5-20250929=Claude Sonnet 4.5", "claude-sonnet-4-6=Claude Sonnet 4.6", "claude-opus-4-6=Claude Opus 4.6",
				"claude-haiku-4-5-20251001=Claude Haiku 4.5", "claude-sonnet-4-20250514=Claude Sonnet 4"),
			"claude-sonnet-4-6", "claude-haiku-4-5-20251001"},
		{"openai", endpoint{kind: core.KindOpenAI, base: "https://api.openai.com/v1"},
			models("gpt-5", "gpt-5.1", "gpt-5-2025-08-07", "gpt-5-mini", "gpt-5.1-codex", "o3", "gpt-4.1-mini"),
			"gpt-5.1", "gpt-5-mini"},
		{"gemini", endpoint{kind: core.KindGemini},
			models("gemini-2.5-pro", "gemini-3-pro-preview", "gemini-2.5-flash", "gemini-3.1-flash-lite"),
			"gemini-3-pro-preview", "gemini-3.1-flash-lite"},
		{"bedrock us", endpoint{kind: core.KindBedrock, region: "us-west-2"},
			models("global.anthropic.claude-sonnet-4-6=Claude Sonnet 4.6 (Global)", "us.anthropic.claude-sonnet-4-6=Claude Sonnet 4.6 (US)",
				"eu.anthropic.claude-sonnet-4-6=Claude Sonnet 4.6 (EU)", "us.anthropic.claude-haiku-4-5-20251001-v1:0=Claude Haiku 4.5 (US)",
				"us.amazon.nova-pro-v1:0=Nova Pro (US)"),
			"us.anthropic.claude-sonnet-4-6", "us.anthropic.claude-haiku-4-5-20251001-v1:0"},
		{"bedrock eu", endpoint{kind: core.KindBedrock, region: "eu-west-1"},
			models("global.anthropic.claude-sonnet-4-6", "eu.anthropic.claude-sonnet-4-6", "us.anthropic.claude-sonnet-4-6"),
			"eu.anthropic.claude-sonnet-4-6", "eu.anthropic.claude-sonnet-4-6"},
		{"bedrock nova only", endpoint{kind: core.KindBedrock, region: "us-east-1"},
			models("us.amazon.nova-pro-v1:0", "us.amazon.nova-lite-v1:0", "us.amazon.nova-micro-v1:0", "us.amazon.nova-2-lite-v1:0"),
			"us.amazon.nova-pro-v1:0", "us.amazon.nova-2-lite-v1:0"},
		{"ollama", endpoint{kind: core.KindOllama, base: "http://host.docker.internal:11434"},
			models("qwen3:8b", "llama3.3:70b", "qwen3:14b"), "qwen3:14b", "qwen3:14b"},
		{"ollama big only", endpoint{kind: core.KindOllama, base: "http://ollama:11434"},
			models("llama3.3:70b", "qwen3:235b"), "llama3.3:70b", "llama3.3:70b"},
		{"openrouter", endpoint{kind: core.KindOpenAICompat, base: "https://openrouter.ai/api/v1"},
			models("anthropic/claude-sonnet-4.5=Anthropic: Claude Sonnet 4.5", "anthropic/claude-haiku-4.5=Anthropic: Claude Haiku 4.5", "openai/gpt-5=OpenAI: GPT-5"),
			"anthropic/claude-sonnet-4.5", "anthropic/claude-haiku-4.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.annotate(tc.e, tc.ms)
			main, quick := suggested(t, tc.ms)
			if main != tc.main || quick != tc.quick {
				t.Fatalf("main %q quick %q, want %q and %q", main, quick, tc.main, tc.quick)
			}
		})
	}
}

func TestSuggestionsCheapestQuick(t *testing.T) {
	f := newHarness(t, nil).f
	ms := models("big-model", "small-model")
	ms[0].Price = core.Price{InPerM: 3_000_000, OutPerM: 15_000_000, Known: true}
	ms[1].Price = core.Price{InPerM: 100_000, OutPerM: 400_000, Known: true}
	f.annotate(endpoint{kind: core.KindOpenAICompat, base: "https://api.groq.com/openai/v1"}, ms)
	if main, quick := suggested(t, ms); main != "big-model" || quick != "small-model" {
		t.Fatalf("main %q quick %q", main, quick)
	}
	// Unknown prices: quick tasks use the main model.
	ms = models("a-model", "b-model")
	f.annotate(endpoint{kind: core.KindOpenAICompat, base: "https://models.example.com/v1"}, ms)
	if main, quick := suggested(t, ms); main != "a-model" || quick != "a-model" {
		t.Fatalf("main %q quick %q", main, quick)
	}
}

func TestBedrockNames(t *testing.T) {
	f := newHarness(t, nil).f
	ms := models("global.anthropic.claude-sonnet-4-5-20250929-v1:0=Claude Sonnet 4.5 (Global, legacy)",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0=Claude Sonnet 4.5 (US, legacy)",
		"us.anthropic.claude-sonnet-4-6=Claude Sonnet 4.6 (US)",
		"us.anthropic.claude-3-haiku-20240307-v1:0=US Anthropic Claude 3 Haiku",
		"us.amazon.nova-premier-v1:0=US Nova Premier")
	f.annotate(endpoint{kind: core.KindBedrock, region: "us-east-1"}, ms)
	want := []string{"Claude Sonnet 4.5 (Global, legacy)", "Claude Sonnet 4.5 (US, legacy)", "Claude Sonnet 4.6", "Claude 3 Haiku", "Nova Premier"}
	for i, m := range ms {
		if m.DisplayName != want[i] {
			t.Errorf("%s: %q, want %q", m.Ref.Model, m.DisplayName, want[i])
		}
		if m.ProviderName != "Amazon Bedrock" {
			t.Errorf("%s provider %q", m.Ref.Model, m.ProviderName)
		}
	}
	if ms[2].ShortName != "Sonnet 4.6" {
		t.Errorf("short name %q", ms[2].ShortName)
	}
}

// /api/models carries the plain names and one suggestion of each kind per
// provider, OpenAI over the Responses API included.
func TestModelsFriendly(t *testing.T) {
	h := newHarness(t, []config.Provider{
		cfgProvider("anthropic", core.KindAnthropic, "https://api.anthropic.com/v1"),
		{ID: "openai", Type: string(core.KindOpenAI), API: "responses", APIKey: "secret:test_key"},
	}, "anthropic/claude-sonnet-4-6")
	ms := h.f.Models(context.Background())
	by := map[string][]core.ModelInfo{}
	for _, m := range ms {
		by[m.Ref.Provider] = append(by[m.Ref.Provider], m)
	}
	for _, p := range []string{"anthropic", "openai"} {
		if len(by[p]) == 0 {
			t.Fatalf("no %s models", p)
		}
		main, quick := suggested(t, by[p])
		if main == "" || quick == "" || main == quick {
			t.Fatalf("%s: main %q quick %q", p, main, quick)
		}
	}
	for _, m := range by["anthropic"] {
		if m.ProviderName != "Anthropic" || !strings.HasPrefix(m.DisplayName, "Claude ") || strings.HasPrefix(m.ShortName, "Claude ") {
			t.Fatalf("anthropic names %+v", m)
		}
	}
	if m, ok := h.f.Model(core.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}); !ok || m.DisplayName != "Claude Sonnet 4.6" || m.ShortName != "Sonnet 4.6" {
		t.Fatalf("Model %+v", m)
	}
}

func TestFailureCodes(t *testing.T) {
	pe := func(kind core.ErrorKind, status int, msg string) error {
		return &core.ProviderError{Provider: "p", Kind: kind, Status: status, Msg: msg}
	}
	for _, tc := range []struct {
		err  error
		kind core.ProviderKind
		want core.FailureCode
	}{
		{pe(core.ErrKindAuth, 401, "invalid x-api-key"), core.KindAnthropic, core.FailKeyRejected},
		{pe(core.ErrKindAuth, 402, "Insufficient credits"), core.KindOpenAICompat, core.FailOutOfCredit},
		{pe(core.ErrKindInvalidRequest, 400, "Your credit balance is too low to access the Anthropic API."), core.KindAnthropic, core.FailOutOfCredit},
		{pe(core.ErrKindRateLimit, 429, "You exceeded your current quota, please check your plan and billing details."), core.KindOpenAI, core.FailOutOfCredit},
		{pe(core.ErrKindRateLimit, 429, "Rate limit reached"), core.KindOpenAI, core.FailRateLimited},
		{pe(core.ErrKindRateLimit, 429, "Rate limit reached for gpt-5 in organization org-x on requests per min (RPM): Limit 3, Used 3. "+
			"You can increase your rate limit by adding a payment method to your account at https://platform.openai.com/account/billing."), core.KindOpenAI, core.FailRateLimited},
		{pe(core.ErrKindRateLimit, 429, "Quota exceeded for quota metric 'Generate Content API requests per minute'"), core.KindGemini, core.FailRateLimited},
		{pe(core.ErrKindAuth, 403, "Your team has either used all available credits or reached its monthly spending limit."), core.KindOpenAICompat, core.FailOutOfCredit},
		{pe(core.ErrKindInvalidRequest, 404, "The model `gpt-9` does not exist or you do not have access to it."), core.KindOpenAI, core.FailNoModelAccess},
		{pe(core.ErrKindInvalidRequest, 404, "not_found_error: model: claude-x"), core.KindAnthropic, core.FailNoModelAccess},
		{pe(core.ErrKindInvalidRequest, 404, "Not Found"), core.KindOpenAICompat, core.FailNotAModelServer},
		{pe(core.ErrKindServer, 200, "malformed stream: invalid character '<'"), core.KindOpenAICompat, core.FailNotAModelServer},
		{pe(core.ErrKindOverloaded, 529, "Overloaded"), core.KindAnthropic, core.FailProviderError},
		{pe(core.ErrKindNetwork, 0, `Post "http://127.0.0.1:1/v1/chat/completions": dial tcp 127.0.0.1:1: connect: connection refused`), core.KindOpenAICompat, core.FailUnreachable},
		{pe(core.ErrKindNetwork, 0, `Post "http://127.0.0.1:11434/api/chat": destination address blocked: 127.0.0.1 resolves to 127.0.0.1 (address in blocked range 127.0.0.0/8)`), core.KindOllama, core.FailPrivateAddress},
		{pe(core.ErrKindNetwork, 0, `Get "https://x/v1/models": tls: failed to verify certificate: x509: certificate signed by unknown authority`), core.KindOpenAICompat, core.FailTLS},
		{fmt.Errorf("probe: %w", context.DeadlineExceeded), core.KindAnthropic, core.FailTimeout},
		{pe(core.ErrKindAuth, 0, "credential unavailable: vault: secret x: not found"), core.KindAnthropic, core.FailUnknown},
		{pe(core.ErrKindAuth, 0, "AWS credentials unavailable: no EC2 IMDS role found"), core.KindBedrock, core.FailAWSNoCredentials},
		{pe(core.ErrKindAuth, 403, "AccessDeniedException: User: arn:aws:sts::<account>:assumed-role/x/<session> is not authorized to perform: bedrock:InvokeModelWithResponseStream"), core.KindBedrock, core.FailAWSDenied},
		{pe(core.ErrKindAuth, 403, "AccessDeniedException: You don't have access to the model with the specified model ID."), core.KindBedrock, core.FailNoModelAccess},
		{pe(core.ErrKindInvalidRequest, 404, "ResourceNotFoundException: Model use case details have not been submitted for this account."), core.KindBedrock, core.FailAWSNoModels},
		{errors.New("something odd"), core.KindAnthropic, core.FailUnknown},
	} {
		if got := failureCode(tc.err, tc.kind); got != tc.want {
			t.Errorf("%v (%s): %q, want %q", tc.err, tc.kind, got, tc.want)
		}
	}
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestProbeCodes(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	denied := newServer(t, jsonReply(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	res, _ := h.f.Probe(ctx, core.ProbeConfig{Kind: core.KindAnthropic, BaseURL: denied.URL, Model: "claude-haiku-4-5", SecretRef: "secret:test_key", AllowPrivate: true})
	if res.OK || res.Code != core.FailKeyRejected || !strings.Contains(res.Error, "invalid x-api-key") || res.Model != "claude-haiku-4-5" {
		t.Fatalf("rejected key: %+v", res)
	}
	broke := newServer(t, jsonReply(400, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`))
	res, _ = h.f.Probe(ctx, core.ProbeConfig{Kind: core.KindAnthropic, BaseURL: broke.URL, Model: "claude-haiku-4-5", SecretRef: "secret:test_key", AllowPrivate: true})
	if res.Code != core.FailOutOfCredit {
		t.Fatalf("no credit: %+v", res)
	}
	res, _ = h.f.Probe(ctx, core.ProbeConfig{Kind: core.KindOllama, BaseURL: "http://127.0.0.1:1", AllowPrivate: true})
	if res.Code != core.FailUnreachable || res.Error == "" {
		t.Fatalf("unreachable: %+v", res)
	}
	empty := newServer(t, jsonReply(200, `{"models":[]}`))
	res, _ = h.f.Probe(ctx, core.ProbeConfig{Kind: core.KindOllama, BaseURL: empty.URL, AllowPrivate: true})
	if res.OK || res.Code != core.FailNoModels {
		t.Fatalf("no models: %+v", res)
	}
	notModels := newServer(t, reply{status: 404, ctype: "text/html", body: "<html>nope</html>"})
	res, _ = h.f.Probe(ctx, core.ProbeConfig{Kind: core.KindOpenAICompat, BaseURL: notModels.URL, AllowPrivate: true})
	if res.OK || res.Code != core.FailNotAModelServer {
		t.Fatalf("not a model server: %+v", res)
	}
	res, _ = h.f.Probe(ctx, core.ProbeConfig{Kind: core.KindAnthropic, SecretRef: "secret:missing"})
	if res.Code != core.FailUnknown || res.Error == "" {
		t.Fatalf("missing secret: %+v", res)
	}
	h.egress.Client = &http.Client{Transport: failingTransport{errors.New("destination address blocked: 192.168.1.20 resolves to 192.168.1.20 (address in blocked range 192.168.0.0/16)")}}
	res, _ = h.f.Probe(ctx, core.ProbeConfig{Kind: core.KindOllama, BaseURL: "http://192.168.1.20:11434"})
	if res.Code != core.FailPrivateAddress {
		t.Fatalf("private address: %+v", res)
	}
}

func TestProbeTestsTheSuggestedModel(t *testing.T) {
	h := newHarness(t, nil)
	tags := `{"models":[{"name":"qwen3:8b","model":"qwen3:8b"},{"name":"qwen3:14b","model":"qwen3:14b"}]}`
	srv := newServer(t, jsonReply(200, tags), jsonReply(200, `{}`), jsonReply(500, `{"error":"stop here"}`))
	res, _ := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOllama, BaseURL: srv.URL, AllowPrivate: true})
	if res.Model != "qwen3:14b" {
		t.Fatalf("probed %q: %+v", res.Model, res)
	}
	var rec *core.ModelChoice
	for i, c := range res.Choices {
		if c.Recommended {
			rec = &res.Choices[i]
		}
	}
	if rec == nil || rec.ID != "qwen3:14b" || rec.DisplayName != "Qwen3 14B" || !rec.QuickTasksDefault || rec.ProviderName != "Ollama" {
		t.Fatalf("choices %+v", res.Choices)
	}
}

func TestDetectSavedKeysAndChoices(t *testing.T) {
	h := newHarness(t, nil)
	h.f.d.Env = func() []string { return nil }
	h.f.d.Vault = fakes.NewVault(map[string]string{"openrouter_api_key": "sk-or-v1-abcdefgh1234", "anthropic-prod": "sk-ant-zzzzzzzz9876",
		"telegram_bot_token": "123:abc", "openrouterx": "nope-nope-nope"})
	tags := `{"models":[{"name":"qwen3:8b","model":"qwen3:8b"},{"name":"qwen3:14b","model":"qwen3:14b"}]}`
	h.egress.Client = &http.Client{Transport: hostRouter{
		"host.docker.internal:11434": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tags)) }),
	}}
	ds := h.f.Detect(context.Background())
	var saved []core.Detection
	var ollama *core.Detection
	for i, d := range ds {
		if d.SecretRef != "" {
			saved = append(saved, d)
		}
		if d.Kind == core.KindOllama {
			ollama = &ds[i]
		}
	}
	if len(saved) != 2 || saved[0].SecretRef != "secret:anthropic-prod" || saved[0].Kind != core.KindAnthropic || saved[0].SecretHint != "9876" ||
		saved[1].SecretRef != "secret:openrouter_api_key" || saved[1].BaseURL != "https://openrouter.ai/api/v1" || saved[1].SecretHint != "1234" {
		t.Fatalf("saved keys %+v", saved)
	}
	for _, d := range saved {
		if strings.Contains(d.Note, "sk-") {
			t.Fatalf("a key value left the process: %+v", d)
		}
	}
	if ollama == nil || len(ollama.Choices) != 2 {
		t.Fatalf("ollama %+v", ollama)
	}
	for _, c := range ollama.Choices {
		if c.Recommended != (c.ID == "qwen3:14b") {
			t.Fatalf("choices %+v", ollama.Choices)
		}
	}
}
