package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
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
// resumed later.
func (s *Service) Run(ctx context.Context) error {
	var events <-chan core.Event
	var sub core.Subscription
	if s.bus != nil {
		sub = s.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvRunFinished}}, outcomeBuffer)
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
			s.onFinished(ctx, e)
			if n := sub.Dropped(); n > 0 {
				s.log.Warn("scheduler: missed run outcomes; failure counts may be stale", "dropped", n)
			}
		}
	}
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
	defer s.mu.Unlock()
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
	_, err = s.fire(ctx, sc, now, next, enabled, false)
	if err != nil && !errors.Is(err, errSkipped) {
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

// errSkipped marks a tick that deliberately started nothing (empty
// HEARTBEAT.md, heartbeat cap reached).
var errSkipped = errors.New("tick skipped")

// fire submits one run of sc and records the outcome in the schedule row. For
// manual runs next and enabled are the row's own values. The returned error
// explains a run that did not start; the row is updated either way.
func (s *Service) fire(ctx context.Context, sc *core.Schedule, now, next time.Time, enabled, manual bool) (core.RunID, error) {
	run, status, failed, err := s.submit(ctx, sc, now)
	failures, last, lastStatus := sc.Failures, sc.LastRunAt, sc.LastStatus
	if failed {
		failures++
	}
	if status != "" {
		last, lastStatus = now, status
	}
	disable := enabled && failures >= maxFailures
	if disable {
		enabled, next = false, time.Time{}
	}
	if !manual || status != "" || disable {
		if serr := s.store.Schedules().SetRun(ctx, sc.ID, last, next, lastStatus, failures, enabled); serr != nil {
			return run, errors.Join(err, fmt.Errorf("scheduler: save run state: %w", serr))
		}
	}
	if status != "" {
		s.publishRun(sc, run, status)
	}
	if disable {
		s.disabled(ctx, sc, lastStatus)
	}
	return run, err
}

// submit starts the run. status "" means nothing was started and nothing is
// recorded; failed counts toward auto-disable.
func (s *Service) submit(ctx context.Context, sc *core.Schedule, now time.Time) (run core.RunID, status core.RunStatus, failed bool, err error) {
	prin, err := s.store.Principals().Get(ctx, core.SystemViewer(), sc.Principal)
	if err != nil {
		return "", core.RunFailed, true, fmt.Errorf("scheduler: principal: %w", err)
	}
	trigger, prompt := core.TrigSchedule, sc.Prompt
	if sc.Kind == core.SchedHeartbeat {
		trigger = core.TrigHeartbeat
		if prompt, err = s.heartbeatPrompt(ctx, sc); err != nil {
			return "", "", false, err
		}
	}
	ses, err := s.targetSession(ctx, sc, now)
	if err != nil {
		return "", core.RunFailed, true, err
	}
	req := core.RunRequest{
		Session: ses.ID, Principal: sc.Principal, User: prin.User, Trigger: trigger, Schedule: sc.ID, Slot: sc.Slot,
		Input:      []core.Part{{Type: core.PartText, Text: prompt}},
		Provenance: core.ProvUser, Sender: "schedule:" + string(sc.ID),
		Sink: s.deliverySink(ctx, sc, prin.User),
	}
	if sc.Limits != (core.RunLimits{}) {
		l := sc.Limits
		req.Limits = &l
	}
	res, err := s.runner.Submit(ctx, req)
	var be *core.BudgetError
	switch {
	case err == nil:
		return res.Run, core.RunQueued, false, nil
	case errors.Is(err, core.ErrPaused):
		return "", "", false, err
	case errors.Is(err, core.ErrBusy):
		return s.recordSkipped(ctx, sc, prin, ses.ID, trigger, now), core.RunSkipped, false, err
	case errors.As(err, &be):
		return "", core.RunBudgetExceeded, false, err
	}
	return "", core.RunFailed, true, fmt.Errorf("scheduler: submit: %w", err)
}

// targetSession is the schedule's own session, or a fresh one per run so
// independent runs never share history (SPEC §5.16).
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
	ses.Title = sc.Name + " · " + now.In(loc).Format(time.DateOnly)
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
		Limits: sc.Limits, CreatedAt: now, FinishedAt: now,
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
func (s *Service) deliverySink(ctx context.Context, sc *core.Schedule, owner core.UserID) core.RunSink {
	chat, ok := s.deliveryChat(ctx, sc, owner)
	if !ok {
		return nil
	}
	sink := s.hub.StreamTo(chat, "final")
	if sc.Kind == core.SchedHeartbeat && sink != nil {
		return &quietSink{next: sink}
	}
	return sink
}

func (s *Service) deliveryChat(ctx context.Context, sc *core.Schedule, owner core.UserID) (core.ChatRef, bool) {
	if s.hub == nil {
		return core.ChatRef{}, false
	}
	d := sc.Deliver
	switch {
	case d == nil:
		return s.hub.PreferredChat(ctx, owner)
	case d.Channel == "web":
		return core.ChatRef{}, false
	case d.ChatID != "":
		return *d, true
	}
	ids, err := s.store.Identities().ForUser(ctx, owner)
	if err != nil {
		s.log.Warn("scheduler: identities", "user", owner, "err", err)
		return core.ChatRef{}, false
	}
	for _, id := range ids {
		if id.Channel == d.Channel && id.Status == core.IdentityLinked && id.DMChat != "" {
			return core.ChatRef{Channel: d.Channel, ChatID: id.DMChat, Kind: core.ChatDM}, true
		}
	}
	s.log.Warn("scheduler: no linked DM to deliver to; web only", "schedule", sc.ID, "channel", d.Channel)
	return core.ChatRef{}, false
}

// onFinished folds a run outcome into its schedule: success resets the
// failure count, and the third consecutive failure disables the schedule.
func (s *Service) onFinished(ctx context.Context, e core.Event) {
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
	if run.Schedule == "" {
		return
	}
	status := run.Status
	switch d := e.Data.(type) {
	case core.RunFinishedData:
		status = d.Status
	case *core.RunFinishedData:
		if d != nil {
			status = d.Status
		}
	}

	s.mu.Lock()
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
		failures++
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
		s.disabled(ctx, sc, status)
	}
}

func (s *Service) publishRun(sc *core.Schedule, run core.RunID, status core.RunStatus) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(core.Event{Type: core.EvScheduleRun, Principal: sc.Principal, Run: run,
		Data: core.ScheduleRunData{Schedule: sc.ID, Status: status}})
}

// disabled audits an automatic disable and tells the owner in their DM.
func (s *Service) disabled(ctx context.Context, sc *core.Schedule, last core.RunStatus) {
	off := *sc
	off.Enabled = false
	s.auditf(ctx, "schedule.update", systemActor, &off, map[string]any{"reason": "consecutive_failures", "failures": maxFailures})
	if s.hub == nil {
		return
	}
	prin, err := s.store.Principals().Get(ctx, core.SystemViewer(), sc.Principal)
	if err != nil {
		return
	}
	chat, ok := s.hub.PreferredChat(ctx, prin.User)
	if !ok {
		return
	}
	msg := fmt.Sprintf("Schedule “%s” was turned off after %d failed runs in a row (last: %s). Turn it back on under Schedules once it is fixed.",
		plain(sc.Name), maxFailures, last)
	if _, err := s.hub.Send(ctx, chat, msg); err != nil {
		s.log.Warn("scheduler: notify owner", "schedule", sc.ID, "err", err)
	}
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
