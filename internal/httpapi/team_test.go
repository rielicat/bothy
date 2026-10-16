package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// teammateFixture records Maya Chen, a teammate on Slack, and the #eng
// channel, each with a schedule.
type teammateFixture struct {
	maya, eng           core.PrincipalID
	mayaSched, engSched core.ScheduleID
}

func seedTeam(t *testing.T, h *harness) teammateFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	var f teammateFixture
	h.touch("slack", "U1", "Maya Chen", core.MemberUnchecked)
	if _, err := h.auth.Admit(ctx, core.ChannelIdentity{Channel: "slack", ExternalID: "U1", DisplayName: "Maya Chen"},
		core.Membership{Status: core.MemberYes}); err != nil {
		t.Fatal(err)
	}
	id, _ := h.store.Identities().Get(ctx, "slack", "U1")
	f.maya = id.Principal
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, Title: "#eng",
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, &core.Principal{ToolProfile: "restricted", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f.eng = sp.Principal
	for _, sc := range []*core.Schedule{
		{Principal: f.maya, Name: "Standup notes", Kind: core.SchedCron, Spec: "0 9 * * 1-5", TZ: "UTC", Prompt: "x"},
		{Principal: f.eng, Name: "PR digest", Kind: core.SchedCron, Spec: "0 8 * * 1", TZ: "UTC", Prompt: "y"},
	} {
		sc.ID = core.ScheduleID(core.NewID(core.PrefixSchedule))
		sc.Slot, sc.MissedRun, sc.Enabled, sc.CreatedBy, sc.CreatedAt, sc.UpdatedAt = core.SlotMain, "skip", true, "run:x", now, now
		if err := h.store.Schedules().Upsert(ctx, sc); err != nil {
			t.Fatal(err)
		}
		if sc.Principal == f.maya {
			f.mayaSched = sc.ID
		} else {
			f.engSched = sc.ID
		}
	}
	return f
}

func TestTeamSchedules(t *testing.T) {
	h := newHarness(t).setup()
	h.useStoreScheduler()
	f := seedTeam(t, h)
	expect(t, h.do("GET", "/api/team/schedules", nil, h.member), http.StatusForbidden)
	list := decodeAs[[]dto.TeamSchedule](t, h.do("GET", "/api/team/schedules", nil, h.admin))
	got := map[core.ScheduleID]dto.TeamSchedule{}
	for _, sc := range list {
		got[sc.ID] = sc
	}
	if m := got[f.mayaSched]; len(list) != 2 || m.Owner != "Maya Chen" || m.OwnerKind != "teammate" || m.App != "slack" || !m.Enabled {
		t.Fatalf("team schedules = %+v", list)
	}
	if e := got[f.engSched]; e.Owner != "#eng" || e.OwnerKind != "channel" || e.Name != "PR digest" {
		t.Fatalf("channel schedule = %+v", e)
	}
	// Members don't see them at all; admins only turn them on or off.
	expect(t, h.do("PATCH", "/api/schedules/"+string(f.mayaSched), map[string]any{"enabled": false}, h.member), http.StatusNotFound)
	rec := h.do("PATCH", "/api/schedules/"+string(f.mayaSched), map[string]any{"enabled": false, "prompt": "do evil"}, h.admin)
	expect(t, rec, http.StatusBadRequest)
	if msg := errMessage(t, rec); msg != "Only turning it on or off can be changed here." {
		t.Fatalf("refusal = %q", msg)
	}
	rec = h.do("PATCH", "/api/schedules/"+string(f.mayaSched), map[string]any{"enabled": false}, h.admin)
	expect(t, rec, http.StatusOK)
	if sc := decodeAs[core.Schedule](t, rec); sc.Enabled || sc.Prompt != "x" {
		t.Fatalf("turned off = %+v", sc)
	}
	expect(t, h.do("DELETE", "/api/schedules/"+string(f.engSched), nil, h.member), http.StatusNotFound)
	expect(t, h.do("DELETE", "/api/schedules/"+string(f.engSched), nil, h.admin), http.StatusNoContent)
	if list := decodeAs[[]dto.TeamSchedule](t, h.do("GET", "/api/team/schedules", nil, h.admin)); len(list) != 1 {
		t.Fatalf("after delete = %+v", list)
	}
}

func TestTeamAuditPhrases(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	f := seedTeam(t, h)
	now := time.Now()
	detail := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	admin := "user:" + string(h.admin.user)
	maya := "channel:slack:U1"
	entries := []core.AuditEntry{
		{Actor: maya, Action: "identity.teammate", Target: maya, Outcome: "ok", Detail: detail(map[string]any{"via": "membership"})},
		{Actor: admin, Action: "identity.let_in", Target: maya, Outcome: "ok"},
		{Actor: admin, Action: "identity.unblock", Target: maya, Outcome: "ok"},
		{Actor: admin, Action: "identity.block", Target: maya, Outcome: "ok"},
		{Actor: admin, Action: "identity.approver", Target: maya, Outcome: "ok", Detail: detail(map[string]any{"can_approve": true})},
		{Actor: "system", Action: "identity.approver", Target: "channel:slack:U2", Outcome: "ok",
			Detail: detail(map[string]any{"can_approve": false, "via": "membership"})},
		{Actor: admin, Action: "identity.forget", Target: "channel:slack:U3", Outcome: "ok"},
		{Actor: admin, Action: "space.update", Target: "space:" + string(f.eng), Outcome: "ok", Detail: detail(map[string]any{"enabled": true})},
		{Actor: admin, Action: "space.update", Target: "space:" + string(f.eng), Outcome: "error", Detail: detail(map[string]any{"enabled": false})},
		{Actor: admin, Action: "space.update", Target: "space:spc_gone", Outcome: "ok"},
		{Actor: "agent:run_x", Action: "tool.read", Target: string(f.maya), Outcome: "ok",
			Detail: detail(map[string]any{"tool": "web_fetch", "target": "example.com/docs"})},
		{Actor: "system", Action: "migration.team_access", Target: "people", Outcome: "ok",
			Detail: detail(map[string]any{"linked": 3, "guests": 1, "approvers": 2})},
	}
	for i := range entries {
		e := entries[i]
		e.At = now.Add(time.Duration(i) * time.Second)
		if len(e.Detail) == 0 {
			e.Detail = json.RawMessage("{}")
		}
		if _, err := h.store.Audit().Append(ctx, &e, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	rows := decodeAs[dto.List[dto.AuditRow]](t, h.do("GET", "/api/audit?limit=100", nil, h.admin))
	var got [][2]string
	for i := len(rows.Items) - 1; i >= 0; i-- {
		got = append(got, [2]string{rows.Items[i].ActorName, rows.Items[i].Summary})
	}
	want := [][2]string{
		{"Maya Chen", "Started using Bothy on Slack"},
		{"admin", "Let Maya Chen on Slack in"},
		{"admin", "Unblocked Maya Chen on Slack"},
		{"admin", "Blocked Maya Chen on Slack"},
		{"admin", "Let Maya Chen on Slack approve requests"},
		{"Bothy", "Someone on Slack can't approve any more: they're no longer in the team"},
		{"admin", "Deleted the data of someone on Slack"},
		{"admin", "Turned on Bothy in #eng"},
		{"admin", "Turned off Bothy in #eng"},
		{"admin", "Changed a channel"},
		{"Bothy", "Read example.com/docs with web_fetch for Maya Chen · Slack"},
		{"Bothy", "Moved people in chat apps off linked accounts: 3 accounts, 1 guest, 2 approvers"},
	}
	if len(got) < len(want) {
		t.Fatalf("rows = %q", got)
	}
	got = got[len(got)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A chat approver's decision names them with their app.
func TestDecidedByChatApprover(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	f := seedTeam(t, h)
	now := time.Now()
	sess := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: f.eng, Origin: "slack", CreatedAt: now, UpdatedAt: now}
	if err := h.store.Sessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: f.eng, Trigger: core.TrigMessage,
		Status: core.RunDone, CreatedAt: now}
	if err := h.store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	a := &core.Approval{ID: core.ApprovalID(core.NewID(core.PrefixApproval)), Run: run.ID,
		Session: sess.ID, Principal: f.eng, ToolCallID: "c1",
		Action:   &core.Action{Tool: "fs_delete", Kind: core.ActFSDelete, SideEffect: core.SEIrreversible, Paths: []string{"a.md"}},
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{Team: true}, Choices: []core.Choice{core.AllowOnce, core.Deny}},
		Card:     core.Card{Title: "Delete a.md", Risk: core.SEIrreversible, Choices: []core.Choice{core.AllowOnce, core.Deny}},
		Status:   core.ApprovalPending, Approver: core.ApproverReq{Team: true}, OnTimeout: core.TimeoutDeny, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := h.store.Approvals().Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if ok, err := h.store.Approvals().Decide(ctx, a.ID, []core.ApprovalStatus{core.ApprovalPending}, core.ApprovalAllowed, core.AllowOnce,
		core.Decider{Identity: "slack:U1"}, "slack", now); err != nil || !ok {
		t.Fatalf("decide: %v %v", ok, err)
	}
	got, err := h.store.Approvals().Get(ctx, core.SystemViewer(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.srv.namer(ctx, core.SystemViewer()).nameApprovals(got)
	if got.DecidedByIdentity != "slack:U1" || got.DecidedByName != "Maya Chen · Slack" {
		t.Fatalf("decided = %q %q", got.DecidedByIdentity, got.DecidedByName)
	}
}
