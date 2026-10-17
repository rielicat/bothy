//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestTeamMemory is E10b: the owner asks Bothy on the web to remember
// something for the whole team. Nothing is saved until an admin approves the
// card; then a teammate in Telegram has it in their prompt. A teammate's own
// proposal reaches the chat approver only as a link (it needs an admin), and
// an admin's deny leaves TEAM.md as it was.
func TestTeamMemory(t *testing.T) {
	const tom = 6004
	e := chStart(t, chOpts{
		telegram: true,
		rules: []mockllm.Rule{
			chTitleRule,
			{MatchToolResult: `Remembered for everyone`, Text: []string{"Done: everyone's chats know it now."}},
			{MatchToolResult: `denied this action`, Text: []string{"An admin said no, so it isn't saved."}},
			{MatchUser: `(?i)remember for the whole team: our deploy day`, Text: []string{"I'll ask an admin to approve this for everyone."},
				ToolCalls: []core.ToolCall{{Name: "memory", Args: json.RawMessage(`{"file":"TEAM","op":"add","new":"Our deploy day is Thursday."}`)}}},
			{MatchUser: `(?i)remember for everyone: lunch`, ToolCalls: []core.ToolCall{{Name: "memory",
				Args: json.RawMessage(`{"file":"TEAM","op":"add","new":"Lunch is at noon."}`)}}},
			{MatchUser: `(?i)when is our deploy day`, Text: []string{"Thursday."}},
			{Text: []string{"OK"}},
		},
	})
	team := func() dto.MemoryDoc {
		t.Helper()
		var d dto.MemoryDoc
		if code := e.JSON(t, http.MethodGet, "/api/memory/team", nil, &d); code != http.StatusOK || d.File != core.FileTeam {
			t.Fatalf("GET /api/memory/team = %d %+v", code, d)
		}
		return d
	}
	waitPending := func(what string) core.Approval {
		t.Helper()
		var aps []core.Approval
		chEventually(t, 20*time.Second, what, func() bool { aps = e.pending(t); return len(aps) == 1 })
		return aps[0]
	}

	sid := e.newSession(t)
	e.send(t, sid, "Please remember for the whole team: our deploy day is Thursday")
	ap := waitPending("the team memory card")
	if ap.Action == nil || ap.Action.Tool != "memory" || !ap.Approver.AdminOnly || ap.Card.Risk != core.SEAdmin ||
		ap.Card.Question != "Remember this for everyone?" || ap.Card.Consequence != "Bothy will use this in every chat, for everyone." ||
		chField(ap.Card, "Note").Value != "Our deploy day is Thursday." {
		t.Fatalf("approval = %+v", ap)
	}
	if d := team(); d.Content != "" {
		t.Fatalf("TEAM.md before the approval = %q", d.Content)
	}
	if code := e.JSON(t, http.MethodPost, "/api/approvals/"+string(ap.ID), dto.ApprovalResolveReq{Choice: core.AllowOnce}, nil); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	if runs := e.waitRuns(t, sid, 1); runs[0].Status != core.RunDone {
		t.Fatalf("run = %+v", runs[0])
	}
	if d := team(); d.Content != "Our deploy day is Thursday.\n" {
		t.Fatalf("TEAM.md after the approval = %q", d.Content)
	}
	chEventually(t, 10*time.Second, "the memory.write audit row", func() bool {
		var rows dto.List[dto.AuditRow]
		if e.JSON(t, http.MethodGet, "/api/audit?action=memory.write", nil, &rows) != http.StatusOK {
			return false
		}
		for _, r := range rows.Items {
			d := string(r.Detail)
			if r.Target == "memory:team/TEAM" && strings.Contains(d, `"scope":"team"`) && strings.Contains(d, `"approval_id":"`+string(ap.ID)+`"`) &&
				!strings.Contains(d, "Thursday") && r.Summary == "Remembered something for everyone" {
				return true
			}
		}
		return false
	})

	// A teammate in Telegram has it in every prompt.
	e.letInTelegram(t, chOwnerTG, "Olivia", true)
	e.letInTelegram(t, tom, "Tom", false)
	e.tg.Inject(telegramtest.Inbound{ChatID: tom, SenderID: tom, FirstName: "Tom", Text: "When is our deploy day?"})
	e.tgMessage(t, tom, "Tom's answer", func(m telegramtest.Message) bool { return strings.Contains(m.Text, "Thursday.") })
	var asked *chReq
	for _, r := range chAgentRequests(t, e) {
		if strings.Contains(r.lastUser(), "When is our deploy day?") {
			asked = &r
		}
	}
	if asked == nil {
		t.Fatal("no agent request for Tom's question")
	}
	if sys := asked.system(); !strings.Contains(sys, "<team-memory>") || !strings.Contains(sys, "Our deploy day is Thursday.\n</team-memory>") {
		t.Fatalf("Tom's system prompt lacks team memory:\n%s", sys)
	}

	// Tom's own proposal needs an admin: the chat approver only gets a link.
	e.tg.Inject(telegramtest.Inbound{ChatID: tom, SenderID: tom, FirstName: "Tom", Text: "Remember for everyone: lunch is at noon"})
	ap = waitPending("Tom's team memory card")
	if !ap.Approver.AdminOnly || ap.Approver.Team || chField(ap.Card, "Note").Value != "Lunch is at noon." {
		t.Fatalf("Tom's approval = %+v", ap)
	}
	e.tgMessage(t, tom, "the needs-an-admin line", func(m telegramtest.Message) bool { return m.Text == "This needs an admin's approval in Bothy." })
	link := e.tgMessage(t, chOwnerTG, "the approver's link card", func(m telegramtest.Message) bool {
		return strings.HasPrefix(m.Text, "A request from chat apps needs an admin.")
	})
	for _, row := range link.Buttons {
		for _, b := range row {
			if b.CallbackData != "" {
				t.Fatalf("the link card carries a decision button: %+v", link.Buttons)
			}
		}
	}
	if code := e.JSON(t, http.MethodPost, "/api/approvals/"+string(ap.ID), dto.ApprovalResolveReq{Choice: core.Deny}, nil); code != http.StatusOK {
		t.Fatalf("deny = %d", code)
	}
	e.tgMessage(t, tom, "the answer after the deny", func(m telegramtest.Message) bool { return strings.Contains(m.Text, "isn't saved") })
	if d := team(); d.Content != "Our deploy day is Thursday.\n" {
		t.Fatalf("TEAM.md after the deny = %q", d.Content)
	}
	var hist dto.List[dto.MemoryChange]
	if code := e.JSON(t, http.MethodGet, "/api/memory/team/history", nil, &hist); code != http.StatusOK || len(hist.Items) != 1 ||
		hist.Items[0].Approval == "" || hist.Items[0].Op != "add" {
		t.Fatalf("team history = %d %s", code, fmt.Sprint(hist.Items))
	}
}
