package conformance

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// recorder collects suite failures instead of failing the test, so a suite
// can be run against a broken implementation.
type recorder struct {
	t      *testing.T
	prefix string
	mu     *sync.Mutex
	fails  *[]string
}

func record(t *testing.T, suite func(r reporter)) []string {
	var mu sync.Mutex
	var fails []string
	r := &recorder{t: t, mu: &mu, fails: &fails}
	done := make(chan struct{})
	go func() { defer close(done); suite(r) }()
	<-done
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(fails)
}

func (r *recorder) Logf(string, ...any) {}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	*r.fails = append(*r.fails, r.prefix+fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func (r *recorder) TempDir() string { return r.t.TempDir() }

// wantFailure asserts that some failure contains want.
func wantFailure(t *testing.T, fails []string, want string) {
	t.Helper()
	for _, f := range fails {
		if strings.Contains(f, want) {
			return
		}
	}
	t.Fatalf("no failure mentions %q; got %d:\n%s", want, len(fails), strings.Join(fails, "\n"))
}

func TestCheckScopesFatal(t *testing.T) {
	fails := record(t, func(r reporter) {
		if check(r, "sub", func(r reporter) {
			check(r, "inner", func(r reporter) {
				r.Fatalf("stop %d%%", 100)
				r.Errorf("unreachable")
			})
			r.Errorf("after inner")
		}) {
			r.Errorf("check reported success")
		}
		if !check(r, "clean", func(reporter) {}) {
			r.Errorf("clean check reported failure")
		}
		r.Fatalf("top")
		r.Errorf("unreachable")
	})
	want := []string{"sub: inner: stop 100%", "sub: after inner", "top"}
	if !slices.Equal(fails, want) {
		t.Fatalf("fails = %q, want %q", fails, want)
	}
}
