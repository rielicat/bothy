package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/starters"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// chat stores a listed chat of c (one message, so the list shows it).
func (h *harness) chat(c *client, title string, at time.Time, mod ...func(*core.Session)) *core.Session {
	h.t.Helper()
	ctx := context.Background()
	s := &core.Session{Principal: core.UserPrincipal(c.user), Origin: "web", Title: title, CreatedAt: at, UpdatedAt: at, LastMessageAt: at}
	for _, m := range mod {
		m(s)
	}
	if err := h.store.Sessions().Create(ctx, s); err != nil {
		h.t.Fatal(err)
	}
	m := &core.Message{Session: s.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUser,
		Parts: []core.Part{{Type: core.PartText, Text: title}}, CreatedAt: at}
	if err := h.store.Messages().Append(ctx, m); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func continued(from *core.Session) func(*core.Session) {
	return func(s *core.Session) { s.ParentID, s.RootID = from.ID, cmp.Or(from.RootID, from.ID) }
}

func (h *harness) schedule(c *client, name string, kind core.ScheduleKind) *core.Schedule {
	h.t.Helper()
	sc := &core.Schedule{Principal: core.UserPrincipal(c.user), Name: name, Kind: kind, Spec: "0 9 * * 1", TZ: "UTC", Prompt: "x",
		Slot: core.SlotMain, MissedRun: "skip", CreatedBy: "user:" + string(c.user), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if kind == core.SchedHeartbeat {
		sc.Spec = "30m"
	}
	if err := h.store.Schedules().Upsert(context.Background(), sc); err != nil {
		h.t.Fatal(err)
	}
	return sc
}

func ids(ss []core.Session) []core.SessionID {
	out := []core.SessionID{}
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

// GET /api/sessions brings the chats a page's chats continued from when
// they aren't on it, never another account's or a deleted or hidden one,
// and nothing extra for a search.
func TestSessionListParents(t *testing.T) {
	h := newHarness(t).setup()
	t0 := time.Now().Add(-time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	root := h.chat(h.owner, "Q3 pricing notes", at(1))
	child := h.chat(h.owner, "Pricing for the EU", at(5), continued(root))
	grandchild := h.chat(h.owner, "EU VAT rules", at(6), continued(child))
	gone := h.chat(h.owner, "Old plan", at(2))
	afterGone := h.chat(h.owner, "Plan after the old one", at(7), continued(gone))
	if err := h.store.Sessions().Archive(context.Background(), core.SystemViewer(), gone.ID); err != nil {
		t.Fatal(err)
	}
	quiet := h.chat(h.owner, "Check-in · 12 Oct", at(3), func(s *core.Session) { s.Origin, s.Hidden = "schedule", true })
	fromQuiet := h.chat(h.owner, "About that deploy", at(8), continued(quiet))
	foreign := h.chat(h.member, "Member's chat", at(4))
	stolen := h.chat(h.owner, "Points at the member's chat", at(9), continued(foreign))

	page := decodeAs[dto.SessionPage](t, h.do("GET", "/api/sessions?limit=3", nil, h.owner))
	if want := []core.SessionID{stolen.ID, fromQuiet.ID, afterGone.ID}; !slices.Equal(ids(page.Items), want) {
		t.Fatalf("items = %v, want %v", ids(page.Items), want)
	}
	if len(page.Parents) != 0 || page.Next != ms(afterGone.LastMessageAt) {
		t.Fatalf("first page: parents %v (foreign, hidden and deleted chats must stay out), next %d", ids(page.Parents), page.Next)
	}
	page = decodeAs[dto.SessionPage](t, h.do("GET", "/api/sessions?limit=1&before="+itoa(ms(afterGone.LastMessageAt)), nil, h.owner))
	if !slices.Equal(ids(page.Items), []core.SessionID{grandchild.ID}) || !slices.Equal(ids(page.Parents), []core.SessionID{child.ID, root.ID}) {
		t.Fatalf("items %v parents %v; want the grandchild with its parent and root", ids(page.Items), ids(page.Parents))
	}
	if page.Next != ms(grandchild.LastMessageAt) {
		t.Fatalf("next = %d comes from the items only", page.Next)
	}
	if page.Parents[1].Title != "Q3 pricing notes" {
		t.Fatalf("parent titles = %+v", page.Parents)
	}
	page = decodeAs[dto.SessionPage](t, h.do("GET", "/api/sessions?limit=2&before="+itoa(ms(afterGone.LastMessageAt)), nil, h.owner))
	if !slices.Equal(ids(page.Items), []core.SessionID{grandchild.ID, child.ID}) || !slices.Equal(ids(page.Parents), []core.SessionID{root.ID}) {
		t.Fatalf("items %v parents %v; a parent on the page is not repeated", ids(page.Items), ids(page.Parents))
	}
	rec := h.do("GET", "/api/sessions?q=EU", nil, h.owner)
	if page := decodeAs[dto.SessionPage](t, rec); len(page.Items) != 2 || strings.Contains(rec.Body.String(), `"parents"`) {
		t.Fatalf("search = %s; want two items and no parents", rec.Body)
	}
	member := decodeAs[dto.SessionPage](t, h.do("GET", "/api/sessions", nil, h.member))
	if !slices.Equal(ids(member.Items), []core.SessionID{foreign.ID}) || len(member.Parents) != 0 {
		t.Fatalf("member's list = %+v", member)
	}

	// A viewer of two principals still never gets a head across them.
	ctx := context.Background()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, Title: "#eng",
		Enabled: true, CreatedAt: t0, UpdatedAt: t0}
	if err := h.store.Spaces().Create(ctx, sp, &core.Principal{CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	group := &core.Session{Principal: sp.Principal, Origin: "slack", Title: "#eng chat", CreatedAt: t0, UpdatedAt: t0, LastMessageAt: t0}
	if err := h.store.Sessions().Create(ctx, group); err != nil {
		t.Fatal(err)
	}
	cross := h.chat(h.owner, "From the channel", at(10), continued(group))
	v := core.Viewer{User: h.owner.user, Principals: []core.PrincipalID{core.UserPrincipal(h.owner.user), sp.Principal}}
	heads, err := h.srv.familyHeads(ctx, v, h.srv.namer(ctx, v), []*core.Session{cross, child})
	if err != nil || !slices.Equal(ids(heads), []core.SessionID{root.ID}) {
		t.Fatalf("heads = %v, %v; want the owner's root only", ids(heads), err)
	}
}

// GET /api/sessions/{id} says where a chat came from.
func TestSessionFrom(t *testing.T) {
	h := newHarness(t).setup()
	now := time.Now()
	parent := h.chat(h.owner, "Q3 pricing notes", now)
	deleted := h.chat(h.owner, "Old plan", now)
	if err := h.store.Sessions().Archive(context.Background(), core.SystemViewer(), deleted.ID); err != nil {
		t.Fatal(err)
	}
	foreign := h.chat(h.member, "Member's chat", now)
	digest := h.schedule(h.owner, "Weekly PR digest", core.SchedCron)
	checkin := h.schedule(h.owner, "heartbeat", core.SchedHeartbeat)
	others := h.schedule(h.member, "Member's digest", core.SchedCron)
	result := func(sc core.ScheduleID) func(*core.Session) {
		return func(s *core.Session) { s.Origin, s.ScheduleID = "schedule", sc }
	}
	for _, c := range []struct {
		name string
		s    *core.Session
		want *dto.SessionFrom
	}{
		{"continued from a chat", h.chat(h.owner, "a", now, continued(parent)), &dto.SessionFrom{Kind: "chat", ID: string(parent.ID), Title: "Q3 pricing notes"}},
		{"continued from a deleted chat", h.chat(h.owner, "b", now, continued(deleted)), &dto.SessionFrom{Kind: "chat"}},
		{"continued from another account's chat", h.chat(h.owner, "c", now, continued(foreign)), &dto.SessionFrom{Kind: "chat"}},
		{"from a schedule", h.chat(h.owner, "d", now, result(digest.ID)), &dto.SessionFrom{Kind: "schedule", ID: string(digest.ID), Title: "Weekly PR digest"}},
		{"from the check-in", h.chat(h.owner, "e", now, result(checkin.ID)), &dto.SessionFrom{Kind: "checkin", ID: string(checkin.ID), Title: "Check-in"}},
		{"from a deleted schedule", h.chat(h.owner, "f", now, result("sch_01m4wa97e4s0p3qgcng95bcyhf")), &dto.SessionFrom{Kind: "schedule"}},
		{"from another account's schedule", h.chat(h.owner, "g", now, result(others.ID)), &dto.SessionFrom{Kind: "schedule"}},
		{"from nowhere", h.chat(h.owner, "h", now), nil},
	} {
		rec := h.do("GET", "/api/sessions/"+string(c.s.ID), nil, h.owner)
		got := decodeAs[dto.SessionDetail](t, rec).From
		if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("%s: from = %+v, want %+v", c.name, got, c.want)
		}
		if c.want == nil && strings.Contains(rec.Body.String(), `"from"`) {
			t.Errorf("%s: from is not left out: %s", c.name, rec.Body)
		}
	}

	// A viewer of two principals still never reads one principal's chat or
	// schedule as the origin of another's chat.
	ctx := context.Background()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "slack", ChatID: "C1", Kind: core.ChatGroup}, Title: "#eng",
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, sp, &core.Principal{CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	group := &core.Session{Principal: sp.Principal, Origin: "slack", Title: "#eng chat", CreatedAt: now, UpdatedAt: now, LastMessageAt: now}
	if err := h.store.Sessions().Create(ctx, group); err != nil {
		t.Fatal(err)
	}
	teamSched := &core.Schedule{Principal: sp.Principal, Name: "Channel digest", Kind: core.SchedCron, Spec: "0 9 * * 1", TZ: "UTC",
		Prompt: "x", Slot: core.SlotMain, MissedRun: "skip", CreatedBy: "system", CreatedAt: now, UpdatedAt: now}
	if err := h.store.Schedules().Upsert(ctx, teamSched); err != nil {
		t.Fatal(err)
	}
	v := core.Viewer{User: h.owner.user, Principals: []core.PrincipalID{core.UserPrincipal(h.owner.user), sp.Principal}}
	n := h.srv.namer(ctx, v)
	for s, want := range map[*core.Session]dto.SessionFrom{
		h.chat(h.owner, "i", now, continued(group)):     {Kind: "chat"},
		h.chat(h.owner, "j", now, result(teamSched.ID)): {Kind: "schedule"},
	} {
		if got := h.srv.sessionFrom(ctx, v, n, s); got == nil || *got != want {
			t.Errorf("%s across principals: from = %+v, want %+v", s.Title, got, want)
		}
	}
}

// POST /api/sessions/{id}/fork makes a related chat of any chat the viewer
// may open, from the web or a token, at any time.
func TestForkAnyChat(t *testing.T) {
	h := newHarness(t).setup()
	root := h.chat(h.owner, "Q3 pricing notes", time.Now())
	rec := h.do("POST", "/api/sessions/"+string(root.ID)+"/fork", nil, h.owner)
	fork := decodeAs[core.Session](t, rec)
	if rec.Code != http.StatusOK || fork.ParentID != root.ID || fork.RootID != root.ID || fork.Title != "Q3 pricing notes (clean)" ||
		!strings.Contains(rec.Body.String(), `"parent_id":"`+string(root.ID)+`"`) {
		t.Fatalf("fork = %d %s", rec.Code, rec.Body)
	}
	rec = h.do("POST", "/api/sessions/"+string(fork.ID)+"/fork", nil, h.tokenFor(h.owner, "chat"))
	again := decodeAs[core.Session](t, rec)
	if rec.Code != http.StatusOK || again.ParentID != fork.ID || again.RootID != root.ID {
		t.Fatalf("fork of a fork with a token = %d %s", rec.Code, rec.Body)
	}
	expect(t, h.do("POST", "/api/sessions/"+string(root.ID)+"/fork", nil, h.member), http.StatusNotFound)
	expect(t, h.do("POST", "/api/sessions/"+string(root.ID)+"/fork", nil, h.tokenFor(h.member, "chat")), http.StatusNotFound)
}

// Renaming a chat keeps the name: a title Bothy gave is replaced by the
// first message's, a person's never is.
func TestPatchTitleClearsTitleAuto(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	s := h.chat(h.owner, "Q3 pricing notes (clean)", time.Now(), func(s *core.Session) { s.TitleAuto = true })
	model := "anthropic/claude-sonnet"
	expect(t, h.do("PATCH", "/api/sessions/"+string(s.ID), dto.SessionPatchReq{ModelOverride: &model}, h.owner), http.StatusOK)
	if got, _ := h.store.Sessions().Get(ctx, core.SystemViewer(), s.ID); !got.TitleAuto {
		t.Fatal("a change other than the title cleared title_auto")
	}
	title := "EU pricing"
	expect(t, h.do("PATCH", "/api/sessions/"+string(s.ID), dto.SessionPatchReq{Title: &title}, h.owner), http.StatusOK)
	if got, _ := h.store.Sessions().Get(ctx, core.SystemViewer(), s.ID); got.TitleAuto || got.Title != "EU pricing" {
		t.Fatalf("after a rename: title %q auto %v", got.Title, got.TitleAuto)
	}
}

func TestWelcomeChat(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	me := func(c *client) dto.Me { return decodeAs[dto.Me](t, h.do("GET", "/api/auth/me", nil, c)) }
	if !me(h.member).WelcomePending || me(h.tokenFor(h.member, "chat")).WelcomePending {
		t.Fatal("welcome_pending: want it for a signed-in web account, never for a token")
	}
	expect(t, h.do("POST", "/api/me/welcome", nil, h.tokenFor(h.member, "chat")), http.StatusForbidden)

	sub := h.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{core.UserPrincipal(h.member.user)},
		Types: []core.EventType{core.EvMessageCreated}}, 8)
	defer sub.Close()
	const racers = 8
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, racers)
	reqs := make([]*http.Request, racers)
	for i := range racers {
		reqs[i] = h.request("POST", "/api/me/welcome", nil, h.member)
	}
	for i := range racers {
		wg.Go(func() {
			recs[i] = httptest.NewRecorder()
			h.h.ServeHTTP(recs[i], reqs[i])
		})
	}
	wg.Wait()
	var chat core.SessionID
	for i, rec := range recs {
		var got dto.WelcomeResp
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("welcome %d = %d %s", i, rec.Code, rec.Body)
		}
		if got.ChatID != "" {
			if chat != "" {
				t.Fatalf("two welcome chats: %s and %s", chat, got.ChatID)
			}
			chat = got.ChatID
		}
	}
	if chat == "" {
		t.Fatal("no welcome chat")
	}
	all, err := h.store.Sessions().List(ctx, core.SystemViewer(), core.SessionFilter{Principal: core.UserPrincipal(h.member.user), IncludeHidden: true})
	if err != nil || len(all) != 1 {
		t.Fatalf("member's chats = %d, %v", len(all), err)
	}
	s := all[0]
	if s.ID != chat || s.Origin != "web" || s.Title != "Welcome to Bothy" || !s.TitleAuto || s.LastMessageAt.IsZero() || s.ParentID != "" || s.Hidden {
		t.Fatalf("welcome chat = %+v", s)
	}
	msgs, err := h.store.Messages().Range(ctx, core.SystemViewer(), chat, 0, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	m := msgs[0]
	if m.Role != core.MsgAssistant || m.Kind != core.KindNotice || m.Provenance != core.ProvSystem || m.Sender != starters.Sender ||
		m.PlainText() != starters.Welcome("member", "Bothy", "") || !strings.HasPrefix(m.PlainText(), "Hi member, I'm Bothy.") {
		t.Fatalf("welcome message = %+v", m)
	}
	if n := len(sub.C()); n != 1 {
		t.Fatalf("message.created events = %d, want 1", n)
	}
	if runs := h.runner.Submitted(); len(runs) != 0 {
		t.Fatalf("the welcome chat started a task: %+v", runs)
	}
	if me(h.member).WelcomePending {
		t.Fatal("welcome_pending after the welcome chat")
	}
	if got := decodeAs[dto.WelcomeResp](t, h.do("POST", "/api/me/welcome", nil, h.member)); got.ChatID != "" {
		t.Fatalf("a second welcome made %s", got.ChatID)
	}

	// An account from before the welcome chat, or one that must change its
	// password first, isn't offered one.
	u, err := h.store.Users().Get(ctx, h.admin.user)
	if err != nil {
		t.Fatal(err)
	}
	if won, err := h.store.Users().ClaimWelcome(ctx, u.ID, time.UnixMilli(1)); err != nil || !won {
		t.Fatal(won, err)
	}
	if me(h.admin).WelcomePending {
		t.Fatal("welcome_pending for a backfilled account")
	}
	if got := decodeAs[dto.WelcomeResp](t, h.do("POST", "/api/me/welcome", nil, h.admin)); got.ChatID != "" {
		t.Fatalf("a backfilled account got a welcome chat %s", got.ChatID)
	}
	o, err := h.store.Users().Get(ctx, h.owner.user)
	if err != nil {
		t.Fatal(err)
	}
	o.MustChangePassword = true
	if err := h.store.Users().Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	rec := h.do("GET", "/api/auth/me", nil, h.owner)
	if rec.Code != http.StatusOK || decodeAs[dto.Me](t, rec).WelcomePending {
		t.Fatalf("me before the password is changed = %d %s; want no welcome_pending", rec.Code, rec.Body)
	}
	expect(t, h.do("POST", "/api/me/welcome", nil, h.owner), http.StatusForbidden)
}

// The welcome message greets people by name and names the repository
// Bothy reads for them.
func TestWelcomeNamesThePersonAndTheirRepository(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Agent.Name = "Ops" }).setup()
	ctx := context.Background()
	u, err := h.store.Users().Get(ctx, h.member.user)
	if err != nil {
		t.Fatal(err)
	}
	u.DisplayName = "Maya Chen"
	if err := h.store.Users().SetProfile(ctx, u); err != nil {
		t.Fatal(err)
	}
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	k.add(core.UserPrincipal(h.member.user), "acme/api")
	chat := decodeAs[dto.WelcomeResp](t, h.do("POST", "/api/me/welcome", nil, h.member)).ChatID
	s, err := h.store.Sessions().Get(ctx, core.SystemViewer(), chat)
	if err != nil || s.Title != "Welcome to Ops" {
		t.Fatalf("welcome chat = %+v, %v", s, err)
	}
	msgs, _ := h.store.Messages().Range(ctx, core.SystemViewer(), chat, 0, 0)
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0].PlainText(), "Hi Maya, I'm Ops. I work for your team: I can research questions, draft documents, read your code in acme/api and ") {
		t.Fatalf("welcome = %+v", msgs)
	}
}

func starterTexts(t *testing.T, h *harness, c *client) []dto.StarterRow {
	t.Helper()
	rec := h.do("GET", "/api/starters", nil, c)
	expect(t, rec, http.StatusOK)
	return decodeAs[[]dto.StarterRow](t, rec)
}

func TestStarters(t *testing.T) {
	h := newHarness(t).setup()
	texts := func(rows []dto.StarterRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Text)
		}
		return out
	}
	member := starterTexts(t, h, h.member)
	if want := []string{"Draft a release checklist for our next version", "Turn meeting notes into a list of tasks", starters.AskWhat}; !slices.Equal(texts(member), want) {
		t.Fatalf("member's starters = %q, want %q", texts(member), want)
	}
	if member[1].Fill != "Turn these meeting notes into a list of tasks, with an owner for each:\n\n" {
		t.Fatalf("fill = %q", member[1].Fill)
	}
	if owner := starterTexts(t, h, h.owner); owner[2].Text != "Connect GitHub so Bothy can read your code" || owner[2].Href != "/settings/connections/github" {
		t.Fatalf("owner's starters = %+v", owner)
	}

	h.srv.d.GitHub = &fakeGitHub{}
	h.cfg.update(func(c *config.Config) { c.Search.Provider = "brave" })
	if got := texts(starterTexts(t, h, h.owner)); !slices.Equal(got, []string{"Summarise the open pull requests in octocat/Hello-World",
		"What changed in octocat/Hello-World this week?", "Connect Slack, Discord or Telegram so your team can ask Bothy there", starters.AskWhat}) {
		t.Fatalf("owner with GitHub = %q", got)
	}
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	old := k.add(core.UserPrincipal(h.member.user), "acme/old")
	old.IndexedAt = time.Now().Add(-time.Hour)
	k.add(core.UserPrincipal(h.member.user), "acme/api").IndexedAt = time.Now()
	k.add(core.UserPrincipal(h.member.user), "acme/queued").Status = core.IndexQueued
	k.add(core.UserPrincipal(h.owner.user), "acme/owners")
	rows := starterTexts(t, h, h.member)
	if got := texts(rows); !slices.Equal(got, []string{"Summarise the open pull requests in acme/api", "What changed in acme/api this week?",
		"Have Bothy do something on a schedule", starters.AskWhat}) {
		t.Fatalf("member with an index = %q", got)
	}
	for _, c := range []*client{h.member, h.tokenFor(h.member, "chat")} {
		for _, r := range starterTexts(t, h, c) {
			if strings.HasPrefix(r.Href, "/settings") {
				t.Fatalf("a member got a settings link: %+v", r)
			}
		}
	}
	h.schedule(h.member, "Standup", core.SchedCron)
	if got := texts(starterTexts(t, h, h.member)); slices.Contains(got, "Have Bothy do something on a schedule") {
		t.Fatalf("a member with a schedule is still pointed at schedules: %q", got)
	}
}

func TestScheduleExamples(t *testing.T) {
	h := newHarness(t).setup()
	get := func(c *client) dto.ScheduleExamples {
		t.Helper()
		rec := h.do("GET", "/api/schedules/examples", nil, c)
		expect(t, rec, http.StatusOK)
		if strings.Contains(rec.Body.String(), "null") {
			t.Fatalf("examples has a null: %s", rec.Body)
		}
		return decodeAs[dto.ScheduleExamples](t, rec)
	}
	exIDs := func(x dto.ScheduleExamples) []string {
		var out []string
		for _, e := range x.Examples {
			out = append(out, e.ID)
		}
		return out
	}
	if x := get(h.member); len(x.Examples) != 0 || !x.Checkin.Empty || len(x.Checkin.Lines) != 0 {
		t.Fatalf("nothing set up = %+v", x)
	}
	h.cfg.update(func(c *config.Config) {
		c.Search.Provider = "brave"
		c.Releases.Environments = []config.ReleaseEnv{{Name: "production", Repo: "octocat/Hello-World"}}
	})
	h.srv.d.GitHub = &fakeGitHub{}
	x := get(h.member)
	if !slices.Equal(exIDs(x), []string{"pr-summary", "build-check", "news"}) {
		t.Fatalf("examples = %q", exIDs(x))
	}
	if e := x.Examples[0]; e.Kind != "cron" || e.Spec != "0 9 * * 1" || e.Slot != core.SlotFast || e.Needs != "github" ||
		!strings.Contains(e.Prompt, "octocat/Hello-World") || e.Name != "Monday pull request summary" {
		t.Fatalf("first example = %+v", e)
	}
	if want := []string{"Tell me if a deploy to production failed.",
		"Tell me about pull requests in octocat/Hello-World that have waited more than 2 days for a review.",
		"Tell me if a build on the default branch of octocat/Hello-World failed."}; !slices.Equal(x.Checkin.Lines, want) || !x.Checkin.Empty {
		t.Fatalf("check-in = %+v", x.Checkin)
	}
	h.memory.Files = map[core.PrincipalID]map[core.MemoryFile]string{core.UserPrincipal(h.member.user): {core.FileHeartbeat: "- Watch the deploys\n"}}
	if x := get(h.member); x.Checkin.Empty {
		t.Fatal("check-in list with a line reads as empty")
	}
	h.schedule(h.member, "MORNING build check", core.SchedCron)
	h.schedule(h.member, "heartbeat", core.SchedHeartbeat)
	if got := exIDs(get(h.member)); !slices.Equal(got, []string{"pr-summary", "news", "release-notes"}) {
		t.Fatalf("with a name in use = %q", got)
	}
	if got := exIDs(get(h.owner)); !slices.Equal(got, []string{"pr-summary", "build-check", "news"}) {
		t.Fatalf("another account's names don't count: %q", got)
	}
	h.schedule(h.member, "Standup", core.SchedCron)
	h.schedule(h.member, "Retro", core.SchedCron)
	if got := get(h.member); len(got.Examples) != 0 {
		t.Fatalf("three schedules of their own still get examples: %+v", got.Examples)
	}
	// The literal path beats {id}: a schedule can't be named "examples".
	expect(t, h.do("GET", "/api/schedules/sch_01m4wa97e4s0p3qgcng95bcyhf", nil, h.member), http.StatusNotFound)
}

// runStored is a scheduler whose Run now leaves a run row behind, as a
// skipped tick does.
type runStored struct {
	*fakes.Scheduler
	store   core.Store
	session core.SessionID
}

func (s runStored) RunNow(ctx context.Context, v core.Viewer, id core.ScheduleID, by core.UserID) (core.RunID, error) {
	if _, err := s.Scheduler.RunNow(ctx, v, id, by); err != nil {
		return "", err
	}
	sc, err := s.store.Schedules().Get(ctx, v, id)
	if err != nil {
		return "", err
	}
	run := &core.Run{Session: s.session, Principal: sc.Principal, User: by, Trigger: core.TrigSchedule, Schedule: id,
		Status: core.RunSkipped, CreatedAt: time.Now()}
	if err := s.store.Runs().Create(ctx, run); err != nil {
		return "", err
	}
	return run.ID, nil
}

// runInChat is a scheduler that says which chat Run now writes in.
type runInChat struct {
	*fakes.Scheduler
	session core.SessionID
}

func (s runInChat) RunNowIn(ctx context.Context, v core.Viewer, id core.ScheduleID, by core.UserID) (core.RunID, core.SessionID, error) {
	run, err := s.RunNow(ctx, v, id, by)
	return run, s.session, err
}

// Run now answers with the chat the run writes in, so the toast can open it.
func TestRunScheduleReturnsTheChat(t *testing.T) {
	h := newHarness(t).setup()
	sc := &core.Schedule{Principal: core.UserPrincipal(h.member.user), Name: "Digest", Kind: core.SchedCron, Spec: "0 9 * * 1", Prompt: "x"}
	if err := h.scheduler.Upsert(context.Background(), core.SystemViewer(), sc, "user:x"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Schedules().Upsert(context.Background(), &core.Schedule{ID: sc.ID, Principal: sc.Principal, Name: sc.Name,
		Kind: sc.Kind, Spec: sc.Spec, Prompt: sc.Prompt, MissedRun: "skip", Slot: core.SlotMain, CreatedBy: "user:x"}); err != nil {
		t.Fatal(err)
	}
	h.srv.d.Scheduler = runInChat{Scheduler: h.scheduler, session: "ses_01m4wa97e4s0p3qgcng95bcyhf"}
	rec := h.do("POST", "/api/schedules/"+string(sc.ID)+"/run", nil, h.member)
	expect(t, rec, http.StatusAccepted)
	if got := decodeAs[dto.RunScheduleResp](t, rec); got.RunID == "" || got.SessionID != "ses_01m4wa97e4s0p3qgcng95bcyhf" {
		t.Fatalf("run now = %+v", got)
	}
	skipped := h.chat(h.member, "Digest · 12 Oct", time.Now())
	h.srv.d.Scheduler = runStored{Scheduler: h.scheduler, store: h.store, session: skipped.ID}
	rec = h.do("POST", "/api/schedules/"+string(sc.ID)+"/run", nil, h.member)
	expect(t, rec, http.StatusAccepted)
	if got := decodeAs[dto.RunScheduleResp](t, rec); got.RunID == "" || got.SessionID != skipped.ID {
		t.Fatalf("run now with its run row = %+v, want %s", got, skipped.ID)
	}
	h.srv.d.Scheduler = h.scheduler
	rec = h.do("POST", "/api/schedules/"+string(sc.ID)+"/run", nil, h.member)
	expect(t, rec, http.StatusAccepted)
	if got := decodeAs[dto.RunScheduleResp](t, rec); got.RunID == "" || got.SessionID != "" || strings.Contains(rec.Body.String(), "session_id") {
		t.Fatalf("run now without a known chat = %s", rec.Body)
	}
	expect(t, h.do("POST", "/api/schedules/"+string(sc.ID)+"/run", nil, h.owner), http.StatusNotFound)
}
