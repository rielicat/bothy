package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
)

func (h *harness) env(chat *core.ChatRef) *core.ToolEnv {
	return &core.ToolEnv{
		User: h.owner, Role: core.RoleOwner,
		Principal: core.Principal{ID: h.p, Kind: core.PrincipalUser, User: h.owner, ToolProfile: "standard"},
		Session:   "ses_1", Run: "run_00000000000000000000000001", CallID: core.NewID(core.PrefixToolCall),
		Trigger: core.TrigMessage, Workspace: h.t.TempDir(), Chat: chat, Interactive: true,
	}
}

// teamEnv is the tool env of the teammate tp's own chat.
func (h *harness) teamEnv(chat *core.ChatRef) *core.ToolEnv {
	return &core.ToolEnv{
		Role:      core.RoleMember,
		Principal: core.Principal{ID: h.tp, Kind: core.PrincipalPerson, DisplayName: "Tom", ToolProfile: "standard"},
		Session:   "ses_2", Run: "run_00000000000000000000000002", CallID: core.NewID(core.PrefixToolCall),
		Trigger: core.TrigMessage, Workspace: h.t.TempDir(), Chat: chat, Interactive: true,
	}
}

func (h *harness) tool(name string) core.Tool {
	for _, tl := range h.svc.Tools() {
		if tl.Spec().Def.Name == name {
			return tl
		}
	}
	h.t.Fatalf("no tool %q", name)
	return nil
}

func TestToolManifests(t *testing.T) {
	h := newHarness(t)
	for name, se := range map[string]core.SideEffect{"schedule_list": core.SERead, "schedule_create": core.SEAdmin, "schedule_delete": core.SEAdmin} {
		m := h.tool(name).Spec().Manifest
		if m.SideEffect != se || !slices.Equal(m.Triggers, []core.Trigger{core.TrigMessage}) || m.Core || m.SpaceOK {
			t.Errorf("%s manifest = %+v", name, m)
		}
	}
}

func TestScheduleToolsConformance(t *testing.T) {
	h := newHarness(t)
	sc := h.upsert(&core.Schedule{Name: "Digest", Kind: core.SchedCron, Spec: "0 8 * * *", Prompt: "p", Enabled: true})
	env, here := h.env(nil), h.teamEnv(&h.dm)
	conformance.Tool(t, h.tool("schedule_list"), []conformance.ToolCase{
		{Name: "empty object", Env: env, Args: json.RawMessage(`{}`)},
		{Name: "extra field", Env: env, Args: json.RawMessage(`{"all":true}`), WantErr: true},
	})
	future := time.Now().Add(72 * time.Hour).UTC().Format("2006-01-02T15:04")
	conformance.Tool(t, h.tool("schedule_create"), []conformance.ToolCase{
		{Name: "cron", Env: env, Args: json.RawMessage(`{"name":"Brief","kind":"cron","spec":"0 8 * * 1-5","prompt":"Summarize my inbox"}`)},
		{Name: "once here", Env: here, Args: json.RawMessage(`{"name":"Call","kind":"once","spec":"` + future + `","tz":"UTC","prompt":"Remind me to call","deliver":"here"}`)},
		{Name: "web", Env: env, Args: json.RawMessage(`{"name":"W","kind":"cron","spec":"*/30 * * * *","prompt":"x","deliver":"web"}`)},
		{Name: "past", Env: env, Args: json.RawMessage(`{"name":"Old","kind":"once","spec":"2020-01-01T00:00","prompt":"x"}`), WantErr: true},
		{Name: "bad cron", Env: env, Args: json.RawMessage(`{"name":"B","kind":"cron","spec":"every day","prompt":"x"}`), WantErr: true},
		{Name: "bad tz", Env: env, Args: json.RawMessage(`{"name":"B","kind":"cron","spec":"0 8 * * *","tz":"Nowhere/Land","prompt":"x"}`), WantErr: true},
		{Name: "here without chat", Env: env, Args: json.RawMessage(`{"name":"B","kind":"cron","spec":"0 8 * * *","prompt":"x","deliver":"here"}`), WantErr: true},
		{Name: "heartbeat kind", Env: env, Args: json.RawMessage(`{"name":"B","kind":"heartbeat","spec":"30m","prompt":"x"}`), WantErr: true},
	})
	conformance.Tool(t, h.tool("schedule_delete"), []conformance.ToolCase{
		{Name: "existing", Env: env, Args: json.RawMessage(`{"id":"` + string(sc.ID) + `"}`)},
		{Name: "unknown", Env: env, Args: json.RawMessage(`{"id":"sch_00000000000000000000000000"}`), WantErr: true},
		{Name: "not an id", Env: env, Args: json.RawMessage(`{"id":"../x"}`), WantErr: true},
	})
}

func run(t *testing.T, tl core.Tool, env *core.ToolEnv, args string) (*core.Action, *core.ToolResult) {
	t.Helper()
	a, err := tl.Plan(context.Background(), env, json.RawMessage(args))
	if err != nil {
		t.Fatalf("Plan(%s): %v", args, err)
	}
	res, err := tl.Exec(context.Background(), env, a)
	if err != nil {
		t.Fatalf("Exec(%s): %v", args, err)
	}
	return a, res
}

func TestScheduleToolsExec(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	env := h.teamEnv(&h.dm)
	_, res := run(t, h.tool("schedule_list"), env, `{}`)
	if res.Content != "No schedules." {
		t.Fatalf("empty list = %q", res.Content)
	}

	a, res := run(t, h.tool("schedule_create"), env, `{"name":"Brief","kind":"cron","spec":"0  8 * * 1-5","prompt":"Summarize my inbox","deliver":"here"}`)
	var payload createPayload
	if err := json.Unmarshal(a.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if a.Kind != core.ActSchedule || a.SideEffect != core.SEAdmin || payload.Spec != "0 8 * * 1-5" || payload.TZ != "Europe/London" ||
		payload.When != "Weekdays at 08:00 (Europe/London)" || payload.Deliver == nil || *payload.Deliver != h.dm {
		t.Fatalf("create action = %+v %s", a, a.Payload)
	}
	if res.IsError || !strings.HasPrefix(res.Content, "Created schedule sch_") || !strings.Contains(res.Content, "Weekdays at 08:00") {
		t.Fatalf("create result = %+v", res)
	}
	all, _ := h.store.Schedules().List(ctx, core.Viewer{Principals: []core.PrincipalID{h.tp}})
	if len(all) != 1 || all[0].CreatedBy != "run:"+string(env.Run) || !all[0].Enabled || all[0].Deliver == nil {
		t.Fatalf("created = %+v", all)
	}
	audit := h.audit.All()
	if len(audit) != 1 || audit[0].Actor != "agent:"+string(env.Run) || audit[0].Run != env.Run {
		t.Fatalf("agent-created schedule audit = %+v", audit)
	}

	_, res = run(t, h.tool("schedule_list"), env, `{}`)
	if !strings.Contains(res.Content, "“Brief” · Weekdays at 08:00 (Europe/London) · on · next ") {
		t.Fatalf("list = %q", res.Content)
	}

	del, res := run(t, h.tool("schedule_delete"), env, `{"id":"`+string(all[0].ID)+`"}`)
	if string(del.Payload) != `{"id":"`+string(all[0].ID)+`","name":"Brief"}` || res.Content != "Deleted schedule "+string(all[0].ID)+" “Brief”." {
		t.Fatalf("delete = %s %+v", del.Payload, res)
	}
	// Approved after the schedule was already gone: reported, not crashed.
	res, err := h.tool("schedule_delete").Exec(ctx, env, del)
	if err != nil || !res.IsError {
		t.Fatalf("delete of a gone schedule = %+v, %v", res, err)
	}

	// The user's own time zone is the default.
	usr, _ := h.store.Users().Get(ctx, h.owner)
	usr.Timezone = "America/New_York"
	if err := h.store.Users().Update(ctx, usr); err != nil {
		t.Fatal(err)
	}
	a, _ = run(t, h.tool("schedule_create"), h.env(nil), `{"name":"NY","kind":"cron","spec":"0 9 * * *","prompt":"x"}`)
	if !strings.Contains(string(a.Payload), `"tz":"America/New_York"`) || strings.Contains(string(a.Payload), `"deliver"`) {
		t.Fatalf("default tz/deliver payload = %s", a.Payload)
	}
	webEnv := h.env(&core.ChatRef{Channel: "web", ChatID: "x", Kind: core.ChatWeb})
	a, _ = run(t, h.tool("schedule_create"), webEnv, `{"name":"W","kind":"cron","spec":"0 9 * * *","prompt":"x","deliver":"here"}`)
	if !strings.Contains(string(a.Payload), `"deliver":{"channel":"web"`) {
		t.Fatalf("deliver=here from the web UI = %s", a.Payload)
	}
}

// TestScheduleCreateSaysWhereResultsGo: the card and the tool result say
// where runs will post, so neither promises a chat the results won't reach.
func TestScheduleCreateSaysWhereResultsGo(t *testing.T) {
	h := newHarness(t)
	create := func(env *core.ToolEnv, deliver string) (createPayload, string) {
		t.Helper()
		args := `{"name":"Digest","kind":"cron","spec":"0 9 * * 1","prompt":"Summarise the open pull requests"` + deliver + `}`
		a, res := run(t, h.tool("schedule_create"), env, args)
		var p createPayload
		if err := json.Unmarshal(a.Payload, &p); err != nil {
			t.Fatal(err)
		}
		return p, res.Content
	}
	team := h.teamEnv(&h.dm)
	p, res := create(team, "")
	if p.Deliver != nil || p.Owner != "Tom" || p.Usual != "telegram" || p.OwnerKind != "person" ||
		!strings.HasSuffix(res, " Results go to Tom's messages on Telegram.") {
		t.Fatalf("a teammate's preferred delivery = %+v, %q", p, res)
	}
	p, res = create(team, `,"deliver":"here"`)
	if p.Owner != "" || p.Usual != "" || !strings.HasSuffix(res, " Results go to Tom's messages on Telegram.") {
		t.Fatalf("a teammate's delivery here = %+v, %q", p, res)
	}
	if _, err := h.tool("schedule_create").Plan(context.Background(), team,
		json.RawMessage(`{"name":"W","kind":"cron","spec":"0 9 * * 1","prompt":"x","deliver":"web"}`)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("a teammate's web delivery: %v", err)
	}

	user := h.env(nil)
	p, res = create(user, "")
	if p.Owner != "owner" || p.Usual != "" || p.OwnerKind != "user" || !strings.HasSuffix(res, " Results stay in Bothy on the web.") {
		t.Fatalf("an account's preferred delivery = %+v, %q", p, res)
	}
	p, res = create(user, `,"deliver":"web"`)
	if p.Owner != "" || !strings.HasSuffix(res, " Results stay in Bothy on the web.") {
		t.Fatalf("an account's web delivery = %+v, %q", p, res)
	}
}
