package auth

import (
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func exposure(t *testing.T, env string, mut func(*config.Server)) []core.Finding {
	t.Helper()
	e := newEnv(t, func(c *config.Config) { mut(&c.Server) })
	e.s.getenv = func(k string) string {
		if k == "BOTHY_DEPLOYMENT" {
			return env
		}
		return ""
	}
	return e.s.checkExposure(ctx)
}

// Inside a container the gateway must listen on 0.0.0.0 and cannot see the
// address Docker publishes the port on, so the stock loopback-only compose
// setup must not fail security-audit with a HIGH finding it cannot clear;
// a host install listening on 0.0.0.0 without TLS stays HIGH.
func TestContainerExposureIsNotHighOnTheRecommendedSetup(t *testing.T) {
	stock := func(s *config.Server) { s.Listen = "0.0.0.0:8080" }
	fs := exposure(t, "container", stock)
	if len(fs) != 1 || fs[0].Severity != core.SevMedium || strings.HasPrefix(fs[0].Fix, "Publish only on 127.0.0.1") ||
		!strings.Contains(fs[0].Detail, "cannot see") {
		t.Fatalf("container finding = %+v", fs)
	}
	if fs := exposure(t, "", stock); len(fs) != 1 || fs[0].Severity != core.SevHigh {
		t.Fatalf("host finding = %+v", fs)
	}
	if fs := exposure(t, "container", func(s *config.Server) {
		s.Listen = "0.0.0.0:8080"
		s.PublicURL = "https://bothy.example.com"
		s.TrustedProxies = []string{"172.16.0.0/12"}
	}); len(fs) != 0 {
		t.Fatalf("container behind an https proxy = %+v", fs)
	}
}
