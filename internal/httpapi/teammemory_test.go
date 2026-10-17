package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// TestTeamMemoryEndpoints: everyone signed in reads what Bothy remembers
// for everyone; only admins change it, with the same etag rules as personal
// memory.
func TestTeamMemoryEndpoints(t *testing.T) {
	h := newHarness(t).setup()
	h.memory.Team = "Deploy day is Thursday\n"
	h.memory.TeamAdmins = []core.UserID{h.owner.user, h.admin.user}

	for _, c := range []*client{h.member, h.tokenFor(h.member, "read"), h.tokenFor(h.member, "chat")} {
		rec := h.do("GET", "/api/memory/team", nil, c)
		expect(t, rec, http.StatusOK)
		doc := decodeAs[dto.MemoryDoc](t, rec)
		if doc.File != core.FileTeam || doc.Content != "Deploy day is Thursday\n" || doc.CapBytes != 4096 || doc.ETag == "" ||
			rec.Header().Get("ETag") != `"`+doc.ETag+`"` {
			t.Fatalf("%s: doc = %+v", c.name, doc)
		}
	}
	expect(t, h.do("GET", "/api/memory/team", nil, nil), http.StatusUnauthorized)

	doc := decodeAs[dto.MemoryDoc](t, h.do("GET", "/api/memory/team", nil, h.admin))
	put := dto.MemoryPutReq{Content: "Deploy day is Friday\n"}
	for _, c := range []*client{h.member, h.tokenFor(h.member, "chat"), h.tokenFor(h.owner, "chat"), h.tokenFor(h.owner, "read")} {
		expect(t, h.do("PUT", "/api/memory/team", put, c, withHeader("If-Match", `"`+doc.ETag+`"`)), http.StatusForbidden)
	}
	expect(t, h.do("PUT", "/api/memory/team", put, h.admin), http.StatusPreconditionRequired)
	expect(t, h.do("PUT", "/api/memory/team", put, h.admin, withHeader("If-Match", `"stale"`)), http.StatusConflict)
	expect(t, h.do("PUT", "/api/memory/team", dto.MemoryPutReq{Content: strings.Repeat("x", 4097)}, h.admin, withHeader("If-Match", doc.ETag)),
		http.StatusBadRequest)
	if h.memory.Team != "Deploy day is Thursday\n" {
		t.Fatalf("a refused PUT changed TEAM.md: %q", h.memory.Team)
	}
	rec := h.do("PUT", "/api/memory/team", put, h.admin, withHeader("If-Match", `W/"`+doc.ETag+`"`))
	expect(t, rec, http.StatusOK)
	if d := decodeAs[dto.MemoryDoc](t, rec); d.Content != put.Content || d.ETag == doc.ETag || d.File != core.FileTeam {
		t.Fatalf("put = %+v", d)
	}
	// An admin token may edit, like other config writes.
	cur := decodeAs[dto.MemoryDoc](t, h.do("GET", "/api/memory/team", nil, h.member))
	expect(t, h.do("PUT", "/api/memory/team", dto.MemoryPutReq{Content: "x\n"}, h.tokenFor(h.owner, "admin"), withHeader("If-Match", cur.ETag)),
		http.StatusOK)

	hist := decodeAs[dto.List[dto.MemoryChange]](t, h.do("GET", "/api/memory/team/history", nil, h.member))
	if len(hist.Items) != 2 || hist.Items[1].Op != "edit" || hist.Items[1].ActorName != "admin" || hist.Items[1].File != core.FileTeam ||
		hist.Items[0].ActorName != "owner" {
		t.Fatalf("history = %+v", hist.Items)
	}

	// The personal routes never reach TEAM.md.
	for _, p := range []string{"/api/memory/TEAM", "/api/memory/TEAM.md", "/api/memory/Team/history"} {
		expect(t, h.do("GET", p, nil, h.admin), http.StatusNotFound)
	}
	expect(t, h.do("PUT", "/api/memory/TEAM.md", put, h.admin, withHeader("If-Match", "x")), http.StatusNotFound)
}

func TestTeamMemoryWords(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    phrase
	}{
		{`{"file":"TEAM","op":"add","new":"x"}`, phrase{"remember something for everyone", "remembering something for everyone", "Remembered something for everyone", "Couldn't save that for everyone"}},
		{`{"file":"TEAM","op":"replace","old":"x","new":"y"}`, phrase{"update a note for everyone", "updating a note for everyone", "Updated a note for everyone", "Couldn't update that note"}},
		{`{"file":"TEAM","op":"remove","old":"x"}`, phrase{"forget something for everyone", "forgetting something for everyone", "Forgot something for everyone", "Couldn't remove that note"}},
	} {
		if got := actionPhrase("memory", &core.Action{Tool: "memory", Payload: json.RawMessage(tc.payload)}); got != tc.want {
			t.Errorf("%s: %+v", tc.payload, got)
		}
	}

	h := newHarness(t).setup()
	an := &auditNamer{namer: h.srv.namer(context.Background(), core.SystemViewer()), approvals: map[core.ApprovalID]*core.Approval{},
		calls: map[core.RunID][]*core.ToolCallRecord{}}
	for detail, want := range map[string]string{
		`{"scope":"team","op":"add"}`:     "Remembered something for everyone",
		`{"scope":"team","op":"replace"}`: "Updated a note for everyone",
		`{"scope":"team","op":"remove"}`:  "Forgot something for everyone",
	} {
		e := &core.AuditEntry{Actor: "agent:run_x", Action: "memory.write", Target: "memory:team/TEAM", Outcome: "ok", Detail: json.RawMessage(detail)}
		if got := an.summary(e); got != want {
			t.Errorf("memory.write %s = %q, want %q", detail, got, want)
		}
	}
	e := &core.AuditEntry{Actor: "user:usr_x", Action: "memory.edit", Target: "memory:team/TEAM", Outcome: "ok", Detail: json.RawMessage(`{"scope":"team","op":"edit"}`)}
	if got := an.summary(e); got != "Edited what Bothy remembers for everyone" {
		t.Errorf("memory.edit = %q", got)
	}
}
