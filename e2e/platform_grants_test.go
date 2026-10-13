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

// TestAllowAlways is E20: approving a card with allow_always creates a
// principal grant under which the identical action in a new session runs
// without a card; revoking the grant brings the card back; irreversible,
// Exposed and scheduled-run cards never offer allow_always.
func TestAllowAlways(t *testing.T) {
	const writeArgs = `{"path":"notes/e20.txt","content":"hello from e20\n"}`
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `E20-`, MatchToolResult: `.`, Text: []string{"Done."}},
		mockllm.Rule{MatchUser: `E20-WRITE`, ToolCalls: []core.ToolCall{pltCall("call_w", "fs_write", writeArgs)}},
		mockllm.Rule{MatchUser: `E20-DELETE`, ToolCalls: []core.ToolCall{pltCall("call_d", "use_tool", `{"name":"fs_delete","args":{"path":"notes/e20.txt"}}`)}},
		mockllm.Rule{MatchUser: `E20-READ`, ToolCalls: []core.ToolCall{pltCall("call_r", "fs_read", `{"path":"notes/e20.txt"}`)}},
		mockllm.Rule{MatchUser: `E20-LOOKUP`, ToolCalls: []core.ToolCall{pltCall("call_l", "use_tool", `{"name":"mcp__e20__lookup","args":{"query":"x"}}`)}},
	)
	mcp := pltNewMCP(t, pltLookupTool("Look up a term.", "LOOKUP-RESULT: from the internet"))
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, pltMCPYAML("e20", mcp, "")+`
policy:
  rules:
    - {id: e20-write, tool: fs_write, verdict: ask}
`)})
	owner := pltOwner(t, g)
	events := pltRecord(owner.Events(t))
	pltMCPStatus(t, owner, "e20", func(st core.MCPStatus) bool { return st.Pending == 1 })
	pltApproveMCP(t, owner, "e20", core.SERead)

	grants := func(t *testing.T) []core.Grant {
		t.Helper()
		var gs []core.Grant
		if code := owner.JSON(t, http.MethodGet, "/api/grants?scope=principal", nil, &gs); code != http.StatusOK {
			t.Fatalf("grants = %d", code)
		}
		return gs
	}
	// ask sends text in a fresh session (or sid) and returns its run and card.
	ask := func(t *testing.T, sid core.SessionID, text string) (core.RunID, *core.Approval) {
		t.Helper()
		if sid == "" {
			sid = owner.NewSession(t).ID
		}
		run := owner.Send(t, sid, text)
		return run, owner.WaitApproval(t, run, core.ApprovalPending)
	}
	choices := func(a *core.Approval) []core.Choice { return a.Card.Choices }

	var grant core.Grant
	t.Run("allow_always creates a principal grant", func(t *testing.T) {
		run, a := ask(t, "", "E20-WRITE save a note")
		if a.Decision.Rule != "rule:e20-write" || !slices.Equal(choices(a), []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny}) {
			t.Fatalf("card = %+v / %+v, want the rule ask offering allow_always", a.Decision, a.Card.Choices)
		}
		ev := events.WaitFor(t, core.EvApprovalRequested, 5*time.Second, func(e pltEvent) bool { return e.Run == run })
		var data core.ApprovalData
		if err := json.Unmarshal(ev.Data, &data); err != nil || data.Approval.ID != a.ID || !slices.Contains(data.Approval.Card.Choices, core.AllowAlways) {
			t.Fatalf("approval.requested = %s", ev.Data)
		}
		if got := owner.Resolve(t, a.ID, core.AllowAlways); got.Status != core.ApprovalAllowed || got.Choice != core.AllowAlways {
			t.Fatalf("resolved = %+v", got)
		}
		if d := owner.Finished(t, run); d.Run.Status != core.RunDone || len(d.ToolCalls) != 1 || d.ToolCalls[0].Status != core.TCOK {
			t.Fatalf("run after allow_always = %+v %+v", d.Run, d.ToolCalls)
		}
		gs := grants(t)
		if len(gs) != 1 {
			t.Fatalf("principal grants = %+v, want exactly one", gs)
		}
		grant = gs[0]
		if grant.Scope != core.GrantPrincipal || grant.ScopeID != "" || grant.Principal != core.UserPrincipal(owner.ID()) ||
			grant.Kind != core.GrantTool || grant.Tool != "fs_write" || len(grant.Pattern) == 0 || grant.CreatedBy != owner.ID() {
			t.Fatalf("grant = %+v, want a narrow principal fs_write grant", grant)
		}
		for ptr, glob := range grant.Pattern {
			if strings.Trim(glob, "*") == "" {
				t.Fatalf("grant pattern %s=%q matches everything", ptr, glob)
			}
		}
		owner.WaitAudit(t, "grant.create", func(e core.AuditEntry) bool {
			return strings.Contains(string(e.Detail), `"card"`) && strings.Contains(e.Target+string(e.Detail), string(grant.ID))
		})
		if body := owner.Do(t, http.MethodGet, "/api/workspace/files?path=notes/e20.txt", nil, nil); body.StatusCode != http.StatusOK ||
			string(body.Body) != "hello from e20\n" {
			t.Fatalf("written file = %s %q", body.Status, body.Body)
		}
	})
	if grant.ID == "" {
		t.FailNow()
	}

	t.Run("identical action in a new session runs without a card", func(t *testing.T) {
		run := owner.Send(t, owner.NewSession(t).ID, "E20-WRITE save it again")
		d := owner.Finished(t, run)
		if d.Run.Status != core.RunDone || len(d.ToolCalls) != 1 || len(d.Approvals) != 0 {
			t.Fatalf("run = %+v tool calls %+v approvals %+v, want it done without a card", d.Run, d.ToolCalls, d.Approvals)
		}
		tc := d.ToolCalls[0]
		if tc.Verdict != core.VerdictAuto || tc.Rule != "grant:"+string(grant.ID) || tc.Status != core.TCOK {
			t.Fatalf("tool call = %+v, want auto under %s", tc, grant.ID)
		}
		for _, f := range events.Frames() {
			var ev pltEvent
			if f.Event == string(core.EvApprovalRequested) && json.Unmarshal(f.Data, &ev) == nil && ev.Run == run {
				t.Fatalf("a card was published for the granted action: %s", f.Data)
			}
		}
		if gs := grants(t); len(gs) != 1 || gs[0].Uses < 1 {
			t.Fatalf("grants after use = %+v, want the use counted", gs)
		}
	})

	t.Run("revoking the grant restores the card", func(t *testing.T) {
		if code := owner.JSON(t, http.MethodDelete, "/api/grants/"+string(grant.ID), nil, nil); code != http.StatusNoContent {
			t.Fatalf("revoke = %d", code)
		}
		if gs := grants(t); len(gs) != 0 {
			t.Fatalf("grants after revoke = %+v", gs)
		}
		run, a := ask(t, "", "E20-WRITE once more")
		if a.Decision.Rule != "rule:e20-write" || !slices.Contains(choices(a), core.AllowAlways) {
			t.Fatalf("card after revoke = %+v %+v", a.Decision, a.Card.Choices)
		}
		owner.Resolve(t, a.ID, core.AllowOnce)
		if d := owner.Finished(t, run); d.Run.Status != core.RunDone {
			t.Fatalf("run = %+v", d.Run)
		}
		if gs := grants(t); len(gs) != 0 {
			t.Fatalf("allow_once created grants %+v", gs)
		}
	})

	refuse := func(t *testing.T, a *core.Approval) {
		t.Helper()
		resp := owner.Do(t, http.MethodPost, "/api/approvals/"+string(a.ID), dto.ApprovalResolveReq{Choice: core.AllowAlways}, nil)
		if resp.StatusCode/100 == 2 {
			t.Fatalf("allow_always on a card that does not offer it = %s %s", resp.Status, resp.Body)
		}
		owner.Resolve(t, a.ID, core.Deny)
		if gs := grants(t); len(gs) != 0 {
			t.Fatalf("grants = %+v after a refused allow_always", gs)
		}
	}

	t.Run("irreversible card has no allow_always", func(t *testing.T) {
		run, a := ask(t, "", "E20-DELETE remove the note")
		if a.Action == nil || a.Action.Tool != "fs_delete" || a.Card.Risk != core.SEIrreversible {
			t.Fatalf("card = %+v action %+v", a.Card, a.Action)
		}
		if c := choices(a); slices.Contains(c, core.AllowAlways) || !slices.Contains(c, core.AllowOnce) || !slices.Contains(c, core.Deny) {
			t.Fatalf("irreversible card choices = %v", c)
		}
		refuse(t, a)
		owner.Finished(t, run)
	})

	t.Run("exposed card has no allow_always", func(t *testing.T) {
		s := owner.NewSession(t)
		for _, text := range []string{"E20-READ the note", "E20-LOOKUP something online"} {
			if d := owner.Finished(t, owner.Send(t, s.ID, text)); d.Run.Status != core.RunDone || len(d.ToolCalls) != 1 || d.ToolCalls[0].Status != core.TCOK {
				t.Fatalf("%s: run = %+v %+v", text, d.Run, d.ToolCalls)
			}
		}
		var sd dto.SessionDetail
		if code := owner.JSON(t, http.MethodGet, "/api/sessions/"+string(s.ID), nil, &sd); code != http.StatusOK || !sd.Session.Taint.Exposed() {
			t.Fatalf("session taint = %d %v, want Exposed after a private read and untrusted MCP output", code, sd.Session.Taint)
		}
		run, a := ask(t, s.ID, "E20-WRITE in the exposed session")
		if c := choices(a); slices.Contains(c, core.AllowAlways) || slices.Contains(c, core.AllowSession) || !slices.Contains(c, core.AllowOnce) {
			t.Fatalf("exposed card choices = %v, want neither allow_session nor allow_always", c)
		}
		refuse(t, a)
		owner.Finished(t, run)
	})

	t.Run("scheduled-run card has no allow_always", func(t *testing.T) {
		sc := dto.ScheduleUpsertReq{Schedule: core.Schedule{Principal: core.UserPrincipal(owner.ID()), Name: "e20 nightly",
			Kind: core.SchedCron, Spec: "0 3 1 1 *", TZ: "UTC", Prompt: "E20-WRITE scheduled note", Slot: core.SlotMain,
			Deliver: &core.ChatRef{Channel: "web"}, MissedRun: "skip", Enabled: true}}
		var created core.Schedule
		if code := owner.JSON(t, http.MethodPost, "/api/schedules", sc, &created); code != http.StatusOK || created.ID == "" {
			t.Fatalf("create schedule = %d", code)
		}
		var fired struct {
			Run core.RunID `json:"run_id"`
		}
		if code := owner.JSON(t, http.MethodPost, "/api/schedules/"+string(created.ID)+"/run", nil, &fired); code != http.StatusAccepted || fired.Run == "" {
			t.Fatalf("run schedule = %d %+v", code, fired)
		}
		a := owner.WaitApproval(t, fired.Run, core.ApprovalPending)
		if a.Decision.Rule != "rule:e20-write" || a.OnTimeout != core.TimeoutPark {
			t.Fatalf("scheduled card = %+v on_timeout %s", a.Decision, a.OnTimeout)
		}
		if c := choices(a); slices.Contains(c, core.AllowAlways) || !slices.Contains(c, core.AllowOnce) {
			t.Fatalf("scheduled card choices = %v", c)
		}
		refuse(t, a)
		if d := owner.Finished(t, fired.Run); d.Run.Trigger != core.TrigSchedule {
			t.Fatalf("scheduled run = %+v", d.Run)
		}
	})
}
