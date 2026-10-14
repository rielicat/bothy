//go:build live

// The live smoke test calls Amazon Bedrock with real AWS credentials. CI
// never builds it. Run it with
//
//	BOTHY_LIVE_AWS_PROFILE=<profile> BOTHY_LIVE_AWS_REGION=us-west-2 \
//	  go test -tags live -run Live -v ./internal/provider/bedrock/
//
// and BOTHY_RECORD=1 to rewrite the testdata/live_* fixtures. It makes nine
// small model calls (a few cents) and two free control-plane listings.
package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

const (
	liveSonnet = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	liveOpus   = "us.anthropic.claude-opus-4-5-20251101-v1:0"
	liveNova   = "us.amazon.nova-lite-v1:0"
)

// recorder keeps the response bodies of a live run, keyed by fixture name.
type recorder struct {
	mu     sync.Mutex
	name   string
	bodies map[string]recorded
}

type recorded struct {
	status int
	header http.Header
	body   []byte
}

func (r *recorder) use(name string) {
	r.mu.Lock()
	r.name = name
	r.mu.Unlock()
}

// hostsOnly is a stand-in for an egress system client: it refuses every host
// not on its list, so the live run proves the computed host lists suffice.
type hostsOnly struct {
	hosts map[string]bool
	rec   *recorder
	base  http.RoundTripper
}

func (h *hostsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	if !h.hosts[strings.ToLower(req.URL.Hostname())+":"+port] {
		return nil, fmt.Errorf("live test egress: %s is not an allowed host", req.URL.Host)
	}
	resp, err := h.base.RoundTrip(req)
	if err != nil || h.rec == nil {
		return resp, err
	}
	b, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if rerr != nil {
		return nil, rerr
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	h.rec.mu.Lock()
	if name := h.rec.name; name != "" {
		// Discover makes two listing calls under one fixture name.
		switch {
		case strings.HasPrefix(req.URL.Path, "/foundation-models"):
			name += "_foundation_models"
		case strings.HasPrefix(req.URL.Path, "/inference-profiles"):
			name += "_inference_profiles"
		}
		h.rec.bodies[name] = recorded{status: resp.StatusCode, header: resp.Header.Clone(), body: b}
	}
	h.rec.mu.Unlock()
	return resp, nil
}

func newHostsClient(rec *recorder, hosts ...string) *http.Client {
	set := map[string]bool{}
	for _, h := range hosts {
		set[h] = true
	}
	return &http.Client{
		Transport:     &hostsOnly{hosts: set, rec: rec, base: http.DefaultTransport.(*http.Transport).Clone()},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func liveClient(t *testing.T) (*Client, *Chain, *recorder) {
	t.Helper()
	profile, region := os.Getenv("BOTHY_LIVE_AWS_PROFILE"), os.Getenv("BOTHY_LIVE_AWS_REGION")
	if region == "" {
		t.Skip("set BOTHY_LIVE_AWS_REGION (and BOTHY_LIVE_AWS_PROFILE) to run the live Bedrock test")
	}
	var authHosts []string
	chain := NewChain(ChainOptions{Region: region, Profile: profile, System: func(hosts []string) *http.Client {
		authHosts = hosts
		return newHostsClient(nil, hosts...)
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src, err := chain.Describe(ctx)
	if err != nil {
		t.Fatalf("resolving AWS credentials: %v", err)
	}
	t.Logf("credentials: %s; credential hosts %v", src, authHosts)
	rec := &recorder{bodies: map[string]recorded{}}
	hc := newHostsClient(rec, "bedrock-runtime."+region+".amazonaws.com:443", "bedrock."+region+".amazonaws.com:443")
	c := New(Options{Region: region, Credentials: chain})
	c.o.ID, c.o.HTTP = "bedrock", hc
	return c, chain, rec
}

func user(text string) core.Message {
	return core.Message{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: text}}}
}

var weatherTool = core.ToolDef{Name: "get_weather", Description: "Current weather for a city.",
	Schema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}

var timeTool = core.ToolDef{Name: "get_time", Description: "Current local time in a city.",
	Schema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}

func TestLiveBedrock(t *testing.T) {
	c, chain, rec := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Run("text", func(t *testing.T) {
		rec.use("live_text")
		var ev wiretest.Events
		resp, err := c.Stream(ctx, &core.Request{Model: liveSonnet, MaxTokens: 40,
			System:   []core.SystemBlock{{Text: "You are terse."}},
			Messages: []core.Message{user("Reply with exactly these words: Hello from Bedrock")}}, ev.On)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Message.PlainText(), "Hello from Bedrock") || resp.Stop != core.StopEnd || resp.UsageEstimated {
			t.Fatalf("resp %+v", resp)
		}
		t.Logf("text %q usage %+v", resp.Message.PlainText(), resp.Usage)
	})

	t.Run("parallel_tools", func(t *testing.T) {
		rec.use("live_parallel_tools")
		resp, err := c.Stream(ctx, &core.Request{Model: liveSonnet, MaxTokens: 400, Tools: []core.ToolDef{weatherTool, timeTool},
			Messages: []core.Message{user("What is the weather and the local time in Paris? Call both tools at once, in parallel, before answering.")}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var calls []string
		for _, p := range resp.Message.Parts {
			if p.Type == core.PartToolCall {
				calls = append(calls, p.ToolCall.Name+string(p.ToolCall.Args))
			}
		}
		if len(calls) != 2 || resp.Stop != core.StopTool {
			t.Fatalf("calls %v stop %q", calls, resp.Stop)
		}
		t.Logf("calls %v usage %+v", calls, resp.Usage)
	})

	t.Run("reasoning_tool_round_trip", func(t *testing.T) {
		thinking := json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":1024}}`)
		req := &core.Request{Model: liveSonnet, MaxTokens: 2000, Tools: []core.ToolDef{weatherTool},
			Messages: []core.Message{user("Should I take an umbrella in Oslo today? Check the weather first.")}}
		rec.use("live_reasoning_tool")
		body := buildRequest(req)
		body.AdditionalModelRequestFields = thinking
		first, err := c.stream(ctx, req.Model, body, nil)
		if err != nil {
			t.Fatal(err)
		}
		var call *core.ToolCall
		signed := false
		for _, p := range first.Message.Parts {
			if p.Type == core.PartThinking && p.Sig != "" && p.SigKind == core.KindBedrock {
				signed = true
			}
			if p.Type == core.PartToolCall {
				call = p.ToolCall
			}
		}
		if !signed || call == nil || first.Message.Parts[0].Type != core.PartThinking {
			t.Fatalf("want signed reasoning before a tool call: %+v", first.Message.Parts)
		}
		req.Messages = append(req.Messages, first.Message, core.Message{Role: core.MsgTool, Parts: []core.Part{{Type: core.PartToolResult,
			ToolResult: &core.ToolResult{CallID: call.ID, Name: call.Name, Content: `{"city":"Oslo","forecast":"rain showers","chance_of_rain":0.8}`}}}})
		rec.use("live_reasoning_tool_result")
		body = buildRequest(req)
		body.AdditionalModelRequestFields = thinking
		second, err := c.stream(ctx, req.Model, body, nil)
		if err != nil {
			t.Fatalf("the signed reasoning did not round-trip: %v", err)
		}
		if !strings.Contains(strings.ToLower(second.Message.PlainText()), "umbrella") {
			t.Fatalf("final answer %q", second.Message.PlainText())
		}
		t.Logf("usage %+v then %+v", first.Usage, second.Usage)
	})

	t.Run("cache", func(t *testing.T) {
		long := strings.Repeat("Bothy is a self-hosted company agent. It keeps secrets in a vault, runs tools in sandboxes and asks people before anything consequential happens. ", 60)
		req := &core.Request{Model: liveSonnet, MaxTokens: 5,
			System:   []core.SystemBlock{{Text: long, CacheBreak: true}},
			Messages: []core.Message{user("Say OK.")}}
		rec.use("live_cache_write")
		w, err := c.Stream(ctx, req, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec.use("live_cache_read")
		r, err := c.Stream(ctx, req, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("write usage %+v stop %q; read usage %+v stop %q", w.Usage, w.Stop, r.Usage, r.Stop)
		if w.Usage.CacheWrite+w.Usage.CacheRead == 0 || r.Usage.CacheRead == 0 {
			t.Fatalf("no cache activity: %+v / %+v", w.Usage, r.Usage)
		}
	})

	t.Run("image", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for x := 0; x < 16; x++ {
			for y := 0; y < 16; y++ {
				img.Set(x, y, color.RGBA{R: 220, A: 255})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		rec.use("live_image")
		resp, err := c.Stream(ctx, &core.Request{Model: liveOpus, MaxTokens: 10, Messages: []core.Message{{Role: core.MsgUser, Parts: []core.Part{
			{Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Name: "red.png", Data: buf.Bytes(), Size: int64(buf.Len())}},
			{Type: core.PartText, Text: "What single colour fills this image? Answer with one lowercase word."},
		}}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(resp.Message.PlainText()), "red") {
			t.Fatalf("answer %q", resp.Message.PlainText())
		}
	})

	t.Run("max_tokens", func(t *testing.T) {
		rec.use("live_max_tokens")
		resp, err := c.Stream(ctx, &core.Request{Model: liveSonnet, MaxTokens: 3,
			Messages: []core.Message{user("Count from one to twenty in words.")}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Stop != core.StopMaxTokens || resp.Usage.Output != 3 {
			t.Fatalf("stop %q usage %+v", resp.Stop, resp.Usage)
		}
	})

	t.Run("nova_tool", func(t *testing.T) {
		rec.use("live_nova_tool")
		resp, err := c.Stream(ctx, &core.Request{Model: liveNova, MaxTokens: 200, Tools: []core.ToolDef{weatherTool},
			System:     []core.SystemBlock{{Text: "Use the tools to answer.", CacheBreak: true}},
			Messages:   []core.Message{user("What is the weather in Lisbon?")},
			ToolChoice: "required"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var call *core.ToolCall
		for _, p := range resp.Message.Parts {
			if p.Type == core.PartToolCall {
				call = p.ToolCall
			}
		}
		if call == nil || call.Name != "get_weather" || resp.Stop != core.StopTool {
			t.Fatalf("resp %+v", resp)
		}
		t.Logf("nova call %s %s usage %+v", call.Name, call.Args, resp.Usage)
	})

	t.Run("errors", func(t *testing.T) {
		rec.use("live_error_unknown_model")
		_, err := c.Stream(ctx, &core.Request{Model: "us.anthropic.claude-does-not-exist-v1:0", MaxTokens: 5, Messages: []core.Message{user("hi")}}, nil)
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind != core.ErrKindInvalidRequest {
			t.Fatalf("unknown model: %v", err)
		}
		t.Logf("unknown model: %v", err)
	})

	t.Run("discover", func(t *testing.T) {
		rec.use("live_list")
		ms, err := c.Discover(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range ms {
			ids = append(ids, m.Ref.Model+" = "+m.Display)
		}
		t.Logf("%d usable models:\n%s", len(ms), strings.Join(ids, "\n"))
		if len(ms) == 0 {
			t.Fatal("no models listed")
		}
	})

	if os.Getenv("BOTHY_RECORD") == "1" {
		writeFixtures(t, rec, chain)
	}
}

var accountIDRe = regexp.MustCompile(`\b[0-9]{12}\b`)

// writeFixtures stores the recorded responses: event streams as one JSON
// line per frame (headers and the payload verbatim), JSON bodies with the
// account id replaced by the documentation account 111122223333, also inside
// Claude reasoning signatures, which embed it. Request headers, request
// signatures and credentials are never part of a recording.
func writeFixtures(t *testing.T, rec *recorder, chain *Chain) {
	t.Helper()
	creds, err := chain.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !accountIDRe.MatchString(creds.AccountID) {
		t.Fatal("the credentials do not name their account, so recordings cannot be scrubbed; not recording")
	}
	acct := creds.AccountID
	secrets := []string{creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken, acct}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for name, r := range rec.bodies {
		var out []byte
		ext := ".json"
		if strings.HasPrefix(r.header.Get("Content-Type"), eventStreamType) {
			ext = ".jsonl"
			out = framesJSONL(t, r.body, acct)
			if name == "live_text" {
				writeFixture(t, "live_text.eventstream", r.body, secrets)
			}
		} else {
			doc := map[string]any{"status": r.status, "content_type": r.header.Get("Content-Type")}
			if et := r.header.Get("X-Amzn-Errortype"); et != "" {
				doc["x_amzn_errortype"] = et
			}
			var body map[string]any
			if err := json.Unmarshal(r.body, &body); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			trimListing(body)
			doc["body"] = body
			b, err := json.MarshalIndent(doc, "", " ")
			if err != nil {
				t.Fatal(err)
			}
			out = append(accountIDRe.ReplaceAll(b, []byte("111122223333")), '\n')
		}
		writeFixture(t, name+ext, out, secrets)
	}
}

// trimListing keeps a listing's Anthropic and Amazon entries and the first
// of two other vendors, in order, so the fixture stays small but still
// exercises the filter.
func trimListing(body map[string]any) {
	for key, field := range map[string]string{"modelSummaries": "providerName", "inferenceProfileSummaries": "inferenceProfileId"} {
		list, ok := body[key].([]any)
		if !ok {
			continue
		}
		var kept []any
		seen := map[string]bool{}
		for _, e := range list {
			vendor, _ := e.(map[string]any)[field].(string)
			if field == "inferenceProfileId" {
				_, base := SplitModel(vendor)
				vendor, _, _ = strings.Cut(base, ".")
			}
			switch strings.ToLower(vendor) {
			case "anthropic", "amazon":
				kept = append(kept, e)
			case "meta", "mistral ai":
				if !seen[vendor] {
					seen[vendor] = true
					kept = append(kept, e)
				}
			}
		}
		body[key] = kept
	}
}

func writeFixture(t *testing.T, name string, b []byte, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && (bytes.Contains(b, []byte(s)) || scrubBase64(string(b), s) != string(b)) {
			t.Fatalf("%s would contain a credential or the account id; not written", name)
		}
	}
	if err := os.WriteFile(filepath.Join("testdata", name), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote testdata/%s (%d bytes)", name, len(b))
}

var base64Re = regexp.MustCompile(`[A-Za-z0-9+/]{16,}={0,2}`)

// scrubBase64 re-encodes base64 tokens whose decoded bytes contain secret
// with 111122223333 in its place.
func scrubBase64(s, secret string) string {
	return base64Re.ReplaceAllStringFunc(s, func(tok string) string {
		raw, err := base64.StdEncoding.DecodeString(tok)
		if err != nil || !bytes.Contains(raw, []byte(secret)) {
			return tok
		}
		return base64.StdEncoding.EncodeToString(bytes.ReplaceAll(raw, []byte(secret), []byte("111122223333")))
	})
}

func framesJSONL(t *testing.T, raw []byte, acct string) []byte {
	t.Helper()
	dec := eventstream.NewDecoder()
	r := bytes.NewReader(raw)
	var out bytes.Buffer
	for r.Len() > 0 {
		m, err := dec.Decode(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		hs := map[string]string{}
		for _, h := range m.Headers {
			hs[h.Name] = h.Value.String()
		}
		payload := strings.ReplaceAll(scrubBase64(string(m.Payload), acct), acct, "111122223333")
		line, err := json.Marshal(frame{Headers: hs, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
