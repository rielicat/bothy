package auth

import (
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// Failures spaced more than a quiet hour apart are forgiven for every
// username alike; if known accounts kept counting, they alone would lock and
// so reveal that they exist.
func TestSpacedFailuresNeverLockKnownOrUnknownNames(t *testing.T) {
	e := newEnv(t)
	e.owner(t)
	for _, name := range []string{"owner", "zz-nobody"} {
		for i := range 7 {
			if i > 0 {
				e.clock.Advance(61 * time.Minute)
			}
			if _, _, err := e.s.Login(ctx, name, "wrong password!", "203.0.113.9"); !isUnauthenticated(err) {
				t.Fatalf("%s attempt %d: %v", name, i, err)
			}
		}
		// Straight after the seventh spaced failure: no lock for either.
		if _, _, err := e.s.Login(ctx, name, "wrong password!", "203.0.113.9"); !isUnauthenticated(err) {
			t.Fatalf("%s after spaced failures: %v", name, err)
		}
	}
	if _, _, err := e.s.Login(ctx, "owner", goodPassword, "203.0.113.10"); err != nil {
		t.Fatalf("login after spaced failures: %v", err)
	}
}

// A lock on a real account still survives a restart.
func TestAccountLockSurvivesRestart(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	e.owner(t)
	for i := range 5 {
		if _, _, err := e.s.Login(ctx, "owner", "wrong password!", "203.0.113."+string(rune('1'+i))); !isUnauthenticated(err) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	s2, err := New(Deps{Store: e.store, Config: config.Static(e.cfg), Audit: e.audit, Bus: e.bus, Redactor: fakes.NewRedactor(),
		DataDir: e.dir, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.Login(ctx, "owner", goodPassword, "203.0.113.50"); retryAfter(err) == 0 {
		t.Fatalf("lock lost on restart: %v", err)
	}
	e.clock.Advance(61 * time.Second)
	if _, a, err := s2.Login(ctx, "owner", goodPassword, "203.0.113.50"); err != nil || a.Role != core.RoleOwner {
		t.Fatalf("login after the lock = %+v, %v", a, err)
	}
}
