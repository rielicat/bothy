package doctor

import (
	"context"
	"io"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Runner struct{ checks []core.Check }

func New(checks []core.Check) *Runner { return &Runner{checks: checks} }

// ConfigChecks returns config.errors and config.safe_mode (from Effective()),
// which no module owns because internal/config is skeleton code.
func ConfigChecks(cfg config.Admin) []core.Check { return nil }

// Run executes every check (security-only when security is true), sorted by severity.
func (r *Runner) Run(ctx context.Context, security bool) []core.Finding { return nil }

// Fix applies AutoFix findings and returns the fixed ids.
func (r *Runner) Fix(ctx context.Context, fs []core.Finding) []string { return nil }

// Print renders findings as text (exit code 1 when any >= high) or JSON.
func Print(w io.Writer, fs []core.Finding, asJSON bool) int { return 0 }
