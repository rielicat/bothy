//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestDailyCapRefusesRuns is E8 (budget): a run that spends past
// budgets.daily_usd is allowed to finish, and every later submission is
// refused before a run row exists, with a notice naming the reset time and
// no model call.
func TestDailyCapRefusesRuns(t *testing.T) {
	// $1 per 1M tokens: the pre-call projection of a few thousand prompt
	// tokens fits the $0.01 cap; the reported 20K input tokens ($0.02) do not.
	llm := pltLLM(t, mockllm.Rule{MatchUser: `E8-SPEND`, Text: []string{"Spent."}, Usage: &core.Usage{Input: 20000, Output: 10}})
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, `
pricing:
  "mock/mock-model": {in: 1, out: 1}
budgets:
  daily_usd: 0.01
`)})
	owner := pltOwner(t, g)
	s := owner.NewSession(t)

	first := owner.Finished(t, owner.Send(t, s.ID, "E8-SPEND please"))
	if first.Run.Status != core.RunDone || first.Run.Cost < core.MicrosFromUSD(0.01) {
		t.Fatalf("first run = %+v, want done with cost >= $0.01", first.Run)
	}
	var today dto.UsageToday
	if code := owner.JSON(t, http.MethodGet, "/api/usage/today", nil, &today); code != http.StatusOK || today.GlobalSpent < today.GlobalLimit {
		t.Fatalf("usage today = %d %+v, want global spend at or over the limit", code, today)
	}
	calls := len(llm.Requests())

	for _, sid := range []core.SessionID{s.ID, owner.NewSession(t).ID} {
		resp := owner.Do(t, http.MethodPost, "/api/sessions/"+string(sid)+"/messages", dto.SendReq{Text: "E8-SPEND again"}, nil)
		var e dto.Error
		if resp.StatusCode != http.StatusTooManyRequests || json.Unmarshal(resp.Body, &e) != nil || e.Error.Code != "budget_exceeded" {
			t.Fatalf("send over the cap = %s %s, want 429 budget_exceeded", resp.Status, resp.Body)
		}
		var runs dto.List[core.Run]
		if code := owner.JSON(t, http.MethodGet, "/api/runs?session="+string(sid), nil, &runs); code != http.StatusOK {
			t.Fatalf("runs = %d", code)
		}
		want := 0
		if sid == s.ID {
			want = 1
		}
		if len(runs.Items) != want {
			t.Fatalf("session %s has %d runs after the refusal, want %d (no run row for a refused submission)", sid, len(runs.Items), want)
		}
		notices := owner.Notices(t, sid)
		if len(notices) == 0 || !strings.Contains(notices[len(notices)-1], "spending cap is reached") ||
			!strings.Contains(notices[len(notices)-1], "start again at") {
			t.Fatalf("session %s notices = %q, want the daily-cap notice with the reset time", sid, notices)
		}
	}

	// The API is refused the same way, as an OpenAI-shaped 429.
	api := owner.Token(t, "", "chat")
	resp := api.Do(t, http.MethodPost, "/v1/chat/completions", map[string]any{
		"model": "bothy", "messages": []map[string]string{{"role": "user", "content": "E8-SPEND via api"}}}, nil)
	var oe struct {
		Error struct{ Type, Code string } `json:"error"`
	}
	if resp.StatusCode != http.StatusTooManyRequests || json.Unmarshal(resp.Body, &oe) != nil ||
		oe.Error.Type != "insufficient_quota" || oe.Error.Code != "budget_exceeded" {
		t.Fatalf("/v1 over the cap = %s %s", resp.Status, resp.Body)
	}
	if n := len(llm.Requests()); n != calls {
		t.Fatalf("mockllm saw %d requests after the cap was reached, want none", n-calls)
	}
}

// TestLoopDetected is E8 (loop): a model that repeats the same tool call
// with the same result is stopped with loop_detected and a Retry notice,
// long before the 25-step budget.
func TestLoopDetected(t *testing.T) {
	llm := pltLLM(t, mockllm.Rule{MatchUser: `E8-LOOP`, ToolCalls: []core.ToolCall{pltCall("call_1", "fs_list", `{"path":"."}`)}})
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, "")})
	owner := pltOwner(t, g)
	events := pltRecord(owner.Events(t))
	s := owner.NewSession(t)

	run := owner.Send(t, s.ID, "E8-LOOP list the files")
	d := owner.Finished(t, run)
	if d.Run.Status != core.RunLoopDetected {
		t.Fatalf("run = %+v, want loop_detected\nlogs:\n%s", d.Run, g.Logs())
	}
	if n := len(d.ToolCalls); n < 3 || n > 5 || d.Run.Steps >= 25 {
		t.Fatalf("run made %d tool calls in %d steps, want it stopped after 3 to 5 identical calls", n, d.Run.Steps)
	}
	for _, tc := range d.ToolCalls {
		if tc.Tool != "fs_list" {
			t.Fatalf("tool call %+v, want fs_list", tc)
		}
	}
	fin := events.WaitFor(t, core.EvRunFinished, 5*time.Second, func(e pltEvent) bool { return e.Run == run })
	var data core.RunFinishedData
	if err := json.Unmarshal(fin.Data, &data); err != nil || data.Status != core.RunLoopDetected {
		t.Fatalf("run.finished = %s, want status loop_detected", fin.Data)
	}
	notices := owner.Notices(t, s.ID)
	if len(notices) == 0 || !strings.Contains(strings.ToLower(strings.Join(notices, "\n")), "retry") {
		t.Fatalf("notices = %q, want a loop notice with a Retry hint", notices)
	}
	// The third identical call's result already carried the warning.
	warned := false
	for _, body := range llm.Requests() {
		if strings.Contains(string(body), "You repeated an identical call 3 times") {
			warned = true
		}
	}
	if !warned && len(d.ToolCalls) > 3 {
		t.Fatalf("no loop warning reached the model before the abort")
	}
}
