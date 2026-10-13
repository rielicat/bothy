// Command catalog-gen writes internal/provider/catalog/catalog.json from
// catwalk provider data (github.com/charmbracelet/catwalk). It reads the
// published JSON (the per-provider files under internal/providers/configs or
// the array the catwalk service returns) instead of importing catwalk, so the
// tools module needs no extra dependency. Long-context price tiers, which
// catwalk lacks, come from the embedded tiers.json.
//
//	go run ./catalog-gen -source "catwalk v0.52.69" -out ../internal/provider/catalog/catalog.json configs/anthropic.json configs/openai.json …
//
// Arguments are files, directories of *.json, or http(s) URLs.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// catwalk's JSON shapes (pkg/catwalk/provider.go).
type cwProvider struct {
	Name        string    `json:"name"`
	ID          string    `json:"id"`
	APIEndpoint string    `json:"api_endpoint"`
	Type        string    `json:"type"`
	Models      []cwModel `json:"models"`
}

type cwModel struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	CostPer1MIn        float64 `json:"cost_per_1m_in"`
	CostPer1MOut       float64 `json:"cost_per_1m_out"`
	CostPer1MInCached  float64 `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached float64 `json:"cost_per_1m_out_cached"`
	ContextWindow      int64   `json:"context_window"`
	DefaultMaxTokens   int64   `json:"default_max_tokens"`
	CanReason          bool    `json:"can_reason"`
	SupportsImages     bool    `json:"supports_attachments"`
}

// Output shapes; they mirror internal/provider/catalog.
type catalog struct {
	Version   int        `json:"version"`
	Source    string     `json:"source"`
	Providers []provider `json:"providers"`
}

type provider struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	BaseURL string  `json:"base_url"`
	Models  []model `json:"models"`
}

type model struct {
	ID        string `json:"id"`
	Display   string `json:"display,omitempty"`
	Context   int    `json:"context,omitempty"`
	MaxOutput int    `json:"max_output,omitempty"`
	Tools     bool   `json:"tools,omitempty"`
	Vision    bool   `json:"vision,omitempty"`
	Thinking  bool   `json:"thinking,omitempty"`
	Cache     string `json:"cache,omitempty"`
	Price     *price `json:"price,omitempty"`
}

type price struct {
	InPerM         int64  `json:"in_per_m"`
	OutPerM        int64  `json:"out_per_m"`
	CacheReadPerM  int64  `json:"cache_read_per_m,omitempty"`
	CacheWritePerM int64  `json:"cache_write_per_m,omitempty"`
	Tiers          []tier `json:"tiers,omitempty"`
}

// tier mirrors core.PriceTier.
type tier struct {
	AboveInput         int `json:"above_input"`
	Permille           int `json:"permille"`
	OutPermille        int `json:"out_permille,omitempty"`
	CacheReadPermille  int `json:"cache_read_permille,omitempty"`
	CacheWritePermille int `json:"cache_write_permille,omitempty"`
}

// tierRule is one entry of tiers.json, the hand-maintained overlay of
// long-context prices: catwalk has no tier data.
type tierRule struct {
	Provider string         `json:"provider"` // catwalk provider id
	Models   string         `json:"models"`   // regexp over model ids
	Tiers    []tier         `json:"tiers"`
	Source   string         `json:"source"` // where and when the prices were read
	re       *regexp.Regexp // compiled Models
}

//go:embed tiers.json
var tiersJSON []byte

func parseTiers(b []byte) ([]tierRule, error) {
	var f struct {
		Rules []tierRule `json:"rules"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("tiers.json: %w", err)
	}
	for i := range f.Rules {
		r := &f.Rules[i]
		re, err := regexp.Compile(r.Models)
		if err != nil {
			return nil, fmt.Errorf("tiers.json: rule %d: %w", i, err)
		}
		r.re = re
		if r.Provider == "" || len(r.Tiers) == 0 || r.Source == "" {
			return nil, fmt.Errorf("tiers.json: rule %d needs provider, tiers and source", i)
		}
		for _, t := range r.Tiers {
			for _, m := range []int{t.Permille, t.OutPermille, t.CacheReadPermille, t.CacheWritePermille} {
				if m != 0 && m < 1000 {
					return nil, fmt.Errorf("tiers.json: rule %d: multipliers below 1000 (1x) are ignored by core.Price.Cost", i)
				}
			}
			if t.Permille == 0 || t.AboveInput <= 0 {
				return nil, fmt.Errorf("tiers.json: rule %d: every tier needs above_input and permille", i)
			}
		}
	}
	return f.Rules, nil
}

// firstParty maps catwalk's vendor types to Bothy kinds and the base URL the
// clients default to (catwalk leaves these as $VARIABLES).
var firstParty = map[string][2]string{
	"anthropic": {"anthropic", "https://api.anthropic.com/v1"},
	"openai":    {"openai", "https://api.openai.com/v1"},
	"google":    {"gemini", "https://generativelanguage.googleapis.com/v1beta"},
}

func main() {
	out := flag.String("out", "", "catalog.json to write (default stdout)")
	source := flag.String("source", "catwalk", "provenance recorded in the catalog")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "catalog-gen: give catwalk JSON files, directories or URLs")
		os.Exit(2)
	}
	rules, err := parseTiers(tiersJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog-gen:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var all []cwProvider
	for _, arg := range flag.Args() {
		ps, err := load(ctx, arg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "catalog-gen:", err)
			os.Exit(1)
		}
		all = append(all, ps...)
	}
	c, unused := convert(all, *source, rules)
	for _, r := range unused {
		fmt.Fprintf(os.Stderr, "catalog-gen: warning: tiers.json rule %s %q matched no priced model; check for renamed models\n", r.Provider, r.Models)
	}
	b, err := render(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog-gen:", err)
		os.Exit(1)
	}
	if *out == "" {
		_, _ = os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "catalog-gen:", err)
		os.Exit(1)
	}
}

func load(ctx context.Context, arg string) ([]cwProvider, error) {
	if strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, arg, nil)
		if err != nil {
			return nil, err
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", arg, resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return nil, err
		}
		return decode(arg, b)
	}
	st, err := os.Stat(arg)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		b, err := os.ReadFile(arg)
		if err != nil {
			return nil, err
		}
		return decode(arg, b)
	}
	files, err := filepath.Glob(filepath.Join(arg, "*.json"))
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	var out []cwProvider
	for _, f := range files {
		ps, err := load(ctx, f)
		if err != nil {
			return nil, err
		}
		out = append(out, ps...)
	}
	return out, nil
}

// decode accepts one provider object or an array of them.
func decode(name string, b []byte) ([]cwProvider, error) {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var ps []cwProvider
		if err := json.Unmarshal(b, &ps); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return ps, nil
	}
	var p cwProvider
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if p.ID == "" {
		return nil, errors.New(name + ": not a catwalk provider")
	}
	return []cwProvider{p}, nil
}

func micros(usd float64) int64 { return int64(math.Round(usd * 1e6)) }

// convert maps catwalk providers to the catalog and applies the tier rules;
// it also returns the rules that matched no priced model of a loaded provider.
func convert(ps []cwProvider, source string, rules []tierRule) (catalog, []tierRule) {
	c := catalog{Version: 1, Source: source}
	seen := map[string]bool{}
	used := make([]bool, len(rules))
	for _, p := range ps {
		kind, base := "", ""
		if fp, ok := firstParty[p.Type]; ok {
			kind, base = fp[0], fp[1]
		} else if p.Type == "openai-compat" || p.Type == "openrouter" || p.Type == "vercel" {
			if !strings.HasPrefix(p.APIEndpoint, "https://") {
				continue // an unresolved $VARIABLE endpoint cannot be matched
			}
			kind, base = "openai_compat", strings.TrimRight(p.APIEndpoint, "/")
		} else {
			continue // azure, bedrock, vertex: not a Bothy kind
		}
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out := provider{ID: p.ID, Kind: kind, BaseURL: base}
		ids := map[string]bool{}
		for _, m := range p.Models {
			if m.ID == "" || ids[m.ID] {
				continue
			}
			ids[m.ID] = true
			cm := convertModel(kind, m)
			for i, r := range rules {
				if cm.Price != nil && r.Provider == p.ID && r.re.MatchString(m.ID) {
					cm.Price.Tiers, used[i] = slices.Clone(r.Tiers), true
				}
			}
			out.Models = append(out.Models, cm)
		}
		slices.SortFunc(out.Models, func(a, b model) int { return strings.Compare(a.ID, b.ID) })
		c.Providers = append(c.Providers, out)
	}
	slices.SortFunc(c.Providers, func(a, b provider) int { return strings.Compare(a.ID, b.ID) })
	var unused []tierRule
	for i, r := range rules {
		if !used[i] && seen[r.Provider] {
			unused = append(unused, r)
		}
	}
	return c, unused
}

// convertModel maps catwalk's prices. catwalk's "in_cached" is the cache
// write price and "out_cached" the cache read price (Anthropic: 3.75 and
// 0.30 against 3.00 input); some entries put a read price in "in_cached"
// instead, recognisable because a cache write never costs less than input.
func convertModel(kind string, m cwModel) model {
	out := model{
		ID: m.ID, Display: m.Name, Context: int(m.ContextWindow), MaxOutput: int(m.DefaultMaxTokens),
		Tools: true, Vision: m.SupportsImages, Thinking: m.CanReason,
	}
	read, write := m.CostPer1MOutCached, m.CostPer1MInCached
	if read == 0 && write > 0 && write < m.CostPer1MIn {
		read, write = write, 0
	}
	free := strings.HasSuffix(m.ID, ":free")
	if m.CostPer1MIn > 0 || m.CostPer1MOut > 0 || read > 0 || write > 0 || free {
		out.Price = &price{InPerM: micros(m.CostPer1MIn), OutPerM: micros(m.CostPer1MOut), CacheReadPerM: micros(read), CacheWritePerM: micros(write)}
	}
	switch {
	case kind == "anthropic":
		out.Cache = "explicit"
	case kind == "openai" || kind == "gemini" || read > 0:
		out.Cache = "implicit"
	}
	return out
}

// render writes one model per line so catalog updates diff cleanly.
func render(c catalog) ([]byte, error) {
	var buf bytes.Buffer
	enc := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
	fmt.Fprintf(&buf, "{\"version\":%d,\"source\":", c.Version)
	if err := enc(c.Source); err != nil {
		return nil, err
	}
	buf.WriteString(",\"providers\":[\n")
	for i, p := range c.Providers {
		buf.WriteString("{\"id\":")
		_ = enc(p.ID)
		buf.WriteString(",\"kind\":")
		_ = enc(p.Kind)
		buf.WriteString(",\"base_url\":")
		_ = enc(p.BaseURL)
		buf.WriteString(",\"models\":[\n")
		for j, m := range p.Models {
			if err := enc(m); err != nil {
				return nil, err
			}
			if j < len(p.Models)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString("]}")
		if i < len(c.Providers)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("]}\n")
	if !json.Valid(buf.Bytes()) {
		return nil, errors.New("rendered catalog is not valid JSON")
	}
	return buf.Bytes(), nil
}
