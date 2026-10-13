package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	listToolName   = "schedule_list"
	createToolName = "schedule_create"
	deleteToolName = "schedule_delete"
)

// Schedule tools exist only in interactive runs: cron cannot create cron
// (SPEC §5.16), and F3 makes create/delete ask an admin every time.
var messageOnly = []core.Trigger{core.TrigMessage}

func makeSpec(def core.ToolDef, m core.Manifest) core.ToolSpec {
	canon, err := core.CanonicalJSON(def)
	if err != nil {
		panic(fmt.Sprintf("scheduler: tool %s: %v", def.Name, err))
	}
	sum := sha256.Sum256(canon)
	return core.ToolSpec{Def: def, Source: "builtin", Manifest: m, Hash: hex.EncodeToString(sum[:])}
}

func decodeArgs(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 || bytes.Equal(bytes.TrimSpace(args), []byte("null")) {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalidf("invalid arguments: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return invalidf("invalid arguments: trailing data after the JSON object")
	}
	return nil
}

func newAction(tool string, se core.SideEffect, payload any) (*core.Action, error) {
	canon, err := core.CanonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: payload: %w", tool, err)
	}
	return &core.Action{Tool: tool, Kind: core.ActSchedule, SideEffect: se,
		Payload: canon, Fingerprint: core.ActionFingerprint(tool, canon)}, nil
}

// ownViewer limits the tools to the run's own principal.
func ownViewer(env *core.ToolEnv) core.Viewer {
	return core.Viewer{User: env.User, Principals: []core.PrincipalID{env.Principal.ID}}
}

func errorResult(name string, err error) (*core.ToolResult, error) {
	var ue *userError
	if errors.As(err, &ue) || errors.Is(err, core.ErrNotFound) {
		return &core.ToolResult{Name: name, Content: err.Error(), IsError: true}, nil
	}
	return nil, err
}

func (s *Service) userTZ(ctx context.Context, u core.UserID) string {
	if usr, err := s.store.Users().Get(ctx, u); err == nil && usr.Timezone != "" {
		if _, err := loadLocation(usr.Timezone); err == nil {
			return usr.Timezone
		}
	}
	return s.cfg.Current().Timezone
}

func (s *Service) summary(sc *core.Schedule) string {
	p, err := parsePlan(sc.Kind, sc.Spec, sc.TZ, sc.ActiveHours)
	if err != nil {
		return sc.Spec
	}
	return describe(p)
}

func formatNext(sc *core.Schedule) string {
	if sc.NextRunAt.IsZero() {
		return "none"
	}
	loc, err := loadLocation(sc.TZ)
	if err != nil {
		loc = time.UTC
	}
	return sc.NextRunAt.In(loc).Format("Mon 2 Jan 2006 15:04 MST")
}

// ---- schedule_list ----

type listTool struct {
	s    *Service
	spec core.ToolSpec
}

func newListTool(s *Service) *listTool {
	return &listTool{s: s, spec: makeSpec(core.ToolDef{
		Name:        listToolName,
		Description: "List your scheduled tasks with their timing, next run and last outcome.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}, core.Manifest{Title: "List schedules", SideEffect: core.SERead, Triggers: messageOnly, Timeout: 10 * time.Second, MaxOutputKB: 16})}
}

func (t *listTool) Spec() core.ToolSpec { return t.spec }

func (t *listTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a struct{}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	return newAction(listToolName, core.SERead, a)
}

func (t *listTool) Exec(ctx context.Context, env *core.ToolEnv, _ *core.Action) (*core.ToolResult, error) {
	all, err := t.s.store.Schedules().List(ctx, ownViewer(env))
	if err != nil {
		return nil, fmt.Errorf("schedule_list: %w", err)
	}
	if len(all) == 0 {
		return &core.ToolResult{Name: listToolName, Content: "No schedules."}, nil
	}
	var b strings.Builder
	for _, sc := range all {
		state := "on"
		if !sc.Enabled {
			state = "off"
		}
		last := ""
		if sc.LastStatus != "" {
			last = " · last " + string(sc.LastStatus)
		}
		fmt.Fprintf(&b, "- %s “%s” · %s · %s · next %s%s\n", sc.ID, sc.Name, t.s.summary(sc), state, formatNext(sc), last)
	}
	return &core.ToolResult{Name: listToolName, Content: b.String()}, nil
}

// ---- schedule_create ----

type createTool struct {
	s    *Service
	spec core.ToolSpec
}

type createArgs struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Spec    string `json:"spec"`
	TZ      string `json:"tz,omitempty"`
	Prompt  string `json:"prompt"`
	Deliver string `json:"deliver,omitempty"`
}

// createPayload is what the approval card shows and Exec runs: every default
// resolved, delivery pinned to a concrete chat.
type createPayload struct {
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Spec    string        `json:"spec"`
	TZ      string        `json:"tz"`
	When    string        `json:"when"`
	Prompt  string        `json:"prompt"`
	Deliver *core.ChatRef `json:"deliver,omitempty"`
}

func newCreateTool(s *Service) *createTool {
	return &createTool{s: s, spec: makeSpec(core.ToolDef{
		Name: createToolName,
		Description: "Create a scheduled task: the prompt runs later as a new conversation and its final answer is sent to the user. " +
			"kind=cron takes a 5-field cron spec (minute hour day-of-month month day-of-week), e.g. \"0 8 * * 1-5\"; " +
			"kind=once takes a date-time, e.g. \"2026-10-12T09:00\". Times are in tz (default: the user's time zone). " +
			"deliver: preferred (default, the user's usual chat), here (this chat) or web. Needs an admin's approval.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","minLength":1,"maxLength":100},` +
			`"kind":{"type":"string","enum":["cron","once"]},` +
			`"spec":{"type":"string","minLength":1,"maxLength":64},` +
			`"tz":{"type":"string","maxLength":64,"description":"IANA time zone, e.g. Europe/London"},` +
			`"prompt":{"type":"string","minLength":1,"maxLength":8000,"description":"what to do when it runs"},` +
			`"deliver":{"type":"string","enum":["preferred","here","web"]}},` +
			`"required":["name","kind","spec","prompt"],"additionalProperties":false}`),
	}, core.Manifest{Title: "Create schedule", SideEffect: core.SEAdmin, Triggers: messageOnly, Timeout: 10 * time.Second, MaxOutputKB: 4})}
}

func (t *createTool) Spec() core.ToolSpec { return t.spec }

func (t *createTool) Plan(ctx context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a createArgs
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	kind := core.ScheduleKind(a.Kind)
	if kind != core.SchedCron && kind != core.SchedOnce {
		return nil, invalidf("kind must be cron or once, got %q", a.Kind)
	}
	tz := strings.TrimSpace(a.TZ)
	if tz == "" {
		tz = t.s.userTZ(ctx, env.User)
	}
	var deliver *core.ChatRef
	switch a.Deliver {
	case "", "preferred":
	case "web":
		deliver = &core.ChatRef{Channel: "web"}
	case "here":
		switch {
		case env.Chat == nil:
			return nil, invalidf("deliver=here needs a chat; this run has none")
		case env.Chat.Channel == "web" || env.Chat.Channel == "api":
			deliver = &core.ChatRef{Channel: "web"}
		default:
			c := *env.Chat
			deliver = &c
		}
	default:
		return nil, invalidf("deliver must be preferred, here or web, got %q", a.Deliver)
	}
	sc := &core.Schedule{Principal: env.Principal.ID, Name: a.Name, Kind: kind, Spec: a.Spec, TZ: tz,
		Prompt: a.Prompt, Deliver: deliver, Enabled: true}
	norm, p, err := t.s.normalize(ctx, sc)
	if err != nil {
		return nil, err
	}
	next, err := p.next(t.s.clock.Now(), 0)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	if next.IsZero() {
		return nil, invalidf("%s is in the past", norm.Spec)
	}
	return newAction(createToolName, core.SEAdmin, createPayload{
		Name: norm.Name, Kind: string(norm.Kind), Spec: norm.Spec, TZ: norm.TZ, When: describe(p),
		Prompt: norm.Prompt, Deliver: norm.Deliver,
	})
}

func (t *createTool) Exec(ctx context.Context, env *core.ToolEnv, act *core.Action) (*core.ToolResult, error) {
	var a createPayload
	if err := json.Unmarshal(act.Payload, &a); err != nil {
		return nil, fmt.Errorf("schedule_create: payload: %w", err)
	}
	sc := &core.Schedule{Principal: env.Principal.ID, Name: a.Name, Kind: core.ScheduleKind(a.Kind), Spec: a.Spec,
		TZ: a.TZ, Prompt: a.Prompt, Deliver: a.Deliver, Enabled: true}
	if err := t.s.Upsert(ctx, ownViewer(env), sc, "run:"+string(env.Run)); err != nil {
		return errorResult(createToolName, err)
	}
	return &core.ToolResult{Name: createToolName, Content: fmt.Sprintf("Created schedule %s “%s”: %s. Next run: %s.",
		sc.ID, sc.Name, t.s.summary(sc), formatNext(sc))}, nil
}

// ---- schedule_delete ----

type deleteTool struct {
	s    *Service
	spec core.ToolSpec
}

type deleteArgs struct {
	ID core.ScheduleID `json:"id"`
}

// deletePayload carries the schedule's stored name so the approval card says
// what is being deleted; Exec uses only the id.
type deletePayload struct {
	ID   core.ScheduleID `json:"id"`
	Name string          `json:"name"`
}

func newDeleteTool(s *Service) *deleteTool {
	return &deleteTool{s: s, spec: makeSpec(core.ToolDef{
		Name:        deleteToolName,
		Description: "Delete one of your scheduled tasks by id (see schedule_list). Needs an admin's approval.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","pattern":"^sch_[0-9a-hjkmnp-tv-z]{26}$"}},` +
			`"required":["id"],"additionalProperties":false}`),
	}, core.Manifest{Title: "Delete schedule", SideEffect: core.SEAdmin, Triggers: messageOnly, Timeout: 10 * time.Second, MaxOutputKB: 4})}
}

func (t *deleteTool) Spec() core.ToolSpec { return t.spec }

func (t *deleteTool) Plan(ctx context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a deleteArgs
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	if !core.ValidID(core.PrefixSchedule, string(a.ID)) {
		return nil, invalidf("id %q is not a schedule id", a.ID)
	}
	sc, err := t.s.store.Schedules().Get(ctx, ownViewer(env), a.ID)
	if errors.Is(err, core.ErrNotFound) {
		return nil, invalidf("no schedule %s; use schedule_list", a.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("schedule_delete: %w", err)
	}
	return newAction(deleteToolName, core.SEAdmin, deletePayload{ID: sc.ID, Name: sc.Name})
}

func (t *deleteTool) Exec(ctx context.Context, env *core.ToolEnv, act *core.Action) (*core.ToolResult, error) {
	var a deletePayload
	if err := json.Unmarshal(act.Payload, &a); err != nil {
		return nil, fmt.Errorf("schedule_delete: payload: %w", err)
	}
	if err := t.s.deleteAs(ctx, ownViewer(env), a.ID, "run:"+string(env.Run)); err != nil {
		return errorResult(deleteToolName, err)
	}
	return &core.ToolResult{Name: deleteToolName, Content: fmt.Sprintf("Deleted schedule %s “%s”.", a.ID, a.Name)}, nil
}
