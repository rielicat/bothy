package scheduler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// heartbeatOK is the reply that means "nothing needs the user's attention".
const heartbeatOK = "HEARTBEAT_OK"

// skipError marks a tick that deliberately started nothing while keeping the
// reason (an ErrInvalid or *core.BudgetError) for RunNow callers.
type skipError struct{ err error }

func (e skipError) Error() string   { return e.err.Error() }
func (e skipError) Unwrap() []error { return []error{errSkipped, e.err} }

// syncHeartbeat keeps exactly one system heartbeat schedule, for the owner's
// principal, matching scheduler.heartbeat in the owner's time zone. pending
// reports that the heartbeat is enabled but there is no owner yet (first run
// before setup).
func (s *Service) syncHeartbeat(ctx context.Context) (pending bool, err error) {
	cfg := s.cfg.Current()
	hb := cfg.Scheduler.Heartbeat
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, err := s.owner(ctx)
	if err != nil {
		return false, err
	}
	all, err := s.store.Schedules().List(ctx, core.SystemViewer())
	if err != nil {
		return false, fmt.Errorf("scheduler: list schedules: %w", err)
	}
	var mine *core.Schedule
	for _, sc := range all {
		if sc.Kind != core.SchedHeartbeat || sc.Name != heartbeatName || sc.CreatedBy != systemActor {
			continue
		}
		if owner != nil && sc.Principal == core.UserPrincipal(owner.ID) && mine == nil {
			mine = sc
			continue
		}
		if sc.Enabled { // left over from a previous owner
			if err := s.store.Schedules().SetRun(ctx, sc.ID, sc.LastRunAt, time.Time{}, sc.LastStatus, sc.Failures, false); err != nil {
				return false, fmt.Errorf("scheduler: disable stale heartbeat: %w", err)
			}
		}
	}
	if owner == nil {
		return hb.Enabled, nil
	}
	want := &core.Schedule{
		Principal: core.UserPrincipal(owner.ID), Name: heartbeatName, Kind: core.SchedHeartbeat,
		Spec: hb.Every.D().String(), TZ: s.zoneOf(owner), ActiveHours: hb.ActiveHours, Prompt: heartbeatInstruction,
		Slot: core.Slot(hb.Slot), Deliver: heartbeatDeliver(hb.Deliver), MissedRun: "skip", Enabled: hb.Enabled,
	}
	if mine == nil {
		if !hb.Enabled {
			return false, nil
		}
		return false, s.upsertLocked(ctx, core.SystemViewer(), want, systemActor)
	}
	if norm, _, err := s.normalize(ctx, want); err == nil && sameSettings(mine, norm) {
		return false, nil
	}
	want.ID = mine.ID
	return false, s.upsertLocked(ctx, core.SystemViewer(), want, systemActor)
}

func (s *Service) owner(ctx context.Context) (*core.User, error) {
	users, err := s.store.Users().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list users: %w", err)
	}
	for _, u := range users {
		if u.Role == core.RoleOwner && !u.Disabled {
			return u, nil
		}
	}
	return nil, nil
}

// heartbeatDeliver maps scheduler.heartbeat.deliver onto Schedule.Deliver:
// preferred = nil, web = web only, a channel name = the owner's DM there.
func heartbeatDeliver(d string) *core.ChatRef {
	switch d {
	case "", "preferred":
		return nil
	}
	return &core.ChatRef{Channel: d}
}

func sameSettings(a, b *core.Schedule) bool {
	return a.Spec == b.Spec && a.TZ == b.TZ && a.ActiveHours == b.ActiveHours && a.Prompt == b.Prompt &&
		a.Slot == b.Slot && a.MissedRun == b.MissedRun && a.Enabled == b.Enabled && a.Limits == b.Limits &&
		reflect.DeepEqual(a.Deliver, b.Deliver)
}

// heartbeatPrompt inlines HEARTBEAT.md (memory files are not in the
// workspace). An empty file or a spent daily heartbeat budget skips the tick
// before any LLM call.
func (s *Service) heartbeatPrompt(ctx context.Context, sc *core.Schedule) (string, error) {
	if s.memory == nil {
		return "", errors.New("scheduler: no memory service for HEARTBEAT.md")
	}
	content, _, err := s.memory.Read(ctx, core.SystemViewer(), sc.Principal, core.FileHeartbeat)
	if errors.Is(err, core.ErrForbidden) {
		return "", skipError{invalidf("spaces have no HEARTBEAT.md")}
	}
	if err != nil {
		return "", fmt.Errorf("scheduler: read HEARTBEAT.md: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return "", skipError{invalidf("HEARTBEAT.md is empty, so there is nothing to check")}
	}
	if err := s.heartbeatBudget(ctx, sc); err != nil {
		return "", err
	}
	instr := sc.Prompt
	if strings.TrimSpace(instr) == "" {
		instr = heartbeatInstruction
	}
	return instr + "\n\n" + content, nil
}

// heartbeatBudget sums today's spend (config timezone) of the schedule's runs
// against budgets.heartbeat_daily_usd; 0 means no heartbeat cap, as for the
// other daily caps.
func (s *Service) heartbeatBudget(ctx context.Context, sc *core.Schedule) error {
	cfg := s.cfg.Current()
	limit := core.MicrosFromUSD(cfg.Budgets.HeartbeatDailyUSD)
	if limit <= 0 {
		return nil
	}
	loc, err := loadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := s.clock.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	rows, err := s.store.Usage().Report(ctx, core.UsageQuery{From: from, GroupBy: "schedule"})
	if err != nil {
		return fmt.Errorf("scheduler: heartbeat spend: %w", err)
	}
	for _, r := range rows {
		if r.Key == string(sc.ID) && r.Cost >= limit {
			return skipError{&core.BudgetError{Kind: core.BudgetDailyCost, Limit: int64(limit), Used: int64(r.Cost)}}
		}
	}
	return nil
}

// maxQuietEvents bounds what a heartbeat sink holds back; a run that long is
// delivered as it goes.
const maxQuietEvents = 4096

// quietSink suppresses delivery of a heartbeat run whose final answer starts
// with HEARTBEAT_OK. The final-mode sink sends one message at run end anyway,
// so holding events until run.finished changes nothing for runs that do get
// delivered. A parked run is flushed at once: the owner must see the card's
// context.
type quietSink struct {
	mu   sync.Mutex
	next core.RunSink
	buf  []core.Event
	text strings.Builder // text of the current step; tool calls start a new one
	pass bool
	done bool
}

func (q *quietSink) OnEvent(e core.Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch {
	case q.done:
		return
	case q.pass:
		q.next.OnEvent(e)
		return
	}
	switch e.Type {
	case core.EvRunDelta:
		switch d := e.Data.(type) {
		case core.DeltaData:
			q.text.WriteString(d.Text)
		case *core.DeltaData:
			if d != nil {
				q.text.WriteString(d.Text)
			}
		}
	case core.EvToolCall, core.EvToolResult:
		q.text.Reset()
	case core.EvRunFinished:
		if isHeartbeatOK(q.text.String()) {
			q.buf, q.done = nil, true
			return
		}
	}
	q.buf = append(q.buf, e)
	if e.Type == core.EvRunFinished || e.Type == core.EvRunParked || len(q.buf) >= maxQuietEvents {
		for _, b := range q.buf {
			q.next.OnEvent(b)
		}
		q.buf, q.pass = nil, true
	}
}

// isHeartbeatOK tolerates markdown emphasis or code ticks around the token.
func isHeartbeatOK(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(strings.TrimSpace(text), "*_`> "), heartbeatOK)
}
