package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// storeScheduler is fakes.Scheduler persisting into the store, like the
// real scheduler, so the store-backed read routes see what it writes.
type storeScheduler struct {
	*fakes.Scheduler
	store core.Store
	runs  []core.ScheduleID
}

func (s *storeScheduler) Upsert(ctx context.Context, v core.Viewer, sc *core.Schedule, by string) error {
	if !v.Can(sc.Principal) {
		return fmt.Errorf("schedule: %w", core.ErrNotFound)
	}
	if sc.ID == "" {
		sc.ID = core.ScheduleID(core.NewID(core.PrefixSchedule))
		sc.CreatedAt = time.Now()
	}
	if sc.Kind == "" {
		sc.Kind, sc.Spec = core.SchedCron, "0 8 * * 1-5"
	}
	if sc.CreatedBy == "" {
		sc.CreatedBy = by
	}
	if sc.MissedRun == "" {
		sc.MissedRun = "skip"
	}
	if sc.Slot == "" {
		sc.Slot = core.SlotMain
	}
	sc.UpdatedAt = time.Now()
	return s.store.Schedules().Upsert(ctx, sc)
}

func (s *storeScheduler) Delete(ctx context.Context, v core.Viewer, id core.ScheduleID, _ core.UserID) error {
	return s.store.Schedules().Delete(ctx, v, id)
}

func (s *storeScheduler) RunNow(ctx context.Context, v core.Viewer, id core.ScheduleID, _ core.UserID) (core.RunID, error) {
	if _, err := s.store.Schedules().Get(ctx, v, id); err != nil {
		return "", err
	}
	s.runs = append(s.runs, id)
	return core.RunID(core.NewID(core.PrefixRun)), nil
}

// storePolicy is fakes.Policy keeping grants in the store, like the real
// policy engine.
type storePolicy struct {
	*fakes.Policy
	store   core.Store
	revoked []core.GrantID
}

func (p *storePolicy) Grant(ctx context.Context, g core.Grant) (core.GrantID, error) {
	if g.ID == "" {
		g.ID = core.GrantID(core.NewID(core.PrefixGrant))
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	return g.ID, p.store.Grants().Add(ctx, &g)
}

func (p *storePolicy) Revoke(ctx context.Context, v core.Viewer, id core.GrantID, _ core.UserID) error {
	if err := p.store.Grants().Revoke(ctx, v, id, time.Now()); err != nil {
		return err
	}
	p.revoked = append(p.revoked, id)
	return nil
}

func (h *harness) useStorePolicy() *storePolicy {
	sp := &storePolicy{Policy: h.policy, store: h.store}
	h.srv.d.Policy = sp
	return sp
}

func (h *harness) useStoreScheduler() *storeScheduler {
	ss := &storeScheduler{Scheduler: h.scheduler, store: h.store}
	h.srv.d.Scheduler = ss
	return ss
}

func multipartUpload(t *testing.T, session core.SessionID, name string, data []byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("session_id", string(session)); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), buf.Bytes()
}

// TestIDORThroughHTTP: every by-ID route answers 404 for another user's ids,
// for cookies and tokens alike, and an admin cannot read a member's chats.
func TestIDORThroughHTTP(t *testing.T) {
	h := newHarness(t).setup()
	h.useStoreScheduler()
	ctx := context.Background()
	alice := h.newUser("alice", core.RoleMember)
	ap := core.UserPrincipal(alice.user)

	sess := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{Title: "secret plans"}, alice))
	run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: ap, User: alice.user, Trigger: core.TrigMessage,
		Status: core.RunDone, CreatedAt: time.Now()}
	if err := h.store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	act := &core.Action{Tool: "shell", Kind: core.ActExec, SideEffect: core.SELocalWrite, Argv: []string{"ls"}, Payload: []byte(`{}`)}
	appr, err := h.approvals.Request(ctx, core.ApprovalRequest{Run: run.ID, Session: sess.ID, Principal: ap, Action: act,
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{User: alice.user}}})
	if err != nil {
		t.Fatal(err)
	}
	sched := decodeAs[core.Schedule](t, h.do("POST", "/api/schedules", dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "daily"}}, alice))
	pin := decodeAs[core.Pin](t, h.do("POST", "/api/sessions/"+string(sess.ID)+"/pins", dto.PinReq{Text: "don't act until I say"}, alice))
	h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{ap: {core.FileMemory: "alice likes tea"}}
	gid, _ := h.policy.Grant(ctx, core.Grant{Principal: ap, Scope: core.GrantSession, ScopeID: string(sess.ID), Kind: core.GrantDomain,
		Value: "example.com:443", MaxSE: core.SEExternalSend})
	expect(t, h.do("PUT", "/api/vault/user:"+string(alice.user)+"/github", dto.VaultPutReq{Value: "ghp_secretvalue"}, alice), http.StatusOK)
	ct, body := multipartUpload(t, sess.ID, "notes.txt", []byte("hello"))
	up := decodeAs[dto.Upload](t, h.do("POST", "/api/uploads", body, alice, withHeader("Content-Type", ct)))

	if !core.ValidID(core.PrefixSession, string(sess.ID)) || !core.ValidID(core.PrefixSchedule, string(sched.ID)) || pin.ID == 0 ||
		!core.ValidID(core.PrefixUpload, up.ID) || gid == "" {
		t.Fatalf("fixtures not created: session %q schedule %q pin %d upload %q grant %q", sess.ID, sched.ID, pin.ID, up.ID, gid)
	}
	sid, rid, aid, scid := string(sess.ID), string(run.ID), string(appr.ID), string(sched.ID)
	routes := []struct{ method, path string }{
		{"GET", "/api/sessions/" + sid},
		{"PATCH", "/api/sessions/" + sid},
		{"DELETE", "/api/sessions/" + sid},
		{"GET", "/api/sessions/" + sid + "/messages"},
		{"POST", "/api/sessions/" + sid + "/messages"},
		{"POST", "/api/sessions/" + sid + "/stop"},
		{"POST", "/api/sessions/" + sid + "/compact"},
		{"GET", "/api/sessions/" + sid + "/context"},
		{"POST", "/api/sessions/" + sid + "/fork"},
		{"GET", "/api/sessions/" + sid + "/pins"},
		{"POST", "/api/sessions/" + sid + "/pins"},
		{"DELETE", fmt.Sprintf("/api/sessions/%s/pins/%d", sid, pin.ID)},
		{"GET", "/api/runs/" + rid},
		{"POST", "/api/runs/" + rid + "/continue"},
		{"GET", "/api/approvals/" + aid},
		{"POST", "/api/approvals/" + aid},
		{"GET", "/api/schedules/" + scid},
		{"PATCH", "/api/schedules/" + scid},
		{"DELETE", "/api/schedules/" + scid},
		{"POST", "/api/schedules/" + scid + "/run"},
		{"GET", "/api/schedules/" + scid + "/runs"},
		{"GET", "/api/memory/MEMORY?principal=" + string(ap)},
		{"PUT", "/api/memory/MEMORY?principal=" + string(ap)},
		{"GET", "/api/memory/MEMORY/history?principal=" + string(ap)},
		{"GET", "/api/memory/export?principal=" + string(ap)},
		{"DELETE", "/api/grants/" + string(gid)},
		{"GET", "/api/vault?scope=user:" + string(alice.user)},
		{"PUT", "/api/vault/user:" + string(alice.user) + "/github"},
		{"DELETE", "/api/vault/user:" + string(alice.user) + "/github"},
		{"GET", "/api/workspace/files?principal=" + string(ap)},
	}
	bodies := map[string]any{
		"PATCH /api/sessions/" + sid:                            dto.SessionPatchReq{},
		"POST /api/sessions/" + sid + "/messages":               dto.SendReq{Text: "hi"},
		"POST /api/sessions/" + sid + "/pins":                   dto.PinReq{Text: "x"},
		"POST /api/approvals/" + aid:                            dto.ApprovalResolveReq{Choice: core.Deny},
		"PATCH /api/schedules/" + scid:                          dto.ScheduleUpsertReq{},
		"PUT /api/memory/MEMORY?principal=" + string(ap):        dto.MemoryPutReq{Content: "pwned"},
		"PUT /api/vault/user:" + string(alice.user) + "/github": dto.VaultPutReq{Value: "x"},
	}
	for _, c := range []*client{h.member, h.tokenFor(h.member, "chat", "read"), h.admin} {
		for _, rt := range routes {
			var opts []reqOpt
			if rt.method == "PUT" && strings.HasPrefix(rt.path, "/api/memory/") {
				opts = append(opts, withHeader("If-Match", "x"))
			}
			if c.token != "" && rt.method == "POST" && strings.HasPrefix(rt.path, "/api/approvals/") {
				continue // tokens are refused before lookup (TestApprovalResolveRefusesEveryToken)
			}
			if c == h.admin && (strings.HasPrefix(rt.path, "/api/vault") || strings.HasPrefix(rt.path, "/api/grants/")) {
				continue // admins administer vault scopes and grants by design
			}
			rec := h.do(rt.method, rt.path, bodies[rt.method+" "+rt.path], c, opts...)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: %s %s = %d %s, want 404", c.name, rt.method, rt.path, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret plans") || strings.Contains(rec.Body.String(), "tea") {
				t.Errorf("%s: %s %s leaked content", c.name, rt.method, rt.path)
			}
		}
	}
	// Uploads of another principal cannot be attached.
	mine := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	rec := h.do("POST", "/api/sessions/"+string(mine.ID)+"/messages", dto.SendReq{Text: "x", Uploads: []string{up.ID}}, h.member)
	expect(t, rec, http.StatusNotFound)
	// Lists never include foreign rows.
	for _, p := range []string{"/api/sessions", "/api/runs?session=" + sid, "/api/approvals", "/api/schedules", "/api/grants",
		"/api/search?q=secret", "/api/egress?session=" + sid} {
		rec := h.do("GET", p, nil, h.member)
		expect(t, rec, http.StatusOK)
		for _, needle := range []string{sid, rid, aid, scid, string(gid)} {
			if strings.Contains(rec.Body.String(), needle) {
				t.Errorf("GET %s lists a foreign id %s", p, needle)
			}
		}
	}
	// The owner still sees everything through their own Viewer.
	expect(t, h.do("GET", "/api/sessions/"+sid, nil, alice), http.StatusOK)
	expect(t, h.do("GET", "/api/approvals/"+aid, nil, alice), http.StatusOK)
	expect(t, h.do("GET", "/api/schedules/"+scid, nil, alice), http.StatusOK)
}
