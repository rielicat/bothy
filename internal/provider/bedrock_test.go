package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
)

const (
	runtimeHost = "bedrock-runtime.us-west-2.amazonaws.com"
	controlHost = "bedrock.us-west-2.amazonaws.com"
	awsKeyID    = "AKIDFACADEEXAMPLE"
	awsSecret   = "facade-secret-CANARY-3b1d"
)

// awsEnvCreds gives the AWS SDK static credentials from the environment, so
// the chain resolves without any HTTP.
func awsEnvCreds(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", awsKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", awsSecret)
}

// awsStream encodes ConverseStream events (type, JSON payload pairs).
func awsStream(t *testing.T, events ...string) []byte {
	t.Helper()
	enc := eventstream.NewEncoder()
	var buf bytes.Buffer
	for i := 0; i+1 < len(events); i += 2 {
		var hs eventstream.Headers
		hs.Set(":event-type", eventstream.StringValue(events[i]))
		hs.Set(":content-type", eventstream.StringValue("application/json"))
		hs.Set(":message-type", eventstream.StringValue("event"))
		if err := enc.Encode(&buf, eventstream.Message{Headers: hs, Payload: []byte(events[i+1])}); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// recordingHandler answers in order (the last reply repeats) and records
// each request's path and Authorization header.
type recordingHandler struct {
	mu      sync.Mutex
	replies []func(w http.ResponseWriter)
	paths   []string
	auths   []string
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	n := len(h.paths)
	h.paths = append(h.paths, r.URL.RequestURI())
	h.auths = append(h.auths, r.Header.Get("Authorization"))
	reply := h.replies[min(n, len(h.replies)-1)]
	h.mu.Unlock()
	reply(w)
}

func eventReply(b []byte) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(b)
	}
}

func jsonReplyFn(status int, body string, hdr ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func recordedList(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("bedrock", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return string(doc.Body)
}

func bedrockProvider(id string) config.Provider {
	return config.Provider{ID: id, Type: string(core.KindBedrock), Region: "us-west-2"}
}

func TestBedrockThroughTheFacade(t *testing.T) {
	model := "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	h := newHarness(t, []config.Provider{bedrockProvider("aws")}, "aws/"+model)
	awsEnvCreds(t)
	raw, err := os.ReadFile("bedrock/testdata/live_text.eventstream")
	if err != nil {
		t.Fatal(err)
	}
	rt := &recordingHandler{replies: []func(http.ResponseWriter){eventReply(raw)}}
	h.egress.Client = &http.Client{Transport: hostRouter{runtimeHost: rt}}

	hist := []core.Message{
		{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "hi"}}},
		{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartThinking, Text: "anthropic thoughts", Sig: "sig", SigKind: core.KindAnthropic}, {Type: core.PartText, Text: "hello"}}},
		{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: "Reply with exactly these words: Hello from Bedrock"}}},
	}
	resp, ci, err := h.f.Complete(context.Background(), core.SlotMain, nil, func(m core.ModelInfo) (*core.Request, error) {
		return &core.Request{Model: m.Ref.Model, Messages: hist}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.PlainText() != "Hello from Bedrock" || ci.Ref.String() != "aws/"+model {
		t.Fatalf("resp %q ci %+v", resp.Message.PlainText(), ci)
	}
	// us. is a geographic profile: 23 in x $3.30 + 8 out x $16.50 per M.
	if !ci.PriceKnown || ci.Cost != 208 || ci.Usage != (core.Usage{Input: 23, Output: 8}) {
		t.Fatalf("cost %d known %v usage %+v", ci.Cost, ci.PriceKnown, ci.Usage)
	}
	if p := rt.paths[0]; p != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse-stream" {
		t.Fatalf("path %s", p)
	}
	if a := rt.auths[0]; !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential="+awsKeyID+"/") || strings.Contains(a, awsSecret) {
		t.Fatalf("authorization %q", a)
	}
	for _, d := range h.egress.Dests {
		if d.Name == "provider:aws" && strings.Join(d.Hosts, ",") != controlHost+":443,"+runtimeHost+":443" &&
			strings.Join(d.Hosts, ",") != runtimeHost+":443,"+controlHost+":443" {
			t.Fatalf("dest %+v", d)
		}
	}
	if strings.Contains(h.logs.String(), awsSecret) {
		t.Fatal("the secret key reached the logs")
	}

	m, ok := h.f.Model(core.ModelRef{Provider: "aws", Model: model})
	if !ok || m.Cache != core.CacheExplicit || m.Context != 200_000 || !m.Vision || m.Local || m.Display != "Claude Sonnet 4.5" {
		t.Fatalf("model info %+v", m)
	}
}

// The model list shows what the account can invoke (configured and
// discovered ids), not the catalog's base ids, most of which only work
// through a cross-region profile.
func TestBedrockModelsList(t *testing.T) {
	p := bedrockProvider("aws")
	p.Models = []config.ProviderModel{{ID: "us.amazon.nova-pro-v1:0"}}
	h := newHarness(t, []config.Provider{p})
	awsEnvCreds(t)
	ms := h.f.Models(context.Background())
	if len(ms) != 1 || ms[0].Ref.Model != "us.amazon.nova-pro-v1:0" || ms[0].Display != "Nova Pro" || !ms[0].Price.Known {
		t.Fatalf("without discovery %+v", ms)
	}

	p.Discover = true
	h.view.set(func() *config.Config { c := h.view.Current(); c.Providers = []config.Provider{p}; return c }())
	cp := &recordingHandler{replies: []func(http.ResponseWriter){
		jsonReplyFn(200, recordedList(t, "live_list_foundation_models.json")),
		jsonReplyFn(200, recordedList(t, "live_list_inference_profiles.json")),
	}}
	h.egress.Client = &http.Client{Transport: hostRouter{controlHost: cp}}
	ms = h.f.Models(context.Background())
	byID := map[string]core.ModelInfo{}
	for _, m := range ms {
		byID[m.Ref.Model] = m
	}
	if _, ok := byID["anthropic.claude-sonnet-4-6"]; ok {
		t.Fatal("a profile-only base id is listed")
	}
	m, ok := byID["global.anthropic.claude-sonnet-4-6"]
	if !ok || m.Price.InPerM != 3_000_000 || m.Context != 200_000 || m.Cache != core.CacheExplicit || m.Display != "Claude Sonnet 4.6 (Global)" {
		t.Fatalf("global sonnet 4.6 %+v (%d models)", m, len(ms))
	}
	if m := byID["us.anthropic.claude-sonnet-4-6"]; m.Price.InPerM != 3_300_000 {
		t.Fatalf("us sonnet 4.6 price %+v", m.Price)
	}
}

func TestBedrockModelInfo(t *testing.T) {
	p := bedrockProvider("aws")
	p.AllowPrivate = true // a VPC endpoint
	p.BaseURL = "https://vpce-0a1b.bedrock-runtime.us-west-2.vpce.amazonaws.com"
	p.Models = []config.ProviderModel{{ID: "meta.llama3-3-70b-instruct-v1:0"}}
	h := newHarness(t, []config.Provider{p})
	for _, tc := range []struct {
		model  string
		cache  core.CacheMode
		in     core.Micros
		known  bool
		window int
		vision bool
	}{
		{"global.anthropic.claude-opus-5-5", core.CacheExplicit, 4_000_000, true, 1_000_000, true},
		{"us.amazon.nova-micro-v1:0", core.CacheExplicit, 35_000, true, 128_000, false},
		{"meta.llama3-3-70b-instruct-v1:0", core.CacheNone, 0, false, 128_000, false},
		{"us.anthropic.claude-3-haiku-20240307-v1:0", core.CacheNone, 0, false, 128_000, true},
	} {
		m, ok := h.f.Model(core.ModelRef{Provider: "aws", Model: tc.model})
		if !ok || m.Cache != tc.cache || m.Price.InPerM != tc.in || m.Price.Known != tc.known || m.Context != tc.window || m.Local || m.Vision != tc.vision {
			t.Errorf("%s: %+v", tc.model, m)
		}
	}
	// The VPC endpoint and the control plane are the provider's egress hosts.
	if _, err := h.f.entry("aws"); err != nil {
		t.Fatal(err)
	}
	d := h.egress.Dests[len(h.egress.Dests)-1]
	if d.Name != "provider:aws" || !d.AllowPrivate || strings.Join(d.Hosts, ",") != "vpce-0a1b.bedrock-runtime.us-west-2.vpce.amazonaws.com:443,"+controlHost+":443" {
		t.Fatalf("dest %+v", d)
	}
}

func TestBedrockAuthFailureBannerAndDoctor(t *testing.T) {
	h := newHarness(t, []config.Provider{bedrockProvider("aws")}, "aws/us.anthropic.claude-haiku-4-5-20251001-v1:0")
	awsEnvCreds(t)
	bus := events.New(events.Options{Clock: h.clock})
	h.f.d.Bus = bus
	rt := &recordingHandler{replies: []func(http.ResponseWriter){jsonReplyFn(403,
		`{"message":"User: arn:aws:sts::123456789012:assumed-role/Dev/me is not authorized to perform: bedrock:InvokeModelWithResponseStream"}`,
		"X-Amzn-Errortype", "AccessDeniedException:http://internal.amazon.com/coral/com.amazon.bedrock/")}}
	h.egress.Client = &http.Client{Transport: hostRouter{runtimeHost: rt}}
	_, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil)
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindAuth || strings.Contains(err.Error(), "123456789012") {
		t.Fatalf("err %v", err)
	}
	banners := bus.Banners(core.Filter{System: true})
	if len(banners) != 1 || !strings.Contains(banners[0].Data.(core.BannerData).Text, "AWS credentials") {
		t.Fatalf("banners %+v", banners)
	}
	var titles []string
	for _, c := range h.f.Checks() {
		if c.ID() != "providers.auth_failed" {
			continue
		}
		for _, f := range c.Run(context.Background()) {
			titles = append(titles, f.Title)
			if strings.Contains(f.Detail, "123456789012") {
				t.Fatal("the account id reached the doctor")
			}
		}
	}
	if len(titles) != 1 || titles[0] != "Provider aws rejects its AWS credentials" {
		t.Fatalf("doctor %v", titles)
	}
}

func TestBedrockCredentialsCheck(t *testing.T) {
	ok := bedrockProvider("aws")
	missing := bedrockProvider("sso")
	missing.Profile = "nowhere"
	off := bedrockProvider("off")
	f := false
	off.Enabled = &f
	h := newHarness(t, []config.Provider{ok, missing, off})
	awsEnvCreds(t)
	var got []core.Finding
	for _, c := range h.f.Checks() {
		if c.ID() == "providers.bedrock_credentials" {
			got = c.Run(context.Background())
			if !errors.Is(c.Fix(context.Background(), core.Finding{}), core.ErrNotImplemented) {
				t.Fatal("no auto fix")
			}
		}
	}
	if len(got) != 2 {
		t.Fatalf("findings %+v", got)
	}
	if got[0].Severity != core.SevInfo || got[0].Title != "Provider aws signs in with access keys in environment variables" {
		t.Fatalf("ok finding %+v", got[0])
	}
	if got[1].Severity != core.SevHigh || got[1].Title != "Provider sso cannot get AWS credentials" ||
		got[1].Fix != "aws sso login --profile nowhere" || !strings.Contains(got[1].Detail, "nowhere") {
		t.Fatalf("failing finding %+v", got[1])
	}
	for _, f := range got {
		if strings.Contains(f.Title+f.Detail, awsSecret) || strings.Contains(f.Title+f.Detail, awsKeyID) {
			t.Fatal("a credential value reached the doctor")
		}
	}
}

func probeStream(t *testing.T) (text, tool []byte) {
	t.Helper()
	text = awsStream(t,
		"messageStart", `{"role":"assistant"}`,
		"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"ok"}}`,
		"contentBlockStop", `{"contentBlockIndex":0}`,
		"messageStop", `{"stopReason":"end_turn"}`,
		"metadata", `{"usage":{"inputTokens":14,"outputTokens":2,"totalTokens":16}}`)
	tool = awsStream(t,
		"messageStart", `{"role":"assistant"}`,
		"contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"tooluse_1","name":"probe_echo"}}}`,
		"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"value\":\"ok\"}"}}}`,
		"contentBlockStop", `{"contentBlockIndex":0}`,
		"messageStop", `{"stopReason":"tool_use"}`,
		"metadata", `{"usage":{"inputTokens":380,"outputTokens":40,"totalTokens":420}}`)
	return text, tool
}

func TestBedrockProbe(t *testing.T) {
	h := newHarness(t, nil)
	awsEnvCreds(t)
	text, tool := probeStream(t)
	rt := &recordingHandler{replies: []func(http.ResponseWriter){eventReply(text), eventReply(tool)}}
	cp := &recordingHandler{replies: []func(http.ResponseWriter){
		jsonReplyFn(200, recordedList(t, "live_list_foundation_models.json")),
		jsonReplyFn(200, recordedList(t, "live_list_inference_profiles.json")),
	}}
	h.egress.Client = &http.Client{Transport: hostRouter{runtimeHost: rt, controlHost: cp}}
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindBedrock, Region: "us-west-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.ToolCalling || !res.UsageReported || res.Credentials != "access keys in environment variables" || res.Detail != "" {
		t.Fatalf("result %+v", res)
	}
	// The probe picks a small model that does not think by default.
	if p := rt.paths[0]; !strings.Contains(p, "claude-haiku-4-5") {
		t.Fatalf("probed %s", p)
	}
	if res.Context != 200_000 || res.Cost == 0 || len(res.Models) < 20 || len(res.Choices) != len(res.Models) {
		t.Fatalf("result %+v", res)
	}
	found := false
	for _, c := range res.Choices {
		if c.ID == "us.anthropic.claude-sonnet-4-6" && c.Display == "Claude Sonnet 4.6 (US)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("choices %+v", res.Choices)
	}

	// No region anywhere: say so instead of guessing one.
	res, err = h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindBedrock})
	if err != nil || res.OK || !strings.Contains(res.Detail, "no region is configured") || res.Credentials == "" {
		t.Fatalf("no region: %v %+v", err, res)
	}
	// Invalid input is an error, not a result.
	for _, pc := range []core.ProbeConfig{
		{Kind: core.KindBedrock, Region: "us-west-2.evil.com"},
		{Kind: core.KindBedrock, Region: "us-west-2", Profile: "a b"},
		{Kind: core.KindBedrock, Region: "us-west-2", BaseURL: "http://bedrock.internal"},
	} {
		if _, err := h.f.Probe(context.Background(), pc); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%+v: %v", pc, err)
		}
	}
}

func TestBedrockProbeWithoutCredentials(t *testing.T) {
	h := newHarness(t, nil)
	res, err := h.f.Probe(context.Background(), core.ProbeConfig{Kind: core.KindBedrock, Region: "us-west-2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.HasPrefix(res.Detail, "Bothy could not get AWS credentials:") || !strings.Contains(res.Detail, "aws sso login") {
		t.Fatalf("result %+v", res)
	}
}

func TestDetectAWS(t *testing.T) {
	h := newHarness(t, nil)
	awsEnvCreds(t)
	t.Setenv("AWS_REGION", "us-west-2")
	cp := &recordingHandler{replies: []func(http.ResponseWriter){
		jsonReplyFn(200, recordedList(t, "live_list_foundation_models.json")),
		jsonReplyFn(200, recordedList(t, "live_list_inference_profiles.json")),
	}}
	h.egress.Client = &http.Client{Transport: hostRouter{controlHost: cp}}
	h.f.d.Env = func() []string { return []string{"AWS_ACCESS_KEY_ID=" + awsKeyID, "AWS_REGION=us-west-2"} }
	ds := h.f.Detect(context.Background())
	if len(ds) == 0 || ds[0].Kind != core.KindBedrock {
		t.Fatalf("detections %+v", ds)
	}
	d := ds[0]
	if d.Region != "us-west-2" || d.BaseURL != "https://"+runtimeHost || d.Source != "access keys in environment variables" ||
		d.Note != "Use this server's AWS access (access keys in environment variables) in us-west-2." || len(d.Models) < 20 || d.Profile != "" {
		t.Fatalf("detection %+v", d)
	}
	b, _ := json.Marshal(ds)
	if bytes.Contains(b, []byte(awsSecret)) || bytes.Contains(b, []byte(awsKeyID)) {
		t.Fatal("a credential left the process")
	}
}

func TestDetectAWSNothingFound(t *testing.T) {
	h := newHarness(t, nil)
	h.f.d.Env = func() []string { return nil }
	h.egress.Client = &http.Client{Transport: hostRouter{}}
	for _, d := range h.f.Detect(context.Background()) {
		if d.Kind == core.KindBedrock {
			t.Fatalf("detected %+v", d)
		}
	}
}

// The AWS chain reaches instance and container credentials only through
// the egress credentials client, handed the container URIs from the
// environment.
func TestBedrockUsesTheCredentialsClient(t *testing.T) {
	h := newHarness(t, []config.Provider{bedrockProvider("aws")}, "aws/us.amazon.nova-lite-v1:0")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "/v2/credentials/task")
	h.f.d.Env = func() []string { return []string{"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI=/v2/credentials/task"} }
	var gotRel, gotFull string
	calls := 0
	h.f.d.Credentials = func(rel, full string) *http.Client {
		gotRel, gotFull = rel, full
		return &http.Client{Transport: hostRouter{"169.254.170.2": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			_, _ = w.Write([]byte(`{"AccessKeyId":"AKIDTASK","SecretAccessKey":"task-secret","Token":"t","Expiration":"2099-01-01T00:00:00Z"}`))
		})}}
	}
	text, _ := probeStream(t)
	rt := &recordingHandler{replies: []func(http.ResponseWriter){eventReply(text)}}
	h.egress.Client = &http.Client{Transport: hostRouter{runtimeHost: rt}}
	if _, _, err := h.f.Complete(context.Background(), core.SlotMain, nil, userReq, nil); err != nil {
		t.Fatal(err)
	}
	if gotRel != "/v2/credentials/task" || gotFull != "" || calls != 1 || !strings.Contains(rt.auths[0], "Credential=AKIDTASK/") {
		t.Fatalf("rel %q full %q calls %d auth %q", gotRel, gotFull, calls, rt.auths[0])
	}
	for _, d := range h.egress.Dests {
		if strings.HasSuffix(d.Name, ":aws-credentials") {
			t.Fatalf("a system client for credential services was made: %+v", d)
		}
	}
}
