//go:build e2e && live

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// TestLiveBedrockGateway runs the real binary against Amazon Bedrock with
// this machine's AWS credentials and completes a chat with a tool call over
// the HTTP API, once per model. CI never builds it (live tag). Run it with
//
//	BOTHY_LIVE_AWS_PROFILE=<profile> BOTHY_LIVE_AWS_REGION=us-west-2 \
//	  go test -tags e2e,live -run LiveBedrock -v ./e2e/
//
// It makes two or three small model calls per model. Nova's second step ends
// in a tool result, where Bedrock refuses a cachePoint for Nova.
func TestLiveBedrockGateway(t *testing.T) {
	region := os.Getenv("BOTHY_LIVE_AWS_REGION")
	if region == "" {
		t.Skip("set BOTHY_LIVE_AWS_REGION (and BOTHY_LIVE_AWS_PROFILE) to run the live Bedrock test")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	profile := os.Getenv("BOTHY_LIVE_AWS_PROFILE")
	for _, model := range []string{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us.amazon.nova-lite-v1:0"} {
		t.Run(model, func(t *testing.T) {
			cfg := fmt.Sprintf("providers:\n  - {id: bedrock, type: bedrock, region: %q, profile: %q}\nmodels:\n  main: [bedrock/%s]\n  fast: [bedrock/%s]\n",
				region, profile, model, model)
			// HOME is the developer's so the SDK finds ~/.aws and its SSO cache.
			g := Start(t, Options{Config: cfg, Env: map[string]string{"HOME": home}})
			u := pltOwner(t, g)
			s := u.NewSession(t)
			run := u.Send(t, s.ID, "Use the fs_list tool on the path \".\" and then tell me in one short sentence how many entries it returned.")
			d := u.WaitRun(t, run, 2*time.Minute, func(r *core.Run) bool { return r.Status.Terminal() })
			if d.Run.Status != core.RunDone {
				t.Fatalf("run %s: %s\n%s", d.Run.Status, d.Run.Error, g.Logs())
			}
			var called, answered bool
			for _, m := range u.Messages(t, s.ID) {
				for _, p := range m.Parts {
					switch {
					case p.Type == core.PartToolCall && p.ToolCall.Name == "fs_list":
						called = true
						t.Logf("tool call %s %s", p.ToolCall.Name, p.ToolCall.Args)
					case p.Type == core.PartText && m.Role == core.MsgAssistant && strings.TrimSpace(p.Text) != "":
						answered = true
						t.Logf("answer %q", p.Text)
						if strings.Contains(p.Text, "<thinking>") {
							t.Errorf("Nova's thinking preamble reached the reply: %q", p.Text)
						}
					case p.Type == core.PartThinking:
						t.Logf("thinking %q", p.Text)
					}
				}
			}
			if !called || !answered || d.Run.Steps < 2 {
				t.Fatalf("called %v answered %v steps %d", called, answered, d.Run.Steps)
			}
			t.Logf("steps %d, usage %+v, cost %d µ$", d.Run.Steps, d.Run.Usage, d.Run.Cost)
			for _, c := range d.LLMCalls {
				t.Logf("llm call %s %s attempts %d error %q", c.Ref, c.Slot, len(c.Attempts), c.Error)
			}
			logs := g.Logs()
			for _, leak := range []string{"aws_secret_access_key", "X-Amz-Security-Token", "Credential=", "Signature="} {
				if strings.Contains(logs, leak) {
					t.Errorf("gateway logs contain %q", leak)
				}
			}
		})
	}
}
