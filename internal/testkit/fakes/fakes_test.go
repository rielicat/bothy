package fakes_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
)

var ctx = context.Background()

func TestClock(t *testing.T) {
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	c := fakes.NewClock(t0)
	c.Advance(90 * time.Second)
	if got := c.Now(); !got.Equal(t0.Add(90 * time.Second)) {
		t.Fatalf("Now = %v", got)
	}
}

func TestAuditor(t *testing.T) {
	var a fakes.Auditor
	for _, act := range []string{"auth.login", "tool.exec"} {
		if err := a.Record(ctx, core.AuditEntry{Action: act}); err != nil {
			t.Fatal(err)
		}
	}
	all := a.All()
	if len(all) != 2 || all[0].ID != 1 || all[1].ID != 2 || all[0].At.IsZero() {
		t.Fatalf("entries = %+v", all)
	}
	if got := a.Actions(); !slices.Equal(got, []string{"auth.login", "tool.exec"}) {
		t.Fatalf("Actions = %v", got)
	}
	if id, err := a.Verify(ctx); id != 0 || err != nil || a.Flush(ctx) != nil {
		t.Fatal("Verify/Flush must report an intact chain")
	}
}

func TestSink(t *testing.T) {
	var s fakes.Sink
	got := make(chan core.Event, 1)
	go func() {
		e, err := s.Wait(ctx, core.EvRunFinished)
		if err != nil {
			t.Error(err)
		}
		got <- e
	}()
	s.OnEvent(core.Event{Type: core.EvRunDelta, Data: core.DeltaData{Text: "Hel"}})
	s.OnEvent(core.Event{Type: core.EvRunDelta, Data: core.DeltaData{Text: "lo"}})
	s.OnEvent(core.Event{Type: core.EvRunFinished, Data: core.RunFinishedData{Status: core.RunDone}})
	select {
	case e := <-got:
		if e.Data.(core.RunFinishedData).Status != core.RunDone {
			t.Fatalf("Wait returned %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return")
	}
	if s.Text() != "Hello" || len(s.All()) != 3 || !slices.Equal(s.Types(), []core.EventType{core.EvRunDelta, core.EvRunDelta, core.EvRunFinished}) {
		t.Fatalf("sink = %q %v", s.Text(), s.Types())
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Wait(cctx, core.EvRunParked); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait on cancelled ctx = %v", err)
	}
}

func TestVault(t *testing.T) {
	const u = core.UserID("usr_0000000000000000000000000a")
	v := fakes.NewVault(map[string]string{
		"openai":                              "sk-global-value",
		"github":                              "ghp_global",
		string(core.UserScope(u)) + "/github": "ghp_user_token",
	})
	if s, err := v.Get(ctx, core.UserScope(u), "github", "tool"); err != nil || s.Reveal() != "ghp_user_token" {
		t.Fatalf("user scope Get = %q, %v", s.Reveal(), err)
	}
	if s, err := v.Get(ctx, core.UserScope(u), "openai", "provider"); err != nil || s.Reveal() != "sk-global-value" {
		t.Fatalf("global fallback Get = %q, %v", s.Reveal(), err)
	}
	if _, err := v.Get(ctx, core.GlobalScope, "missing", "x"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing Get = %v", err)
	}
	if s, err := v.Resolve(ctx, "Bearer secret:github", u, "mcp"); err != nil || s.Reveal() != "Bearer ghp_user_token" {
		t.Fatalf("embedded Resolve = %q, %v", s.Reveal(), err)
	}
	if s, err := v.Resolve(ctx, "secret:github", "", "channel"); err != nil || s.Reveal() != "ghp_global" {
		t.Fatalf("global-only Resolve = %q, %v", s.Reveal(), err)
	}
	_, err := v.Resolve(ctx, "plaintext-hunter2", "", "x")
	if !errors.Is(err, core.ErrInvalid) || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("plaintext Resolve = %v", err)
	}
	if !slices.Contains(v.Uses, "github|mcp") || !slices.Contains(v.Uses, "openai|provider") {
		t.Fatalf("Uses = %v", v.Uses)
	}

	red := v.Redactor()
	if got := red.Redact("key sk-global-value and ghp_user_token"); got != "key [redacted] and [redacted]" {
		t.Fatalf("Redact = %q", got)
	}
	if _, err := v.Put(ctx, core.GlobalScope, "brave", []byte("brave-key-1234"), u); err != nil {
		t.Fatal(err)
	}
	list, _ := v.List(ctx, core.GlobalScope)
	if len(list) != 3 || list[0].Name != "brave" || list[0].Hint != "1234" || list[0].Source != "vault" {
		t.Fatalf("List = %+v", list)
	}
	if err := v.Delete(ctx, core.GlobalScope, "brave", u); err != nil {
		t.Fatal(err)
	}
	if red.Redact("brave-key-1234") != "brave-key-1234" {
		t.Fatal("deleted secret still redacted")
	}
	if err := v.Delete(ctx, core.GlobalScope, "brave", u); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
	if st := v.Status(); st.Secrets != 3 || st.Sealed {
		t.Fatalf("Status = %+v", st)
	}

	v.Seal()
	if _, err := v.Get(ctx, core.GlobalScope, "openai", "x"); !errors.Is(err, core.ErrSealed) {
		t.Fatalf("sealed Get = %v", err)
	}
	if _, err := v.Resolve(ctx, "secret:openai", "", "x"); !errors.Is(err, core.ErrSealed) || !v.Status().Sealed {
		t.Fatalf("sealed Resolve = %v", err)
	}
	if err := v.Unseal(ctx, []byte("anything")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, core.GlobalScope, "openai", "x"); err != nil {
		t.Fatalf("unsealed Get = %v", err)
	}
}

func TestRedactorLongestFirst(t *testing.T) {
	r := fakes.NewRedactor()
	r.Add("a", []byte("abc"))
	r.Add("b", []byte("abcdef"))
	r.Add("empty", nil)
	if got := string(r.RedactBytes([]byte("x abcdef y abc"))); got != "x [redacted] y [redacted]" {
		t.Fatalf("RedactBytes = %q", got)
	}
	r.Remove("a")
	if got := r.Redact("abc"); got != "abc" {
		t.Fatalf("after Remove = %q", got)
	}
}

func shellSpec() core.ToolSpec {
	return core.ToolSpec{Def: core.ToolDef{Name: "shell"}, Manifest: core.Manifest{Title: "Run shell command", SideEffect: core.SELocalWrite, Taint: 0}}
}

func TestPolicy(t *testing.T) {
	p := &fakes.Policy{Verdicts: map[string]core.Verdict{"shell": core.VerdictAsk, "fs_delete": core.VerdictDeny}}
	in := core.PolicyInput{User: "usr_a", Trigger: core.TrigMessage, Spec: shellSpec(), Action: &core.Action{Tool: "shell"}}
	d := p.Decide(ctx, in)
	if d.Verdict != core.VerdictAsk || d.Rule != "fake" || d.OnTimeout != core.TimeoutDeny || d.Timeout != fakes.InteractiveTimeout ||
		!slices.Contains(d.Choices, core.AllowSession) || d.Approver.User != "usr_a" {
		t.Fatalf("interactive ask = %+v", d)
	}
	in.Trigger = core.TrigSchedule
	if d := p.Decide(ctx, in); d.OnTimeout != core.TimeoutPark || d.Timeout != fakes.ScheduledTimeout {
		t.Fatalf("scheduled ask = %+v", d)
	}
	in.Action.Tool = "fs_delete"
	if d := p.Decide(ctx, in); d.Verdict != core.VerdictDeny {
		t.Fatalf("deny = %+v", d)
	}
	in.Action.Tool = "fs_read"
	if d := p.Decide(ctx, in); d.Verdict != core.VerdictAuto || d.Choices != nil {
		t.Fatalf("default = %+v", d)
	}
	p.Default = core.VerdictAsk
	if d := p.Decide(ctx, in); d.Verdict != core.VerdictAsk {
		t.Fatalf("Default = %+v", d)
	}
	p.Func = func(core.PolicyInput) core.Decision { return core.Decision{Verdict: core.VerdictDeny, Rule: "custom"} }
	if d := p.Decide(ctx, in); d.Rule != "custom" {
		t.Fatalf("Func = %+v", d)
	}
	if len(p.Inputs) != 6 {
		t.Fatalf("Inputs = %d", len(p.Inputs))
	}

	a := &core.Action{Tool: "shell", SideEffect: core.SELocalWrite, Payload: json.RawMessage(`{"argv":["ls"]}`)}
	g, ok := p.NarrowGrant(a, "usr_a", core.GrantSession, "ses_1", "usr_a")
	if !ok || g.Kind != core.GrantTool || g.Tool != "shell" || g.Pattern[""] != `{"argv":["ls"]}` || g.MaxSE != core.SELocalWrite {
		t.Fatalf("NarrowGrant = %+v", g)
	}
	if _, ok := p.NarrowGrant(nil, "usr_a", core.GrantSession, "", ""); ok {
		t.Fatal("NarrowGrant(nil) ok")
	}
	id, err := p.Grant(ctx, g)
	if err != nil || !core.ValidID(core.PrefixGrant, string(id)) || p.Grants[0].ID != id {
		t.Fatalf("Grant = %q, %v", id, err)
	}
	if err := p.Revoke(ctx, core.Viewer{Principals: []core.PrincipalID{"usr_b"}}, id, "usr_b"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign Revoke = %v", err)
	}
	if err := p.Revoke(ctx, core.Viewer{Principals: []core.PrincipalID{"usr_a"}}, id, "usr_a"); err != nil || p.Revoked[0] != id {
		t.Fatalf("Revoke = %v", err)
	}

	spec := core.ToolSpec{Manifest: core.Manifest{Taint: core.TaintPrivate}}
	if got := p.NextTaint(0, spec, nil, &core.ToolResult{}, false); got != core.TaintPrivate {
		t.Fatalf("NextTaint manifest = %v", got)
	}
	if got := p.NextTaint(core.TaintPrivate, core.ToolSpec{}, nil, nil, true); !got.Exposed() {
		t.Fatalf("NextTaint egress = %v", got)
	}
	if got := p.NextTaint(0, core.ToolSpec{}, nil, &core.ToolResult{Untrusted: true}, false); got != core.TaintUntrusted {
		t.Fatalf("NextTaint untrusted = %v", got)
	}
}

func askRequest(run core.RunID, p core.PrincipalID, onTimeout core.TimeoutAction) core.ApprovalRequest {
	return core.ApprovalRequest{
		Run: run, Session: "ses_1", Principal: p, ToolCallID: "tcl_1", Spec: shellSpec(), Context: "Telegram · DM",
		Action:   &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: core.SELocalWrite, Argv: []string{"bash", "-lc", "ls"}},
		Decision: core.Decision{Verdict: core.VerdictAsk, Choices: []core.Choice{core.AllowOnce, core.Deny}, OnTimeout: onTimeout, Approver: core.ApproverReq{User: "usr_a"}},
	}
}

func TestApprovalsImmediate(t *testing.T) {
	ap := &fakes.Approvals{}
	a, err := ap.Request(ctx, askRequest("run_1", "usr_a", core.TimeoutDeny))
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != core.ApprovalAllowed || a.Choice != core.AllowOnce || a.Card.Title != "Run shell command" || a.Card.Code != "bash -lc ls" || a.Card.Expires.IsZero() {
		t.Fatalf("approval = %+v", a)
	}
	if c, err := ap.Await(ctx, a.ID); c != core.AllowOnce || err != nil {
		t.Fatalf("Await = %q, %v", c, err)
	}
	ap.Choice = core.Deny
	a2, _ := ap.Request(ctx, askRequest("run_1", "usr_a", core.TimeoutDeny))
	if c, err := ap.Await(ctx, a2.ID); c != core.Deny || err != nil {
		t.Fatalf("Await deny = %q, %v", c, err)
	}
	if len(ap.Requests) != 2 {
		t.Fatalf("Requests = %d", len(ap.Requests))
	}
	if _, err := ap.Resolve(ctx, core.Resolution{ID: a.ID, Choice: core.Deny}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Resolve of a decided approval = %v", err)
	}
}

func TestApprovalsManual(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ap := &fakes.Approvals{Manual: true}
		var resolved []*core.Approval
		var mu sync.Mutex
		ap.OnResolved(func(a *core.Approval) { mu.Lock(); resolved = append(resolved, a); mu.Unlock() })

		a, _ := ap.Request(ctx, askRequest("run_1", "usr_a", core.TimeoutDeny))
		if a.Status != core.ApprovalPending {
			t.Fatalf("status = %s", a.Status)
		}
		done := make(chan core.Choice)
		go func() {
			c, err := ap.Await(ctx, a.ID)
			if err != nil {
				t.Error(err)
			}
			done <- c
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("Await returned before Resolve")
		default:
		}
		got, err := ap.Resolve(ctx, core.Resolution{ID: a.ID, Choice: core.AllowSession, By: "usr_a", Via: "telegram"})
		if err != nil || got.Status != core.ApprovalAllowed || got.DecidedBy != "usr_a" || got.DecidedVia != "telegram" {
			t.Fatalf("Resolve = %+v, %v", got, err)
		}
		if c := <-done; c != core.AllowSession {
			t.Fatalf("Await = %q", c)
		}
		if _, err := ap.Resolve(ctx, core.Resolution{ID: a.ID, Choice: core.Deny}); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("double Resolve = %v", err)
		}
		if len(resolved) != 1 || resolved[0].ID != a.ID || len(ap.Resolutions) != 1 {
			t.Fatalf("callbacks %d resolutions %d", len(resolved), len(ap.Resolutions))
		}

		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		b, _ := ap.Request(ctx, askRequest("run_2", "usr_a", core.TimeoutDeny))
		if _, err := ap.Await(cctx, b.ID); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Await past ctx deadline = %v", err)
		}
	})
}

func TestApprovalsTimeouts(t *testing.T) {
	ap := &fakes.Approvals{Manual: true}
	var calls []core.ApprovalStatus
	ap.OnResolved(func(a *core.Approval) { calls = append(calls, a.Status) })

	park, _ := ap.Request(ctx, askRequest("run_p", "usr_a", core.TimeoutPark))
	if err := ap.Expire(park.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ap.Await(ctx, park.ID); !errors.Is(err, core.ErrParked) {
		t.Fatalf("Await parked = %v", err)
	}
	if got, _ := ap.Get(ctx, core.SystemViewer(), park.ID); got.Status != core.ApprovalParked || got.ParkUntil.IsZero() {
		t.Fatalf("parked approval = %+v", got)
	}
	if len(calls) != 0 {
		t.Fatal("parking must not run callbacks")
	}
	if _, err := ap.Resolve(ctx, core.Resolution{ID: park.ID, Choice: core.AllowOnce}); err != nil {
		t.Fatalf("Resolve parked = %v", err)
	}

	deny, _ := ap.Request(ctx, askRequest("run_d", "usr_a", core.TimeoutDeny))
	if err := ap.Expire(deny.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ap.Await(ctx, deny.ID); !errors.Is(err, core.ErrApprovalExpired) {
		t.Fatalf("Await expired = %v", err)
	}
	if err := ap.Expire(deny.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second Expire = %v", err)
	}

	stale, _ := ap.Request(ctx, askRequest("run_s", "usr_a", core.TimeoutPark))
	_ = ap.Expire(stale.ID)
	_ = ap.Expire(stale.ID)
	if got, _ := ap.Get(ctx, core.SystemViewer(), stale.ID); got.Status != core.ApprovalExpired {
		t.Fatalf("park TTL expiry = %s", got.Status)
	}
	if !slices.Equal(calls, []core.ApprovalStatus{core.ApprovalAllowed, core.ApprovalExpired, core.ApprovalExpired}) {
		t.Fatalf("callbacks = %v", calls)
	}

	c1, _ := ap.Request(ctx, askRequest("run_c", "usr_a", core.TimeoutDeny))
	c2, _ := ap.Request(ctx, askRequest("run_c", "usr_a", core.TimeoutPark))
	_ = ap.Expire(c2.ID)
	if err := ap.CancelRun(ctx, "run_c"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []core.ApprovalID{c1.ID, c2.ID} {
		if _, err := ap.Await(ctx, id); !errors.Is(err, core.ErrStopped) {
			t.Fatalf("Await cancelled = %v", err)
		}
	}
	if err := ap.Expire("apr_missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Expire missing = %v", err)
	}
}

func TestApprovalsQueries(t *testing.T) {
	ap := &fakes.Approvals{Manual: true}
	mine, _ := ap.Request(ctx, askRequest("run_1", "usr_a", core.TimeoutDeny))
	theirs, _ := ap.Request(ctx, askRequest("run_2", "usr_b", core.TimeoutDeny))
	adminReq := askRequest("run_3", "usr_b", core.TimeoutDeny)
	adminReq.Decision.Approver = core.ApproverReq{AdminOnly: true}
	admin, _ := ap.Request(ctx, adminReq)

	va := core.Viewer{User: "usr_a", Principals: []core.PrincipalID{"usr_a"}}
	if _, err := ap.Get(ctx, va, theirs.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign Get = %v", err)
	}
	if _, err := ap.Get(ctx, va, admin.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("admin-only Get by member = %v", err)
	}
	va.Admin = true
	if _, err := ap.Get(ctx, va, admin.ID); err != nil {
		t.Fatalf("admin-only Get by admin = %v", err)
	}
	if p, _ := ap.Pending(ctx, va); len(p) != 2 || p[0].ID != mine.ID || p[1].ID != admin.ID {
		t.Fatalf("Pending = %v", p)
	}
	_, _ = ap.Resolve(ctx, core.Resolution{ID: mine.ID, Choice: core.Deny})
	_, _ = ap.Resolve(ctx, core.Resolution{ID: admin.ID, Choice: core.AllowOnce})
	if r, _ := ap.Recent(ctx, va, core.Page{}); len(r) != 2 || r[0].ID != admin.ID || r[1].ID != mine.ID {
		t.Fatalf("Recent = %v", r)
	}
	if r, _ := ap.Recent(ctx, va, core.Page{Limit: 1}); len(r) != 1 {
		t.Fatalf("Recent limit = %v", r)
	}
	if r, _ := ap.Recent(ctx, va, core.Page{Before: mine.CreatedAt.UnixMilli()}); len(r) != 0 {
		t.Fatalf("Recent before = %v", r)
	}
	if len(ap.All()) != 3 {
		t.Fatal("All")
	}

	ref := core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "1"}, ID: "7"}
	if err := ap.AddMessageRef(ctx, mine.ID, ref, true); err != nil {
		t.Fatal(err)
	}
	if refs, _ := ap.MessageRefs(ctx, mine.ID); len(refs) != 1 || refs[0].Ref != ref || !refs[0].Truncated {
		t.Fatalf("MessageRefs = %+v", refs)
	}
	if err := ap.AddMessageRef(ctx, "apr_x", ref, false); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("AddMessageRef unknown = %v", err)
	}
}

func TestApprovalsButtonData(t *testing.T) {
	ap := &fakes.Approvals{}
	id := core.ApprovalID(core.NewID(core.PrefixApproval))
	for _, c := range []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny} {
		data := ap.ButtonData(id, c)
		if len(data) > 64 {
			t.Fatalf("button data %q is %d bytes", data, len(data))
		}
		gotID, gotC, err := ap.ParseButtonData(data)
		if err != nil || gotID != id || gotC != c {
			t.Fatalf("ParseButtonData(%q) = %q %q %v", data, gotID, gotC, err)
		}
	}
	data := ap.ButtonData(id, core.Deny)
	tampered := strings.Replace(data, ":d:", ":o:", 1)
	for _, bad := range []string{tampered, "a:" + string(id) + ":x:abcdefgh", "s:run_1", "a:apr_short:o:abcdefgh"} {
		if _, _, err := ap.ParseButtonData(bad); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("ParseButtonData(%q) = %v", bad, err)
		}
	}
}

func build(got *[]core.ModelInfo) core.BuildFunc {
	return func(m core.ModelInfo) (*core.Request, error) {
		*got = append(*got, m)
		return &core.Request{Model: m.Ref.Model, Messages: []core.Message{{Role: core.MsgUser}}}, nil
	}
}

func TestLLMScript(t *testing.T) {
	l := &fakes.LLM{Steps: []fakes.LLMStep{
		{Text: []string{"Hi ", "there"}, Usage: core.Usage{Input: 10, Output: 2}},
		{Text: []string{"Let me look."}, ToolCalls: []core.ToolCall{{Name: "fs_read", Args: json.RawMessage(`{"path":"a"}`)}, {Name: "fs_list"}}},
	}}
	var models []core.ModelInfo
	var events []core.StreamEvent
	on := func(e core.StreamEvent) error { events = append(events, e); return nil }

	resp, ci, err := l.Complete(ctx, core.SlotMain, nil, build(&models), on)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != core.StopEnd || resp.Message.PlainText() != "Hi there" || resp.Usage.Input != 10 || resp.Model != "fake-model" {
		t.Fatalf("response = %+v", resp)
	}
	if ci.Ref != fakes.DefaultModel.Ref || ci.Slot != core.SlotMain || ci.Usage.Output != 2 || !core.ValidID(core.PrefixLLMCall, ci.ID) {
		t.Fatalf("call info = %+v", ci)
	}
	if len(models) != 1 || models[0].Context != fakes.DefaultModel.Context {
		t.Fatalf("build saw %+v", models)
	}
	wantTypes := []core.StreamEventType{core.StreamTextDelta, core.StreamTextDelta, core.StreamUsage}
	if len(events) != 3 || events[0].Text != "Hi " || !slices.Equal(eventTypes(events), wantTypes) {
		t.Fatalf("events = %+v", events)
	}

	events = nil
	resp, _, err = l.Complete(ctx, core.SlotMain, nil, build(&models), on)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.StreamEventType{core.StreamTextDelta, core.StreamToolCallStart, core.StreamToolCallDone, core.StreamToolCallStart, core.StreamToolCallDone, core.StreamUsage}
	if !slices.Equal(eventTypes(events), want) {
		t.Fatalf("event types = %v", eventTypes(events))
	}
	calls := toolCalls(resp.Message)
	if resp.Stop != core.StopTool || len(calls) != 2 || calls[0].ID == "" || calls[0].ID == calls[1].ID || string(calls[1].Args) != "{}" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if events[2].Call.ID != calls[0].ID || string(events[2].Call.Args) != `{"path":"a"}` {
		t.Fatalf("done event = %+v", events[2].Call)
	}

	if _, _, err := l.Complete(ctx, core.SlotMain, nil, build(&models), on); err == nil || !strings.Contains(err.Error(), "no scripted step") {
		t.Fatalf("exhausted script = %v", err)
	}
	if l.Calls() != 3 || len(l.Recorded()) != 3 || l.Recorded()[0].Model != "fake-model" {
		t.Fatalf("recorded %d calls", l.Calls())
	}
}

func eventTypes(evs []core.StreamEvent) []core.StreamEventType {
	out := make([]core.StreamEventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func toolCalls(m core.Message) []core.ToolCall {
	var out []core.ToolCall
	for _, p := range m.Parts {
		if p.ToolCall != nil {
			out = append(out, *p.ToolCall)
		}
	}
	return out
}

func TestLLMErrors(t *testing.T) {
	boom := errors.New("provider down")
	l := &fakes.LLM{Steps: []fakes.LLMStep{{Err: boom}, {Text: []string{"partial"}, Err: boom}, {Text: []string{"x"}}}}
	var models []core.ModelInfo
	var texts []string
	on := func(e core.StreamEvent) error { texts = append(texts, e.Text); return nil }

	if _, ci, err := l.Complete(ctx, core.SlotMain, nil, build(&models), on); !errors.Is(err, boom) || len(texts) != 0 || ci.Error == "" {
		t.Fatalf("immediate error = %v, events %v", err, texts)
	}
	if _, _, err := l.Complete(ctx, core.SlotMain, nil, build(&models), on); !errors.Is(err, boom) || !slices.Equal(texts, []string{"partial"}) {
		t.Fatalf("mid-stream error = %v, events %v", err, texts)
	}
	stop := errors.New("sink full")
	if _, _, err := l.Complete(ctx, core.SlotMain, nil, build(&models), func(core.StreamEvent) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("callback error = %v", err)
	}
	buildErr := errors.New("does not fit")
	if _, _, err := (&fakes.LLM{}).Complete(ctx, core.SlotMain, nil, func(core.ModelInfo) (*core.Request, error) { return nil, buildErr }, nil); !errors.Is(err, buildErr) {
		t.Fatalf("build error = %v", err)
	}
}

func TestLLMBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		l := &fakes.LLM{Steps: []fakes.LLMStep{
			{Text: []string{"first", "second"}, Block: release},
			{Block: make(chan struct{})},
		}}
		var mu sync.Mutex
		var texts []string
		on := func(e core.StreamEvent) error {
			mu.Lock()
			texts = append(texts, e.Text)
			mu.Unlock()
			return nil
		}
		seen := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(texts) }
		var models []core.ModelInfo

		errc := make(chan error)
		go func() {
			_, _, err := l.Complete(ctx, core.SlotMain, nil, build(&models), on)
			errc <- err
		}()
		synctest.Wait()
		if got := seen(); !slices.Equal(got, []string{"first"}) {
			t.Fatalf("before release: %v", got)
		}
		close(release)
		if err := <-errc; err != nil || !slices.Equal(seen(), []string{"first", "second", ""}) {
			t.Fatalf("after release: %v %q", err, seen())
		}

		cctx, cancel := context.WithCancel(ctx)
		go func() {
			_, _, err := l.Complete(cctx, core.SlotMain, nil, build(&models), on)
			errc <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked call after cancel = %v", err)
		}
	})
}

func TestLLMModels(t *testing.T) {
	small := core.ModelInfo{Context: 16384}
	l := &fakes.LLM{Info: small, Steps: []fakes.LLMStep{{Text: []string{"ok"}}}}
	if c := l.Chain(core.SlotMain, nil); len(c) != 1 || c[0].Context != 16384 || c[0].Ref != fakes.DefaultModel.Ref {
		t.Fatalf("Chain = %+v", c)
	}
	ref := core.ModelRef{Provider: "anthropic", Model: "claude"}
	if c := l.Chain(core.SlotMain, &ref); c[0].Ref != ref {
		t.Fatalf("override Chain = %+v", c)
	}
	if _, ok := l.Model(fakes.DefaultModel.Ref); !ok {
		t.Fatal("Model(default) not found")
	}
	if _, ok := l.Model(ref); ok {
		t.Fatal("Model(other) found")
	}
	if l.Estimate(strings.Repeat("x", 36), ref) != 10 || l.Estimate("", ref) != 0 {
		t.Fatal("Estimate is not bytes/3.6")
	}
	req := &core.Request{Model: "claude"}
	if _, ci, err := l.Passthrough(ctx, ref, req, nil); err != nil || ci.Ref != ref || l.Recorded()[0] != req {
		t.Fatalf("Passthrough = %+v, %v", ci, err)
	}
	if p, err := l.Probe(ctx, core.ProbeConfig{}); err != nil || !p.OK || p.Context != 16384 {
		t.Fatalf("Probe = %+v", p)
	}
}

// sessionStore is the slice of core.Store the Runner fake touches.
type sessionStore struct {
	core.Store
	repo *sessionRepo
}

func (s sessionStore) Sessions() core.SessionRepo { return s.repo }

type sessionRepo struct {
	core.SessionRepo
	mu   sync.Mutex
	rows map[core.SessionID]core.Session
}

func (r *sessionRepo) Create(_ context.Context, s *core.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[s.ID] = *s
	return nil
}

func (r *sessionRepo) Get(_ context.Context, v core.Viewer, id core.SessionID) (*core.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.rows[id]
	if !ok || !v.Can(s.Principal) {
		return nil, core.ErrNotFound
	}
	return &s, nil
}

func (r *sessionRepo) ByChannelKey(_ context.Context, p core.PrincipalID, key string) (*core.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.rows {
		if s.Principal == p && s.ChannelKey == key && !s.Archived {
			return &s, nil
		}
	}
	return nil, core.ErrNotFound
}

func (r *sessionRepo) Archive(_ context.Context, v core.Viewer, id core.SessionID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.rows[id]
	if !ok || !v.Can(s.Principal) {
		return core.ErrNotFound
	}
	s.Archived = true
	r.rows[id] = s
	return nil
}

func TestRunnerSubmit(t *testing.T) {
	r := &fakes.Runner{}
	res1, err1 := r.Submit(ctx, core.RunRequest{Session: "ses_1"})
	res2, _ := r.Submit(ctx, core.RunRequest{Session: "ses_1"})
	if err1 != nil || !core.ValidID(core.PrefixRun, string(res1.Run)) || res1.Run == res2.Run {
		t.Fatalf("Submit = %+v %+v, %v", res1, res2, err1)
	}
	r.Result = core.SubmitResult{Queued: true}
	if res, _ := r.Submit(ctx, core.RunRequest{}); res != r.Result {
		t.Fatalf("Result = %+v", res)
	}
	r.Err = core.ErrBusy
	if _, err := r.Submit(ctx, core.RunRequest{}); !errors.Is(err, core.ErrBusy) {
		t.Fatalf("Err = %v", err)
	}
	if err := r.SetPaused(ctx, true, "usr_a"); err != nil || !r.Paused() {
		t.Fatal("SetPaused")
	}
	if _, err := r.Submit(ctx, core.RunRequest{}); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("paused Submit = %v", err)
	}
	if len(r.Submitted()) != 5 {
		t.Fatalf("Submits = %d", len(r.Submitted()))
	}
	if ok, err := r.Stop(ctx, core.SystemViewer(), "ses_1", "usr_a"); !ok || err != nil || !slices.Equal(r.Stopped(), []core.SessionID{"ses_1"}) {
		t.Fatal("Stop")
	}
	_, _ = r.StopAll(ctx, "usr_a")
	_ = r.Resume(ctx, &core.Approval{ID: "apr_1"})
	if r.StopAlls != 1 || !slices.Equal(r.Resumed, []core.ApprovalID{"apr_1"}) {
		t.Fatalf("StopAlls %d Resumed %v", r.StopAlls, r.Resumed)
	}
}

func TestRunnerSessions(t *testing.T) {
	chat := core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM}
	v := core.Viewer{Principals: []core.PrincipalID{"usr_a"}}
	for name, r := range map[string]*fakes.Runner{
		"in memory": {},
		"store":     {Store: sessionStore{repo: &sessionRepo{rows: map[core.SessionID]core.Session{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			s1, err := r.ResolveSession(ctx, "usr_a", chat)
			if err != nil || s1.ChannelKey != chat.Key() || s1.Origin != "telegram" || s1.Chat == nil {
				t.Fatalf("ResolveSession = %+v, %v", s1, err)
			}
			if s2, _ := r.ResolveSession(ctx, "usr_a", chat); s2.ID != s1.ID {
				t.Fatal("same chat resolved to a new session")
			}
			if s3, _ := r.ResolveSession(ctx, "usr_b", chat); s3.ID == s1.ID {
				t.Fatal("another principal shares the session")
			}
			if _, err := r.Fork(ctx, core.Viewer{Principals: []core.PrincipalID{"usr_b"}}, s1.ID, "usr_b"); !errors.Is(err, core.ErrNotFound) {
				t.Fatalf("foreign Fork = %v", err)
			}
			fork, err := r.Fork(ctx, v, s1.ID, "usr_a")
			if err != nil || fork.ID == s1.ID || fork.ChannelKey != chat.Key() {
				t.Fatalf("Fork = %+v, %v", fork, err)
			}
			if s4, _ := r.ResolveSession(ctx, "usr_a", chat); s4.ID != fork.ID {
				t.Fatalf("chat did not move to the fork: %s", s4.ID)
			}
			web, _ := r.NewSession(ctx, "usr_a", "web", nil)
			if web.ChannelKey != "" || web.Chat != nil || web.ModelSlot != core.SlotMain {
				t.Fatalf("NewSession = %+v", web)
			}
		})
	}
}

func TestHub(t *testing.T) {
	h := &fakes.Hub{PreferredChats: map[core.UserID]core.ChatRef{"usr_a": {Channel: "telegram", ChatID: "42"}}}
	chat := core.ChatRef{Channel: "telegram", ChatID: "42"}
	ref, err := h.Send(ctx, chat, "hello")
	if err != nil || ref.ID != "1" || !slices.Equal(h.Messages(), []string{"telegram:42:|hello"}) {
		t.Fatalf("Send = %+v %v %v", ref, err, h.Sent)
	}
	s1 := h.StreamTo(chat, "stream")
	s2 := h.StreamTo(chat, "final")
	if s1 != s2 || h.StreamModes[chat.Key()] != "final" {
		t.Fatal("StreamTo must reuse the chat's sink and record the mode")
	}
	s1.OnEvent(core.Event{Type: core.EvRunDelta, Data: core.DeltaData{Text: "x"}})
	if got, ok := h.Stream(chat); !ok || got.Text() != "x" {
		t.Fatal("Stream")
	}
	if c, ok := h.PreferredChat(ctx, "usr_a"); !ok || c.ChatID != "42" {
		t.Fatal("PreferredChat")
	}
	if _, ok := h.PreferredChat(ctx, "usr_b"); ok {
		t.Fatal("PreferredChat for unknown user")
	}
	h.OnMessage(ctx, core.Inbound{Text: "hi"})
	h.OnAction(ctx, core.ActionEvent{Data: "s:run_1"})
	if len(h.Inbound) != 1 || len(h.Actions) != 1 {
		t.Fatal("inbound not recorded")
	}
	if _, err := h.Test(ctx, "slack"); !errors.Is(err, core.ErrNotFound) || h.DeepLink("slack", "c") != "" {
		t.Fatal("unknown channel")
	}
}

func TestAuth(t *testing.T) {
	clock := fakes.NewClock(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	a := &fakes.Auth{
		Identities: map[string]core.IdentityResolution{"telegram:7": {Status: core.IdentityLinked, User: "usr_a"}},
		Viewers:    map[core.UserID]core.Viewer{"usr_admin": {User: "usr_admin", Admin: true}},
		Codes:      map[string]core.UserID{"7KQ2MX4D": "usr_b"},
		Clock:      clock,
	}
	if r, _ := a.ResolveIdentity(ctx, "telegram", "7"); r.User != "usr_a" {
		t.Fatalf("ResolveIdentity = %+v", r)
	}
	if r, _ := a.ResolveIdentity(ctx, "telegram", "8"); r.Status != "" {
		t.Fatalf("unknown identity = %+v", r)
	}
	if v, _ := a.ViewerFor(ctx, "usr_admin"); !v.Admin {
		t.Fatal("configured viewer")
	}
	if v, _ := a.ViewerFor(ctx, "usr_x"); v.User != "usr_x" || !v.Can("usr_x") || v.Admin {
		t.Fatalf("default viewer = %+v", v)
	}

	stranger := core.ChannelIdentity{Channel: "telegram", ExternalID: "99"}
	n1, _ := a.SenderNotice(ctx, stranger)
	clock.Advance(time.Hour)
	n2, _ := a.SenderNotice(ctx, stranger)
	clock.Advance(24 * time.Hour)
	n3, _ := a.SenderNotice(ctx, stranger)
	if !n1.Reply || n2.Reply || !n3.Reply || len(n1.Code.Code) != 9 || n1.Code.Code[4] != '-' || len(a.Notices) != 3 {
		t.Fatalf("notices = %+v %+v %+v", n1, n2, n3)
	}

	linked, err := a.RedeemPairing(ctx, stranger, "7kq2-mx4d")
	if err != nil || linked.Status != core.IdentityLinked || linked.User != "usr_b" {
		t.Fatalf("RedeemPairing = %+v, %v", linked, err)
	}
	if r, _ := a.ResolveIdentity(ctx, "telegram", "99"); r.Status != core.IdentityLinked || r.User != "usr_b" {
		t.Fatalf("after pairing = %+v", r)
	}
	if _, err := a.RedeemPairing(ctx, stranger, "7KQ2-MX4D"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("code reuse = %v", err)
	}
	code, err := a.NewPairingCode(ctx, core.Actor{User: "usr_c"})
	if err != nil || code.ExpiresAt.Sub(clock.Now()) != fakes.PairingTTL {
		t.Fatalf("NewPairingCode = %+v, %v", code, err)
	}
	if got, err := a.RedeemPairing(ctx, core.ChannelIdentity{Channel: "slack", ExternalID: "U1"}, code.Code); err != nil || got.User != "usr_c" {
		t.Fatalf("redeem new code = %+v, %v", got, err)
	}
	if len(a.Redeemed) != 3 || !strings.HasPrefix(a.Redeemed[0], "telegram:99|") {
		t.Fatalf("Redeemed = %v", a.Redeemed)
	}
}

func TestBudget(t *testing.T) {
	b := &fakes.Budget{}
	m := b.StartRun("run_1", "usr_a", core.RunLimits{MaxSteps: 2, MaxTokens: 1000, MaxCost: 500})
	if m.Step() != nil || m.Step() != nil {
		t.Fatal("steps within the limit failed")
	}
	var be *core.BudgetError
	if err := m.Step(); !errors.As(err, &be) || be.Kind != core.BudgetSteps || be.Limit != 2 || be.Used != 2 {
		t.Fatalf("third Step = %v", err)
	}
	if err := m.BeforeCall(ctx, 1_000_000, core.ModelInfo{}); err != nil {
		t.Fatalf("BeforeCall without Deny = %v", err)
	}
	_ = m.AfterCall(ctx, core.CallInfo{Usage: core.Usage{Input: 100, Output: 50}, Cost: 200})
	m.ObserveTool("shell", 1, 2)
	if m.ObserveText(7) != core.LoopOK {
		t.Fatal("loop verdict")
	}
	tot := m.Totals()
	if tot.Steps != 2 || tot.ToolCalls != 1 || tot.Usage.Output != 50 || tot.Cost != 200 {
		t.Fatalf("Totals = %+v", tot)
	}
	if r := m.Remaining(); r.MaxSteps != 0 || r.MaxTokens != 850 || r.MaxCost != 300 || r.MaxTime != 0 {
		t.Fatalf("Remaining = %+v", r)
	}
	_ = b.RecordSpend(ctx, "usr_a", core.CallInfo{Cost: 100})
	if spent, _, _ := b.Daily(ctx, "usr_a"); spent != 300 || len(b.Calls) != 2 {
		t.Fatalf("Daily = %v, Calls %d", spent, len(b.Calls))
	}

	b.Deny = &core.BudgetError{Kind: core.BudgetDailyCost, Limit: 5, Used: 5}
	if err := b.CheckDaily(ctx, "usr_a"); !errors.As(err, &be) || be.Kind != core.BudgetDailyCost {
		t.Fatalf("CheckDaily = %v", err)
	}
	if err := m.BeforeCall(ctx, 1, core.ModelInfo{}); err == nil {
		t.Fatal("BeforeCall ignored Deny")
	}
	if ok, _ := b.AllowSender("telegram:1"); !ok {
		t.Fatal("a daily-cost Deny must not rate-limit senders")
	}
	b.Deny = &core.BudgetError{Kind: core.BudgetRate}
	if ok, after := b.AllowSender("telegram:1"); ok || after <= 0 {
		t.Fatal("rate Deny not applied")
	}
	if b.CheckDaily(ctx, "usr_a") != nil || len(b.Senders) != 2 {
		t.Fatal("a rate Deny must not fail CheckDaily")
	}
	if l := b.DefaultLimits(core.TrigMessage); l.MaxSteps != 25 || l.MaxTokens != 400_000 || l.MaxTime != 10*time.Minute {
		t.Fatalf("interactive defaults = %+v", l)
	}
	if l := b.DefaultLimits(core.TrigHeartbeat); l.MaxSteps != 15 || l.MaxCost != core.MicrosFromUSD(0.25) {
		t.Fatalf("scheduled defaults = %+v", l)
	}
}

func TestBudgetDeadlinePause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &fakes.Budget{}
		m := b.StartRun("run_1", "usr_a", core.RunLimits{MaxTime: 10 * time.Minute})
		start := time.Now()
		if !m.Deadline().Equal(start.Add(10 * time.Minute)) {
			t.Fatalf("Deadline = %v", m.Deadline())
		}
		time.Sleep(time.Minute)
		m.Pause()
		time.Sleep(5 * time.Minute)
		m.Resume()
		if got := m.Deadline(); !got.Equal(start.Add(15 * time.Minute)) {
			t.Fatalf("Deadline after a 5m pause = %v", got.Sub(start))
		}
		if got := m.Totals().Active; got != time.Minute {
			t.Fatalf("Active = %v", got)
		}
		if got := m.Remaining().MaxTime; got != 9*time.Minute {
			t.Fatalf("Remaining time = %v", got)
		}
		if !b.StartRun("run_2", "usr_a", core.RunLimits{}).Deadline().IsZero() {
			t.Fatal("unlimited run has a deadline")
		}
	})
}

type tool struct {
	spec    core.ToolSpec
	enabled *bool
}

func (t tool) Spec() core.ToolSpec { return t.spec }
func (t tool) Plan(context.Context, *core.ToolEnv, json.RawMessage) (*core.Action, error) {
	return nil, core.ErrNotImplemented
}
func (t tool) Exec(context.Context, *core.ToolEnv, *core.Action) (*core.ToolResult, error) {
	return nil, core.ErrNotImplemented
}

type gatedTool struct{ tool }

func (g gatedTool) Enabled(*core.ToolEnv) bool { return *g.enabled }

func mk(name, desc, source string, m core.Manifest) tool {
	if m.SideEffect == 0 {
		m.SideEffect = core.SERead
	}
	return tool{spec: core.ToolSpec{Def: core.ToolDef{Name: name, Description: desc}, Source: source, Manifest: m}}
}

func names(specs []core.ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Def.Name
	}
	return out
}

func TestRegistry(t *testing.T) {
	on := true
	r := &fakes.Registry{}
	for _, tl := range []core.Tool{
		mk("shell", "Run a command in the sandbox", "builtin", core.Manifest{Exec: true, SideEffect: core.SELocalWrite}),
		mk("fs_read", "Read a file", "builtin", core.Manifest{SpaceOK: true}),
		mk("schedule_create", "Create a schedule", "builtin", core.Manifest{Triggers: []core.Trigger{core.TrigMessage}, SideEffect: core.SEAdmin}),
		mk("mcp__docs__search", "Search docs", "mcp:docs", core.Manifest{}),
		gatedTool{tool{spec: mk("skill_load", "Load a skill", "builtin", core.Manifest{SpaceOK: true}).spec, enabled: &on}},
	} {
		if err := r.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(mk("shell", "", "", core.Manifest{})); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("duplicate Register = %v", err)
	}
	if err := r.Register(tool{spec: core.ToolSpec{Def: core.ToolDef{Name: "bad"}}}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("invalid Register = %v", err)
	}

	user := &core.ToolEnv{Trigger: core.TrigMessage, Principal: core.Principal{Kind: core.PrincipalUser}}
	all := []string{"fs_read", "mcp__docs__search", "schedule_create", "shell", "skill_load"}
	if got := names(r.ForRun(user, core.Selection{SandboxOK: true})); !slices.Equal(got, all) {
		t.Fatalf("ForRun = %v", got)
	}
	if got := names(r.ForRun(user, core.Selection{})); slices.Contains(got, "shell") {
		t.Fatalf("exec tool without sandbox: %v", got)
	}
	sched := &core.ToolEnv{Trigger: core.TrigSchedule}
	if got := names(r.ForRun(sched, core.Selection{SandboxOK: true})); slices.Contains(got, "schedule_create") {
		t.Fatalf("schedule tool for a schedule trigger: %v", got)
	}
	space := &core.ToolEnv{Trigger: core.TrigMessage, Principal: core.Principal{Kind: core.PrincipalSpace, ToolProfile: "restricted"}}
	if got := names(r.ForRun(space, core.Selection{SandboxOK: true})); !slices.Equal(got, []string{"fs_read", "skill_load"}) {
		t.Fatalf("restricted space ForRun = %v", got)
	}
	on = false
	if r.Available("skill_load", user, core.Selection{}) || !r.Available("fs_read", nil, core.Selection{}) || r.Available("nope", user, core.Selection{}) {
		t.Fatal("Available")
	}
	if got := names(r.Search("SEARCH", user, core.Selection{}, 0)); !slices.Equal(got, []string{"mcp__docs__search"}) {
		t.Fatalf("Search = %v", got)
	}
	if got := r.Search("a", user, core.Selection{SandboxOK: true}, 2); len(got) != 2 {
		t.Fatalf("Search limit = %v", names(got))
	}
	r.UnregisterSource("mcp:docs")
	if _, ok := r.Get("mcp__docs__search"); ok || len(r.All()) != 4 || r.All()[0].Def.Name != "fs_read" {
		t.Fatalf("after UnregisterSource: %v", names(r.All()))
	}
}

func TestMemory(t *testing.T) {
	const p = core.PrincipalID("usr_a")
	m := &fakes.Memory{Files: map[core.PrincipalID]map[core.MemoryFile]string{p: {core.FileUser: "Name: Ada"}}}
	snap, _ := m.Snapshot(ctx, p)
	if !snap.Private || snap.User != "Name: Ada" || snap.LastID != 0 {
		t.Fatalf("Snapshot = %+v", snap)
	}
	if s, _ := m.Snapshot(ctx, "usr_empty"); s.Private {
		t.Fatal("empty memory is private")
	}

	e1, err := m.Apply(ctx, p, "run_1", core.MemoryOp{File: core.FileMemory, Op: "add", New: "Prefers tea"}, core.ProvUser)
	if err != nil || e1.ID != 1 || e1.After != "Prefers tea" || e1.Actor != "agent:run_1" {
		t.Fatalf("add = %+v, %v", e1, err)
	}
	_, _ = m.Apply(ctx, p, "run_1", core.MemoryOp{File: core.FileMemory, Op: "add", New: "Lives in Leith"}, core.ProvUser)
	_, _ = m.Apply(ctx, p, "run_1", core.MemoryOp{File: core.FileMemory, Op: "replace", Old: "Prefers tea", New: "Prefers coffee"}, core.ProvUser)
	if _, err := m.Apply(ctx, p, "run_1", core.MemoryOp{File: core.FileMemory, Op: "remove", Old: "nope"}, core.ProvUser); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("remove missing = %v", err)
	}
	if _, err := m.Apply(ctx, p, "run_1", core.MemoryOp{File: core.FileMemory, Op: "remove", Old: "Prefers coffee"}, core.ProvUser); err != nil {
		t.Fatal(err)
	}
	v := core.Viewer{Principals: []core.PrincipalID{p}}
	content, tag, _ := m.Read(ctx, v, p, core.FileMemory)
	if content != "Lives in Leith" {
		t.Fatalf("content = %q", content)
	}
	delta, last, _ := m.DeltasSince(ctx, p, 1)
	want := "MEMORY add: Lives in Leith\nMEMORY replace: Prefers coffee\nLives in Leith\nMEMORY remove: Prefers coffee\nLives in Leith\n"
	if last != 4 || delta != want {
		t.Fatalf("DeltasSince = %d %q", last, delta)
	}
	if d, l, _ := m.DeltasSince(ctx, p, 4); d != "" || l != 4 {
		t.Fatalf("no deltas = %q %d", d, l)
	}

	newTag, err := m.Write(ctx, v, p, core.FileMemory, "edited", tag, "usr_a")
	if err != nil || newTag == tag {
		t.Fatalf("Write = %q, %v", newTag, err)
	}
	if _, err := m.Write(ctx, v, p, core.FileMemory, "stale", tag, "usr_a"); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale Write = %v", err)
	}
	if h, _ := m.History(ctx, v, p, core.FileMemory, core.Page{Limit: 2}); len(h) != 2 || h[0].Op != "edit" || h[0].ID != 5 {
		t.Fatalf("History = %+v", h)
	}
	if h, _ := m.History(ctx, v, p, core.FileMemory, core.Page{Before: 2}); len(h) != 1 || h[0].ID != 1 {
		t.Fatalf("History before = %+v", h)
	}
	if _, _, err := m.Read(ctx, core.Viewer{Principals: []core.PrincipalID{"usr_b"}}, p, core.FileMemory); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign Read = %v", err)
	}

	space := core.PrincipalID(core.NewID(core.PrefixSpace))
	if s, err := m.Snapshot(ctx, space); err != nil || s != (core.MemorySnapshot{}) {
		t.Fatalf("space Snapshot = %+v", s)
	}
	if _, err := m.Apply(ctx, space, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: "x"}, core.ProvUser); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("space Apply = %v", err)
	}
	if _, _, err := m.Read(ctx, core.SystemViewer(), space, core.FileMemory); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("space Read = %v", err)
	}
}

func TestSkills(t *testing.T) {
	s := &fakes.Skills{
		Metas: []core.SkillMeta{
			{Name: "pdf", Status: core.SkillEnabled, Hash: "h1", ApprovedHash: "h1"},
			{Name: "drift", Status: core.SkillEnabled, Hash: "h2", ApprovedHash: "old"},
			{Name: "new", Status: core.SkillPending, Hash: "h3"},
		},
		Bodies: map[string]string{"pdf": "# PDF"},
		Files:  map[string]map[string][]byte{"pdf": {"scripts/x.py": []byte("print()")}},
	}
	if idx := s.Index(ctx); len(idx) != 1 || idx[0].Name != "pdf" {
		t.Fatalf("Index = %+v", idx)
	}
	body, files, err := s.Load(ctx, "pdf")
	if err != nil || body != "# PDF" || !slices.Equal(files, []string{"scripts/x.py"}) {
		t.Fatalf("Load = %q %v %v", body, files, err)
	}
	if _, _, err := s.Load(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Load missing = %v", err)
	}
	if b, err := s.ReadFile(ctx, "pdf", "scripts/x.py"); err != nil || string(b) != "print()" {
		t.Fatalf("ReadFile = %q %v", b, err)
	}
	if err := s.SetEnabled(ctx, "new", true, "usr_admin"); err != nil {
		t.Fatal(err)
	}
	if idx := s.Index(ctx); len(idx) != 2 || idx[0].Name != "new" || idx[0].ApprovedBy != "usr_admin" {
		t.Fatalf("Index after enable = %+v", idx)
	}
	if err := s.Remove(ctx, "pdf", "usr_admin"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(ctx); len(l) != 2 || l[0].Name != "drift" {
		t.Fatalf("List = %+v", l)
	}
	if err := s.SetEnabled(ctx, "pdf", true, ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("SetEnabled removed = %v", err)
	}
}

func TestMCPAndScheduler(t *testing.T) {
	m := &fakes.MCP{StatusV: []core.MCPStatus{{Name: "docs", State: "ready"}}}
	if m.Start(ctx) != nil || len(m.Status()) != 1 || m.Status()[0].State != "ready" || m.Close() != nil {
		t.Fatal("MCP")
	}

	s := &fakes.Scheduler{}
	va := core.Viewer{Principals: []core.PrincipalID{"usr_a"}}
	vb := core.Viewer{Principals: []core.PrincipalID{"usr_b"}}
	sc := &core.Schedule{Principal: "usr_a", Name: "digest", Kind: core.SchedCron, Spec: "0 8 * * *"}
	if err := s.Upsert(ctx, va, sc, "user:usr_a"); err != nil || !core.ValidID(core.PrefixSchedule, string(sc.ID)) || s.Schedules[sc.ID].CreatedBy != "user:usr_a" {
		t.Fatalf("Upsert = %+v, %v", sc, err)
	}
	if err := s.Upsert(ctx, vb, &core.Schedule{ID: sc.ID, Principal: "usr_b"}, "user:usr_b"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("hijacking Upsert = %v", err)
	}
	if _, err := s.RunNow(ctx, vb, sc.ID, "usr_b"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign RunNow = %v", err)
	}
	if run, err := s.RunNow(ctx, va, sc.ID, "usr_a"); err != nil || run == "" || len(s.RunNows) != 1 {
		t.Fatalf("RunNow = %q, %v", run, err)
	}
	if err := s.Delete(ctx, vb, sc.ID, "usr_b"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign Delete = %v", err)
	}
	if err := s.Delete(ctx, va, sc.ID, "usr_a"); err != nil || len(s.Schedules) != 0 {
		t.Fatalf("Delete = %v", err)
	}
	s.SetPaused(true)
	if !s.PausedV {
		t.Fatal("SetPaused")
	}

	at, err := s.Preview(core.SchedOnce, "2026-10-12T09:30:00+01:00", "Europe/London", 3)
	if err != nil || len(at) != 1 || at[0].UTC().Hour() != 8 {
		t.Fatalf("Preview once = %v, %v", at, err)
	}
	if hb, err := s.Preview(core.SchedHeartbeat, "30m", "", 3); err != nil || len(hb) != 3 || hb[1].Sub(hb[0]) != 30*time.Minute {
		t.Fatalf("Preview heartbeat = %v, %v", hb, err)
	}
	if _, err := s.Preview(core.SchedCron, "* * * * *", "", 1); !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("Preview cron = %v", err)
	}
}
