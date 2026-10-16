package policy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

// statusLog records OnResolved callbacks from any goroutine.
type statusLog struct {
	mu sync.Mutex
	s  []core.ApprovalStatus
}

func (l *statusLog) add(a *core.Approval) {
	l.mu.Lock()
	l.s = append(l.s, a.Status)
	l.mu.Unlock()
}

func (l *statusLog) list() []core.ApprovalStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]core.ApprovalStatus(nil), l.s...)
}

// request files an approval for a send action decided by the engine.
func (h *harness) request(trig core.Trigger) *core.Approval {
	h.t.Helper()
	s := spec("send_mail", core.SEExternalSend)
	a := sendAction("send_mail", "bob@example.com", core.SEExternalSend)
	in := h.in(a, s)
	in.Trigger = trig
	d := h.decide(in)
	if d.Verdict != core.VerdictAsk {
		h.t.Fatalf("expected ask, got %+v", d)
	}
	run := h.newRun(h.session, h.ownerP.ID, h.owner.ID)
	chat := core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM}
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{
		Run: run, Session: h.session, Principal: h.ownerP.ID, ToolCallID: core.NewID(core.PrefixToolCall),
		Action: a, Spec: s, Decision: d, Chat: &chat, Context: "Telegram · DM",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return ap
}

func (h *harness) status(id core.ApprovalID) *core.Approval {
	h.t.Helper()
	a, err := h.store.Approvals().Get(h.ctx, core.SystemViewer(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func TestRequestPersistsRendersAndPublishes(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Server.PublicURL = "https://bothy.example.com/" })
	sub := h.bus.Subscribe(core.Filter{System: true}, 16)
	defer sub.Close()
	ap := h.request(core.TrigMessage)
	if ap.Status != core.ApprovalPending || ap.Card.Title != "Run send_mail" || ap.Card.WebURL != "https://bothy.example.com/approvals/"+string(ap.ID) {
		t.Errorf("approval %+v", ap)
	}
	if !ap.Card.Expires.Equal(ap.ExpiresAt) || ap.ExpiresAt.Sub(ap.CreatedAt) != 15*time.Minute {
		t.Errorf("expiry %v → %v", ap.CreatedAt, ap.ExpiresAt)
	}
	stored := h.status(ap.ID)
	if stored.Card.Title != ap.Card.Title || stored.Action.Fingerprint != ap.Action.Fingerprint {
		t.Errorf("stored %+v", stored)
	}
	ev := <-sub.C()
	if ev.Type != core.EvApprovalRequested || ev.Principal != h.ownerP.ID || ev.Data.(core.ApprovalData).Approval.ID != ap.ID {
		t.Errorf("event %+v", ev)
	}
	select {
	case ev := <-sub.C():
		t.Errorf("unexpected admin copy for a user approval: %+v", ev)
	default:
	}
	if acts := h.audit.Actions(); acts[len(acts)-1] != "approval.request" {
		t.Errorf("audit %v", acts)
	}
	if card := h.ap.RenderCard(core.ApprovalRequest{Action: ap.Action, Spec: spec("send_mail", core.SEExternalSend), Decision: ap.Decision}); card.WebURL != "" {
		t.Errorf("preview card has a web url: %q", card.WebURL)
	}
	if _, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: ap.Run, Session: h.session, Principal: h.ownerP.ID, Action: ap.Action, Decision: core.Decision{Verdict: core.VerdictAuto}}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("auto decision accepted: %v", err)
	}
}

func TestAdminOnlyApprovals(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.user("admin2", core.RoleAdmin)
	sub := h.bus.Subscribe(core.Filter{System: true}, 16)
	defer sub.Close()
	s := spec("schedule_create", core.SEAdmin)
	a := action("schedule_create", core.ActSchedule, core.SEAdmin, map[string]any{"cron": "0 8 * * *"})
	in := h.in(a, s)
	in.Principal, in.User, in.Role, in.Session = h.memberP, h.member.ID, core.RoleMember, h.memberSession
	d := h.decide(in)
	run := h.newRun(h.memberSession, h.memberP.ID, h.member.ID)
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: h.memberSession, Principal: h.memberP.ID, Action: a, Spec: s, Decision: d})
	if err != nil {
		t.Fatal(err)
	}
	var principals []core.PrincipalID
	for range 2 {
		principals = append(principals, (<-sub.C()).Principal)
	}
	if principals[0] != h.memberP.ID || principals[1] != "" {
		t.Errorf("event principals %v", principals)
	}
	adminViewer := core.Viewer{User: admin.ID, Principals: []core.PrincipalID{core.UserPrincipal(admin.ID)}, Admin: true}
	if got, err := h.ap.Get(h.ctx, adminViewer, ap.ID); err != nil || got.ID != ap.ID {
		t.Errorf("admin cannot see admin-only approval: %v", err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, By: h.member.ID, Via: "web"}); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("member resolved an admin-only approval: %v", err)
	}
	got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, By: admin.ID, Via: "web"})
	if err != nil || got.Status != core.ApprovalAllowed || got.DecidedBy != admin.ID {
		t.Fatalf("admin resolve: %+v %v", got, err)
	}
	for range 2 {
		ev := <-sub.C()
		if ev.Type != core.EvApprovalResolved || ev.Data.(core.ApprovalResolvedData).Status != core.ApprovalAllowed {
			t.Errorf("resolved event %+v", ev)
		}
	}
}

// TestDecisionReasonAndRequester: a deny's reason is kept on the approval
// and sent with approval.resolved; every approval names who asked, an
// admin-only one through its run.
func TestDecisionReasonAndRequester(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.store.Users().Update(h.ctx, &core.User{ID: h.member.ID, Username: "member", DisplayName: "Mel", Role: core.RoleMember}); err != nil {
		t.Fatal(err)
	}
	sub := h.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvApprovalResolved}}, 16)
	defer sub.Close()

	ap := h.request(core.TrigMessage)
	if ap.Requester == nil || *ap.Requester != (core.ApprovalPerson{User: h.owner.ID, Name: "owner"}) || ap.Approvals != nil {
		t.Errorf("personal approval requester %+v approvals %+v", ap.Requester, ap.Approvals)
	}
	got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, By: h.owner.ID, Via: "web", Comment: "Wrong address"})
	if err != nil || got.Comment != "Wrong address" {
		t.Fatalf("deny = %+v, %v", got, err)
	}
	if ev := <-sub.C(); ev.Data.(core.ApprovalResolvedData).Comment != "Wrong address" {
		t.Errorf("resolved event %+v", ev.Data)
	}
	if stored, _ := h.ap.Get(h.ctx, core.SystemViewer(), ap.ID); stored.Comment != "Wrong address" {
		t.Errorf("stored comment %q", stored.Comment)
	}

	s := spec("schedule_create", core.SEAdmin)
	a := action("schedule_create", core.ActSchedule, core.SEAdmin, map[string]any{"name": "x"})
	in := h.in(a, s)
	in.Principal, in.User, in.Role, in.Session = h.memberP, h.member.ID, core.RoleMember, h.memberSession
	run := h.newRun(h.memberSession, h.memberP.ID, h.member.ID)
	admin, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: h.memberSession, Principal: h.memberP.ID, Action: a, Spec: s, Decision: h.decide(in)})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Requester == nil || *admin.Requester != (core.ApprovalPerson{User: h.member.ID, Name: "Mel"}) {
		t.Errorf("admin-only requester %+v", admin.Requester)
	}
	list, err := h.ap.Pending(h.ctx, core.SystemViewer())
	if err != nil || len(list) != 1 || list[0].Requester == nil || list[0].Requester.Name != "Mel" {
		t.Errorf("pending = %+v, %v", list, err)
	}
}

func TestResolveAuthority(t *testing.T) {
	h := newHarness(t, nil)
	ap := h.request(core.TrigMessage)
	chat := core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM}
	full := core.MsgRef{Chat: chat, ID: "100"}
	short := core.MsgRef{Chat: chat, ID: "101"}
	if err := h.ap.AddMessageRef(h.ctx, ap.ID, full, false); err != nil {
		t.Fatal(err)
	}
	if err := h.ap.AddMessageRef(h.ctx, ap.ID, short, true); err != nil {
		t.Fatal(err)
	}
	refs, err := h.ap.MessageRefs(h.ctx, ap.ID)
	if err != nil || len(refs) != 2 {
		t.Fatalf("refs %v %v", refs, err)
	}
	unrecorded := core.MsgRef{Chat: chat, ID: "999"}
	otherChat := core.MsgRef{Chat: core.ChatRef{Channel: "telegram", ChatID: "43"}, ID: "100"}
	disabled := h.user("gone", core.RoleAdmin)
	disabled.Disabled = true
	if err := h.store.Users().Update(h.ctx, disabled); err != nil {
		t.Fatal(err)
	}
	reject := map[string]core.Resolution{
		"non-approver":          {ID: ap.ID, Choice: core.AllowOnce, By: h.member.ID, Via: "web"},
		"no user":               {ID: ap.ID, Choice: core.AllowOnce, Via: "web"},
		"disabled user":         {ID: ap.ID, Choice: core.AllowOnce, By: disabled.ID, Via: "web"},
		"unrecorded message":    {ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "telegram", ViaMsg: &unrecorded},
		"message in other chat": {ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "telegram", ViaMsg: &otherChat},
		"no message":            {ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "telegram"},
		"channel mismatch":      {ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "discord", ViaMsg: &full},
		"approve truncated":     {ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "telegram", ViaMsg: &short},
		"session on truncated":  {ID: ap.ID, Choice: core.AllowSession, By: h.owner.ID, Via: "telegram", ViaMsg: &short},
	}
	for name, r := range reject {
		if _, err := h.ap.Resolve(h.ctx, r); !errors.Is(err, core.ErrForbidden) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	invalid := map[string]core.Resolution{
		"unknown choice": {ID: ap.ID, Choice: "allow_forever", By: h.owner.ID, Via: "web"},
		"reserved via":   {ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "timeout"},
		"bad via":        {ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "Web UI"},
	}
	for name, r := range invalid {
		if _, err := h.ap.Resolve(h.ctx, r); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if h.status(ap.ID).Status != core.ApprovalPending {
		t.Fatal("a rejected resolution changed the approval")
	}
	got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, By: h.owner.ID, Via: "telegram", ViaMsg: &short})
	if err != nil || got.Status != core.ApprovalDenied || got.DecidedVia != "telegram" {
		t.Errorf("deny from a truncated card: %+v %v", got, err)
	}
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "web"}); !errors.Is(err, core.ErrConflict) || strings.Contains(err.Error(), string(ap.ID)) {
		t.Errorf("second resolve: %v", err)
	}

	ap2 := h.request(core.TrigMessage)
	if err := h.ap.AddMessageRef(h.ctx, ap2.ID, full, false); err != nil {
		t.Fatal(err)
	}
	if got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap2.ID, Choice: core.AllowAlways, By: h.owner.ID, Via: "telegram", ViaMsg: &full}); err != nil || got.Choice != core.AllowAlways {
		t.Errorf("chat allow_always: %+v %v", got, err)
	}

	ap3 := h.request(core.TrigSchedule)
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap3.ID, Choice: core.AllowAlways, By: h.owner.ID, Via: "web"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("allow_always on a scheduled-run card: %v", err)
	}
}

// TestAllowAlwaysCardFlow walks the runner's allow_always path end to end:
// card → allow_always → principal grant → no card in a new session →
// revoke → card again.
func TestAllowAlwaysCardFlow(t *testing.T) {
	h := newHarness(t, nil)
	s := spec("send_mail", core.SEExternalSend)
	a := sendAction("send_mail", "bob@example.com", core.SEExternalSend)
	ap := h.request(core.TrigMessage)
	if !slices.Contains(ap.Card.Choices, core.AllowAlways) {
		t.Fatalf("card choices %v", ap.Card.Choices)
	}
	got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowAlways, By: h.owner.ID, Via: "web"})
	if err != nil || got.Choice != core.AllowAlways {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	g, ok := h.engine.NarrowGrant(got.Action, got.Principal, core.GrantPrincipal, "", core.Decider{User: got.DecidedBy})
	if !ok {
		t.Fatal("no principal grant for the card's action")
	}
	id, err := h.engine.Grant(h.ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	in := h.in(a, s)
	in.Session = h.newSession(h.ownerP.ID)
	if d := h.decide(in); d.Verdict != core.VerdictAuto || d.Rule != "grant:"+string(id) {
		t.Errorf("new session: %+v", d)
	}
	list, err := h.store.Grants().List(h.ctx, core.Viewer{Principals: []core.PrincipalID{h.ownerP.ID}}, core.GrantPrincipal, "")
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Errorf("grant not listed for the owner: %v %v", list, err)
	}
	if err := h.engine.Revoke(h.ctx, core.Viewer{Principals: []core.PrincipalID{h.ownerP.ID}}, id, h.owner.ID); err != nil {
		t.Fatal(err)
	}
	if d := h.decide(in); d.Verdict != core.VerdictAsk {
		t.Errorf("after revoke: %+v", d)
	}
}

func TestRequestNormalisesChoices(t *testing.T) {
	h := newHarness(t, nil)
	run := h.newRun(h.session, h.ownerP.ID, h.owner.ID)
	a := action("schedule_create", core.ActSchedule, core.SEAdmin, map[string]any{"x": 1})
	ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{
		Run: run, Session: h.session, Principal: h.ownerP.ID, Action: a, Spec: spec("schedule_create", core.SEAdmin),
		Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{AdminOnly: true},
			Choices: []core.Choice{core.AllowSession, core.AllowAlways}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ap.Decision.Choices, []core.Choice{core.AllowOnce, core.Deny}) || !slices.Equal(ap.Card.Choices, ap.Decision.Choices) {
		t.Errorf("admin-only choices %v / card %v", ap.Decision.Choices, ap.Card.Choices)
	}
	if ap.OnTimeout != core.TimeoutDeny || ap.ExpiresAt.Sub(ap.CreatedAt) != 15*time.Minute {
		t.Errorf("defaults: %s %v", ap.OnTimeout, ap.ExpiresAt.Sub(ap.CreatedAt))
	}
	if _, err := h.ap.Request(h.ctx, core.ApprovalRequest{Run: run, Session: h.session, Principal: h.ownerP.ID, Action: a,
		Decision: core.Decision{Verdict: core.VerdictAsk}}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("request without an approver: %v", err)
	}
}

func TestConcurrentResolveOneWins(t *testing.T) {
	h := newHarness(t, nil)
	ap := h.request(core.TrigMessage)
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := core.AllowOnce
			if i%2 == 0 {
				c = core.Deny
			}
			_, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: c, By: h.owner.ID, Via: "web"})
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, core.ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("resolve: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 31 {
		t.Errorf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
}

func TestAwaitResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ap := h.request(core.TrigMessage)
		var resolved statusLog
		h.ap.OnResolved(resolved.add)
		done := make(chan core.Choice)
		go func() {
			c, err := h.ap.Await(h.ctx, ap.ID)
			if err != nil {
				t.Error(err)
			}
			done <- c
		}()
		time.Sleep(time.Minute)
		synctest.Wait()
		if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowSession, By: h.owner.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		if c := <-done; c != core.AllowSession {
			t.Errorf("choice %s", c)
		}
		if got := resolved.list(); len(got) != 1 || got[0] != core.ApprovalAllowed {
			t.Errorf("callbacks %v", got)
		}

		denied := h.request(core.TrigMessage)
		go func() {
			c, err := h.ap.Await(h.ctx, denied.ID)
			if err != nil || c != core.Deny {
				t.Errorf("deny: %s %v", c, err)
			}
			done <- c
		}()
		synctest.Wait()
		if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: denied.ID, Choice: core.Deny, By: h.owner.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		<-done
	})
}

func TestAwaitTimeoutDeny(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ap := h.request(core.TrigMessage)
		var expired atomic.Int32
		h.ap.OnResolved(func(a *core.Approval) {
			if a.Status == core.ApprovalExpired {
				expired.Add(1)
			}
		})
		start := time.Now()
		_, err := h.ap.Await(h.ctx, ap.ID)
		if !errors.Is(err, core.ErrApprovalExpired) {
			t.Fatalf("err = %v", err)
		}
		if waited := time.Since(start); waited < 15*time.Minute-time.Second || waited > 15*time.Minute+time.Second {
			t.Errorf("waited %v", waited)
		}
		got := h.status(ap.ID)
		if got.Status != core.ApprovalExpired || got.DecidedVia != "timeout" || expired.Load() != 1 {
			t.Errorf("after timeout: %+v callbacks=%d", got, expired.Load())
		}
		if !strings.Contains(strings.Join(h.audit.Actions(), ","), "approval.expire") {
			t.Errorf("expiry not audited: %v", h.audit.Actions())
		}
		if _, err := h.ap.Await(h.ctx, ap.ID); !errors.Is(err, core.ErrApprovalExpired) {
			t.Errorf("await after expiry: %v", err)
		}
	})
}

func TestAwaitParkAndParkTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ap := h.request(core.TrigSchedule)
		if ap.OnTimeout != core.TimeoutPark || ap.ExpiresAt.Sub(ap.CreatedAt) != 2*time.Minute {
			t.Fatalf("scheduled approval %+v", ap)
		}
		var callbacks statusLog
		h.ap.OnResolved(callbacks.add)
		if _, err := h.ap.Await(h.ctx, ap.ID); !errors.Is(err, core.ErrParked) {
			t.Fatalf("err = %v", err)
		}
		got := h.status(ap.ID)
		if got.Status != core.ApprovalParked || got.ParkUntil.Sub(time.Now()) < 24*time.Hour-time.Second {
			t.Fatalf("parked %+v", got)
		}
		if got := callbacks.list(); len(got) != 0 {
			t.Errorf("parking is not a resolution: %v", got)
		}
		if pending, err := h.ap.Pending(h.ctx, core.Viewer{Principals: []core.PrincipalID{h.ownerP.ID}}); err != nil || len(pending) != 1 {
			t.Errorf("parked approvals are pending: %v %v", pending, err)
		}

		ctx, cancel := context.WithCancel(h.ctx)
		defer cancel()
		go func() { _ = h.ap.Run(ctx) }()
		time.Sleep(24*time.Hour + time.Minute)
		synctest.Wait()
		if got := h.status(ap.ID); got.Status != core.ApprovalExpired {
			t.Errorf("park TTL: %+v", got)
		}
		if got := callbacks.list(); len(got) != 1 || got[0] != core.ApprovalExpired {
			t.Errorf("callbacks %v", got)
		}
	})
}

func TestParkedApprovalCanBeResolved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ap := h.request(core.TrigHeartbeat)
		if _, err := h.ap.Await(h.ctx, ap.ID); !errors.Is(err, core.ErrParked) {
			t.Fatal(err)
		}
		var seen *core.Approval
		h.ap.OnResolved(func(a *core.Approval) { seen = a })
		got, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "web"})
		if err != nil || got.Status != core.ApprovalAllowed {
			t.Fatalf("resolve parked: %+v %v", got, err)
		}
		if seen == nil || seen.ID != ap.ID || seen.Choice != core.AllowOnce {
			t.Errorf("runner callback %+v", seen)
		}
	})
}

func TestSweeperAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ap := h.request(core.TrigMessage)
		// A new process over the same store: no Await is running.
		_, ap2, err := New(Deps{Store: h.store, Config: config.Static(h.cfg), Bus: h.bus, Audit: h.audit})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(h.ctx)
		defer cancel()
		go func() { _ = ap2.Run(ctx) }()
		time.Sleep(15*time.Minute + 31*time.Second)
		synctest.Wait()
		if got := h.status(ap.ID); got.Status != core.ApprovalExpired {
			t.Errorf("sweeper: %+v", got)
		}
	})
}

func TestCancelRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ap := h.request(core.TrigMessage)
		var callbacks atomic.Int32
		h.ap.OnResolved(func(*core.Approval) { callbacks.Add(1) })
		errc := make(chan error)
		go func() {
			_, err := h.ap.Await(h.ctx, ap.ID)
			errc <- err
		}()
		synctest.Wait()
		if err := h.ap.CancelRun(h.ctx, ap.Run); err != nil {
			t.Fatal(err)
		}
		if err := <-errc; !errors.Is(err, core.ErrStopped) {
			t.Errorf("await: %v", err)
		}
		if got := h.status(ap.ID); got.Status != core.ApprovalCancelled || got.DecidedVia != "stop" {
			t.Errorf("cancelled %+v", got)
		}
		if callbacks.Load() != 0 {
			t.Error("CancelRun fired callbacks")
		}
		if err := h.ap.CancelRun(h.ctx, ap.Run); err != nil {
			t.Errorf("second cancel: %v", err)
		}
	})
}

func TestAwaitContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		ap := h.request(core.TrigMessage)
		ctx, cancel := context.WithCancelCause(h.ctx)
		errc := make(chan error)
		go func() {
			_, err := h.ap.Await(ctx, ap.ID)
			errc <- err
		}()
		synctest.Wait()
		cancel(core.ErrStopped)
		if err := <-errc; !errors.Is(err, core.ErrStopped) {
			t.Errorf("await: %v", err)
		}
		if h.status(ap.ID).Status != core.ApprovalPending {
			t.Error("a cancelled wait changed the approval")
		}
	})
}

func TestButtonData(t *testing.T) {
	h := newHarness(t, nil)
	id := core.ApprovalID(core.NewID(core.PrefixApproval))
	for _, c := range []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny} {
		data := h.ap.ButtonData(id, c)
		if len(data) > 64 || !strings.HasPrefix(data, "a:"+string(id)+":") {
			t.Errorf("%s: %q (%d bytes)", c, data, len(data))
		}
		gotID, gotC, err := h.ap.ParseButtonData(data)
		if err != nil || gotID != id || gotC != c {
			t.Errorf("%s round trip: %s %s %v", c, gotID, gotC, err)
		}
	}
	if h.ap.ButtonData(id, "allow_forever") != "" {
		t.Error("button for an unknown choice")
	}
	good := h.ap.ButtonData(id, core.Deny)
	parts := strings.Split(good, ":")
	flip := func(s string) string {
		b := []byte(s)
		if b[0] == 'a' {
			b[0] = 'b'
		} else {
			b[0] = 'a'
		}
		return string(b)
	}
	other := core.ApprovalID(core.NewID(core.PrefixApproval))
	for name, data := range map[string]string{
		"mac tamper":    strings.Join([]string{parts[0], parts[1], parts[2], flip(parts[3])}, ":"),
		"choice swap":   strings.Join([]string{parts[0], parts[1], "o", parts[3]}, ":"),
		"id swap":       strings.Join([]string{parts[0], string(other), parts[2], parts[3]}, ":"),
		"stop data":     "s:" + core.NewID(core.PrefixRun),
		"bad id":        "a:apr_x:o:" + parts[3],
		"extra field":   good + ":x",
		"too long":      good + strings.Repeat("x", 64),
		"unknown":       strings.Join([]string{parts[0], parts[1], "z", parts[3]}, ":"),
		"empty":         "",
		"no mac":        strings.Join(parts[:3], ":"),
		"upper-case id": strings.Join([]string{parts[0], strings.ToUpper(parts[1]), parts[2], parts[3]}, ":"),
	} {
		if _, _, err := h.ap.ParseButtonData(data); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: accepted %q (%v)", name, data, err)
		}
	}
	// The key persists in meta: a restart verifies old buttons; another
	// install does not.
	_, again, err := New(Deps{Store: h.store, Config: config.Static(h.cfg)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := again.ParseButtonData(good); err != nil {
		t.Errorf("after restart: %v", err)
	}
	_, fresh, err := New(Deps{Store: memstore.New(), Config: config.Static(h.cfg)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh.ParseButtonData(good); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("another key accepted the mac: %v", err)
	}
}

func TestMACKeyStorage(t *testing.T) {
	st := memstore.New()
	if _, _, err := New(Deps{Store: st, Config: config.Static(config.Default())}); err != nil {
		t.Fatal(err)
	}
	v, err := st.Meta().Get(context.Background(), "approval_mac_key")
	if err != nil || len(v) != 64 {
		t.Fatalf("meta key %q %v", v, err)
	}
	if err := st.Meta().Set(context.Background(), "approval_mac_key", "not hex"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(Deps{Store: st, Config: config.Static(config.Default())}); err == nil {
		t.Error("corrupt key accepted")
	}
	if _, _, err := New(Deps{Config: config.Static(config.Default())}); err == nil {
		t.Error("missing store accepted")
	}
}

func TestRecentAndViewerScoping(t *testing.T) {
	h := newHarness(t, nil)
	ap := h.request(core.TrigMessage)
	if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.Deny, By: h.owner.ID, Via: "web"}); err != nil {
		t.Fatal(err)
	}
	owner := core.Viewer{Principals: []core.PrincipalID{h.ownerP.ID}}
	member := core.Viewer{Principals: []core.PrincipalID{h.memberP.ID}}
	if recent, err := h.ap.Recent(h.ctx, owner, core.Page{}); err != nil || len(recent) != 1 {
		t.Errorf("recent %v %v", recent, err)
	}
	if recent, err := h.ap.Recent(h.ctx, member, core.Page{}); err != nil || len(recent) != 0 {
		t.Errorf("member sees owner's approvals: %v %v", recent, err)
	}
	if _, err := h.ap.Get(h.ctx, member, ap.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("member get: %v", err)
	}
}

// TestAwaitLeavesNoWaiters checks that the per-approval change channels go
// away with the last Await, however it returns.
func TestAwaitLeavesNoWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		waiters := func() int {
			h.ap.mu.Lock()
			defer h.ap.mu.Unlock()
			return len(h.ap.changed)
		}
		resolved := h.request(core.TrigMessage)
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() {
				if c, err := h.ap.Await(h.ctx, resolved.ID); err != nil || c != core.AllowOnce {
					t.Errorf("await: %s %v", c, err)
				}
			})
		}
		synctest.Wait()
		if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: resolved.ID, Choice: core.AllowOnce, By: h.owner.ID, Via: "web"}); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if _, err := h.ap.Await(h.ctx, resolved.ID); err != nil {
			t.Fatal(err)
		}

		abandoned := h.request(core.TrigMessage)
		ctx, cancel := context.WithCancel(h.ctx)
		go cancel()
		if _, err := h.ap.Await(ctx, abandoned.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled await: %v", err)
		}
		if n := waiters(); n != 0 {
			t.Errorf("%d waiter entries left behind", n)
		}
	})
}

// TestTimeoutCallbacksMayReenter checks that OnResolved callbacks for an
// expiry run after the transition lock is released, so a callback that
// touches another due approval does not deadlock.
func TestTimeoutCallbacksMayReenter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		first, second := h.request(core.TrigMessage), h.request(core.TrigMessage)
		var inner error
		h.ap.OnResolved(func(a *core.Approval) {
			if a.ID == first.ID {
				_, inner = h.ap.Await(h.ctx, second.ID)
			}
		})
		time.Sleep(16 * time.Minute)
		if _, err := h.ap.Await(h.ctx, first.ID); !errors.Is(err, core.ErrApprovalExpired) {
			t.Fatalf("await: %v", err)
		}
		if !errors.Is(inner, core.ErrApprovalExpired) {
			t.Errorf("callback await: %v", inner)
		}
		for _, id := range []core.ApprovalID{first.ID, second.ID} {
			if st := h.status(id).Status; st != core.ApprovalExpired {
				t.Errorf("%s: %s", id, st)
			}
		}
	})
}

// TestRequestEnforcesOfferableChoices covers callers that build choices
// outside Decide (egress holds): allow_always never reaches a card for a run
// no human started, an irreversible action, or without allow_session.
func TestRequestEnforcesOfferableChoices(t *testing.T) {
	h := newHarness(t, nil)
	runFor := func(trig core.Trigger) core.RunID {
		s := h.newSession(h.ownerP.ID)
		r := &core.Run{Session: s, Principal: h.ownerP.ID, User: h.owner.ID, Trigger: trig, Status: core.RunQueued, CreatedAt: time.Now()}
		if err := h.store.Runs().Create(h.ctx, r); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	all := []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny}
	net := action("net_allow", core.ActNet, core.SEExternalSend, map[string]any{"host": "example.com", "port": 443}, func(a *core.Action) { a.Host, a.Port = "example.com", 443 })
	del := action("fs_delete", core.ActFSDelete, core.SEIrreversible, map[string]any{"path": "a/b"})
	for _, tc := range []struct {
		name   string
		trig   core.Trigger
		a      *core.Action
		in     []core.Choice
		expect []core.Choice
	}{
		{"interactive", core.TrigMessage, net, all, all},
		{"schedule", core.TrigSchedule, net, all, []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}},
		{"heartbeat", core.TrigHeartbeat, net, all, []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}},
		{"api", core.TrigAPI, net, all, []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}},
		{"irreversible", core.TrigMessage, del, all, []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}},
		{"always without session", core.TrigMessage, net, []core.Choice{core.AllowAlways, core.Deny}, []core.Choice{core.AllowOnce, core.Deny}},
		{"unknown and duplicate", core.TrigMessage, net, []core.Choice{"approve_all", core.AllowOnce, core.AllowOnce, core.Deny}, []core.Choice{core.AllowOnce, core.Deny}},
	} {
		run := runFor(tc.trig)
		runRow, err := h.store.Runs().Get(h.ctx, core.SystemViewer(), run)
		if err != nil {
			t.Fatal(err)
		}
		ap, err := h.ap.Request(h.ctx, core.ApprovalRequest{
			Run: run, Session: runRow.Session, Principal: h.ownerP.ID, Action: tc.a, Spec: spec(tc.a.Tool, tc.a.SideEffect),
			Decision: core.Decision{Verdict: core.VerdictAsk, Approver: core.ApproverReq{User: h.owner.ID}, Choices: tc.in},
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(ap.Decision.Choices, tc.expect) || !slices.Equal(ap.Card.Choices, tc.expect) {
			t.Errorf("%s: choices %v card %v, want %v", tc.name, ap.Decision.Choices, ap.Card.Choices, tc.expect)
		}
		if slices.Contains(tc.expect, core.AllowAlways) {
			continue
		}
		if _, err := h.ap.Resolve(h.ctx, core.Resolution{ID: ap.ID, Choice: core.AllowAlways, By: h.owner.ID, Via: "web"}); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: allow_always resolved: %v", tc.name, err)
		}
	}
}
