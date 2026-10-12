package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

var t0 = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// budget scales a timing budget for the race detector.
func budget(d time.Duration) time.Duration {
	if raceEnabled {
		return 10 * d
	}
	return d
}

type world struct {
	t   *testing.T
	ctx context.Context
	db  *DB
	n   int
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db, _ := openTemp(t)
	return &world{t: t, ctx: context.Background(), db: db}
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) user(role core.Role) *core.User {
	w.t.Helper()
	w.n++
	u := &core.User{Username: fmt.Sprintf("user%d", w.n), Role: role, PWHash: "h", Timezone: "UTC", CreatedAt: t0, UpdatedAt: t0}
	w.must(w.db.Users().Create(w.ctx, u))
	return u
}

func (w *world) space(owner *core.User) *core.Space {
	w.t.Helper()
	w.n++
	s := &core.Space{Kind: "group", Chat: core.ChatRef{Channel: "telegram", ChatID: fmt.Sprintf("-%d", w.n), Kind: core.ChatGroup},
		Owner: owner.ID, CreatedAt: t0, UpdatedAt: t0}
	w.must(w.db.Spaces().Create(w.ctx, s, &core.Principal{}))
	return s
}

func (w *world) session(p core.PrincipalID, mod ...func(*core.Session)) *core.Session {
	w.t.Helper()
	w.n++
	s := &core.Session{Principal: p, Origin: "web", Title: fmt.Sprintf("session %d", w.n), CreatedAt: t0, UpdatedAt: t0, LastMessageAt: t0}
	for _, m := range mod {
		m(s)
	}
	w.must(w.db.Sessions().Create(w.ctx, s))
	return s
}

func (w *world) message(s core.SessionID, prov core.Provenance, text string) *core.Message {
	w.t.Helper()
	m := &core.Message{Session: s, Role: core.MsgUser, Kind: core.KindNormal, Provenance: prov,
		Parts: []core.Part{{Type: core.PartText, Text: text}}, CreatedAt: t0}
	w.must(w.db.Messages().Append(w.ctx, m))
	return m
}

func (w *world) run(s *core.Session, status core.RunStatus) *core.Run {
	w.t.Helper()
	r := &core.Run{Session: s.ID, Principal: s.Principal, User: core.UserID(s.Principal), Trigger: core.TrigMessage, Status: status, CreatedAt: t0}
	w.must(w.db.Runs().Create(w.ctx, r))
	return r
}

func viewerOf(u *core.User, extra ...core.PrincipalID) core.Viewer {
	return core.Viewer{User: u.ID, Principals: append([]core.PrincipalID{core.UserPrincipal(u.ID)}, extra...)}
}
