// Package catalog holds the embedded model catalog (catalog.json, generated
// at release time by tools/catalog-gen from catwalk) and its lookups: window,
// output limit, capabilities and USD-micros pricing per model. Owner: M2.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

//go:embed catalog.json
var embedded []byte

// Catalog is the parsed catalog.json.
type Catalog struct {
	Version   int        `json:"version"`
	Source    string     `json:"source"`
	Providers []Provider `json:"providers"`
}

// Provider is one vendor endpoint. First-party kinds are matched by kind;
// openai_compat entries by the host of BaseURL.
type Provider struct {
	ID      string            `json:"id"`
	Kind    core.ProviderKind `json:"kind"`
	BaseURL string            `json:"base_url"`
	Models  []Model           `json:"models"`
}

// Model is one catalog entry. Price is nil when the catalog does not know it.
type Model struct {
	ID        string `json:"id"`
	Display   string `json:"display,omitempty"`
	Context   int    `json:"context,omitempty"`
	MaxOutput int    `json:"max_output,omitempty"`
	Tools     bool   `json:"tools,omitempty"`
	Vision    bool   `json:"vision,omitempty"`
	Thinking  bool   `json:"thinking,omitempty"`
	Cache     string `json:"cache,omitempty"` // explicit | implicit | ""
	Price     *Price `json:"price,omitempty"`
}

// Price is USD micros per 1M tokens.
type Price struct {
	InPerM         core.Micros      `json:"in_per_m"`
	OutPerM        core.Micros      `json:"out_per_m"`
	CacheReadPerM  core.Micros      `json:"cache_read_per_m,omitempty"`
	CacheWritePerM core.Micros      `json:"cache_write_per_m,omitempty"`
	Tiers          []core.PriceTier `json:"tiers,omitempty"`
}

var (
	once     sync.Once
	def      *Catalog
	errParse error
)

// Default returns the embedded catalog. A broken embed is a build defect, so
// it yields an empty catalog (unknown prices) rather than failing the boot;
// the package test guarantees the file parses.
func Default() *Catalog {
	once.Do(func() {
		def, errParse = Parse(embedded)
		if errParse != nil {
			def = &Catalog{}
		}
	})
	return def
}

// Parse decodes and checks a catalog.
func Parse(b []byte) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if c.Version != 1 {
		return nil, fmt.Errorf("catalog: unsupported version %d", c.Version)
	}
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if m.Price != nil && (m.Price.InPerM < 0 || m.Price.OutPerM < 0 || m.Price.CacheReadPerM < 0 || m.Price.CacheWritePerM < 0) {
				return nil, fmt.Errorf("catalog: %s/%s: negative price", p.ID, m.ID)
			}
		}
	}
	return &c, nil
}

// Find returns the catalog entry for a configured provider, or nil.
func (c *Catalog) Find(kind core.ProviderKind, baseURL string) *Provider {
	if c == nil {
		return nil
	}
	host := hostOf(baseURL)
	for i := range c.Providers {
		p := &c.Providers[i]
		switch kind {
		case core.KindAnthropic, core.KindOpenAI, core.KindGemini:
			if p.Kind == kind {
				return p
			}
		case core.KindOpenAICompat:
			if p.Kind == kind && host != "" && hostOf(p.BaseURL) == host {
				return p
			}
		}
	}
	return nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

var dateSuffix = regexp.MustCompile(`-(20[0-9]{6}|[0-9]{4}-[0-9]{2}-[0-9]{2})$`)

// Model finds id exactly or, failing that, ignoring a trailing release date
// ("claude-haiku-4-5" matches "claude-haiku-4-5-20251001" and back).
func (p *Provider) Model(id string) (Model, bool) {
	if p == nil {
		return Model{}, false
	}
	for _, m := range p.Models {
		if m.ID == id {
			return m, true
		}
	}
	base := dateSuffix.ReplaceAllString(id, "")
	for _, m := range p.Models {
		if dateSuffix.ReplaceAllString(m.ID, "") == base {
			return m, true
		}
	}
	return Model{}, false
}

// Info converts an entry into a ModelInfo for ref.
func (m Model) Info(ref core.ModelRef) core.ModelInfo {
	info := core.ModelInfo{
		Ref: ref, Display: m.Display, Context: m.Context, MaxOutput: m.MaxOutput,
		Tools: m.Tools, Vision: m.Vision, Thinking: m.Thinking, Source: "catalog",
	}
	switch m.Cache {
	case "explicit":
		info.Cache = core.CacheExplicit
	case "implicit":
		info.Cache = core.CacheImplicit
	}
	if m.Price != nil {
		info.Price = core.Price{
			InPerM: m.Price.InPerM, OutPerM: m.Price.OutPerM,
			CacheReadPerM: m.Price.CacheReadPerM, CacheWritePerM: m.Price.CacheWritePerM,
			Tiers: append([]core.PriceTier(nil), m.Price.Tiers...), Known: true,
		}
	}
	return info
}
