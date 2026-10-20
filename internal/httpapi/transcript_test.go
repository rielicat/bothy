package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// transcriptFixture stores a chat whose run made tool calls in every state.
type transcriptFixture struct {
	h    *harness
	ctx  context.Context
	sess *core.Session
	run  *core.Run
	step int
	asks int // approvals stored, each a millisecond after the last
}

func newTranscript(t *testing.T, h *harness, status core.RunStatus) *transcriptFixture {
	t.Helper()
	ctx := context.Background()
	mp := core.UserPrincipal(h.member.user)
	sess := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: mp, Origin: "web", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := h.store.Sessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: mp, User: h.member.user,
		Trigger: core.TrigMessage, Status: status, CreatedAt: time.Now()}
	if err := h.store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	f := &transcriptFixture{h: h, ctx: ctx, sess: sess, run: run}
	f.message(core.MsgUser, core.ProvUser, core.Part{Type: core.PartText, Text: "Clean up the staging notes"})
	return f
}

func (f *transcriptFixture) message(role core.MsgRole, prov core.Provenance, parts ...core.Part) {
	f.h.t.Helper()
	m := &core.Message{Session: f.sess.ID, Run: f.run.ID, Role: role, Kind: core.KindNormal, Provenance: prov, Parts: parts,
		Sender: "web:" + string(f.h.member.user), CreatedAt: time.Now()}
	if err := f.h.store.Messages().Append(f.ctx, m); err != nil {
		f.h.t.Fatal(err)
	}
}

// call stores a tool call, its record and optionally its result.
func (f *transcriptFixture) call(id, tool, path string, st core.ToolCallStatus, verdict core.Verdict, ap *core.Approval, res *core.ToolResult) {
	f.h.t.Helper()
	f.step++
	args, _ := json.Marshal(map[string]string{"path": path})
	f.message(core.MsgAssistant, core.ProvSystem, core.Part{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: id, Name: tool, Args: args}})
	rec := &core.ToolCallRecord{Run: f.run.ID, Session: f.sess.ID, Principal: f.run.Principal, Step: f.step, CallID: id, Tool: tool,
		Source: "builtin", SideEffect: core.SELocalWrite, Verdict: verdict, Status: st, CreatedAt: time.Now(),
		Action: &core.Action{Tool: tool, Kind: core.ActFSDelete, SideEffect: core.SEIrreversible, Paths: []string{path}, Payload: args}}
	if ap != nil {
		rec.Approval = ap.ID
	}
	if err := f.h.store.Runs().AddToolCall(f.ctx, rec); err != nil {
		f.h.t.Fatal(err)
	}
	if res != nil {
		res.CallID, res.Name = id, tool
		f.message(core.MsgTool, core.ProvTool, core.Part{Type: core.PartToolResult, ToolResult: res})
	}
}

// approval stores a pending approval, for the tool call record named by
// toolCallID when one is given.
func (f *transcriptFixture) approval(path string, toolCallID ...string) *core.Approval {
	f.h.t.Helper()
	payload := json.RawMessage(`{"path":"` + path + `"}`)
	f.asks++
	now := time.Now().Add(time.Duration(f.asks) * time.Millisecond)
	tc := core.NewID(core.PrefixToolCall)
	if len(toolCallID) > 0 {
		tc = toolCallID[0]
	}
	a := &core.Approval{Run: f.run.ID, Session: f.sess.ID, Principal: f.run.Principal, ToolCallID: tc,
		Action: &core.Action{Tool: "fs_delete", Kind: core.ActFSDelete, SideEffect: core.SEIrreversible, Paths: []string{path},
			Payload: payload, Fingerprint: core.ActionFingerprint("fs_delete", payload)},
		Decision: core.Decision{Verdict: core.VerdictAsk, Rule: "default:irreversible", Approver: core.ApproverReq{User: f.h.member.user},
			Timeout: 15 * time.Minute, OnTimeout: core.TimeoutDeny, Choices: []core.Choice{core.AllowOnce, core.Deny}},
		Card:   core.Card{Title: "Delete " + path, Risk: core.SEIrreversible, Choices: []core.Choice{core.AllowOnce, core.Deny}, Expires: now.Add(time.Hour)},
		Status: core.ApprovalPending, Approver: core.ApproverReq{User: f.h.member.user}, OnTimeout: core.TimeoutDeny,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := f.h.store.Approvals().Create(f.ctx, a); err != nil {
		f.h.t.Fatal(err)
	}
	return a
}

func (f *transcriptFixture) decide(a *core.Approval, to core.ApprovalStatus, c core.Choice, by core.UserID) {
	f.h.t.Helper()
	if ok, err := f.h.store.Approvals().Decide(f.ctx, a.ID, []core.ApprovalStatus{core.ApprovalPending}, to, c, core.Decider{User: by}, "slack", time.Now()); err != nil || !ok {
		f.h.t.Fatalf("decide %s: %v %v", a.ID, ok, err)
	}
}

func (f *transcriptFixture) page(t *testing.T) dto.MessagePage {
	t.Helper()
	return decodeAs[dto.MessagePage](t, f.h.do("GET", "/api/sessions/"+string(f.sess.ID)+"/messages", nil, f.h.member))
}

func toolParts(p dto.MessagePage) map[string]core.Part {
	out := map[string]core.Part{}
	for _, m := range p.Messages {
		for _, part := range m.Parts {
			if part.Type == core.PartToolCall {
				out[part.ToolCall.ID] = part
			}
		}
	}
	return out
}

func TestTranscriptToolStates(t *testing.T) {
	h := newHarness(t).setup()
	f := newTranscript(t, h, core.RunWaitingApproval)
	approved := f.approval("notes/old.md")
	f.decide(approved, core.ApprovalAllowed, core.AllowOnce, h.owner.user)
	denied := f.approval("notes/keep.md")
	f.decide(denied, core.ApprovalDenied, core.Deny, h.member.user)
	expired := f.approval("notes/late.md")
	f.decide(expired, core.ApprovalExpired, "", "")
	pending := f.approval("notes/staging.md")

	f.call("c_done", "fs_read", "a.md", core.TCOK, core.VerdictAuto, nil, &core.ToolResult{Content: "hi"})
	f.call("c_failed", "fs_read", "b.md", core.TCError, core.VerdictAuto, nil, &core.ToolResult{Content: "no such file", IsError: true})
	f.call("c_blocked", "fs_delete", "/etc/x", core.TCDenied, core.VerdictDeny, nil, &core.ToolResult{Content: "Denied by policy", IsError: true})
	f.call("c_approved", "fs_delete", "notes/old.md", core.TCOK, core.VerdictAsk, approved, &core.ToolResult{Content: "deleted"})
	f.call("c_denied", "fs_delete", "notes/keep.md", core.TCDenied, core.VerdictAsk, denied, &core.ToolResult{Content: "The user denied this action.", IsError: true})
	f.call("c_expired", "fs_delete", "notes/late.md", core.TCDenied, core.VerdictAsk, expired, &core.ToolResult{Content: "Approval expired (denied).", IsError: true})
	f.call("c_running", "fs_read", "c.md", core.TCRunning, core.VerdictAuto, nil, nil)
	f.call("c_waiting", "fs_delete", "notes/staging.md", core.TCWaiting, core.VerdictAsk, pending, nil)

	p := f.page(t)
	parts := toolParts(p)
	want := map[string]core.ToolState{"c_done": core.ToolDone, "c_failed": core.ToolFailed, "c_blocked": core.ToolBlocked,
		"c_approved": core.ToolDone, "c_denied": core.ToolDenied, "c_expired": core.ToolExpired, "c_running": core.ToolRunning,
		"c_waiting": core.ToolWaitingApproval}
	for id, st := range want {
		if parts[id].State != st {
			t.Errorf("%s state = %q, want %q", id, parts[id].State, st)
		}
	}
	if parts["c_waiting"].Approval != pending.ID || parts["c_approved"].Approval != approved.ID || parts["c_done"].Approval != "" {
		t.Fatalf("approval ids on parts: %+v", parts)
	}
	if len(p.Approvals) != 4 {
		t.Fatalf("page approvals = %d, want the 4 the calls asked for, decided ones included", len(p.Approvals))
	}
	byID := map[core.ApprovalID]*core.Approval{}
	for _, ap := range p.Approvals {
		byID[ap.ID] = ap
	}
	if a := byID[approved.ID]; a == nil || a.Status != core.ApprovalAllowed || a.DecidedByName != "owner" || a.DecidedVia != "slack" {
		t.Fatalf("decided approval after reload = %+v", a)
	}
	if a := byID[pending.ID]; a == nil || a.Status != core.ApprovalPending || a.DecidedByName != "" {
		t.Fatalf("pending approval = %+v", a)
	}

	// The run ends without the running call finishing: it never completed.
	f.run.Status = core.RunInterrupted
	if err := h.store.Runs().Update(f.ctx, f.run); err != nil {
		t.Fatal(err)
	}
	if st := toolParts(f.page(t))["c_running"].State; st != core.ToolFailed {
		t.Fatalf("an unfinished call of an ended run = %q", st)
	}
	stored, _ := h.store.Messages().Range(f.ctx, core.SystemViewer(), f.sess.ID, 0, 0)
	for _, m := range stored {
		for _, part := range m.Parts {
			if part.State != "" || part.Approval != "" {
				t.Fatal("tool states are never stored")
			}
		}
	}
}

func TestToolStateNeverDoneBeforeTheResult(t *testing.T) {
	ap := &core.Approval{Status: core.ApprovalPending}
	rec := &core.ToolCallRecord{Status: core.TCWaiting, Approval: "apr_x"}
	if st := toolState(rec, ap, nil, true); st != core.ToolWaitingApproval {
		t.Fatalf("pending fs_delete = %q", st)
	}
	ap.Status = core.ApprovalAllowed
	rec.Status = core.TCApproved
	if st := toolState(rec, ap, nil, true); st != core.ToolRunning {
		t.Fatalf("approved, no result = %q", st)
	}
	rec.Status = core.TCRunning
	if st := toolState(rec, ap, &core.ToolResult{Content: "deleted"}, true); st != core.ToolDone {
		t.Fatalf("result stored before the record = %q", st)
	}
	parked := &core.Approval{Status: core.ApprovalParked}
	if st := toolState(&core.ToolCallRecord{Status: core.TCWaiting}, parked, &core.ToolResult{Content: "Parked: waiting"}, true); st != core.ToolWaitingApproval {
		t.Fatalf("parked call with its placeholder result = %q", st)
	}
	if st := toolState(&core.ToolCallRecord{Status: core.TCDenied, Verdict: core.VerdictAsk}, nil, nil, false); st != core.ToolBlocked {
		t.Fatalf("refused without a card (API run) = %q", st)
	}
	cancelled := &core.Approval{Status: core.ApprovalCancelled}
	if st := toolState(&core.ToolCallRecord{Status: core.TCDenied}, cancelled, nil, false); st != core.ToolExpired {
		t.Fatalf("cancelled when the chat stopped = %q", st)
	}
	// Approved while the record still says waiting (the run picks the
	// decision up, or a parked run resumes): no longer waiting, not done.
	allowed := &core.Approval{Status: core.ApprovalAllowed}
	parkedRes := &core.ToolResult{Content: "Parked: waiting"}
	if st := toolState(&core.ToolCallRecord{Status: core.TCWaiting}, allowed, parkedRes, true); st != core.ToolRunning {
		t.Fatalf("approved, not run yet = %q", st)
	}
	if st := toolState(&core.ToolCallRecord{Status: core.TCWaiting}, allowed, parkedRes, false); st != core.ToolExpired {
		t.Fatalf("approved after the task ended = %q", st)
	}
	if st := toolState(&core.ToolCallRecord{Status: core.TCPlanned}, nil, nil, false); st != core.ToolExpired {
		t.Fatalf("planned when the task stopped = %q", st)
	}
}

// An exec whose connection the egress proxy held for approval waits on that
// card, and the decided card stays in the transcript after a reload.
func TestTranscriptKeepsHeldConnectionCards(t *testing.T) {
	h := newHarness(t).setup()
	f := newTranscript(t, h, core.RunRunning)
	f.call("c_shell", "shell", "", core.TCRunning, core.VerdictAuto, nil, nil)
	recs, err := h.store.Runs().ToolCalls(f.ctx, core.SystemViewer(), f.run.ID)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %v %v", recs, err)
	}
	first := f.approval("registry.npmjs.org", recs[0].ID+"#net")
	second := f.approval("example.com", recs[0].ID+"#net")
	f.decide(first, core.ApprovalAllowed, core.AllowOnce, h.member.user)

	p := f.page(t)
	part := toolParts(p)["c_shell"]
	if part.State != core.ToolWaitingApproval || part.Approval != second.ID || len(p.Approvals) != 2 {
		t.Fatalf("held exec = %q on %q, page approvals %d; want waiting on %s with both cards", part.State, part.Approval, len(p.Approvals), second.ID)
	}
	f.decide(second, core.ApprovalDenied, core.Deny, h.member.user)
	p = f.page(t)
	part = toolParts(p)["c_shell"]
	if part.State != core.ToolRunning || part.Approval != second.ID || len(p.Approvals) != 2 ||
		p.Approvals[0].ID != first.ID || p.Approvals[1].Status != core.ApprovalDenied || p.Approvals[1].DecidedByName != "member" {
		t.Fatalf("after both decisions: %q on %q, approvals %+v", part.State, part.Approval, p.Approvals)
	}
}

func TestCreateSessionWithFirstMessage(t *testing.T) {
	h := newHarness(t).setup()
	rec := h.do("POST", "/api/sessions", dto.SessionCreateReq{Message: &dto.SendReq{Text: "Draft a release checklist"}}, h.member)
	expect(t, rec, http.StatusAccepted)
	got := decodeAs[dto.SessionCreated](t, rec)
	subs := h.runner.Submitted()
	if got.ID == "" || got.Run == nil || got.Run.Run == "" || len(subs) != 1 || subs[0].Session != got.ID ||
		subs[0].Input[0].Text != "Draft a release checklist" {
		t.Fatalf("created %+v, submitted %+v", got, subs)
	}

	// No message: the plain session as before, never listed while empty.
	plain := decodeAs[dto.SessionCreated](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	if plain.Run != nil || plain.ID == "" {
		t.Fatalf("plain create = %+v", plain)
	}
	if l := decodeAs[dto.List[core.Session]](t, h.do("GET", "/api/sessions", nil, h.member)); len(l.Items) != 0 {
		t.Fatalf("sessions without messages are listed: %+v", l.Items)
	}

	// A refused first message leaves no chat to list.
	h.runner.Err = core.ErrPaused
	rec = h.do("POST", "/api/sessions", dto.SessionCreateReq{Message: &dto.SendReq{Text: "hello"}}, h.member)
	if errCode(t, rec) != "paused" {
		t.Fatalf("paused = %d %s", rec.Code, rec.Body.String())
	}
	h.runner.Err = nil
	archived := decodeAs[dto.List[core.Session]](t, h.do("GET", "/api/sessions?archived=true", nil, h.member))
	all, _ := h.store.Sessions().List(context.Background(), core.SystemViewer(), core.SessionFilter{Archived: true})
	if len(archived.Items) != 0 || len(all) != 1 {
		t.Fatalf("refused first message: %d archived sessions, listed %+v", len(all), archived.Items)
	}

	// Validation happens before anything is stored.
	before, _ := h.store.Sessions().List(context.Background(), core.SystemViewer(), core.SessionFilter{})
	expect(t, h.do("POST", "/api/sessions", dto.SessionCreateReq{Message: &dto.SendReq{Text: "  "}}, h.member), http.StatusBadRequest)
	expect(t, h.do("POST", "/api/sessions", dto.SessionCreateReq{Message: &dto.SendReq{Uploads: []string{"upl_nope"}}}, h.member), http.StatusNotFound)
	after, _ := h.store.Sessions().List(context.Background(), core.SystemViewer(), core.SessionFilter{})
	if len(after) != len(before) {
		t.Fatal("an invalid first message stored a session")
	}
}

func TestUploadForANewChat(t *testing.T) {
	h := newHarness(t).setup()
	upload := func(field, value string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if field != "" {
			_ = mw.WriteField(field, value)
		}
		fw, _ := mw.CreateFormFile("file", "notes.txt")
		_, _ = fw.Write([]byte("meeting notes"))
		_ = mw.Close()
		return h.do("POST", "/api/uploads", buf.Bytes(), h.member, withHeader("Content-Type", mw.FormDataContentType()))
	}
	expect(t, upload("", ""), http.StatusBadRequest)
	expect(t, upload("principal_id", string(core.UserPrincipal(h.owner.user))), http.StatusNotFound)
	rec := upload("principal_id", string(core.UserPrincipal(h.member.user)))
	expect(t, rec, http.StatusOK)
	up := decodeAs[dto.Upload](t, rec)
	created := decodeAs[dto.SessionCreated](t, h.do("POST", "/api/sessions",
		dto.SessionCreateReq{Message: &dto.SendReq{Text: "Turn these into tasks", Uploads: []string{up.ID}}}, h.member))
	subs := h.runner.Submitted()
	if created.Run == nil || len(subs) != 1 || len(subs[0].Input) != 2 || subs[0].Input[1].Blob == nil || subs[0].Input[1].Blob.Name != "notes.txt" {
		t.Fatalf("first message with an attachment: %+v", subs)
	}
}

func TestSessionListNamesUntitledChats(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	mp := core.UserPrincipal(h.member.user)
	mk := func(title string, text string) *core.Session {
		s := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: mp, Origin: "web", Title: title,
			CreatedAt: time.Now(), UpdatedAt: time.Now(), LastMessageAt: time.Now()}
		if err := h.store.Sessions().Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		if text != "" {
			m := &core.Message{Session: s.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUser,
				Parts: []core.Part{{Type: core.PartText, Text: text}}, CreatedAt: time.Now()}
			if err := h.store.Messages().Append(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	legacy := mk("", "Can you summarise\nthe open pull requests in acme/web for me?")
	titled := mk("Release checklist", "Draft a release checklist")
	empty := mk("", "")
	l := decodeAs[dto.List[core.Session]](t, h.do("GET", "/api/sessions", nil, h.member))
	titles := map[core.SessionID]string{}
	for _, s := range l.Items {
		titles[s.ID] = s.Title
	}
	if _, listed := titles[empty.ID]; listed || len(titles) != 2 {
		t.Fatalf("listed %v", titles)
	}
	if got := titles[legacy.ID]; got != "Can you summarise the open pull requests in…" {
		t.Fatalf("untitled chat = %q", got)
	}
	if titles[titled.ID] != "Release checklist" {
		t.Fatalf("titled chat = %q", titles[titled.ID])
	}
	if d := decodeAs[dto.SessionDetail](t, h.do("GET", "/api/sessions/"+string(legacy.ID), nil, h.member)); d.Session.Title != titles[legacy.ID] {
		t.Fatalf("detail title %q differs from the list", d.Session.Title)
	}
	res := decodeAs[dto.SearchResp](t, h.do("GET", "/api/search?q=pull+requests", nil, h.member))
	if len(res.Chats) != 1 || res.Chats[0].Title != titles[legacy.ID] {
		t.Fatalf("search title %+v differs from the list", res.Chats)
	}
}
