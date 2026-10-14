package provider

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/catalog"
	"github.com/rielicat/bothy/internal/provider/openai"
)

type Deps struct {
	Config config.View
	Vault  core.Vault
	Egress core.Egress     // SystemClient per provider base URL
	Env    func() []string // Detect only: names of well-known key env vars
	Clock  core.Clock
	Log    *slog.Logger
	Bus    core.Bus // optional: provider_auth banner on a rejected key (§5.14)
}

// Facade implements core.LLM and core.CheckProvider.
type Facade struct {
	d     Deps
	cat   *catalog.Catalog
	clock core.Clock
	log   *slog.Logger
	boot  time.Time

	// sleep and jitter are replaced in tests.
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() float64

	mu      sync.Mutex
	entries map[string]*entry
	health  map[string]*health
	disc    map[string]*discovery
	corr    map[string]float64
	banner  bool // provider_auth banner raised

	// effort is shared by every Chat Completions client, so what one call
	// learned survives the client being rebuilt for another provider's
	// config change (Forget drops only the changed provider's entries).
	effort *openai.EffortMemory

	// bannerMu serializes updateBanner so a raise and a clear computed
	// concurrently cannot be published in the wrong order.
	bannerMu sync.Mutex
}

var (
	_ core.LLM           = (*Facade)(nil)
	_ core.CheckProvider = (*Facade)(nil)
)

func New(d Deps) (*Facade, error) {
	if d.Config == nil || d.Egress == nil {
		return nil, errors.New("provider: Config and Egress are required")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.Env == nil {
		d.Env = func() []string { return nil }
	}
	f := &Facade{
		d:       d,
		cat:     catalog.Default(),
		clock:   d.Clock,
		log:     d.Log.With("component", "provider"),
		boot:    d.Clock.Now(),
		sleep:   sleepCtx,
		jitter:  rand.Float64,
		entries: map[string]*entry{},
		health:  map[string]*health{},
		disc:    map[string]*discovery{},
		corr:    map[string]float64{},
	}
	f.effort = openai.NewEffortMemory(maxEffortEntries, func(e openai.EffortEntry) {
		f.log.Warn("chat completions rejects tools with reasoning on", "provider", e.Provider, "model", e.Model,
			"reasoning_off_works", e.Mode == openai.EffortOff)
	})
	return f, nil
}

// maxEffortEntries bounds what the Chat Completions clients remember about
// tools and reasoning: one entry per (provider, model) that needed it.
const maxEffortEntries = 256

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
