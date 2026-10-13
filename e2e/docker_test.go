//go:build e2e && docker

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestComposeShell brings up deploy/compose.yaml for real (gateway and
// sandboxd containers, the Docker socket, named volumes with subpath mounts)
// and drives first run over HTTP from the host: setup token from the logs ->
// owner -> mockllm reached from the container -> a chat whose model calls
// the shell tool, which runs in a real box and returns its output.
func TestComposeShell(t *testing.T) {
	const model = "mock-model"
	// The last line goes through sbx-init's relay and the box's egress socket
	// on the run volume to the gateway's proxy, which refuses plain HTTP.
	const probe = "echo bothy-box uid=$(id -u) gid=$(id -g) pid1=$(cat /proc/1/comm); uname -m; " +
		"touch /workspace/proof && stat -c 'proof %u:%g %a' /workspace/proof; " +
		"ls /var/run/docker.sock 2>&1; getent hosts example.com || echo no-dns; " +
		`python3 -c 'import urllib.request as r, urllib.error as e
try: r.urlopen("http://example.com/", timeout=20)
except e.HTTPError as x: print("egress", x.code, x.read().decode().strip())'`
	c := StartCompose(t, ComposeOptions{
		MockLLM: []mockllm.Rule{
			{MatchSystem: `Write a title`, Text: []string{"Box check"}},
			{MatchToolResult: `bothy-box uid=`, Text: []string{"The box ", "answered."}},
			{MatchUser: `(?i)box check`, ToolCalls: []core.ToolCall{{ID: "call_box_1", Name: "shell",
				Args: mustJSON(t, map[string]string{"command": probe})}}},
			{Text: []string{"OK"}},
		},
		Models: []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: model}, Context: 65536, MaxOutput: 2048, Tools: true}},
	})
	t.Logf("compose %s: both containers healthy %s after up", c.Prefix, c.Healthy)
	c.FirstRun(t, model, 65536)
	g := c.HTTP

	var info dto.SandboxInfo
	sb := &info.Status
	deadline := time.Now().Add(60 * time.Second)
	for {
		if code := g.JSON(t, http.MethodGet, "/api/sandbox", nil, &info); code != http.StatusOK {
			t.Fatalf("GET /api/sandbox = %d", code)
		}
		if sb.Available {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandbox never became available: %+v", sb)
		}
		time.Sleep(time.Second)
	}
	t.Logf("sandbox: backend %s runtime %s engine %q rootless %v", sb.Backend, sb.Runtime, sb.Engine, sb.Rootless)
	if sb.Backend != "sandboxd" {
		t.Fatalf("sandbox backend %q, want sandboxd", sb.Backend)
	}

	events := g.Events(t, "", nil)
	events.WaitFor(t, "hello", 5*time.Second)
	var sess core.Session
	if code := g.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &sess); code/100 != 2 {
		t.Fatalf("create session = %d", code)
	}
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Text: "please run the box check"}, nil); code/100 != 2 {
		t.Fatalf("send = %d", code)
	}
	run := composeCollectRun(t, events, sess.ID, 3*time.Minute)
	if run.finished.Status != core.RunDone {
		t.Fatalf("run.finished = %+v (tool results %+v)", run.finished, run.results)
	}
	if run.text != "The box answered." {
		t.Fatalf("final text %q", run.text)
	}
	if len(run.calls) != 1 || run.calls[0].Tool != "shell" || run.calls[0].Verdict != core.VerdictAuto {
		t.Fatalf("tool calls = %+v", run.calls)
	}
	if len(run.results) != 1 || run.results[0].Status != core.TCOK {
		t.Fatalf("tool results = %+v", run.results)
	}

	// The model saw what the box printed: the box uid, sbx-init as PID 1, a
	// workspace file in the shared group, no Docker socket, no resolver, and
	// the proxy's refusal.
	result := lastToolResult(t, c.LLM.Requests())
	t.Logf("shell result as the model saw it:\n%s", result)
	for _, want := range []string{"bothy-box uid=10001 gid=10001 pid1=sbx-init", "proof 10001:10001 664",
		"No such file or directory", "no-dns", "egress 403 bothy: egress to example.com:80 denied (plain HTTP is not allowed)"} {
		if !strings.Contains(result, want) {
			t.Errorf("tool result lacks %q", want)
		}
	}
	if !strings.Contains(result, "aarch64") && !strings.Contains(result, "x86_64") {
		t.Errorf("tool result lacks the machine type: %s", result)
	}
	if n := len(c.Boxes()); n != 1 {
		t.Errorf("%d box containers running, want 1", n)
	}
	for _, svc := range []string{"bothy", "sandboxd"} {
		if rss, err := c.RSS(svc); err != nil {
			t.Errorf("%s RSS: %v", svc, err)
		} else {
			t.Logf("%s container RSS after the run: %.1f MiB", svc, float64(rss)/(1<<20))
		}
	}

	var cons struct {
		Content string `json:"content"`
	}
	if code := g.JSON(t, http.MethodGet, "/api/constitution", nil, &cons); code != http.StatusOK {
		t.Fatalf("GET /api/constitution = %d", code)
	}
	o := MeasureTokenOverhead(t, c.LLM.Requests(), cons.Content)
	t.Logf("fixed token overhead with the sandbox: S1 %d + tools %d = %d estimated tokens (budget 2500; tools budget 1500); whole system prompt %d; tools %v",
		o.S1, o.Tools, o.Fixed(), o.System, o.ToolNames)
	if o.Fixed() > 2500 || o.Tools > 1500 {
		t.Errorf("fixed token overhead over budget: S1 %d, tools %d", o.S1, o.Tools)
	}
}

// TestComposeSocketOnboarding gives sandboxd the wrong group for the Docker
// socket (the stock DOCKER_GID default is wrong on Docker Desktop, where the
// socket is root:root inside containers): exec stays unavailable and the
// gateway shows the exact fix line from SPEC §2.10.
func TestComposeSocketOnboarding(t *testing.T) {
	c := StartCompose(t, ComposeOptions{DockerGID: "4242"})
	if c.SocketGID == "4242" {
		t.Skip("the socket's group happens to be 4242")
	}
	c.SetupOwner(t)
	var info dto.SandboxInfo
	want := "Set DOCKER_GID=" + c.SocketGID + " in deploy/.env and run: docker compose up -d"
	deadline := time.Now().Add(60 * time.Second)
	for {
		if code := c.HTTP.JSON(t, http.MethodGet, "/api/sandbox", nil, &info); code != http.StatusOK {
			t.Fatalf("GET /api/sandbox = %d", code)
		}
		if info.Status.Available {
			t.Skip("the Docker socket is accessible without its group")
		}
		if strings.Contains(info.Status.Fix, "DOCKER_GID") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no socket fix line: %+v", info.Status)
		}
		time.Sleep(time.Second)
	}
	t.Logf("sandbox unavailable: %s; fix: %s", info.Status.Reason, info.Status.Fix)
	if info.Status.Fix != want || !strings.Contains(info.Status.Reason, "cannot access /var/run/docker.sock") {
		t.Fatalf("status %+v, want fix %q", info.Status, want)
	}
	if info.Compose == "" {
		t.Error("no compose hint while the sandbox is unavailable")
	}
}

// TestComposeIdleRSS samples the gateway container's RSS (the Linux figure
// of the real image under the compose memory settings) while it idles after
// first run and one chat. It runs only with BOTHY_RSS_WAIT set (10m for the
// SPEC §11.3 wait); the channel fakes cannot be reached from a container, so
// TestIdleRSS remains the budget with channels connected.
func TestComposeIdleRSS(t *testing.T) {
	v := os.Getenv("BOTHY_RSS_WAIT")
	if v == "" {
		t.Skip("set BOTHY_RSS_WAIT (e.g. 10m) to sample the container's idle RSS")
	}
	wait, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("BOTHY_RSS_WAIT: %v", err)
	}
	const model = "mock-model"
	c := StartCompose(t, ComposeOptions{
		MockLLM: []mockllm.Rule{{Text: []string{"OK"}}},
		Models:  []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: model}, Context: 65536, MaxOutput: 2048, Tools: true}},
	})
	c.FirstRun(t, model, 65536)
	events := c.HTTP.Events(t, "", nil)
	events.WaitFor(t, "hello", 5*time.Second)
	var sess core.Session
	if code := c.HTTP.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &sess); code/100 != 2 {
		t.Fatalf("create session = %d", code)
	}
	if code := c.HTTP.JSON(t, http.MethodPost, "/api/sessions/"+string(sess.ID)+"/messages", dto.SendReq{Text: "hello"}, nil); code/100 != 2 {
		t.Fatalf("send = %d", code)
	}
	if run := composeCollectRun(t, events, sess.ID, time.Minute); run.finished.Status != core.RunDone {
		t.Fatalf("run.finished = %+v", run.finished)
	}
	events.Close()
	var first, last, peak int64
	for end := time.Now().Add(wait); ; {
		rss, err := c.RSS("bothy")
		if err != nil {
			t.Fatal(err)
		}
		if first == 0 {
			first = rss
		}
		last, peak = rss, max(peak, rss)
		if !time.Now().Before(end) {
			break
		}
		time.Sleep(min(10*time.Second, time.Until(end)))
	}
	mib := func(b int64) float64 { return float64(b) / (1 << 20) }
	t.Logf("gateway container RSS: %.1f MiB after a chat, %.1f MiB after %s idle, peak %.1f MiB (budget 60)", mib(first), mib(last), wait, mib(peak))
	if peak > 60<<20 {
		t.Errorf("container RSS %.1f MiB exceeds 60 MiB", mib(peak))
	}
}

type composeRun struct {
	text     string
	calls    []core.ToolCallData
	results  []core.ToolResultData
	finished *core.RunFinishedData
}

func composeCollectRun(t *testing.T, events *EventStream, sid core.SessionID, timeout time.Duration) composeRun {
	t.Helper()
	var r composeRun
	var text strings.Builder
	deadline := time.Now().Add(timeout)
	for r.finished == nil {
		f, err := events.Next(time.Until(deadline))
		if err != nil {
			t.Fatalf("sse: %v (text so far %q)", err, text.String())
		}
		var ev struct {
			Session core.SessionID  `json:"session_id"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(f.Data, &ev) != nil || ev.Session != sid {
			continue
		}
		var dst any
		switch f.Event {
		case string(core.EvRunDelta):
			var d core.DeltaData
			if err := json.Unmarshal(ev.Data, &d); err != nil {
				t.Fatal(err)
			}
			text.WriteString(d.Text)
		case string(core.EvToolCall):
			r.calls = append(r.calls, core.ToolCallData{})
			dst = &r.calls[len(r.calls)-1]
		case string(core.EvToolResult):
			r.results = append(r.results, core.ToolResultData{})
			dst = &r.results[len(r.results)-1]
		case string(core.EvRunFinished):
			r.finished = new(core.RunFinishedData)
			dst = r.finished
		}
		if dst != nil {
			if err := json.Unmarshal(ev.Data, dst); err != nil {
				t.Fatalf("%s: %v", f.Event, err)
			}
		}
	}
	r.text = text.String()
	return r
}

// lastToolResult is the content of the last tool message mockllm received.
func lastToolResult(t *testing.T, requests [][]byte) string {
	t.Helper()
	out := ""
	for _, body := range requests {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &req) != nil {
			continue
		}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				var s string
				if json.Unmarshal(m.Content, &s) == nil {
					out = s
				}
			}
		}
	}
	if out == "" {
		t.Fatal("mockllm received no tool result")
	}
	return out
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
