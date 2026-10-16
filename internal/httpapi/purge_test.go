package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// deletingStore records UserRepo.Delete calls.
type deletingStore struct {
	core.Store
	deleted []core.UserID
}

func (d *deletingStore) Users() core.UserRepo { return deletingUsers{d.Store.Users(), d} }

type deletingUsers struct {
	core.UserRepo
	d *deletingStore
}

func (u deletingUsers) Delete(_ context.Context, id core.UserID) error {
	u.d.deleted = append(u.d.deleted, id)
	return nil
}

type purgeFixture struct {
	alice     *client
	ap        core.PrincipalID
	space     core.PrincipalID
	session   *core.Session
	ws, spcWS core.WorkspaceID
}

func seedPurge(t *testing.T, h *harness) purgeFixture {
	t.Helper()
	ctx := context.Background()
	alice := h.newUser("alice", core.RoleMember)
	ap := core.UserPrincipal(alice.user)
	f := purgeFixture{alice: alice, ap: ap}
	f.session = decodeAs[*core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, alice))
	// A channel of the team: purging a user never touches it, nor anyone
	// in a chat app.
	now := time.Now()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "telegram", ChatID: "-900", Kind: core.ChatGroup},
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	pr := &core.Principal{Kind: core.PrincipalSpace, ToolProfile: "restricted", CreatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, pr); err != nil {
		t.Fatal(err)
	}
	f.space, f.spcWS = pr.ID, pr.Workspace
	for _, ext := range []string{"42", "7"} {
		if _, err := h.store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: ext, Member: core.MemberUnknown,
			LastSeen: now}); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, h.do("PUT", "/api/vault/user:"+string(alice.user)+"/github", dto.VaultPutReq{Value: "ghp_aliceSecretValue"}, alice), http.StatusOK)
	if _, err := h.vault.Put(ctx, core.GlobalScope, "shared", []byte("global-value"), h.owner.user); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"example.com:443", "api.example.com:443"} {
		if err := h.store.Grants().Add(ctx, &core.Grant{ID: core.GrantID(core.NewID(core.PrefixGrant)), Principal: ap, Scope: core.GrantPrincipal,
			Kind: core.GrantDomain, Value: host, MaxSE: core.SEExternalSend, CreatedBy: alice.user, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	h.useStoreScheduler()
	expect(t, h.do("POST", "/api/schedules", dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "daily"}}, alice), http.StatusOK)
	p, _ := h.store.Principals().Get(ctx, core.SystemViewer(), ap)
	f.ws = p.Workspace
	for _, dir := range []string{filepath.Join(h.dataDir, "memory", string(ap)), filepath.Join(h.workDir, "ws", string(f.ws), ".bothy"),
		filepath.Join(h.workDir, "ws", string(f.spcWS)), filepath.Join(h.dataDir, "github", "checkouts", string(f.ws), "octocat", "hello-world")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.dataDir, "memory", string(ap), "MEMORY.md"), []byte("alice likes tea"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.cfg.update(func(c *config.Config) {
		c.MCP.Servers = []config.MCPServer{{Name: "notes", Transport: "stdio"}}
	})
	return f
}

func TestDeleteUserDisablesWithoutPurge(t *testing.T) {
	h := newHarness(t).setup()
	f := seedPurge(t, h)
	expect(t, h.do("DELETE", "/api/users/"+string(f.alice.user), nil, h.admin), http.StatusNoContent)
	expect(t, h.do("GET", "/api/auth/me", nil, f.alice), http.StatusUnauthorized)
	u, _ := h.store.Users().Get(context.Background(), f.alice.user)
	if !u.Disabled {
		t.Fatal("not disabled")
	}
	if ms, _ := h.vault.List(context.Background(), core.UserScope(f.alice.user)); len(ms) != 1 {
		t.Fatal("non-purge delete removed secrets")
	}
	// The owner and the caller themselves are protected.
	expect(t, h.do("DELETE", "/api/users/"+string(h.owner.user), nil, h.admin), http.StatusForbidden)
	expect(t, h.do("DELETE", "/api/users/"+string(h.admin.user), nil, h.admin), http.StatusForbidden)
	expect(t, h.do("DELETE", "/api/users/"+string(f.alice.user), nil, h.member), http.StatusForbidden)
}

func TestPurgeWipesPrincipalData(t *testing.T) {
	h := newHarness(t).setup()
	f := seedPurge(t, h)
	sp := h.useStorePolicy()
	ctx := context.Background()
	auditBefore := len(h.audit.All())
	expect(t, h.do("DELETE", "/api/users/"+string(f.alice.user)+"?purge=true", nil, h.owner), http.StatusNoContent)

	if ms, _ := h.vault.List(ctx, core.UserScope(f.alice.user)); len(ms) != 0 {
		t.Errorf("vault rows left: %+v", ms)
	}
	if ms, _ := h.vault.List(ctx, core.GlobalScope); len(ms) != 1 {
		t.Errorf("global secrets touched: %+v", ms)
	}
	v := core.Viewer{Principals: []core.PrincipalID{f.ap}}
	if gs, _ := h.store.Grants().List(ctx, v, "", ""); len(gs) != 0 {
		t.Errorf("active store grants left: %+v", gs)
	}
	if len(sp.revoked) != 2 {
		t.Errorf("grants revoked through the policy = %v", sp.revoked)
	}
	if scs, _ := h.store.Schedules().List(ctx, v); len(scs) != 0 {
		t.Errorf("schedules left: %+v", scs)
	}
	if ids, _ := h.store.Identities().List(ctx, ""); len(ids) != 2 {
		t.Errorf("identities touched by a user purge: %+v", ids)
	}
	for _, dir := range []string{filepath.Join(h.dataDir, "memory", string(f.ap)), filepath.Join(h.workDir, "ws", string(f.ws)),
		filepath.Join(h.dataDir, "github", "checkouts", string(f.ws))} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(h.workDir, "ws", string(f.spcWS))); err != nil {
		t.Errorf("the channel's workspace went with the user: %v", err)
	}
	if _, err := h.store.Spaces().Get(ctx, core.SystemViewer(), f.space); err != nil {
		t.Errorf("the channel went with the user: %v", err)
	}
	destroyed := h.sandbox.Destroyed
	for _, key := range []string{core.DerivedID("box", "session:"+string(f.session.ID)), core.DerivedID("box", "mcp:notes:"+string(f.ap))} {
		if !slices.Contains(destroyed, key) {
			t.Errorf("box %s not destroyed (destroyed %v)", key, destroyed)
		}
	}
	if slices.Contains(destroyed, core.DerivedID("box", "mcp:notes:"+string(f.space))) {
		t.Error("the channel's box went with the user")
	}
	if ss, _ := h.store.Sessions().List(ctx, v, core.SessionFilter{IncludeHidden: true}); len(ss) != 0 {
		t.Errorf("open sessions left: %d", len(ss))
	}
	// Audit rows are kept, and the purge itself is audited with counts.
	all := h.audit.All()
	if len(all) <= auditBefore {
		t.Fatal("audit rows disappeared")
	}
	last := all[len(all)-1]
	if last.Action != "user.update" || last.Target != "user:"+string(f.alice.user) || last.Outcome != "ok" ||
		!strings.Contains(string(last.Detail), `"purge":true`) || !strings.Contains(string(last.Detail), `"secrets":1`) {
		t.Fatalf("purge audit = %+v %s", last, last.Detail)
	}
}

func TestPurgeDeletesUserRowWhenSupported(t *testing.T) {
	h := newHarness(t).setup()
	ds := &deletingStore{Store: h.store}
	h.srv.d.Store = ds
	f := seedPurge(t, h)
	expect(t, h.do("DELETE", "/api/users/"+string(f.alice.user)+"?purge=yes", nil, h.owner), http.StatusBadRequest)
	expect(t, h.do("DELETE", "/api/users/"+string(f.alice.user)+"?purge=1", nil, h.owner), http.StatusNoContent)
	if !slices.Equal(ds.deleted, []core.UserID{f.alice.user}) {
		t.Fatalf("deleted = %v", ds.deleted)
	}
}
