package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/catalog"
	"github.com/rielicat/bothy/internal/provider/ollama"
)

func refs(ms []core.ModelInfo) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Ref.String()
	}
	return out
}

func TestChain(t *testing.T) {
	h := newHarness(t, []config.Provider{
		cfgProvider("anthropic", core.KindAnthropic, "https://api.anthropic.com/v1"),
		cfgProvider("ollama", core.KindOllama, "http://host.docker.internal:11434"),
	}, "anthropic/claude-sonnet-4-6", "ollama/qwen3:14b", "anthropic/claude-sonnet-4-6", "ghost/model", "bad-ref")
	got := refs(h.f.Chain(core.SlotMain, nil))
	if strings.Join(got, ",") != "anthropic/claude-sonnet-4-6,ollama/qwen3:14b" {
		t.Fatalf("main %v", got)
	}
	// Fast falls back to main when unset.
	if got := refs(h.f.Chain(core.SlotFast, nil)); len(got) != 2 {
		t.Fatalf("fast %v", got)
	}
	cfg := *h.view.Current()
	cfg.Models.Fast = []string{"anthropic/claude-haiku-4-5"}
	h.view.set(&cfg)
	if got := refs(h.f.Chain(core.SlotFast, nil)); strings.Join(got, ",") != "anthropic/claude-haiku-4-5" {
		t.Fatalf("fast %v", got)
	}
	// The override leads, followed by the slot chain without duplicates.
	got = refs(h.f.Chain(core.SlotMain, &core.ModelRef{Provider: "ollama", Model: "qwen3:14b"}))
	if strings.Join(got, ",") != "ollama/qwen3:14b,anthropic/claude-sonnet-4-6" {
		t.Fatalf("override %v", got)
	}
	if _, ok := h.f.Model(core.ModelRef{Provider: "ghost", Model: "x"}); ok {
		t.Fatal("unknown provider resolved")
	}
}

func TestModelResolution(t *testing.T) {
	yes := true
	h := newHarness(t, []config.Provider{
		cfgProvider("anthropic", core.KindAnthropic, ""),
		cfgProvider("lan", core.KindOpenAICompat, "http://192.168.1.20:8000/v1",
			config.ProviderModel{ID: "qwen", Context: 32768, MaxOutput: 4096, Tools: &yes, Vision: true}),
		cfgProvider("router", core.KindOpenAICompat, "https://openrouter.ai/api/v1"),
	})
	h.view.c.Providers[0].AllowPrivate = false
	h.view.c.Providers[2].AllowPrivate = false

	m, ok := h.f.Model(core.ModelRef{Provider: "anthropic", Model: "claude-haiku-4-5"})
	if !ok || m.Source != "catalog" || m.Context != 200000 || m.Cache != core.CacheExplicit || !m.Price.Known || m.Price.InPerM != 1_000_000 || m.Local {
		t.Fatalf("catalog model (date-suffix match) %+v", m)
	}
	m, _ = h.f.Model(core.ModelRef{Provider: "lan", Model: "qwen"})
	if m.Source != "config" || m.Context != 32768 || m.MaxOutput != 4096 || !m.Vision || !m.Local || !m.Price.Known || m.Price.InPerM != 0 {
		t.Fatalf("config model %+v", m)
	}
	m, _ = h.f.Model(core.ModelRef{Provider: "router", Model: "unknown/model"})
	if m.Price.Known || m.Context != defaultContext[core.KindOpenAICompat] || m.MaxOutput != 8192 || m.Local {
		t.Fatalf("unknown compat model %+v", m)
	}
	// The pricing section overrides the catalog.
	h.view.c.Pricing = map[string]config.PriceO{"anthropic/claude-haiku-4-5": {In: 0.8, Out: 4, CacheRead: 0.08, CacheWrite: 1}}
	m, _ = h.f.Model(core.ModelRef{Provider: "anthropic", Model: "claude-haiku-4-5"})
	if p := m.Price; p.InPerM != 800_000 || p.OutPerM != 4_000_000 || p.CacheReadPerM != 80_000 || p.CacheWritePerM != 1_000_000 || !p.Known || len(p.Tiers) != 0 {
		t.Fatalf("pricing override %+v", m.Price)
	}
}

// TestCost checks CallInfo.Cost to the micro: catalog price, a tiered price
// and an unknown price.
func TestCost(t *testing.T) {
	srv := newServer(t, sse(okStream)) // usage: 600 input, 400 cache read, 200 output
	h := newHarness(t, []config.Provider{cfgProvider("p", core.KindOpenAICompat, srv.URL)}, "p/priced")
	h.view.c.Providers[0].AllowPrivate = false
	cat, err := catalog.Parse([]byte(`{"version":1,"providers":[{"id":"p","kind":"openai_compat","base_url":"` + srv.URL + `","models":[
		{"id":"priced","price":{"in_per_m":3000000,"out_per_m":15000000,"cache_read_per_m":300000}},
		{"id":"tiered","price":{"in_per_m":3000000,"out_per_m":15000000,"cache_read_per_m":300000,"tiers":[{"above_input":999,"permille":2000}]}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	h.f.cat = cat
	_, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 600*3 + 200*15 + 400*0.3 = 1800 + 3000 + 120 micros.
	if ci.Cost != 4920 || !ci.PriceKnown {
		t.Fatalf("cost %d known %v", ci.Cost, ci.PriceKnown)
	}
	_, ci, _ = h.f.Complete(context.Background(), core.SlotMain, &core.ModelRef{Provider: "p", Model: "tiered"}, userReq, nil)
	if ci.Cost != 9840 { // total input 1000 > 999: everything at 2x
		t.Fatalf("tiered cost %d", ci.Cost)
	}
	_, ci, _ = h.f.Complete(context.Background(), core.SlotMain, &core.ModelRef{Provider: "p", Model: "mystery"}, userReq, nil)
	if ci.Cost != 0 || ci.PriceKnown {
		t.Fatalf("unknown price: cost %d known %v", ci.Cost, ci.PriceKnown)
	}
	if ci.PrefixHash == "" || len(ci.PrefixHash) != 16 || ci.ID == "" || !core.ValidID(core.PrefixLLMCall, ci.ID) {
		t.Fatalf("call info %+v", ci)
	}
}

func TestEstimateAndCalibrate(t *testing.T) {
	h := newHarness(t, nil)
	ref := core.ModelRef{Provider: "p", Model: "m"}
	text := strings.Repeat("x", 36)
	if got := h.f.Estimate(text, ref); got != 10 {
		t.Fatalf("estimate %d", got)
	}
	if h.f.Estimate("", ref) != 0 {
		t.Fatal("empty text")
	}
	h.f.Calibrate(ref, 100, 150) // correction 1 -> 1.1
	if got := h.f.Estimate(text, ref); got != 11 {
		t.Fatalf("calibrated %d", got)
	}
	for range 100 {
		h.f.Calibrate(ref, 1, 1000) // clamped
	}
	if got := h.f.Estimate(text, ref); got != 40 {
		t.Fatalf("clamped %d", got)
	}
	h.f.Calibrate(ref, 0, 10) // ignored
	if other := h.f.Estimate(text, core.ModelRef{Provider: "p", Model: "other"}); other != 10 {
		t.Fatalf("per-model correction leaked: %d", other)
	}
	// Passthrough model names are unbounded; the table is not.
	for i := range maxCorrections + 50 {
		h.f.Calibrate(core.ModelRef{Provider: "p", Model: strconv.Itoa(i)}, 100, 150)
	}
	if n := len(h.f.corr); n != maxCorrections {
		t.Fatalf("%d corrections kept", n)
	}
	if got := h.f.Estimate(text, ref); got != 40 {
		t.Fatalf("known model lost its correction: %d", got)
	}
}

// recorder collects request bodies from handler goroutines.
type recorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.bodies = append(r.bodies, s)
	r.mu.Unlock()
}

func (r *recorder) get(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[i]
}

// ollamaServer routes the documented Ollama responses by path; /api/ps is
// served only when ps is given.
func ollamaServer(t *testing.T, show string, chats *recorder, ps ...string) *httptest.Server {
	t.Helper()
	tags, _ := os.ReadFile("ollama/testdata/docs_tags.json")
	chat, _ := os.ReadFile("ollama/testdata/docs_chat_stream.ndjson")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write(tags)
		case "/api/show":
			_, _ = w.Write([]byte(show))
		case "/api/chat":
			chats.add(string(b))
			_, _ = w.Write(chat)
		case "/api/ps":
			if len(ps) == 0 {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(ps[0]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func numCtx(t *testing.T, body string) int {
	t.Helper()
	var req struct {
		Options struct {
			NumCtx int `json:"num_ctx"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return req.Options.NumCtx
}

func TestOllamaNumCtx(t *testing.T) {
	show, _ := os.ReadFile("ollama/testdata/docs_show.json") // gemma4.context_length 131072
	chats := &recorder{}
	srv := ollamaServer(t, string(show), chats)
	h := newHarness(t, []config.Provider{
		{ID: "ollama", Type: "ollama", BaseURL: srv.URL, AllowPrivate: true, Discover: true,
			Models: []config.ProviderModel{{ID: "qwen3:14b", Context: 16384}}},
	}, "ollama/qwen3:14b")
	// Configured: num_ctx is the configured context.
	if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil || ci.Cost != 0 || !ci.PriceKnown {
		t.Fatalf("%v %+v", err, ci)
	}
	if got := numCtx(t, chats.get(0)); got != 16384 {
		t.Fatalf("configured num_ctx %d", got)
	}
	if m := h.f.Chain(core.SlotMain, nil)[0]; m.Context != 16384 || !m.Local {
		t.Fatalf("info %+v", m)
	}
	// Discovered: the trained maximum /api/show reports (131,072) would make
	// Ollama allocate a KV cache small machines cannot hold, so num_ctx is
	// capped at the default window.
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, &core.ModelRef{Provider: "ollama", Model: "gemma4"}, userReq, nil); err != nil {
		t.Fatal(err)
	}
	if got := numCtx(t, chats.get(1)); got != ollama.DefaultWindow {
		t.Fatalf("discovered num_ctx %d, want %d", got, ollama.DefaultWindow)
	}
	if m, _ := h.f.Model(core.ModelRef{Provider: "ollama", Model: "gemma4"}); m.Context != ollama.DefaultWindow || m.Source != "discovery" || !m.Vision || m.Tools {
		t.Fatalf("discovered info %+v", m)
	}
}

// A served context larger than the default (OLLAMA_CONTEXT_LENGTH set by the
// operator) is kept; a smaller one, such as Ollama's 4,096 default for a
// model another client loaded, does not shrink the window.
func TestOllamaNumCtxFollowsALargerServedContext(t *testing.T) {
	show, _ := os.ReadFile("ollama/testdata/docs_show.json") // gemma4.context_length 131072
	for _, tc := range []struct{ served, want int }{{32768, 32768}, {4096, ollama.DefaultWindow}, {262144, 131072}} {
		chats := &recorder{}
		ps := fmt.Sprintf(`{"models":[{"name":"gemma4","model":"gemma4","context_length":%d}]}`, tc.served)
		srv := ollamaServer(t, string(show), chats, ps)
		h := newHarness(t, []config.Provider{
			{ID: "ollama", Type: "ollama", BaseURL: srv.URL, AllowPrivate: true, Discover: true},
		}, "ollama/gemma4")
		if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
			t.Fatal(err)
		}
		if got := numCtx(t, chats.get(0)); got != tc.want {
			t.Fatalf("served %d: num_ctx %d, want %d", tc.served, got, tc.want)
		}
	}
}

// The doctor explains a capped window for chain models, and is quiet once
// config sizes the model.
func TestOllamaContextCheck(t *testing.T) {
	show, _ := os.ReadFile("ollama/testdata/docs_show.json") // gemma4.context_length 131072
	srv := ollamaServer(t, string(show), &recorder{})
	check := func(models []config.ProviderModel) []core.Finding {
		h := newHarness(t, []config.Provider{
			{ID: "ollama", Type: "ollama", BaseURL: srv.URL, AllowPrivate: true, Models: models},
		}, "ollama/gemma4")
		for _, c := range h.f.Checks() {
			if c.ID() == "providers.ollama_context" {
				return c.Run(context.Background())
			}
		}
		t.Fatal("no providers.ollama_context check")
		return nil
	}
	fs := check(nil)
	if len(fs) != 1 || fs[0].Severity != core.SevInfo || !strings.Contains(fs[0].Detail, "num_ctx 16,384") ||
		!strings.Contains(fs[0].Detail, "131,072") || fs[0].Fix != "/settings/providers" {
		t.Fatalf("findings = %+v", fs)
	}
	if fs := check([]config.ProviderModel{{ID: "gemma4", Context: 65536}}); len(fs) != 0 {
		t.Fatalf("a configured context still reported: %+v", fs)
	}
}

func TestModelsList(t *testing.T) {
	show, _ := os.ReadFile("ollama/testdata/docs_show.json")
	srv := ollamaServer(t, string(show), &recorder{})
	off := false
	h := newHarness(t, []config.Provider{
		{ID: "ollama", Type: "ollama", BaseURL: srv.URL, AllowPrivate: true, Discover: true, Models: []config.ProviderModel{{ID: "qwen3:14b", Context: 16384}}},
		cfgProvider("deepseek", core.KindOpenAICompat, "https://api.deepseek.com/v1"),
		{ID: "off", Type: "anthropic", Enabled: &off},
	})
	got := refs(h.f.Models(context.Background()))
	want := []string{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro", "ollama/deepseek-r1:latest", "ollama/llama3.2:latest", "ollama/qwen3:14b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models\n got %v\nwant %v", got, want)
	}
}
