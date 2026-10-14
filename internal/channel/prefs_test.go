package channel

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
)

// A chat app the person chose (Settings → Account) wins over the one they
// used last, while it is connected.
func TestPreferredChatFollowsTheChoice(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.hub.Add(channeltest.New("matrix"))
		synctest.Wait()
		if _, err := h.store.Identities().Touch(h.ctx, &core.ChannelIdentity{Channel: "matrix", ExternalID: "@olive",
			Status: core.IdentityLinked, User: h.owner.ID, DMChat: "!dm", LastSeen: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if c, ok := h.hub.PreferredChat(h.ctx, h.owner.ID); !ok || c.Channel != "matrix" {
			t.Fatalf("PreferredChat = %+v %v; want the app used last", c, ok)
		}
		u, _ := h.store.Users().Get(h.ctx, h.owner.ID)
		u.PreferredChat = "telegram"
		if err := h.store.Users().Update(h.ctx, u); err != nil {
			t.Fatal(err)
		}
		if c, ok := h.hub.PreferredChat(h.ctx, h.owner.ID); !ok || c != h.dmChat(ownerChat) {
			t.Fatalf("PreferredChat = %+v %v; want the chosen app", c, ok)
		}
		u.PreferredChat = "slack" // linked nowhere live: the last used one stands in
		if err := h.store.Users().Update(h.ctx, u); err != nil {
			t.Fatal(err)
		}
		if c, ok := h.hub.PreferredChat(h.ctx, h.owner.ID); !ok || c.Channel != "matrix" {
			t.Fatalf("PreferredChat = %+v %v", c, ok)
		}
	})
}

// Someone who keeps approval requests in Bothy gets no card in a chat app,
// for single and multi-approver requests alike; a run started in their own
// DM still asks there.
func TestApprovalsWebOnlySendsNoCard(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		u, _ := h.store.Users().Get(h.ctx, h.owner.ID)
		u.ApprovalsWebOnly = true
		if err := h.store.Users().Update(h.ctx, u); err != nil {
			t.Fatal(err)
		}
		web := h.request(core.UserPrincipal(h.owner.ID), core.ApproverReq{User: h.owner.ID}, nil)
		if refs := h.refs(web.ID); len(refs) != 0 {
			t.Fatalf("web-origin card sent to a web-only approver: %+v", refs)
		}
		multi := h.request(core.UserPrincipal(h.owner.ID), core.ApproverReq{Roles: []core.Role{core.RoleOwner, core.RoleAdmin}, Required: 2}, nil)
		if refs := h.refs(multi.ID); len(refs) != 0 {
			t.Fatalf("vote card sent to a web-only approver: %+v", refs)
		}
		dm := h.dmChat(ownerChat)
		own := h.request(core.UserPrincipal(h.owner.ID), core.ApproverReq{User: h.owner.ID}, &dm)
		if refs := h.refs(own.ID); len(refs) != 1 || refs[0].Ref.Chat.ChatID != ownerChat {
			t.Fatalf("card for a run in the approver's own DM = %+v", refs)
		}
	})
}
