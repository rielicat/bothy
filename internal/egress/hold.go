package egress

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

type holdResult struct {
	ok     bool
	reason string
}

// holdGroup collapses concurrent holds for the same box, tool call and
// destination into one approval card (pip opens several connections at once).
type holdGroup struct {
	mu    sync.Mutex
	calls map[string]*holdCall
}

type holdCall struct {
	done chan struct{}
	res  holdResult
}

func (g *holdGroup) do(key string, fn func() holdResult) holdResult {
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-c.done
		return c.res
	}
	if g.calls == nil {
		g.calls = map[string]*holdCall{}
	}
	c := &holdCall{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(c.done)
	}()
	c.res = fn()
	return c.res
}

// holdForApproval keeps an unknown CONNECT open while the user decides
// (SPEC §5.8 "Egress holds"). The wait is bounded by egress.hold_for_approval
// and by the box's lifetime, not by the waiting connection.
func (s *Service) holdForApproval(b *box, be core.BoxEgress, bind core.EgressBinding, h hostname, port int, shared bool) holdResult {
	key := b.key + "|" + bind.ToolCallID + "|" + h.hostport(port)
	return s.holds.do(key, func() holdResult { return s.requestHold(b.ctx, b, be, bind, h, port, shared) })
}

func (s *Service) requestHold(ctx context.Context, b *box, be core.BoxEgress, bind core.EgressBinding, h hostname, port int, shared bool) holdResult {
	hp := h.hostport(port)
	a, err := netAction(h, port)
	if err != nil {
		return holdResult{reason: "approval unavailable"}
	}
	pr, err := s.store.Principals().Get(ctx, core.SystemViewer(), be.Principal)
	if err != nil {
		s.log.Warn("egress: hold without a principal", "principal", be.Principal, "err", err)
		return holdResult{reason: "approval unavailable"}
	}
	var taint core.Taint
	if be.Session != "" {
		if sess, err := s.store.Sessions().Get(ctx, core.SystemViewer(), be.Session); err == nil {
			taint = sess.Taint
		}
	}
	spec := netAllowSpec()
	spec.Manifest.Title = "Sandbox wants to reach " + hp
	d := s.policy.Decide(ctx, core.PolicyInput{
		User: bind.User, Role: bind.Role, Principal: *pr, Session: be.Session, Schedule: bind.Schedule,
		Trigger: bind.Trigger, Action: a, Spec: spec, Taint: taint, Chat: bind.Chat,
	})
	if d.Verdict == core.VerdictDeny {
		return holdResult{reason: "denied by policy (" + d.Rule + "): " + d.Reason}
	}
	wait := s.cfg().Egress.HoldForApproval.D()
	d.Verdict, d.Timeout, d.OnTimeout = core.VerdictAsk, wait, core.TimeoutDeny
	d.Choices = holdChoices(d.Choices, shared)
	ap, err := s.approvals.Request(ctx, core.ApprovalRequest{
		Run: bind.Run, Session: be.Session, Principal: be.Principal, ToolCallID: bind.ToolCallID + "#net",
		Action: a, Spec: spec, Decision: d, Chat: bind.Chat, Context: bind.Context,
	})
	if err != nil {
		s.log.Warn("egress: approval request failed", "host", hp, "err", err)
		return holdResult{reason: "approval unavailable"}
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	// A refusal is remembered for the rest of the tool call: clients retry
	// failed connections, and each retry would otherwise ask again.
	refused := func(reason string) holdResult {
		b.addRefused(bind.ToolCallID, hp, reason)
		return holdResult{reason: reason}
	}
	choice, err := s.approvals.Await(wctx, ap.ID)
	switch {
	case errors.Is(err, core.ErrApprovalExpired), errors.Is(err, context.DeadlineExceeded):
		return refused("approval timed out")
	case err != nil:
		return refused("approval not granted")
	}
	switch choice {
	case core.AllowOnce:
	case core.AllowSession, core.AllowAlways:
		scope, scopeID := core.GrantSession, string(be.Session)
		if choice == core.AllowAlways {
			scope, scopeID = core.GrantPrincipal, ""
		}
		if g, ok := s.policy.NarrowGrant(a, be.Principal, scope, scopeID, s.decidedBy(ctx, ap.ID, bind.User)); ok {
			if _, err := s.policy.Grant(ctx, g); err != nil {
				s.log.Warn("egress: storing the approved grant failed", "host", hp, "err", err)
			}
		}
	case core.Deny:
		return refused("denied by the user")
	default:
		return refused("approval not granted")
	}
	// The approved call proceeds even if the grant could not be stored.
	b.addAllowOnce(bind.ToolCallID, hp)
	s.audit(ctx, "egress.allow", hp, "ok", be.Session, bind.Run,
		map[string]any{"dialer": "box", "approval": string(ap.ID), "choice": string(choice)})
	return holdResult{ok: true}
}

// holdChoices: allow_once always; session and permanent grants only where the
// policy offers them and never for shared-tenant hosts, which grants cannot
// admit anyway.
func holdChoices(offered []core.Choice, shared bool) []core.Choice {
	out := []core.Choice{core.AllowOnce}
	if !shared {
		for _, c := range []core.Choice{core.AllowSession, core.AllowAlways} {
			if slices.Contains(offered, c) {
				out = append(out, c)
			}
		}
	}
	return append(out, core.Deny)
}

func (s *Service) decidedBy(ctx context.Context, id core.ApprovalID, fallback core.UserID) core.UserID {
	if a, err := s.approvals.Get(ctx, core.SystemViewer(), id); err == nil && a.DecidedBy != "" {
		return a.DecidedBy
	}
	return fallback
}
