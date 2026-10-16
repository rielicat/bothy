package httpapi

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// TestInboxItems: GET /api/inbox puts every kind of decision in one list in
// plain copy, the caller's own first; admins also get team requests, MCP
// tools and skills to review, people waiting to be let in and new group
// chats.
func TestInboxItems(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	now := time.Now()
	h.approvals.Render = func(r core.ApprovalRequest) core.Card {
		c := h.approvals.RenderCard(r)
		switch r.Action.Tool {
		case "notes":
			c.Question, c.Kind = "Update the notes for acme/web?", core.CardReview
		case "release_deploy":
			c.Question, c.Kind = "Deploy acme/web to staging?", core.CardDeploy
		default:
			c.Question, c.Kind = "Delete notes/old.md?", core.CardAction
		}
		return c
	}
	request := func(p core.PrincipalID, tool string, approver core.ApproverReq) *core.Approval {
		ap, err := h.approvals.Request(ctx, core.ApprovalRequest{Run: core.RunID(core.NewID(core.PrefixRun)), Principal: p,
			Session: core.SessionID(core.NewID(core.PrefixSession)), Action: &core.Action{Tool: tool, Kind: core.ActGeneric, SideEffect: core.SELocalWrite},
			Decision: core.Decision{Verdict: core.VerdictAsk, Approver: approver, Choices: []core.Choice{core.AllowOnce, core.Deny}}})
		if err != nil {
			t.Fatal(err)
		}
		return ap
	}
	memberP := core.UserPrincipal(h.member.user)
	del := request(memberP, "fs_delete", core.ApproverReq{User: h.member.user})
	notes := request(memberP, "notes", core.ApproverReq{User: h.member.user})
	deploy := request(memberP, "release_deploy", core.ApproverReq{Roles: []core.Role{core.RoleAdmin}, Required: 2, Requester: h.member.user})
	person := &core.Space{Kind: core.SpacePerson, Chat: core.ChatRef{Channel: "slack", ChatID: "U9", Kind: core.ChatDM}, Title: "Ana",
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := h.store.Spaces().Create(ctx, person, &core.Principal{DisplayName: "Ana", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	team := request(person.Principal, "fs_delete", core.ApproverReq{Team: true})

	h.mcp.StatusV = []core.MCPStatus{{Name: "linear", State: "pins_pending", Pending: 2}, {Name: "calm", State: "ready"}}
	h.mcp.PinsV = map[string][]*core.MCPPin{"linear": {
		{Server: "linear", Tool: "create_issue", Status: core.PinPending, SideEffect: core.SEExternalSend, FirstSeen: now.Add(-time.Hour)},
		{Server: "linear", Tool: "list_issues", Status: core.PinApproved, SideEffect: core.SERead, FirstSeen: now.Add(-48 * time.Hour)},
		{Server: "linear", Tool: "delete_issue", Status: core.PinPending, SideEffect: core.SEIrreversible, FirstSeen: now.Add(-2 * time.Hour)},
		{Server: "linear", Tool: "delete_issue", Hash: "approved-before", Status: core.PinApproved, SideEffect: core.SEIrreversible,
			FirstSeen: now.Add(-72 * time.Hour)},
	}}
	h.skills.Metas = []core.SkillMeta{{Name: "pdf", Status: core.SkillChanged, UpdatedAt: now}, {Name: "csv", Status: core.SkillEnabled},
		{Name: "slides", Status: core.SkillPending, InstalledBy: h.admin.user, UpdatedAt: now}}

	ids := h.store.Identities()
	sender := func(channel, ext, name string, seen time.Time, dm bool, m core.MemberStatus, text string) {
		got, err := ids.Touch(ctx, &core.ChannelIdentity{Channel: channel, ExternalID: ext, DisplayName: name, Username: "u" + ext, Member: m,
			FirstSeen: seen, LastSeen: seen})
		if err != nil {
			t.Fatal(err)
		}
		if dm {
			got.DMChat = ext
		}
		got.FirstText = text
		if err := ids.Update(ctx, got); err != nil {
			t.Fatal(err)
		}
	}
	sender("telegram", "41", "Jordan K", now, true, core.MemberUnknown, "hi, is this the support bot?")
	sender("telegram", "42", "Group talker", now, false, core.MemberUnknown, "")
	sender("telegram", "43", "Long gone", now.Add(-40*24*time.Hour), true, core.MemberUnknown, "hello")
	sender("slack", "U7", "Sam Patel", now, true, core.MemberUnchecked, "")
	sender("slack", "U8", "Guest", now, true, core.MemberOutsider, "")

	group := func(chat string, enabled, reviewed bool) core.PrincipalID {
		at := now.Add(-time.Minute).Truncate(time.Millisecond)
		sp := &core.Space{Kind: core.SpaceGroup, Chat: core.ChatRef{Channel: "slack", ChatID: chat, Kind: core.ChatGroup}, Title: "#" + chat,
			AddedBy: "U7", Enabled: enabled, MentionOnly: true, CreatedAt: at, UpdatedAt: at}
		if reviewed {
			sp.ReviewedAt = at
		}
		if err := h.store.Spaces().Create(ctx, sp, &core.Principal{DisplayName: sp.Title, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
		return sp.Principal
	}
	fresh := group("eng", false, false)
	group("ops", true, true)
	group("old", false, true)

	byID := func(c *client) map[string]dto.InboxItem {
		rec := h.do(http.MethodGet, "/api/inbox", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("inbox for %s = %d %s", c.name, rec.Code, rec.Body)
		}
		inbox := decodeAs[dto.Inbox](t, rec)
		out := map[string]dto.InboxItem{}
		seenWaiting := false
		for _, it := range inbox.Items {
			out[it.ID] = it
			if !it.ForYou {
				seenWaiting = true
			} else if seenWaiting {
				t.Errorf("%s: an item for the caller after one waiting for others: %+v", c.name, inbox.Items)
			}
		}
		return out
	}

	admin := byID(h.admin)
	want := map[string]struct {
		typ      dto.InboxType
		question string
		summary  string
		forYou   bool
	}{
		string(deploy.ID):        {dto.InboxApproval, "Deploy acme/web to staging?", "0 of 2 approvals", true},
		"mcp:linear":             {dto.InboxMCPReview, "Review changes to Linear tools", "2 tools changed", true},
		"skill:pdf":              {dto.InboxSkillReview, "Review changes to the “pdf” skill", "Changed since it was approved", true},
		"skill:slides":           {dto.InboxSkillReview, "Review the new “slides” skill", "Added by admin", true},
		string(team.ID):          {dto.InboxApproval, "Delete notes/old.md?", "", true},
		"sender:telegram:41":     {dto.InboxUnknownSender, "Someone new messaged Bothy on Telegram", "Jordan K (@u41)", true},
		"group:" + string(fresh): {dto.InboxGroupChat, "Bothy was added to #eng in Slack", "Added by Sam Patel", true},
	}
	if len(admin) != len(want) {
		t.Errorf("admin inbox has %d items, want %d: %+v", len(admin), len(want), admin)
	}
	for id, w := range want {
		it, ok := admin[id]
		if !ok || it.Type != w.typ || it.Question != w.question || it.Summary != w.summary || it.ForYou != w.forYou {
			t.Errorf("admin item %s = %+v, want %+v", id, it, w)
		}
	}
	if it := admin["mcp:linear"]; len(it.MCP.Tools) != 3 || it.MCP.Tools[0].Tool != "create_issue" || it.MCP.Tools[2].Hash != "approved-before" ||
		!it.CreatedAt.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("mcp item = %+v", it.MCP)
	}
	if it := admin["sender:telegram:41"]; it.Sender == nil || it.Sender.FirstText != "hi, is this the support bot?" {
		t.Errorf("sender item = %+v", it.Sender)
	}

	member := byID(h.member)
	if len(member) != 3 {
		t.Errorf("member inbox = %+v; want their two approvals and the deploy they wait on", member)
	}
	if it := member[string(del.ID)]; it.Type != dto.InboxApproval || !it.ForYou || it.Question != "Delete notes/old.md?" || it.Approval == nil {
		t.Errorf("member's own approval = %+v", it)
	}
	if it := member[string(notes.ID)]; it.Type != dto.InboxNoteSuggestion || !it.ForYou {
		t.Errorf("note suggestion = %+v", it)
	}
	if it := member[string(deploy.ID)]; it.ForYou || it.Summary != "0 of 2 approvals" {
		t.Errorf("the requester's deploy = %+v; they wait for others", it)
	}
	if it := admin["group:"+string(fresh)]; it.Group == nil || it.Group.AddedBy != "Sam Patel" || !it.Group.Space.MentionOnly {
		t.Errorf("the new group = %+v", it)
	}
	for id := range member {
		if slices.Contains([]string{"mcp:linear", "skill:pdf", "sender:telegram:41", "group:" + string(fresh), string(team.ID)}, id) {
			t.Errorf("a member sees the admin item %s", id)
		}
	}

	if rec := h.do(http.MethodPatch, "/api/spaces/"+string(fresh), dto.SpacePatchReq{Enabled: new(false)}, h.member); rec.Code != http.StatusForbidden {
		t.Fatalf("a member changed a group = %d %s", rec.Code, rec.Body)
	}
	// A title the platform fills in later doesn't make the group reviewed.
	sp, _ := h.store.Spaces().Get(ctx, core.SystemViewer(), fresh)
	sp.Title, sp.UpdatedAt = "#engineering", now
	if err := h.store.Spaces().Update(ctx, sp); err != nil {
		t.Fatal(err)
	}
	if _, ok := byID(h.admin)["group:"+string(fresh)]; !ok {
		t.Error("a title backfill dropped the new group")
	}
	if rec := h.do(http.MethodPatch, "/api/spaces/"+string(fresh), dto.SpacePatchReq{Enabled: new(false)}, h.admin); rec.Code != http.StatusOK {
		t.Fatalf("ignore group = %d %s", rec.Code, rec.Body)
	}
	if _, ok := byID(h.admin)["group:"+string(fresh)]; ok {
		t.Error("a group chat an admin turned off is still new")
	}

	// A teammate a member check let in whom Discord can't vouch for waits
	// for an admin too.
	sender("discord", "1100000000000000099", "Dee", now, true, core.MemberUnknown, "")
	dee, _ := ids.Get(ctx, "discord", "1100000000000000099")
	dee.Status, dee.Via, dee.Principal = core.IdentityTeammate, core.ViaMembership, person.Principal
	if err := ids.Update(ctx, dee); err != nil {
		t.Fatal(err)
	}
	if _, ok := byID(h.admin)["sender:discord:1100000000000000099"]; !ok {
		t.Error("a membership teammate Discord can't vouch for isn't listed")
	}
}
