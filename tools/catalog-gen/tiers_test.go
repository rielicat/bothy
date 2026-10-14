package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTierOverlay(t *testing.T) {
	rules, err := parseTiers(tiersJSON)
	if err != nil {
		t.Fatal(err)
	}
	c, unused := convert([]cwProvider{
		{ID: "gemini", Type: "google", Models: []cwModel{
			{ID: "gemini-2.5-pro", CostPer1MIn: 1.25, CostPer1MOut: 10, CostPer1MOutCached: 0.125},
			{ID: "gemini-2.5-flash", CostPer1MIn: 0.3, CostPer1MOut: 2.5},
			{ID: "gemini-3.1-pro-preview"}, // unpriced: a tier of an unknown price means nothing
		}},
		{ID: "xai", Type: "openai-compat", APIEndpoint: "https://api.x.ai/v1", Models: []cwModel{
			{ID: "grok-4.5", CostPer1MIn: 2, CostPer1MOut: 6},
		}},
	}, "t", rules)
	got := map[string][]tier{}
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if m.Price != nil {
				got[p.ID+"/"+m.ID] = m.Price.Tiers
			}
		}
	}
	want := map[string][]tier{
		"gemini/gemini-2.5-pro":   {{AboveInput: 200_000, Permille: 2000, OutPermille: 1500}},
		"gemini/gemini-2.5-flash": nil,
		"xai/grok-4.5":            {{AboveInput: 199_999, Permille: 2000}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tiers %+v, want %+v", got, want)
	}
	// Only rules for loaded providers can be stale; openai and openrouter were not loaded.
	if len(unused) != 0 {
		t.Fatalf("unused rules %+v", unused)
	}
	_, unused = convert([]cwProvider{{ID: "xai", Type: "openai-compat", APIEndpoint: "https://api.x.ai/v1",
		Models: []cwModel{{ID: "renamed-4.5", CostPer1MIn: 2}}}}, "t", rules)
	if len(unused) != 1 || unused[0].Provider != "xai" {
		t.Fatalf("a rule matching nothing must be reported: %+v", unused)
	}
}

func TestParseTiersRejects(t *testing.T) {
	for _, bad := range []string{
		`{"rules":[{"provider":"x","models":"(","tiers":[{"above_input":1,"permille":2000}],"source":"s"}]}`,
		`{"rules":[{"provider":"x","models":"^a","tiers":[{"above_input":1,"permille":2000}]}]}`,
		`{"rules":[{"provider":"x","models":"^a","tiers":[{"above_input":1,"permille":500}],"source":"s"}]}`,
		`{"rules":[{"provider":"x","models":"^a","tiers":[{"above_input":1,"permille":2000,"out_permille":900}],"source":"s"}]}`,
		`{"rules":[{"provider":"x","models":"^a","tiers":[{"permille":2000}],"source":"s"}]}`,
	} {
		if _, err := parseTiers([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// TestCommittedCatalogMatchesOverlay keeps the embedded catalog in step with
// tiers.json, so a hand edit of either one is caught before release.
func TestCommittedCatalogMatchesOverlay(t *testing.T) {
	rules, err := parseTiers(tiersJSON)
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
	tiered := 0
	for _, p := range c.Providers {
		if p.ID == "bedrock" {
			continue // its tiers come from bedrock.json (TestCommittedCatalogMatchesBedrockOverlay)
		}
		for _, m := range p.Models {
			var want []tier
			for _, r := range rules {
				if m.Price != nil && r.Provider == p.ID && r.re.MatchString(m.ID) {
					want = r.Tiers
				}
			}
			var have []tier
			if m.Price != nil {
				have = m.Price.Tiers
			}
			if !reflect.DeepEqual(have, want) {
				t.Errorf("%s/%s: catalog tiers %+v, tiers.json gives %+v", p.ID, m.ID, have, want)
			}
			if len(want) > 0 {
				tiered++
			}
		}
	}
	if tiered == 0 {
		t.Fatal("no catalog model carries a tier")
	}
}
