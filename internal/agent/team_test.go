package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// teamPrincipal adds a channel (kind group) or a teammate (kind person) to
// the harness store, with a titled session in its chat.
func (h *harness) teamPrincipal(kind, chatID, name string) (core.PrincipalID, *core.Session) {
	h.t.Helper()
	chatKind := core.ChatGroup
	if kind == core.SpacePerson {
		chatKind = core.ChatDM
	}
	chat := core.ChatRef{Channel: "slack", ChatID: chatID, Kind: chatKind}
	sp := &core.Space{Kind: kind, Chat: chat, Title: name, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	pr := &core.Principal{DisplayName: name, ToolProfile: "standard", CreatedAt: time.Now()}
	if err := h.store.Spaces().Create(h.ctx, sp, pr); err != nil {
		h.t.Fatal(err)
	}
	s, err := h.r.NewSession(h.ctx, sp.Principal, "slack", &chat)
	if err != nil {
		h.t.Fatal(err)
	}
	if s, err = h.r.mutateSession(h.ctx, s.ID, func(s *core.Session) error { s.Title = "Team"; return nil }); err != nil {
		h.t.Fatal(err)
	}
	return sp.Principal, s
}

func teamReq(p core.PrincipalID, s *core.Session, sender, name, text string) core.RunRequest {
	return core.RunRequest{Session: s.ID, Principal: p, Trigger: core.TrigMessage, Input: []core.Part{{Type: core.PartText, Text: text}},
		Provenance: core.ProvUser, Sender: sender, SenderName: name, Chat: s.Chat}
}

func TestTeammateRunContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		look := &testTool{name: "look"}
		h := newHarness(t, opts{tools: []core.Tool{look}, steps: []fakes.LLMStep{
			{ToolCalls: []core.ToolCall{call("look", `{}`)}},
			{Text: []string{"Hi Maya."}},
		}})
		defer h.close()
		maya, sess := h.teamPrincipal(core.SpacePerson, "U1", "Maya Chen")
		res, err := h.r.Submit(h.ctx, teamReq(maya, sess, "slack:U1", "Maya Chen", "hello"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		run := h.run(res.Run)
		if run.Status != core.RunDone || run.User != "" {
			t.Fatalf("run = %s, user %q; want done, billed to no account", run.Status, run.User)
		}
		reqs := h.requests()
		if len(reqs) == 0 {
			t.Fatal("no model call")
		}
		text := allText(reqs[0])
		if !strings.Contains(text, "sender: Maya Chen") || !strings.Contains(text, "a teammate in Slack") {
			t.Fatalf("the turn context doesn't name the teammate:\n%s", text)
		}
		if len(h.pol.Inputs) == 0 || h.pol.Inputs[0].User != "" || h.pol.Inputs[0].Role != core.RoleMember || h.pol.Inputs[0].Principal.Kind != core.PrincipalPerson {
			t.Fatalf("policy input = %+v, want no user and a member's role", h.pol.Inputs)
		}
		if _, err := h.r.Submit(h.ctx, core.RunRequest{Session: sess.ID, Principal: maya, User: h.user.ID, Trigger: core.TrigMessage,
			Input: []core.Part{{Type: core.PartText, Text: "x"}}}); err == nil {
			t.Fatal("an account submitted into a teammate's principal")
		}
	})
}

// A resumed run of a team-owned principal acts for no account, whatever an
// older row says.
func TestTeamRunContextDropsStaleUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{})
		defer h.close()
		eng, sess := h.teamPrincipal(core.SpaceGroup, "C1", "#eng")
		rs := &runState{r: h.r, ctx: h.ctx, req: core.RunRequest{Session: sess.ID, Principal: eng, User: h.user.ID}}
		if err := h.r.loadRunContext(h.ctx, rs); err != nil {
			t.Fatal(err)
		}
		if rs.req.User != "" || rs.user != nil || rs.role != core.RoleMember {
			t.Fatalf("run context = user %q / %+v / role %q", rs.req.User, rs.user, rs.role)
		}
		rs = &runState{r: h.r, ctx: h.ctx, req: core.RunRequest{Session: h.sess.ID, Principal: h.pid, User: h.user.ID}}
		if err := h.r.loadRunContext(h.ctx, rs); err != nil || rs.role != core.RoleOwner || rs.user == nil {
			t.Fatalf("an account's run context = %+v, %v", rs, err)
		}
	})
}

func TestTeamDenialAndGrantNameTheChatDecider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{})
		defer h.close()
		maya, sess := h.teamPrincipal(core.SpacePerson, "U1", "Maya Chen")
		if _, err := h.store.Identities().Touch(h.ctx, &core.ChannelIdentity{Channel: "slack", ExternalID: "U9", DisplayName: "Sam Patel",
			LastSeen: time.Now()}); err != nil {
			t.Fatal(err)
		}
		rs := &runState{r: h.r, ctx: h.ctx, id: "run_1", req: core.RunRequest{Session: sess.ID, Principal: maya}}
		if err := h.r.loadRunContext(h.ctx, rs); err != nil {
			t.Fatal(err)
		}
		denied := &core.Approval{Status: core.ApprovalDenied, DecidedByIdentity: "slack:U9", Comment: "not today",
			Approver: core.ApproverReq{Team: true, RequesterIdentity: "slack:U1"}}
		if got := h.r.denialText(h.ctx, rs, denied); got != "An approver, “Sam Patel”, denied this action. Their reason: “not today”" {
			t.Fatalf("denial by a chat approver = %q", got)
		}
		denied.DecidedByIdentity, denied.Comment = "slack:U1", ""
		if got := h.r.denialText(h.ctx, rs, denied); got != "The user cancelled the request before it was approved." {
			t.Fatalf("the requester's withdraw = %q", got)
		}
		denied.DecidedByIdentity, denied.DecidedBy = "", h.user.ID
		if got := h.r.denialText(h.ctx, rs, denied); got != "An approver, “Olive”, denied this action." {
			t.Fatalf("a web admin's deny of a team request = %q", got)
		}

		action := &core.Action{Tool: "act", SideEffect: core.SELocalWrite, Payload: json.RawMessage(`{}`)}
		pc := &pcall{action: action, dec: core.Decision{Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.Deny}}}
		h.r.grantFor(h.ctx, rs, pc, core.AllowSession, &core.Approval{DecidedByIdentity: "slack:U9"})
		if len(h.pol.Grants) != 1 || h.pol.Grants[0].CreatedByIdentity != "slack:U9" || h.pol.Grants[0].CreatedBy != "" {
			t.Fatalf("grants = %+v, want one created by the chat approver", h.pol.Grants)
		}
		var actor string
		for _, a := range h.audit.All() {
			if a.Action == "grant.create" {
				actor = a.Actor
			}
		}
		if actor != "channel:slack:U9" {
			t.Fatalf("grant.create actor = %q", actor)
		}
	})
}

func TestTeamRunsTakeSlotsPerPrincipal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{cfg: func(c *config.Config) { c.Agent.MaxRunsPerUser = 1 }, steps: []fakes.LLMStep{
			{Text: []string{"one"}, Block: block}, {Text: []string{"two"}, Block: block}, {Text: []string{"three"}},
		}})
		defer h.close()
		maya, mayaSess := h.teamPrincipal(core.SpacePerson, "U1", "Maya")
		sam, samSess := h.teamPrincipal(core.SpacePerson, "U2", "Sam")
		mayaChat2 := core.ChatRef{Channel: "slack", ChatID: "U1", ThreadID: "t2", Kind: core.ChatDM}
		mayaSess2, err := h.r.NewSession(h.ctx, maya, "slack", &mayaChat2)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.r.mutateSession(h.ctx, mayaSess2.ID, func(s *core.Session) error { s.Title = "x"; return nil })
		for _, r := range []core.RunRequest{teamReq(maya, mayaSess, "slack:U1", "Maya", "a"), teamReq(sam, samSess, "slack:U2", "Sam", "b"),
			teamReq(maya, mayaSess2, "slack:U1", "Maya", "c")} {
			if _, err := h.r.Submit(h.ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		if n := len(h.requests()); n != 2 {
			t.Fatalf("model calls while blocked = %d, want 2: two teammates run at once, Maya's second waits for her slot", n)
		}
		close(block)
		synctest.Wait()
		if n := len(h.requests()); n != 3 {
			t.Fatalf("model calls after = %d, want 3", n)
		}
	})
}

func TestChannelSteerOnlyFromSameSender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"working"}, Block: block}, {Text: []string{"done"}}, {Text: []string{"bob's turn"}}}})
		defer h.close()
		eng, sess := h.teamPrincipal(core.SpaceGroup, "C1", "#eng")
		maya := teamReq(eng, sess, "slack:U1", "Maya", "deploy notes")
		maya.Provenance = core.ProvUntrusted
		res, err := h.r.Submit(h.ctx, maya)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		bob := teamReq(eng, sess, "slack:U2", "Bob", "and delete the logs")
		bob.Provenance = core.ProvUntrusted
		got, err := h.r.Submit(h.ctx, bob)
		if err != nil || got.Steered {
			t.Fatalf("another sender's message = %+v, %v; want its own turn", got, err)
		}
		again := teamReq(eng, sess, "slack:U1", "Maya", "also the changelog")
		again.Provenance = core.ProvUntrusted
		if got, err := h.r.Submit(h.ctx, again); err != nil || !got.Steered || got.Run != res.Run {
			t.Fatalf("the same sender's message = %+v, %v; want it to steer", got, err)
		}
		close(block)
		synctest.Wait()
	})
}

func TestStopPrincipal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		act := &testTool{name: "act", se: core.SEExternalSend}
		h := newHarness(t, opts{tools: []core.Tool{act}, appr: &fakes.Approvals{Manual: true},
			pol: &fakes.Policy{Verdicts: map[string]core.Verdict{"act": core.VerdictAsk}},
			steps: []fakes.LLMStep{
				{Text: []string{"working"}, Block: block},
				{ToolCalls: []core.ToolCall{call("act", `{}`)}},
			}})
		defer h.close()
		maya, sess := h.teamPrincipal(core.SpacePerson, "U1", "Maya")
		chat2 := core.ChatRef{Channel: "slack", ChatID: "U1", ThreadID: "t2", Kind: core.ChatDM}
		sess2, err := h.r.NewSession(h.ctx, maya, "slack", &chat2)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.r.mutateSession(h.ctx, sess2.ID, func(s *core.Session) error { s.Title = "x"; return nil })
		r1, err := h.r.Submit(h.ctx, teamReq(maya, sess, "slack:U1", "Maya", "a"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		r2, err := h.r.Submit(h.ctx, teamReq(maya, sess2, "slack:U1", "Maya", "b"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if st := h.run(r2.Run).Status; st != core.RunWaitingApproval {
			t.Fatalf("second run = %s, want waiting for approval", st)
		}
		own := h.submit("an account's run keeps going")
		_ = own
		ctx := core.WithAuditOrigin(context.Background(), core.AuditOrigin{Actor: "user:" + string(h.user.ID)})
		n, err := h.r.StopPrincipal(ctx, maya)
		if err != nil || n < 2 {
			t.Fatalf("StopPrincipal = %d, %v; want both runs", n, err)
		}
		close(block)
		synctest.Wait()
		for _, id := range []core.RunID{r1.Run, r2.Run} {
			if st := h.run(id).Status; st != core.RunStopped {
				t.Errorf("run %s = %s, want stopped", id, st)
			}
		}
		if a := h.appr.All()[0]; a.Status != core.ApprovalCancelled {
			t.Errorf("approval = %s, want cancelled", a.Status)
		}
		var stops int
		for _, a := range h.audit.All() {
			if a.Action == "run.stop" && a.Actor == "user:"+string(h.user.ID) {
				stops++
			}
		}
		if stops == 0 {
			t.Error("run.stop isn't audited with the origin's actor")
		}
	})
}

func TestTeamReadsAudited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fetch := &testTool{name: "web_fetch"}
		local := &testTool{name: "fs_read"}
		h := newHarness(t, opts{tools: []core.Tool{fetch, local}, steps: []fakes.LLMStep{
			{ToolCalls: []core.ToolCall{call("web_fetch", `{}`), call("fs_read", `{}`)}},
			{Text: []string{"read"}},
			{ToolCalls: []core.ToolCall{call("web_fetch", `{}`)}},
			{Text: []string{"read too"}},
		}})
		defer h.close()
		fetch.planURL = "https://docs.example.com/guide?token=x"
		maya, sess := h.teamPrincipal(core.SpacePerson, "U1", "Maya")
		if _, err := h.r.Submit(h.ctx, teamReq(maya, sess, "slack:U1", "Maya", "read the guide")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		h.submit("an account reads it too")
		synctest.Wait()
		var reads []core.AuditEntry
		for _, a := range h.audit.All() {
			if a.Action == "tool.read" {
				reads = append(reads, a)
			}
		}
		if len(reads) != 1 || reads[0].Target != string(maya) || !strings.Contains(string(reads[0].Detail), `"target":"docs.example.com/guide"`) ||
			!strings.HasPrefix(reads[0].Actor, "agent:") {
			t.Fatalf("tool.read rows = %+v, want one for the teammate's web read only", reads)
		}
	})
}
