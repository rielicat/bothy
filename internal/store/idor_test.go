package store

import (
	"errors"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// TestIDOR covers every repository method that takes a Viewer, for rows owned
// by a space principal: any viewer without that principal (admin or not,
// empty, user-only, the owner's own principal alone) gets ErrNotFound and
// changes nothing, while the owner viewer and SystemViewer succeed.
func TestIDOR(t *testing.T) {
	w := newWorld(t)
	owner, admin := w.user(core.RoleMember), w.user(core.RoleAdmin)
	sp := w.space()
	p := sp.Principal
	s := w.session(p)
	w.message(s.ID, core.ProvUntrusted, "space secret")
	pin := &core.Pin{Session: s.ID, Text: "pin", Source: "user"}
	w.must(w.db.Pins().Add(w.ctx, pin))
	run := w.run(s, core.RunWaitingApproval)
	w.must(w.db.Runs().AddLLMCall(w.ctx, run.ID, p, owner.ID, core.CallInfo{Ref: core.ModelRef{Provider: "p", Model: "m"}, Purpose: "agent"}))
	w.must(w.db.Runs().AddToolCall(w.ctx, &core.ToolCallRecord{Run: run.ID, Session: s.ID, Principal: p, Tool: "fs_read", Source: "builtin", SideEffect: core.SERead, Status: core.TCOK}))
	apr := &core.Approval{Run: run.ID, Session: s.ID, Principal: p, Status: core.ApprovalPending, OnTimeout: core.TimeoutDeny, Approver: core.ApproverReq{User: owner.ID}, Card: core.Card{Risk: core.SEExternalSend}}
	w.must(w.db.Approvals().Create(w.ctx, apr))
	decided := &core.Approval{Run: run.ID, Session: s.ID, Principal: p, Status: core.ApprovalAllowed, OnTimeout: core.TimeoutDeny, Approver: core.ApproverReq{User: owner.ID}, Card: core.Card{Risk: core.SEExternalSend}}
	w.must(w.db.Approvals().Create(w.ctx, decided))
	w.must(w.db.Egress().Add(w.ctx, []core.EgressRecord{
		{At: t0, Dialer: "box", Session: s.ID, Principal: p, Host: "a.example", Port: 443, Decision: "allow"},
		{At: t0, Dialer: "system", Dest: "anthropic", Host: "api.anthropic.com", Port: 443, Decision: "allow"},
	}))
	grant := &core.Grant{Principal: p, Scope: core.GrantSession, ScopeID: string(s.ID), Kind: core.GrantDomain, Value: "a.example:443", MaxSE: core.SEExternalSend}
	w.must(w.db.Grants().Add(w.ctx, grant))
	sch := &core.Schedule{Principal: p, Name: "s", Kind: core.SchedCron, Spec: "0 9 * * *", TZ: "UTC", Prompt: "x", MissedRun: "skip", CreatedBy: "system"}
	w.must(w.db.Schedules().Upsert(w.ctx, sch))
	_, err := w.db.MemoryEvents().Add(w.ctx, &core.MemoryEvent{Principal: p, File: core.FileMemory, Op: "add", Actor: "user:x"})
	w.must(err)
	up := &core.Upload{Principal: p, Session: s.ID, Name: "a", MIME: "text/plain", Ref: "a"}
	w.must(w.db.Uploads().Create(w.ctx, up))

	reads := map[string]func(core.Viewer) error{
		"Principals.Get": func(v core.Viewer) error { _, err := w.db.Principals().Get(w.ctx, v, p); return err },
		"Spaces.Get":     func(v core.Viewer) error { _, err := w.db.Spaces().Get(w.ctx, v, p); return err },
		"Sessions.Get":   func(v core.Viewer) error { _, err := w.db.Sessions().Get(w.ctx, v, s.ID); return err },
		"Messages.Range": func(v core.Viewer) error { _, err := w.db.Messages().Range(w.ctx, v, s.ID, 1, 0); return err },
		"Messages.Page":  func(v core.Viewer) error { _, err := w.db.Messages().Page(w.ctx, v, s.ID, core.Page{}); return err },
		"Pins.List":      func(v core.Viewer) error { _, err := w.db.Pins().List(w.ctx, v, s.ID); return err },
		"Runs.Get":       func(v core.Viewer) error { _, err := w.db.Runs().Get(w.ctx, v, run.ID); return err },
		"Runs.LLMCalls":  func(v core.Viewer) error { _, err := w.db.Runs().LLMCalls(w.ctx, v, run.ID); return err },
		"Runs.ToolCalls": func(v core.Viewer) error { _, err := w.db.Runs().ToolCalls(w.ctx, v, run.ID); return err },
		"Approvals.Get":  func(v core.Viewer) error { _, err := w.db.Approvals().Get(w.ctx, v, apr.ID); return err },
		"Schedules.Get":  func(v core.Viewer) error { _, err := w.db.Schedules().Get(w.ctx, v, sch.ID); return err },
		"MemoryEvents.List": func(v core.Viewer) error {
			_, err := w.db.MemoryEvents().List(w.ctx, v, p, "", core.Page{})
			return err
		},
		"Uploads.Get": func(v core.Viewer) error { _, err := w.db.Uploads().Get(w.ctx, v, up.ID); return err },
	}
	writes := map[string]func(core.Viewer) error{
		"Sessions.Archive": func(v core.Viewer) error { return w.db.Sessions().Archive(w.ctx, v, s.ID) },
		"Pins.Delete":      func(v core.Viewer) error { return w.db.Pins().Delete(w.ctx, v, s.ID, pin.ID) },
		"Grants.Revoke":    func(v core.Viewer) error { return w.db.Grants().Revoke(w.ctx, v, grant.ID, t0) },
		"Schedules.Delete": func(v core.Viewer) error { return w.db.Schedules().Delete(w.ctx, v, sch.ID) },
	}
	lists := map[string]func(core.Viewer) (int, error){
		"Spaces.List": func(v core.Viewer) (int, error) { x, err := w.db.Spaces().List(w.ctx, v); return len(x), err },
		"Sessions.List": func(v core.Viewer) (int, error) {
			x, err := w.db.Sessions().List(w.ctx, v, core.SessionFilter{IncludeHidden: true, Principal: p})
			return len(x), err
		},
		"Messages.Search": func(v core.Viewer) (int, error) {
			x, err := w.db.Messages().Search(w.ctx, v, "secret", 10)
			return len(x), err
		},
		"Runs.List": func(v core.Viewer) (int, error) {
			x, err := w.db.Runs().List(w.ctx, v, core.RunFilter{})
			return len(x), err
		},
		"Approvals.Pending": func(v core.Viewer) (int, error) { x, err := w.db.Approvals().Pending(w.ctx, v); return len(x), err },
		"Approvals.Recent": func(v core.Viewer) (int, error) {
			x, err := w.db.Approvals().Recent(w.ctx, v, core.Page{})
			return len(x), err
		},
		// Scoped to the session: admins also see the system-dialer row, which
		// belongs to no principal and no session.
		"Egress.Query": func(v core.Viewer) (int, error) {
			x, err := w.db.Egress().Query(w.ctx, v, s.ID, core.Page{})
			return len(x), err
		},
		"Grants.List":    func(v core.Viewer) (int, error) { x, err := w.db.Grants().List(w.ctx, v, "", ""); return len(x), err },
		"Schedules.List": func(v core.Viewer) (int, error) { x, err := w.db.Schedules().List(w.ctx, v); return len(x), err },
	}

	adminV := viewerOf(admin)
	adminV.Admin = true
	foreign := map[string]core.Viewer{
		"foreign":         viewerOf(admin),
		"foreign admin":   adminV,
		"empty":           {},
		"user only":       {User: owner.ID},
		"owner principal": viewerOf(owner), // the space is a separate principal
	}
	for vname, v := range foreign {
		for name, call := range reads {
			if err := call(v); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("%s viewer: %s = %v, want ErrNotFound", vname, name, err)
			}
		}
		for name, call := range writes {
			if err := call(v); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("%s viewer: %s = %v, want ErrNotFound", vname, name, err)
			}
		}
		for name, call := range lists {
			if n, err := call(v); err != nil || n != 0 {
				t.Errorf("%s viewer: %s returned %d rows, %v", vname, name, n, err)
			}
		}
	}
	for vname, v := range map[string]core.Viewer{"owner": viewerOf(owner, p), "system": core.SystemViewer()} {
		for name, call := range reads {
			if err := call(v); err != nil {
				t.Errorf("%s viewer: %s = %v", vname, name, err)
			}
		}
		for name, call := range lists {
			if n, err := call(v); err != nil || n == 0 {
				t.Errorf("%s viewer: %s returned %d rows, %v", vname, name, n, err)
			}
		}
	}
	got, err := w.db.Sessions().Get(w.ctx, core.SystemViewer(), s.ID)
	if err != nil || got.Archived {
		t.Errorf("a foreign Archive took effect: %+v %v", got, err)
	}
	if pins, _ := w.db.Pins().List(w.ctx, core.SystemViewer(), s.ID); len(pins) != 1 {
		t.Error("a foreign Pins.Delete took effect")
	}
	if gs, _ := w.db.Grants().List(w.ctx, core.SystemViewer(), "", ""); len(gs) != 1 {
		t.Error("a foreign Grants.Revoke took effect")
	}
	if _, err := w.db.Schedules().Get(w.ctx, core.SystemViewer(), sch.ID); err != nil {
		t.Error("a foreign Schedules.Delete took effect")
	}
	for name, call := range writes {
		if err := call(viewerOf(owner, p)); err != nil {
			t.Errorf("owner viewer: %s = %v", name, err)
		}
	}
}

// TestApprovalAdminException: Viewer.Admin reveals admin-only approvals of
// other principals and nothing else.
func TestApprovalAdminException(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.user(core.RoleMember), w.user(core.RoleAdmin)
	run := w.run(w.session(core.UserPrincipal(alice.ID)), core.RunWaitingApproval)
	mk := func(adminOnly bool) *core.Approval {
		a := &core.Approval{Run: run.ID, Session: run.Session, Principal: run.Principal, Status: core.ApprovalPending,
			OnTimeout: core.TimeoutDeny, Approver: core.ApproverReq{User: alice.ID, AdminOnly: adminOnly}, Card: core.Card{Risk: core.SEExternalSend}}
		w.must(w.db.Approvals().Create(w.ctx, a))
		return a
	}
	plain, adminOnly := mk(false), mk(true)
	v := viewerOf(bob)
	v.Admin = true
	if _, err := w.db.Approvals().Get(w.ctx, v, adminOnly.ID); err != nil {
		t.Errorf("admin cannot see an admin-only approval: %v", err)
	}
	if _, err := w.db.Approvals().Get(w.ctx, v, plain.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("admin sees a plain approval of another principal: %v", err)
	}
	empty := core.Viewer{Admin: true}
	if as, err := w.db.Approvals().Pending(w.ctx, empty); err != nil || len(as) != 1 || as[0].ID != adminOnly.ID {
		t.Errorf("admin with no principals: Pending = %v, %v", as, err)
	}
	if _, err := w.db.Runs().Get(w.ctx, v, run.ID); !errors.Is(err, core.ErrNotFound) {
		t.Error("Viewer.Admin leaked a run")
	}
}
