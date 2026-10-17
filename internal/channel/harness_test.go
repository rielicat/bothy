package channel

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
	"github.com/rielicat/bothy/internal/testkit/sandboxtest"
)

// cfgView is a config.View whose snapshot tests can replace, notifying
// watchers like config.Manager does.
type cfgView struct {
	mu       sync.Mutex
	c        *config.Config
	watchers map[int]func(old, cur *config.Config)
	next     int
}

func newCfgView(c *config.Config) *cfgView {
	return &cfgView{c: c, watchers: map[int]func(old, cur *config.Config){}}
}

func (v *cfgView) Current() *config.Config {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.c
}

func (v *cfgView) Watch(fn func(old, cur *config.Config)) func() {
	v.mu.Lock()
	defer v.mu.Unlock()
	id := v.next
	v.next++
	v.watchers[id] = fn
	return func() {
		v.mu.Lock()
		delete(v.watchers, id)
		v.mu.Unlock()
	}
}

// Set applies mut to a copy of the config and notifies watchers.
func (v *cfgView) Set(mut func(c *config.Config)) {
	v.mu.Lock()
	old := v.c
	cur := *old
	cur.Policy.Approval.Notify = slices.Clone(old.Policy.Approval.Notify)
	mut(&cur)
	v.c = &cur
	ws := make([]func(old, cur *config.Config), 0, len(v.watchers))
	for _, w := range v.watchers {
		ws = append(ws, w)
	}
	v.mu.Unlock()
	for _, w := range ws {
		w(old, &cur)
	}
}

type harness struct {
	t         *testing.T
	ctx       context.Context
	hub       *Hub
	ch        *channeltest.Channel
	mc        *channeltest.Checker // set by withChecker: the adapter can say who is in the team
	servers   []core.TeamInfo      // with a checker: the servers it lists, as Discord does
	app       string               // the adapter's name
	store     core.Store
	auth      *auth.Service
	runner    *fakes.Runner
	budget    *fakes.Budget
	approvals *fakes.Approvals
	audit     *fakes.Auditor
	redactor  *fakes.Redactor
	sandbox   *sandboxtest.Sandbox
	bus       *events.Bus
	cfg       *cfgView
	owner     *core.User
	member    *core.User
	work      string

	runner2 core.Runner                             // overrides runner in Deps when set
	llm     core.LLM                                // Deps.LLM; nil leaves models unnamed
	wrap    func(*channeltest.Channel) core.Channel // wraps the adapter the hub sees
	cancel  context.CancelFunc
	done    chan error
}

// ownerChat and memberChat are teammates on the harness's chat app, let in
// by an admin: Olivia can approve, Mark can't. Their DMs share their ids.
const (
	ownerChat  = "1001"
	memberChat = "2002"
)

// newHarness builds a hub over memstore, the real auth service and the
// testkit fakes, with a channeltest adapter named "telegram" (or a member
// checker, withChecker). It must run inside a synctest bubble.
func newHarness(t *testing.T, opts ...func(*harness)) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{
		t: t, ctx: ctx, store: memstore.New(), runner: &fakes.Runner{}, budget: &fakes.Budget{},
		approvals: &fakes.Approvals{Manual: true}, audit: &fakes.Auditor{}, redactor: fakes.NewRedactor(),
		sandbox: &sandboxtest.Sandbox{}, bus: events.New(events.Options{}), work: t.TempDir(), app: "telegram",
	}
	h.runner.Store = h.store
	h.approvals.Render = plainRender(h.approvals)
	h.owner = h.user("olivia", "Olivia", core.RoleOwner)
	h.member = h.user("mark", "Mark", core.RoleMember)
	c := config.Default()
	c.Channels.Telegram.Enabled = true
	h.cfg = newCfgView(c)
	h.ch = channeltest.New("telegram")
	for _, o := range opts {
		o(h)
	}
	a, err := auth.New(auth.Deps{Store: h.store, Config: h.cfg, Audit: h.audit, Bus: h.bus})
	if err != nil {
		t.Fatal(err)
	}
	h.auth = a
	h.letIn(ownerChat, "Olivia", true)
	h.letIn(memberChat, "Mark", false)
	var runner core.Runner = h.runner
	if h.runner2 != nil {
		runner = h.runner2
	}
	hub, err := New(Deps{
		Config: h.cfg, Auth: h.auth, Runner: runner, Approvals: h.approvals, Budget: h.budget, Sandbox: h.sandbox, LLM: h.llm,
		Store: h.store, Bus: h.bus, Audit: h.audit, Redactor: h.redactor, WorkDir: h.work,
		Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.hub = hub
	var ch core.Channel = h.ch
	if h.mc != nil {
		ch = h.mc
		if h.servers != nil {
			ch = &serverChecker{Checker: h.mc, servers: h.servers}
		}
	}
	if h.wrap != nil {
		ch = h.wrap(h.ch)
	}
	hub.Add(ch)
	return h
}

// withChecker makes the harness's adapter a chat app that says who is in
// the team, named name ("slack" or "discord"). Olivia and Mark are members.
// Slack's commands are typed after /bothy.
func withChecker(name string) func(*harness) {
	return func(h *harness) {
		h.mc = channeltest.NewChecker(name)
		h.ch, h.app = h.mc.Channel, name
		if name == "slack" {
			h.ch.CapsV.CommandPrefix, h.ch.CapsV.PrivatePosts = "/bothy ", 5
		}
		h.mc.Scope = "T1/"
		for _, id := range []string{ownerChat, memberChat} {
			h.mc.Members[id] = core.Membership{Status: core.MemberYes, Team: "Acme"}
		}
		h.cfg.Set(func(c *config.Config) {
			c.Channels.Telegram.Enabled = false
			switch name {
			case "slack":
				c.Channels.Slack.Enabled = true
			case "discord":
				c.Channels.Discord.Enabled = true
			}
		})
	}
}

// admin is an admin acting on the web.
func (h *harness) admin() core.Actor {
	return core.Actor{User: h.owner.ID, Role: core.RoleOwner, Via: "cookie"}
}

// letIn records ext as someone an admin let in on the harness's chat app,
// already greeted, with a DM chat of the same id.
func (h *harness) letIn(ext, name string, canApprove bool) core.PrincipalID {
	h.t.Helper()
	member := core.MemberUnknown
	if h.mc != nil {
		member = core.MemberYes
	}
	now := time.Now()
	if _, err := h.store.Identities().Touch(h.ctx, &core.ChannelIdentity{Channel: h.app, ExternalID: ext, DisplayName: name,
		Member: member, FirstSeen: now, LastSeen: now}); err != nil {
		h.t.Fatal(err)
	}
	id, err := h.store.Identities().Get(h.ctx, h.app, ext)
	if err != nil {
		h.t.Fatal(err)
	}
	id.DMChat, id.Member, id.MemberAt = ext, member, now
	if member == core.MemberYes {
		id.Status = core.IdentityPending
	}
	if err := h.store.Identities().Update(h.ctx, id); err != nil {
		h.t.Fatal(err)
	}
	if member == core.MemberYes {
		// A member check lets them in.
		if _, err := h.auth.Admit(h.ctx, *id, core.Membership{Status: core.MemberYes}); err != nil {
			h.t.Fatal(err)
		}
	}
	teammate := core.IdentityTeammate
	p := auth.TeammatePatch{CanApprove: &canApprove}
	if member != core.MemberYes {
		p.Status = &teammate
	}
	if _, err := h.auth.SetTeammate(h.ctx, h.admin(), h.app, ext, p); err != nil {
		h.t.Fatal(err)
	}
	id, err = h.store.Identities().Get(h.ctx, h.app, ext)
	if err != nil {
		h.t.Fatal(err)
	}
	id.Greet = ""
	if err := h.store.Identities().Update(h.ctx, id); err != nil {
		h.t.Fatal(err)
	}
	return id.Principal
}

// pid is a teammate's person principal.
func (h *harness) pid(ext string) core.PrincipalID {
	h.t.Helper()
	id, err := h.store.Identities().Get(h.ctx, h.app, ext)
	if err != nil {
		h.t.Fatal(err)
	}
	return id.Principal
}

// ident returns the identity of ext on the harness's chat app.
func (h *harness) ident(ext string) *core.ChannelIdentity {
	h.t.Helper()
	id, err := h.store.Identities().Get(h.ctx, h.app, ext)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

// opsChannel records when the hub sends or edits, to check throttling.
type opsChannel struct {
	*channeltest.Channel
	mu  sync.Mutex
	ops []op
}

type op struct {
	kind string
	at   time.Time
	msg  core.OutMessage
}

func (c *opsChannel) Send(ctx context.Context, m core.OutMessage) (core.MsgRef, error) {
	c.record("send", m)
	return c.Channel.Send(ctx, m)
}

func (c *opsChannel) Edit(ctx context.Context, ref core.MsgRef, m core.OutMessage) error {
	c.record("edit", m)
	return c.Channel.Edit(ctx, ref, m)
}

func (c *opsChannel) record(kind string, m core.OutMessage) {
	c.mu.Lock()
	c.ops = append(c.ops, op{kind, time.Now(), m})
	c.mu.Unlock()
}

func (c *opsChannel) all() []op {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ops)
}

// start runs the hub until the test ends.
func (h *harness) start() *harness {
	ctx, cancel := context.WithCancel(h.ctx)
	h.cancel, h.done = cancel, make(chan error, 1)
	go func() { h.done <- h.hub.Run(ctx) }()
	h.t.Cleanup(h.stop)
	synctest.Wait()
	if st := h.ch.Status(); st.State != "connected" {
		h.t.Fatalf("adapter not running after start: %+v", st)
	}
	return h
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	if err := <-h.done; err != nil {
		h.t.Errorf("Run returned %v", err)
	}
	h.cancel = nil
}

func (h *harness) user(name, display string, role core.Role) *core.User {
	h.t.Helper()
	u := &core.User{Username: name, DisplayName: display, Role: role, Timezone: "UTC", CreatedAt: time.Now()}
	if err := h.store.Users().Create(h.ctx, u); err != nil {
		h.t.Fatal(err)
	}
	return u
}

func (h *harness) dmChat(id string) core.ChatRef {
	return core.ChatRef{Channel: h.app, ChatID: id, Kind: core.ChatDM}
}

func (h *harness) groupChat(id string) core.ChatRef {
	return core.ChatRef{Channel: h.app, ChatID: id, Kind: core.ChatGroup}
}

// dm injects a DM from sender and waits for the hub to settle.
func (h *harness) dm(sender, text string) {
	h.inject(core.Inbound{Chat: h.dmChat(sender), SenderID: sender, SenderName: "User " + sender, Text: text, Mentioned: true})
}

func (h *harness) group(chat, sender, text string, mentioned bool) {
	h.inject(core.Inbound{Chat: h.groupChat(chat), SenderID: sender, SenderName: "User " + sender, Text: text, Mentioned: mentioned})
}

func (h *harness) inject(in core.Inbound) {
	h.ch.Inject(in)
	synctest.Wait()
}

func (h *harness) visible(chat string) []string { return h.ch.Visible(chat) }

func (h *harness) lastText(chat string) string {
	v := h.visible(chat)
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func (h *harness) wantVisible(chat, sub string) {
	h.t.Helper()
	for _, v := range h.visible(chat) {
		if strings.Contains(v, sub) {
			return
		}
	}
	h.t.Fatalf("chat %s shows %q; want a message containing %q", chat, h.visible(chat), sub)
}

// space records a group chat of the team.
func (h *harness) space(chat string, enabled, mentionOnly bool) *core.Space {
	h.t.Helper()
	sp := &core.Space{Kind: core.SpaceGroup, Chat: h.groupChat(chat), Title: "Team", Enabled: enabled,
		MentionOnly: mentionOnly, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	p := &core.Principal{Kind: core.PrincipalSpace, DisplayName: "Team", ToolProfile: "restricted"}
	if err := h.store.Spaces().Create(h.ctx, sp, p); err != nil {
		h.t.Fatal(err)
	}
	return sp
}

// session returns the open session of chat for principal p.
func (h *harness) session(p core.PrincipalID, chat core.ChatRef) *core.Session {
	h.t.Helper()
	s, err := h.store.Sessions().ByChannelKey(h.ctx, p, chat.Key())
	if err != nil {
		h.t.Fatalf("no session for %s: %v", chat.Key(), err)
	}
	return s
}

func bubble(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	synctest.Test(t, f)
}

// plainRender adds the plain copy the policy module writes for the tools
// these tests request (shell, release_deploy), so the hub is tested on the
// words real cards carry.
func plainRender(fake *fakes.Approvals) func(core.ApprovalRequest) core.Card {
	return func(r core.ApprovalRequest) core.Card {
		c := fake.RenderCard(r)
		if r.Action == nil {
			return c
		}
		c.Kind, c.Tone = core.CardAction, core.ToneDefault
		switch r.Action.Tool {
		case "shell":
			c.Question, c.PrimaryLabel, c.Ing = "Run this command?", "Run command", "running a command"
			c.Fields, c.Code = []core.CardField{{Label: "Command", Value: r.Action.Argv[len(r.Action.Argv)-1], Mono: true}}, ""
		case "release_deploy":
			c.Question, c.PrimaryLabel, c.Ing = "Deploy acme/web to staging?", "Approve deploy", "deploying acme/web to staging"
			c.Consequence, c.Kind, c.Code = "This updates staging for everyone using it.", core.CardDeploy, ""
		case "fs_delete":
			c.Question, c.PrimaryLabel, c.Ing = "Delete notes/old.md?", "Delete file", "deleting notes/old.md"
			c.Consequence, c.Tone = "This can't be undone.", core.ToneDestructive
		}
		return c
	}
}

func authPatch(status *core.IdentityStatus, canApprove *bool) auth.TeammatePatch {
	return auth.TeammatePatch{Status: status, CanApprove: canApprove}
}

// serverChecker is a member checker that lists its servers (Discord).
type serverChecker struct {
	*channeltest.Checker
	servers []core.TeamInfo
}

func (c *serverChecker) Teams() []core.TeamInfo { return slices.Clone(c.servers) }
