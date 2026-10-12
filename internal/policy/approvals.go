package policy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const sweepInterval = 30 * time.Second

var (
	openStatuses = []core.ApprovalStatus{core.ApprovalPending, core.ApprovalParked}
	channelRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// Request implements core.Approvals.
func (ap *Approvals) Request(ctx context.Context, r core.ApprovalRequest) (*core.Approval, error) {
	switch {
	case r.Action == nil:
		return nil, fmt.Errorf("%w: approval request without an action", core.ErrInvalid)
	case r.Decision.Verdict != core.VerdictAsk:
		return nil, fmt.Errorf("%w: approval request for a %q decision", core.ErrInvalid, r.Decision.Verdict)
	case r.Run == "" || r.Session == "" || r.Principal == "":
		return nil, fmt.Errorf("%w: approval request needs run, session and principal", core.ErrInvalid)
	case !r.Decision.Approver.AdminOnly && r.Decision.Approver.User == "":
		return nil, fmt.Errorf("%w: approval request without an approver", core.ErrInvalid)
	}
	r.Decision.Choices = slices.Clone(r.Decision.Choices)
	if r.Decision.Approver.AdminOnly {
		r.Decision.Choices = slices.DeleteFunc(r.Decision.Choices, func(c core.Choice) bool {
			return c == core.AllowSession || c == core.AllowAlways
		})
	}
	if !slices.ContainsFunc(r.Decision.Choices, func(c core.Choice) bool { return c != core.Deny }) {
		r.Decision.Choices = append([]core.Choice{core.AllowOnce}, r.Decision.Choices...)
	}
	if !slices.Contains(r.Decision.Choices, core.Deny) {
		r.Decision.Choices = append(r.Decision.Choices, core.Deny)
	}
	c := ap.cfg.Current()
	now := msTime(ap.clock.Now())
	timeout := r.Decision.Timeout
	if timeout <= 0 {
		timeout = orDefault(c.Policy.Approval.InteractiveTimeout.D(), defaultInteractiveTimeout)
	}
	onTimeout := r.Decision.OnTimeout
	if onTimeout != core.TimeoutPark {
		onTimeout = core.TimeoutDeny
	}
	a := &core.Approval{
		ID:  core.ApprovalID(core.NewID(core.PrefixApproval)),
		Run: r.Run, Session: r.Session, Principal: r.Principal, ToolCallID: r.ToolCallID,
		Action: r.Action, Decision: r.Decision, Status: core.ApprovalPending, Approver: r.Decision.Approver,
		Chat: r.Chat, OnTimeout: onTimeout, CreatedAt: now, ExpiresAt: msTime(now.Add(timeout)),
	}
	a.Card = renderCard(r)
	a.Card.Expires = a.ExpiresAt
	if base := strings.TrimRight(c.Server.PublicURL, "/"); base != "" {
		a.Card.WebURL = base + "/approvals/" + string(a.ID)
	}
	if err := ap.store.Approvals().Create(ctx, a); err != nil {
		return nil, fmt.Errorf("store approval: %w", err)
	}
	ap.audit1(ctx, core.AuditEntry{
		Actor: "agent:" + string(r.Run), Action: "approval.request", Target: string(a.ID), Outcome: "ok",
		Session: r.Session, Run: r.Run,
		Detail: auditDetail(map[string]any{
			"tool": r.Action.Tool, "fingerprint": r.Action.Fingerprint, "rule": r.Decision.Rule,
			"admin_only": r.Decision.Approver.AdminOnly, "choices": r.Decision.Choices,
		}),
	})
	ap.publish(core.EvApprovalRequested, a, func(cp *core.Approval) core.EventData { return core.ApprovalData{Approval: cp} })
	out := *a
	return &out, nil
}

// Await implements core.Approvals. It also applies the timeout itself when
// the deadline passes, so an interactive wait never depends on the sweeper.
func (ap *Approvals) Await(ctx context.Context, id core.ApprovalID) (core.Choice, error) {
	for {
		c, done, err := ap.awaitOnce(ctx, id)
		if done {
			return c, err
		}
	}
}

// awaitOnce reads the approval and, while it is pending, waits for one change
// or its deadline; done is false when the caller should read again.
func (ap *Approvals) awaitOnce(ctx context.Context, id core.ApprovalID) (core.Choice, bool, error) {
	ch, release := ap.watch(id) // taken before reading so no change is missed
	defer release()
	a, err := ap.store.Approvals().Get(ctx, core.SystemViewer(), id)
	if err != nil {
		return "", true, fmt.Errorf("approval %s: %w", id, err)
	}
	switch a.Status {
	case core.ApprovalAllowed:
		return a.Choice, true, nil
	case core.ApprovalDenied:
		return core.Deny, true, nil
	case core.ApprovalExpired:
		return "", true, core.ErrApprovalExpired
	case core.ApprovalParked:
		return "", true, core.ErrParked
	case core.ApprovalCancelled:
		return "", true, core.ErrStopped
	}
	wait := a.ExpiresAt.Sub(ap.clock.Now())
	if wait <= 0 {
		if err := ap.timeout(ctx, id); err != nil {
			return "", true, err
		}
		return "", false, nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
		if err := ap.timeout(ctx, id); err != nil {
			return "", true, err
		}
	case <-ctx.Done():
		return "", true, context.Cause(ctx)
	}
	return "", false, nil
}

// Resolve implements core.Approvals.
func (ap *Approvals) Resolve(ctx context.Context, r core.Resolution) (*core.Approval, error) {
	var to core.ApprovalStatus
	switch r.Choice {
	case core.AllowOnce, core.AllowSession, core.AllowAlways:
		to = core.ApprovalAllowed
	case core.Deny:
		to = core.ApprovalDenied
	default:
		return nil, fmt.Errorf("%w: unknown choice %q", core.ErrInvalid, r.Choice)
	}
	a, err := ap.store.Approvals().Get(ctx, core.SystemViewer(), r.ID)
	if err != nil {
		return nil, fmt.Errorf("approval %s: %w", r.ID, err)
	}
	if !slices.Contains(openStatuses, a.Status) {
		return nil, fmt.Errorf("approval %s: already decided: %w", r.ID, core.ErrConflict)
	}
	if r.Choice != core.Deny && !slices.Contains(a.Decision.Choices, r.Choice) {
		return nil, fmt.Errorf("%w: %s was not offered for this approval", core.ErrInvalid, r.Choice)
	}
	if err := ap.authorize(ctx, a, r); err != nil {
		return nil, err
	}
	now := ap.clock.Now()
	ok, err := ap.store.Approvals().Decide(ctx, a.ID, openStatuses, to, r.Choice, r.By, r.Via, now)
	if err != nil {
		return nil, fmt.Errorf("decide approval %s: %w", a.ID, err)
	}
	if !ok {
		return nil, fmt.Errorf("approval %s: already decided: %w", a.ID, core.ErrConflict)
	}
	a.Status, a.Choice, a.DecidedBy, a.DecidedVia, a.DecidedAt = to, r.Choice, r.By, r.Via, msTime(now)
	ap.audit1(ctx, core.AuditEntry{
		Actor: "user:" + string(r.By), Action: "approval.decide", Target: string(a.ID), Outcome: "ok",
		Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "fingerprint": fingerprintOf(a)}),
	})
	ap.resolved(a)
	out := *a
	return &out, nil
}

// authorize enforces who may resolve an approval and from where (§5.8,
// §5.9). The approver's role comes from the store, not from the caller.
func (ap *Approvals) authorize(ctx context.Context, a *core.Approval, r core.Resolution) error {
	forbid := func(why string) error {
		return fmt.Errorf("%w: %s", core.ErrForbidden, why)
	}
	if r.By == "" {
		return forbid("not an approver")
	}
	u, err := ap.store.Users().Get(ctx, r.By)
	if err != nil || u.Disabled {
		return forbid("not an approver")
	}
	if a.Approver.AdminOnly {
		if !u.Role.IsAdmin() {
			return forbid("only an admin can approve this")
		}
		if r.Choice == core.AllowSession || r.Choice == core.AllowAlways {
			return forbid("admin-class actions are approved one at a time")
		}
	} else if a.Approver.User == "" || r.By != a.Approver.User {
		return forbid("not an approver")
	}
	switch {
	case r.Via == "web":
		return nil
	case r.Via == "timeout" || r.Via == "stop" || !channelRe.MatchString(r.Via):
		return fmt.Errorf("%w: invalid resolution channel %q", core.ErrInvalid, r.Via)
	}
	if r.ViaMsg == nil || r.ViaMsg.Chat.Channel != r.Via || r.ViaMsg.ID == "" {
		return forbid("button press from an unrecorded message")
	}
	msgs, err := ap.store.Approvals().Messages(ctx, a.ID)
	if err != nil {
		return fmt.Errorf("card messages of %s: %w", a.ID, err)
	}
	for _, m := range msgs {
		if m.Ref.ID != r.ViaMsg.ID || m.Ref.Chat.Channel != r.ViaMsg.Chat.Channel || m.Ref.Chat.ChatID != r.ViaMsg.Chat.ChatID {
			continue
		}
		if m.Truncated && r.Choice != core.Deny {
			return forbid("this card can only deny from chat; open it in the web UI to approve")
		}
		return nil
	}
	return forbid("button press from an unrecorded message")
}

// CancelRun implements core.Approvals. It fires no OnResolved callbacks: the
// caller is stopping the run itself.
func (ap *Approvals) CancelRun(ctx context.Context, run core.RunID) error {
	list, err := ap.store.Approvals().ByRun(ctx, run)
	if err != nil {
		return fmt.Errorf("approvals of run %s: %w", run, err)
	}
	var errs []error
	for _, a := range list {
		if !slices.Contains(openStatuses, a.Status) {
			continue
		}
		now := ap.clock.Now()
		ok, err := ap.store.Approvals().Decide(ctx, a.ID, openStatuses, core.ApprovalCancelled, "", "", "stop", now)
		if err != nil {
			errs = append(errs, fmt.Errorf("cancel approval %s: %w", a.ID, err))
			continue
		}
		if !ok {
			continue
		}
		a.Status, a.DecidedVia, a.DecidedAt = core.ApprovalCancelled, "stop", msTime(now)
		ap.audit1(ctx, core.AuditEntry{
			Actor: "system", Action: "approval.decide", Target: string(a.ID), Outcome: "ok", Session: a.Session, Run: a.Run,
			Detail: auditDetail(map[string]any{"status": core.ApprovalCancelled, "via": "stop", "tool": toolOf(a)}),
		})
		ap.publishResolved(a)
		ap.notify(a.ID)
	}
	return errors.Join(errs...)
}

// RenderCard implements core.Approvals.
func (ap *Approvals) RenderCard(r core.ApprovalRequest) core.Card { return renderCard(r) }

// Get implements core.Approvals.
func (ap *Approvals) Get(ctx context.Context, v core.Viewer, id core.ApprovalID) (*core.Approval, error) {
	return ap.store.Approvals().Get(ctx, v, id)
}

// Pending implements core.Approvals.
func (ap *Approvals) Pending(ctx context.Context, v core.Viewer) ([]*core.Approval, error) {
	return ap.store.Approvals().Pending(ctx, v)
}

// Recent implements core.Approvals.
func (ap *Approvals) Recent(ctx context.Context, v core.Viewer, p core.Page) ([]*core.Approval, error) {
	return ap.store.Approvals().Recent(ctx, v, p)
}

// AddMessageRef implements core.Approvals.
func (ap *Approvals) AddMessageRef(ctx context.Context, id core.ApprovalID, ref core.MsgRef, truncated bool) error {
	return ap.store.Approvals().AddMessage(ctx, id, core.CardMessage{Ref: ref, Truncated: truncated})
}

// MessageRefs implements core.Approvals.
func (ap *Approvals) MessageRefs(ctx context.Context, id core.ApprovalID) ([]core.CardMessage, error) {
	return ap.store.Approvals().Messages(ctx, id)
}

// OnResolved registers a callback run synchronously, in registration order,
// after an approval is allowed, denied or expires.
func (ap *Approvals) OnResolved(fn func(*core.Approval)) {
	ap.mu.Lock()
	ap.callbacks = append(ap.callbacks, fn)
	ap.mu.Unlock()
}

// Run expires due approvals every 30s until ctx is done.
func (ap *Approvals) Run(ctx context.Context) error {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		ap.sweep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (ap *Approvals) sweep(ctx context.Context) {
	due, err := ap.store.Approvals().Due(ctx, ap.clock.Now())
	if err != nil {
		ap.log.ErrorContext(ctx, "approvals: listing due approvals failed", "err", err)
		return
	}
	for _, a := range due {
		if err := ap.timeout(ctx, a.ID); err != nil {
			ap.log.ErrorContext(ctx, "approvals: timeout failed", "approval", a.ID, "err", err)
		}
	}
}

// timeout applies the approval's on_timeout once it is due: a pending
// approval parks (park_until = now + park_ttl) or expires; a parked one
// expires once park_until passes.
func (ap *Approvals) timeout(ctx context.Context, id core.ApprovalID) error {
	a, err := ap.transition(ctx, id)
	if a != nil {
		// Callbacks run outside transMu: they may call back into Approvals.
		ap.resolved(a)
	}
	return err
}

// transition performs the timeout under transMu and returns the approval
// when it expired (and callbacks are due).
func (ap *Approvals) transition(ctx context.Context, id core.ApprovalID) (*core.Approval, error) {
	ap.transMu.Lock()
	defer ap.transMu.Unlock()
	a, err := ap.store.Approvals().Get(ctx, core.SystemViewer(), id)
	if err != nil {
		return nil, fmt.Errorf("approval %s: %w", id, err)
	}
	now := ap.clock.Now()
	switch {
	case a.Status == core.ApprovalPending && a.ExpiresAt.After(now),
		a.Status == core.ApprovalParked && a.ParkUntil.After(now),
		!slices.Contains(openStatuses, a.Status):
		return nil, nil
	case a.Status == core.ApprovalPending && a.OnTimeout == core.TimeoutPark:
		until := msTime(now.Add(orDefault(ap.cfg.Current().Policy.Approval.ParkTTL.D(), defaultParkTTL)))
		if err := ap.store.Approvals().Park(ctx, id, until); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return nil, nil
			}
			return nil, fmt.Errorf("park approval %s: %w", id, err)
		}
		ap.audit1(ctx, core.AuditEntry{
			Actor: "system", Action: "approval.expire", Target: string(id), Outcome: "ok", Session: a.Session, Run: a.Run,
			Detail: auditDetail(map[string]any{"status": core.ApprovalParked, "park_until": until.UnixMilli(), "tool": toolOf(a)}),
		})
		ap.notify(id)
		return nil, nil
	}
	ok, err := ap.store.Approvals().Decide(ctx, id, []core.ApprovalStatus{a.Status}, core.ApprovalExpired, "", "", "timeout", now)
	if err != nil {
		return nil, fmt.Errorf("expire approval %s: %w", id, err)
	}
	if !ok {
		return nil, nil
	}
	a.Status, a.Choice, a.DecidedBy, a.DecidedVia, a.DecidedAt = core.ApprovalExpired, "", "", "timeout", msTime(now)
	ap.audit1(ctx, core.AuditEntry{
		Actor: "system", Action: "approval.expire", Target: string(id), Outcome: "ok", Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"status": core.ApprovalExpired, "tool": toolOf(a)}),
	})
	return a, nil
}

// resolved publishes approval.resolved, wakes waiters and runs callbacks.
func (ap *Approvals) resolved(a *core.Approval) {
	ap.publishResolved(a)
	ap.notify(a.ID)
	ap.mu.Lock()
	cbs := slices.Clone(ap.callbacks)
	ap.mu.Unlock()
	for _, fn := range cbs {
		cp := *a
		fn(&cp)
	}
}

func (ap *Approvals) publishResolved(a *core.Approval) {
	ap.publish(core.EvApprovalResolved, a, func(*core.Approval) core.EventData {
		return core.ApprovalResolvedData{Approval: a.ID, Status: a.Status, Choice: a.Choice, By: a.DecidedBy, Via: a.DecidedVia}
	})
}

// publish sends the event to the approval's principal and, for admin-only
// approvals, a copy as an admin broadcast (principal "").
func (ap *Approvals) publish(typ core.EventType, a *core.Approval, data func(*core.Approval) core.EventData) {
	if ap.bus == nil {
		return
	}
	principals := []core.PrincipalID{a.Principal}
	if a.Approver.AdminOnly {
		principals = append(principals, "")
	}
	for _, p := range principals {
		cp := *a
		ap.bus.Publish(core.Event{Type: typ, Principal: p, Session: a.Session, Run: a.Run, Data: data(&cp)})
	}
}

// waiter is the change channel shared by every Await of one approval; n
// counts them so the entry goes away with the last one even when the
// approval never changes again.
type waiter struct {
	ch chan struct{}
	n  int
}

func (ap *Approvals) watch(id core.ApprovalID) (<-chan struct{}, func()) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	w, ok := ap.changed[id]
	if !ok {
		w = &waiter{ch: make(chan struct{})}
		ap.changed[id] = w
	}
	w.n++
	return w.ch, func() {
		ap.mu.Lock()
		defer ap.mu.Unlock()
		if w.n--; w.n == 0 && ap.changed[id] == w {
			delete(ap.changed, id)
		}
	}
}

func (ap *Approvals) notify(id core.ApprovalID) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if w, ok := ap.changed[id]; ok {
		close(w.ch)
		delete(ap.changed, id)
	}
}

func (ap *Approvals) audit1(ctx context.Context, en core.AuditEntry) {
	recordAudit(ctx, ap.audit, ap.log, en)
}

func toolOf(a *core.Approval) string {
	if a.Action == nil {
		return ""
	}
	return a.Action.Tool
}

func fingerprintOf(a *core.Approval) string {
	if a.Action == nil {
		return ""
	}
	return a.Action.Fingerprint
}

// msTime truncates to the millisecond precision the store keeps.
func msTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return time.UnixMilli(t.UnixMilli())
}
