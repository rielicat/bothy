package catalog

import (
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestEmbeddedCatalogParses(t *testing.T) {
	c, err := Parse(embedded)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) == 0 || c.Source == "" {
		t.Fatalf("catalog %+v", c)
	}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if seen[p.ID] || p.BaseURL == "" || len(p.Models) == 0 {
			t.Fatalf("provider %+v", p)
		}
		seen[p.ID] = true
		switch p.Kind {
		case core.KindAnthropic, core.KindOpenAI, core.KindGemini, core.KindOpenAICompat, core.KindBedrock:
		default:
			t.Fatalf("%s: kind %q", p.ID, p.Kind)
		}
		// A cache hit never costs more than the uncached input it replaces;
		// a higher read price is a source-data error that overbills every
		// cached prompt.
		for _, m := range p.Models {
			for _, pr := range []*Price{m.Price, m.GeoPrice} {
				if pr != nil && pr.CacheReadPerM > pr.InPerM {
					t.Errorf("%s/%s: cache read %d above input %d per M", p.ID, m.ID, pr.CacheReadPerM, pr.InPerM)
				}
			}
			if m.GeoPrice != nil && p.Kind != core.KindBedrock {
				t.Errorf("%s/%s: geo_price outside bedrock", p.ID, m.ID)
			}
		}
	}
	if Default() == nil || len(Default().Providers) != len(c.Providers) {
		t.Fatal("Default differs from the embedded file")
	}
}

func TestFindAndModel(t *testing.T) {
	c := Default()
	a := c.Find(core.KindAnthropic, "")
	m, ok := a.Model("claude-haiku-4-5") // catalog id carries a date suffix
	if !ok || m.ID != "claude-haiku-4-5-20251001" || m.Price == nil || m.Price.InPerM != 1_000_000 || m.Cache != "explicit" {
		t.Fatalf("%v %+v", ok, m)
	}
	if _, ok := a.Model("claude-haiku-4-5-20990101"); !ok {
		t.Fatal("dated request id should match the undated family")
	}
	// Compatible providers are matched by host, whatever the path or case.
	if p := c.Find(core.KindOpenAICompat, "https://OpenRouter.ai/api/v1/"); p == nil || p.ID != "openrouter" {
		t.Fatalf("openrouter %+v", p)
	}
	if p := c.Find(core.KindOpenAICompat, "http://localhost:1234/v1"); p != nil {
		t.Fatalf("local server matched %s", p.ID)
	}
	if c.Find(core.KindOllama, "http://localhost:11434") != nil {
		t.Fatal("ollama has no catalog")
	}
	var nilProvider *Provider
	if _, ok := nilProvider.Model("x"); ok {
		t.Fatal("nil provider")
	}
}

func TestInfo(t *testing.T) {
	m := Model{ID: "x", Display: "X", Context: 1000, MaxOutput: 100, Tools: true, Vision: true, Cache: "implicit",
		Price: &Price{InPerM: 1, OutPerM: 2, CacheReadPerM: 3, Tiers: []core.PriceTier{{AboveInput: 10, Permille: 2000}}}}
	info := m.Info(core.ModelRef{Provider: "p", Model: "x"})
	if info.Source != "catalog" || info.Cache != core.CacheImplicit || !info.Price.Known || info.Price.CacheReadPerM != 3 || len(info.Price.Tiers) != 1 {
		t.Fatalf("%+v", info)
	}
	info.Price.Tiers[0].Permille = 1 // the catalog's slice is not shared
	if m.Price.Tiers[0].Permille != 2000 {
		t.Fatal("tiers aliased")
	}
	if (Model{ID: "y"}).Info(core.ModelRef{}).Price.Known {
		t.Fatal("no price must mean unknown")
	}
}

func TestParseRejects(t *testing.T) {
	for _, b := range []string{`{`, `{"version":2}`, `{"version":1,"providers":[{"id":"p","models":[{"id":"m","price":{"in_per_m":-1,"out_per_m":0}}]}]}`,
		`{"version":1,"providers":[{"id":"p","models":[{"id":"m","geo_price":{"in_per_m":1,"out_per_m":-1}}]}]}`} {
		if _, err := Parse([]byte(b)); err == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}

func TestBedrockLookup(t *testing.T) {
	b := Default().Find(core.KindBedrock, "https://bedrock-runtime.eu-west-1.amazonaws.com")
	if b == nil || b.ID != "bedrock" {
		t.Fatalf("bedrock provider %+v", b)
	}
	for _, tc := range []struct {
		id       string
		in, out  core.Micros
		cacheRd  core.Micros
		display  string
		hasTiers bool
	}{
		{"global.anthropic.claude-sonnet-4-5-20250929-v1:0", 3_000_000, 15_000_000, 300_000, "Claude Sonnet 4.5", false},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", 3_300_000, 16_500_000, 330_000, "Claude Sonnet 4.5", false},
		{"eu.anthropic.claude-opus-5-5", 4_400_000, 22_000_000, 220_000, "Claude Opus 5.5", false},
		{"arn:aws:bedrock:us-west-2:111122223333:inference-profile/global.anthropic.claude-opus-5-5", 4_000_000, 20_000_000, 200_000, "Claude Opus 5.5", false},
		{"anthropic.claude-sonnet-4-20250514-v1:0", 3_000_000, 15_000_000, 300_000, "Claude Sonnet 4", false},
		{"us.amazon.nova-pro-v1:0", 800_000, 3_200_000, 200_000, "Nova Pro", false},
		{"amazon.nova-2-lite-v1:0", 330_000, 2_750_000, 82_500, "Nova 2 Lite", false},
		{"global.anthropic.claude-haiku-5-5", 100_000, 500_000, 10_000, "Claude Haiku 5.5", true},
	} {
		m, ok := b.Model(tc.id)
		if !ok || m.Price == nil || m.Price.InPerM != tc.in || m.Price.OutPerM != tc.out || m.Price.CacheReadPerM != tc.cacheRd ||
			m.Display != tc.display || m.Cache != "explicit" || !m.Tools || m.GeoPrice != nil || (len(m.Price.Tiers) > 0) != tc.hasTiers {
			t.Errorf("%s: %v %+v %+v", tc.id, ok, m, m.Price)
		}
	}
	m, _ := b.Model("global.anthropic.claude-haiku-5-5")
	// Above 100K prompt tokens Haiku 5.5 bills 5x.
	if got := m.Info(core.ModelRef{}).Price.Cost(core.Usage{Input: 200_000, Output: 1_000}); got != 102_500 {
		t.Fatalf("long-context cost %d", got)
	}
	if _, ok := b.Model("us.meta.llama3-3-70b-instruct-v1:0"); ok {
		t.Fatal("unknown model found")
	}
	if _, ok := b.Model("claude-sonnet-4-5"); ok {
		t.Fatal("an Anthropic API id must not match a Bedrock entry")
	}
}
