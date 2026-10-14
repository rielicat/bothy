package httpapi

import (
	"net/http"
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
