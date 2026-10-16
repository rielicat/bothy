package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// No viewer reaches a teammate's or a channel's chats, runs or memory: not
// even an admin, whose wider view covers only group chats, grants, team
// schedules and names.
func TestTeamChatsStayPrivate(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	f := seedTeam(t, h)
	now := time.Now()
	for _, p := range []core.PrincipalID{f.maya, f.eng} {
		sess := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: p, Origin: "slack", Title: "private",
			CreatedAt: now, UpdatedAt: now}
		if err := h.store.Sessions().Create(ctx, sess); err != nil {
			t.Fatal(err)
		}
		run := &core.Run{ID: core.RunID(core.NewID(core.PrefixRun)), Session: sess.ID, Principal: p, Trigger: core.TrigMessage,
			Status: core.RunDone, CreatedAt: now}
		if err := h.store.Runs().Create(ctx, run); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*client{h.owner, h.admin, h.member} {
			expect(t, h.do("GET", "/api/sessions/"+string(sess.ID), nil, c), http.StatusNotFound)
			expect(t, h.do("GET", "/api/sessions/"+string(sess.ID)+"/messages", nil, c), http.StatusNotFound)
			expect(t, h.do("GET", "/api/runs/"+string(run.ID), nil, c), http.StatusNotFound)
			if rec := h.do("GET", "/api/memory/MEMORY?principal="+string(p), nil, c); rec.Code == http.StatusOK {
				t.Fatalf("%s read a team principal's memory", c.name)
			}
		}
		list := decodeAs[dto.List[core.Session]](t, h.do("GET", "/api/sessions", nil, h.owner))
		for _, s := range list.Items {
			if s.Principal == p {
				t.Fatalf("an admin's chat list shows %s", s.ID)
			}
		}
	}
}
