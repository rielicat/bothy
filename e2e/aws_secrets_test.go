//go:build e2e

package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestAWSSecretRefsWithoutCredentials boots the real binary with aws-sm
// references and no AWS credentials at all. Admins see the references (never
// a value or a hint), members do not, the doctor says the credentials do not
// resolve, and a provider test fails in plain words with aws_no_credentials
// before anything reaches the provider. Every use is audited.
func TestAWSSecretRefsWithoutCredentials(t *testing.T) {
	llm := pltLLM(t, mockllm.Rule{Text: []string{"unused"}})
	g := Start(t, Options{
		Config: pltYAML(llm.URL(), nil, `mode: multi
secrets:
  aws: {region: us-west-2}
search: {provider: brave, brave_api_key: "aws-sm:prod/search#brave"}
`),
		// No instance metadata either, whatever host runs the test.
		Env: map[string]string{"AWS_EC2_METADATA_DISABLED": "true"},
	})
	owner := pltOwner(t, g)

	var metas []core.SecretMeta
	if code := owner.JSON(t, http.MethodGet, "/api/vault", nil, &metas); code != http.StatusOK {
		t.Fatalf("GET /api/vault = %d", code)
	}
	i := slices.IndexFunc(metas, func(m core.SecretMeta) bool { return m.Source == "aws-sm" })
	if i < 0 || metas[i].Name != "aws-sm:prod/search#brave" || metas[i].Hint != "" || metas[i].Scope != core.GlobalScope {
		t.Fatalf("vault list = %+v", metas)
	}
	var st core.VaultStatus
	if code := owner.JSON(t, http.MethodGet, "/api/vault/status", nil, &st); code != http.StatusOK || st.External["aws-sm"] != 1 {
		t.Fatalf("vault status = %d %+v", code, st)
	}
	member := owner.CreateUser(t, "mel")
	metas = nil
	if code := member.JSON(t, http.MethodGet, "/api/vault", nil, &metas); code != http.StatusOK {
		t.Fatalf("member GET /api/vault = %d", code)
	}
	if slices.ContainsFunc(metas, func(m core.SecretMeta) bool { return m.Source == "aws-sm" }) {
		t.Fatalf("a member sees company references: %+v", metas)
	}

	var doc dto.DoctorReport
	if code := owner.JSON(t, http.MethodGet, "/api/doctor", nil, &doc); code != http.StatusOK {
		t.Fatalf("doctor = %d", code)
	}
	j := slices.IndexFunc(doc.Findings, func(f core.Finding) bool { return f.ID == "secrets.aws_access" })
	if j < 0 || doc.Findings[j].Severity != core.SevHigh || !strings.Contains(doc.Findings[j].Detail, "search.brave_api_key") {
		t.Fatalf("doctor findings = %+v", doc.Findings)
	}

	var res core.ProbeResult
	req := dto.ProviderTestReq{Kind: core.KindOpenAICompat, BaseURL: llm.URL(), SecretRef: "aws-sm:prod/llm#openai", AllowPrivate: true}
	if code := owner.JSON(t, http.MethodPost, "/api/providers/test", req, &res); code != http.StatusOK {
		t.Fatalf("provider test = %d", code)
	}
	if res.Code != core.FailAWSNoCredentials || !strings.Contains(res.Detail, "could not get AWS credentials") || !strings.Contains(res.Detail, "aws-sm:prod/llm#openai") {
		t.Fatalf("provider test = %+v", res)
	}
	if n := len(llm.History()); n != 0 {
		t.Fatalf("the provider was called %d times without a key", n)
	}
	e := owner.WaitAudit(t, "secret.use", func(e core.AuditEntry) bool { return e.Target == "aws-sm:prod/llm#openai" })
	if e.Outcome != "error" || !strings.Contains(string(e.Detail), `"purpose":"provider:probe"`) || !strings.Contains(string(e.Detail), `"code":"aws_no_credentials"`) {
		t.Fatalf("audit row %+v %s", e, e.Detail)
	}
	if code := member.JSON(t, http.MethodPost, "/api/providers/test", req, nil); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("a member may test a provider with a company reference: %d", code)
	}
}
