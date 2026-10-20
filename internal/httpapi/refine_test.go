package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
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
	want := dto.RefineInfo{Enabled: true, Drafts: 4, Depth: 1, CriticSlot: core.SlotFast, MaxCalls: 16, Multiplier: 16}
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

func TestRefineEstimate(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Agent.Refine = config.Refine{Drafts: 3, Depth: 2, EarlyStop: true, CriticSlot: "main"}
	}).setup()
	ctx := context.Background()
	mp := core.UserPrincipal(h.member.user)
	sess := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	url := "/api/sessions/" + string(sess.ID) + "/refine-estimate"

	got := decodeAs[dto.RefineEstimate](t, h.do("GET", url, nil, h.member))
	if got.USD != nil || got.Drafts != 3 || got.Multiplier != 21 {
		t.Fatalf("a chat with no answer yet = %+v (3 drafts, every round counted despite early stop: 3×(1+3×2))", got)
	}
	mkRun := func(status core.RunStatus, at time.Time, calls ...core.CallInfo) {
		run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: mp, User: h.member.user,
			Trigger: core.TrigMessage, Status: status, CreatedAt: at}
		if err := h.store.Runs().Create(ctx, run); err != nil {
			t.Fatal(err)
		}
		for _, c := range calls {
			c.ID, c.Ref, c.PriceKnown = core.NewID(core.PrefixLLMCall), core.ModelRef{Provider: "p", Model: "m"}, true
			if err := h.store.Runs().AddLLMCall(ctx, run.ID, mp, h.member.user, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	base := time.Now().Add(-time.Hour)
	step := func(n int, cost core.Micros) core.CallInfo {
		return core.CallInfo{Purpose: "agent", Step: n, Cost: cost}
	}
	// The answer step is the first call of a run's last step; tool steps,
	// titles and refine calls after it do not count.
	mkRun(core.RunDone, base, step(1, 50_000), step(2, 10_000), core.CallInfo{Purpose: "title", Cost: 900_000})
	mkRun(core.RunDone, base.Add(time.Minute), step(1, 30_000), step(1, 99_000), step(1, 99_000))
	mkRun(core.RunFailed, base.Add(2*time.Minute), step(1, 5_000_000))
	got = decodeAs[dto.RefineEstimate](t, h.do("GET", url, nil, h.member))
	if got.USD == nil || *got.USD < 0.4199 || *got.USD > 0.4201 {
		t.Fatalf("estimate = %+v, want (10000+30000)/2 µ$ × 21 = $0.42", got)
	}

	if info := decodeAs[dto.RefineInfo](t, h.do("GET", "/api/refine", nil, h.member)); info.Multiplier != got.Multiplier {
		t.Fatalf("a new chat's multiplier %v differs from the estimate's %v", info.Multiplier, got.Multiplier)
	}
	h.cfg.update(func(c *config.Config) { c.Agent.Refine.EarlyStop = false })
	if got := decodeAs[dto.RefineEstimate](t, h.do("GET", url, nil, h.member)); got.Multiplier != 21 {
		t.Fatalf("early stop changes nothing: %+v", got)
	}
	expect(t, h.do("GET", url, nil, h.admin), http.StatusNotFound)
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

func TestV1StreamKeepsAliveWhileRefining(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status core.RunStatusData
		want   int
	}{
		{"refining", core.RunStatusData{Status: "thinking", Detail: core.RefineStatusDetail}, 2},
		{"thinking", core.RunStatusData{Status: "thinking"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := newRunQueue()
				rec := httptest.NewRecorder()
				ar := &agentRun{s: &Server{clock: core.SystemClock{}}, w: rec, r: httptest.NewRequest("POST", "/v1/chat/completions", nil),
					req: oaRequest{Model: "bothy", Stream: true}, q: q, detach: true}
				done := make(chan error, 1)
				go func() { done <- ar.serve() }()
				q.OnEvent(core.Event{Type: core.EvRunStatus, Data: tc.status})
				time.Sleep(25 * time.Second)
				q.OnEvent(core.Event{Type: core.EvRunDelta, Data: core.DeltaData{Text: "42"}})
				q.OnEvent(core.Event{Type: core.EvRunFinished, Data: core.RunFinishedData{Status: core.RunDone}})
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				body := rec.Body.String()
				if n := strings.Count(body, ": refining\n"); n != tc.want {
					t.Fatalf("%d refining comments in 25 s, want %d:\n%s", n, tc.want, body)
				}
				if !strings.Contains(body, `"content":"42"`) || !strings.Contains(body, "data: [DONE]") {
					t.Fatalf("stream %s", body)
				}
			})
		})
	}
}
