package policy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const sweepInterval = 30 * time.Second

var (
	openStatuses = []core.ApprovalStatus{core.ApprovalPending, core.ApprovalParked}
	channelRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	// The API shows these to the person who pressed the button, so they name
	// no approval id.
	errDecided = fmt.Errorf("%w: already decided", core.ErrConflict)
	errVoted   = fmt.Errorf("%w: you already voted", core.ErrConflict)
	// errWaiting refuses another team request while maxTeamOpen are open:
	// a flood of cards ends in rubber-stamping.
	errWaiting = fmt.Errorf("%w: Bothy is still waiting on earlier requests from this chat", core.ErrBusy)
)

// maxTeamOpen bounds a team-owned principal's open (pending or parked)
// requests.
const maxTeamOpen = 5

// chatSender reports whether a run's Sender is a person in a chat app
// ("<app>:<external id>") rather than the web, the API or a schedule.
func chatSender(s string) bool {
	app, id, ok := strings.Cut(s, ":")
	if !ok || id == "" || !channelRe.MatchString(app) {
		return false
	}
	switch app {
	case "web", "api", "schedule", "heartbeat":
		return false
	}
	return true
}

// Request implements core.Approvals.
func (ap *Approvals) Request(ctx context.Context, r core.ApprovalRequest) (*core.Approval, error) {
	switch {
	case r.Action == nil:
		return nil, fmt.Errorf("%w: approval request without an action", core.ErrInvalid)
	case r.Decision.Verdict != core.VerdictAsk:
		return nil, fmt.Errorf("%w: approval request for a %q decision", core.ErrInvalid, r.Decision.Verdict)
	case r.Run == "" || r.Session == "" || r.Principal == "":
		return nil, fmt.Errorf("%w: approval request needs run, session and principal", core.ErrInvalid)
	case r.Decision.Approver.Team && (r.Decision.Approver.User != "" || r.Decision.Approver.AdminOnly ||
		len(r.Decision.Approver.Users) > 0 || len(r.Decision.Approver.Roles) > 0):
		return nil, fmt.Errorf("%w: a team request names no other approver", core.ErrInvalid)
	case !r.Decision.Approver.AdminOnly && !r.Decision.Approver.Team && r.Decision.Approver.User == "" && !r.Decision.Approver.Multi():
		return nil, fmt.Errorf("%w: approval request without an approver", core.ErrInvalid)
	case r.Decision.Approver.Multi() && (r.Decision.Approver.User != "" || r.Decision.Approver.Required < 0):
		return nil, fmt.Errorf("%w: a multi-approver request names its approvers and requester only", core.ErrInvalid)
	}
	pr, err := ap.store.Principals().Get(ctx, core.SystemViewer(), r.Principal)
	if err != nil {
		return nil, fmt.Errorf("approval principal %s: %w", r.Principal, err)
	}
	if r.Decision.Approver.Multi() && r.Decision.Approver.Requester == "" && pr.Kind != core.PrincipalPerson {
		return nil, fmt.Errorf("%w: a multi-approver request names its requester", core.ErrInvalid)
	}
	if r.Decision.Approver.Team {
		n, err := ap.store.Approvals().CountOpen(ctx, r.Principal)
		if err != nil {
			return nil, fmt.Errorf("open approvals of %s: %w", r.Principal, err)
		}
		if n >= maxTeamOpen {
			return nil, errWaiting
		}
	}
	choices, err := ap.offered(ctx, r)
	if err != nil {
		return nil, err
	}
	r.Decision.Choices = choices
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
	if pr.TeamOwned() && chatSender(r.Sender) {
		a.Approver.RequesterIdentity = r.Sender
		a.Decision.Approver.RequesterIdentity = r.Sender
	}
	a.Card = renderCard(r)
	switch {
	case a.Approver.Multi():
		// The context line names the requester's session, whose title comes
		// from their conversation; the other approvers are outside that
		// principal and see only who asked (Approval.Requester).
		a.Card.Context = ""
	case pr.TeamOwned():
		// Chat approvers aren't in the chat: they read whose chat it is,
		// never its title.
		a.Card.Context = ap.teamContext(ctx, pr)
	}
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
			"admin_only": r.Decision.Approver.AdminOnly, "team": r.Decision.Approver.Team, "choices": r.Decision.Choices,
			"approvers": approverDetail(r.Decision.Approver),
		}),
	})
	ap.fillQuiet(ctx, a)
	ap.publish(ctx, core.EvApprovalRequested, a, func(cp *core.Approval) core.EventData { return core.ApprovalData{Approval: cp} })
	out := *a
	return &out, nil
}

// offered returns the choices a card may carry whatever the caller asked
// for (§5.8): known choices only, no grants for admin-only approvals, and
// allow_always only next to allow_session, never for irreversible actions
// and never in a run no human started. Egress holds build their choices
// outside Decide, so this is checked here as well.
func (ap *Approvals) offered(ctx context.Context, r core.ApprovalRequest) ([]core.Choice, error) {
	in := r.Decision.Choices
	// Votes answer an action once; a grant would let one approver stand in for
	// the others next time.
	admin := r.Decision.Approver.AdminOnly || r.Decision.Approver.Multi()
	always := !admin && slices.Contains(in, core.AllowAlways) && slices.Contains(in, core.AllowSession) &&
		actionClass(r.Action, r.Spec.Manifest) != core.SEIrreversible
	if always {
		run, err := ap.store.Runs().Get(ctx, core.SystemViewer(), r.Run)
		if err != nil {
			return nil, fmt.Errorf("approval run %s: %w", r.Run, err)
		}
		always = !run.Trigger.Scheduled() && run.Trigger != core.TrigAPI
	}
	out := make([]core.Choice, 0, 4)
	for _, c := range in {
		switch {
		case slices.Contains(out, c):
		case c == core.AllowOnce, c == core.Deny,
			c == core.AllowSession && !admin,
			c == core.AllowAlways && always:
			out = append(out, c)
		}
	}
	if !slices.ContainsFunc(out, func(c core.Choice) bool { return c != core.Deny }) {
		out = append([]core.Choice{core.AllowOnce}, out...)
	}
	if !slices.Contains(out, core.Deny) {
		out = append(out, core.Deny)
	}
	return out, nil
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
		return nil, errDecided
	}
	if a.Approver.Multi() {
		return ap.vote(ctx, a, r)
	}
	if r.Choice != core.Deny && !slices.Contains(a.Decision.Choices, r.Choice) {
		return nil, fmt.Errorf("%w: %s was not offered for this approval", core.ErrInvalid, r.Choice)
	}
	if err := ap.authorize(ctx, a, r); err != nil {
		return nil, err
	}
	now := ap.clock.Now()
	ok, err := ap.store.Approvals().Decide(ctx, a.ID, openStatuses, to, r.Choice, core.Decider{User: r.By, Identity: r.ByIdentity}, r.Via, now)
	if err != nil {
		return nil, fmt.Errorf("decide approval %s: %w", a.ID, err)
	}
	if !ok {
		return nil, errDecided
	}
	a.Status, a.Choice, a.DecidedBy, a.DecidedVia, a.DecidedAt = to, r.Choice, r.By, r.Via, msTime(now)
	a.DecidedByIdentity = r.ByIdentity
	if r.Comment != "" {
		// The decision stands without its reason: a failed write only loses
		// the words shown next to it.
		if err := ap.store.Approvals().SetComment(ctx, a.ID, r.Comment); err != nil {
			ap.log.WarnContext(ctx, "approvals: saving the decision's reason failed", "approval", a.ID, "err", err)
		} else {
			a.Comment = r.Comment
		}
	}
	ap.audit1(ctx, core.AuditEntry{
		Actor: resolverActor(r), Action: "approval.decide", Target: string(a.ID), Outcome: "ok",
		Session: a.Session, Run: a.Run,
		Detail: auditDetail(map[string]any{"choice": r.Choice, "via": r.Via, "tool": toolOf(a), "fingerprint": fingerprintOf(a)}),
	})
	ap.fillQuiet(ctx, a)
	ap.resolved(ctx, a)
	out := *a
	return &out, nil
}

// resolverActor is the audit actor of whoever resolved: an account, or a
// teammate pressing a chat card.
func resolverActor(r core.Resolution) string {
	if r.ByIdentity != "" {
		return "channel:" + r.ByIdentity
	}
	return "user:" + string(r.By)
}

// notChannelAlways refuses a chat approver a standing rule for everyone in
// a channel.
const notChannelAlways = "Only an admin on the web can always allow this for a channel."

// authorize enforces who may resolve an approval and from where (§5.8,
// §5.9). Roles and Can approve come from the store, not from the caller.
func (ap *Approvals) authorize(ctx context.Context, a *core.Approval, r core.Resolution) error {
	forbid := func(why string) error {
		return fmt.Errorf("%w: %s", core.ErrForbidden, why)
	}
	if r.ByIdentity != "" {
		return ap.authorizeChat(ctx, a, r)
	}
	if r.By == "" {
		return forbid(notApprover)
	}
	u, err := ap.store.Users().Get(ctx, r.By)
	if err != nil || u.Disabled {
		return forbid(notApprover)
	}
	if a.Approver.Team {
		if !u.Role.IsAdmin() {
			return forbid(notApprover)
		}
		return ap.checkVia(ctx, a, r)
	}
	if a.Approver.AdminOnly {
		if !u.Role.IsAdmin() {
			return forbid("only an admin can approve this")
		}
		if r.Choice == core.AllowSession || r.Choice == core.AllowAlways {
			return forbid("admin-class actions are approved one at a time")
		}
	} else if a.Approver.User == "" || r.By != a.Approver.User {
		return forbid(notApprover)
	}
	return ap.checkVia(ctx, a, r)
}

// authorizeChat decides a press by a teammate. The person who asked may
// always withdraw (deny) from their own chat app; otherwise only a team
// request can be decided in chat, by an admitted teammate an admin marked
// "Can approve", who may not always-allow for a whole channel.
func (ap *Approvals) authorizeChat(ctx context.Context, a *core.Approval, r core.Resolution) error {
	forbid := func(why string) error {
		return fmt.Errorf("%w: %s", core.ErrForbidden, why)
	}
	app, ext, ok := strings.Cut(r.ByIdentity, ":")
	if r.By != "" || !ok || ext == "" || app != r.Via {
		return forbid(notApprover)
	}
	if r.Choice == core.Deny && r.ByIdentity == a.Approver.RequesterIdentity {
		return ap.checkVia(ctx, a, r)
	}
	if !a.Approver.Team {
		return forbid("open it in Bothy to approve")
	}
	id, err := ap.store.Identities().Get(ctx, app, ext)
	if err != nil || id.Status != core.IdentityTeammate || !id.CanApprove {
		return forbid(notApprover)
	}
	res := core.IdentityResolution{Status: id.Status, Via: id.Via, Member: id.Member}
	if sp, err := ap.store.Spaces().Get(ctx, core.SystemViewer(), id.Principal); err == nil {
		res.SpaceOn = sp.Enabled
	}
	if !res.Admitted() {
		return forbid(notApprover)
	}
	if r.Choice == core.AllowAlways {
		pr, err := ap.store.Principals().Get(ctx, core.SystemViewer(), a.Principal)
		if err != nil || pr.Kind == core.PrincipalSpace {
			return forbid(notChannelAlways)
		}
	}
	return ap.checkVia(ctx, a, r)
}

// teamContext is a team request's card context: whose chat it is, never
// what it is called inside, since chat approvers aren't in it.
func (ap *Approvals) teamContext(ctx context.Context, pr *core.Principal) string {
	sp, err := ap.store.Spaces().Get(ctx, core.SystemViewer(), pr.ID)
	if err != nil {
		return ""
	}
	app := appTitle(sp.Chat.Channel)
	if pr.Kind == core.PrincipalPerson {
		return pr.DisplayName + "'s direct messages on " + app
	}
	return sp.Title + " on " + app
}

// appTitle names a chat app in card copy.
func appTitle(channel string) string {
	switch channel {
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	case "slack":
		return "Slack"
	}
	if channel == "" {
		return channel
	}
	return strings.ToUpper(channel[:1]) + channel[1:]
}

// checkVia enforces where a resolution may come from: the web UI, or a press
// on a recorded card message of that channel, which may only deny when the
// card was truncated.
func (ap *Approvals) checkVia(ctx context.Context, a *core.Approval, r core.Resolution) error {
	forbid := func(why string) error {
		return fmt.Errorf("%w: %s", core.ErrForbidden, why)
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
		// A waiting line sits in the requester's chat, where anyone may press
		// its Cancel request; only the person who asked may.
		if m.Kind == core.CardMsgNotice && (r.ByIdentity == "" || r.ByIdentity != a.Approver.RequesterIdentity) {
			return forbid("only the person who asked can cancel this")
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
		ok, err := ap.store.Approvals().Decide(ctx, a.ID, openStatuses, core.ApprovalCancelled, "", core.Decider{}, "stop", now)
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
		ap.publishResolved(ctx, a)
		ap.notify(a.ID)
	}
	return errors.Join(errs...)
}

// RenderCard implements core.Approvals.
func (ap *Approvals) RenderCard(r core.ApprovalRequest) core.Card { return renderCard(r) }

// Get implements core.Approvals.
func (ap *Approvals) Get(ctx context.Context, v core.Viewer, id core.ApprovalID) (*core.Approval, error) {
	a, err := ap.store.Approvals().Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	if err := ap.fill(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Pending implements core.Approvals.
func (ap *Approvals) Pending(ctx context.Context, v core.Viewer) ([]*core.Approval, error) {
	as, err := ap.store.Approvals().Pending(ctx, v)
	if err != nil {
		return nil, err
	}
	if err := ap.fill(ctx, as...); err != nil {
		return nil, err
	}
	return as, nil
}

// Recent implements core.Approvals.
func (ap *Approvals) Recent(ctx context.Context, v core.Viewer, p core.Page) ([]*core.Approval, error) {
	as, err := ap.store.Approvals().Recent(ctx, v, p)
	if err != nil {
		return nil, err
	}
	if err := ap.fill(ctx, as...); err != nil {
		return nil, err
	}
	return as, nil
}

// AddMessage implements core.Approvals.
func (ap *Approvals) AddMessage(ctx context.Context, id core.ApprovalID, m core.CardMessage) error {
	return ap.store.Approvals().AddMessage(ctx, id, m)
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
		ap.resolved(ctx, a)
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
	ok, err := ap.store.Approvals().Decide(ctx, id, []core.ApprovalStatus{a.Status}, core.ApprovalExpired, "", core.Decider{}, "timeout", now)
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
func (ap *Approvals) resolved(ctx context.Context, a *core.Approval) {
	ap.publishResolved(ctx, a)
	ap.notify(a.ID)
	ap.mu.Lock()
	cbs := slices.Clone(ap.callbacks)
	ap.mu.Unlock()
	for _, fn := range cbs {
		cp := *a
		fn(&cp)
	}
}

func (ap *Approvals) publishResolved(ctx context.Context, a *core.Approval) {
	ap.publish(ctx, core.EvApprovalResolved, a, func(*core.Approval) core.EventData {
		return core.ApprovalResolvedData{Approval: a.ID, Status: a.Status, Choice: a.Choice, By: a.DecidedBy, ByIdentity: a.DecidedByIdentity,
			Via: a.DecidedVia, Comment: a.Comment}
	})
}

// publish sends the event to the approval's principal and, for admin-only
// and team approvals, a copy as an admin broadcast (principal ""), and for
// multi-approver approvals a copy to each eligible approver's own principal.
func (ap *Approvals) publish(ctx context.Context, typ core.EventType, a *core.Approval, data func(*core.Approval) core.EventData) {
	if ap.bus == nil {
		return
	}
	principals := []core.PrincipalID{a.Principal}
	if a.Approver.AdminOnly || a.Approver.Team {
		principals = append(principals, "")
	}
	for _, u := range ap.voters(ctx, a.Approver) {
		if p := core.UserPrincipal(u.ID); !slices.Contains(principals, p) {
			principals = append(principals, p)
		}
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
