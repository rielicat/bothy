package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
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
		body, status = `{"name":"@linear/mcp","version":"1.4.2","bin":{"linear-admin":"./dist/admin.js","mcp":"./dist/index.js"},"dist":{"integrity":"sha512-abc=="}}`, http.StatusOK
	case r.URL.EscapedPath() == "/@linear%2Fmcp/1.4.0":
		body, status = `{"name":"@linear/mcp","version":"1.4.0","dist":{"integrity":"sha512-old=="}}`, http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestNPMCommand(t *testing.T) {
	for _, tc := range []struct {
		bin  string
		want []string
	}{
		{`"cli.js"`, []string{"node", "node_modules/@acme/tool/cli.js"}},
		{`{"zz":"./b.js","aa":"a.js"}`, []string{"node", "node_modules/@acme/tool/a.js"}},
		{`{"tool":"bin/t.js","aa":"a.js"}`, []string{"node", "node_modules/@acme/tool/bin/t.js"}},
		{`{"x":"../../etc/passwd"}`, nil},
		{`{"x":"/usr/bin/env"}`, nil},
		{``, nil},
	} {
		if got := npmCommand("@acme/tool", json.RawMessage(tc.bin)); !slices.Equal(got, tc.want) {
			t.Errorf("bin %s = %v, want %v", tc.bin, got, tc.want)
		}
	}
}

// An admin names a package; Bothy pins the exact version and hash npm reports.
func TestMCPResolvePinsVersionAndIntegrity(t *testing.T) {
	h := newHarness(t).setup()
	reg := &npmRegistry{}
	h.egress.Client = &http.Client{Transport: reg}
	got := decodeAs[dto.MCPResolveResp](t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "@linear/mcp"}, h.admin))
	// The server's command runs the package's own program, which the add
	// dialog has no field for.
	if got.Name != "@linear/mcp" || got.Version != "1.4.2" || got.Integrity != "sha512-abc==" ||
		!slices.Equal(got.Command, []string{"node", "node_modules/@linear/mcp/dist/index.js"}) {
		t.Fatalf("latest = %+v", got)
	}
	got = decodeAs[dto.MCPResolveResp](t, h.do("POST", "/api/mcp/resolve", dto.MCPResolveReq{Name: "@linear/mcp", Version: "1.4.0"}, h.admin))
	if got.Version != "1.4.0" || got.Integrity != "sha512-old==" || got.Command != nil {
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
