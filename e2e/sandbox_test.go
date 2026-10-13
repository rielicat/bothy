//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// Sandbox scenarios that need no Docker. The Docker variants (E2, E3 with a
// box, E7 with a box, E12) are in sandbox_docker_test.go.

// startWithLLM boots a gateway whose only provider is a mockllm started
// here, so the config file can name it before the first boot.
func startWithLLM(t *testing.T, rules []mockllm.Rule, extraYAML string) (*Gateway, *mockllm.Server) {
	t.Helper()
	llm := mockllm.New(rules, sbxModels())
	t.Cleanup(llm.Close)
	g := Start(t, Options{Config: MockProviderYAML(llm.URL(), sbxModel, 32768) + extraYAML})
	setupOwner(t, g, g.SetupToken(t))
	return g, llm
}

// TestNoSandbox is E14: with no sandbox, shell is absent from the tools sent
// to the model, the facts block says so, a forced shell call is an unknown
// tool, and the no_sandbox banner is up.
func TestNoSandbox(t *testing.T) {
	g, llm := startWithLLM(t, []mockllm.Rule{
		{MatchUser: `try shell`, MatchToolResult: `Unknown tool shell`, Text: []string{"No shell here."}},
		{MatchUser: `try shell`, MatchToolResult: `.`, Text: []string{"UNEXPECTED tool result"}},
		{MatchUser: `try shell`, ToolCalls: []core.ToolCall{shellCall("uname -a")}},
		{Text: []string{"OK"}},
	}, "")

	tr := track(t, g)
	var hello struct {
		Sandbox bool              `json:"sandbox"`
		Banners []core.BannerData `json:"banners"`
	}
	if err := json.Unmarshal(tr.hello, &hello); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(hello.Banners, func(b core.BannerData) bool { return b.Code == "no_sandbox" })
	if hello.Sandbox || i < 0 || !strings.Contains(hello.Banners[i].Text, "No sandbox") {
		t.Fatalf("hello = %s, want sandbox false and a no_sandbox banner", tr.hello)
	}
	var me dto.Me
	if code := g.JSON(t, http.MethodGet, "/api/auth/me", nil, &me); code != http.StatusOK || me.Sandbox ||
		!slices.ContainsFunc(me.Banners, func(b core.BannerData) bool { return b.Code == "no_sandbox" }) {
		t.Fatalf("/api/auth/me = %d sandbox %v banners %+v", code, me.Sandbox, me.Banners)
	}
	var info dto.SandboxInfo
	if code := g.JSON(t, http.MethodGet, "/api/sandbox", nil, &info); code != http.StatusOK || info.Status.Available || info.Compose == "" {
		t.Fatalf("/api/sandbox = %d %+v", code, info)
	}

	s := newSession(t, g)
	sendText(t, g, s, "try shell please")
	fin, text := tr.awaitFinished(t, s, 20*time.Second)
	if fin.Status != core.RunDone || text != "No shell here." {
		t.Fatalf("run = %+v text %q", fin, text)
	}
	if res := tr.toolResults(t, tr.lastRun(s)); len(res) != 1 || res[0].Status != core.TCInvalid {
		t.Fatalf("forced shell call results = %+v, want one invalid call", res)
	}

	reqs := llm.Requests()
	if len(reqs) < 2 {
		t.Fatalf("mockllm saw %d requests, want the tool round trip", len(reqs))
	}
	for i, body := range reqs {
		tools := requestTools(t, body)
		if slices.Contains(tools, "shell") || slices.Contains(tools, "python") {
			t.Errorf("request %d offers exec tools without a sandbox: %v", i, tools)
		}
		if !slices.Contains(tools, "fs_read") {
			t.Errorf("request %d has no fs_read; tools %v", i, tools)
		}
		if !strings.Contains(string(body), "- Sandbox: unavailable") {
			t.Errorf("request %d: the facts block does not say the sandbox is unavailable", i)
		}
	}
}

// TestDenyAndPark is E3 without Docker (the box variant is in
// TestDockerSandbox): an interactive deny never runs the tool; a scheduled
// run parks, is approved in the web inbox, resumes and runs the tool exactly
// once.
func TestDenyAndPark(t *testing.T) {
	appendCall := func(path string) core.ToolCall {
		return toolCall("fs_write", map[string]any{"path": path, "content": "ran\n", "mode": "append"})
	}
	g, llm := startWithLLM(t, []mockllm.Rule{
		{MatchUser: `interactive job`, MatchToolResult: `denied`, Text: []string{"Understood, not writing."}},
		{MatchUser: `interactive job`, MatchToolResult: `.`, Text: []string{"UNEXPECTED tool result"}},
		{MatchUser: `interactive job`, ToolCalls: []core.ToolCall{appendCall("denied.txt")}},
		{MatchUser: `scheduled job`, ToolCalls: []core.ToolCall{appendCall("parked.txt")}},
		{MatchUser: `(?s)<approved-action tool="fs_write".*parked\.txt`, Text: []string{"Scheduled write done."}},
		{Text: []string{"UNEXPECTED request"}},
	}, `
policy:
  rules:
    - {id: ask-writes, tool: fs_write, verdict: ask}
  approval:
    scheduled_timeout: 1s
`)
	tr := track(t, g)

	// Interactive deny.
	s := newSession(t, g)
	sendText(t, g, s, "interactive job")
	a := tr.awaitApproval(t, s, 20*time.Second)
	if a.Action == nil || a.Action.Tool != "fs_write" || !slices.Contains(a.Decision.Choices, core.Deny) {
		t.Fatalf("approval = %+v", a)
	}
	if got := resolveApproval(t, g, a.ID, core.Deny); got.Status != core.ApprovalDenied {
		t.Fatalf("deny -> %+v", got)
	}
	fin, text := tr.awaitFinished(t, s, 20*time.Second)
	if fin.Status != core.RunDone || text != "Understood, not writing." {
		t.Fatalf("denied run = %+v text %q", fin, text)
	}
	if body, ok := workspaceFile(t, g, "denied.txt"); ok {
		t.Fatalf("a denied fs_write ran: denied.txt = %q", body)
	}
	if n := tr.count(core.EvApprovalRequested, s); n != 1 {
		t.Fatalf("%d cards for one denied call", n)
	}

	// Scheduled run: the approval parks after scheduled_timeout.
	var sc core.Schedule
	up := dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "e3", Kind: core.SchedCron, Spec: "0 0 1 1 *", TZ: "UTC",
		Prompt: "scheduled job", Slot: core.SlotMain, Enabled: true, MissedRun: "skip"}}
	if code := g.JSON(t, http.MethodPost, "/api/schedules", up, &sc); code != http.StatusOK || sc.ID == "" {
		t.Fatalf("create schedule = %d %+v", code, sc)
	}
	var fired struct {
		Run core.RunID `json:"run_id"`
	}
	if code := g.JSON(t, http.MethodPost, "/api/schedules/"+string(sc.ID)+"/run", nil, &fired); code != http.StatusAccepted || fired.Run == "" {
		t.Fatalf("run schedule = %d %+v", code, fired)
	}
	parked := tr.await(t, "run.parked", 30*time.Second, func(e sev) bool { return e.Type == string(core.EvRunParked) && e.Run == fired.Run })
	pid := decodeEv[core.RunParkedData](t, parked).Approval
	var pa core.Approval
	if code := g.JSON(t, http.MethodGet, "/api/approvals/"+string(pid), nil, &pa); code != http.StatusOK || pa.Status != core.ApprovalParked {
		t.Fatalf("parked approval = %d %+v", code, pa)
	}
	if slices.Contains(pa.Decision.Choices, core.AllowAlways) {
		t.Errorf("a scheduled run's card offers allow_always: %v", pa.Decision.Choices)
	}
	var run dto.RunDetail
	if code := g.JSON(t, http.MethodGet, "/api/runs/"+string(fired.Run), nil, &run); code != http.StatusOK || run.Run.Status != core.RunParked {
		t.Fatalf("parked run = %d %+v", code, run.Run)
	}
	if _, ok := workspaceFile(t, g, "parked.txt"); ok {
		t.Fatal("the parked fs_write ran before approval")
	}

	if got := resolveApproval(t, g, pid, core.AllowOnce); got.Status != core.ApprovalAllowed {
		t.Fatalf("approve parked -> %+v", got)
	}
	resumed := tr.await(t, "resumed run.finished", 30*time.Second, func(e sev) bool { return e.Type == string(core.EvRunFinished) && e.Run == fired.Run })
	if fin := decodeEv[core.RunFinishedData](t, resumed); fin.Status != core.RunDone || tr.text(fired.Run) != "Scheduled write done." {
		t.Fatalf("resumed run = %+v text %q", fin, tr.text(fired.Run))
	}
	// A second approve loses the CAS and must not run the action again.
	if resp := g.Do(t, http.MethodPost, "/api/approvals/"+string(pid), dto.ApprovalResolveReq{Choice: core.AllowOnce}, nil); resp.StatusCode/100 == 2 {
		t.Fatalf("second resolve of a decided approval = %s %s", resp.Status, resp.Body)
	}
	time.Sleep(500 * time.Millisecond)
	if body, ok := workspaceFile(t, g, "parked.txt"); !ok || body != "ran\n" {
		t.Fatalf("parked.txt = %q (exists %v), want exactly one append", body, ok)
	}
	if code := g.JSON(t, http.MethodGet, "/api/runs/"+string(fired.Run), nil, &run); code != http.StatusOK {
		t.Fatalf("run detail = %d", code)
	}
	var execs int
	for _, tc := range run.ToolCalls {
		if tc.Tool == "fs_write" && tc.Status == core.TCOK {
			execs++
		}
	}
	if execs != 1 || run.Run.Status != core.RunDone {
		t.Fatalf("run %s: %d successful fs_write calls, status %s; tool calls %+v", fired.Run, execs, run.Run.Status, run.ToolCalls)
	}
	if n := len(llm.Requests()); n < 4 {
		t.Fatalf("mockllm saw %d requests", n)
	}
}

// TestStopHungStream is the provider half of E7 (the box half, sleep 600, is
// in TestDockerSandbox): /stop during a stream that hangs mid-reply stops the
// run in under a second, and the session takes the next message.
func TestStopHungStream(t *testing.T) {
	g, _ := startWithLLM(t, []mockllm.Rule{
		// Times: the stopped turn stays in history and merges with the next
		// user message, which would match again.
		{MatchUser: `hang forever`, Text: []string{"never ", "arrives"}, Delay: 10 * time.Minute, Times: 1},
		{Text: []string{"Back again."}},
	}, "")
	tr := track(t, g)
	s := newSession(t, g)
	sendText(t, g, s, "hang forever")
	started := tr.await(t, "run.started", 20*time.Second, of(core.EvRunStarted, s))
	tr.await(t, "run.status thinking", 20*time.Second, func(e sev) bool {
		return e.Type == string(core.EvRunStatus) && e.Run == started.Run && strings.Contains(string(e.Data), "thinking")
	})
	time.Sleep(300 * time.Millisecond) // the request is in flight and the stream is open

	stopRun(t, g, tr, s, started.Run)

	sendText(t, g, s, "are you there")
	fin, text := tr.awaitFinished(t, s, 20*time.Second)
	if fin.Status != core.RunDone || text != "Back again." {
		t.Fatalf("run after stop = %+v text %q", fin, text)
	}
}

// stopRun posts /stop and requires the stop response and the run's
// run.finished(stopped) within a second of the request.
func stopRun(t *testing.T, g *Gateway, tr *tracker, s core.SessionID, run core.RunID) time.Time {
	t.Helper()
	t0 := time.Now()
	var sr dto.StopResp
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(s)+"/stop", nil, &sr); code != http.StatusOK || !sr.Stopped {
		t.Fatalf("stop = %d %+v", code, sr)
	}
	took := time.Since(t0)
	fin := tr.await(t, "run.finished after stop", 5*time.Second, func(e sev) bool { return e.Type == string(core.EvRunFinished) && e.Run == run })
	if st := decodeEv[core.RunFinishedData](t, fin).Status; st != core.RunStopped {
		t.Fatalf("run.finished status = %s, want stopped", st)
	}
	finished := fin.At.Sub(t0)
	t.Logf("stop: response after %s, run.finished after %s", took, finished)
	if took >= time.Second || finished >= time.Second {
		t.Fatalf("stop took %s (response) / %s (run.finished), want < 1s", took, finished)
	}
	return t0
}
