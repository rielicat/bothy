package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type fakeCheck struct {
	id    string
	fs    []core.Finding
	panic bool
	block bool
	fixes []string
}

func (c *fakeCheck) ID() string { return c.id }
func (c *fakeCheck) Run(ctx context.Context) []core.Finding {
	if c.panic {
		panic("boom")
	}
	if c.block {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
	}
	return c.fs
}
func (c *fakeCheck) Fix(_ context.Context, f core.Finding) error {
	if !f.AutoFix {
		return core.ErrNotImplemented
	}
	c.fixes = append(c.fixes, f.ID)
	return nil
}

type fakeConfig struct {
	config.Admin
	eff config.Effective
}

func (f fakeConfig) Effective() config.Effective { return f.eff }

func ids(fs []core.Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.ID
	}
	return out
}

func TestRunSortsAndFilters(t *testing.T) {
	r := New([]core.Check{
		&fakeCheck{id: "a.low", fs: []core.Finding{{ID: "a.low", Severity: core.SevLow, Security: true}}},
		&fakeCheck{id: "b.crit", fs: []core.Finding{{ID: "b.crit", Severity: core.SevCritical, Security: true}}},
		&fakeCheck{id: "c.ok"},
		&fakeCheck{id: "d.high", fs: []core.Finding{{ID: "d.high", Severity: core.SevHigh}, {ID: "d.info", Severity: core.SevInfo}}},
		&fakeCheck{id: "e.med", fs: []core.Finding{{ID: "e.med", Severity: core.SevMedium, Security: true}}},
	})
	got := ids(r.Run(context.Background(), false))
	want := []string{"b.crit", "d.high", "e.med", "a.low", "d.info"}
	if !slices.Equal(got, want) {
		t.Fatalf("Run = %v, want %v", got, want)
	}
	got = ids(r.Run(context.Background(), true))
	if want := []string{"b.crit", "e.med", "a.low"}; !slices.Equal(got, want) {
		t.Fatalf("security Run = %v, want %v", got, want)
	}
}

func TestRunSurvivesPanicsAndTimeouts(t *testing.T) {
	old := checkTimeout
	checkTimeout = 50 * time.Millisecond
	defer func() { checkTimeout = old }()
	r := New([]core.Check{&fakeCheck{id: "x.panics", panic: true}, &fakeCheck{id: "y.hangs", block: true}})
	fs := r.Run(context.Background(), false)
	if len(fs) != 2 || fs[0].Severity != core.SevMedium || !strings.Contains(fs[0].Detail+fs[1].Detail, "panicked") {
		t.Fatalf("findings = %+v", fs)
	}
}

func TestFixRoutesToReporter(t *testing.T) {
	multi := &fakeCheck{id: "tokens", fs: []core.Finding{
		{ID: "tokens.expired_unrevoked", Severity: core.SevLow, AutoFix: true},
		{ID: "tokens.admin_scope", Severity: core.SevLow},
	}}
	exact := &fakeCheck{id: "skills.changed"}
	r := New([]core.Check{multi, exact})
	fs := r.Run(context.Background(), false)
	fs = append(fs, core.Finding{ID: "skills.changed", AutoFix: true}, core.Finding{ID: "unknown.thing", AutoFix: true})
	fixed := r.Fix(context.Background(), fs)
	if !slices.Equal(fixed, []string{"tokens.expired_unrevoked", "skills.changed"}) {
		t.Fatalf("fixed = %v", fixed)
	}
	if !slices.Equal(multi.fixes, []string{"tokens.expired_unrevoked"}) || !slices.Equal(exact.fixes, []string{"skills.changed"}) {
		t.Fatalf("fix calls = %v / %v", multi.fixes, exact.fixes)
	}
}

func TestConfigChecks(t *testing.T) {
	clean := ConfigChecks(fakeConfig{})
	r := New(clean)
	if fs := r.Run(context.Background(), false); len(fs) != 0 {
		t.Fatalf("clean config findings = %+v", fs)
	}
	var errs []config.FieldError
	for i := 0; i < 7; i++ {
		errs = append(errs, config.FieldError{Path: "x", Message: "bothy.yaml:4: models.main[0]: unknown provider"})
	}
	r = New(ConfigChecks(fakeConfig{eff: config.Effective{Errors: errs, SafeMode: true}}))
	fs := r.Run(context.Background(), false)
	if got := ids(fs); !slices.Equal(got, []string{"config.errors", "config.safe_mode"}) {
		t.Fatalf("ids = %v", got)
	}
	if fs[0].Severity != core.SevHigh || !strings.Contains(fs[0].Detail, "and 2 more") {
		t.Fatalf("config.errors = %+v", fs[0])
	}
	for _, c := range ConfigChecks(fakeConfig{}) {
		if err := c.Fix(context.Background(), core.Finding{}); !errors.Is(err, core.ErrNotImplemented) {
			t.Fatalf("%s Fix = %v", c.ID(), err)
		}
	}
}

func TestPrint(t *testing.T) {
	fs := []core.Finding{
		{ID: "a", Title: "minor", Severity: core.SevLow, Detail: "line one\nline two", Fix: "do it", AutoFix: true},
		{ID: "b", Title: "bad", Severity: core.SevHigh},
	}
	var buf bytes.Buffer
	if code := Print(&buf, fs, false); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "[HIGH] b: bad\n[LOW] a: minor\n    line one\n    line two\n    fix: do it (auto-fixable with --fix)\n") ||
		!strings.Contains(out, "2 finding(s): 1 high, 1 low") {
		t.Fatalf("text = %q", out)
	}
	buf.Reset()
	if code := Print(&buf, fs[:1], true); code != 0 {
		t.Fatalf("json exit code = %d", code)
	}
	var rep struct {
		Findings []core.Finding `json:"findings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil || len(rep.Findings) != 1 || rep.Findings[0].ID != "a" {
		t.Fatalf("json = %s, %v", buf.Bytes(), err)
	}
	buf.Reset()
	if code := Print(&buf, nil, true); code != 0 || !strings.Contains(buf.String(), `"findings": []`) {
		t.Fatalf("empty json = %q", buf.String())
	}
	buf.Reset()
	Print(&buf, nil, false)
	if buf.String() != "No findings.\n" {
		t.Fatalf("empty text = %q", buf.String())
	}
}
