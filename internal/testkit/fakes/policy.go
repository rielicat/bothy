package fakes

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var (
	_ core.Policy    = (*Policy)(nil)
	_ core.Approvals = (*Approvals)(nil)
)

// Approval timeouts mirror the policy.approval defaults (SPEC §5.9).
const (
	InteractiveTimeout = 15 * time.Minute
	ScheduledTimeout   = 2 * time.Minute
	ParkTTL            = 24 * time.Hour
)

// Policy returns Verdicts[tool] (default Default) with Rule "fake".
//
// An empty Default means auto. Ask decisions offer allow_once, allow_session
// and deny, time out after InteractiveTimeout with deny, or ScheduledTimeout
// with park for scheduled triggers. Func, when set, replaces the table.
// Grants are recorded but never consulted by Decide.
type Policy struct {
	core.Policy
	Verdicts map[string]core.Verdict
	Default  core.Verdict
	Func     func(in core.PolicyInput) core.Decision

	mu      sync.Mutex
	Inputs  []core.PolicyInput
	Grants  []core.Grant
	Revoked []core.GrantID
}

func (p *Policy) Decide(_ context.Context, in core.PolicyInput) core.Decision {
	p.mu.Lock()
	p.Inputs = append(p.Inputs, in)
	fn := p.Func
	tool := in.Spec.Def.Name
	if in.Action != nil && in.Action.Tool != "" {
		tool = in.Action.Tool
	}
	v, ok := p.Verdicts[tool]
	if !ok {
		v = p.Default
	}
	p.mu.Unlock()
	if fn != nil {
		return fn(in)
	}
	if v == "" {
		v = core.VerdictAuto
	}
	d := core.Decision{Verdict: v, Rule: "fake", Reason: "fake policy", Approver: core.ApproverReq{User: in.User}}
	if v == core.VerdictAsk {
		d.Choices = []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}
		d.Timeout, d.OnTimeout = InteractiveTimeout, core.TimeoutDeny
		if in.Trigger.Scheduled() {
			d.Timeout, d.OnTimeout = ScheduledTimeout, core.TimeoutPark
		}
	}
	return d
}

// NarrowGrant returns a tool grant pinned to the exact canonical payload.
func (p *Policy) NarrowGrant(a *core.Action, pr core.PrincipalID, scope core.GrantScope, scopeID string, by core.UserID) (core.Grant, bool) {
	if a == nil {
		return core.Grant{}, false
	}
	return core.Grant{
		Principal: pr, Scope: scope, ScopeID: scopeID, Kind: core.GrantTool, Tool: a.Tool,
		Pattern: map[string]string{"": string(a.Payload)}, MaxSE: a.SideEffect,
		CreatedBy: by, CreatedAt: time.Now(),
	}, true
}

func (p *Policy) Grant(_ context.Context, g core.Grant) (core.GrantID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if g.ID == "" {
		g.ID = core.GrantID(core.NewID(core.PrefixGrant))
	}
	p.Grants = append(p.Grants, g)
	return g.ID, nil
}

func (p *Policy) Revoke(_ context.Context, v core.Viewer, id core.GrantID, _ core.UserID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, g := range p.Grants {
		if g.ID == id && v.Can(g.Principal) {
			p.Revoked = append(p.Revoked, id)
			return nil
		}
	}
	return fmt.Errorf("grant %s: %w", id, core.ErrNotFound)
}

// NextTaint adds the manifest's legs, plus Untrusted for an untrusted result
// or box egress.
func (p *Policy) NextTaint(prev core.Taint, spec core.ToolSpec, _ *core.Action, res *core.ToolResult, boxEgressed bool) core.Taint {
	t := prev | spec.Manifest.Taint
	if boxEgressed || res != nil && res.Untrusted {
		t |= core.TaintUntrusted
	}
	return t
}

// Approvals resolves every request immediately with Choice (or blocks until
// Resolve when Manual is true). Records requests.
//
// An empty Choice means allow_once. Immediate resolutions do not invoke
// OnResolved callbacks; Resolve and Expire do, synchronously, after the fake
// releases its lock. Await on a cancelled approval returns core.ErrStopped.
// Resolve checks only that the approval exists and is pending or parked.
type Approvals struct {
	core.Approvals
	Choice   core.Choice
	Manual   bool
	Requests []core.ApprovalRequest

	mu          sync.Mutex
	Resolutions []core.Resolution
	items       map[core.ApprovalID]*approvalState
	order       []core.ApprovalID
	callbacks   []func(a *core.Approval)
	changed     chan struct{}
}

type approvalState struct {
	a    core.Approval
	refs []core.CardMessage
}

func (ap *Approvals) Request(_ context.Context, r core.ApprovalRequest) (*core.Approval, error) {
	now := time.Now()
	timeout := r.Decision.Timeout
	if timeout == 0 {
		timeout = InteractiveTimeout
	}
	a := core.Approval{
		ID: core.ApprovalID(core.NewID(core.PrefixApproval)), Run: r.Run, Session: r.Session,
		Principal: r.Principal, ToolCallID: r.ToolCallID, Action: r.Action, Decision: r.Decision,
		Status: core.ApprovalPending, Approver: r.Decision.Approver, Chat: r.Chat,
		OnTimeout: r.Decision.OnTimeout, CreatedAt: now, ExpiresAt: now.Add(timeout),
	}
	a.Card = ap.RenderCard(r)
	a.Card.Expires = a.ExpiresAt
	a.Card.WebURL = "/approvals/" + string(a.ID)

	ap.mu.Lock()
	defer ap.mu.Unlock()
	ap.Requests = append(ap.Requests, r)
	if !ap.Manual {
		c := ap.Choice
		if c == "" {
			c = core.AllowOnce
		}
		decide(&a, c, "", "fake", now)
	}
	if ap.items == nil {
		ap.items = map[core.ApprovalID]*approvalState{}
	}
	ap.items[a.ID] = &approvalState{a: a}
	ap.order = append(ap.order, a.ID)
	ap.notifyLocked()
	return clone(a), nil
}

func decide(a *core.Approval, c core.Choice, by core.UserID, via string, at time.Time) {
	a.Status = core.ApprovalAllowed
	if c == core.Deny {
		a.Status = core.ApprovalDenied
	}
	a.Choice, a.DecidedBy, a.DecidedVia, a.DecidedAt = c, by, via, at
}

func clone(a core.Approval) *core.Approval { return &a }

func (ap *Approvals) notifyLocked() {
	if ap.changed != nil {
		close(ap.changed)
		ap.changed = nil
	}
}

// Await follows the approval's status: allowed or denied return the choice,
// expired core.ErrApprovalExpired, parked core.ErrParked.
func (ap *Approvals) Await(ctx context.Context, id core.ApprovalID) (core.Choice, error) {
	for {
		ap.mu.Lock()
		st, ok := ap.items[id]
		if !ok {
			ap.mu.Unlock()
			return "", fmt.Errorf("approval %s: %w", id, core.ErrNotFound)
		}
		status, choice := st.a.Status, st.a.Choice
		if status == core.ApprovalPending {
			if ap.changed == nil {
				ap.changed = make(chan struct{})
			}
			ch := ap.changed
			ap.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		ap.mu.Unlock()
		switch status {
		case core.ApprovalExpired:
			return "", core.ErrApprovalExpired
		case core.ApprovalParked:
			return "", core.ErrParked
		case core.ApprovalCancelled:
			return "", core.ErrStopped
		}
		return choice, nil
	}
}

func (ap *Approvals) Resolve(_ context.Context, r core.Resolution) (*core.Approval, error) {
	ap.mu.Lock()
	st, ok := ap.items[r.ID]
	if !ok {
		ap.mu.Unlock()
		return nil, fmt.Errorf("approval %s: %w", r.ID, core.ErrNotFound)
	}
	if st.a.Status != core.ApprovalPending && st.a.Status != core.ApprovalParked {
		ap.mu.Unlock()
		return nil, fmt.Errorf("approval %s already decided: %w", r.ID, core.ErrConflict)
	}
	ap.Resolutions = append(ap.Resolutions, r)
	decide(&st.a, r.Choice, r.By, r.Via, time.Now())
	ap.notifyLocked()
	a, cbs := st.a, slices.Clone(ap.callbacks)
	ap.mu.Unlock()
	for _, fn := range cbs {
		fn(clone(a))
	}
	return clone(a), nil
}

// Expire applies a timeout: a pending approval with OnTimeout park becomes
// parked (ParkUntil = now + ParkTTL, no callbacks); any other pending or
// parked approval becomes expired and OnResolved callbacks run.
func (ap *Approvals) Expire(id core.ApprovalID) error {
	ap.mu.Lock()
	st, ok := ap.items[id]
	if !ok {
		ap.mu.Unlock()
		return fmt.Errorf("approval %s: %w", id, core.ErrNotFound)
	}
	now := time.Now()
	switch {
	case st.a.Status == core.ApprovalPending && st.a.OnTimeout == core.TimeoutPark:
		st.a.Status, st.a.ParkUntil = core.ApprovalParked, now.Add(ParkTTL)
		ap.notifyLocked()
		ap.mu.Unlock()
		return nil
	case st.a.Status == core.ApprovalPending || st.a.Status == core.ApprovalParked:
		st.a.Status, st.a.DecidedVia, st.a.DecidedAt = core.ApprovalExpired, "timeout", now
	default:
		ap.mu.Unlock()
		return fmt.Errorf("approval %s already decided: %w", id, core.ErrConflict)
	}
	ap.notifyLocked()
	a, cbs := st.a, slices.Clone(ap.callbacks)
	ap.mu.Unlock()
	for _, fn := range cbs {
		fn(clone(a))
	}
	return nil
}

func (ap *Approvals) CancelRun(_ context.Context, run core.RunID) error {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	now := time.Now()
	for _, st := range ap.items {
		if st.a.Run == run && (st.a.Status == core.ApprovalPending || st.a.Status == core.ApprovalParked) {
			st.a.Status, st.a.DecidedVia, st.a.DecidedAt = core.ApprovalCancelled, "stop", now
		}
	}
	ap.notifyLocked()
	return nil
}

// RenderCard builds a plain card from the Action: title, tool, command or
// payload, diff, URL, recipient and paths.
func (ap *Approvals) RenderCard(r core.ApprovalRequest) core.Card {
	c := core.Card{Title: r.Spec.Manifest.Title, Context: r.Context, Choices: r.Decision.Choices, Warnings: r.Decision.Warnings}
	if c.Title == "" {
		c.Title = r.Spec.Def.Name
	}
	a := r.Action
	if a == nil {
		return c
	}
	c.Risk, c.Diff = a.SideEffect, a.Diff
	c.Fields = append(c.Fields, core.CardField{Label: "Tool", Value: a.Tool, Mono: true})
	if len(a.Argv) > 0 {
		c.Code = strings.Join(a.Argv, " ")
	} else {
		c.Code = string(a.Payload)
	}
	if a.URL != "" {
		c.Fields = append(c.Fields, core.CardField{Label: "URL", Value: a.URL, Mono: true})
	}
	if a.Recipient != "" {
		c.Fields = append(c.Fields, core.CardField{Label: "Recipient", Value: a.Recipient})
	}
	if len(a.Paths) > 0 {
		c.Fields = append(c.Fields, core.CardField{Label: "Paths", Value: strings.Join(a.Paths, "\n"), Mono: true})
	}
	return c
}

func visible(v core.Viewer, a *core.Approval) bool {
	return v.Can(a.Principal) || v.Admin && a.Approver.AdminOnly
}

func (ap *Approvals) Get(_ context.Context, v core.Viewer, id core.ApprovalID) (*core.Approval, error) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	st, ok := ap.items[id]
	if !ok || !visible(v, &st.a) {
		return nil, fmt.Errorf("approval %s: %w", id, core.ErrNotFound)
	}
	return clone(st.a), nil
}

// Pending returns pending and parked approvals visible to v, oldest first.
func (ap *Approvals) Pending(_ context.Context, v core.Viewer) ([]*core.Approval, error) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	var out []*core.Approval
	for _, id := range ap.order {
		a := ap.items[id].a
		if (a.Status == core.ApprovalPending || a.Status == core.ApprovalParked) && visible(v, &a) {
			out = append(out, clone(a))
		}
	}
	return out, nil
}

// Recent returns decided approvals visible to v, newest first, with
// p.Before an exclusive created_at unix-ms bound.
func (ap *Approvals) Recent(_ context.Context, v core.Viewer, p core.Page) ([]*core.Approval, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 500)
	ap.mu.Lock()
	defer ap.mu.Unlock()
	var out []*core.Approval
	for i := len(ap.order) - 1; i >= 0 && len(out) < limit; i-- {
		a := ap.items[ap.order[i]].a
		if a.Status == core.ApprovalPending || a.Status == core.ApprovalParked || !visible(v, &a) {
			continue
		}
		if p.Before > 0 && a.CreatedAt.UnixMilli() >= p.Before {
			continue
		}
		out = append(out, clone(a))
	}
	return out, nil
}

var choiceLetters = map[core.Choice]string{core.AllowOnce: "o", core.AllowSession: "s", core.AllowAlways: "a", core.Deny: "d"}

// ButtonData returns "a:<id>:<o|s|a|d>:<mac8>" with an unkeyed mac.
func (ap *Approvals) ButtonData(id core.ApprovalID, c core.Choice) string {
	return "a:" + string(id) + ":" + choiceLetters[c] + ":" + buttonMAC(id, c)
}

func buttonMAC(id core.ApprovalID, c core.Choice) string {
	sum := sha256.Sum256([]byte(string(id) + "|" + string(c)))
	return strings.ToLower(base32.StdEncoding.EncodeToString(sum[:5]))
}

func (ap *Approvals) ParseButtonData(data string) (core.ApprovalID, core.Choice, error) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 || parts[0] != "a" || !core.ValidID(core.PrefixApproval, parts[1]) {
		return "", "", fmt.Errorf("%w: malformed button data", core.ErrInvalid)
	}
	id := core.ApprovalID(parts[1])
	for c, l := range choiceLetters {
		if l == parts[2] {
			if parts[3] != buttonMAC(id, c) {
				return "", "", fmt.Errorf("%w: bad button mac", core.ErrInvalid)
			}
			return id, c, nil
		}
	}
	return "", "", fmt.Errorf("%w: unknown choice %q", core.ErrInvalid, parts[2])
}

func (ap *Approvals) AddMessageRef(_ context.Context, id core.ApprovalID, ref core.MsgRef, truncated bool) error {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	st, ok := ap.items[id]
	if !ok {
		return fmt.Errorf("approval %s: %w", id, core.ErrNotFound)
	}
	st.refs = append(st.refs, core.CardMessage{Ref: ref, Truncated: truncated})
	return nil
}

func (ap *Approvals) MessageRefs(_ context.Context, id core.ApprovalID) ([]core.CardMessage, error) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	st, ok := ap.items[id]
	if !ok {
		return nil, fmt.Errorf("approval %s: %w", id, core.ErrNotFound)
	}
	return append([]core.CardMessage(nil), st.refs...), nil
}

func (ap *Approvals) OnResolved(fn func(a *core.Approval)) {
	ap.mu.Lock()
	ap.callbacks = append(ap.callbacks, fn)
	ap.mu.Unlock()
}

// All returns every approval in creation order.
func (ap *Approvals) All() []core.Approval {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	out := make([]core.Approval, len(ap.order))
	for i, id := range ap.order {
		out[i] = ap.items[id].a
	}
	return out
}
