package httpapi

import (
	"net/http"
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
