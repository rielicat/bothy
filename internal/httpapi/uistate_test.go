package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func TestProfileRoutes(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()

	p := decodeAs[dto.Profile](t, h.do(http.MethodGet, "/api/me", nil, h.member))
	if p.User != h.member.user || p.Username != "member" || p.DisplayName != "member" {
		t.Fatalf("profile = %+v", p)
	}
	if body := h.do(http.MethodGet, "/api/me", nil, h.member).Body.String(); strings.Contains(body, "approvals_in") || strings.Contains(body, "chats") {
		t.Fatalf("profile still has chat fields: %s", body)
	}
	// A read token sees its owner's profile; no token may change it.
	if got := decodeAs[dto.Profile](t, h.do(http.MethodGet, "/api/me", nil, h.tokenFor(h.member, "read"))); got.User != h.member.user {
		t.Fatalf("token profile = %+v", got)
	}
	for _, scopes := range [][]string{{"chat"}, {"chat", "read", "llm"}} {
		expect(t, h.do(http.MethodPatch, "/api/me", dto.ProfilePatch{DisplayName: new("Mallory")}, h.tokenFor(h.member, scopes...)), http.StatusForbidden)
	}

	// Members rename themselves; a field the profile doesn't have, such as
	// someone's id or the old approvals_in, is refused.
	expect(t, h.do(http.MethodPatch, "/api/me", map[string]any{"display_name": "Mallory", "user_id": h.admin.user}, h.member), http.StatusBadRequest)
	expect(t, h.do(http.MethodPatch, "/api/me", map[string]any{"approvals_in": "web"}, h.member), http.StatusBadRequest)
	rec := h.do(http.MethodPatch, "/api/me", map[string]any{"display_name": "  Maya Chen "}, h.member)
	expect(t, rec, http.StatusOK)
	if got := decodeAs[dto.Profile](t, rec); got.DisplayName != "Maya Chen" || got.User != h.member.user {
		t.Fatalf("renamed = %+v", got)
	}
	if u, _ := h.store.Users().Get(ctx, h.admin.user); u.DisplayName == "Maya Chen" {
		t.Fatal("the body's user_id renamed someone else")
	}
	if pr, err := h.store.Principals().Get(ctx, core.SystemViewer(), core.UserPrincipal(h.member.user)); err != nil || pr.DisplayName != "Maya Chen" {
		t.Fatalf("user principal = %+v %v; the agent would keep the old name", pr, err)
	}
	if me := decodeAs[dto.Me](t, h.do(http.MethodGet, "/api/auth/me", nil, h.member)); me.User.DisplayName != "Maya Chen" {
		t.Fatalf("me = %+v", me.User)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 65), "a\x07b"} {
		expect(t, h.do(http.MethodPatch, "/api/me", dto.ProfilePatch{DisplayName: &bad}, h.member), http.StatusBadRequest)
	}

	if !strings.Contains(strings.Join(h.audit.Actions(), " "), "user.profile") {
		t.Fatalf("audit = %v", h.audit.Actions())
	}

}

// People see and change their own time zone; their schedule previews read
// it, while the daily limits keep the configured one.
func TestProfileTimezone(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Timezone = "Europe/London" }).setup()

	if p := decodeAs[dto.Profile](t, h.do(http.MethodGet, "/api/me", nil, h.member)); p.Timezone != "Europe/London" {
		t.Fatalf("new member's zone = %q; want the configured one", p.Timezone)
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus", "Europe/London\x00"} {
		expect(t, h.do(http.MethodPatch, "/api/me", dto.ProfilePatch{Timezone: &bad}, h.member), http.StatusBadRequest)
	}
	expect(t, h.do(http.MethodPatch, "/api/me", dto.ProfilePatch{Timezone: new("Asia/Tokyo")}, h.tokenFor(h.member, "chat", "read")),
		http.StatusForbidden)
	if p := decodeAs[dto.Profile](t, h.do(http.MethodPatch, "/api/me", dto.ProfilePatch{Timezone: new("Asia/Tokyo")}, h.member)); p.Timezone != "Asia/Tokyo" {
		t.Fatalf("patched = %+v", p)
	}
	if me := decodeAs[dto.Me](t, h.do(http.MethodGet, "/api/auth/me", nil, h.member)); me.User.Timezone != "Asia/Tokyo" {
		t.Fatalf("me = %+v", me.User)
	}
	prev := decodeAs[dto.SchedulePreviewResp](t, h.do(http.MethodPost, "/api/schedules/preview",
		dto.SchedulePreviewReq{Kind: core.SchedHeartbeat, Spec: "30m"}, h.member))
	if prev.Human != "heartbeat 30m (Asia/Tokyo)" {
		t.Fatalf("preview without a zone = %q; want the member's own", prev.Human)
	}
	if today := decodeAs[dto.UsageToday](t, h.do(http.MethodGet, "/api/usage/today", nil, h.member)); today.Timezone != "Europe/London" {
		t.Fatalf("today = %+v; daily limits reset on the configured zone", today)
	}

	// Only the owner's change can move the check-in.
	if h.scheduler.Resyncs != 0 {
		t.Fatalf("a member's change resynced the check-in %d times", h.scheduler.Resyncs)
	}
	expect(t, h.do(http.MethodPatch, "/api/me", dto.ProfilePatch{Timezone: new("America/Santiago")}, h.owner), http.StatusOK)
	if h.scheduler.Resyncs != 1 {
		t.Fatalf("owner's change resynced the check-in %d times", h.scheduler.Resyncs)
	}
}

func mustActor(t *testing.T, h *harness, c *client) core.Actor {
	t.Helper()
	a, err := h.auth.FromCookie(context.Background(), c.cookie)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLastLoginAt(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	if _, err := h.auth.CreateUser(ctx, mustActor(t, h, h.owner), "newbie", testPassword, "", core.RoleMember); err != nil {
		t.Fatal(err)
	}
	users := decodeAs[[]core.User](t, h.do(http.MethodGet, "/api/users", nil, h.admin))
	seen := map[string]bool{}
	for _, u := range users {
		seen[u.Username] = !u.LastLoginAt.IsZero()
	}
	if !seen["owner"] || !seen["member"] || seen["newbie"] {
		t.Fatalf("signed in = %v; want owner and member, not newbie", seen)
	}
	if me := decodeAs[dto.Me](t, h.do(http.MethodGet, "/api/auth/me", nil, h.member)); me.User.LastLoginAt.IsZero() {
		t.Fatal("me has no last sign-in")
	}
}

func TestPausedByOnMe(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	u, _ := h.store.Users().Get(ctx, h.admin.user)
	u.DisplayName = "Sam Patel"
	if err := h.store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	expect(t, h.do(http.MethodPost, "/api/admin/pause", dto.PauseReq{Paused: true}, h.admin), http.StatusNoContent)
	me := decodeAs[dto.Me](t, h.do(http.MethodGet, "/api/auth/me", nil, h.member))
	if !me.Paused || me.PausedBy != string(h.admin.user) || me.PausedByName != "Sam Patel" || me.PausedAt.IsZero() {
		t.Fatalf("member me = %+v", me)
	}
	bs := h.bus.Banners(core.Filter{Admin: true})
	if len(bs) != 1 {
		t.Fatalf("banners = %+v", bs)
	}
	if b := bs[0].Data.(core.BannerData); b.Code != "paused" || b.By != string(h.admin.user) || b.ByName != "Sam Patel" || b.At.IsZero() {
		t.Fatalf("banner = %+v", b)
	}
	// Pausing again keeps who paused first.
	expect(t, h.do(http.MethodPost, "/api/admin/pause", dto.PauseReq{Paused: true}, h.owner), http.StatusNoContent)
	if me := decodeAs[dto.Me](t, h.do(http.MethodGet, "/api/auth/me", nil, h.member)); me.PausedBy != string(h.admin.user) {
		t.Fatalf("paused again by %s", me.PausedBy)
	}
	expect(t, h.do(http.MethodPost, "/api/admin/pause", dto.PauseReq{Paused: false}, h.owner), http.StatusNoContent)
	me = decodeAs[dto.Me](t, h.do(http.MethodGet, "/api/auth/me", nil, h.member))
	if me.Paused || me.PausedBy != "" || !me.PausedAt.IsZero() || me.PausedByName != "" {
		t.Fatalf("resumed me = %+v", me)
	}
}

// healthLLM reports fixed provider rows.
type healthLLM struct {
	*fakes.LLM
	rows []core.ProviderHealth
}

func (l healthLLM) Health() []core.ProviderHealth {
	return append([]core.ProviderHealth(nil), l.rows...)
}

// envVault lists one secret as set by the environment.
type envVault struct {
	*fakes.Vault
	env string
}

func (v envVault) List(ctx context.Context, sc core.Scope) ([]core.SecretMeta, error) {
	out, err := v.Vault.List(ctx, sc)
	return append(out, core.SecretMeta{Scope: sc, Name: v.env, Source: "env"}), err
}

func TestProviderCredentialSource(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Providers = []config.Provider{
			{ID: "anthropic", Type: "anthropic", APIKey: "secret:anthropic_api_key"},
			{ID: "openai", Type: "openai", APIKey: "secret:openai_key"},
			{ID: "groq", Type: "openai_compat", BaseURL: "https://api.groq.com/openai/v1", APIKey: "aws-sm:prod/groq#key"},
			{ID: "ollama", Type: "ollama", BaseURL: "http://127.0.0.1:11434"},
			{ID: "bedrock", Type: "bedrock", Region: "us-east-1"},
		}
	}).setup()
	if _, err := h.vault.Put(context.Background(), core.GlobalScope, "anthropic_api_key", []byte("sk-ant-secret-value"), h.owner.user); err != nil {
		t.Fatal(err)
	}
	h.srv.d.Vault = envVault{Vault: h.vault, env: "openai_key"}
	var rows []core.ProviderHealth
	for _, id := range []string{"anthropic", "openai", "groq", "ollama"} {
		rows = append(rows, core.ProviderHealth{ID: id, State: "ok"})
	}
	rows = append(rows, core.ProviderHealth{ID: "bedrock", Kind: core.KindBedrock, State: "ok", Source: "This server's AWS access (SSO profile demo)"})
	h.srv.d.LLM = healthLLM{LLM: h.llm, rows: rows}

	rec := h.do(http.MethodGet, "/api/providers", nil, h.admin)
	expect(t, rec, http.StatusOK)
	got := map[string]string{}
	for _, r := range decodeAs[[]core.ProviderHealth](t, rec) {
		got[r.ID] = r.Source
	}
	want := map[string]string{"anthropic": "Saved key", "openai": "Environment variable", "groq": "AWS Secrets Manager", "ollama": "",
		"bedrock": "This server's AWS access (SSO profile demo)"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s source = %q, want %q", id, got[id], w)
		}
	}
	if strings.Contains(rec.Body.String(), "sk-ant") || strings.Contains(rec.Body.String(), "prod/groq") {
		t.Fatalf("a credential or reference leaked: %s", rec.Body)
	}
	expect(t, h.do(http.MethodGet, "/api/providers", nil, h.member), http.StatusForbidden)
}

// searchEgress is the egress stand-in with a web search check.
type searchEgress struct {
	core.Egress
	mu    sync.Mutex
	calls []string
	err   error
}

func (e *searchEgress) TestSearch(_ context.Context, provider, url string, allowPrivate bool, key core.Secret) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, fmt.Sprintf("%s|%s|%v|%s", provider, url, allowPrivate, key.Reveal()))
	return e.err
}

type searchErr struct {
	code core.FailureCode
	msg  string
}

func (e searchErr) Error() string                 { return e.msg }
func (e searchErr) FailureCode() core.FailureCode { return e.code }

func TestSearchTestRoute(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Search.SearxngAllowPrivate = true }).setup()
	eg := &searchEgress{Egress: h.srv.d.Egress}
	h.srv.d.Egress = eg

	rec := h.do(http.MethodPost, "/api/search/test", dto.SearchTestReq{Provider: "searxng", SearxngURL: "http://searxng:8080"}, h.admin)
	expect(t, rec, http.StatusOK)
	if got := decodeAs[dto.SearchTestResp](t, rec); !got.OK || got.Code != "" {
		t.Fatalf("searxng = %+v", got)
	}
	key := "BSAkey-very-secret-1234"
	eg.err = searchErr{code: core.FailKeyRejected, msg: "brave rejected the request (401 Unauthorized) for " + key}
	rec = h.do(http.MethodPost, "/api/search/test", dto.SearchTestReq{Provider: "brave", APIKey: key}, h.owner)
	expect(t, rec, http.StatusOK)
	got := decodeAs[dto.SearchTestResp](t, rec)
	if got.OK || got.Code != core.FailKeyRejected || got.Error == "" || strings.Contains(rec.Body.String(), key) {
		t.Fatalf("brave = %+v (%s); the key must not come back", got, rec.Body)
	}
	eg.err = errors.New("tavily unreachable: dial tcp: connection refused")
	got = decodeAs[dto.SearchTestResp](t, h.do(http.MethodPost, "/api/search/test", dto.SearchTestReq{Provider: "tavily"}, h.admin))
	if got.OK || got.Code != core.FailUnreachable {
		t.Fatalf("tavily = %+v", got)
	}
	if want := []string{"searxng|http://searxng:8080|true|", "brave||true|" + key, "tavily||true|"}; strings.Join(eg.calls, ";") != strings.Join(want, ";") {
		t.Fatalf("calls = %q", eg.calls)
	}
	for _, bad := range []dto.SearchTestReq{{Provider: "google"}, {Provider: "searxng"}, {Provider: "searxng", SearxngURL: "file:///etc/passwd"},
		{Provider: "searxng", SearxngURL: "http://x", APIKey: "k"}, {Provider: "brave", SearxngURL: "http://x"}} {
		expect(t, h.do(http.MethodPost, "/api/search/test", bad, h.admin), http.StatusBadRequest)
	}
	expect(t, h.do(http.MethodPost, "/api/search/test", dto.SearchTestReq{Provider: "brave"}, h.member), http.StatusForbidden)
	expect(t, h.do(http.MethodPost, "/api/search/test", dto.SearchTestReq{Provider: "brave"}, h.tokenFor(h.owner, "chat")), http.StatusForbidden)
	if len(eg.calls) != 3 {
		t.Fatalf("refused requests reached the check: %q", eg.calls)
	}
}

func TestSkillFileRoute(t *testing.T) {
	h := newHarness(t).setup()
	h.skills.Metas = []core.SkillMeta{
		{Name: "release-notes", Status: core.SkillEnabled, Hash: "h1", ApprovedHash: "h1"},
		{Name: "drafty", Status: core.SkillPending, Hash: "h2"},
	}
	h.skills.Bodies = map[string]string{"release-notes": "Write notes.", "drafty": "New."}
	h.skills.Files = map[string]map[string][]byte{
		"release-notes": {"templates/notes.md": []byte("## {version}\n<script>alert(1)</script>\n"), "logo.png": {0x89, 'P', 'N', 'G', 0}},
		"drafty":        {"run.sh": []byte("echo hi\n")},
	}
	rec := h.do(http.MethodGet, "/api/skills/release-notes/files/templates/notes.md", nil, h.member)
	expect(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", rec.Header())
	}
	if !strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("body = %q", rec.Body)
	}
	expect(t, h.do(http.MethodGet, "/api/skills/release-notes/files/logo.png", nil, h.admin), http.StatusUnsupportedMediaType)
	expect(t, h.do(http.MethodGet, "/api/skills/release-notes/files/missing.md", nil, h.admin), http.StatusNotFound)
	// A skill nobody approved yet is for admins to review only.
	expect(t, h.do(http.MethodGet, "/api/skills/drafty/files/run.sh", nil, h.member), http.StatusNotFound)
	expect(t, h.do(http.MethodGet, "/api/skills/drafty/files/run.sh", nil, h.admin), http.StatusOK)
	expect(t, h.do(http.MethodGet, "/api/skills/nope/files/run.sh", nil, h.admin), http.StatusNotFound)
	expect(t, h.do(http.MethodGet, "/api/skills/release-notes/files/templates/notes.md", nil, nil), http.StatusUnauthorized)
}

// libSkills reads files like the skill library: a clean relative path or
// ErrInvalid.
type libSkills struct{ *fakes.Skills }

func (s libSkills) ReadFile(ctx context.Context, name, rel string) ([]byte, error) {
	if strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") {
		return nil, fmt.Errorf("path %q: %w", rel, core.ErrInvalid)
	}
	return s.Skills.ReadFile(ctx, name, rel)
}

func TestSkillFileRefusesEscapes(t *testing.T) {
	h := newHarness(t).setup()
	h.skills.Metas = []core.SkillMeta{{Name: "release-notes", Status: core.SkillEnabled, Hash: "h1", ApprovedHash: "h1"}}
	h.skills.Bodies = map[string]string{"release-notes": "x"}
	h.srv.d.Skills = libSkills{h.skills}
	for _, p := range []string{"..%2f..%2fbothy.yaml", "a/..%2f..%2fkeys/kek"} {
		if rec := h.do(http.MethodGet, "/api/skills/release-notes/files/"+p, nil, h.admin); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestInboxSkillChanges(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	for _, m := range []core.SkillMeta{
		{Name: "release-notes", Status: core.SkillChanged, Hash: "h2", ApprovedHash: "h1"},
		{Name: "brand-new", Status: core.SkillPending, Hash: "h3"},
		{Name: "old-approval", Status: core.SkillChanged, Hash: "h5", ApprovedHash: "h4"},
	} {
		h.skills.Metas = append(h.skills.Metas, m)
		if err := h.store.Skills().Upsert(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	h.skills.Bodies = map[string]string{"release-notes": "x", "brand-new": "y", "old-approval": "z"}
	h.skills.Files = map[string]map[string][]byte{
		"release-notes": {"SKILL.md": []byte("---\nname: release-notes\n---\nWrite notes.\n"), "notes.md": []byte("v2\n"), "new.sh": []byte("curl evil\n")},
		"brand-new":     {"SKILL.md": []byte("---\nname: brand-new\n---\nHello.\n")},
		"old-approval":  {"SKILL.md": []byte("changed\n")},
	}
	if err := h.store.Skills().SetApproved(ctx, "release-notes", []core.SkillFile{
		{Path: "SKILL.md", SHA256: sha256Hex([]byte("---\nname: release-notes\n---\nWrite notes.\n")), Text: true, Content: "---\nname: release-notes\n---\nWrite notes.\n"},
		{Path: "notes.md", SHA256: sha256Hex([]byte("v1\n")), Text: true, Content: "v1\n"},
		{Path: "gone.txt", SHA256: sha256Hex([]byte("bye\n")), Text: true, Content: "bye\n"},
	}); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodGet, "/api/inbox", nil, h.admin)
	expect(t, rec, http.StatusOK)
	byID := map[string]dto.InboxItem{}
	for _, it := range decodeAs[dto.Inbox](t, rec).Items {
		byID[it.ID] = it
	}
	c := byID["skill:release-notes"].Changes
	if c == nil || c.NoBaseline || c.Truncated {
		t.Fatalf("release-notes changes = %+v", c)
	}
	got := map[string]dto.SkillFileChange{}
	for _, f := range c.Files {
		got[f.Path] = f
	}
	if len(got) != 3 || got["notes.md"].Change != "changed" || got["notes.md"].Before != "v1\n" || got["notes.md"].After != "v2\n" ||
		got["new.sh"].Change != "added" || got["new.sh"].After != "curl evil\n" || got["gone.txt"].Change != "removed" || got["gone.txt"].Before != "bye\n" {
		t.Fatalf("files = %+v", c.Files)
	}
	if n := byID["skill:brand-new"].Changes; n == nil || len(n.Files) != 1 || n.Files[0].Change != "added" || n.Files[0].Path != "SKILL.md" {
		t.Fatalf("new skill = %+v", n)
	}
	if o := byID["skill:old-approval"].Changes; o == nil || !o.NoBaseline || len(o.Files) != 0 {
		t.Fatalf("approved before snapshots = %+v", o)
	}
	// Members never see skill reviews, let alone their files.
	if strings.Contains(h.do(http.MethodGet, "/api/inbox", nil, h.member).Body.String(), "curl evil") {
		t.Fatal("a member saw a skill's files")
	}
}

func (f *fakeKnowledge) NotesHistory(_ context.Context, v core.Viewer, id int64) ([]core.NotesVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(v, id); err != nil {
		return nil, err
	}
	return append([]core.NotesVersion(nil), f.history[id]...), nil
}

func (f *fakeKnowledge) NotesVersion(ctx context.Context, v core.Viewer, id, version int64) (*core.NotesVersion, error) {
	list, err := f.NotesHistory(ctx, v, id)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == version {
			return &list[i], nil
		}
	}
	return nil, core.ErrNotFound
}

func TestNotesHistoryRoutes(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	mine := k.add(core.UserPrincipal(h.member.user), "acme/web")
	theirs := k.add(core.UserPrincipal(h.admin.user), "acme/api")
	now := time.Now()
	chat := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: core.UserPrincipal(h.member.user), Origin: "web",
		Title: "Login bug", CreatedAt: now, UpdatedAt: now}
	if err := h.store.Sessions().Create(ctx, chat); err != nil {
		t.Fatal(err)
	}
	foreign := core.SessionID(core.NewID(core.PrefixSession))
	k.history = map[int64][]core.NotesVersion{
		mine.ID: {
			{ID: 3, Actor: core.NotesByUser, User: h.member.user, Before: "b", After: "c", At: now},
			{ID: 2, Actor: core.NotesByBothy, Session: chat.ID, Run: "run_01x", Before: "a", After: "b", At: now.Add(-time.Hour)},
			{ID: 1, Actor: core.NotesByBothy, Session: foreign, Run: "run_01y", Before: "", After: "a", At: now.Add(-2 * time.Hour)},
		},
		theirs.ID: {{ID: 1, Actor: core.NotesByUser, User: h.admin.user, Before: "", After: "secret plans", At: now}},
	}
	rec := h.do(http.MethodGet, fmt.Sprintf("/api/knowledge/repos/%d/notes/history", mine.ID), nil, h.member)
	expect(t, rec, http.StatusOK)
	items := decodeAs[dto.List[dto.NotesChange]](t, rec).Items
	if len(items) != 3 || items[0].Name != "member" || items[1].Chat == nil || items[1].Chat.Title != "Login bug" || items[1].Chat.ID != chat.ID {
		t.Fatalf("history = %+v", items)
	}
	if items[2].Chat != nil || items[2].Session != "" || items[2].Run != "" || strings.Contains(rec.Body.String(), string(foreign)) {
		t.Fatalf("a chat the caller cannot open is named: %+v", items[2])
	}
	one := decodeAs[dto.NotesChange](t, h.do(http.MethodGet, fmt.Sprintf("/api/knowledge/repos/%d/notes/history/2", mine.ID), nil, h.member))
	if one.ID != 2 || one.Before != "a" || one.After != "b" || one.Chat == nil {
		t.Fatalf("version 2 = %+v", one)
	}
	// IDOR: another person's repo, and ids that are not versions, are 404.
	for _, p := range []string{
		fmt.Sprintf("/api/knowledge/repos/%d/notes/history", theirs.ID),
		fmt.Sprintf("/api/knowledge/repos/%d/notes/history/1", theirs.ID),
		fmt.Sprintf("/api/knowledge/repos/%d/notes/history/9", mine.ID),
		fmt.Sprintf("/api/knowledge/repos/%d/notes/history/x", mine.ID),
		"/api/knowledge/repos/0/notes/history",
	} {
		rec := h.do(http.MethodGet, p, nil, h.member)
		expect(t, rec, http.StatusNotFound)
		if strings.Contains(rec.Body.String(), "secret plans") {
			t.Fatalf("%s leaked notes", p)
		}
	}
	// The owner of the other repo reads it with a read token.
	expect(t, h.do(http.MethodGet, fmt.Sprintf("/api/knowledge/repos/%d/notes/history", theirs.ID), nil, h.tokenFor(h.admin, "read")), http.StatusOK)
}

// headGitHub answers BranchHead and Workflows for allowlisted repositories.
type headGitHub struct {
	fakeGitHub
	heads map[string]string
	asked map[string]int
}

func (g *headGitHub) BranchHead(_ context.Context, repo string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.asked == nil {
		g.asked = map[string]int{}
	}
	g.asked[repo]++
	sha, ok := g.heads[repo]
	if !ok {
		return "", core.ErrNotFound
	}
	return sha, nil
}

func (g *headGitHub) Workflows(_ context.Context, repo string) ([]core.GitHubWorkflow, error) {
	if repo != "octocat/Hello-World" {
		return nil, fmt.Errorf("github: %s: %w", repo, core.ErrNotFound)
	}
	return []core.GitHubWorkflow{{ID: 1, Name: "CI", Path: ".github/workflows/ci.yml", State: "active"},
		{ID: 2, Name: "Deploy", Path: ".github/workflows/deploy.yml", State: "active"}}, nil
}

func TestKnowledgeStaleFlag(t *testing.T) {
	h := newHarness(t).setup()
	k := newFakeKnowledge()
	h.srv.d.Knowledge = k
	gh := &headGitHub{heads: map[string]string{"acme/web": strings.Repeat("b", 40), "acme/api": strings.Repeat("a", 40)}}
	h.srv.d.GitHub = gh
	p := core.UserPrincipal(h.member.user)
	web, api, local := k.add(p, "acme/web"), k.add(p, "acme/api"), k.add(p, "local/notes")
	for _, r := range []*core.IndexedRepo{web, api} {
		k.repos[r.ID].Source, k.repos[r.ID].IndexedSHA = core.KnowledgeGitHub, strings.Repeat("a", 40)
	}
	k.repos[local.ID].IndexedSHA = strings.Repeat("c", 40)
	list := decodeAs[dto.List[core.IndexedRepo]](t, h.do(http.MethodGet, "/api/knowledge/repos", nil, h.member)).Items
	stale := map[string]bool{}
	for _, r := range list {
		stale[r.Repo] = r.Stale
	}
	if !stale["acme/web"] || stale["acme/api"] || stale["local/notes"] {
		t.Fatalf("stale = %v", stale)
	}
	one := decodeAs[core.IndexedRepo](t, h.do(http.MethodGet, fmt.Sprintf("/api/knowledge/repos/%d", web.ID), nil, h.member))
	if !one.Stale {
		t.Fatalf("repo = %+v", one)
	}
	if gh.asked["acme/web"] != 1 || gh.asked["local/notes"] != 0 {
		t.Fatalf("GitHub asked %v; want once per repository while the answer is fresh", gh.asked)
	}
}

func TestGitHubWorkflowsRoute(t *testing.T) {
	h := newHarness(t).setup()
	expect(t, h.do(http.MethodGet, "/api/github/repos/octocat/Hello-World/workflows", nil, h.admin), http.StatusServiceUnavailable)
	h.srv.d.GitHub = &headGitHub{}
	rec := h.do(http.MethodGet, "/api/github/repos/octocat/Hello-World/workflows", nil, h.admin)
	expect(t, rec, http.StatusOK)
	if got := decodeAs[[]core.GitHubWorkflow](t, rec); len(got) != 2 || got[1].Path != ".github/workflows/deploy.yml" {
		t.Fatalf("workflows = %+v", got)
	}
	expect(t, h.do(http.MethodGet, "/api/github/repos/acme/private/workflows", nil, h.admin), http.StatusNotFound)
	expect(t, h.do(http.MethodGet, "/api/github/repos/octocat/Hello-World/workflows", nil, h.member), http.StatusForbidden)
}

func TestRunOriginAndReason(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	now := time.Now()
	mp := core.UserPrincipal(h.member.user)
	session := func(origin string) *core.Session {
		s := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: mp, Origin: origin, Title: origin + " chat", CreatedAt: now, UpdatedAt: now}
		if err := h.store.Sessions().Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	run := func(s *core.Session, trig core.Trigger, st core.RunStatus, errText string, by core.UserID, refined bool) core.RunID {
		r := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: s.ID, Principal: mp, User: h.member.user, Trigger: trig,
			Status: st, Error: errText, StoppedBy: by, Refined: refined, CreatedAt: now}
		if err := h.store.Runs().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	web, tg, api := session("web"), session("telegram"), session("api")
	rWeb := run(web, core.TrigMessage, core.RunDone, "", "", true)
	rTG := run(tg, core.TrigMessage, core.RunFailed, "model error: anthropic: 401 invalid x-api-key sk-ant-123", "", false)
	rAPI := run(api, core.TrigAPI, core.RunStopped, "", h.member.user, false)
	rSched := run(web, core.TrigSchedule, core.RunStopped, "", h.admin.user, false)
	rOdd := run(web, core.TrigMessage, core.RunFailed, "tool registry exploded", "", false)
	byID := map[core.RunID]dto.RunView{}
	rec := h.do(http.MethodGet, "/api/runs", nil, h.member)
	for _, r := range decodeAs[dto.List[dto.RunView]](t, rec).Items {
		byID[r.ID] = r
	}
	for id, want := range map[core.RunID][2]string{
		rWeb:   {"web", ""},
		rTG:    {"telegram", "the AI model service didn't accept Bothy's key"},
		rAPI:   {"api", "by you"},
		rSched: {"schedule", "by admin"},
		rOdd:   {"web", ""},
	} {
		if got := [2]string{byID[id].Origin, byID[id].Reason}; got != want {
			t.Errorf("run %s origin, reason = %q, want %q", id, got, want)
		}
	}
	if !byID[rWeb].Refined || byID[rTG].Refined {
		t.Fatalf("refined = %v %v", byID[rWeb].Refined, byID[rTG].Refined)
	}
	if d := decodeAs[dto.RunDetail](t, h.do(http.MethodGet, "/api/runs/"+string(rTG), nil, h.member)); d.Run.Origin != "telegram" || d.Run.Reason == "" {
		t.Fatalf("detail = %+v", d.Run)
	}
}

func TestDeployViews(t *testing.T) {
	h := releaseHarness(t)
	ctx := context.Background()
	u, _ := h.store.Users().Get(ctx, h.member.user)
	u.DisplayName = "Maya Chen"
	if err := h.store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	deploys := h.store.(core.ReleaseStore).Releases()
	d := &core.Deploy{Environment: "staging", Repo: "acme/web", Workflow: "deploy.yml", Ref: "main", Principal: core.UserPrincipal(h.member.user),
		Session: core.SessionID(core.NewID(core.PrefixSession)), RequestedBy: h.member.user, ApprovedBy: []core.UserID{h.admin.user, h.owner.user},
		Status: core.DeploySucceeded, SHA: "4f2c1a9e8b7d6c5f4e3d2c1b0a9f8e7d6c5b4a39", Subject: "Fix <b>login</b> redirect loop",
		CreatedAt: time.Now().Add(-time.Minute)}
	if err := deploys.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	other := &core.Deploy{Environment: "staging", Repo: "acme/web", Workflow: "deploy.yml", Ref: "main", Principal: core.UserPrincipal(h.admin.user),
		Session: core.SessionID(core.NewID(core.PrefixSession)), RequestedBy: h.admin.user, Status: core.DeployFailed, CreatedAt: time.Now()}
	if err := deploys.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	r := decodeAs[dto.Releases](t, h.do(http.MethodGet, "/api/releases", nil, h.member))
	if len(r.Deploys) != 1 {
		t.Fatalf("member deploys = %+v; want only their own", r.Deploys)
	}
	v := r.Deploys[0]
	if v.RequestedByName != "Maya Chen" || strings.Join(v.ApprovedByNames, ",") != "admin,owner" || v.ShortSHA != "4f2c1a9" ||
		v.Subject != "Fix <b>login</b> redirect loop" {
		t.Fatalf("deploy view = %+v", v)
	}
	l := decodeAs[dto.List[dto.DeployView]](t, h.do(http.MethodGet, "/api/releases/deploys", nil, h.admin))
	if len(l.Items) != 2 || l.Items[1].RequestedByName != "Maya Chen" || l.Items[0].ShortSHA != "" || l.Items[0].ApprovedByNames == nil {
		t.Fatalf("admin deploys = %+v", l.Items)
	}
}
