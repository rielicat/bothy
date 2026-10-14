//go:build e2e

package e2e

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestChatTranscriptAndActivity drives the server side of the v0.2 chat
// (docs/design/ui.md §17 S8, S10, S11, S14, S15, S16) through the real
// runner, policy and store: a chat is stored with its first message and
// named after it, a pending delete reads as waiting until its result, the
// decided card is still in the transcript after a reload, and search,
// tasks and the audit log name what happened.
func TestChatTranscriptAndActivity(t *testing.T) {
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `UI-`, MatchToolResult: `.`, Text: []string{"Done."}},
		mockllm.Rule{MatchUser: `UI-WRITE`, ToolCalls: []core.ToolCall{pltCall("call_w", "fs_write", `{"path":"notes/staging.md","content":"old\n"}`)}},
		mockllm.Rule{MatchUser: `UI-DELETE`, ToolCalls: []core.ToolCall{pltCall("call_d", "use_tool", `{"name":"fs_delete","args":{"path":"notes/staging.md"}}`)}},
	)
	g := Start(t, Options{Config: pltYAML(llm.URL(), nil, "")})
	owner := pltOwner(t, g)

	owner.NewSession(t) // opened and left: never listed
	var created dto.SessionCreated
	req := dto.SessionCreateReq{Message: &dto.SendReq{Text: "UI-WRITE save the staging notes"}}
	if code := owner.JSON(t, http.MethodPost, "/api/sessions", req, &created); code != http.StatusAccepted || created.Run == nil {
		t.Fatalf("create with the first message = %d %+v", code, created)
	}
	if d := owner.Finished(t, created.Run.Run); d.Run.Status != core.RunDone {
		t.Fatalf("first run = %s %s", d.Run.Status, d.Run.Error)
	}
	title := ""
	deadline := time.Now().Add(10 * time.Second)
	for title != "Platform scenario" {
		var l dto.List[core.Session]
		if code := owner.JSON(t, http.MethodGet, "/api/sessions", nil, &l); code != http.StatusOK || len(l.Items) != 1 || l.Items[0].ID != created.ID {
			t.Fatalf("sessions = %d %+v, want only the chat with a message", code, l.Items)
		}
		title = l.Items[0].Title
		if title != "UI-WRITE save the staging notes" && title != "Platform scenario" {
			t.Fatalf("title %q is neither the first message nor the generated title", title)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the generated title never replaced %q", title)
		}
		time.Sleep(25 * time.Millisecond)
	}

	page := func() (dto.MessagePage, map[string]core.Part) {
		t.Helper()
		var p dto.MessagePage
		if code := owner.JSON(t, http.MethodGet, "/api/sessions/"+string(created.ID)+"/messages", nil, &p); code != http.StatusOK {
			t.Fatalf("messages = %d", code)
		}
		parts := map[string]core.Part{}
		for _, m := range p.Messages {
			for _, part := range m.Parts {
				if part.Type == core.PartToolCall {
					parts[part.ToolCall.ID] = part
				}
			}
		}
		return p, parts
	}
	run := owner.Send(t, created.ID, "UI-DELETE clean up the staging notes")
	ap := owner.WaitApproval(t, run, core.ApprovalPending)
	p, parts := page()
	if st := parts["call_d"].State; st != core.ToolWaitingApproval || parts["call_d"].Approval != ap.ID {
		t.Fatalf("a pending delete reads %q (approval %q), want waiting_approval on %s", st, parts["call_d"].Approval, ap.ID)
	}
	if parts["call_w"].State != core.ToolDone {
		t.Fatalf("the finished write reads %q", parts["call_w"].State)
	}
	if len(p.Approvals) != 1 || p.Approvals[0].ID != ap.ID || p.Approvals[0].Status != core.ApprovalPending {
		t.Fatalf("page approvals = %+v", p.Approvals)
	}
	if got := owner.Resolve(t, ap.ID, core.AllowOnce); got.DecidedByName != "owner" {
		t.Fatalf("resolved approval names its decider %q", got.DecidedByName)
	}
	if d := owner.Finished(t, run); d.Run.Status != core.RunDone || d.Run.ChatTitle != "Platform scenario" || d.Run.RequestedByName != "owner" {
		t.Fatalf("delete run = %s, names %q / %q", d.Run.Status, d.Run.ChatTitle, d.Run.RequestedByName)
	}
	p, parts = page() // a reload
	if st := parts["call_d"].State; st != core.ToolDone {
		t.Fatalf("the approved delete reads %q after its result", st)
	}
	if len(p.Approvals) != 1 || p.Approvals[0].Status != core.ApprovalAllowed || p.Approvals[0].DecidedByName != "owner" {
		t.Fatalf("the decided card is not in the transcript after a reload: %+v", p.Approvals)
	}

	var res dto.SearchResp
	if code := owner.JSON(t, http.MethodGet, "/api/search?q="+url.QueryEscape("staging notes"), nil, &res); code != http.StatusOK ||
		len(res.Chats) != 1 || res.Chats[0].Session != created.ID || res.Chats[0].Title != title || len(res.Chats[0].Ranges) != 2 {
		t.Fatalf("search = %d %+v", code, res)
	}

	var audit dto.List[dto.AuditRow]
	if code := owner.JSON(t, http.MethodGet, "/api/audit?action=approval.", nil, &audit); code != http.StatusOK {
		t.Fatalf("audit = %d", code)
	}
	summaries := map[string]string{}
	for _, r := range audit.Items {
		summaries[r.Action] = r.ActorName + ": " + r.Summary
	}
	if summaries["approval.request"] != "Bothy: Asked to delete notes/staging.md" ||
		summaries["approval.decide"] != "owner: Approved deleting notes/staging.md" {
		t.Fatalf("audit summaries = %q", summaries)
	}

	var est dto.RefineEstimate
	if code := owner.JSON(t, http.MethodGet, "/api/sessions/"+string(created.ID)+"/refine-estimate", nil, &est); code != http.StatusOK ||
		est.Drafts != 3 || est.Multiplier != 12 {
		t.Fatalf("refine estimate = %d %+v", code, est)
	}
}
