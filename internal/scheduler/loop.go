package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	// missedGrace is how late a tick may fire before it counts as missed (the
	// process was down, asleep or paused) and the missed-run policy applies.
	missedGrace = time.Minute
	// missedOnceWindow: missed_run "once" catches up only ticks missed by less.
	missedOnceWindow = time.Hour
	maxFailures      = 3

	idleRecheck   = time.Hour
	retryDelay    = 30 * time.Second
	syncRetry     = time.Minute
	firesPerStep  = 32
	outcomeBuffer = 256
)

// Run fires due schedules from a single timer on the earliest next_run_at and
// keeps the owner's heartbeat schedule in sync with the config. Run outcomes
// arrive as run.finished events, which also covers runs that parked and were
// resumed later; run.parked lists a check-in waiting on an approval.
func (s *Service) Run(ctx context.Context) error {
	var events <-chan core.Event
	var sub core.Subscription
	if s.bus != nil {
		sub = s.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvRunFinished, core.EvRunParked}}, outcomeBuffer)
		defer sub.Close()
		events = sub.C()
	}
	stop := s.cfg.Watch(func(_, _ *config.Config) {
		s.syncHB.Store(true)
		s.poke()
	})
	defer stop()
	s.syncHB.Store(true)

	timer := time.NewTimer(idleRecheck)
	defer timer.Stop()
	for {
		timer.Reset(s.step(ctx))
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		case <-s.wake:
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			s.onRunEvent(ctx, e)
			if n := sub.Dropped(); n > 0 {
				s.log.Warn("scheduler: missed run outcomes; failure counts may be stale", "dropped", n)
			}
		}
	}
}

// ResyncHeartbeat has the loop sync the owner's heartbeat again, as a
// config change does: its time zone is the owner's, kept on their account.
func (s *Service) ResyncHeartbeat() {
	s.syncHB.Store(true)
	s.poke()
}

// step syncs the heartbeat when asked, fires everything due and returns how
// long to sleep.
func (s *Service) step(ctx context.Context) time.Duration {
	wait := idleRecheck
	if s.syncHB.Swap(false) {
		pending, err := s.syncHeartbeat(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Error("scheduler: heartbeat sync", "err", err)
		}
		if err != nil || pending {
			s.syncHB.Store(true)
			wait = syncRetry
		}
	}
	for range firesPerStep {
		d, more := s.fireDue(ctx)
		if !more {
			return min(wait, d)
		}
	}
	return 0
}

// fireDue handles the earliest due schedule. more=false means nothing is due
// and d is the time until the next one.
func (s *Service) fireDue(ctx context.Context) (d time.Duration, more bool) {
	s.mu.Lock()
	d, more = s.fireDueLocked(ctx)
	notes := s.takeNotices()
	s.mu.Unlock()
	s.sendNotices(ctx, notes)
	return d, more
}

func (s *Service) fireDueLocked(ctx context.Context) (time.Duration, bool) {
	sc, err := s.store.Schedules().NextDue(ctx)
	if errors.Is(err, core.ErrNotFound) {
		return idleRecheck, false
	}
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("scheduler: next due", "err", err)
		}
		return retryDelay, false
	}
	now := s.clock.Now()
	if sc.NextRunAt.After(now) {
		return sc.NextRunAt.Sub(now), false
	}
	if err := s.due(ctx, sc, now); err != nil {
		if ctx.Err() == nil {
			s.log.Error("scheduler: fire", "schedule", sc.ID, "err", err)
		}
		return retryDelay, false
	}
	return 0, true
}

// due fires sc, or reschedules it without firing when the scheduler is paused
// or the tick was missed and the missed-run policy says skip. The next run is
// always computed from now, so a long outage yields at most one catch-up.
func (s *Service) due(ctx context.Context, sc *core.Schedule, now time.Time) error {
	if st, ok := s.unsaved[sc.ID]; ok {
		delete(s.unsaved, sc.ID)
		if st.tick.Equal(sc.NextRunAt) {
			// This tick already started its run; firing it again would
			// duplicate the run, so only the row update is retried.
			if err := s.saveRun(ctx, sc, st); err != nil {
				s.unsaved[sc.ID] = st
				return err
			}
			return nil
		}
	}
	p, err := parsePlan(sc.Kind, sc.Spec, sc.TZ, sc.ActiveHours)
	var next time.Time
	if err == nil {
		next, err = p.next(now, s.jitter())
	}
	if err != nil {
		s.log.Error("scheduler: disabling a schedule that can no longer fire", "schedule", sc.ID, "err", err)
		return s.store.Schedules().SetRun(ctx, sc.ID, sc.LastRunAt, time.Time{}, sc.LastStatus, sc.Failures, false)
	}
	enabled := !next.IsZero()
	missed := now.Sub(sc.NextRunAt) > missedGrace
	catchUp := sc.MissedRun == "once" && missedRecently(p, sc, now)
	if s.isPaused() || (missed && !catchUp) {
		return s.store.Schedules().SetRun(ctx, sc.ID, sc.LastRunAt, next, sc.LastStatus, sc.Failures, enabled)
	}
	_, _, err = s.fire(ctx, sc, now, next, enabled, false)
	switch {
	case errors.Is(err, errUnsaved):
		return err
	case err != nil && !errors.Is(err, errSkipped):
		s.log.Info("scheduler: tick did not start a run", "schedule", sc.ID, "err", err)
	}
	return nil
}

// missedRecently reports whether the latest occurrence that was missed lies
// within missedOnceWindow. For a recurring schedule that is the newest tick
// before now, not the stored (oldest) missed one, so a frequent job always
// gets its single catch-up after an outage.
func missedRecently(p plan, sc *core.Schedule, now time.Time) bool {
	if now.Sub(sc.NextRunAt) < missedOnceWindow {
		return true
	}
	if p.kind == core.SchedOnce {
		return false
	}
	t, err := p.next(now.Add(-missedOnceWindow), 0)
	return err == nil && !t.IsZero() && !t.After(now)
}

var (
	// errSkipped marks a tick that deliberately started nothing (empty
	// HEARTBEAT.md, heartbeat cap reached, disabled user or space).
	errSkipped = errors.New("tick skipped")
	// errUnsaved marks a run whose outcome could not be written to its
	// schedule row.
	errUnsaved = errors.New("run state not saved")
)

// runState is what one fired tick writes into its schedule row. It is kept
// in Service.unsaved when the write fails, so the loop retries the write
// instead of firing the same tick again.
type runState struct {
	tick, last, next time.Time
	status           core.RunStatus
	failures         int
	enabled, disable bool
	run              core.RunID
	session          core.SessionID
}

// notice is an automatic disable the owner must hear about. Notices are sent
// after s.mu is released: a slow channel API must not stall the loop.
type notice struct {
	sc   *core.Schedule
	last core.RunStatus
	kind core.BudgetKind // which limit stopped the last task, when it hit one
	run  core.RunID      // the last task, when it started
	// session is the chat an account hears in: the last task's, or the one its
	// tick made; "" when the tick failed before making one.
	session core.SessionID
}

// fire submits one run of sc and records the outcome in the schedule row. For
// manual runs next and enabled are the row's own values. It returns the run
// and the chat it writes in; the error explains a run that did not start, and
// the row is updated either way.
func (s *Service) fire(ctx context.Context, sc *core.Schedule, now, next time.Time, enabled, manual bool) (core.RunID, core.SessionID, error) {
	run, ses, status, failed, err := s.submit(ctx, sc, now)
	st := runState{tick: sc.NextRunAt, last: sc.LastRunAt, next: next, status: sc.LastStatus, failures: sc.Failures, enabled: enabled,
		session: ses}
	if failed && !manual {
		st.failures++
	}
	if manual && run != "" {
		s.manual[run] = true
	}
	st.run = run
	if status != "" {
		st.last, st.status = now, status
	}
	if enabled && st.failures >= maxFailures {
		st.enabled, st.next, st.disable = false, time.Time{}, true
	}
	var serr error
	if !manual || status != "" || st.disable {
		if serr = s.saveRun(ctx, sc, st); serr != nil && !manual {
			s.unsaved[sc.ID] = st
		}
	}
	if status != "" {
		s.publishRun(sc, run, status)
	}
	if serr != nil {
		return run, ses, errors.Join(err, serr)
	}
	return run, ses, err
}

// saveRun writes a tick's outcome and queues the owner notice when it turned
// the schedule off. Callers hold s.mu.
func (s *Service) saveRun(ctx context.Context, sc *core.Schedule, st runState) error {
	if err := s.store.Schedules().SetRun(ctx, sc.ID, st.last, st.next, st.status, st.failures, st.enabled); err != nil {
		return fmt.Errorf("scheduler: save run state: %w: %w", errUnsaved, err)
	}
	if st.disable {
		s.notices = append(s.notices, notice{sc: sc, last: st.status, run: st.run, session: st.session})
	}
	return nil
}

func (s *Service) takeNotices() []notice {
	n := s.notices
	s.notices = nil
	return n
}

func (s *Service) sendNotices(ctx context.Context, notes []notice) {
	for _, n := range notes {
		s.disabled(ctx, n)
	}
}

// submit starts the run in the chat it returns. status "" means nothing was
// started and nothing is recorded; failed counts toward auto-disable.
func (s *Service) submit(ctx context.Context, sc *core.Schedule, now time.Time) (run core.RunID, session core.SessionID, status core.RunStatus, failed bool, err error) {
	prin, err := s.store.Principals().Get(ctx, core.SystemViewer(), sc.Principal)
	if err != nil {
		return "", "", core.RunFailed, true, fmt.Errorf("scheduler: principal: %w", err)
	}
	if err := s.checkActive(ctx, prin); err != nil {
		if errors.Is(err, errSkipped) {
			return "", "", "", false, err
		}
		return "", "", core.RunFailed, true, err
	}
	trigger, input := core.TrigSchedule, []core.Part{{Type: core.PartText, Text: sc.Prompt}}
	if sc.Kind == core.SchedHeartbeat {
		trigger = core.TrigHeartbeat
		if input, err = s.heartbeatInput(ctx, sc); err != nil {
			return "", "", "", false, err
		}
	}
	ses, err := s.targetSession(ctx, sc, now)
	if err != nil {
		return "", "", core.RunFailed, true, err
	}
	req := core.RunRequest{
		Session: ses.ID, Principal: sc.Principal, User: prin.User, Trigger: trigger, Schedule: sc.ID, Slot: sc.Slot,
		Input:      input,
		Provenance: core.ProvUser, Sender: "schedule:" + string(sc.ID),
		Sink:   s.deliverySink(ctx, sc, prin),
		Limits: s.runLimits(sc.Limits),
	}
	res, err := s.runner.Submit(ctx, req)
	var be *core.BudgetError
	switch {
	case err == nil:
		return res.Run, ses.ID, core.RunQueued, false, nil
	case errors.Is(err, core.ErrPaused):
		return "", ses.ID, "", false, err
	case errors.Is(err, core.ErrBusy):
		return s.recordSkipped(ctx, sc, prin, ses.ID, trigger, now), ses.ID, core.RunSkipped, false, err
	case errors.As(err, &be):
		return "", ses.ID, core.RunBudgetExceeded, false, err
	}
	return "", ses.ID, core.RunFailed, true, fmt.Errorf("scheduler: submit: %w", err)
}

// checkActive refuses ticks of a disabled user's schedules, and of a
// channel or teammate that may not run now (turned off, blocked, no longer
// in the team): neither may start runs, whatever their schedules say. A
// refusal is a skip, never a failure towards auto-disable.
func (s *Service) checkActive(ctx context.Context, prin *core.Principal) error {
	if prin.TeamOwned() {
		if s.hub != nil {
			if err := s.hub.TeamActive(ctx, prin.ID); err != nil {
				return skipError{fmt.Errorf("%s may not run now: %w: %w", prin.ID, core.ErrForbidden, err)}
			}
			return nil
		}
		sp, err := s.store.Spaces().Get(ctx, core.SystemViewer(), prin.ID)
		if err != nil {
			return fmt.Errorf("scheduler: space: %w", err)
		}
		if !sp.Enabled {
			return skipError{fmt.Errorf("space %s is disabled: %w", prin.ID, core.ErrForbidden)}
		}
		return nil
	}
	u, err := s.store.Users().Get(ctx, prin.User)
	if err != nil {
		return fmt.Errorf("scheduler: user: %w", err)
	}
	if u.Disabled {
		return skipError{fmt.Errorf("user %s is disabled: %w", u.ID, core.ErrForbidden)}
	}
	return nil
}

// runLimits fills the limits a schedule leaves at zero from budgets.scheduled
// (SPEC §5.16), so a partly set override never lifts the other limits.
func (s *Service) runLimits(l core.RunLimits) *core.RunLimits {
	d := s.cfg.Current().Budgets.Scheduled
	if l.MaxSteps == 0 {
		l.MaxSteps = d.MaxSteps
	}
	if l.MaxTokens == 0 {
		l.MaxTokens = d.MaxTokens
	}
	if l.MaxTime == 0 {
		l.MaxTime = d.MaxTime.D()
	}
	if l.MaxCost == 0 {
		l.MaxCost = core.MicrosFromUSD(d.MaxUSD)
	}
	return &l
}

// targetSession is the schedule's own session, or a fresh one per run so
// independent runs never share history (SPEC §5.16). A fresh one is filed
// under its schedule and named after it with the local date ("Check-in ·
// 19 Oct"); a check-in's stays hidden until it has something to say
// (showCheckIn).
func (s *Service) targetSession(ctx context.Context, sc *core.Schedule, now time.Time) (*core.Session, error) {
	if sc.Session != "" {
		ses, err := s.store.Sessions().Get(ctx, core.SystemViewer(), sc.Session)
		if err == nil && !ses.Archived && ses.Principal == sc.Principal {
			return ses, nil
		}
		s.log.Warn("scheduler: target session is gone; using a fresh session", "schedule", sc.ID, "session", sc.Session)
	}
	ses, err := s.runner.NewSession(ctx, sc.Principal, "schedule", nil)
	if err != nil {
		return nil, fmt.Errorf("scheduler: new session: %w", err)
	}
	loc, lerr := loadLocation(sc.TZ)
	if lerr != nil {
		loc = time.UTC
	}
	name := sc.Name
	if sc.Kind == core.SchedHeartbeat {
		name = checkInName
	}
	day := now.In(loc).Format("2 Jan")
	if !sc.LastRunAt.IsZero() && sc.LastRunAt.In(loc).Format("2006-01-02") == now.In(loc).Format("2006-01-02") {
		// Another run already made "Name · 19 Oct" today; the sidebar would show two alike.
		day += ", " + now.In(loc).Format("15:04")
	}
	ses.Title, ses.ScheduleID, ses.Hidden = name+" · "+day, sc.ID, sc.Kind == core.SchedHeartbeat
	ses.UpdatedAt = now
	if err := s.store.Sessions().Update(ctx, ses); err != nil {
		s.log.Warn("scheduler: title schedule session", "session", ses.ID, "err", err)
	}
	return ses, nil
}

func (s *Service) recordSkipped(ctx context.Context, sc *core.Schedule, prin *core.Principal, ses core.SessionID, trigger core.Trigger, now time.Time) core.RunID {
	r := &core.Run{
		ID: core.RunID(core.NewID(core.PrefixRun)), Session: ses, Principal: sc.Principal, User: prin.User,
		Trigger: trigger, Schedule: sc.ID, Status: core.RunSkipped, Error: "session queue full",
		Limits: *s.runLimits(sc.Limits), CreatedAt: now, FinishedAt: now,
	}
	if err := s.store.Runs().Create(ctx, r); err != nil {
		s.log.Error("scheduler: record skipped run", "schedule", sc.ID, "err", err)
		return ""
	}
	return r.ID
}

// deliverySink returns the final-mode sink for the schedule's delivery chat,
// or nil for web-only delivery. Heartbeat sinks hold the run back until it is
// clear whether the answer is HEARTBEAT_OK.
func (s *Service) deliverySink(ctx context.Context, sc *core.Schedule, prin *core.Principal) core.RunSink {
	chat, ok := s.deliveryChat(ctx, sc, prin)
	if !ok {
		return nil
	}
	sink := s.hub.StreamTo(chat, "final")
	if sc.Kind == core.SchedHeartbeat && sink != nil {
		return &quietSink{next: sink}
	}
	return sink
}

// deliveryChat resolves the delivery target when the schedule fires. A stored
// chat is checked again here: the teammate or channel may have changed since
// the schedule was saved. An account's results stay on the web.
func (s *Service) deliveryChat(ctx context.Context, sc *core.Schedule, prin *core.Principal) (core.ChatRef, bool) {
	if s.hub == nil || !prin.TeamOwned() {
		return core.ChatRef{}, false
	}
	d := sc.Deliver
	switch {
	case d == nil:
		return s.hub.PrincipalChat(ctx, prin.ID)
	case d.Channel == "web":
		return core.ChatRef{}, false
	case d.ChatID != "":
		chat, ok, err := s.ownChat(ctx, prin, *d)
		if err != nil || !ok {
			s.log.Warn("scheduler: delivery chat is no longer the principal's; web only", "schedule", sc.ID, "channel", d.Channel, "err", err)
			return core.ChatRef{}, false
		}
		return chat, true
	}
	if chat, ok := s.hub.PrincipalChat(ctx, prin.ID); ok && chat.Channel == d.Channel {
		return chat, true
	}
	s.log.Warn("scheduler: no chat on that app to deliver to; web only", "schedule", sc.ID, "channel", d.Channel)
	return core.ChatRef{}, false
}

// ownChat resolves c to a chat the principal's output may go to: a
// teammate's own DM, or a channel's own chat. Scheduled output carries the
// principal's private context, so no other chat qualifies; an account has
// no chat at all.
func (s *Service) ownChat(ctx context.Context, prin *core.Principal, c core.ChatRef) (core.ChatRef, bool, error) {
	switch prin.Kind {
	case core.PrincipalPerson:
		id, err := s.store.Identities().ByPrincipal(ctx, prin.ID)
		if errors.Is(err, core.ErrNotFound) {
			return core.ChatRef{}, false, nil
		}
		if err != nil {
			return core.ChatRef{}, false, fmt.Errorf("scheduler: identity: %w", err)
		}
		if id.Channel != c.Channel || (c.ChatID != id.DMChat && c.ChatID != id.ExternalID) {
			return core.ChatRef{}, false, nil
		}
		return core.ChatRef{Channel: c.Channel, ChatID: c.ChatID, Kind: core.ChatDM}, true, nil
	case core.PrincipalSpace:
		sp, err := s.store.Spaces().Get(ctx, core.SystemViewer(), prin.ID)
		if errors.Is(err, core.ErrNotFound) {
			return core.ChatRef{}, false, nil
		}
		if err != nil {
			return core.ChatRef{}, false, fmt.Errorf("scheduler: space: %w", err)
		}
		if sp.Chat.Channel != c.Channel || sp.Chat.ChatID != c.ChatID {
			return core.ChatRef{}, false, nil
		}
		return core.ChatRef{Channel: c.Channel, ChatID: c.ChatID, ThreadID: c.ThreadID, Kind: sp.Chat.Kind}, true, nil
	}
	return core.ChatRef{}, false, nil
}

// onRunEvent handles a run.finished or run.parked.
func (s *Service) onRunEvent(ctx context.Context, e core.Event) {
	if e.Run == "" {
		return
	}
	run, err := s.store.Runs().Get(ctx, core.SystemViewer(), e.Run)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			s.log.Error("scheduler: run outcome", "run", e.Run, "err", err)
		}
		return
	}
	if e.Type == core.EvRunFinished {
		s.onFinished(ctx, e, run)
	}
	s.showCheckIn(ctx, e, run)
}

// runOutcome is how a run.finished says the run ended, else fallback.
func runOutcome(e core.Event, fallback core.RunStatus) (core.RunStatus, core.BudgetKind) {
	switch d := e.Data.(type) {
	case core.RunFinishedData:
		return d.Status, d.Budget
	case *core.RunFinishedData:
		if d != nil {
			return d.Status, d.Budget
		}
	}
	return fallback, ""
}

// onFinished folds a run outcome into its schedule: success resets the
// failure count, and the third consecutive failure disables the schedule.
func (s *Service) onFinished(ctx context.Context, e core.Event, run *core.Run) {
	if run.Schedule == "" {
		return
	}
	status, kind := runOutcome(e, run.Status)

	s.mu.Lock()
	manual := s.manual[run.ID]
	delete(s.manual, run.ID)
	sc, err := s.store.Schedules().Get(ctx, core.SystemViewer(), run.Schedule)
	if err != nil {
		s.mu.Unlock()
		return
	}
	failures, enabled, next := sc.Failures, sc.Enabled, sc.NextRunAt
	switch status {
	case core.RunDone:
		failures = 0
	case core.RunFailed, core.RunBudgetExceeded, core.RunLoopDetected:
		if !manual {
			failures++
		}
	}
	disable := enabled && failures >= maxFailures
	if disable {
		enabled, next = false, time.Time{}
	}
	err = s.store.Schedules().SetRun(ctx, sc.ID, sc.LastRunAt, next, status, failures, enabled)
	s.mu.Unlock()
	if err != nil {
		s.log.Error("scheduler: save run outcome", "schedule", sc.ID, "err", err)
		return
	}
	s.publishRun(sc, e.Run, status)
	if disable {
		s.sendNotices(ctx, []notice{{sc: sc, last: status, kind: kind, run: run.ID, session: run.Session}})
	}
}

func (s *Service) publishRun(sc *core.Schedule, run core.RunID, status core.RunStatus) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(core.Event{Type: core.EvScheduleRun, Principal: sc.Principal, Run: run,
		Data: core.ScheduleRunData{Schedule: sc.ID, Status: status}})
}

// disabled audits an automatic disable and tells the owner: a teammate or a
// channel in their chat, an account in the chat of the last failed task.
func (s *Service) disabled(ctx context.Context, n notice) {
	sc, last, kind := n.sc, n.last, n.kind
	off := *sc
	off.Enabled = false
	s.auditf(ctx, "schedule.update", systemActor, &off, "", map[string]any{"reason": "consecutive_failures", "failures": maxFailures})
	prin, err := s.store.Principals().Get(ctx, core.SystemViewer(), sc.Principal)
	if err != nil {
		return
	}
	msg := fmt.Sprintf("The schedule “%s” was turned off because its last %d tasks failed%s. Turn it back on under Schedules once it's fixed.",
		plain(sc.Name), maxFailures, lastFailure(last, kind))
	if !prin.TeamOwned() {
		s.webNotice(ctx, n, msg)
		return
	}
	if s.hub == nil {
		return
	}
	chat, ok := s.hub.PrincipalChat(ctx, prin.ID)
	if !ok {
		return
	}
	if prin.TeamOwned() {
		msg = fmt.Sprintf("The schedule “%s” was turned off because its last %d tasks failed%s. An admin can turn it back on in Bothy once it's fixed.",
			plain(sc.Name), maxFailures, lastFailure(last, kind))
	}
	if _, err := s.hub.Send(ctx, chat, msg); err != nil {
		s.log.Warn("scheduler: notify owner", "schedule", sc.ID, "err", err)
	}
}

// webNotice adds msg as a notice to the chat where an account reads how its
// schedule's tasks went: the last task's, else the one its tick made. A tick
// that failed before making one, or a chat the account has since deleted,
// gets a new chat, so the notice still arrives.
func (s *Service) webNotice(ctx context.Context, n notice, msg string) {
	now := s.clock.Now()
	run := n.run
	var chat *core.Session
	if n.session != "" {
		if c, err := s.store.Sessions().Get(ctx, core.SystemViewer(), n.session); err == nil && !c.Archived {
			chat = c
		}
	}
	if chat == nil {
		c, err := s.targetSession(ctx, n.sc, now)
		if err != nil {
			s.log.Warn("scheduler: notify owner", "schedule", n.sc.ID, "err", err)
			return
		}
		// The notice no longer sits beside that task's messages.
		chat, run = c, ""
	}
	m := &core.Message{Session: chat.ID, Run: run, Role: core.MsgAssistant, Kind: core.KindNotice, Provenance: core.ProvSystem,
		Parts: []core.Part{{Type: core.PartText, Text: msg}}, CreatedAt: now}
	if err := s.store.Messages().Append(ctx, m); err != nil {
		s.log.Warn("scheduler: notify owner", "schedule", n.sc.ID, "err", err)
		return
	}
	// A quiet check-in's chat is hidden; the notice must be seen.
	if chat.Hidden {
		if err := s.store.Sessions().SetHidden(ctx, chat.ID, false); err != nil {
			s.log.Warn("scheduler: notify owner", "schedule", n.sc.ID, "err", err)
		}
		chat.Hidden = false
	}
	// A chat with only this notice was never written in: without a time it
	// would sort below every other chat.
	if chat.LastMessageAt.IsZero() {
		chat.LastMessageAt, chat.UpdatedAt = now, now
		if err := s.store.Sessions().Update(ctx, chat); err != nil {
			s.log.Warn("scheduler: notify owner", "schedule", n.sc.ID, "err", err)
		}
	}
	if s.bus != nil {
		s.bus.Publish(core.Event{Type: core.EvMessageCreated, Principal: n.sc.Principal, Session: chat.ID, Run: run, Data: core.MessageData{Message: m}})
	}
}

// lastFailure says how the last task ended when that helps fix the schedule,
// naming the limit it hit so the owner raises the right one.
func lastFailure(st core.RunStatus, kind core.BudgetKind) string {
	switch st {
	case core.RunBudgetExceeded:
		switch kind {
		case core.BudgetRunCost, core.BudgetDailyCost:
			return " and the last one hit the spending limit"
		case core.BudgetSteps:
			return " and the last one hit the step limit"
		case core.BudgetTime:
			return " and the last one ran out of time"
		case core.BudgetTokens:
			return " and the last one grew too long"
		}
		return " and the last one hit one of its limits"
	case core.RunLoopDetected:
		return " and in the last one Bothy kept repeating the same step"
	}
	return ""
}

// plain strips characters the chat markdown subset gives meaning to, so a
// schedule name cannot turn the notice into a link or formatting.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("*_`[]()<>\\", r) {
			return -1
		}
		return r
	}, s)
}
