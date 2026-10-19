package scheduler

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	maxNameRunes   = 100
	maxPromptBytes = 8000
	maxPreview     = 50

	heartbeatName = "heartbeat"
	systemActor   = "system"

	heartbeatInstruction = "Here is HEARTBEAT.md. If nothing needs the user's attention, reply exactly HEARTBEAT_OK."
)

var channelNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// userError carries a message meant for the model or the web UI verbatim while
// still matching a core sentinel with errors.Is.
type userError struct {
	msg  string
	kind error
}

func (e *userError) Error() string { return e.msg }
func (e *userError) Unwrap() error { return e.kind }

func invalidf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...), kind: core.ErrInvalid}
}

// Upsert validates and stores a schedule. Run-state fields (failures, last
// run, next run, created_by) are system-managed: caller values are ignored.
// A schedule without a time zone gets the viewer's own.
func (s *Service) Upsert(ctx context.Context, v core.Viewer, sc *core.Schedule, by string) error {
	if sc == nil {
		return invalidf("schedule is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsertLocked(ctx, v, sc, by); err != nil {
		return err
	}
	s.poke()
	return nil
}

func (s *Service) upsertLocked(ctx context.Context, v core.Viewer, in *core.Schedule, by string) error {
	now := s.clock.Now()
	var cur *core.Schedule
	if in.ID != "" {
		c, err := s.store.Schedules().Get(ctx, v, in.ID)
		if err != nil {
			return err
		}
		cur = c
		if in.Principal == "" {
			in.Principal = cur.Principal
		}
		if in.Principal != cur.Principal {
			return invalidf("a schedule cannot move to another principal")
		}
	}
	if in.Principal == "" && v.User != "" {
		in.Principal = core.UserPrincipal(v.User)
	}
	if in.Principal == "" || !v.Can(in.Principal) {
		return fmt.Errorf("principal %q: %w", in.Principal, core.ErrNotFound)
	}
	if strings.TrimSpace(in.TZ) == "" && v.User != "" {
		in.TZ = s.userTZ(ctx, v.User)
	}
	sc, p, err := s.normalize(ctx, in)
	if err != nil {
		return err
	}
	sc.CreatedBy, sc.CreatedAt = by, now
	sc.Failures, sc.LastRunAt, sc.LastStatus = 0, time.Time{}, ""
	if cur != nil {
		sc.CreatedBy, sc.CreatedAt = cur.CreatedBy, cur.CreatedAt
		sc.Failures, sc.LastRunAt, sc.LastStatus = cur.Failures, cur.LastRunAt, cur.LastStatus
		if sc.Enabled && !cur.Enabled {
			sc.Failures = 0
		}
	}
	sc.UpdatedAt = now
	sc.NextRunAt = time.Time{}
	if sc.Enabled {
		next, err := p.next(now, s.jitter())
		if err != nil {
			return invalidf("%v", err)
		}
		if next.IsZero() {
			return invalidf("%s is in the past", p.once.In(p.loc).Format("Mon 2 Jan 2006 15:04 MST"))
		}
		sc.NextRunAt = next
	}
	if err := s.store.Schedules().Upsert(ctx, sc); err != nil {
		return fmt.Errorf("scheduler: save schedule: %w", err)
	}
	delete(s.unsaved, sc.ID)
	*in = *sc
	action := "schedule.update"
	if cur == nil {
		action = "schedule.create"
	}
	s.auditf(ctx, action, auditActor(by), sc, "", nil)
	return nil
}

// normalize returns a validated copy of in with defaults filled in, and its
// parsed plan.
func (s *Service) normalize(ctx context.Context, in *core.Schedule) (*core.Schedule, plan, error) {
	sc := *in
	cfg := s.cfg.Current()
	sc.Name = strings.TrimSpace(sc.Name)
	switch n := utf8.RuneCountInString(sc.Name); {
	case n == 0:
		return nil, plan{}, invalidf("name is required")
	case n > maxNameRunes:
		return nil, plan{}, invalidf("name is longer than %d characters", maxNameRunes)
	case strings.IndexFunc(sc.Name, unicode.IsControl) >= 0:
		return nil, plan{}, invalidf("name contains control characters")
	}
	sc.TZ = strings.TrimSpace(sc.TZ)
	if sc.TZ == "" {
		sc.TZ = cfg.Timezone
	}
	p, err := parsePlan(sc.Kind, sc.Spec, sc.TZ, sc.ActiveHours)
	if err != nil {
		return nil, plan{}, invalidf("%v", err)
	}
	sc.Spec, sc.ActiveHours = p.spec, p.active.String()

	if strings.TrimSpace(sc.Prompt) == "" {
		if sc.Kind != core.SchedHeartbeat {
			return nil, plan{}, invalidf("prompt is required")
		}
		sc.Prompt = heartbeatInstruction
	}
	if len(sc.Prompt) > maxPromptBytes {
		return nil, plan{}, invalidf("prompt is longer than %d bytes", maxPromptBytes)
	}
	if !utf8.ValidString(sc.Prompt) || strings.ContainsRune(sc.Prompt, 0) {
		return nil, plan{}, invalidf("prompt is not valid text")
	}

	if sc.MissedRun == "" {
		sc.MissedRun = cfg.Scheduler.MissedRun
	}
	if sc.MissedRun != "skip" && sc.MissedRun != "once" {
		return nil, plan{}, invalidf("missed_run must be skip or once, got %q", sc.MissedRun)
	}
	if sc.Slot == "" {
		sc.Slot = core.SlotMain
		if sc.Kind == core.SchedHeartbeat {
			sc.Slot = core.SlotFast
		}
	}
	if sc.Slot != core.SlotMain && sc.Slot != core.SlotFast {
		return nil, plan{}, invalidf("slot must be main or fast, got %q", sc.Slot)
	}
	if l := sc.Limits; l.MaxSteps < 0 || l.MaxTokens < 0 || l.MaxTime < 0 || l.MaxCost < 0 {
		return nil, plan{}, invalidf("limits must not be negative")
	}

	prin, err := s.store.Principals().Get(ctx, core.SystemViewer(), sc.Principal)
	if err != nil {
		return nil, plan{}, fmt.Errorf("principal %q: %w", sc.Principal, err)
	}
	if sc.Kind == core.SchedHeartbeat && prin.Kind != core.PrincipalUser {
		return nil, plan{}, invalidf("a check-in needs an account on the web")
	}
	if sc.Deliver, err = s.checkDeliver(ctx, prin, sc.Deliver); err != nil {
		return nil, plan{}, err
	}
	if sc.Session != "" {
		ses, err := s.store.Sessions().Get(ctx, core.SystemViewer(), sc.Session)
		if err != nil || ses.Principal != sc.Principal {
			return nil, plan{}, invalidf("session %s is not a session of this principal", sc.Session)
		}
		if ses.Archived {
			return nil, plan{}, invalidf("session %s is archived", sc.Session)
		}
	}
	return &sc, p, nil
}

// checkDeliver confines delivery to chats the principal's output may reach
// (ownChat). A channel without a chat id means the principal's own chat on
// that channel, resolved when the schedule fires. The web is only for
// accounts: a teammate or channel hears in their chat.
func (s *Service) checkDeliver(ctx context.Context, prin *core.Principal, d *core.ChatRef) (*core.ChatRef, error) {
	if d == nil {
		return nil, nil
	}
	c := *d
	c.Channel = strings.TrimSpace(c.Channel)
	switch {
	case c.Channel == "web" && prin.TeamOwned():
		return nil, invalidf("deliver: web is only for accounts on the web; use preferred")
	case c.Channel == "web":
		return &core.ChatRef{Channel: "web"}, nil
	case !channelNameRe.MatchString(c.Channel):
		return nil, invalidf("deliver: unknown channel %q", c.Channel)
	case c.ChatID == "":
		return &core.ChatRef{Channel: c.Channel}, nil
	}
	chat, ok, err := s.ownChat(ctx, prin, c)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, invalidf("deliver: %s chat %s is not this principal's own chat", c.Channel, c.ChatID)
	}
	return &chat, nil
}

func (s *Service) Delete(ctx context.Context, v core.Viewer, id core.ScheduleID, by core.UserID) error {
	return s.deleteAs(ctx, v, id, "user:"+string(by))
}

// deleteAs deletes a schedule; by follows Upsert ("user:<id>" or
// "run:<id>" for an agent-approved delete).
func (s *Service) deleteAs(ctx context.Context, v core.Viewer, id core.ScheduleID, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, err := s.store.Schedules().Get(ctx, v, id)
	if err != nil {
		return err
	}
	if err := s.store.Schedules().Delete(ctx, v, id); err != nil {
		return err
	}
	delete(s.unsaved, id)
	s.auditf(ctx, "schedule.delete", auditActor(by), sc, "", nil)
	s.poke()
	return nil
}

// RunNow fires a schedule immediately without moving its next run. The run
// is the schedule owner's, so the audit is what says who started it.
func (s *Service) RunNow(ctx context.Context, v core.Viewer, id core.ScheduleID, by core.UserID) (core.RunID, error) {
	s.mu.Lock()
	sc, run, err := s.runNowLocked(ctx, v, id)
	notes := s.takeNotices()
	s.mu.Unlock()
	if err == nil {
		s.auditf(ctx, "schedule.run", "user:"+string(by), sc, run, nil)
	}
	s.sendNotices(ctx, notes)
	return run, err
}

func (s *Service) runNowLocked(ctx context.Context, v core.Viewer, id core.ScheduleID) (*core.Schedule, core.RunID, error) {
	sc, err := s.store.Schedules().Get(ctx, v, id)
	if err != nil {
		return nil, "", err
	}
	if s.isPaused() {
		return nil, "", fmt.Errorf("scheduler: %w", core.ErrPaused)
	}
	run, err := s.fire(ctx, sc, s.clock.Now(), sc.NextRunAt, sc.Enabled, true)
	return sc, run, err
}

// Preview returns the next n fire times in tz (heartbeats without jitter).
func (s *Service) Preview(kind core.ScheduleKind, spec, tz string, n int) ([]time.Time, error) {
	p, err := s.previewPlan(kind, spec, tz)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		n = 5
	}
	n = min(n, maxPreview)
	var out []time.Time
	t := s.clock.Now()
	for len(out) < n {
		nt, err := p.next(t, 0)
		if err != nil {
			if len(out) > 0 {
				break
			}
			return nil, invalidf("%v", err)
		}
		if nt.IsZero() {
			break
		}
		out = append(out, nt.In(p.loc))
		t = nt
	}
	return out, nil
}

// Describe renders a schedule spec in plain English, e.g. "Weekdays at 08:00
// (Europe/London)", for the preview endpoint and the schedule tools.
func (s *Service) Describe(kind core.ScheduleKind, spec, tz string) (string, error) {
	p, err := s.previewPlan(kind, spec, tz)
	if err != nil {
		return "", err
	}
	return describe(p), nil
}

func (s *Service) previewPlan(kind core.ScheduleKind, spec, tz string) (plan, error) {
	if strings.TrimSpace(tz) == "" {
		tz = s.cfg.Current().Timezone
	}
	p, err := parsePlan(kind, spec, strings.TrimSpace(tz), "")
	if err != nil {
		return plan{}, invalidf("%v", err)
	}
	return p, nil
}

// SetPaused stops firing independently of the kill switch, which isPaused
// reads from Runner.Paused(). Ticks that fall due while paused are skipped,
// not replayed on resume.
func (s *Service) SetPaused(paused bool) {
	s.paused.Store(paused)
	s.poke()
}

func (s *Service) isPaused() bool {
	return s.paused.Load() || s.runner.Paused() || !s.cfg.Current().Scheduler.Enabled
}

func auditActor(by string) string {
	if run, ok := strings.CutPrefix(by, "run:"); ok {
		return "agent:" + run
	}
	return by
}

// auditf records an action on sc; run is the task it is about, and an agent
// actor's own run when empty.
func (s *Service) auditf(ctx context.Context, action, actor string, sc *core.Schedule, run core.RunID, extra map[string]any) {
	if s.audit == nil {
		return
	}
	d := map[string]any{"schedule_id": sc.ID, "principal_id": sc.Principal, "name": sc.Name, "kind": sc.Kind,
		"spec": sc.Spec, "tz": sc.TZ, "enabled": sc.Enabled}
	var ip string
	// A change made over HTTP is recorded here only, with the request's
	// actor (a token, say), address and detail.
	if o, ok := core.AuditOriginFrom(ctx); ok {
		actor, ip = cmp.Or(o.Actor, actor), o.IP
		maps.Copy(d, o.Detail)
	}
	maps.Copy(d, extra)
	detail, _ := json.Marshal(d)
	e := core.AuditEntry{At: s.clock.Now(), Actor: actor, Action: action, Target: "schedule:" + string(sc.ID), Outcome: "ok", Detail: detail, Run: run, IP: ip}
	if agentRun, ok := strings.CutPrefix(actor, "agent:"); ok && run == "" {
		e.Run = core.RunID(agentRun)
	}
	if err := s.audit.Record(ctx, e); err != nil {
		s.log.Error("scheduler: audit", "action", action, "err", err)
	}
}
