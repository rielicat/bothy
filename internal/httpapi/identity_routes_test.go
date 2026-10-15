package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// A linked chat identity receives approval cards and answers them as its
// user, so creating or reassigning one is cookie-only like approvals: a
// leaked token must not mint a pairing code and approve from a chat app.
func TestIdentityRoutesRefuseTokens(t *testing.T) {
	h := newHarness(t).setup()
	all := []string{"chat", "read", "llm", "admin"}
	for _, c := range []*client{h.tokenFor(h.owner, "chat"), h.tokenFor(h.owner, all...), h.tokenFor(h.member, "chat")} {
		expect(t, h.do("POST", "/api/pairing", nil, c), http.StatusForbidden)
		expect(t, h.do("DELETE", "/api/identities/telegram/1", nil, c), http.StatusForbidden)
	}
	admin := h.tokenFor(h.owner, all...)
	expect(t, h.do("POST", "/api/pairing/approve", dto.PairingApproveReq{Code: "ABCD-EFGH", As: "link", UserID: h.owner.user}, admin),
		http.StatusForbidden)
	expect(t, h.do("PATCH", "/api/identities/telegram/1", dto.IdentityPatchReq{Status: core.IdentityLinked, UserID: h.owner.user}, admin),
		http.StatusForbidden)
	expect(t, h.do("POST", "/api/pairing", nil, h.member), http.StatusOK)
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

// Someone an admin lets in hears so in their chat; their last word from
// Bothy was "ask an admin to approve code …".
func TestApprovedSenderIsWelcomed(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	ident := core.ChannelIdentity{Channel: "telegram", ExternalID: "777", DMChat: "777", DisplayName: "Guest"}
	notice, err := h.auth.SenderNotice(ctx, ident)
	if err != nil || !notice.Reply {
		t.Fatalf("notice = %+v %v", notice, err)
	}
	expect(t, h.do("POST", "/api/pairing/approve", dto.PairingApproveReq{Code: notice.Code.Code, As: "link", UserID: h.member.user}, h.owner),
		http.StatusOK)
	var sent []string
	for _, s := range h.hub.Sent {
		if strings.HasPrefix(s, "telegram:777:") {
			sent = append(sent, s)
		}
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "Hi member! An admin approved you") {
		t.Fatalf("welcome = %q", sent)
	}
}
