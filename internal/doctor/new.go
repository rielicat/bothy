package doctor

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const parallel = 4

// checkTimeout bounds each check's Run and Fix.
var checkTimeout = 15 * time.Second

type Runner struct {
	checks []core.Check

	mu    sync.Mutex
	owner map[string]core.Check // finding id -> check that last reported it
}

func New(checks []core.Check) *Runner {
	return &Runner{checks: slices.Clone(checks), owner: map[string]core.Check{}}
}

// ConfigChecks returns config.errors and config.safe_mode (from Effective()),
// which no module owns because internal/config is skeleton code.
func ConfigChecks(cfg config.Admin) []core.Check {
	return []core.Check{
		check{id: "config.errors", run: func(context.Context) []core.Finding {
			errs := cfg.Effective().Errors
			if len(errs) == 0 {
				return nil
			}
			lines := make([]string, 0, min(len(errs), 5))
			for _, e := range errs[:min(len(errs), 5)] {
				lines = append(lines, e.Message)
			}
			if len(errs) > 5 {
				lines = append(lines, fmt.Sprintf("… and %d more", len(errs)-5))
			}
			return []core.Finding{{
				ID: "config.errors", Area: "config", Severity: core.SevHigh,
				Title:  fmt.Sprintf("%d config value(s) are invalid and fell back to a lower layer", len(errs)),
				Detail: strings.Join(lines, "\n"),
				Fix:    "bothy config check (Docker: docker compose exec bothy /bothy config check)",
			}}
		}},
		check{id: "config.safe_mode", run: func(context.Context) []core.Finding {
			if !cfg.Effective().SafeMode {
				return nil
			}
			return []core.Finding{{
				ID: "config.safe_mode", Area: "config", Severity: core.SevHigh,
				Title:  "Safe mode: bothy.yaml could not be parsed",
				Detail: "Chat, channels, schedules and MCP are off until bothy.yaml parses and the gateway restarts.",
				Fix:    "bothy config check (Docker: docker compose exec bothy /bothy config check), then restart",
			}}
		}},
	}
}

type check struct {
	id  string
	run func(context.Context) []core.Finding
}

func (c check) ID() string                             { return c.id }
func (c check) Run(ctx context.Context) []core.Finding { return c.run(ctx) }
func (c check) Fix(context.Context, core.Finding) error {
	return core.ErrNotImplemented
}

var severityRank = map[core.Severity]int{core.SevCritical: 4, core.SevHigh: 3, core.SevMedium: 2, core.SevLow: 1, core.SevInfo: 0}

// Run executes every check (security-only when security is true), sorted by
// severity. A check that panics or overruns its timeout is reported as a
// finding instead of failing the whole run.
func (r *Runner) Run(ctx context.Context, security bool) []core.Finding {
	results := make([][]core.Finding, len(r.checks))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, c := range r.checks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runOne(ctx, c)
		}()
	}
	wg.Wait()
	var out []core.Finding
	r.mu.Lock()
	for i, fs := range results {
		for _, f := range fs {
			r.owner[f.ID] = r.checks[i]
			if !security || f.Security {
				out = append(out, f)
			}
		}
	}
	r.mu.Unlock()
	sortFindings(out)
	return out
}

func runOne(ctx context.Context, c core.Check) (fs []core.Finding) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	done := make(chan []core.Finding, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- []core.Finding{failed(c.ID(), fmt.Sprintf("the check panicked: %v", p))}
			}
		}()
		done <- c.Run(ctx)
	}()
	select {
	case fs = <-done:
		return fs
	case <-ctx.Done():
		return []core.Finding{failed(c.ID(), "the check did not finish within "+checkTimeout.String())}
	}
}

func failed(id, detail string) core.Finding {
	area, _, _ := strings.Cut(id, ".")
	return core.Finding{ID: id, Area: area, Severity: core.SevMedium, Title: "Check " + id + " could not run", Detail: detail}
}

func sortFindings(fs []core.Finding) {
	slices.SortStableFunc(fs, func(a, b core.Finding) int {
		return cmp.Or(cmp.Compare(severityRank[b.Severity], severityRank[a.Severity]), cmp.Compare(a.ID, b.ID))
	})
}

// Fix applies AutoFix findings and returns the fixed ids. A finding is routed
// to the check that reported it in the last Run, or to the check whose id
// equals the finding id.
func (r *Runner) Fix(ctx context.Context, fs []core.Finding) []string {
	var fixed []string
	for _, f := range fs {
		if !f.AutoFix || slices.Contains(fixed, f.ID) {
			continue
		}
		c := r.checkFor(f.ID)
		if c == nil {
			continue
		}
		if fixOne(ctx, c, f) == nil {
			fixed = append(fixed, f.ID)
		}
	}
	return fixed
}

// fixOne applies one fix; a panicking fix counts as failed rather than
// taking down `bothy security-audit --fix` or the request serving it.
func fixOne(ctx context.Context, c core.Check, f core.Finding) (err error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("fix %s panicked: %v", f.ID, p)
		}
	}()
	return c.Fix(ctx, f)
}

func (r *Runner) checkFor(id string) core.Check {
	r.mu.Lock()
	c := r.owner[id]
	r.mu.Unlock()
	if c != nil {
		return c
	}
	for _, c := range r.checks {
		if c.ID() == id {
			return c
		}
	}
	return nil
}

// Print renders findings as text (exit code 1 when any >= high) or JSON.
func Print(w io.Writer, fs []core.Finding, asJSON bool) int {
	sorted := slices.Clone(fs)
	sortFindings(sorted)
	code := 0
	for _, f := range sorted {
		if severityRank[f.Severity] >= severityRank[core.SevHigh] {
			code = 1
		}
	}
	if asJSON {
		if sorted == nil {
			sorted = []core.Finding{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Findings []core.Finding `json:"findings"`
		}{sorted})
		return code
	}
	if len(sorted) == 0 {
		fmt.Fprintln(w, "No findings.")
		return code
	}
	counts := map[core.Severity]int{}
	for _, f := range sorted {
		counts[f.Severity]++
		fmt.Fprintf(w, "[%s] %s: %s\n", strings.ToUpper(string(f.Severity)), f.ID, f.Title)
		for _, line := range strings.Split(strings.TrimSpace(f.Detail), "\n") {
			if line != "" {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
		if f.Fix != "" {
			// Only security-audit applies fixes, and every auto-fixable
			// finding is a security one, so the hint names that command
			// even when `bothy doctor` printed it.
			auto := ""
			if f.AutoFix {
				auto = " (auto-fixable: bothy security-audit --fix)"
			}
			fmt.Fprintf(w, "    fix: %s%s\n", f.Fix, auto)
		}
	}
	var parts []string
	for _, s := range []core.Severity{core.SevCritical, core.SevHigh, core.SevMedium, core.SevLow, core.SevInfo} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	fmt.Fprintf(w, "\n%d finding(s): %s\n", len(sorted), strings.Join(parts, ", "))
	return code
}
