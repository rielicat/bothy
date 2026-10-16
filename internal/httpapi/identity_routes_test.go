package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// Letting someone in or making them an approver lets them decide requests
// in a chat app, so it is cookie-only like approvals; deleting someone's
// data too.
func TestIdentityRoutesRefuseTokens(t *testing.T) {
	h := newHarness(t).setup()
	all := []string{"chat", "read", "llm", "admin"}
	let := core.IdentityTeammate
	for _, c := range []*client{h.tokenFor(h.owner, "chat"), h.tokenFor(h.owner, all...), h.tokenFor(h.member, "chat")} {
		expect(t, h.do("PATCH", "/api/identities/telegram/1", dto.IdentityPatchReq{Status: &let}, c), http.StatusForbidden)
		expect(t, h.do("DELETE", "/api/teammates/telegram/1", nil, c), http.StatusForbidden)
	}
	expect(t, h.do("GET", "/api/identities", nil, h.member), http.StatusForbidden)
	expect(t, h.do("GET", "/api/teammates", nil, h.member), http.StatusForbidden)
	expect(t, h.do("GET", "/api/spaces", nil, h.member), http.StatusForbidden)
	for _, rt := range [][2]string{{"POST", "/api/pairing"}, {"POST", "/api/pairing/approve"}, {"DELETE", "/api/identities/telegram/1"}} {
		if rec := h.do(rt[0], rt[1], nil, h.owner); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want gone", rt[0], rt[1], rec.Code)
		}
	}
}

// Pre-authorized schedule grants answer approvals in advance; a token, whose
// runs follow on_approval, must not be able to create them.
func TestScheduleGrantsRefuseTokens(t *testing.T) {
	h := newHarness(t).setup()
	h.useStoreScheduler()
	h.useStorePolicy()
	tok := h.tokenFor(h.member, "chat")
	grants := []core.Grant{{Kind: core.GrantDomain, Value: "api.weather.example:443", MaxSE: core.SERead}}
	expect(t, h.do("POST", "/api/schedules", dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "weather"}, Grants: grants}, tok),
		http.StatusForbidden)
	rec := h.do("POST", "/api/schedules", dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "weather"}}, tok)
	expect(t, rec, http.StatusOK)
	id := decodeAs[core.Schedule](t, rec).ID
	expect(t, h.do("PATCH", "/api/schedules/"+string(id), dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "weather"}, Grants: grants}, tok),
		http.StatusForbidden)
	expect(t, h.do("PATCH", "/api/schedules/"+string(id), dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "weather"}, Grants: grants}, h.member),
		http.StatusOK)
}

// touch records a chat sender the way the hub does.
func (h *harness) touch(channel, ext, name string, m core.MemberStatus) {
	h.t.Helper()
	now := time.Now()
	if _, err := h.store.Identities().Touch(context.Background(), &core.ChannelIdentity{Channel: channel, ExternalID: ext, DisplayName: name,
		Username: "u" + ext, Member: m, FirstSeen: now, LastSeen: now}); err != nil {
		h.t.Fatal(err)
	}
}

func errMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", rec.Body, err)
	}
	return body.Error.Message
}

func (h *harness) actions(action string) []core.AuditEntry {
	var out []core.AuditEntry
	for _, e := range h.audit.All() {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// TestPatchIdentity: letting someone in greets them in their chat, making
// them an approver tells them, blocking stops their tasks; refusals come
// back in plain words, and the migration's reminder goes away.
func TestPatchIdentity(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	if err := h.store.Meta().Set(ctx, "team_access_report", `{"at":1}`); err != nil {
		t.Fatal(err)
	}
	h.touch("telegram", "777", "Jordan", core.MemberUnknown)
	let, block, yes := core.IdentityTeammate, core.IdentityBlocked, true

	rec := h.do("PATCH", "/api/identities/telegram/777", dto.IdentityPatchReq{Status: &let}, h.owner)
	expect(t, rec, http.StatusOK)
	tm := decodeAs[dto.Teammate](t, rec)
	if tm.Status != core.IdentityTeammate || tm.Name != "Jordan" || tm.Handle != "u777" || tm.Principal == "" || tm.CanApprove {
		t.Fatalf("let in = %+v", tm)
	}
	if got := h.hub.Greetings(); !slices.Equal(got, []string{"telegram:777"}) {
		t.Fatalf("greetings = %v", got)
	}
	if len(h.actions("identity.let_in")) != 1 {
		t.Fatal("letting someone in wasn't audited")
	}
	if _, err := h.store.Meta().Get(ctx, "team_access_report"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the migration report is still there: %v", err)
	}

	expect(t, h.do("PATCH", "/api/identities/telegram/777", dto.IdentityPatchReq{CanApprove: &yes}, h.owner), http.StatusOK)
	if got := h.hub.ToldApprovers(); !slices.Equal(got, []string{"telegram:777"}) {
		t.Fatalf("approver notes = %v", got)
	}
	expect(t, h.do("PATCH", "/api/identities/telegram/777", dto.IdentityPatchReq{Status: &block}, h.owner), http.StatusOK)
	if got := h.runner.StoppedPrincipals(); !slices.Equal(got, []core.PrincipalID{tm.Principal}) {
		t.Fatalf("stopped principals = %v", got)
	}
	expect(t, h.do("PATCH", "/api/identities/telegram/777", dto.IdentityPatchReq{Status: &let}, h.owner), http.StatusOK)
	if len(h.hub.Greetings()) != 1 || len(h.actions("identity.unblock")) != 1 {
		t.Fatal("unblocking greeted again or wasn't audited")
	}

	h.touch("slack", "U1", "Maya", core.MemberUnchecked)
	rec = h.do("PATCH", "/api/identities/slack/U1", dto.IdentityPatchReq{Status: &let}, h.owner)
	expect(t, rec, http.StatusBadRequest)
	if msg := errMessage(t, rec); msg != "Slack decides who can use Bothy there. They get in when they message Bothy, if they're in the team." {
		t.Fatalf("refusal = %v", msg)
	}
	expect(t, h.do("PATCH", "/api/identities/slack/U1", dto.IdentityPatchReq{CanApprove: &yes}, h.owner), http.StatusBadRequest)
	expect(t, h.do("PATCH", "/api/identities/slack/U1", dto.IdentityPatchReq{Status: &let}, h.member), http.StatusForbidden)
	expect(t, h.do("PATCH", "/api/identities/slack/nobody", dto.IdentityPatchReq{Status: &let}, h.owner), http.StatusNotFound)
}

func TestListTeammates(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	let, block, yes := core.IdentityTeammate, core.IdentityBlocked, true
	for _, p := range []struct {
		ext, name string
	}{{"1", "Ana"}, {"2", "Ben"}, {"3", "Cy"}, {"4", "Dee"}} {
		h.touch("telegram", p.ext, p.name, core.MemberUnknown)
		time.Sleep(time.Millisecond)
	}
	h.touch("telegram", "5", "Pending", core.MemberUnknown)
	for _, ext := range []string{"1", "2", "3", "4"} {
		expect(t, h.do("PATCH", "/api/identities/telegram/"+ext, dto.IdentityPatchReq{Status: &let}, h.owner), http.StatusOK)
	}
	expect(t, h.do("PATCH", "/api/identities/telegram/3", dto.IdentityPatchReq{CanApprove: &yes}, h.owner), http.StatusOK)
	expect(t, h.do("PATCH", "/api/identities/telegram/1", dto.IdentityPatchReq{Status: &block}, h.owner), http.StatusOK)
	ben, _ := h.store.Identities().Get(ctx, "telegram", "2")
	day := time.Now().In(h.srv.location()).Format(time.DateOnly)
	if err := h.store.Usage().AddSpend(ctx, ben.Principal, day, core.CallInfo{Cost: core.MicrosFromUSD(0.25)}); err != nil {
		t.Fatal(err)
	}
	rec := h.do("GET", "/api/teammates", nil, h.owner)
	expect(t, rec, http.StatusOK)
	list := decodeAs[[]dto.Teammate](t, rec)
	var order []string
	for _, tm := range list {
		order = append(order, tm.Name)
	}
	if !slices.Equal(order, []string{"Cy", "Dee", "Ben", "Ana"}) {
		t.Fatalf("order = %v, want approvers, then by last active, then blocked", order)
	}
	if list[2].SpentToday != core.MicrosFromUSD(0.25) {
		t.Fatalf("Ben spent %v today", list[2].SpentToday)
	}
}

// TestForgetTeammate: only someone blocked (or no longer in the team) can
// have their data deleted; their principal goes, they stay blocked.
func TestForgetTeammate(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	let, block := core.IdentityTeammate, core.IdentityBlocked
	h.touch("telegram", "777", "Jordan", core.MemberUnknown)
	tm := decodeAs[dto.Teammate](t, h.do("PATCH", "/api/identities/telegram/777", dto.IdentityPatchReq{Status: &let}, h.owner))
	rec := h.do("DELETE", "/api/teammates/telegram/777", nil, h.owner)
	expect(t, rec, http.StatusBadRequest)
	if msg := errMessage(t, rec); msg != "Block them first." {
		t.Fatalf("refusal = %v", msg)
	}
	expect(t, h.do("PATCH", "/api/identities/telegram/777", dto.IdentityPatchReq{Status: &block}, h.owner), http.StatusOK)
	expect(t, h.do("DELETE", "/api/teammates/telegram/777", nil, h.admin), http.StatusNoContent)
	if _, err := h.store.Principals().Get(ctx, core.SystemViewer(), tm.Principal); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("their principal is still there: %v", err)
	}
	id, _ := h.store.Identities().Get(ctx, "telegram", "777")
	if id.Status != core.IdentityBlocked || id.Principal != "" {
		t.Fatalf("identity after forgetting = %+v", id)
	}
	rows := h.actions("identity.forget")
	if len(rows) != 1 {
		t.Fatalf("forget audit = %+v", rows)
	}
	var detail map[string]any
	_ = json.Unmarshal(rows[0].Detail, &detail)
	if detail["principal"] != string(tm.Principal) {
		t.Fatalf("forget detail = %s", rows[0].Detail)
	}
}
