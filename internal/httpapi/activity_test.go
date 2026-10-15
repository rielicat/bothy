package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func TestRunsAndUsageCarryNames(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	now := time.Now()
	mp := core.UserPrincipal(h.member.user)
	u, err := h.store.Users().Get(ctx, h.member.user)
	if err != nil {
		t.Fatal(err)
	}
	u.DisplayName = "Maya Chen"
	if err := h.store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	sp := &core.Space{Kind: "group", Chat: core.ChatRef{Channel: "telegram", ChatID: "-5", Kind: core.ChatGroup}, Title: "#eng",
		Owner: h.member.user, CreatedAt: now, UpdatedAt: now}
	pr := &core.Principal{Kind: core.PrincipalSpace, User: h.member.user, ToolProfile: "restricted", CreatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, pr); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "77", DisplayName: "Sam Patel",
		Status: core.IdentityPending, FirstSeen: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	cookie, ma, err := h.auth.Login(ctx, "member", testPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	member := &client{name: "member", user: h.member.user, cookie: cookie, csrf: ma.CSRF}

	session := func(p core.PrincipalID, title string) *core.Session {
		s := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: p, Origin: "web", Title: title, CreatedAt: now, UpdatedAt: now}
		if err := h.store.Sessions().Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	run := func(s *core.Session, trig core.Trigger, sch core.ScheduleID, at time.Time) *core.Run {
		r := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: s.ID, Principal: s.Principal, User: h.member.user,
			Trigger: trig, Schedule: sch, Status: core.RunDone, CreatedAt: at}
		if err := h.store.Runs().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Runs().AddLLMCall(ctx, r.ID, s.Principal, h.member.user, core.CallInfo{ID: core.NewID(core.PrefixLLMCall),
			Ref: core.ModelRef{Provider: "p", Model: "m"}, Purpose: "agent", Step: 1, Cost: 1000, PriceKnown: true}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	web := session(mp, "Fix login redirect")
	group := session(pr.ID, "")
	digest := &core.Schedule{ID: core.ScheduleID(core.NewID(core.PrefixSchedule)), Principal: mp, Name: "Weekly PR digest", Kind: core.SchedCron,
		Spec: "0 8 * * 1", TZ: "UTC", Prompt: "digest", Slot: core.SlotMain, MissedRun: "skip", Enabled: true, CreatedBy: "user:x",
		CreatedAt: now, UpdatedAt: now}
	checkIn := &core.Schedule{ID: core.ScheduleID(core.NewID(core.PrefixSchedule)), Principal: mp, Name: "heartbeat", Kind: core.SchedHeartbeat,
		Spec: "30m", TZ: "UTC", Prompt: "check", Slot: core.SlotFast, MissedRun: "skip", Enabled: true, CreatedBy: "system",
		CreatedAt: now, UpdatedAt: now}
	for _, sc := range []*core.Schedule{digest, checkIn} {
		if err := h.store.Schedules().Upsert(ctx, sc); err != nil {
			t.Fatal(err)
		}
	}
	rWeb := run(web, core.TrigMessage, "", now.Add(-4*time.Minute))
	rGroup := run(group, core.TrigMessage, "", now.Add(-3*time.Minute))
	rDigest := run(web, core.TrigSchedule, digest.ID, now.Add(-2*time.Minute))
	rCheck := run(web, core.TrigHeartbeat, checkIn.ID, now.Add(-time.Minute))
	m := &core.Message{Session: group.ID, Run: rGroup.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUntrusted,
		Sender: "telegram:77", Parts: []core.Part{{Type: core.PartText, Text: "@bothy what broke the build?"}}, CreatedAt: now}
	if err := h.store.Messages().Append(ctx, m); err != nil {
		t.Fatal(err)
	}

	runs := decodeAs[dto.List[dto.RunView]](t, h.do("GET", "/api/runs", nil, member))
	byID := map[core.RunID]dto.RunView{}
	for _, r := range runs.Items {
		byID[r.ID] = r
	}
	for id, want := range map[core.RunID][3]string{
		rWeb.ID:    {"Maya Chen", "Fix login redirect", ""},
		rGroup.ID:  {"Sam Patel", "@bothy what broke the build?", ""},
		rDigest.ID: {"", "Fix login redirect", "Weekly PR digest"},
		rCheck.ID:  {"", "Fix login redirect", "Check-in"},
	} {
		r := byID[id]
		if got := [3]string{r.RequestedByName, r.ChatTitle, r.ScheduleName}; got != want {
			t.Errorf("run %s names = %q, want %q", id, got, want)
		}
	}
	d := decodeAs[dto.RunDetail](t, h.do("GET", "/api/runs/"+string(rDigest.ID), nil, member))
	if d.Run.ScheduleName != "Weekly PR digest" || d.Run.ID != rDigest.ID {
		t.Fatalf("run detail = %+v", d.Run)
	}
	sr := decodeAs[dto.List[dto.RunView]](t, h.do("GET", "/api/schedules/"+string(digest.ID)+"/runs", nil, member))
	if len(sr.Items) != 1 || sr.Items[0].ScheduleName != "Weekly PR digest" {
		t.Fatalf("schedule runs = %+v", sr.Items)
	}

	names := func(c *client, group string) map[string]string {
		u := decodeAs[dto.UsageResp](t, h.do("GET", "/api/usage?group_by="+group, nil, c))
		out := map[string]string{}
		for _, row := range u.Rows {
			out[row.Key] = row.Name
		}
		return out
	}
	if n := names(member, "session"); n[string(web.ID)] != "Fix login redirect" || n[string(group.ID)] != "@bothy what broke the build?" {
		t.Fatalf("usage by chat = %v", n)
	}
	if n := names(member, "schedule"); n[string(digest.ID)] != "Weekly PR digest" || n[string(checkIn.ID)] != "Check-in" || n[""] != "" {
		t.Fatalf("usage by schedule = %v", n)
	}
	if n := names(h.admin, "user"); n[string(h.member.user)] != "Maya Chen" {
		t.Fatalf("usage by person = %v", n)
	}
	// A chat's title and a schedule's name are private to their owner: an
	// admin reading everyone's spending sees the amounts without them.
	if n := names(h.admin, "session"); n[string(web.ID)] != "" || n[string(group.ID)] != "" {
		t.Fatalf("an admin read the titles of someone else's chats: %v", n)
	}
	if n := names(h.admin, "schedule"); n[string(digest.ID)] != "" {
		t.Fatalf("an admin read the name of someone else's schedule: %v", n)
	}
}

func TestAuditRowsAndEgressOutcomes(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	now := time.Now()
	f := newTranscript(t, h, core.RunDone)
	f.sess.Title = "Secret merger notes"
	if err := h.store.Sessions().Update(ctx, f.sess); err != nil {
		t.Fatal(err)
	}
	sched := &core.Schedule{ID: core.ScheduleID(core.NewID(core.PrefixSchedule)), Principal: f.sess.Principal, Name: "Private digest",
		Kind: core.SchedCron, Spec: "0 8 * * 1", TZ: "UTC", Prompt: "digest", Slot: core.SlotMain, MissedRun: "skip", Enabled: true,
		CreatedBy: "user:x", CreatedAt: now, UpdatedAt: now}
	if err := h.store.Schedules().Upsert(ctx, sched); err != nil {
		t.Fatal(err)
	}
	ap := f.approval("notes/staging.md")
	f.decide(ap, core.ApprovalAllowed, core.AllowOnce, h.member.user)
	f.call("c1", "fs_delete", "notes/staging.md", core.TCOK, core.VerdictAsk, ap, &core.ToolResult{Content: "deleted"})
	recs, _ := h.store.Runs().ToolCalls(ctx, core.SystemViewer(), f.run.ID)
	fp := core.ActionFingerprint("fs_delete", recs[0].Action.Payload)
	recs[0].Action.Fingerprint = fp
	if err := h.store.Runs().UpdateToolCall(ctx, recs[0]); err != nil {
		t.Fatal(err)
	}
	memPayload := json.RawMessage(`{"file":"MEMORY","op":"remove","old":"Staging resets on Mondays"}`)
	memFP := core.ActionFingerprint("memory", memPayload)
	if err := h.store.Runs().AddToolCall(ctx, &core.ToolCallRecord{Run: f.run.ID, Session: f.sess.ID, Principal: f.run.Principal, Step: 9,
		CallID: "c9", Tool: "memory", Source: "builtin", SideEffect: core.SELocalWrite, Verdict: core.VerdictAuto, Status: core.TCOK, CreatedAt: now,
		Action: &core.Action{Tool: "memory", Kind: core.ActMemory, SideEffect: core.SELocalWrite, Payload: memPayload, Fingerprint: memFP}}); err != nil {
		t.Fatal(err)
	}
	agent := "agent:" + string(f.run.ID)
	detail := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	entries := []core.AuditEntry{
		{Actor: "user:" + string(h.member.user), Action: "auth.login", Target: "user:" + string(h.member.user), Outcome: "ok"},
		{Actor: agent, Action: "approval.request", Target: string(ap.ID), Outcome: "ok", Run: f.run.ID, Detail: detail(map[string]any{"tool": "fs_delete"})},
		{Actor: "user:" + string(h.member.user), Action: "approval.decide", Target: string(ap.ID), Outcome: "ok", Run: f.run.ID,
			Detail: detail(map[string]any{"choice": "allow_once", "via": "slack", "tool": "fs_delete"})},
		{Actor: agent, Action: "tool.exec", Target: "fs_delete", Outcome: "ok", Run: f.run.ID, Detail: detail(map[string]any{"tool": "fs_delete", "fingerprint": fp})},
		{Actor: agent, Action: "tool.exec", Target: "shell", Outcome: "error", Run: "run_gone", Detail: detail(map[string]any{"tool": "shell", "fingerprint": "x"})},
		{Actor: agent, Action: "tool.exec", Target: "memory", Outcome: "ok", Run: f.run.ID, Detail: detail(map[string]any{"tool": "memory", "fingerprint": memFP})},
		{Actor: agent, Action: "memory.write", Target: "memory:usr_a/MEMORY", Outcome: "ok", Run: f.run.ID, Detail: detail(map[string]any{"file": "MEMORY", "op": "add"})},
		{Actor: agent, Action: "memory.write", Target: "memory:usr_b/MEMORY", Outcome: "ok", Run: f.run.ID, Detail: detail(map[string]any{"file": "MEMORY", "op": "replace"})},
		{Actor: agent, Action: "memory.write", Target: "memory:usr_c/MEMORY", Outcome: "ok", Run: f.run.ID, Detail: detail(map[string]any{"file": "MEMORY", "op": "remove"})},
		{Actor: agent, Action: "egress.deny", Target: "paste.example.net:443", Outcome: "denied", Run: f.run.ID},
		{Actor: "system", Action: "schedule.update", Target: "schedule:sch_x", Outcome: "ok", Detail: detail(map[string]any{"name": "Daily standup", "reason": "consecutive_failures"})},
		{Actor: "system", Action: "knowledge.index", Target: "knowledge:usr_x/acme/web", Outcome: "error"},
		{Actor: "system", Action: "something.new", Outcome: "ok"},
		{Actor: "user:" + string(h.member.user), Action: "run.stop", Target: string(f.sess.ID), Outcome: "ok", Session: f.sess.ID},
		{Actor: "user:" + string(h.member.user), Action: "schedule.delete", Target: "schedule:" + string(sched.ID), Outcome: "ok"},
		{Actor: "user:" + string(h.admin.user), Action: "schedule.run", Target: "schedule:sch_y", Outcome: "ok", Run: f.run.ID,
			Detail: detail(map[string]any{"name": "Weekly PR digest"})},
		{Actor: "user:" + string(h.admin.user), Action: "auth.password_changed", Target: "user:" + string(h.member.user), Outcome: "ok",
			Detail: detail(map[string]any{"via": "admin", "one_time": true})},
		{Actor: "user:" + string(h.admin.user), Action: "provider.connect", Target: "provider:anthropic", Outcome: "ok",
			Detail: detail(map[string]any{"kind": "anthropic"})},
		{Actor: "user:" + string(h.member.user), Action: "approval.decide", Target: "apr_withdrawn", Outcome: "ok",
			Detail: detail(map[string]any{"choice": "deny", "tool": "fs_delete", "withdrawn": true})},
		{Actor: "system", Action: "release.finish", Target: "dep_lost", Outcome: "ok",
			Detail: detail(map[string]any{"environment": "production", "status": "unknown", "conclusion": ""})},
		{Actor: "system", Action: "release.finish", Target: "dep_failed", Outcome: "ok",
			Detail: detail(map[string]any{"environment": "production", "status": "failed", "conclusion": "failure"})},
		{Actor: "user:" + string(h.admin.user), Action: "mcp.install", Target: "mcp:deepwiki", Outcome: "ok"},
		{Actor: "user:" + string(h.admin.user), Action: "mcp.install", Target: "mcp:team-docs", Outcome: "error"},
		{Actor: "user:" + string(h.admin.user), Action: "mcp.approve", Target: "mcp:linear", Outcome: "ok"},
		{Actor: "system", Action: "mcp.changed", Target: "mcp:github", Outcome: "ok"},
		{Actor: "user:" + string(h.admin.user), Action: "skill.install", Target: "skill:pdf", Outcome: "ok"},
		{Actor: "system", Action: "skill.changed", Target: "skill:slides", Outcome: "ok"},
		{Actor: agent, Action: "github.checkout", Target: "acme/web", Outcome: "error"},
		{Actor: agent, Action: "tool.exec", Target: "tool_search", Outcome: "ok", Run: f.run.ID, Detail: detail(map[string]any{"tool": "tool_search"})},
		{Actor: "user:" + string(h.member.user), Action: "identity.unlink", Target: "channel:slack:U123", Outcome: "ok",
			Detail: detail(map[string]any{"user": string(h.member.user)})},
		{Actor: "user:" + string(h.member.user), Action: "user.profile", Target: "user:" + string(h.member.user), Outcome: "ok",
			Detail: detail(map[string]any{"timezone": "Europe/London"})},
	}
	for i := range entries {
		e := entries[i]
		e.At = now.Add(time.Duration(i) * time.Second)
		if len(e.Detail) == 0 {
			e.Detail = json.RawMessage("{}")
		}
		if _, err := h.store.Audit().Append(ctx, &e, make([]byte, 32), make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	rows := decodeAs[dto.List[dto.AuditRow]](t, h.do("GET", "/api/audit", nil, h.admin))
	got := map[string][2]string{}
	for _, r := range rows.Items {
		got[r.Action+"/"+r.Target] = [2]string{r.ActorName, r.Summary}
	}
	for k, want := range map[string][2]string{
		"auth.login/user:" + string(h.member.user):            {"member", "Signed in"},
		"approval.request/" + string(ap.ID):                   {"Bothy", "Asked to delete notes/staging.md"},
		"approval.decide/" + string(ap.ID):                    {"member", "Approved deleting notes/staging.md in Slack"},
		"tool.exec/fs_delete":                                 {"Bothy", "Deleted notes/staging.md"},
		"tool.exec/shell":                                     {"Bothy", "A command finished with errors"},
		"tool.exec/memory":                                    {"Bothy", "Forgot something"},
		"memory.write/memory:usr_a/MEMORY":                    {"Bothy", "Remembered something"},
		"memory.write/memory:usr_b/MEMORY":                    {"Bothy", "Updated a saved note"},
		"memory.write/memory:usr_c/MEMORY":                    {"Bothy", "Forgot something"},
		"egress.deny/paste.example.net:443":                   {"Bothy", "Blocked from reaching paste.example.net"},
		"schedule.update/schedule:sch_x":                      {"Bothy", "Turned off the schedule “Daily standup” after it failed several times"},
		"knowledge.index/knowledge:usr_x/acme/web":            {"Bothy", "Couldn't read acme/web"},
		"something.new/":                                      {"Bothy", "Something new"},
		"run.stop/" + string(f.sess.ID):                       {"member", "Stopped a task"},
		"schedule.delete/schedule:" + string(sched.ID):        {"member", "Deleted a schedule"},
		"schedule.run/schedule:sch_y":                         {"admin", "Started the schedule “Weekly PR digest” by hand"},
		"auth.password_changed/user:" + string(h.member.user): {"admin", "Gave member a one-time password"},
		"provider.connect/provider:anthropic":                 {"admin", "Connected an AI model"},
		"approval.decide/apr_withdrawn":                       {"member", "Cancelled the request to delete a file"},
		"release.finish/dep_lost":                             {"Bothy", "Bothy stopped following the deploy to production"},
		"release.finish/dep_failed":                           {"Bothy", "The deploy to production didn't finish"},
		"mcp.install/mcp:deepwiki":                            {"admin", "Installed DeepWiki"},
		"mcp.install/mcp:team-docs":                           {"admin", "Couldn't install Team docs"},
		"mcp.approve/mcp:linear":                              {"admin", "Approved Linear's actions"},
		"mcp.changed/mcp:github":                              {"Bothy", "GitHub changed its actions"},
		"skill.install/skill:pdf":                             {"admin", "Installed the skill “pdf”"},
		"skill.changed/skill:slides":                          {"Bothy", "The skill “slides” changed"},
		"github.checkout/acme/web":                            {"Bothy", "Couldn't get a copy of acme/web"},
		"tool.exec/tool_search":                               {"Bothy", "Searched for other things Bothy can do"},
		"identity.unlink/channel:slack:U123":                  {"member", "Unlinked member on Slack"},
		"user.profile/user:" + string(h.member.user):          {"member", "Changed their profile"},
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}

	if err := h.store.Egress().Add(ctx, []core.EgressRecord{
		{At: now, Dialer: "public", Principal: f.run.Principal, Session: f.sess.ID, Host: "api.github.com", Port: 443, Decision: "allow", Down: 10},
		{At: now, Dialer: "box", Principal: f.run.Principal, Session: f.sess.ID, Host: "api.example.com", Port: 443, Decision: "held_allow", Reason: "connect failed"},
		{At: now, Dialer: "box", Principal: f.run.Principal, Session: f.sess.ID, Host: "example.com", Port: 443, Decision: "deny", Reason: "not allowlisted"},
		{At: now, Dialer: "box", Principal: f.run.Principal, Session: f.sess.ID, Host: "held.example", Port: 443, Decision: "held_deny"},
	}); err != nil {
		t.Fatal(err)
	}
	eg := decodeAs[dto.List[dto.EgressRow]](t, h.do("GET", "/api/egress", nil, h.member))
	outcomes := map[string]dto.EgressOutcome{}
	for _, e := range eg.Items {
		outcomes[e.Host] = e.Outcome
	}
	want := map[string]dto.EgressOutcome{"api.github.com": dto.EgressReached, "api.example.com": dto.EgressAllowedUnreachable,
		"example.com": dto.EgressBlocked, "held.example": dto.EgressBlocked}
	for host, w := range want {
		if outcomes[host] != w {
			t.Errorf("egress %s = %q, want %q", host, outcomes[host], w)
		}
	}
	expect(t, h.do("GET", "/api/audit", nil, h.member), http.StatusForbidden)
}

func TestActionPhrases(t *testing.T) {
	for _, c := range []struct {
		tool string
		a    *core.Action
		want phrase
	}{
		{"fs_delete", &core.Action{Paths: []string{"notes/a.md"}}, phrase{"delete notes/a.md", "deleting notes/a.md", "Deleted notes/a.md", "Couldn't delete notes/a.md"}},
		{"fs_delete", nil, phrase{"delete a file", "deleting a file", "Deleted a file", "Couldn't delete a file"}},
		{"shell", &core.Action{Argv: []string{"bash", "-lc", "npm   test"}}, phrase{"run npm test", "running npm test", "Ran npm test", "npm test finished with errors"}},
		{"web_fetch", &core.Action{Host: "api.example.com", Method: "POST"}, phrase{"send data to api.example.com", "sending data to api.example.com", "Sent data to api.example.com", "Couldn't reach api.example.com"}},
		{"fs_write", &core.Action{Paths: []string{"a.md"}, Payload: json.RawMessage(`{"mode":"append"}`)}, phrase{"add to a.md", "adding to a.md", "Added to a.md", "Couldn't save a.md"}},
		{"release_deploy", &core.Action{Payload: json.RawMessage(`{"environment":"production","repo":"acme/web"}`)}, phrase{"deploy acme/web to production", "deploying acme/web to production", "Started deploying acme/web to production", "Deploy to production failed"}},
		{"memory", &core.Action{Payload: json.RawMessage(`{"file":"MEMORY","op":"add","new":"x"}`)}, phrase{"remember something", "remembering something", "Remembered something", "Couldn't save that"}},
		{"memory", &core.Action{Payload: json.RawMessage(`{"file":"MEMORY","op":"replace","old":"x","new":"y"}`)}, phrase{"update a saved note", "updating a saved note", "Updated a saved note", "Couldn't update that note"}},
		{"memory", &core.Action{Payload: json.RawMessage(`{"file":"USER","op":"remove","old":"x"}`)}, phrase{"forget something", "forgetting something", "Forgot something", "Couldn't remove that note"}},
		{"mcp__linear__create_issue", nil, phrase{"use Linear: Create issue", "using Linear: Create issue", "Used Linear: Create issue", "Linear: Create issue failed"}},
		{"mcp__deepwiki__read_wiki_structure", nil, phrase{"use DeepWiki: Read wiki structure", "using DeepWiki: Read wiki structure", "Used DeepWiki: Read wiki structure", "DeepWiki: Read wiki structure failed"}},
		{"tool_search", nil, phrase{"search for other things Bothy can do", "searching for other things Bothy can do", "Searched for other things Bothy can do", "Couldn't search for other things Bothy can do"}},
	} {
		if got := actionPhrase(c.tool, c.a); got != c.want {
			t.Errorf("%s = %+v, want %+v", c.tool, got, c.want)
		}
	}
}

func TestFailureReasonUnreadableKey(t *testing.T) {
	for msg, want := range map[string]string{
		"model error: credential unavailable: vault: AWS refused this server's identity access to aws-sm:x": "Bothy couldn't read its key for the AI model",
		"model error: credential unavailable: vault: Bothy could not reach AWS Secrets Manager: timeout":    "Bothy couldn't read its key for the AI model",
		"model error: openai: auth: 401 invalid api key":                                                    "the AI model service didn't accept Bothy's key",
	} {
		if got := failureReason(msg); got != want {
			t.Errorf("failureReason(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestActionPhraseListsPullRequests(t *testing.T) {
	list := actionPhrase("github_pr", &core.Action{Tool: "github_pr", Payload: json.RawMessage(`{"repo":"acme/web","state":"open"}`)})
	if list.done != "Listed pull requests in acme/web" {
		t.Errorf("list = %+v", list)
	}
	one := actionPhrase("github_pr", &core.Action{Tool: "github_pr", Payload: json.RawMessage(`{"repo":"acme/web","number":7}`)})
	if one.done != "Read pull request #7" {
		t.Errorf("one = %+v", one)
	}
}
