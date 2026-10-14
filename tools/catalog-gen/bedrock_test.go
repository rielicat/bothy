package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBedrockOverlay(t *testing.T) {
	o, err := parseBedrock([]byte(`{"source":"s","models":[
		{"id":"anthropic.claude-x-v1:0","like":"claude-x","price":{"in":3,"out":15,"cache_read":0.3,"cache_write":3.75},
		 "geo_price":{"in":3.3,"out":16.5,"cache_read":0.33,"cache_write":4.125}},
		{"id":"amazon.nova-y-v1:0","display":"Nova Y","context":300000,"max_output":10000,"vision":true,
		 "price":{"in":0.8,"out":3.2,"cache_read":0.2},"geo_price":{"in":0.8,"out":3.2,"cache_read":0.2},
		 "tiers":[{"above_input":100000,"permille":5000}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := catalog{Version: 1, Providers: []provider{
		{ID: "anthropic", Kind: "anthropic", Models: []model{{ID: "claude-x", Display: "Claude X", Context: 200000, MaxOutput: 64000, Vision: true, Thinking: true}}},
		{ID: "zeta", Kind: "openai_compat"},
	}}
	got, err := withBedrock(c, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 3 || got.Providers[1].ID != "bedrock" || got.Providers[1].BaseURL != bedrockBaseURL {
		t.Fatalf("providers %+v", got.Providers)
	}
	want := []model{
		{ID: "amazon.nova-y-v1:0", Display: "Nova Y", Context: 300000, MaxOutput: 10000, Tools: true, Vision: true, Cache: "explicit",
			Price: &price{InPerM: 800000, OutPerM: 3200000, CacheReadPerM: 200000, Tiers: []tier{{AboveInput: 100000, Permille: 5000}}}},
		{ID: "anthropic.claude-x-v1:0", Display: "Claude X", Context: 200000, MaxOutput: 64000, Tools: true, Vision: true, Thinking: true, Cache: "explicit",
			Price:    &price{InPerM: 3000000, OutPerM: 15000000, CacheReadPerM: 300000, CacheWritePerM: 3750000},
			GeoPrice: &price{InPerM: 3300000, OutPerM: 16500000, CacheReadPerM: 330000, CacheWritePerM: 4125000}},
	}
	if !reflect.DeepEqual(got.Providers[1].Models, want) {
		b, _ := json.Marshal(got.Providers[1].Models)
		t.Fatalf("models %s", b)
	}
	// An unresolvable like fails the generation instead of guessing.
	o.Models[0].Like = "claude-missing"
	if _, err := withBedrock(c, o); err == nil {
		t.Fatal("unknown like accepted")
	}
}

func TestParseBedrockRejects(t *testing.T) {
	for _, bad := range []string{
		`{"models":[]}`,
		`{"source":"s","models":[{"id":"a","like":"x"}]}`,
		`{"source":"s","models":[{"id":"a","price":{"in":1,"out":1}}]}`,
		`{"source":"s","models":[{"id":"a","like":"x","price":{"in":1,"out":1}},{"id":"a","like":"x","price":{"in":1,"out":1}}]}`,
		`{"source":"s","models":[{"id":"a","like":"x","price":{"in":1,"out":1,"cache_read":2}}]}`,
		`{"source":"s","models":[{"id":"a","like":"x","price":{"in":1,"out":1},"geo_price":{"in":0.9,"out":1}}]}`,
		`{"source":"s","models":[{"id":"a","like":"x","price":{"in":1,"out":1},"tiers":[{"above_input":1,"permille":500}]}]}`,
		`{"source":"s","models":[{"id":"a","like":"x","price":{"in":1,"out":1},"typo":true}]}`,
	} {
		if _, err := parseBedrock([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := parseBedrock(bedrockJSON); err != nil {
		t.Fatalf("embedded overlay: %v", err)
	}
}

// TestCommittedCatalogMatchesBedrockOverlay keeps the embedded catalog's
// bedrock provider in step with bedrock.json and the Anthropic entries it
// copies from.
func TestCommittedCatalogMatchesBedrockOverlay(t *testing.T) {
	o, err := parseBedrock(bedrockJSON)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "provider", "catalog", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c catalog
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	want, err := bedrockProvider(o, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.Providers {
		if p.ID == "bedrock" {
			if !reflect.DeepEqual(p, want) {
				t.Fatal("catalog.json's bedrock provider differs from bedrock.json; regenerate the catalog")
			}
			return
		}
	}
	t.Fatal("catalog.json has no bedrock provider")
}
