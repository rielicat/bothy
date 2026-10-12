package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
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
	*in = *sc
	action := "schedule.update"
	if cur == nil {
		action = "schedule.create"
	}
	s.auditf(ctx, action, auditActor(by), sc, nil)
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
		return nil, plan{}, invalidf("a heartbeat needs HEARTBEAT.md, and spaces have no memory")
	}
	if sc.Deliver, err = s.checkDeliver(ctx, prin.User, sc.Deliver); err != nil {
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

// checkDeliver confines delivery to the owner's own chats: a linked DM, or the
// chat of a space they own. A channel without a chat id means "the owner's DM
// on that channel", resolved when the schedule fires.
func (s *Service) checkDeliver(ctx context.Context, owner core.UserID, d *core.ChatRef) (*core.ChatRef, error) {
	if d == nil {
		return nil, nil
	}
	c := *d
	c.Channel = strings.TrimSpace(c.Channel)
	switch {
	case c.Channel == "web":
		return &core.ChatRef{Channel: "web"}, nil
	case !channelNameRe.MatchString(c.Channel):
		return nil, invalidf("deliver: unknown channel %q", c.Channel)
	case c.ChatID == "":
		return &core.ChatRef{Channel: c.Channel}, nil
	}
	ids, err := s.store.Identities().ForUser(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("scheduler: identities: %w", err)
	}
	for _, id := range ids {
		if id.Channel == c.Channel && id.Status == core.IdentityLinked && id.DMChat == c.ChatID {
			return &core.ChatRef{Channel: c.Channel, ChatID: c.ChatID, Kind: core.ChatDM}, nil
		}
	}
	if sp, err := s.store.Spaces().ByChat(ctx, c.Channel, c.ChatID); err == nil && sp.Owner == owner {
		return &core.ChatRef{Channel: c.Channel, ChatID: c.ChatID, ThreadID: c.ThreadID, Kind: sp.Chat.Kind}, nil
	}
	return nil, invalidf("deliver: %s chat %s is not one of your chats", c.Channel, c.ChatID)
}

func (s *Service) Delete(ctx context.Context, v core.Viewer, id core.ScheduleID, by core.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, err := s.store.Schedules().Get(ctx, v, id)
	if err != nil {
		return err
	}
	if err := s.store.Schedules().Delete(ctx, v, id); err != nil {
		return err
	}
	s.auditf(ctx, "schedule.delete", "user:"+string(by), sc, nil)
	s.poke()
	return nil
}

// RunNow fires a schedule immediately without moving its next run.
func (s *Service) RunNow(ctx context.Context, v core.Viewer, id core.ScheduleID, _ core.UserID) (core.RunID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, err := s.store.Schedules().Get(ctx, v, id)
	if err != nil {
		return "", err
	}
	if s.isPaused() {
		return "", fmt.Errorf("scheduler: %w", core.ErrPaused)
	}
	return s.fire(ctx, sc, s.clock.Now(), sc.NextRunAt, sc.Enabled, true)
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

// SetPaused stops firing (kill switch). Ticks that fall due while paused are
// skipped, not replayed on resume.
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

func (s *Service) auditf(ctx context.Context, action, actor string, sc *core.Schedule, extra map[string]any) {
	if s.audit == nil {
		return
	}
	d := map[string]any{"schedule_id": sc.ID, "principal_id": sc.Principal, "name": sc.Name, "kind": sc.Kind,
		"spec": sc.Spec, "tz": sc.TZ, "enabled": sc.Enabled}
	for k, v := range extra {
		d[k] = v
	}
	detail, _ := json.Marshal(d)
	e := core.AuditEntry{At: s.clock.Now(), Actor: actor, Action: action, Target: "schedule:" + string(sc.ID), Outcome: "ok", Detail: detail}
	if run, ok := strings.CutPrefix(actor, "agent:"); ok {
		e.Run = core.RunID(run)
	}
	if err := s.audit.Record(ctx, e); err != nil {
		s.log.Error("scheduler: audit", "action", action, "err", err)
	}
}
