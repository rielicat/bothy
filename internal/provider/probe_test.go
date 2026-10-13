package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

func TestProbeOllamaSmallContext(t *testing.T) {
	tags, _ := os.ReadFile("ollama/testdata/docs_tags.json")
	chat, _ := os.ReadFile("ollama/testdata/docs_chat_stream.ndjson")
	show, _ := os.ReadFile("ollama/testdata/docs_show.json")
	ps, _ := os.ReadFile("ollama/testdata/docs_ps.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write(tags)
		case "/api/chat":
			_, _ = w.Write(chat)
		case "/api/show":
			_, _ = w.Write(show)
		case "/api/ps":
			_, _ = w.Write(ps)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOllama, BaseURL: srv.URL, Model: "gemma4", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Context != 4096 || res.Cost != 0 || !res.UsageReported || res.ToolCalling {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(res.Detail, "4,096 context") || !strings.Contains(res.Detail, "OLLAMA_CONTEXT_LENGTH") || !strings.Contains(res.Detail, "131,072") {
		t.Fatalf("detail %q", res.Detail)
	}
	if strings.Join(res.Models, ",") != "deepseek-r1:latest,llama3.2:latest" {
		t.Fatalf("models %v", res.Models)
	}
	if d := h.egress.Dests[len(h.egress.Dests)-1]; d.Name != "provider:probe" || !d.AllowPrivate {
		t.Fatalf("egress dest %+v", d)
	}
}

func TestProbeToolCallingAndCost(t *testing.T) {
	srv := mockllm.New([]mockllm.Rule{
		{MatchUser: "probe_echo", ToolCalls: []core.ToolCall{{Name: probeTool, Args: json.RawMessage(`{"value":"ok"}`)}}, Usage: &core.Usage{Input: 100, Output: 20}},
		{MatchUser: "single word", Text: []string{"ok"}, Usage: &core.Usage{Input: 1000, Output: 1}},
	}, []core.ModelInfo{{Ref: core.ModelRef{Model: "deepseek-v4-flash"}}})
	t.Cleanup(srv.Close)
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{
		Kind: core.KindOpenAICompat, BaseURL: srv.URL(), AllowPrivate: true, APIKey: core.NewSecret([]byte(canary)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.ToolCalling || !res.UsageReported || res.Detail != "" {
		t.Fatalf("result %+v", res)
	}
	if strings.Join(res.Models, ",") != "deepseek-v4-flash" || res.Context != 32768 {
		t.Fatalf("models %v context %d", res.Models, res.Context)
	}
	// The key went in the Authorization header of every request and nowhere
	// else, and nothing about it was kept.
	for _, r := range srv.History() {
		if r.Header.Get("Authorization") != "Bearer "+canary || strings.Contains(r.Path, canary) {
			t.Fatalf("request %s %v", r.Path, r.Header)
		}
	}
	if len(h.f.entries) != 0 || strings.Contains(h.logs.String(), canary) {
		t.Fatal("probe left state or logged the key")
	}
}

func TestProbeFailures(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: "bogus"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad kind: %v", err)
	}
	if _, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOpenAICompat}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("compat without base: %v", err)
	}
	denied := newServer(t, jsonReply(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindAnthropic, BaseURL: denied.URL, Model: "claude-haiku-4-5", SecretRef: "secret:test_key", AllowPrivate: true})
	if err != nil || res.OK || !strings.Contains(res.Detail, "rejected the API key") {
		t.Fatalf("%v %+v", err, res)
	}
	if !contains(h.vault.Uses, "test_key|provider:probe") {
		t.Fatalf("vault uses %v", h.vault.Uses)
	}
	res, _ = h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOllama, BaseURL: "http://127.0.0.1:1", AllowPrivate: true})
	if res.OK || !strings.Contains(res.Detail, "Could not reach") || !strings.Contains(res.Detail, "Is Ollama running?") {
		t.Fatalf("unreachable: %+v", res)
	}
	res, _ = h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindAnthropic, SecretRef: "secret:missing"})
	if res.OK || !strings.Contains(res.Detail, "vault") {
		t.Fatalf("missing secret: %+v", res)
	}
}

// hostRouter answers requests by host:port without any network.
type hostRouter map[string]http.Handler

func (hr hostRouter) RoundTrip(r *http.Request) (*http.Response, error) {
	hnd, ok := hr[r.URL.Host]
	if !ok {
		return nil, errors.New("connection refused")
	}
	rec := httptest.NewRecorder()
	hnd.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func TestDetect(t *testing.T) {
	tags, _ := os.ReadFile("ollama/testdata/docs_tags.json")
	h := newHarness(t, nil)
	h.egress.Client = &http.Client{Transport: hostRouter{
		"localhost:11434": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tags) }),
		"localhost:1234": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen2.5-7b-instruct","object":"model"}]}`))
		}),
	}}
	h.f.d.Env = func() []string {
		return []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=" + canary, "OPENAI_API_KEY=", "GROQ_API_KEY=gsk_x"}
	}
	got := h.f.Detect(context.Background())
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), canary) || strings.Contains(string(b), "gsk_x") {
		t.Fatal("a key value left the process")
	}
	if len(got) != 4 {
		t.Fatalf("detections %s", b)
	}
	if got[0].Kind != core.KindOllama || got[0].BaseURL != "http://localhost:11434" || len(got[0].Models) != 2 {
		t.Fatalf("ollama %+v", got[0])
	}
	if got[1].Kind != core.KindOpenAICompat || got[1].BaseURL != "http://localhost:1234/v1" || got[1].Models[0] != "qwen2.5-7b-instruct" {
		t.Fatalf("lm studio %+v", got[1])
	}
	if got[2].EnvVar != "ANTHROPIC_API_KEY" || got[3].EnvVar != "GROQ_API_KEY" || got[3].BaseURL != "https://api.groq.com/openai/v1" {
		t.Fatalf("env %s", b)
	}
	for _, d := range h.egress.Dests {
		if !d.AllowPrivate || len(d.Hosts) != 1 {
			t.Fatalf("dest %+v", d)
		}
	}
}
