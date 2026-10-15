package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func TestProviderTestBedrock(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("POST", "/api/providers/test", dto.ProviderTestReq{Kind: core.KindBedrock, Region: "us-west-2",
		Profile: "software_development_automation", Model: "us.anthropic.claude-sonnet-4-6"}, h.owner)
	expect(t, rec, http.StatusOK)
	if res := decodeAs[core.ProbeResult](t, rec); !res.OK {
		t.Fatalf("result %+v", res)
	}
	if len(h.llm.Probes) != 1 {
		t.Fatalf("probes %+v", h.llm.Probes)
	}
	if p := h.llm.Probes[0]; p.Kind != core.KindBedrock || p.Region != "us-west-2" || p.Profile != "software_development_automation" ||
		p.Model != "us.anthropic.claude-sonnet-4-6" || !p.APIKey.IsZero() {
		t.Fatalf("probe config %+v", p)
	}

	for _, bad := range []dto.ProviderTestReq{
		{Kind: core.KindBedrock, Region: "us-west-2.attacker.example"},
		{Kind: core.KindBedrock, Region: "us-west-2", Profile: "x]\n[evil"},
		{Kind: core.KindBedrock, Region: "us-west-2", APIKey: "AKIDEXAMPLE"},
		{Kind: core.KindBedrock, SecretRef: "secret:aws"},
		{Kind: core.KindAnthropic, Region: "us-west-2"},
		{Kind: core.KindOpenAI, Profile: "default"},
	} {
		rec := h.do("POST", "/api/providers/test", bad, h.owner)
		expect(t, rec, http.StatusBadRequest)
	}
	if len(h.llm.Probes) != 1 {
		t.Fatalf("rejected requests reached the probe: %+v", h.llm.Probes[1:])
	}
	// Members cannot probe providers.
	expect(t, h.do("POST", "/api/providers/test", dto.ProviderTestReq{Kind: core.KindBedrock, Region: "us-west-2"}, h.member), http.StatusForbidden)
}

// npmRegistry answers like registry.npmjs.org for one package.
type npmRegistry struct{ paths []string }

func (n *npmRegistry) RoundTrip(r *http.Request) (*http.Response, error) {
	n.paths = append(n.paths, r.URL.EscapedPath())
	body, status := `{"error":"Not found"}`, http.StatusNotFound
	switch {
	case r.URL.Host != "registry.npmjs.org":
	case r.URL.EscapedPath() == "/@linear%2Fmcp/latest":
		body, status = `{"name":"@linear/mcp","version":"1.4.2","dist":{"integrity":"sha512-abc=="}}`, http.StatusOK
	case r.URL.EscapedPath() == "/@linear%2Fmcp/1.4.0":
		body, status = `{"name":"@linear/mcp","version":"1.4.0","dist":{"integrity":"sha512-old=="}}`, http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// An admin names a package; Bothy pins the exact version and hash npm reports.
func TestMCPResolvePinsVersionAndIntegrity(t *testing.T) {
	h := newHarness(t).setup()
	reg := &npmRegistry{}
	h.egress.Client = &http.Client{Transport: reg}
	got := decodeAs[dto.MCPResolveResp](t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "@linear/mcp"}, h.admin))
	if got != (dto.MCPResolveResp{Name: "@linear/mcp", Version: "1.4.2", Integrity: "sha512-abc=="}) {
		t.Fatalf("latest = %+v", got)
	}
	got = decodeAs[dto.MCPResolveResp](t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "@linear/mcp", Version: "1.4.0"}, h.admin))
	if got.Version != "1.4.0" || got.Integrity != "sha512-old==" {
		t.Fatalf("pinned = %+v", got)
	}
	expect(t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "@linear/mcp", Version: "9.9.9"}, h.admin), http.StatusNotFound)
	expect(t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "../etc/passwd"}, h.admin), http.StatusBadRequest)
	expect(t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "@linear/mcp", Version: "^1"}, h.admin), http.StatusBadRequest)
	expect(t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "@linear/mcp"}, h.member), http.StatusForbidden)
	if dests := h.egress.Dests; len(dests) == 0 || dests[0].Hosts[0] != "registry.npmjs.org:443" {
		t.Fatalf("egress = %+v", dests)
	}
}
