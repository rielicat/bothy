package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/budget"
	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/events"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
	"github.com/bothyhq/bothy/internal/testkit/memstore"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

// harness wires a Runner to memstore and the testkit fakes. Build it inside
// a synctest bubble so every goroutine and timer belongs to the bubble.
type harness struct {
	t      *testing.T
	ctx    context.Context
	r      *Runner
	store  core.Store
	cfg    *config.Config
	llm    *routerLLM
	reg    *fakes.Registry
	tools  core.ToolRegistry
	pol    *fakes.Policy
	appr   *fakes.Approvals
	budget core.Budget
	mem    *fakes.Memory
	skills *fakes.Skills
	sbx    *sandboxtest.Sandbox
	egress *sandboxtest.Egress
	red    *fakes.Redactor
	bus    *events.Bus
	audit  *fakes.Auditor
	hub    *fakes.Hub
	user   *core.User
	pid    core.PrincipalID
	sess   *core.Session
	sink   *fakes.Sink
	data   string
	work   string
}

type opts struct {
	cfg        func(c *config.Config)
	steps      []fakes.LLMStep
	info       core.ModelInfo
	realBudget bool
	tools      []core.Tool
	store      core.Store
	appr       *fakes.Approvals
	pol        *fakes.Policy
	work, data string
	noHub      bool
	coreOnly   bool // ForRun hides lazy tools, as the real registry does
}

func newHarness(t *testing.T, o opts) *harness {
	t.Helper()
	ctx := context.Background()
	cfg := config.Default()
	cfg.Agent.CoalesceWindow = 0
	if o.cfg != nil {
		o.cfg(cfg)
	}
	h := &harness{t: t, ctx: ctx, cfg: cfg, sink: &fakes.Sink{}}
	h.store = o.store
	if h.store == nil {
		h.store = memstore.New()
	}
	h.data, h.work = o.data, o.work
	if h.data == "" {
		h.data = t.TempDir()
	}
	if h.work == "" {
		h.work = t.TempDir()
	}
	h.llm = &routerLLM{LLM: &fakes.LLM{Steps: o.steps, Info: o.info}}
	h.reg = &fakes.Registry{}
	h.tools = h.reg
	if o.coreOnly {
		h.tools = coreOnlyRegistry{h.reg}
	}
	for _, tool := range o.tools {
		if err := h.reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	h.pol = o.pol
	if h.pol == nil {
		h.pol = &fakes.Policy{}
	}
	h.appr = o.appr
	if h.appr == nil {
		h.appr = &fakes.Approvals{}
	}
	h.mem = &fakes.Memory{}
	h.skills = &fakes.Skills{}
	h.sbx = &sandboxtest.Sandbox{}
	h.egress = &sandboxtest.Egress{}
	h.red = fakes.NewRedactor()
	h.bus = events.New(events.Options{})
	h.audit = &fakes.Auditor{}
	h.hub = &fakes.Hub{}
	if users, _ := h.store.Users().List(ctx); len(users) > 0 {
		h.user = users[0]
	} else {
		h.user = &core.User{Username: "owner", DisplayName: "Olive", Role: core.RoleOwner, PWHash: "x", Timezone: "UTC", CreatedAt: time.Now()}
		if err := h.store.Users().Create(ctx, h.user); err != nil {
			t.Fatal(err)
		}
	}
	h.pid = core.UserPrincipal(h.user.ID)
	if o.realBudget {
		b, err := budget.New(budget.Deps{Store: h.store, Config: config.Static(cfg), Bus: h.bus, Audit: h.audit})
		if err != nil {
			t.Fatal(err)
		}
		h.budget = b
	} else {
		h.budget = &fakes.Budget{}
	}
	h.r = h.newRunner()
	if !o.noHub {
		h.r.SetHub(h.hub)
	}
	h.sess = h.newSession("web", nil)
	return h
}

func (h *harness) newRunner() *Runner {
	h.t.Helper()
	r, err := New(Deps{Store: h.store, Config: config.Static(h.cfg), LLM: h.llm, Tools: h.tools, Policy: h.pol,
		Approvals: h.appr, Budget: h.budget, Memory: h.mem, Skills: h.skills, Sandbox: h.sbx, Egress: h.egress,
		Redactor: h.red, Bus: h.bus, Audit: h.audit, DataDir: h.data, WorkDir: h.work})
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// newSession creates a titled session so no title call consumes a step.
func (h *harness) newSession(origin string, chat *core.ChatRef) *core.Session {
	h.t.Helper()
	s, err := h.r.NewSession(h.ctx, h.pid, origin, chat)
	if err != nil {
		h.t.Fatal(err)
	}
	s, err = h.r.mutateSession(h.ctx, s.ID, func(s *core.Session) error { s.Title = "Test"; return nil })
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) req(text string) core.RunRequest {
	return core.RunRequest{Session: h.sess.ID, Principal: h.pid, User: h.user.ID, Trigger: core.TrigMessage,
		Input: []core.Part{{Type: core.PartText, Text: text}}, Provenance: core.ProvUser, Sender: "web:" + string(h.user.ID),
		Sink: h.sink}
}

func (h *harness) submit(text string) core.SubmitResult {
	h.t.Helper()
	res, err := h.r.Submit(h.ctx, h.req(text))
	if err != nil {
		h.t.Fatalf("submit %q: %v", text, err)
	}
	return res
}

func (h *harness) run(id core.RunID) *core.Run {
	h.t.Helper()
	run, err := h.store.Runs().Get(h.ctx, core.SystemViewer(), id)
	if err != nil {
		h.t.Fatalf("run %s: %v", id, err)
	}
	return run
}

func (h *harness) runs() []*core.Run {
	h.t.Helper()
	runs, err := h.store.Runs().List(h.ctx, core.SystemViewer(), core.RunFilter{Session: h.sess.ID})
	if err != nil {
		h.t.Fatal(err)
	}
	return runs
}

func (h *harness) messages() []*core.Message {
	h.t.Helper()
	msgs, err := h.store.Messages().Range(h.ctx, core.SystemViewer(), h.sess.ID, 0, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return msgs
}

func (h *harness) notices() []string {
	var out []string
	for _, m := range h.messages() {
		if m.Kind == core.KindNotice {
			out = append(out, m.PlainText())
		}
	}
	return out
}

func (h *harness) session() *core.Session {
	h.t.Helper()
	s, err := h.store.Sessions().Get(h.ctx, core.SystemViewer(), h.sess.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) requests() []*core.Request { return h.llm.agentRequests() }

// close shuts the runner down so no bubble goroutine outlives the test.
func (h *harness) close() { h.r.shutdown() }

func (h *harness) toolCalls(id core.RunID) []*core.ToolCallRecord {
	h.t.Helper()
	recs, err := h.store.Runs().ToolCalls(h.ctx, core.SystemViewer(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return recs
}

// routerLLM answers compaction and title requests itself and passes agent
// turns to the scripted fakes.LLM, so scripts list agent steps only.
type routerLLM struct {
	*fakes.LLM
	mu        sync.Mutex
	summaries []string // consumed in order; "" or no entry = fail
	summaryN  int
	agentReqs []*core.Request
	auxReqs   []*core.Request
	titles    int
}

var errSummarizer = errors.New("summarizer unavailable")

func (l *routerLLM) Complete(ctx context.Context, slot core.Slot, override *core.ModelRef, build core.BuildFunc, on func(core.StreamEvent) error) (*core.Response, core.CallInfo, error) {
	info := l.LLM.Chain(slot, override)[0]
	req, err := build(info)
	if err != nil {
		return nil, core.CallInfo{}, err
	}
	sys := ""
	if len(req.System) > 0 {
		sys = req.System[0].Text
	}
	ci := core.CallInfo{ID: core.NewID(core.PrefixLLMCall), Ref: info.Ref, Slot: slot}
	switch {
	case strings.HasPrefix(sys, "You compress a conversation"):
		l.mu.Lock()
		l.auxReqs = append(l.auxReqs, req)
		var text string
		if l.summaryN < len(l.summaries) {
			text = l.summaries[l.summaryN]
		}
		l.summaryN++
		l.mu.Unlock()
		if text == "" {
			ci.Error = errSummarizer.Error()
			return nil, ci, errSummarizer
		}
		ci.Usage = core.Usage{Input: 100, Output: 50}
		return &core.Response{Message: core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: text}}}, Usage: ci.Usage}, ci, nil
	case sys == titlePrompt:
		l.mu.Lock()
		l.titles++
		l.auxReqs = append(l.auxReqs, req)
		l.mu.Unlock()
		return &core.Response{Message: core.Message{Role: core.MsgAssistant, Parts: []core.Part{{Type: core.PartText, Text: "\"Weekend plans.\""}}}}, ci, nil
	}
	l.mu.Lock()
	l.agentReqs = append(l.agentReqs, req)
	l.mu.Unlock()
	return l.LLM.Complete(ctx, slot, override, func(core.ModelInfo) (*core.Request, error) { return req, nil }, on)
}

func (l *routerLLM) agentRequests() []*core.Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.agentReqs)
}

func (l *routerLLM) setSummaries(s ...string) {
	l.mu.Lock()
	l.summaries, l.summaryN = s, 0
	l.mu.Unlock()
}

// testTool is a configurable core.Tool.
type testTool struct {
	name   string
	se     core.SideEffect
	exec   bool
	taint  core.Taint
	lazy   bool
	schema string
	run    func(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error)
	calls  atomic.Int32
}

func (t *testTool) Spec() core.ToolSpec {
	schema := t.schema
	if schema == "" {
		schema = `{"type":"object"}`
	}
	se := t.se
	if se == 0 {
		se = core.SERead
	}
	return core.ToolSpec{
		Def:      core.ToolDef{Name: t.name, Description: "test tool " + t.name, Schema: json.RawMessage(schema)},
		Source:   "builtin",
		Manifest: core.Manifest{Title: "Run " + t.name, SideEffect: se, Exec: t.exec, Taint: t.taint, Core: !t.lazy, SpaceOK: true},
	}
}

func (t *testTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	return &core.Action{Tool: t.name, Kind: core.ActGeneric, SideEffect: t.Spec().Manifest.SideEffect, Payload: args}, nil
}

func (t *testTool) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	t.calls.Add(1)
	if t.run != nil {
		return t.run(ctx, env, a)
	}
	return &core.ToolResult{Content: "ok " + string(a.Payload)}, nil
}

func call(name, args string) core.ToolCall {
	return core.ToolCall{Name: name, Args: json.RawMessage(args)}
}

// checkPairs asserts the provider invariant: every assistant tool call is
// followed by exactly one matching result before the next non-tool message.
func checkPairs(t *testing.T, req *core.Request) {
	t.Helper()
	msgs := req.Messages
	for i, m := range msgs {
		if m.Role != core.MsgAssistant {
			continue
		}
		var want []string
		for _, p := range m.Parts {
			if p.Type == core.PartToolCall {
				want = append(want, p.ToolCall.ID)
			}
		}
		var got []string
		for j := i + 1; j < len(msgs) && msgs[j].Role == core.MsgTool; j++ {
			for _, p := range msgs[j].Parts {
				got = append(got, p.ToolResult.CallID)
			}
		}
		if !slices.Equal(want, got) {
			t.Fatalf("message %d: calls %v answered by %v", i, want, got)
		}
	}
	for i, m := range msgs {
		if m.Role == core.MsgTool && (i == 0 || (msgs[i-1].Role != core.MsgAssistant && msgs[i-1].Role != core.MsgTool)) {
			t.Fatalf("message %d: tool result without a preceding call", i)
		}
	}
}

func lastUserText(req *core.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == core.MsgUser {
			return req.Messages[i].PlainText()
		}
	}
	return ""
}

func allText(req *core.Request) string {
	var b strings.Builder
	for _, s := range req.System {
		b.WriteString(s.Text + "\n")
	}
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			switch {
			case p.ToolResult != nil:
				b.WriteString(p.ToolResult.Content + "\n")
			case p.ToolCall != nil:
				b.WriteString(p.ToolCall.Name + string(p.ToolCall.Args) + "\n")
			default:
				b.WriteString(p.Text + "\n")
			}
		}
	}
	return b.String()
}
