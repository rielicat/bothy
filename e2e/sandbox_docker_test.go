//go:build e2e && docker

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestDockerSandbox runs the sandbox scenarios against the real gateway and
// sandboxd in containers (sandbox_dockerstack_test.go):
//
//	go test -tags 'e2e docker' -count=1 -run TestDockerSandbox ./e2e/
func TestDockerSandbox(t *testing.T) {
	s := newDockerSuite(t)
	t.Run("E2_ShellApproval", func(t *testing.T) { dockerShellApproval(t, s) })
	t.Run("E3_DenyAndPark", func(t *testing.T) { dockerDenyAndPark(t, s) })
	t.Run("E7_Stop", func(t *testing.T) { dockerStop(t, s) })
	t.Run("E12_Egress", func(t *testing.T) {
		if os.Getenv("BOTHY_E2E_NET") == "0" {
			t.Skip("BOTHY_E2E_NET=0: E12 needs example.com:443")
		}
		dockerEgress(t, s)
	})
}

const askShellYAML = `
policy:
  rules:
    - {id: ask-shell, tool: shell, verdict: ask}
`

// dockerShellApproval is E2: a rule makes shell ask, the card arrives over
// SSE with the exact command, nothing runs before approval, and after it the
// command runs in a real box whose output reaches the model.
func dockerShellApproval(t *testing.T, s *dockerSuite) {
	const cmd = "touch e2-ran; uname -s; id"
	st := s.start(t, "e2", []mockllm.Rule{
		{MatchUser: `E2 box`, MatchToolResult: `(?s)exit 0.*Linux.*uid=10001`, Text: []string{"The box runs Linux as uid 10001."}},
		{MatchUser: `E2 box`, MatchToolResult: `.`, Text: []string{"UNEXPECTED tool result"}},
		{MatchUser: `E2 box`, ToolCalls: []core.ToolCall{shellCall(cmd)}},
		{Text: []string{"UNEXPECTED request"}},
	}, askShellYAML)
	tr := track(t, st.Gateway)
	sess := newSession(t, st.Gateway)
	sendText(t, st.Gateway, sess, "E2 box check")

	a := tr.awaitApproval(t, sess, 60*time.Second)
	if a.Action == nil || a.Action.Tool != "shell" || !slices.Equal(a.Action.Argv, []string{"bash", "-lc", cmd}) ||
		a.Decision.Rule != "rule:ask-shell" || !strings.Contains(a.Card.Code, cmd) || !slices.Contains(a.Decision.Choices, core.AllowOnce) {
		t.Fatalf("approval = %+v\ncard %+v", a, a.Card)
	}
	if _, ok := workspaceFile(t, st.Gateway, "e2-ran"); ok {
		t.Fatal("the command ran before approval")
	}
	if got := resolveApproval(t, st.Gateway, a.ID, core.AllowOnce); got.Status != core.ApprovalAllowed {
		t.Fatalf("approve -> %+v", got)
	}
	fin, text := tr.awaitFinished(t, sess, 3*time.Minute)
	if fin.Status != core.RunDone || text != "The box runs Linux as uid 10001." {
		t.Fatalf("run = %+v text %q", fin, text)
	}
	res := tr.toolResults(t, tr.lastRun(sess))
	if len(res) != 1 || res[0].Status != core.TCOK || !strings.Contains(res[0].Preview, "uid=10001") {
		t.Fatalf("tool results = %+v", res)
	}
	if _, ok := workspaceFile(t, st.Gateway, "e2-ran"); !ok {
		t.Fatal("a file the box wrote is not visible in the workspace")
	}
	box := st.sessionBox(t, sess)
	if box == "" {
		t.Fatal("no box container for the session")
	}
	var spec struct {
		Config     struct{ User string }
		HostConfig struct {
			NetworkMode    string
			ReadonlyRootfs bool
			CapDrop        []string
		}
	}
	if err := json.Unmarshal([]byte(dockerT(t, "inspect", "--format", "{{json .}}", box)), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Config.User != "10001:10001" || spec.HostConfig.NetworkMode != "none" || !spec.HostConfig.ReadonlyRootfs ||
		!slices.Contains(spec.HostConfig.CapDrop, "ALL") {
		t.Fatalf("box %s is not the hardened box: %+v", box, spec)
	}
	if !strings.Contains(string(st.LLM.Requests()[0]), "- Sandbox: available") {
		t.Error("the facts block does not say the sandbox is available")
	}
	if !slices.Contains(requestTools(t, st.LLM.Requests()[0]), "shell") {
		t.Error("shell is not offered with a sandbox")
	}
}

// dockerDenyAndPark is E3 with a real box: an interactive deny never runs
// the command; a scheduled run parks, is approved in the web inbox, resumes
// and runs the command in the box exactly once.
func dockerDenyAndPark(t *testing.T, s *dockerSuite) {
	st := s.start(t, "e3", []mockllm.Rule{
		{MatchUser: `E3 interactive`, MatchToolResult: `denied`, Text: []string{"Not running it."}},
		{MatchUser: `E3 interactive`, MatchToolResult: `.`, Text: []string{"UNEXPECTED tool result"}},
		{MatchUser: `E3 interactive`, ToolCalls: []core.ToolCall{shellCall("echo ran >> denied.txt")}},
		{MatchUser: `E3 scheduled`, ToolCalls: []core.ToolCall{shellCall("echo ran >> parked.txt")}},
		{MatchUser: `(?s)<approved-action tool="shell"[^>]*status="ok"`, Text: []string{"Scheduled shell done."}},
		{Text: []string{"UNEXPECTED request"}},
	}, askShellYAML+`  approval:
    scheduled_timeout: 1s
`)
	g := st.Gateway
	tr := track(t, g)

	sess := newSession(t, g)
	sendText(t, g, sess, "E3 interactive job")
	a := tr.awaitApproval(t, sess, 60*time.Second)
	if a.Action == nil || a.Action.Tool != "shell" {
		t.Fatalf("approval = %+v", a)
	}
	resolveApproval(t, g, a.ID, core.Deny)
	if fin, text := tr.awaitFinished(t, sess, time.Minute); fin.Status != core.RunDone || text != "Not running it." {
		t.Fatalf("denied run = %+v text %q", fin, text)
	}
	if body, ok := workspaceFile(t, g, "denied.txt"); ok {
		t.Fatalf("a denied shell command ran: %q", body)
	}

	var sc core.Schedule
	up := dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "e3", Kind: core.SchedCron, Spec: "0 0 1 1 *", TZ: "UTC",
		Prompt: "E3 scheduled job", Slot: core.SlotMain, Enabled: true, MissedRun: "skip"}}
	if code := g.JSON(t, http.MethodPost, "/api/schedules", up, &sc); code != http.StatusOK {
		t.Fatalf("create schedule = %d", code)
	}
	var fired struct {
		Run core.RunID `json:"run_id"`
	}
	if code := g.JSON(t, http.MethodPost, "/api/schedules/"+string(sc.ID)+"/run", nil, &fired); code != http.StatusAccepted || fired.Run == "" {
		t.Fatalf("run schedule = %d %+v", code, fired)
	}
	parked := tr.await(t, "run.parked", time.Minute, func(e sev) bool { return e.Type == string(core.EvRunParked) && e.Run == fired.Run })
	pid := decodeEv[core.RunParkedData](t, parked).Approval
	if _, ok := workspaceFile(t, g, "parked.txt"); ok {
		t.Fatal("the parked command ran before approval")
	}
	resolveApproval(t, g, pid, core.AllowOnce)
	fin := tr.await(t, "resumed run.finished", 3*time.Minute, func(e sev) bool { return e.Type == string(core.EvRunFinished) && e.Run == fired.Run })
	if f := decodeEv[core.RunFinishedData](t, fin); f.Status != core.RunDone || tr.text(fired.Run) != "Scheduled shell done." {
		t.Fatalf("resumed run = %+v text %q", f, tr.text(fired.Run))
	}
	time.Sleep(time.Second)
	if body, ok := workspaceFile(t, g, "parked.txt"); !ok || body != "ran\n" {
		t.Fatalf("parked.txt = %q (exists %v), want exactly one run of the command", body, ok)
	}
	var run dto.RunDetail
	if code := g.JSON(t, http.MethodGet, "/api/runs/"+string(fired.Run), nil, &run); code != http.StatusOK {
		t.Fatalf("run detail = %d", code)
	}
	n := 0
	for _, tc := range run.ToolCalls {
		if tc.Tool == "shell" && tc.Status == core.TCOK {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d successful shell calls in the resumed run: %+v", n, run.ToolCalls)
	}
}

// dockerStop is E7: /stop during `sleep 600` (plus a setsid'd sleep that
// escaped the exec's process group) and during a hung LLM stream; both
// stop in under a second and leave no process in the box.
func dockerStop(t *testing.T, s *dockerSuite) {
	st := s.start(t, "e7", []mockllm.Rule{
		{MatchUser: `E7 sleep`, ToolCalls: []core.ToolCall{shellCall("setsid sleep 600 & sleep 600")}, Times: 1},
		{MatchUser: `E7 hang`, Text: []string{"never ", "arrives"}, Delay: 10 * time.Minute, Times: 1},
		{Text: []string{"Ready."}},
	}, "")
	g := st.Gateway
	tr := track(t, g)
	sess := newSession(t, g)

	sendText(t, g, sess, "E7 sleep")
	call := tr.await(t, "run.tool_call", time.Minute, of(core.EvToolCall, sess))
	var box string
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if box == "" {
			box = st.sessionBox(t, sess)
		}
		if box != "" {
			procs := st.boxProcs(t, box)
			if n := countMatching(procs, "sleep 600"); n >= 2 {
				t.Logf("box %s before stop: %v", box[:12], procs)
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sleeps never started in the box (box %q)", box)
		}
		time.Sleep(100 * time.Millisecond)
	}

	t0 := stopRun(t, g, tr, sess, call.Run)
	for {
		procs := st.boxProcs(t, box)
		if len(procs) == 0 {
			t.Logf("box empty %s after /stop", time.Since(t0))
			break
		}
		if time.Since(t0) >= time.Second {
			t.Fatalf("box processes left 1s after /stop: %v", procs)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The run goroutine finalizes the killed call shortly after the stop.
	for deadline := time.Now().Add(10 * time.Second); ; {
		var rd dto.RunDetail
		if code := g.JSON(t, http.MethodGet, "/api/runs/"+string(call.Run), nil, &rd); code != http.StatusOK {
			t.Fatalf("run detail = %d", code)
		}
		if rd.Run.Status == core.RunStopped && len(rd.ToolCalls) == 1 && rd.ToolCalls[0].Status == core.TCStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stopped run %s: status %s, tool calls %+v", call.Run, rd.Run.Status, rd.ToolCalls)
		}
		time.Sleep(200 * time.Millisecond)
	}

	sendText(t, g, sess, "E7 hang")
	started := tr.await(t, "run.started", time.Minute, of(core.EvRunStarted, sess))
	tr.await(t, "run.status thinking", time.Minute, func(e sev) bool {
		return e.Type == string(core.EvRunStatus) && e.Run == started.Run && strings.Contains(string(e.Data), "thinking")
	})
	time.Sleep(300 * time.Millisecond)
	stopRun(t, g, tr, sess, started.Run)
	if b := st.sessionBox(t, sess); b != "" {
		if procs := st.boxProcs(t, b); len(procs) != 0 {
			t.Fatalf("box processes after the second stop: %v", procs)
		}
	}

	sendText(t, g, sess, "are you there")
	if fin, text := tr.awaitFinished(t, sess, time.Minute); fin.Status != core.RunDone || text != "Ready." {
		t.Fatalf("run after the stops = %+v text %q", fin, text)
	}
}

func countMatching(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// curlCmd prints the proxy's CONNECT answer and the HTTP status; a refused
// CONNECT shows as connect=403 code=000.
const curlCmd = "curl -sS -o /dev/null --max-time 90 -w 'connect=%{http_connect} code=%{http_code}\\n' https://example.com/"

// dockerEgress is E12: a blocked domain is held with a card, "allow for
// session" lets the held request through and the retry needs no card; a
// read-class principal domain grant does not open the proxy while the
// external_send grant the web UI's "Allow this site" creates does; and an
// Exposed session gets an immediate 403 with every grant suspended.
func dockerEgress(t *testing.T, s *dockerSuite) {
	fetchRule := func(user, okText string) []mockllm.Rule {
		return []mockllm.Rule{
			{MatchUser: user, MatchToolResult: `connect=200 code=200`, Text: []string{okText}},
			{MatchUser: user, MatchToolResult: `connect=403`, Text: []string{"refused"}},
			{MatchUser: user, MatchToolResult: `.`, Text: []string{"UNEXPECTED tool result"}},
			{MatchUser: user, ToolCalls: []core.ToolCall{shellCall(curlCmd)}},
		}
	}
	var rules []mockllm.Rule
	rules = append(rules, fetchRule(`E12 first`, "first fetch ok")...)
	rules = append(rules, fetchRule(`E12 retry`, "retry ok")...)
	rules = append(rules, fetchRule(`E12 read grant`, "read grant opened the proxy")...)
	rules = append(rules, fetchRule(`E12 site grant`, "site grant ok")...)
	rules = append(rules, fetchRule(`E12 exposed`, "exposed session got through")...)
	rules = append(rules,
		mockllm.Rule{MatchUser: `E12 web`, MatchToolResult: `.`, Text: []string{"fetched"}},
		mockllm.Rule{MatchUser: `E12 web`, ToolCalls: []core.ToolCall{toolCall("web_fetch", map[string]any{"url": "https://example.com/"})}},
		mockllm.Rule{Text: []string{"UNEXPECTED request"}},
	)
	st := s.start(t, "e12", rules, "")
	g := st.Gateway
	tr := track(t, g)
	var me dto.Me
	if code := g.JSON(t, http.MethodGet, "/api/auth/me", nil, &me); code != http.StatusOK {
		t.Fatalf("me = %d", code)
	}
	principal := core.UserPrincipal(me.User.ID)

	// Hold card, allow for session, retry without a card.
	a := newSession(t, g)
	sendText(t, g, a, "E12 first fetch")
	card := tr.awaitApproval(t, a, 2*time.Minute)
	if card.Action == nil || card.Action.Kind != core.ActNet || card.Action.Host != "example.com" || card.Action.Port != 443 ||
		!strings.HasSuffix(card.ToolCallID, "#net") || !slices.Contains(card.Decision.Choices, core.AllowSession) ||
		card.Card.Title != "Sandbox wants to reach example.com:443" {
		t.Fatalf("hold card = %+v\naction %+v", card, card.Action)
	}
	resolveApproval(t, g, card.ID, core.AllowSession)
	if fin, text := tr.awaitFinished(t, a, 2*time.Minute); fin.Status != core.RunDone || text != "first fetch ok" {
		t.Fatalf("held fetch = %+v text %q", fin, text)
	}
	cards := tr.count(core.EvApprovalRequested, a)
	sendText(t, g, a, "E12 retry fetch")
	if fin, text := tr.awaitFinished(t, a, 2*time.Minute); fin.Status != core.RunDone || text != "retry ok" {
		t.Fatalf("retry = %+v text %q", fin, text)
	}
	if n := tr.count(core.EvApprovalRequested, a); n != cards {
		t.Fatalf("the retry raised %d more cards", n-cards)
	}

	// A read-class principal grant (what the web UI's "Allow this site" used
	// to create) is not honoured: the connection is held again.
	b := newSession(t, g)
	grant := func(maxSE string) {
		t.Helper()
		body := map[string]any{"principal_id": principal, "kind": "domain", "value": "example.com:443", "max_se": maxSE}
		if code := g.JSON(t, http.MethodPost, "/api/grants", body, nil); code != http.StatusOK {
			t.Fatalf("POST /api/grants max_se %s = %d", maxSE, code)
		}
	}
	grant("read")
	sendText(t, g, b, "E12 read grant fetch")
	held := tr.awaitApproval(t, b, 2*time.Minute)
	if held.Action == nil || held.Action.Kind != core.ActNet {
		t.Fatalf("expected a hold card despite the read grant, got %+v", held)
	}
	resolveApproval(t, g, held.ID, core.Deny)
	if fin, text := tr.awaitFinished(t, b, 2*time.Minute); fin.Status != core.RunDone || text != "refused" {
		t.Fatalf("denied hold = %+v text %q", fin, text)
	}
	// egress_log rows are written in batches (every second).
	for deadline := time.Now().Add(15 * time.Second); ; {
		var log dto.List[core.EgressRecord]
		if code := g.JSON(t, http.MethodGet, "/api/egress?session="+string(b)+"&limit=50", nil, &log); code != http.StatusOK {
			t.Fatalf("GET /api/egress = %d", code)
		}
		if slices.ContainsFunc(log.Items, func(r core.EgressRecord) bool {
			return r.Host == "example.com" && r.Port == 443 && r.Decision == "held_deny" && r.Principal == principal
		}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no held_deny row with a principal (the Settings \"Recently blocked\" list): %+v", log.Items)
		}
		time.Sleep(250 * time.Millisecond)
	}

	// The grant "Allow this site" creates now: same shape, external_send.
	grant("external_send")
	cards = tr.count(core.EvApprovalRequested, b)
	sendText(t, g, b, "E12 site grant fetch")
	if fin, text := tr.awaitFinished(t, b, 2*time.Minute); fin.Status != core.RunDone || text != "site grant ok" {
		t.Fatalf("site grant fetch = %+v text %q", fin, text)
	}
	if n := tr.count(core.EvApprovalRequested, b); n != cards {
		t.Fatalf("the site grant still raised %d cards", n-cards)
	}

	// Exposed: private (non-empty MEMORY.md in the snapshot) and untrusted
	// (web_fetch output). Grants are suspended and nothing is held.
	var doc dto.MemoryDoc
	if code := g.JSON(t, http.MethodGet, "/api/memory/MEMORY", nil, &doc); code != http.StatusOK {
		t.Fatalf("GET memory = %d", code)
	}
	put := g.Do(t, http.MethodPut, "/api/memory/MEMORY", dto.MemoryPutReq{Content: "- the user keeps a private diary\n"},
		http.Header{"If-Match": {doc.ETag}})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT memory = %s %s", put.Status, put.Body)
	}
	c := newSession(t, g)
	sendText(t, g, c, "E12 web fetch")
	if fin, text := tr.awaitFinished(t, c, 2*time.Minute); fin.Status != core.RunDone || text != "fetched" {
		t.Fatalf("web_fetch run = %+v text %q", fin, text)
	}
	var detail dto.SessionDetail
	if code := g.JSON(t, http.MethodGet, "/api/sessions/"+string(c), nil, &detail); code != http.StatusOK || !detail.Session.Taint.Exposed() {
		t.Fatalf("session %s taint = %v (code %d), want Exposed", c, detail.Session.Taint, code)
	}
	t0 := time.Now()
	sendText(t, g, c, "E12 exposed fetch")
	fin, text := tr.awaitFinished(t, c, 2*time.Minute)
	if fin.Status != core.RunDone || text != "refused" {
		t.Fatalf("exposed fetch = %+v text %q", fin, text)
	}
	if took := time.Since(t0); took > 20*time.Second {
		t.Fatalf("the exposed session's refusal took %s, want immediate", took)
	}
	if res := tr.toolResults(t, tr.lastRun(c)); len(res) != 1 || !strings.Contains(res[0].Preview, "connect=403") || res[0].Ms > 5000 {
		t.Fatalf("exposed curl = %+v, want an immediate 403 from the proxy", res)
	}
	if n := tr.count(core.EvApprovalRequested, c); n != 0 {
		t.Fatalf("an Exposed session raised %d hold cards", n)
	}
	blocked := slices.IndexFunc(tr.seen, func(e sev) bool {
		return e.Type == string(core.EvEgressBlocked) && e.Session == c && strings.Contains(string(e.Data), "suspended")
	})
	if blocked < 0 {
		t.Fatalf("no egress.blocked event naming the suspended grants:\n%s", tr.dump())
	}
}
