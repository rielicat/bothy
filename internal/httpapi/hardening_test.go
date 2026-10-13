package httpapi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/httpapi/dto"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
)

func TestForwardedForUnreadableHopIsPublic(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Server.TrustedProxies = []string{"127.0.0.1/32"} })
	// A local proxy that forwards a hop the server cannot read must not make
	// the remote client look like the proxy itself.
	for _, xff := range []string{"unknown", "203.0.113.9, garbage", "203.0.113.9,"} {
		rec := h.do("GET", "/api/setup/state", nil, nil, withHeader("X-Forwarded-For", xff))
		expect(t, rec, http.StatusForbidden)
	}
	expect(t, h.do("GET", "/api/setup/state", nil, nil), http.StatusOK)
	expect(t, h.do("GET", "/api/setup/state", nil, nil, withHeader("X-Forwarded-For", "127.0.0.1")), http.StatusOK)
}

func tokenID(c *client) string {
	return core.PrefixToken + "_" + c.token[len("bothy_pat_"):len("bothy_pat_")+26]
}

// stillOpen proves a stream is alive by delivering a fresh event through it.
func (sc *sseConn) stillOpen(t *testing.T, h *harness, p core.PrincipalID) {
	t.Helper()
	time.Sleep(100 * time.Millisecond) // several re-checks at the test interval
	h.bus.Publish(delta(p, "alive"))
	if f := sc.next(t); f.event != string(core.EvRunDelta) {
		t.Fatalf("frame = %+v", f)
	}
}

func TestSSERechecksCredentials(t *testing.T) {
	h := newHarness(t).setup()
	h.srv.ssePing, h.srv.sseRecheck = 10*time.Millisecond, 0
	srv := h.server(t)

	revoked := h.tokenFor(h.member, "read")
	kept := h.tokenFor(h.member, "read")
	cr, ck := h.openSSE(t, srv, revoked, ""), h.openSSE(t, srv, kept, "")
	cr.next(t)
	ck.next(t)
	expect(t, h.do("DELETE", "/api/tokens/"+tokenID(revoked), nil, h.member), http.StatusNoContent)
	if !cr.closed(t) {
		t.Fatal("stream of a revoked token stayed open")
	}
	ck.stillOpen(t, h, core.UserPrincipal(h.member.user))

	other := h.newUser("leaver", core.RoleMember)
	co := h.openSSE(t, srv, other, "")
	co.next(t)
	expect(t, h.do("POST", "/api/auth/logout", nil, other), http.StatusNoContent)
	if !co.closed(t) {
		t.Fatal("stream of a logged-out session stayed open")
	}
}

func TestSSEPings(t *testing.T) {
	h := newHarness(t).setup()
	h.srv.ssePing = 10 * time.Millisecond
	srv := h.server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: cookiePlain, Value: h.member.cookie})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("no ping before %v", err)
		}
		if line == ": ping\n" {
			return
		}
	}
}

func TestSSEReconnectsWhenPrincipalsChange(t *testing.T) {
	h := newHarness(t).setup()
	h.srv.ssePing, h.srv.sseRecheck = 10*time.Millisecond, 0
	srv := h.server(t)
	c := h.openSSE(t, srv, h.admin, "")
	c.next(t)
	ctx := context.Background()
	ident := core.ChannelIdentity{Channel: "telegram", ExternalID: "777", DMChat: "777", DisplayName: "Guest"}
	if _, err := h.auth.SenderNotice(ctx, ident); err != nil {
		t.Fatal(err)
	}
	admin, err := h.auth.FromCookie(ctx, h.admin.cookie)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.auth.SetIdentityStatus(ctx, admin, "telegram", "777", core.IdentityGuest, ""); err != nil {
		t.Fatal(err)
	}
	if !c.closed(t) {
		t.Fatal("stream kept a filter without the admin's new guest space")
	}
}

func TestSSEClosedOnRoleAndPasswordChange(t *testing.T) {
	h := newHarness(t).setup()
	srv := h.server(t)
	ca := h.openSSE(t, srv, h.admin, "")
	ca.next(t)
	member := core.RoleMember
	expect(t, h.do("PATCH", "/api/users/"+string(h.admin.user), dto.UserPatchReq{Role: &member}, h.owner), http.StatusOK)
	if !ca.closed(t) {
		t.Fatal("a demoted admin kept a stream with the admin filter")
	}

	cm := h.openSSE(t, srv, h.member, "")
	cm.next(t)
	expect(t, h.do("POST", "/api/auth/password", dto.PasswordReq{Old: testPassword, New: "quartz lantern ripple harbor"}, h.member),
		http.StatusNoContent)
	if !cm.closed(t) {
		t.Fatal("stream outlived a password change")
	}
}

func TestBannersAcceptPointerPayloads(t *testing.T) {
	h := newHarness(t).setup()
	h.bus.Publish(core.Event{Type: core.EvBanner, Principal: core.UserPrincipal(h.member.user),
		Data: &core.BannerData{Level: "warn", Code: "budget", Text: "80% of today's budget used"}})
	me := decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, h.member))
	if len(me.Banners) != 1 || me.Banners[0].Code != "budget" {
		t.Fatalf("banners = %+v", me.Banners)
	}
}

func TestWorkspaceListingBounded(t *testing.T) {
	h := newHarness(t).setup()
	p, err := h.store.Principals().Get(context.Background(), core.SystemViewer(), core.UserPrincipal(h.member.user))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.workDir, "ws", string(p.Workspace), "many")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxListing + 3 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := h.do("GET", "/api/workspace/files?path=many", nil, h.member)
	expect(t, rec, http.StatusOK)
	if got := decodeAs[[]fileEntry](t, rec); len(got) != maxListing || rec.Header().Get("X-Bothy-Truncated") != "true" {
		t.Fatalf("listing has %d entries, truncated header %q", len(got), rec.Header().Get("X-Bothy-Truncated"))
	}
}

// slowCompactRunner blocks Compact until released and counts the calls.
type slowCompactRunner struct {
	*fakes.Runner
	calls   atomic.Int32
	release chan struct{}
}

func (r *slowCompactRunner) Compact(ctx context.Context, _ core.Viewer, _ core.SessionID) error {
	r.calls.Add(1)
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestCompactionOncePerSession(t *testing.T) {
	h := newHarness(t).setup()
	rn := &slowCompactRunner{Runner: h.runner, release: make(chan struct{})}
	h.srv.d.Runner = rn
	sess := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	other := decodeAs[core.Session](t, h.do("POST", "/api/sessions", dto.SessionCreateReq{}, h.member))
	for range 5 {
		expect(t, h.do("POST", "/api/sessions/"+string(sess.ID)+"/compact", nil, h.member), http.StatusAccepted)
	}
	expect(t, h.do("POST", "/api/sessions/"+string(other.ID)+"/compact", nil, h.member), http.StatusAccepted)
	deadline := time.Now().Add(3 * time.Second)
	for rn.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := rn.calls.Load(); n != 2 {
		t.Fatalf("Compact ran %d times for 6 requests over 2 sessions, want 2", n)
	}
	close(rn.release)
	h.srv.jobs.wait()
	expect(t, h.do("POST", "/api/sessions/"+string(sess.ID)+"/compact", nil, h.member), http.StatusAccepted)
	h.srv.jobs.wait()
	if n := rn.calls.Load(); n != 3 {
		t.Fatalf("a finished compaction blocked the next one (%d calls)", n)
	}
}

// strictAuditor refuses canceled contexts, as a synchronous SQLite write
// would.
type strictAuditor struct{ *fakes.Auditor }

func (a strictAuditor) Record(ctx context.Context, e core.AuditEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.Auditor.Record(ctx, e)
}

type strictBudget struct{ *fakes.Budget }

func (b strictBudget) RecordSpend(ctx context.Context, u core.UserID, c core.CallInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.Budget.RecordSpend(ctx, u, c)
}

// hangUpLLM answers, then the client hangs up before the handler records
// spend and audit.
type hangUpLLM struct {
	core.LLM
	hangUp context.CancelFunc
}

func (l hangUpLLM) Passthrough(ctx context.Context, ref core.ModelRef, req *core.Request, on func(core.StreamEvent) error) (*core.Response, core.CallInfo, error) {
	resp, ci, err := l.LLM.Passthrough(ctx, ref, req, on)
	l.hangUp()
	return resp, ci, err
}

func TestAuditAndSpendSurviveClientHangup(t *testing.T) {
	h := newHarness(t).setup()
	h.srv.d.Audit = strictAuditor{h.audit}
	h.srv.d.Budget = strictBudget{h.budget}
	ctx, hangUp := context.WithCancel(context.Background())
	h.srv.d.LLM = hangUpLLM{LLM: h.llm, hangUp: hangUp}
	h.llm.Steps = []fakes.LLMStep{{Text: []string{"hi"}, Usage: core.Usage{Input: 10, Output: 2}}}
	body := map[string]any{"model": "fake/fake-model", "messages": []map[string]any{{"role": "user", "content": "hello"}}}
	h.do("POST", "/v1/chat/completions", body, h.tokenFor(h.member, "llm"), withCtx(ctx))
	if len(h.budget.Calls) != 1 {
		t.Fatalf("spend of a call whose client hung up was not recorded (%d calls)", len(h.budget.Calls))
	}
	found := false
	for _, e := range h.audit.All() {
		found = found || e.Action == "api.passthrough"
	}
	if !found {
		t.Fatal("api.passthrough audit row lost when the client hung up")
	}
}

// failingPolicy grants until fail is reached, then errors.
type failingPolicy struct {
	*storePolicy
	mu    sync.Mutex
	left  int
	given []core.GrantID
}

func (p *failingPolicy) Grant(ctx context.Context, g core.Grant) (core.GrantID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.left == 0 {
		return "", fmt.Errorf("%w: policy store unavailable", core.ErrConflict)
	}
	p.left--
	id, err := p.storePolicy.Grant(ctx, g)
	p.given = append(p.given, id)
	return id, err
}

func TestScheduleCreateRollsBackOnGrantFailure(t *testing.T) {
	h := newHarness(t).setup()
	h.useStoreScheduler()
	fp := &failingPolicy{storePolicy: h.useStorePolicy(), left: 1}
	h.srv.d.Policy = fp
	grants := []core.Grant{
		{Kind: core.GrantDomain, Value: "api.weather.example:443", MaxSE: core.SERead},
		{Kind: core.GrantRecipient, Value: "telegram:42:", MaxSE: core.SEExternalSend},
	}
	rec := h.do("POST", "/api/schedules", dto.ScheduleUpsertReq{Schedule: core.Schedule{Name: "weather"}, Grants: grants}, h.member)
	expect(t, rec, http.StatusConflict)
	if l := decodeAs[[]core.Schedule](t, h.do("GET", "/api/schedules", nil, h.member)); len(l) != 0 {
		t.Fatalf("failed create left schedules behind: %+v", l)
	}
	v := core.Viewer{Principals: []core.PrincipalID{core.UserPrincipal(h.member.user)}}
	gs, err := h.store.Grants().List(context.Background(), v, core.GrantSchedule, "")
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	if len(gs) != 0 || len(fp.revoked) != len(fp.given) {
		t.Fatalf("grants left active: %+v (given %v, revoked %v)", gs, fp.given, fp.revoked)
	}
}

// activeRunner reports one active run, on the session in active.
type activeRunner struct {
	*fakes.Runner
	active core.SessionID
}

func (r *activeRunner) Active() []core.RunInfo {
	if r.active == "" {
		return nil
	}
	return []core.RunInfo{{Run: "run_busy", Session: r.active, Status: core.RunRunning}}
}

func TestV1BusySessionIsNotConsumed(t *testing.T) {
	h := newHarness(t).setup()
	ar := &activeRunner{Runner: h.runner}
	h.srv.d.Runner = ar
	tok := h.tokenFor(h.member, "chat")
	own := decodeAs[core.Session](t, h.do("POST", "/api/sessions", map[string]any{}, h.member))
	body := map[string]any{"model": "bothy", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	ar.active = own.ID
	rec := h.do("POST", "/v1/chat/completions", body, tok, withHeader("X-Bothy-Session", string(own.ID)))
	expect(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" || len(h.runner.Submitted()) != 0 {
		t.Fatalf("busy session: Retry-After %q, %d submits", rec.Header().Get("Retry-After"), len(h.runner.Submitted()))
	}
	// A run that starts between the check and Submit takes the message; a
	// retryable 429 would make the client send it twice.
	ar.active = ""
	h.runner.Result = core.SubmitResult{Steered: true}
	rec = h.do("POST", "/v1/chat/completions", body, tok, withHeader("X-Bothy-Session", string(own.ID)))
	expect(t, rec, http.StatusConflict)
	if errCode(t, rec) != "steered" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
