package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/wire/wiretest"
)

// recordedBody returns the body of a recorded JSON fixture.
func recordedBody(t *testing.T, name string) []byte {
	t.Helper()
	var doc struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(wiretest.File(t, "testdata/"+name), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Body
}

func TestDiscoverRecordedAccount(t *testing.T) {
	srv := wiretest.NewServer(t,
		wiretest.JSON(http.StatusOK, recordedBody(t, "live_list_foundation_models.json")),
		wiretest.JSON(http.StatusOK, recordedBody(t, "live_list_inference_profiles.json")))
	c, _ := testClient(srv)
	ms, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || reqs[0].URL != "/foundation-models?byOutputModality=TEXT" ||
		reqs[1].URL != "/inference-profiles?maxResults=1000&type=SYSTEM_DEFINED" || reqs[1].Method != http.MethodGet {
		t.Fatalf("requests %+v", reqs)
	}
	for _, r := range reqs {
		verifySigV4(t, r, strings.TrimPrefix(srv.URL, "http://"), testSecret, "us-west-2")
	}
	byID := map[string]core.ModelInfo{}
	var order []string
	for _, m := range ms {
		byID[m.Ref.Model] = m
		order = append(order, m.Ref.Model)
		if m.Ref.Provider != "bedrock" || !m.Tools || m.Source != "discovery" {
			t.Fatalf("model %+v", m)
		}
	}
	for id, display := range map[string]string{
		"us.anthropic.claude-sonnet-4-6":                  "Claude Sonnet 4.6 (US)",
		"global.anthropic.claude-opus-5-5":                "Claude Opus 5.5 (Global)",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0":    "Claude Sonnet 4.5 (US, legacy)",
		"global.anthropic.claude-haiku-4-5-20251001-v1:0": "Claude Haiku 4.5 (Global)",
		"us.amazon.nova-micro-v1:0":                       "Nova Micro (US)",
		"amazon.nova-lite-v1:0":                           "Nova Lite",
		"us.amazon.nova-premier-v1:0":                     "US Nova Premier",
	} {
		m, ok := byID[id]
		if !ok || m.Display != display {
			t.Errorf("%s: %+v (listed: %v)", id, m, ok)
		}
	}
	if byID["us.amazon.nova-micro-v1:0"].Vision || !byID["us.amazon.nova-lite-v1:0"].Vision || !byID["us.anthropic.claude-sonnet-4-6"].Vision {
		t.Error("vision from the listed input modalities")
	}
	if byID["us.anthropic.claude-sonnet-4-6"].Cache != core.CacheExplicit || byID["us.amazon.nova-pro-v1:0"].Cache != core.CacheExplicit {
		t.Error("cache mode")
	}
	for _, id := range order {
		if strings.Contains(id, "sonic") || strings.Contains(id, "titan") || strings.Contains(id, "meta.") || strings.Contains(id, "mistral") {
			t.Errorf("not a usable Claude/Nova text model: %s", id)
		}
	}
	// Bare ids only where the model runs on demand in the region.
	if _, ok := byID["anthropic.claude-sonnet-4-6"]; ok {
		t.Error("a profile-only model listed by its bare id")
	}
	// Claude first, retiring models after the active ones of their family.
	firstNova := slices.IndexFunc(order, func(id string) bool { return strings.Contains(id, "amazon.nova") })
	legacy := slices.Index(order, "us.anthropic.claude-sonnet-4-5-20250929-v1:0")
	active := slices.Index(order, "us.anthropic.claude-sonnet-5-5")
	if firstNova < 0 || legacy > firstNova || legacy < active {
		t.Fatalf("order %v", order)
	}
}

func TestDiscoverPaginationAndFailures(t *testing.T) {
	fm := []byte(`{"modelSummaries":[{"modelId":"anthropic.claude-haiku-5-5","modelName":"Claude Haiku 5.5","providerName":"Anthropic",` +
		`"inputModalities":["TEXT","IMAGE"],"outputModalities":["TEXT"],"responseStreamingSupported":true,"inferenceTypesSupported":["INFERENCE_PROFILE"],"modelLifecycle":{"status":"ACTIVE"}}]}`)
	page1 := []byte(`{"inferenceProfileSummaries":[{"inferenceProfileId":"us.anthropic.claude-haiku-5-5","inferenceProfileName":"US Anthropic Claude Haiku 5.5","status":"ACTIVE","type":"SYSTEM_DEFINED"}],"nextToken":"tok/2+="}`)
	page2 := []byte(`{"inferenceProfileSummaries":[{"inferenceProfileId":"global.anthropic.claude-haiku-5-5","inferenceProfileName":"Global Anthropic Claude Haiku 5.5","status":"ACTIVE","type":"SYSTEM_DEFINED"}]}`)
	srv := wiretest.NewServer(t, wiretest.JSON(200, fm), wiretest.JSON(200, page1), wiretest.JSON(200, page2))
	c, _ := testClient(srv)
	ms, err := c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Ref.Model != "global.anthropic.claude-haiku-5-5" || ms[1].Display != "Claude Haiku 5.5 (US)" {
		t.Fatalf("models %+v", ms)
	}
	if u := srv.Requests()[2].URL; u != "/inference-profiles?maxResults=1000&nextToken=tok%2F2%2B%3D&type=SYSTEM_DEFINED" {
		t.Fatalf("second page %s", u)
	}
	for _, r := range srv.Requests() {
		verifySigV4(t, r, strings.TrimPrefix(srv.URL, "http://"), testSecret, "us-west-2")
	}

	// One listing failing still yields the other; both failing is an error.
	denied := wiretest.Response{Status: 403, ContentType: "application/json", Body: []byte(`{"message":"not authorized to perform: bedrock:ListFoundationModels"}`),
		Header: http.Header{"X-Amzn-Errortype": {"AccessDeniedException"}}}
	srv = wiretest.NewServer(t, denied, wiretest.JSON(200, page2))
	c, _ = testClient(srv)
	if ms, err := c.Discover(context.Background()); err != nil || len(ms) != 1 || ms[0].Display != "Global Anthropic Claude Haiku 5.5" {
		t.Fatalf("partial listing: %v %+v", err, ms)
	}
	srv = wiretest.NewServer(t, denied)
	c, _ = testClient(srv)
	_, err = c.Discover(context.Background())
	var pe *core.ProviderError
	if !errors.As(err, &pe) || pe.Kind != core.ErrKindAuth {
		t.Fatalf("both listings denied: %v", err)
	}

	c.o.Discover = false
	c.o.Models = []core.ModelInfo{{Ref: core.ModelRef{Provider: "bedrock", Model: "us.amazon.nova-pro-v1:0"}, Source: "config"}}
	if ms, err := c.Models(context.Background()); err != nil || len(ms) != 1 {
		t.Fatalf("Models without discovery: %v %+v", err, ms)
	}
}
