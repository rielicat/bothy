//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// Helpers shared by the sandbox scenarios (E2, E3, E7, E12, E14), with and
// without Docker.

const sbxModel = "mock-model"

func sbxModels() []core.ModelInfo {
	return []core.ModelInfo{{Ref: core.ModelRef{Provider: "mock", Model: sbxModel}, Context: 32768, MaxOutput: 2048, Tools: true}}
}

// setupOwner claims the setup token, creates the owner and completes setup.
// The provider comes from the config file, so no PATCH is needed.
func setupOwner(t *testing.T, g *Gateway, token string) dto.Me {
	t.Helper()
	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: token}, &claim); code != http.StatusOK || claim.CSRF == "" {
		t.Fatalf("setup claim = %d %+v", code, claim)
	}
	g.SetCSRF(claim.CSRF)
	var me dto.Me
	owner := dto.SetupOwnerReq{Username: "owner", Password: "Granite-River-42-Lantern", Timezone: "UTC"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK || me.CSRF == "" {
		t.Fatalf("setup owner = %d %+v", code, me)
	}
	g.SetCSRF(me.CSRF)
	if code := g.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("setup complete = %d", code)
	}
	return me
}

func newSession(t *testing.T, g *Gateway) core.SessionID {
	t.Helper()
	var s core.Session
	if code := g.JSON(t, http.MethodPost, "/api/sessions", dto.SessionCreateReq{}, &s); code/100 != 2 || s.ID == "" {
		t.Fatalf("create session = %d %+v", code, s)
	}
	return s.ID
}

func sendText(t *testing.T, g *Gateway, s core.SessionID, text string) {
	t.Helper()
	var res core.SubmitResult
	if code := g.JSON(t, http.MethodPost, "/api/sessions/"+string(s)+"/messages", dto.SendReq{Text: text}, &res); code != http.StatusAccepted {
		t.Fatalf("send %q = %d", text, code)
	}
}

func resolveApproval(t *testing.T, g *Gateway, id core.ApprovalID, choice core.Choice) core.Approval {
	t.Helper()
	var a core.Approval
	if code := g.JSON(t, http.MethodPost, "/api/approvals/"+string(id), dto.ApprovalResolveReq{Choice: choice}, &a); code != http.StatusOK {
		t.Fatalf("resolve %s %s = %d", id, choice, code)
	}
	return a
}

// workspaceFile downloads a file from the owner's workspace; ok is false on 404.
func workspaceFile(t *testing.T, g *Gateway, rel string) (string, bool) {
	t.Helper()
	resp := g.Do(t, http.MethodGet, "/api/workspace/files?path="+rel, nil, nil)
	switch resp.StatusCode {
	case http.StatusOK:
		return string(resp.Body), true
	case http.StatusNotFound:
		return "", false
	}
	t.Fatalf("GET workspace file %s = %s %s", rel, resp.Status, resp.Body)
	return "", false
}

// sev is one decoded SSE event.
type sev struct {
	Type    string
	Session core.SessionID
	Run     core.RunID
	Data    json.RawMessage
	At      time.Time // when the test received it
}

// tracker reads an EventStream and keeps every event, so a test can wait for
// one event and later assert on what else arrived.
type tracker struct {
	es    *EventStream
	seen  []sev
	hello json.RawMessage
}

func track(t *testing.T, g *Gateway) *tracker {
	t.Helper()
	es := g.Events(t, "", nil)
	if es.Status != http.StatusOK {
		t.Fatalf("GET /api/events = %d", es.Status)
	}
	f := es.WaitFor(t, "hello", 10*time.Second)
	return &tracker{es: es, hello: f.Data}
}

func (tr *tracker) next(timeout time.Duration) (sev, error) {
	f, err := tr.es.Next(timeout)
	if err != nil {
		return sev{}, err
	}
	var e struct {
		Session core.SessionID  `json:"session_id"`
		Run     core.RunID      `json:"run_id"`
		Data    json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(f.Data, &e)
	ev := sev{Type: f.Event, Session: e.Session, Run: e.Run, Data: e.Data, At: time.Now()}
	tr.seen = append(tr.seen, ev)
	return ev, nil
}

// await returns the first new event matching match.
func (tr *tracker) await(t *testing.T, what string, timeout time.Duration, match func(sev) bool) sev {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			t.Fatalf("sse: no %s within %s; events seen:\n%s", what, timeout, tr.dump())
		}
		ev, err := tr.next(left)
		if err != nil {
			t.Fatalf("sse: waiting for %s: %v; events seen:\n%s", what, err, tr.dump())
		}
		if match(ev) {
			return ev
		}
	}
}

func (tr *tracker) dump() string {
	var b strings.Builder
	for _, e := range tr.seen {
		d := string(e.Data)
		if len(d) > 300 {
			d = d[:300] + "…"
		}
		fmt.Fprintf(&b, "  %s %s %s %s\n", e.Type, e.Session, e.Run, d)
	}
	return b.String()
}

func of(typ core.EventType, s core.SessionID) func(sev) bool {
	return func(e sev) bool { return e.Type == string(typ) && (s == "" || e.Session == s) }
}

func decodeEv[T any](t *testing.T, e sev) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(e.Data, &v); err != nil {
		t.Fatalf("decode %s: %v\n%s", e.Type, err, e.Data)
	}
	return v
}

// awaitApproval waits for the next approval card of session s.
func (tr *tracker) awaitApproval(t *testing.T, s core.SessionID, timeout time.Duration) *core.Approval {
	t.Helper()
	a := decodeEv[core.ApprovalData](t, tr.await(t, "approval.requested", timeout, of(core.EvApprovalRequested, s))).Approval
	if a == nil {
		t.Fatal("approval.requested without an approval")
	}
	return a
}

// awaitFinished waits for run.finished of session s and returns it with the
// text the run streamed.
func (tr *tracker) awaitFinished(t *testing.T, s core.SessionID, timeout time.Duration) (core.RunFinishedData, string) {
	t.Helper()
	e := tr.await(t, "run.finished", timeout, of(core.EvRunFinished, s))
	return decodeEv[core.RunFinishedData](t, e), tr.text(e.Run)
}

func (tr *tracker) text(run core.RunID) string {
	var b strings.Builder
	for _, e := range tr.seen {
		if e.Type == string(core.EvRunDelta) && e.Run == run {
			var d core.DeltaData
			if json.Unmarshal(e.Data, &d) == nil {
				b.WriteString(d.Text)
			}
		}
	}
	return b.String()
}

// lastRun is the run of the latest event seen for session s.
func (tr *tracker) lastRun(s core.SessionID) core.RunID {
	for i := len(tr.seen) - 1; i >= 0; i-- {
		if e := tr.seen[i]; e.Session == s && e.Run != "" {
			return e.Run
		}
	}
	return ""
}

// count reports how many events of typ for session s arrived so far.
func (tr *tracker) count(typ core.EventType, s core.SessionID) int {
	n := 0
	for _, e := range tr.seen {
		if of(typ, s)(e) {
			n++
		}
	}
	return n
}

// toolResults returns the run.tool_result payloads of run, in order.
func (tr *tracker) toolResults(t *testing.T, run core.RunID) []core.ToolResultData {
	t.Helper()
	var out []core.ToolResultData
	for _, e := range tr.seen {
		if e.Type == string(core.EvToolResult) && e.Run == run {
			out = append(out, decodeEv[core.ToolResultData](t, e))
		}
	}
	return out
}

// requestTools returns the tool names of one mockllm chat request.
func requestTools(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("mockllm request: %v", err)
	}
	var names []string
	for _, tl := range req.Tools {
		names = append(names, tl.Function.Name)
	}
	slices.Sort(names)
	return names
}

func shellCall(cmd string) core.ToolCall {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return core.ToolCall{Name: "shell", Args: b}
}

func toolCall(name string, args map[string]any) core.ToolCall {
	b, _ := json.Marshal(args)
	return core.ToolCall{Name: name, Args: b}
}
