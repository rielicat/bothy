//go:build e2e && budgets

package e2e

import (
	"os"
	"sort"
	"testing"
	"time"
)

// SPEC §11.3 footprint budgets. scripts/budgets/footprint.sh runs these.

func TestColdStart(t *testing.T) {
	const budget = 500 * time.Millisecond
	var boots []time.Duration
	for range 5 {
		g := Start(t, Options{})
		boots = append(boots, g.Boot)
		if err := g.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(boots, func(i, j int) bool { return boots[i] < boots[j] })
	median := boots[len(boots)/2]
	t.Logf("cold start to /healthz: median %s (all %v), budget %s", median, boots, budget)
	if median > budget {
		t.Errorf("cold start %s exceeds %s", median, budget)
	}
}

func TestIdleRSS(t *testing.T) {
	const budget = 60 << 20
	wait := 10 * time.Minute
	if v := os.Getenv("BOTHY_RSS_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("BOTHY_RSS_WAIT: %v", err)
		}
		wait = d
	}
	// Same memory limit as deploy/compose.yaml.
	g := Start(t, Options{Env: map[string]string{"GOMEMLIMIT": "48MiB"}})
	var peak int64
	for end := time.Now().Add(wait); ; {
		rss, err := g.RSS()
		if err != nil {
			t.Fatal(err)
		}
		peak = max(peak, rss)
		if !time.Now().Before(end) {
			break
		}
		time.Sleep(min(10*time.Second, time.Until(end)))
	}
	t.Logf("idle RSS after %s: peak %.1f MiB, budget %d MiB", wait, float64(peak)/(1<<20), budget>>20)
	if peak > budget {
		t.Errorf("idle RSS %.1f MiB exceeds %d MiB", float64(peak)/(1<<20), budget>>20)
	}
}
