package conformance

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// reporter is the part of *testing.T the suites use. The kit's own tests
// implement it to run a suite against a broken implementation and inspect the
// failures, which a real *testing.T cannot do without failing itself.
type reporter interface {
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	TempDir() string
}

type tReporter struct{ t *testing.T }

func (r tReporter) Logf(format string, args ...any)   { r.t.Helper(); r.t.Logf(format, args...) }
func (r tReporter) Errorf(format string, args ...any) { r.t.Helper(); r.t.Errorf(format, args...) }
func (r tReporter) Fatalf(format string, args ...any) { r.t.Helper(); r.t.Fatalf(format, args...) }
func (r tReporter) TempDir() string                   { return r.t.TempDir() }

// check runs f as a named part of a suite and reports whether it passed; a
// Fatalf inside f ends only that part. Suites use it instead of t.Run, which
// panics inside a testing/synctest bubble, where adapter tests often run.
func check(r reporter, name string, f func(reporter)) bool {
	c := &checkReporter{parent: r, prefix: name + ": "}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(c)
	}()
	<-done
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.failed
}

type checkReporter struct {
	parent reporter
	prefix string
	mu     sync.Mutex
	failed bool
}

func (c *checkReporter) Logf(format string, args ...any) {
	c.parent.Logf("%s%s", c.prefix, fmt.Sprintf(format, args...))
}

func (c *checkReporter) Errorf(format string, args ...any) {
	c.mu.Lock()
	c.failed = true
	c.mu.Unlock()
	c.parent.Errorf("%s%s", c.prefix, fmt.Sprintf(format, args...))
}

// Fatalf ends the check's own goroutine, never the test goroutine, so it is
// safe with a real *testing.T.
func (c *checkReporter) Fatalf(format string, args ...any) {
	c.Errorf(format, args...)
	runtime.Goexit()
}

func (c *checkReporter) TempDir() string { return c.parent.TempDir() }
