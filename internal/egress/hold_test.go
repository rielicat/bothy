package egress

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type connectResult struct {
	code int
	msg  string
}

// asyncConnect sends a bare CONNECT in the background, for refusals of held
// connections.
func asyncConnect(sock, authority string) <-chan connectResult {
	ch := make(chan connectResult, 1)
	go func() {
		code, msg, err := rawConnect(sock, authority)
		if err != nil {
			msg = err.Error()
		}
		ch <- connectResult{code, msg}
	}()
	return ch
}

// asyncGet fetches an https URL through the box proxy in the background; the
// upstream's certificate is not checked, but the client still sends SNI.
func asyncGet(sock, rawURL string) <-chan connectResult {
	ch := make(chan connectResult, 1)
	go func() {
		resp, err := proxyClient(sock, &tls.Config{InsecureSkipVerify: true}).Get(rawURL)
		if err != nil {
			ch <- connectResult{msg: err.Error()}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ch <- connectResult{resp.StatusCode, string(b)}
	}()
	return ch
}

func (e *testEnv) pendingApproval(n int) core.Approval {
	e.t.Helper()
	waitFor(e.t, "approval request", func() bool { return len(e.apr.All()) >= n })
	return e.apr.All()[n-1]
}

func (e *testEnv) resolve(id core.ApprovalID, c core.Choice) {
	e.t.Helper()
	if _, err := e.apr.Resolve(context.Background(), core.Resolution{ID: id, Choice: c, By: e.user, ByRole: core.RoleOwner, Via: "web"}); err != nil {
		e.t.Fatal(err)
	}
}

func result(t *testing.T, ch <-chan connectResult) connectResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("held connection never answered")
		return connectResult{}
	}
}

func TestHoldAllowOnceScopedToToolCall(t *testing.T) {
	e := newEnv(t, nil)
	e.res.set("pypi.org", []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	b := e.bind(key, true)

	ch := asyncGet(sock, "https://pypi.org/simple")
	ap := e.pendingApproval(1)
	if ap.ToolCallID != b.ToolCallID+"#net" || ap.Run != b.Run || ap.Session != e.sess || ap.Principal != e.principal() {
		t.Fatalf("approval = %+v", ap)
	}
	a := ap.Action
	if a.Tool != "net_allow" || a.Kind != core.ActNet || a.SideEffect != core.SEExternalSend || a.Host != "pypi.org" || a.Port != 443 ||
		a.Fingerprint != core.ActionFingerprint(a.Tool, a.Payload) {
		t.Fatalf("action = %+v", a)
	}
	if ap.Decision.Timeout != e.cfg.Egress.HoldForApproval.D() || ap.Decision.OnTimeout != core.TimeoutDeny {
		t.Fatalf("decision timeout = %v %s", ap.Decision.Timeout, ap.Decision.OnTimeout)
	}
	if want := []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}; !slices.Equal(ap.Decision.Choices, want) {
		t.Fatalf("choices = %v, want %v", ap.Decision.Choices, want)
	}
	if ap.Card.Title != "Sandbox wants to reach pypi.org:443" {
		t.Fatalf("card title = %q", ap.Card.Title)
	}
	in := e.pol.decided()[0]
	if in.Spec.Def.Name != "net_allow" || in.User != e.user || in.Trigger != core.TrigMessage || in.Session != e.sess ||
		in.Principal.ID != e.principal() {
		t.Fatalf("policy input = %+v", in)
	}
	e.resolve(ap.ID, core.AllowOnce)
	if r := result(t, ch); r.code != http.StatusOK || r.msg != "hello /simple" {
		t.Fatalf("allowed request = %+v", r)
	}

	if r := result(t, asyncGet(sock, "https://pypi.org/again")); r.code != http.StatusOK {
		t.Fatalf("second request in the same tool call = %+v", r)
	}
	if n := len(e.apr.All()); n != 1 {
		t.Fatalf("allow_once asked again within the tool call (%d approvals)", n)
	}

	e.bind(key, true)
	ch = asyncConnect(sock, "pypi.org:443")
	ap = e.pendingApproval(2)
	e.resolve(ap.ID, core.Deny)
	if r := result(t, ch); r.code != http.StatusForbidden || !strings.Contains(r.msg, "denied by the user") {
		t.Fatalf("denied CONNECT = %+v", r)
	}

	recs := e.records(3)
	var decisions []string
	for _, r := range recs {
		decisions = append(decisions, r.Decision)
	}
	slices.Sort(decisions)
	if want := []string{"allow", "held_allow", "held_deny"}; !slices.Equal(decisions, want) {
		t.Fatalf("decisions = %v, want %v", decisions, want)
	}
	if !slices.Contains(e.aud.Actions(), "egress.allow") {
		t.Fatalf("audit = %v, want egress.allow", e.aud.Actions())
	}
}

func TestHoldAllowSessionAndAlwaysCreateGrants(t *testing.T) {
	e := newEnv(t, nil)
	e.pol.Func = func(in core.PolicyInput) core.Decision {
		return core.Decision{Verdict: core.VerdictAsk, Rule: "floor:net_allow", Floor: true,
			Choices: []core.Choice{core.AllowSession, core.AllowAlways, core.Deny}}
	}
	e.res.set("pypi.org", []string{publicIP})
	e.res.set("files.pythonhosted.org", []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, true)

	ch := asyncGet(sock, "https://pypi.org/")
	ap := e.pendingApproval(1)
	if want := []core.Choice{core.AllowOnce, core.AllowSession, core.AllowAlways, core.Deny}; !slices.Equal(ap.Decision.Choices, want) {
		t.Fatalf("choices = %v, want %v", ap.Decision.Choices, want)
	}
	e.resolve(ap.ID, core.AllowSession)
	if r := result(t, ch); r.code != http.StatusOK {
		t.Fatalf("request after allow_session = %+v", r)
	}
	ch = asyncGet(sock, "https://files.pythonhosted.org/")
	e.resolve(e.pendingApproval(2).ID, core.AllowAlways)
	if r := result(t, ch); r.code != http.StatusOK {
		t.Fatalf("request after allow_always = %+v", r)
	}
	gs := e.pol.recorded()
	if len(gs) != 2 {
		t.Fatalf("grants = %+v", gs)
	}
	if g := gs[0]; g.Scope != core.GrantSession || g.ScopeID != string(e.sess) || g.Principal != e.principal() || g.CreatedBy != e.user {
		t.Fatalf("session grant = %+v", g)
	}
	if g := gs[1]; g.Scope != core.GrantPrincipal || g.ScopeID != "" || g.Principal != e.principal() {
		t.Fatalf("principal grant = %+v", g)
	}
}

func TestHoldTimesOut(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Egress.HoldForApproval = config.Duration(150 * time.Millisecond) })
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, true)
	start := time.Now()
	code, msg := connectStatus(t, sock, "pypi.org:443")
	if code != http.StatusForbidden || !strings.Contains(msg, "approval timed out") {
		t.Fatalf("timed-out hold = %d %q", code, msg)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 5*time.Second {
		t.Fatalf("hold lasted %v", d)
	}
	if n := len(e.apr.All()); n != 1 {
		t.Fatalf("approvals = %d", n)
	}
	if r := e.records(1)[0]; r.Decision != "held_deny" {
		t.Fatalf("record = %+v", r)
	}
}

func TestNoHoldWhenNotInteractiveDisabledOrDenied(t *testing.T) {
	cases := []struct {
		name        string
		mut         func(*config.Config)
		interactive bool
		verdict     core.Verdict
		want        string
	}{
		{"non-interactive", nil, false, core.VerdictAsk, "not in the allowlist"},
		{"hold off", func(c *config.Config) { c.Egress.HoldForApproval = 0 }, true, core.VerdictAsk, "not in the allowlist"},
		{"policy deny", nil, true, core.VerdictDeny, "denied by policy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.mut)
			e.pol.Default = c.verdict
			key, sock := e.openBox(core.BoxEgress{})
			e.bind(key, c.interactive)
			start := time.Now()
			code, msg := connectStatus(t, sock, "pypi.org:443")
			if code != http.StatusForbidden || !strings.Contains(msg, c.want) {
				t.Fatalf("= %d %q, want 403 %q", code, msg, c.want)
			}
			if time.Since(start) > time.Second {
				t.Fatal("refusal was not immediate")
			}
			if n := len(e.apr.All()); n != 0 {
				t.Fatalf("approvals requested: %d", n)
			}
		})
	}
}

func TestConcurrentHoldsShareOneApproval(t *testing.T) {
	e := newEnv(t, nil)
	e.res.set("pypi.org", []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, true)
	var chans []<-chan connectResult
	for range 3 {
		chans = append(chans, asyncGet(sock, "https://pypi.org/"))
	}
	ap := e.pendingApproval(1)
	time.Sleep(100 * time.Millisecond) // let the other connections join the hold
	e.resolve(ap.ID, core.AllowOnce)
	var wg sync.WaitGroup
	for _, ch := range chans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case r := <-ch:
				if r.code != http.StatusOK {
					t.Errorf("request = %+v", r)
				}
			case <-time.After(10 * time.Second):
				t.Error("request never answered")
			}
		}()
	}
	wg.Wait()
	if n := len(e.apr.All()); n != 1 {
		t.Fatalf("approvals = %d, want 1", n)
	}
}

func TestHoldForSharedTenantOffersOnlyAllowOnce(t *testing.T) {
	e := newEnv(t, nil)
	e.res.set("x.workers.dev", []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, true)
	ch := asyncGet(sock, "https://x.workers.dev/")
	ap := e.pendingApproval(1)
	if want := []core.Choice{core.AllowOnce, core.Deny}; !slices.Equal(ap.Decision.Choices, want) {
		t.Fatalf("choices = %v, want %v", ap.Decision.Choices, want)
	}
	e.resolve(ap.ID, core.AllowOnce)
	if r := result(t, ch); r.code != http.StatusOK {
		t.Fatalf("request = %+v", r)
	}
}
