package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// bedrock.json is the hand-maintained Amazon Bedrock overlay: catwalk's
// Bedrock data has neither AWS's own prices (global and geographic profiles
// differ) nor Nova, so prices come from the AWS Price List API and Claude's
// window and capabilities from the Anthropic entries of the same model.
//
//go:embed bedrock.json
var bedrockJSON []byte

// bedrockBaseURL documents the endpoint; catalog.Find matches bedrock
// providers by kind, whatever their region.
const bedrockBaseURL = "https://bedrock-runtime.{region}.amazonaws.com"

type usdPrice struct {
	In         float64 `json:"in"`
	Out        float64 `json:"out"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type bedrockModel struct {
	ID        string    `json:"id"`
	Like      string    `json:"like"` // Anthropic catalog id to copy window and capabilities from
	Display   string    `json:"display"`
	Context   int       `json:"context"`
	MaxOutput int       `json:"max_output"`
	Vision    bool      `json:"vision"`
	Thinking  bool      `json:"thinking"`
	Price     *usdPrice `json:"price"`
	GeoPrice  *usdPrice `json:"geo_price"`
	Tiers     []tier    `json:"tiers"`
	Note      string    `json:"note"`
}

type bedrockOverlay struct {
	Source string         `json:"source"`
	Models []bedrockModel `json:"models"`
}

func parseBedrock(b []byte) (bedrockOverlay, error) {
	var o bedrockOverlay
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return o, fmt.Errorf("bedrock.json: %w", err)
	}
	if o.Source == "" {
		return o, errors.New("bedrock.json: source is required")
	}
	seen := map[string]bool{}
	for _, m := range o.Models {
		switch {
		case m.ID == "" || seen[m.ID]:
			return o, fmt.Errorf("bedrock.json: missing or duplicate id %q", m.ID)
		case m.Price == nil:
			return o, fmt.Errorf("bedrock.json: %s: price is required", m.ID)
		case m.Like == "" && (m.Display == "" || m.Context <= 0 || m.MaxOutput <= 0):
			return o, fmt.Errorf("bedrock.json: %s: without like, display, context and max_output are required", m.ID)
		}
		seen[m.ID] = true
		for _, p := range []*usdPrice{m.Price, m.GeoPrice} {
			if p == nil {
				continue
			}
			if p.In < 0 || p.Out < 0 || p.CacheRead < 0 || p.CacheWrite < 0 || p.CacheRead > p.In {
				return o, fmt.Errorf("bedrock.json: %s: negative price or a cache read above input", m.ID)
			}
		}
		if g := m.GeoPrice; g != nil && (g.In < m.Price.In || g.Out < m.Price.Out) {
			return o, fmt.Errorf("bedrock.json: %s: geo_price below the global price", m.ID)
		}
		for _, t := range m.Tiers {
			if t.AboveInput <= 0 || t.Permille < 1000 {
				return o, fmt.Errorf("bedrock.json: %s: a tier needs above_input and a permille of at least 1000", m.ID)
			}
		}
	}
	return o, nil
}

func (p usdPrice) micros(tiers []tier) *price {
	return &price{InPerM: micros(p.In), OutPerM: micros(p.Out), CacheReadPerM: micros(p.CacheRead),
		CacheWritePerM: micros(p.CacheWrite), Tiers: slices.Clone(tiers)}
}

// bedrockProvider builds the catalog's bedrock provider from the overlay and
// the generated catalog's Anthropic entries.
func bedrockProvider(o bedrockOverlay, c catalog) (provider, error) {
	byID := map[string]model{}
	for _, p := range c.Providers {
		if p.Kind == "anthropic" {
			for _, m := range p.Models {
				byID[m.ID] = m
			}
		}
	}
	out := provider{ID: "bedrock", Kind: "bedrock", BaseURL: bedrockBaseURL}
	for _, bm := range o.Models {
		m := model{ID: bm.ID, Display: bm.Display, Context: bm.Context, MaxOutput: bm.MaxOutput,
			Tools: true, Vision: bm.Vision, Thinking: bm.Thinking, Cache: "explicit"}
		if bm.Like != "" {
			a, ok := byID[bm.Like]
			if !ok {
				return provider{}, fmt.Errorf("bedrock.json: %s: like %q is not in the catalog's Anthropic models", bm.ID, bm.Like)
			}
			m.Display, m.Context, m.MaxOutput, m.Vision, m.Thinking = a.Display, a.Context, a.MaxOutput, a.Vision, a.Thinking
		}
		m.Price = bm.Price.micros(bm.Tiers)
		if bm.GeoPrice != nil && *bm.GeoPrice != *bm.Price {
			m.GeoPrice = bm.GeoPrice.micros(bm.Tiers)
		}
		out.Models = append(out.Models, m)
	}
	slices.SortFunc(out.Models, func(a, b model) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// withBedrock adds the bedrock provider to a converted catalog.
func withBedrock(c catalog, o bedrockOverlay) (catalog, error) {
	bp, err := bedrockProvider(o, c)
	if err != nil {
		return c, err
	}
	c.Providers = slices.DeleteFunc(c.Providers, func(p provider) bool { return p.ID == bp.ID })
	c.Providers = append(c.Providers, bp)
	slices.SortFunc(c.Providers, func(a, b provider) int { return strings.Compare(a.ID, b.ID) })
	return c, nil
}
