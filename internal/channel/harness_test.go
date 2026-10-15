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
	store     core.Store
	auth      *fakes.Auth
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

const (
	ownerChat  = "1001"
	memberChat = "2002"
)

// newHarness builds a hub over memstore and the testkit fakes, with a
// channeltest adapter named "telegram". It must run inside a synctest bubble.
func newHarness(t *testing.T, opts ...func(*harness)) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{
		t: t, ctx: ctx, store: memstore.New(), runner: &fakes.Runner{}, budget: &fakes.Budget{},
		approvals: &fakes.Approvals{Manual: true}, audit: &fakes.Auditor{}, redactor: fakes.NewRedactor(),
		sandbox: &sandboxtest.Sandbox{}, bus: events.New(events.Options{}), work: t.TempDir(),
	}
	h.runner.Store = h.store
	h.approvals.Render = plainRender(h.approvals)
	h.auth = &fakes.Auth{Identities: map[string]core.IdentityResolution{}}
	h.owner = h.user("olivia", "Olivia", core.RoleOwner)
	h.member = h.user("mark", "Mark", core.RoleMember)
	h.link(ownerChat, h.owner)
	h.link(memberChat, h.member)
	c := config.Default()
	c.Channels.Telegram.Enabled = true
	h.cfg = newCfgView(c)
	h.ch = channeltest.New("telegram")
	for _, o := range opts {
		o(h)
	}
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
	if h.wrap != nil {
		ch = h.wrap(h.ch)
	}
	hub.Add(ch)
	return h
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

// link makes the Telegram user ext a linked identity of u with a DM chat of
// the same id (Telegram private chats share the user's id).
func (h *harness) link(ext string, u *core.User) {
	h.t.Helper()
	_, err := h.store.Identities().Touch(h.ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: ext,
		Status: core.IdentityLinked, User: u.ID, DMChat: ext, LastSeen: time.Now()})
	if err != nil {
		h.t.Fatal(err)
	}
	h.auth.Identities["telegram:"+ext] = core.IdentityResolution{Status: core.IdentityLinked, User: u.ID, Role: u.Role,
		Principal: core.UserPrincipal(u.ID)}
}

func (h *harness) dmChat(id string) core.ChatRef {
	return core.ChatRef{Channel: "telegram", ChatID: id, Kind: core.ChatDM}
}

func (h *harness) groupChat(id string) core.ChatRef {
	return core.ChatRef{Channel: "telegram", ChatID: id, Kind: core.ChatGroup}
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

// space records a group space owned by owner.
func (h *harness) space(chat string, enabled, mentionOnly bool) *core.Space {
	h.t.Helper()
	sp := &core.Space{Kind: "group", Chat: h.groupChat(chat), Title: "Team", Owner: h.owner.ID, Enabled: enabled,
		MentionOnly: mentionOnly, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	p := &core.Principal{Kind: core.PrincipalSpace, User: h.owner.ID, DisplayName: "Team", ToolProfile: "restricted"}
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
