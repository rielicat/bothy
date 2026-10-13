package catalog

import (
	"testing"

	"github.com/bothyhq/bothy/internal/core"
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
		case core.KindAnthropic, core.KindOpenAI, core.KindGemini, core.KindOpenAICompat:
		default:
			t.Fatalf("%s: kind %q", p.ID, p.Kind)
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
	for _, b := range []string{`{`, `{"version":2}`, `{"version":1,"providers":[{"id":"p","models":[{"id":"m","price":{"in_per_m":-1,"out_per_m":0}}]}]}`} {
		if _, err := Parse([]byte(b)); err == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}
