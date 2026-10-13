package egress

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

type check struct {
	id  string
	run func(ctx context.Context) []core.Finding
}

func (c *check) ID() string                              { return c.id }
func (c *check) Run(ctx context.Context) []core.Finding  { return c.run(ctx) }
func (c *check) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

// principalDomainGrants returns the active principal-scoped domain grants of
// every principal.
func (s *Service) principalDomainGrants(ctx context.Context) []*core.Grant {
	gs, err := s.store.Grants().List(ctx, core.SystemViewer(), core.GrantPrincipal, "")
	if err != nil {
		s.log.Warn("egress: listing grants for doctor failed", "err", err)
		return nil
	}
	return slices.DeleteFunc(gs, func(g *core.Grant) bool { return g.Kind != core.GrantDomain })
}

func (s *Service) checkWildcards(ctx context.Context) []core.Finding {
	var found []string
	for _, e := range s.cfg().Egress.Allow {
		if strings.HasPrefix(e, "*.") {
			found = append(found, "egress.allow "+e)
		}
	}
	for _, g := range s.principalDomainGrants(ctx) {
		if strings.HasPrefix(g.Value, "*.") {
			found = append(found, fmt.Sprintf("grant %s (%s) %s", g.ID, g.Principal, g.Value))
		}
	}
	if len(found) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: "egress.wildcards", Area: "egress", Title: "Wildcard egress entries",
		Detail:   "Tool boxes may reach every subdomain of: " + strings.Join(found, "; ") + ".",
		Fix:      "Replace wildcards with the exact hosts you need (Settings → Network, Settings → Grants).",
		Severity: core.SevMedium, Security: true,
	}}
}

func (s *Service) checkSharedTenant(ctx context.Context) []core.Finding {
	c := s.cfg().Egress
	var found []string
	for _, e := range c.Allow {
		if coversSharedTenant(e, c.SharedTenantSuffixes) {
			found = append(found, "egress.allow "+e)
		}
	}
	for _, m := range s.cfg().MCP.Servers {
		for _, e := range m.Egress {
			if coversSharedTenant(e, c.SharedTenantSuffixes) {
				found = append(found, "mcp.servers."+m.Name+".egress "+e)
			}
		}
	}
	for _, g := range s.principalDomainGrants(ctx) {
		if coversSharedTenant(g.Value, c.SharedTenantSuffixes) {
			found = append(found, fmt.Sprintf("grant %s (%s) %s", g.ID, g.Principal, g.Value))
		}
	}
	if len(found) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: "egress.shared_tenant_allowed", Area: "egress", Title: "Shared-hosting domains allowed",
		Detail:   "Anyone can publish under these domains, so allowing them can open an exfiltration path: " + strings.Join(found, "; ") + ".",
		Fix:      "Allow only the exact tenant host you trust, or remove the entry (Settings → Network, Settings → MCP, Settings → Grants).",
		Severity: core.SevMedium, Security: true,
	}}
}

// coversSharedTenant reports whether an allow entry or grant value reaches a
// host under a shared-tenant suffix, including wildcards broader than one.
func coversSharedTenant(entry string, suffixes []string) bool {
	p, ok := parsePattern(entry)
	if !ok {
		return false
	}
	if !p.wildcard {
		return sharedTenant(p.host, suffixes)
	}
	for _, s := range suffixes {
		s = strings.Trim(strings.ToLower(s), ".")
		if s != "" && (p.suffix == s || strings.HasSuffix(s, "."+p.suffix) || strings.HasSuffix(p.suffix, "."+s)) {
			return true
		}
	}
	return false
}

func (s *Service) checkPresets(context.Context) []core.Finding {
	p := s.cfg().Egress.Presets
	var on []string
	if p.PythonPackages {
		on = append(on, "python_packages ("+strings.Join(presetPython, ", ")+")")
	}
	if p.NPMPackages {
		on = append(on, "npm_packages ("+strings.Join(presetNPM, ", ")+")")
	}
	if len(on) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: "egress.presets_enabled", Area: "egress", Title: "Package registry presets enabled",
		Detail:   "Every tool box can download packages without approval: " + strings.Join(on, "; ") + ". Registries also accept uploads of arbitrary data.",
		Fix:      "Turn the presets off (egress.presets) and approve registries per session when needed.",
		Severity: core.SevLow, Security: true,
	}}
}

func (s *Service) checkMCPPlaintext(context.Context) []core.Finding {
	var found []string
	for _, m := range s.cfg().MCP.Servers {
		if m.Transport != "http" || (m.Enabled != nil && !*m.Enabled) {
			continue
		}
		u, err := url.Parse(m.URL)
		if err != nil || u.Scheme != "http" || privateHost(u.Hostname()) {
			continue
		}
		found = append(found, m.Name+" ("+u.Host+")")
	}
	if len(found) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: "mcp.http_plaintext", Area: "mcp", Title: "MCP servers over plain HTTP",
		Detail:   "Requests, vault-injected headers and tool results cross the network unencrypted: " + strings.Join(found, ", ") + ".",
		Fix:      "Use an https:// URL for these servers (mcp.servers.<name>.url).",
		Severity: core.SevHigh, Security: true,
	}}
}

// privateHost judges a URL host without resolving it: private or loopback
// literals, localhost, single-label names (compose services) and the
// conventional private suffixes.
func privateHost(host string) bool {
	h, err := parseHost(host)
	if err != nil {
		return false
	}
	if h.isIP() {
		return addrProblem(h.ip, false) != "" && addrProblem(h.ip, true) == ""
	}
	if !strings.Contains(h.name, ".") {
		return true
	}
	for _, sfx := range []string{"localhost", "local", "internal", "lan", "home.arpa"} {
		if h.name == sfx || strings.HasSuffix(h.name, "."+sfx) {
			return true
		}
	}
	return false
}
