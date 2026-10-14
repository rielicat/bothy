package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/openai"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// apiServer answers /responses with the spec's text stream and
// /chat/completions with okStream, and records each request's path and body.
type apiServer struct {
	*httptest.Server
	rec *recorder
}

func newAPIServer(t *testing.T) *apiServer {
	t.Helper()
	spec, err := os.ReadFile("openai/testdata/spec_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	s := &apiServer{rec: &recorder{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.rec.add(r.URL.Path + " " + string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.HasSuffix(r.URL.Path, "/responses"):
			_, _ = w.Write(spec)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			_, _ = io.WriteString(w, okStream)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func (s *apiServer) last(t *testing.T) (path, body string) {
	t.Helper()
	all := s.rec.all()
	if len(all) == 0 {
		t.Fatal("no request")
	}
	path, body, _ = strings.Cut(all[len(all)-1], " ")
	return path, body
}

func TestAPISelection(t *testing.T) {
	for _, tc := range []struct {
		kind core.ProviderKind
		api  string
		path string
	}{
		{core.KindOpenAI, "", "/responses"},
		{core.KindOpenAI, "auto", "/responses"},
		{core.KindOpenAI, "chat", "/chat/completions"},
		{core.KindOpenAICompat, "", "/chat/completions"},
		{core.KindOpenAICompat, "auto", "/chat/completions"},
		{core.KindOpenAICompat, "responses", "/responses"},
	} {
		srv := newAPIServer(t)
		p := cfgProvider("p", tc.kind, srv.URL, config.ProviderModel{ID: "m"})
		p.API = tc.api
		h := newHarness(t, []config.Provider{p}, "p/m")
		if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
			t.Fatalf("%s %q: %v", tc.kind, tc.api, err)
		}
		if path, _ := srv.last(t); path != tc.path {
			t.Fatalf("%s %q: %s", tc.kind, tc.api, path)
		}
	}
}

// Changing providers[].api takes effect on the next call (hot reload), and
// what one API wrote never reaches the other.
func TestHotReloadSwitchesAPI(t *testing.T) {
	srv := newAPIServer(t)
	p := cfgProvider("p", core.KindOpenAI, srv.URL, config.ProviderModel{ID: "m"})
	h := newHarness(t, []config.Provider{p}, "p/m")
	history := []core.Message{
		{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "q"}}},
		{Role: core.MsgAssistant, Parts: []core.Part{
			{Type: core.PartThinking, Sig: `p:{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC-SECRET"}`, SigKind: openai.SigResponses},
			{Type: core.PartText, Text: "a", Sig: `p:{"id":"msg_1"}`, SigKind: openai.SigResponses},
		}},
		{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "more"}}},
	}
	build := func(m core.ModelInfo) (*core.Request, error) {
		return &core.Request{Model: m.Ref.Model, Messages: history}, nil
	}
	call := func() (string, string) {
		t.Helper()
		if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil); err != nil {
			t.Fatal(err)
		}
		return srv.last(t)
	}
	if path, body := call(); path != "/responses" || !strings.Contains(body, `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC-SECRET"}`) || !strings.Contains(body, `"id":"msg_1"`) {
		t.Fatalf("responses: %s %s", path, body)
	}
	cfg := *h.view.Current()
	p.API = "chat"
	cfg.Providers = []config.Provider{p}
	h.view.set(&cfg)
	if path, body := call(); path != "/chat/completions" || strings.Contains(body, "ENC-SECRET") || strings.Contains(body, "msg_1") || strings.Contains(body, "reasoning_effort") {
		t.Fatalf("chat: %s %s", path, body)
	}
	if e, _ := h.f.entry("p"); e.api != openai.APIChat || e.sig != core.KindOpenAI {
		t.Fatalf("entry %+v", e)
	}
}

func TestPrepareKeepsOnlyTheClientsSigKind(t *testing.T) {
	parts := []core.Part{
		{Type: core.PartThinking, Text: "chat", SigKind: core.KindOpenAI},
		{Type: core.PartThinking, Sig: "p:{}", SigKind: openai.SigResponses},
		{Type: core.PartText, Text: "a", Sig: `p:{"id":"msg_1"}`, SigKind: openai.SigResponses},
	}
	r := &core.Request{Messages: []core.Message{{Role: core.MsgAssistant, Parts: parts}}}
	got := prepare(r, core.ModelInfo{}, core.KindOpenAI, openai.SigResponses).Messages[0].Parts
	if len(got) != 2 || got[0].SigKind != openai.SigResponses || got[1].Sig == "" {
		t.Fatalf("responses client: %+v", got)
	}
	got = prepare(r, core.ModelInfo{}, core.KindOpenAI, core.KindOpenAI).Messages[0].Parts
	if len(got) != 2 || got[0].Text != "chat" || got[1].Sig != "" || got[1].SigKind != "" {
		t.Fatalf("chat client: %+v", got)
	}
}

// The facade bills a Responses reply from the catalog price, exact to the
// micro: GPT-6 Astra is $10 in and $50 out per 1M tokens, reasoning billed
// as output.
func TestResponsesCostFromCatalog(t *testing.T) {
	fixture, err := os.ReadFile("openai/testdata/cookbook_reasoning_tool.sse")
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, sse(string(fixture)))
	h := newHarness(t, []config.Provider{cfgProvider("openai", core.KindOpenAI, srv.URL)}, "openai/gpt-6-astra")
	build := func(m core.ModelInfo) (*core.Request, error) {
		r, _ := userReq(m)
		r.Tools = []core.ToolDef{{Name: "get_weather"}}
		return r, nil
	}
	resp, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Usage != (core.Usage{Input: 81, Output: 1035, Reasoning: 832}) || ci.Cost != 810+51_750 || !ci.PriceKnown || resp.Stop != core.StopTool {
		t.Fatalf("call info %+v", ci)
	}
	var body struct {
		Store           *bool    `json:"store"`
		Include         []string `json:"include"`
		MaxOutputTokens int      `json:"max_output_tokens"`
	}
	if err := json.Unmarshal([]byte(srv.body(0)), &body); err != nil {
		t.Fatal(err)
	}
	if body.Store == nil || *body.Store || len(body.Include) != 1 || body.MaxOutputTokens != 128_000 {
		t.Fatalf("request %s", srv.body(0))
	}
}

// The catalog marks GPT-6 Astra as reasoning, so a /v1 client's temperature
// never reaches it; a catalog model that does not reason keeps it.
func TestResponsesTemperatureFromCatalog(t *testing.T) {
	srv := newAPIServer(t)
	h := newHarness(t, []config.Provider{cfgProvider("openai", core.KindOpenAI, srv.URL+"/v1",
		config.ProviderModel{ID: "gpt-6-astra"}, config.ProviderModel{ID: "gpt-4.1"})})
	temp := 0.2
	for model, want := range map[string]bool{"gpt-6-astra": false, "gpt-4.1": true} {
		req := &core.Request{Model: model, Temperature: &temp, Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "hi"}}}}}
		if _, _, err := h.f.Passthrough(context.Background(), core.ModelRef{Provider: "openai", Model: model}, req, nil); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		path, body := srv.last(t)
		if !strings.HasSuffix(path, "/responses") || strings.Contains(body, `"temperature"`) != want {
			t.Fatalf("%s: %s %s", model, path, body)
		}
	}
}

func TestChatToolsReasoningOffAndDoctor(t *testing.T) {
	srv := mockllm.New([]mockllm.Rule{{ToolCalls: []core.ToolCall{{Name: "probe_echo", Args: json.RawMessage(`{"value":"ok"}`)}}, Fault: mockllm.FaultToolsReasoning}},
		[]core.ModelInfo{{Ref: core.ModelRef{Model: "gpt-6-sol"}}})
	t.Cleanup(srv.Close)
	p := cfgProvider("router", core.KindOpenAICompat, srv.URL(), config.ProviderModel{ID: "gpt-6-sol"})
	h := newHarness(t, []config.Provider{p}, "router/gpt-6-sol")
	build := func(m core.ModelInfo) (*core.Request, error) {
		r, _ := userReq(m)
		r.Tools = []core.ToolDef{{Name: "probe_echo"}}
		return r, nil
	}
	for range 2 {
		if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil); err != nil || len(ci.Attempts) != 1 {
			t.Fatalf("%v %+v", err, ci)
		}
	}
	if n := len(srv.Requests()); n != 3 {
		t.Fatalf("%d requests, want the refused one, its retry and one with reasoning off at once", n)
	}
	fs := findings(t, h, "providers.chat_tools")
	if len(fs) != 1 || fs[0].Severity != core.SevLow || !strings.Contains(fs[0].Title, "router/gpt-6-sol calls tools with reasoning turned off") ||
		!strings.Contains(fs[0].Detail, "Set api: responses on provider router if the server offers the Responses API.") {
		t.Fatalf("findings %+v", fs)
	}
	if !strings.Contains(h.logs.String(), "chat completions rejects tools with reasoning on") {
		t.Fatalf("not logged:\n%s", h.logs.String())
	}
	// A changed provider config starts over.
	cfg := *h.view.Current()
	p.Discover = true
	cfg.Providers = []config.Provider{p}
	h.view.set(&cfg)
	if fs := findings(t, h, "providers.chat_tools"); len(fs) != 0 {
		t.Fatalf("findings after a config change %+v", fs)
	}

	astra := mockllm.New([]mockllm.Rule{{ToolCalls: []core.ToolCall{{Name: "probe_echo", Args: json.RawMessage(`{"value":"ok"}`)}}, Fault: mockllm.FaultToolsResponsesOnly}},
		[]core.ModelInfo{{Ref: core.ModelRef{Model: "gpt-6-astra"}}})
	t.Cleanup(astra.Close)
	q := cfgProvider("openai", core.KindOpenAI, astra.URL(), config.ProviderModel{ID: "gpt-6-astra"})
	q.API = "chat"
	h = newHarness(t, []config.Provider{q}, "openai/gpt-6-astra")
	_, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest {
		t.Fatalf("%v", err)
	}
	fs = findings(t, h, "providers.chat_tools")
	if len(fs) != 1 || fs[0].Severity != core.SevMedium || !strings.Contains(fs[0].Title, "cannot call tools over Chat Completions") ||
		!strings.Contains(fs[0].Detail, "Set api: responses (or auto) on provider openai.") {
		t.Fatalf("findings %+v", fs)
	}
}

// A model that cannot call tools over Chat Completions is a capability of
// that model, not a failed request: the facade moves on to the next model
// before anything streams, leaves the provider's breaker alone, and a later
// call pays one refused request (the remembered mode) before moving on.
func TestChatNoToolsFallsBack(t *testing.T) {
	astra := mockllm.New([]mockllm.Rule{{ToolCalls: []core.ToolCall{{Name: "probe_echo", Args: json.RawMessage(`{"value":"ok"}`)}}, Fault: mockllm.FaultToolsResponsesOnly}},
		[]core.ModelInfo{{Ref: core.ModelRef{Model: "gpt-6-astra"}}})
	t.Cleanup(astra.Close)
	backup := newServer(t, sse(okStream))
	q := cfgProvider("openai", core.KindOpenAI, astra.URL(), config.ProviderModel{ID: "gpt-6-astra"})
	q.API = "chat"
	h := newHarness(t, []config.Provider{q, cfgProvider("b", core.KindOpenAICompat, backup.URL)}, "openai/gpt-6-astra", "b/m")
	build := func(m core.ModelInfo) (*core.Request, error) {
		r, _ := userReq(m)
		r.Tools = []core.ToolDef{{Name: "probe_echo"}}
		return r, nil
	}
	for call := 1; call <= 2; call++ {
		var events int
		resp, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, func(core.StreamEvent) error { events++; return nil })
		if err != nil || ci.Ref.String() != "b/m" || resp.Message.PlainText() != "Hi" || events == 0 {
			t.Fatalf("call %d: %v %+v", call, err, ci)
		}
		if len(ci.Attempts) != 2 || ci.Attempts[0].Kind != core.ErrKindInvalidRequest || !strings.Contains(ci.Attempts[0].Err, "Tools cannot be used over Chat Completions") {
			t.Fatalf("call %d: attempts %+v", call, ci.Attempts)
		}
	}
	if n, b := len(astra.Requests()), backup.count(); n != 3 || b != 2 {
		t.Fatalf("astra %d requests, backup %d; want the refused one and its retry, then one refused request", n, b)
	}
	for _, ph := range h.f.Health() {
		if ph.State != "ok" {
			t.Fatalf("health %+v", ph)
		}
	}
	if len(h.sleeps) != 0 {
		t.Fatalf("backoff %v", h.sleeps)
	}

	// Alone in the chain, the model's error is the call's error, unchanged.
	h = newHarness(t, []config.Provider{q}, "openai/gpt-6-astra")
	_, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest || !errors.Is(err, core.ErrModelUnsupported) {
		t.Fatalf("%v", err)
	}
	// Any other invalid request still returns at once.
	bad := newServer(t, jsonReply(400, errBody(`"invalid_value"`, "invalid_request_error", "Invalid value for 'tool_choice'.")))
	h = newHarness(t, []config.Provider{cfgProvider("a", core.KindOpenAICompat, bad.URL), cfgProvider("b", core.KindOpenAICompat, backup.URL)}, "a/m", "b/m")
	if _, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, build, nil); err == nil || len(ci.Attempts) != 1 {
		t.Fatalf("%v %+v", err, ci)
	}
}

func findings(t *testing.T, h *harness, id string) []core.Finding {
	t.Helper()
	var out []core.Finding
	for _, c := range h.f.Checks() {
		if c.ID() == id {
			out = append(out, c.Run(context.Background())...)
		}
	}
	return out
}

func probeMock(t *testing.T, fault string) *mockllm.Server {
	t.Helper()
	srv := mockllm.New([]mockllm.Rule{
		{MatchUser: "probe_echo", ToolCalls: []core.ToolCall{{Name: probeTool, Args: json.RawMessage(`{"value":"ok"}`)}}, Fault: fault, Usage: &core.Usage{Input: 100, Output: 20}},
		{MatchUser: "single word", Text: []string{"ok"}, Usage: &core.Usage{Input: 1000, Output: 1}},
	}, []core.ModelInfo{{Ref: core.ModelRef{Model: "gpt-6-astra"}}})
	t.Cleanup(srv.Close)
	return srv
}

func posts(srv *mockllm.Server) []string {
	var out []string
	for _, r := range srv.History() {
		if r.Method == http.MethodPost {
			out = append(out, r.Path)
		}
	}
	return out
}

func TestProbeUsesTheResponsesAPIForOpenAI(t *testing.T) {
	srv := probeMock(t, "")
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOpenAI, BaseURL: srv.URL(), Model: "gpt-6-astra", AllowPrivate: true, APIKey: core.NewSecret([]byte(canary))})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.ToolCalling || !res.UsageReported || res.API != openai.APIResponses || res.Detail != "Tested with the Responses API." {
		t.Fatalf("result %+v", res)
	}
	// Catalog price for gpt-6-astra: 1000 in + 1 out, then 100 in + 20 out.
	if res.Cost != 10_000+50+1_000+1_000 {
		t.Fatalf("cost %d", res.Cost)
	}
	if got := strings.Join(posts(srv), " "); got != "/v1/responses /v1/responses" {
		t.Fatalf("posts %s", got)
	}
	for _, r := range srv.History() {
		if r.Header.Get("Authorization") != "Bearer "+canary || strings.Contains(r.Path, canary) {
			t.Fatalf("request %s %v", r.Path, r.Header)
		}
	}
}

func TestProbeExplainsChatToolsWithReasoningOff(t *testing.T) {
	srv := probeMock(t, mockllm.FaultToolsReasoning)
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOpenAICompat, BaseURL: srv.URL(), Model: "gpt-6-astra", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.ToolCalling || res.API != openai.APIChat ||
		!strings.Contains(res.Detail, "Tool calling works over Chat Completions only with reasoning turned off") ||
		!strings.Contains(res.Detail, "set api: responses for this provider") {
		t.Fatalf("result %+v", res)
	}
	if got := strings.Join(posts(srv), " "); got != "/v1/chat/completions /v1/chat/completions /v1/chat/completions" {
		t.Fatalf("posts %s", got)
	}
	// What the probe learned is not kept: the provider is not saved yet.
	if len(h.f.effort.Entries()) != 0 {
		t.Fatalf("probe left state %+v", h.f.effort.Entries())
	}
}

func TestProbeExplainsNoToolsOverChat(t *testing.T) {
	srv := probeMock(t, mockllm.FaultToolsResponsesOnly)
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOpenAI, API: "chat", BaseURL: srv.URL(), Model: "gpt-6-astra", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ToolCalling || res.API != openai.APIChat ||
		!strings.HasPrefix(res.Detail, "Tested with the Chat Completions API. Tool calling failed: this model cannot call tools over Chat Completions") ||
		!strings.Contains(res.Detail, "Set api to responses or auto for this provider") ||
		!strings.Contains(res.Detail, "The provider said: Function tools with reasoning_effort are not supported for gpt-6-astra") {
		t.Fatalf("result %+v", res)
	}
}

func TestProbeServerWithoutResponsesAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"local-model","object":"model"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOpenAICompat, API: "responses", BaseURL: srv.URL + "/v1", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.API != openai.APIResponses || !strings.Contains(res.Detail, "This server has no Responses API at "+srv.URL+"/v1/responses") ||
		!strings.Contains(res.Detail, "Set api: chat") {
		t.Fatalf("result %+v", res)
	}
	if _, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOpenAI, API: "assistants"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("unknown api: %v", err)
	}
}

// A reasoning model that spends the tool test's whole output limit thinking
// has not shown that it cannot call tools.
func TestProbeToolTestCutOffWhileReasoning(t *testing.T) {
	spec, err := os.ReadFile("openai/testdata/spec_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := os.ReadFile("openai/testdata/spec_incomplete.json")
	if err != nil {
		t.Fatal(err)
	}
	var cut bytes.Buffer
	if err := json.Compact(&cut, incomplete); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if !strings.Contains(string(b), probeTool) {
			_, _ = w.Write(spec)
			return
		}
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]},"sequence_number":0}`+"\n\n"+
			`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"gAAAA1"},"sequence_number":1}`+"\n\n"+
			"data: "+cut.String()+"\n\n")
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindOpenAI, BaseURL: srv.URL, Model: "gpt-6-astra", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ToolCalling || !strings.Contains(res.Detail, "used the test's 1024-token output limit before calling the test tool") ||
		strings.Contains(res.Detail, "tool use may be unreliable") {
		t.Fatalf("result %+v", res)
	}
}
