//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// refineRules script a model for refine mode. Role calls are recognised by
// the header of their appended message and answer from the quoted draft;
// the question's first answer has no final answer line, one later draft
// says 41 and the rest say 42, so the vote is 42 whichever parallel draft
// gets which rule.
func refineRules() []mockllm.Rule {
	return []mockllm.Rule{
		{MatchUser: `(?s)\[Bothy refine · generator\].*Final answer: 41`, Text: []string{"It is 41.\nFinal answer: 41"}},
		{MatchUser: `(?s)\[Bothy refine · generator\]`, Text: []string{"Six times seven is 42.\nFinal answer: 42"}},
		{MatchUser: `(?s)\[Bothy refine · critic\].*Final answer: 41`, Text: []string{"Verdict: needs changes\nSix times seven is 42, not 41."}},
		{MatchUser: `(?s)\[Bothy refine · critic\]`, Text: []string{"Verdict: correct\nThe product is right."}},
		{MatchUser: `(?s)\[Bothy refine · corrector\]`, Text: []string{"6 × 7 = 42.\nFinal answer: 42"}},
		{MatchUser: `six times seven`, Text: []string{"Six times seven is 42."}, Times: 1},
		{MatchUser: `six times seven`, Text: []string{"It is 41.\nFinal answer: 41"}, Times: 1},
		{MatchUser: `six times seven`, Text: []string{"Final answer: 42"}},
		{MatchUser: `two plus two`, Text: []string{"Two plus two is 4.\nFinal answer: 4"}},
	}
}

// agentRequests counts the chat completions mockllm received, leaving out
// the session-title request.
func agentRequests(llm *mockllm.Server) int {
	n := 0
	for _, b := range llm.Requests() {
		if !bytes.Contains(b, []byte("Write a title of at most six words")) {
			n++
		}
	}
	return n
}

func agentCalls(d *dto.RunDetail) int {
	n := 0
	for _, c := range d.LLMCalls {
		if c.Purpose == "agent" {
			n++
		}
	}
	return n
}

// lastAnswer is the session's last assistant reply and its refine trace.
func lastAnswer(t *testing.T, u *pltUser, sid core.SessionID) (string, *core.RefineTrace) {
	t.Helper()
	msgs := u.Messages(t, sid)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != core.MsgAssistant || m.Kind != core.KindNormal {
			continue
		}
		var tr *core.RefineTrace
		for _, p := range m.Parts {
			if p.Type == core.PartRefine {
				tr = p.Refine
			}
		}
		return m.PlainText(), tr
	}
	t.Fatal("no assistant reply")
	return "", nil
}

// TestRefineMode is F3: the composer's toggle refines the final answer with
// at most drafts × (1 + 3 × depth) model calls and replies with the voted
// answer; without the toggle the same question takes one call, a session
// setting turns it on for every message, and early_stop can be turned off.
func TestRefineMode(t *testing.T) {
	llm := pltLLM(t, refineRules()...)
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, `
agent:
  refine: {drafts: 3, depth: 2, early_stop: true}
`)})
	owner := pltOwner(t, g)
	var info dto.RefineInfo
	if code := owner.JSON(t, http.MethodGet, "/api/refine", nil, &info); code != http.StatusOK || info.Enabled || info.MaxCalls != 21 {
		t.Fatalf("refine info = %d %+v", code, info)
	}
	bound := info.MaxCalls
	rec := pltRecord(owner.Events(t))
	rec.WaitForFrame(t, "hello", 5*time.Second)
	s := owner.NewSession(t)
	send := func(text string, refine *bool) *dto.RunDetail {
		t.Helper()
		before := agentRequests(llm)
		var res core.SubmitResult
		if code := owner.JSON(t, http.MethodPost, "/api/sessions/"+string(s.ID)+"/messages", dto.SendReq{Text: text, Refine: refine}, &res); code != http.StatusAccepted {
			t.Fatalf("send %q = %d", text, code)
		}
		d := owner.Finished(t, res.Run)
		if d.Run.Status != core.RunDone {
			t.Fatalf("run %s: %s (%s)", res.Run, d.Run.Status, d.Run.Error)
		}
		if got := agentRequests(llm) - before; got != agentCalls(d) {
			t.Fatalf("mockllm saw %d calls, the run recorded %d", got, agentCalls(d))
		}
		return d
	}
	on := true

	// Toggle on: 1 answer + 2 drafts, then one round (3 continues, 3
	// critiques, 1 correction of the 41 draft) before every draft agrees.
	d := send("What is six times seven?", &on)
	if n := agentCalls(d); n != 10 || n > bound || d.Run.Steps != 1 {
		t.Fatalf("refined run: %d calls (bound %d), %d steps", n, bound, d.Run.Steps)
	}
	reply, tr := lastAnswer(t, owner, s.ID)
	if !strings.Contains(reply, "42") || strings.Contains(reply, "41") || strings.Contains(reply, "Final answer") {
		t.Fatalf("reply %q, want the voted answer 42 without the vote's line", reply)
	}
	if tr == nil || tr.Answer != "42" || !tr.EarlyStop || tr.Rounds != 1 || tr.Calls != 10 || len(tr.Votes) != 1 || tr.Votes[0].Count != 3 {
		t.Fatalf("trace %+v", tr)
	}
	if d.Refine == nil || d.Refine.Calls != 10 || len(d.Refine.Entries) != 10 {
		t.Fatalf("run detail trace %+v", d.Refine)
	}
	rec.WaitFor(t, core.EvRunRefine, 5*time.Second, func(e pltEvent) bool {
		var data core.RefineData
		return e.Run == d.Run.ID && json.Unmarshal(e.Data, &data) == nil && data.Phase == "done" && data.Trace != nil && data.Trace.Answer == "42"
	})
	entries := 0
	for _, f := range rec.Frames() {
		var e pltEvent
		var data core.RefineData
		if f.Event == string(core.EvRunRefine) && json.Unmarshal(f.Data, &e) == nil && e.Run == d.Run.ID &&
			json.Unmarshal(e.Data, &data) == nil && data.Phase == "entry" {
			entries++
		}
	}
	if entries != 10 {
		t.Fatalf("%d run.refine entry events, want one per call", entries)
	}

	// Toggle off: one call, no trace.
	d = send("And what is six times seven now?", nil)
	if n := agentCalls(d); n != 1 || d.Refine != nil {
		t.Fatalf("unrefined run: %d calls, trace %+v", n, d.Refine)
	}

	// The session setting refines a message that does not say.
	mode := core.RefineOn
	if code := owner.JSON(t, http.MethodPatch, "/api/sessions/"+string(s.ID), dto.SessionPatchReq{Refine: &mode}, nil); code != http.StatusOK {
		t.Fatalf("patch session = %d", code)
	}
	var detail dto.SessionDetail
	if owner.JSON(t, http.MethodGet, "/api/sessions/"+string(s.ID), nil, &detail); detail.Refine != core.RefineOn {
		t.Fatalf("session refine = %q", detail.Refine)
	}
	d = send("What is two plus two?", nil)
	if _, tr := lastAnswer(t, owner, s.ID); agentCalls(d) != 3 || tr == nil || !tr.EarlyStop || tr.Rounds != 0 || tr.Answer != "4" {
		t.Fatalf("session refine: %d calls, trace %+v", agentCalls(d), tr)
	}

	// Without early stop every round runs, still within the bound.
	set := dto.ConfigPatchReq{Set: map[string]json.RawMessage{"agent.refine.early_stop": json.RawMessage(`false`)}}
	if code := owner.JSON(t, http.MethodPatch, "/api/config", set, nil); code != http.StatusOK {
		t.Fatalf("config patch = %d", code)
	}
	if owner.JSON(t, http.MethodGet, "/api/refine", nil, &info); info.EarlyStop {
		t.Fatal("early_stop did not reload")
	}
	d = send("Once more: what is six times seven?", &on)
	reply, tr = lastAnswer(t, owner, s.ID)
	if n := agentCalls(d); n != 15 || n > bound || tr == nil || tr.EarlyStop || tr.Rounds != 2 || !strings.Contains(reply, "42") || strings.Contains(reply, "Final answer") {
		t.Fatalf("without early stop: %d calls (bound %d), trace %+v, reply %q", n, bound, tr, reply)
	}
}
