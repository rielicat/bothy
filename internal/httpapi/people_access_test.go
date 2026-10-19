package httpapi

import (
	"net/http"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// An admin token must not hand out an account: adding someone returns a
// password, a promotion makes an account an admin's, and a purge can't be
// undone. All three need a signed-in admin, like setting a password.
func TestPeopleChangesATokenCannotMake(t *testing.T) {
	h := newHarness(t).setup()
	tok := h.tokenFor(h.admin, "admin")
	expect(t, h.do("POST", "/api/users", dto.UserCreateReq{Username: "eve", Password: testPassword, Role: core.RoleAdmin}, tok), http.StatusForbidden)
	expect(t, h.do("POST", "/api/users", dto.UserCreateReq{Username: "eve", Role: core.RoleMember}, tok), http.StatusForbidden)
	admin, member := core.RoleAdmin, core.RoleMember
	path := "/api/users/" + string(h.member.user)
	expect(t, h.do("PATCH", path, dto.UserPatchReq{Role: &admin}, tok), http.StatusForbidden)
	name := "Mem Ber"
	expect(t, h.do("PATCH", path, dto.UserPatchReq{Role: &member, DisplayName: &name}, tok), http.StatusOK)
	expect(t, h.do("DELETE", path+"?purge=true", nil, tok), http.StatusForbidden)
	if u, _ := h.store.Users().Get(t.Context(), h.member.user); u.Role != core.RoleMember || u.Disabled {
		t.Fatalf("member after token calls = %+v", u)
	}
	expect(t, h.do("DELETE", path, nil, tok), http.StatusNoContent)
	expect(t, h.do("POST", "/api/users", dto.UserCreateReq{Username: "eve", Role: core.RoleMember}, h.admin), http.StatusOK)
}

func TestOnlyTheOwnerTurnsOffOrRemovesAnAdmin(t *testing.T) {
	h := newHarness(t).setup()
	other := h.newUser("otto", core.RoleAdmin)
	path := "/api/users/" + string(other.user)
	member, off := core.RoleMember, true
	expect(t, h.do("PATCH", path, dto.UserPatchReq{Role: &member}, h.admin), http.StatusForbidden)
	expect(t, h.do("PATCH", path, dto.UserPatchReq{Disabled: &off}, h.admin), http.StatusForbidden)
	expect(t, h.do("DELETE", path, nil, h.admin), http.StatusForbidden)
	expect(t, h.do("PATCH", path, dto.UserPatchReq{Disabled: &off}, h.owner), http.StatusOK)
	// Turned off by the owner, the account still isn't another admin's to purge.
	expect(t, h.do("DELETE", path+"?purge=true", nil, h.admin), http.StatusForbidden)
	expect(t, h.do("DELETE", path+"?purge=true", nil, h.owner), http.StatusNoContent)
}
