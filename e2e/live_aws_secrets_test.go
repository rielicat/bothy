//go:build e2e && live

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestLiveAWSSecretRef runs the real binary with a provider key that is a
// JSON key of an existing AWS Secrets Manager secret, read with this
// machine's AWS credentials through the production chain and egress. It
// checks that the key resolves (the provider receives a non-empty value)
// and that the value appears nowhere else: not in the gateway logs, audit
// rows, run details or messages. The value is never printed. CI never
// builds it (live tag). Run it with
//
//	AWS_PROFILE=<profile> BOTHY_LIVE_AWS_REGION=us-west-2 \
//	  BOTHY_LIVE_AWS_SM_REF='aws-sm:<secret name or ARN>#<json key>' \
//	  go test -tags e2e,live -run LiveAWSSecretRef -v ./e2e/
//
// It skips when the credentials do not resolve or have expired (aws sso login).
func TestLiveAWSSecretRef(t *testing.T) {
	raw := os.Getenv("BOTHY_LIVE_AWS_SM_REF")
	if raw == "" {
		t.Skip("set BOTHY_LIVE_AWS_SM_REF (and AWS_PROFILE, BOTHY_LIVE_AWS_REGION) to run the live aws-sm test")
	}
	ref, err := config.ParseAWSSecretRef(raw)
	if err != nil || ref.Key == "" {
		t.Fatalf("BOTHY_LIVE_AWS_SM_REF must be aws-sm:<secret>#<json key> (%v)", err)
	}
	region := os.Getenv("BOTHY_LIVE_AWS_REGION")
	if region == "" {
		region = "us-west-2"
	}
	profile := os.Getenv("BOTHY_LIVE_AWS_PROFILE")
	if profile == "" {
		profile = os.Getenv("AWS_PROFILE")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	llm := pltLLM(t, mockllm.Rule{MatchUser: `LIVE-AWS-SM`, Text: []string{"Done."}})
	yml := strings.Replace(pltYAML(llm.URL(), nil, fmt.Sprintf("secrets:\n  aws: {region: %q, profile: %q}\n", region, profile)),
		"    allow_private: true\n", fmt.Sprintf("    allow_private: true\n    api_key: %q\n", ref.String()), 1)
	// HOME is the developer's so the SDK finds ~/.aws and its SSO cache.
	g := Start(t, Options{Config: yml, Env: map[string]string{"HOME": home}})
	var value []byte
	t.Cleanup(func() {
		// The harness prints the logs of a failed test; a leaked value must
		// not reach the test output that way.
		if len(value) > 0 {
			g.logs.mu.Lock()
			scrubbed := bytes.ReplaceAll(g.logs.b.Bytes(), value, []byte("[value]"))
			g.logs.b.Reset()
			g.logs.b.Write(scrubbed)
			g.logs.mu.Unlock()
		}
	})
	owner := pltOwner(t, g)

	var doc dto.DoctorReport
	if code := owner.JSON(t, http.MethodGet, "/api/doctor", nil, &doc); code != http.StatusOK {
		t.Fatalf("doctor = %d", code)
	}
	i := slices.IndexFunc(doc.Findings, func(f core.Finding) bool { return f.ID == "secrets.aws_access" })
	if i < 0 {
		t.Fatalf("no secrets.aws_access finding: %+v", doc.Findings)
	}
	if f := doc.Findings[i]; f.Severity != core.SevInfo {
		t.Skipf("AWS credentials unavailable or expired: %s", f.Detail)
	}
	t.Logf("doctor: %s", doc.Findings[i].Title)

	s := owner.NewSession(t)
	run := owner.Send(t, s.ID, "LIVE-AWS-SM say done")
	d := owner.WaitRun(t, run, time.Minute, func(r *core.Run) bool { return r.Status.Terminal() })
	if d.Run.Status != core.RunDone {
		if strings.Contains(d.Run.Error, "could not get AWS credentials") {
			t.Skip("AWS credentials expired during the test")
		}
		t.Fatalf("run %s: %s", d.Run.Status, d.Run.Error)
	}
	reqs := llm.History()
	if len(reqs) == 0 {
		t.Fatal("the provider was never called")
	}
	value = []byte(strings.TrimPrefix(reqs[len(reqs)-1].Header.Get("Authorization"), "Bearer "))
	if len(value) == 0 || string(value) == ref.String() || strings.HasPrefix(string(value), config.AWSSecretPrefix) {
		t.Fatalf("the provider did not receive the resolved value (%d bytes)", len(value))
	}
	t.Logf("the JSON key resolved: the provider received a %d-byte value", len(value))

	audit := owner.Audit(t, "")
	use := slices.IndexFunc(audit, func(e core.AuditEntry) bool {
		return e.Action == "secret.use" && e.Target == ref.String() && e.Outcome == "ok"
	})
	if use < 0 {
		t.Error("no secret.use audit row for the reference")
	}
	auditJSON, _ := json.Marshal(audit)
	runJSON, _ := json.Marshal(d)
	msgsJSON, _ := json.Marshal(owner.Messages(t, s.ID))
	for where, b := range map[string][]byte{"gateway logs": []byte(g.Logs()), "audit rows": auditJSON, "run detail": runJSON, "messages": msgsJSON} {
		if bytes.Contains(b, value) {
			t.Errorf("the secret value appears in the %s", where)
		}
	}
}
