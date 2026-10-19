package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// Another person's schedule keeps its name and timing out of the log, as
// their schedules list does; the reader's own schedules keep theirs.
func TestAuditKeepsOtherPeoplesScheduleNamesPrivate(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	row := func(owner core.UserID, id, name string) {
		d, _ := json.Marshal(map[string]any{"schedule_id": id, "principal_id": core.UserPrincipal(owner), "name": name, "kind": "cron", "spec": "0 6 * * 1", "tz": "UTC"})
		e := core.AuditEntry{At: time.Now(), Actor: "user:" + string(owner), Action: "schedule.create", Target: "schedule:" + id, Outcome: "ok", Detail: d}
		if _, err := h.store.Audit().Append(ctx, &e, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	row(h.member.user, "sch_1", "plan-for-max")
	row(h.admin.user, "sch_2", "admins-own")
	rows := decodeAs[dto.List[dto.AuditRow]](t, h.do("GET", "/api/audit?action=schedule.", nil, h.admin))
	got := map[string]dto.AuditRow{}
	for _, r := range rows.Items {
		got[r.Target] = r
	}
	theirs, mine := got["schedule:sch_1"], got["schedule:sch_2"]
	if theirs.Summary != "Created a schedule" || strings.Contains(string(theirs.Detail), "plan-for-max") || strings.Contains(string(theirs.Detail), "0 6") {
		t.Fatalf("another person's schedule row = %q %s", theirs.Summary, theirs.Detail)
	}
	if mine.Summary != "Created the schedule “admins-own”" {
		t.Fatalf("own schedule row = %q %s", mine.Summary, mine.Detail)
	}
	expect(t, h.do("GET", "/api/audit", nil, h.member), http.StatusForbidden)
}

// Rows the handler wrote before the scheduler wrote the one row name no
// principal; whoever made the change stands in for it.
func TestAuditKeepsOlderScheduleRowsPrivate(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	row := func(by core.UserID, id string) {
		e := core.AuditEntry{At: time.Now(), Actor: "user:" + string(by), Action: "schedule.create", Target: "schedule:" + id, Outcome: "ok",
			Detail: json.RawMessage(`{"name":"plan-for-max","kind":"cron","grants":0}`)}
		if _, err := h.store.Audit().Append(ctx, &e, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	row(h.member.user, "sch_old1")
	row(h.admin.user, "sch_old2")
	got := map[string]dto.AuditRow{}
	for _, r := range decodeAs[dto.List[dto.AuditRow]](t, h.do("GET", "/api/audit?action=schedule.", nil, h.admin)).Items {
		got[r.Target] = r
	}
	if theirs := got["schedule:sch_old1"]; theirs.Summary != "Created a schedule" || strings.Contains(string(theirs.Detail), "plan-for-max") {
		t.Fatalf("another person's older schedule row = %q %s", theirs.Summary, theirs.Detail)
	}
	if mine := got["schedule:sch_old2"]; mine.Summary != "Created the schedule “plan-for-max”" {
		t.Fatalf("own older schedule row = %q %s", mine.Summary, mine.Detail)
	}
}

// A deploy request one approver approved before it expired didn't go
// unanswered.
func TestAuditExpiryCountsApprovalsThatCameIn(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	now := time.Now()
	sess := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: core.UserPrincipal(h.member.user), Origin: "web", CreatedAt: now, UpdatedAt: now}
	if err := h.store.Sessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: sess.Principal, User: h.member.user, Trigger: core.TrigMessage, Status: core.RunDone, CreatedAt: now}
	if err := h.store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	req := core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: h.member.user}
	act := &core.Action{Tool: "release_deploy", Kind: core.ActGeneric, SideEffect: core.SEIrreversible, Payload: []byte(`{"environment":"production","ref":"main","repo":"acme/web"}`)}
	mk := func(votes ...core.UserID) core.ApprovalID {
		a := &core.Approval{ID: core.ApprovalID(core.NewID(core.PrefixApproval)), Run: run.ID, Session: sess.ID, Principal: sess.Principal, ToolCallID: "c1",
			Action: act, Decision: core.Decision{Verdict: core.VerdictAsk, Approver: req, Choices: []core.Choice{core.AllowOnce, core.Deny}},
			Card: core.Card{Title: "Deploy", Risk: core.SEIrreversible, Choices: []core.Choice{core.AllowOnce, core.Deny}}, Status: core.ApprovalPending,
			Approver: req, OnTimeout: core.TimeoutDeny, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := h.store.Approvals().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		for _, u := range votes {
			if err := h.store.Approvals().AddVote(ctx, core.ApprovalVote{Approval: a.ID, User: u, Choice: core.AllowOnce, Via: "web", At: now}); err != nil {
				t.Fatal(err)
			}
		}
		e := core.AuditEntry{At: now, Actor: "system", Action: "approval.expire", Target: string(a.ID), Outcome: "ok", Detail: json.RawMessage(`{}`)}
		if _, err := h.store.Audit().Append(ctx, &e, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	partly, none := mk(h.admin.user), mk()
	rows := decodeAs[dto.List[dto.AuditRow]](t, h.do("GET", "/api/audit?action=approval.expire", nil, h.owner))
	got := map[string]string{}
	for _, r := range rows.Items {
		got[r.Target] = r.Summary
	}
	if !strings.HasPrefix(got[string(partly)], "Didn't get enough approvals in time to ") || !strings.HasPrefix(got[string(none)], "Nobody approved ") {
		t.Fatalf("summaries = %q", got)
	}
}
