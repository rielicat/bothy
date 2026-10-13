package provider

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/ollama"
)

const (
	discoveryTTL     = 10 * time.Minute
	discoveryFailTTL = time.Minute
	discoveryTimeout = 5 * time.Second
)

// Windows assumed when neither config, catalog nor discovery knows a model.
// They err large for the vendors (every current model there has at least
// this) and small for compatible and local servers, where an oversized
// num_ctx can fail to load.
var defaultContext = map[core.ProviderKind]int{
	core.KindAnthropic:    200_000,
	core.KindOpenAI:       128_000,
	core.KindGemini:       1_000_000,
	core.KindOpenAICompat: 32_768,
	core.KindOllama:       8_192,
}

type discovery struct {
	at     time.Time
	ok     bool
	models map[string]core.ModelInfo
}

// discovered returns cached discovery data for a model.
func (f *Facade) discovered(id, model string) (core.ModelInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.disc[id]
	if d == nil {
		return core.ModelInfo{}, false
	}
	m, ok := d.models[model]
	return m, ok
}

// remember stores a full discovery result; a failure keeps what an earlier
// success found.
func (f *Facade) remember(id string, ms []core.ModelInfo, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.disc[id]
	if d == nil || ok {
		d = &discovery{models: map[string]core.ModelInfo{}}
		f.disc[id] = d
	}
	d.at, d.ok = f.clock.Now(), ok
	for _, m := range ms {
		d.models[m.Ref.Model] = m
	}
}

// learn adds one model's discovered data without touching the list's age.
func (f *Facade) learn(id string, m core.ModelInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.disc[id]
	if d == nil {
		d = &discovery{models: map[string]core.ModelInfo{}}
		f.disc[id] = d
	}
	d.models[m.Ref.Model] = m
}

// refresh runs discovery for an entry when its cache is stale.
func (f *Facade) refresh(ctx context.Context, e *entry) {
	f.mu.Lock()
	d := f.disc[e.id]
	fresh := d != nil && (d.ok && f.clock.Now().Sub(d.at) < discoveryTTL || !d.ok && f.clock.Now().Sub(d.at) < discoveryFailTTL)
	f.mu.Unlock()
	if fresh {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	ms, err := e.cl.Discover(ctx)
	if err != nil {
		f.log.Debug("model discovery failed", "provider", e.id, "err", err)
	}
	f.remember(e.id, ms, err == nil)
}

// ensureContext fills an Ollama model's window from /api/show and /api/ps
// (ollama.Window) before its first call when config does not set it, because
// that window is what num_ctx sends.
func (f *Facade) ensureContext(ctx context.Context, e *entry, m *core.ModelInfo) {
	if e.kind != core.KindOllama || configured(e.cfg, m.Ref.Model).Context > 0 {
		return
	}
	if d, ok := f.discovered(e.id, m.Ref.Model); ok && d.Context > 0 {
		return
	}
	oc, ok := e.cl.(*ollama.Client)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	s, err := oc.Show(ctx, m.Ref.Model)
	if err != nil {
		f.log.Debug("ollama show failed", "provider", e.id, "model", m.Ref.Model, "err", err)
		return
	}
	if running, err := oc.Running(ctx); err == nil {
		s.Served = running[m.Ref.Model]
	}
	f.learn(e.id, oc.Info(m.Ref.Model, s))
	*m = f.resolve(e, m.Ref.Model)
}

func configured(p config.Provider, model string) config.ProviderModel {
	for _, m := range p.Models {
		if m.ID == model {
			return m
		}
	}
	return config.ProviderModel{}
}

// resolve builds the ModelInfo for a model of an entry. Precedence, lowest
// first: kind defaults < discovery < catalog < config models < pricing.
func (f *Facade) resolve(e *entry, model string) core.ModelInfo {
	ref := core.ModelRef{Provider: e.id, Model: model}
	info := core.ModelInfo{Ref: ref, Tools: true, Source: "config"}
	if d, ok := f.discovered(e.id, model); ok {
		info = d
		info.Ref = ref
	}
	if cm, ok := f.cat.Find(e.kind, e.base).Model(model); ok {
		ci := cm.Info(ref)
		if ci.Context == 0 {
			ci.Context = info.Context
		}
		if ci.MaxOutput == 0 {
			ci.MaxOutput = info.MaxOutput
		}
		info = ci
	}
	if pm := configured(e.cfg, model); pm.ID != "" {
		if pm.Context > 0 {
			info.Context = pm.Context
		}
		if pm.MaxOutput > 0 {
			info.MaxOutput = pm.MaxOutput
		}
		if pm.Tools != nil {
			info.Tools = *pm.Tools
		}
		if pm.Vision {
			info.Vision = true
		}
		info.Source = "config"
	}
	if info.Display == "" {
		info.Display = model
	}
	if info.Context <= 0 {
		info.Context = defaultContext[e.kind]
	}
	if info.MaxOutput <= 0 {
		info.MaxOutput = min(8192, max(256, info.Context/4))
	}
	switch e.kind {
	case core.KindAnthropic:
		info.Cache = core.CacheExplicit
	case core.KindOpenAI, core.KindGemini:
		if info.Cache == core.CacheNone {
			info.Cache = core.CacheImplicit
		}
	case core.KindOllama:
		info.Cache = core.CacheNone
	}
	info.Local = e.kind == core.KindOllama || e.cfg.AllowPrivate
	if info.Local && !info.Price.Known {
		info.Price = core.Price{Known: true}
	}
	if cfg := f.d.Config.Current(); cfg != nil {
		if po, ok := cfg.Pricing[ref.String()]; ok {
			info.Price = core.Price{
				InPerM: core.MicrosFromUSD(po.In), OutPerM: core.MicrosFromUSD(po.Out),
				CacheReadPerM: core.MicrosFromUSD(po.CacheRead), CacheWritePerM: core.MicrosFromUSD(po.CacheWrite),
				Tiers: overrideTiers(po.Tiers, info.Price.Tiers), Known: true,
			}
		}
	}
	return info
}

// overrideTiers converts a pricing override's tiers (keys validated by
// config). Without any, the catalog's stay: they are multipliers of whatever
// base price applies, and the vendor's long-context surcharge still does.
func overrideTiers(tiers map[string]config.PriceTierO, catalog []core.PriceTier) []core.PriceTier {
	if len(tiers) == 0 {
		return catalog
	}
	out := make([]core.PriceTier, 0, len(tiers))
	for k, t := range tiers {
		above, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		out = append(out, core.PriceTier{AboveInput: above, Permille: t.Permille, OutPermille: t.OutPermille,
			CacheReadPermille: t.CacheReadPermille, CacheWritePermille: t.CacheWritePermille})
	}
	slices.SortFunc(out, func(a, b core.PriceTier) int { return cmp.Compare(a.AboveInput, b.AboveInput) })
	return out
}

// Chain resolves the fallback chain: the override (if any) first, then the
// slot's list; the fast slot falls back to main when it is empty. Refs whose
// provider is unknown or disabled are left out.
func (f *Facade) Chain(slot core.Slot, override *core.ModelRef) []core.ModelInfo {
	cfg := f.d.Config.Current()
	var refs []string
	if override != nil {
		refs = append(refs, override.String())
	}
	if cfg != nil {
		list := cfg.Models.Main
		if slot == core.SlotFast && len(cfg.Models.Fast) > 0 {
			list = cfg.Models.Fast
		}
		refs = append(refs, list...)
	}
	var out []core.ModelInfo
	seen := map[string]bool{}
	for _, s := range refs {
		if seen[s] {
			continue
		}
		seen[s] = true
		ref, err := core.ParseModelRef(s)
		if err != nil {
			continue
		}
		if m, ok := f.Model(ref); ok {
			out = append(out, m)
		}
	}
	return out
}

func (f *Facade) Model(ref core.ModelRef) (core.ModelInfo, bool) {
	e, err := f.entry(ref.Provider)
	if err != nil || ref.Model == "" {
		return core.ModelInfo{}, false
	}
	return f.resolve(e, ref.Model), true
}

// Models lists every model of the enabled providers: configured ones, the
// catalog's for that endpoint and, with discover on, what the server lists.
func (f *Facade) Models(ctx context.Context) []core.ModelInfo {
	cfg := f.d.Config.Current()
	if cfg == nil {
		return nil
	}
	var es []*entry
	for _, p := range cfg.Providers {
		if !enabled(p) {
			continue
		}
		if e, err := f.entry(p.ID); err == nil {
			es = append(es, e)
		}
	}
	var wg sync.WaitGroup
	for _, e := range es {
		if e.cfg.Discover {
			wg.Add(1)
			go func() {
				defer wg.Done()
				f.refresh(ctx, e)
			}()
		}
	}
	wg.Wait()
	byRef := map[string]core.ModelInfo{}
	for _, e := range es {
		var names []string
		for _, m := range e.cfg.Models {
			names = append(names, m.ID)
		}
		if cp := f.cat.Find(e.kind, e.base); cp != nil {
			for _, m := range cp.Models {
				names = append(names, m.ID)
			}
		}
		f.mu.Lock()
		if d := f.disc[e.id]; d != nil {
			for name := range d.models {
				names = append(names, name)
			}
		}
		f.mu.Unlock()
		for _, n := range names {
			ref := core.ModelRef{Provider: e.id, Model: n}
			if _, ok := byRef[ref.String()]; !ok {
				byRef[ref.String()] = f.resolve(e, n)
			}
		}
	}
	out := make([]core.ModelInfo, 0, len(byRef))
	for _, m := range byRef {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b core.ModelInfo) int { return cmp.Compare(a.Ref.String(), b.Ref.String()) })
	return out
}
