package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func TestRefineInfo(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Agent.Refine = config.Refine{Enabled: true, Drafts: 4, Depth: 1, CriticSlot: "fast"}
	}).setup()
	got := decodeAs[dto.RefineInfo](t, h.do("GET", "/api/refine", nil, h.member))
	want := dto.RefineInfo{Enabled: true, Drafts: 4, Depth: 1, CriticSlot: core.SlotFast, MaxCalls: 16}
	if got != want {
		t.Fatalf("refine info = %+v, want %+v", got, want)
	}
}

func TestSessionRefineToggle(t *testing.T) {
	h := newHarness(t).setup()
	sess := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	sid := "/api/sessions/" + string(sess.ID)
	if d := decodeAs[dto.SessionDetail](t, h.do("GET", sid, nil, h.member)); d.Refine != "" {
		t.Fatalf("a new session follows the default: %q", d.Refine)
	}
	on, off, clear, bad := core.RefineOn, core.RefineOff, "", "maybe"
	expect(t, h.do("PATCH", sid, dto.SessionPatchReq{Refine: &bad}, h.member), http.StatusBadRequest)
	expect(t, h.do("PATCH", sid, dto.SessionPatchReq{Refine: &on}, h.admin), http.StatusNotFound)
	for _, v := range []string{on, off, clear} {
		expect(t, h.do("PATCH", sid, dto.SessionPatchReq{Refine: &v}, h.member), http.StatusOK)
		if d := decodeAs[dto.SessionDetail](t, h.do("GET", sid, nil, h.member)); d.Refine != v {
			t.Fatalf("after PATCH refine=%q the session says %q", v, d.Refine)
		}
	}
	if _, err := h.store.Meta().Get(context.Background(), core.RefineMetaKey(sess.ID)); err == nil {
		t.Fatal("clearing the setting removes its meta key")
	}

	yes := true
	expect(t, h.do("POST", sid+"/messages", dto.SendReq{Text: "think hard", Refine: &yes}, h.member), http.StatusAccepted)
	expect(t, h.do("POST", sid+"/messages", dto.SendReq{Text: "quick one"}, h.member), http.StatusAccepted)
	subs := h.runner.Submitted()
	if len(subs) != 2 || subs[0].Refine == nil || !*subs[0].Refine || subs[1].Refine != nil {
		t.Fatalf("run requests %+v", subs)
	}
}

func TestRunDetailCarriesTheRefineTrace(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	mp := core.UserPrincipal(h.member.user)
	sess := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	start := time.Now().Add(-time.Minute)
	mkRun := func(steps int) *core.Run {
		run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: mp, User: h.member.user,
			Trigger: core.TrigMessage, Status: core.RunDone, Steps: steps, CreatedAt: start}
		if err := h.store.Runs().Create(ctx, run); err != nil {
			t.Fatal(err)
		}
		return run
	}
	addCalls := func(run *core.Run, n int) {
		for range n {
			c := core.CallInfo{ID: core.NewID(core.PrefixLLMCall), Ref: core.ModelRef{Provider: "p", Model: "m"}, Purpose: "agent", Step: 1}
			if err := h.store.Runs().AddLLMCall(ctx, run.ID, mp, h.member.user, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	trace := &core.RefineTrace{Drafts: 2, Depth: 1, Rounds: 1, Winner: 1, Answer: "42", Calls: 3,
		Votes: []core.RefineVote{{Answer: "42", Count: 2, Drafts: []int{0, 1}}}}
	refined := mkRun(1)
	addCalls(refined, 3)
	plain := mkRun(1)
	addCalls(plain, 1)
	for _, m := range []*core.Message{
		{Session: sess.ID, Run: refined.ID, Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "Final answer: 42"},
			{Type: core.PartRefine, Refine: trace}}},
		{Session: sess.ID, Run: plain.ID, Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "Hi."}}},
	} {
		m.Kind, m.Provenance, m.CreatedAt = core.KindNormal, core.ProvSystem, time.Now()
		if err := h.store.Messages().Append(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	d := decodeAs[dto.RunDetail](t, h.do("GET", "/api/runs/"+string(refined.ID), nil, h.member))
	if d.Refine == nil || d.Refine.Winner != 1 || d.Refine.Answer != "42" || len(d.Refine.Votes) != 1 || len(d.LLMCalls) != 3 {
		t.Fatalf("refined run detail = %+v", d)
	}
	if d := decodeAs[dto.RunDetail](t, h.do("GET", "/api/runs/"+string(plain.ID), nil, h.member)); d.Refine != nil {
		t.Fatalf("a run that did not refine has no trace: %+v", d.Refine)
	}
	expect(t, h.do("GET", "/api/runs/"+string(refined.ID), nil, h.admin), http.StatusNotFound)
}

func TestV1RefineHeader(t *testing.T) {
	h := newHarness(t).setup()
	sr := h.useScriptRunner(defaultScript)
	tok := h.tokenFor(h.member, "chat")
	body := map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	expect(t, h.do("POST", "/v1/chat/completions", body, tok, withHeader("X-Bothy-Refine", "On")), http.StatusOK)
	expect(t, h.do("POST", "/v1/chat/completions", body, tok), http.StatusOK)
	expect(t, h.do("POST", "/v1/chat/completions", body, tok, withHeader("X-Bothy-Refine", "off")), http.StatusOK)
	rec := h.do("POST", "/v1/chat/completions", body, tok, withHeader("X-Bothy-Refine", "sometimes"))
	expect(t, rec, http.StatusBadRequest)
	if e := decodeAs[oaError](t, rec).Error; e.Param == nil || *e.Param != "X-Bothy-Refine" {
		t.Fatalf("error %+v", e)
	}
	subs := sr.Submitted()
	if len(subs) != 3 || subs[0].Refine == nil || !*subs[0].Refine || subs[1].Refine != nil || subs[2].Refine == nil || *subs[2].Refine {
		t.Fatalf("run requests %+v", subs)
	}
}
