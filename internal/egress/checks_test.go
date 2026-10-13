package egress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

func runChecks(t *testing.T, e *testEnv) map[string]core.Finding {
	t.Helper()
	out := map[string]core.Finding{}
	for _, c := range e.s.Checks() {
		for _, f := range c.Run(context.Background()) {
			if f.ID != c.ID() {
				t.Fatalf("check %s produced finding %s", c.ID(), f.ID)
			}
			if !f.Security || f.AutoFix || f.Fix == "" || f.Detail == "" {
				t.Fatalf("finding = %+v", f)
			}
			if err := c.Fix(context.Background(), f); !errors.Is(err, core.ErrNotImplemented) {
				t.Fatalf("Fix = %v", err)
			}
			out[f.ID] = f
		}
	}
	return out
}

func TestChecksCleanByDefault(t *testing.T) {
	e := newEnv(t, nil)
	ids := map[string]bool{}
	for _, c := range e.s.Checks() {
		ids[c.ID()] = true
	}
	for _, id := range []string{"egress.wildcards", "egress.shared_tenant_allowed", "egress.presets_enabled", "mcp.http_plaintext"} {
		if !ids[id] {
			t.Errorf("missing check %s", id)
		}
	}
	if f := runChecks(t, e); len(f) != 0 {
		t.Fatalf("default config has findings: %+v", f)
	}
}

func TestChecksFindings(t *testing.T) {
	yes, no := true, false
	e := newEnv(t, func(c *config.Config) {
		c.Egress.Allow = []string{"*.example.org", "assets.s3.amazonaws.com", "*.io", "ok.example"}
		c.Egress.Presets.PythonPackages = true
		c.MCP.Servers = []config.MCPServer{
			{Name: "public", Transport: "http", URL: "http://mcp.example.com/mcp"},
			{Name: "lan", Transport: "http", URL: "http://192.168.1.5:9000/mcp"},
			{Name: "compose", Transport: "http", URL: "http://docs-mcp:8080/mcp"},
			{Name: "tls", Transport: "http", URL: "https://mcp.example.com/mcp", Enabled: &yes},
			{Name: "off", Transport: "http", URL: "http://off.example.com/mcp", Enabled: &no},
			{Name: "box", Transport: "stdio", Egress: []string{"api.box.example:443", "x.workers.dev:443"}},
		}
	})
	e.addGrant(core.Grant{Value: "*.corp.example:443", Scope: core.GrantPrincipal, ScopeID: ""})
	e.addGrant(core.Grant{Value: "me.github.io:443", Scope: core.GrantPrincipal, ScopeID: ""})

	f := runChecks(t, e)
	w := f["egress.wildcards"]
	if w.Severity != core.SevMedium || !strings.Contains(w.Detail, "*.example.org") || !strings.Contains(w.Detail, "*.corp.example:443") ||
		strings.Contains(w.Detail, "ok.example") {
		t.Errorf("wildcards = %+v", w)
	}
	s := f["egress.shared_tenant_allowed"]
	if s.Severity != core.SevMedium || !strings.Contains(s.Detail, "assets.s3.amazonaws.com") || !strings.Contains(s.Detail, "*.io") ||
		!strings.Contains(s.Detail, "me.github.io") || !strings.Contains(s.Detail, "mcp.servers.box.egress x.workers.dev:443") ||
		strings.Contains(s.Detail, "example.org") || strings.Contains(s.Detail, "api.box.example") {
		t.Errorf("shared tenant = %+v", s)
	}
	if p := f["egress.presets_enabled"]; p.Severity != core.SevLow || !strings.Contains(p.Detail, "pypi.org") {
		t.Errorf("presets = %+v", p)
	}
	m := f["mcp.http_plaintext"]
	if m.Severity != core.SevHigh || m.Area != "mcp" || !strings.Contains(m.Detail, "public") ||
		strings.Contains(m.Detail, "lan") || strings.Contains(m.Detail, "compose") || strings.Contains(m.Detail, "off") ||
		strings.Contains(m.Detail, "tls") {
		t.Errorf("mcp = %+v", m)
	}
}
