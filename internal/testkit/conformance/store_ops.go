package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// auditRow has exported fields so diff can compare the times inside it.
type auditRow struct {
	E          core.AuditEntry
	Prev, Hash []byte
}

func testAudit(f *fx) {
	r := f.s.Audit()
	id, hash, err := r.Head(f.ctx)
	f.must(err, "Head of an empty log")
	f.check(id == 0 && hash == nil, "Head of an empty log = (%d, %x)", id, hash)

	genesis := make([]byte, 32)
	entries := []core.AuditEntry{
		{At: at(1), Actor: "user:usr_x", Action: "auth.login", Target: "usr_x", Outcome: "ok", IP: "203.0.113.9", Detail: json.RawMessage(`{"method":"password"}`)},
		{At: at(2), Actor: "system", Action: "config.update", Outcome: "error", Session: "ses_x", Run: "run_x", Detail: json.RawMessage(`{"keys":["a","b"]}`)},
		{At: at(3), Actor: "user:usr_x", Action: "auth.logout", Outcome: "denied"},
	}
	var rows []auditRow
	prev := genesis
	for i := range entries {
		e := entries[i]
		h := sum(string(prev) + e.Action)
		id, err := r.Append(f.ctx, &e, prev, h)
		f.must(err, "Append")
		f.check(len(rows) == 0 || id > rows[len(rows)-1].E.ID, "Append ids not increasing: %d", id)
		e.ID = id
		if e.Detail == nil {
			e.Detail = json.RawMessage(`{}`)
		}
		rows = append(rows, auditRow{E: e, Prev: prev, Hash: h})
		prev = h
	}

	forged := core.AuditEntry{ID: rows[0].E.ID, At: at(4), Actor: "agent:run_x", Action: "tool.exec", Outcome: "ok", Detail: json.RawMessage(`{"tool":"shell"}`)}
	h := sum("forged")
	id, err = r.Append(f.ctx, &forged, prev, h)
	f.must(err, "Append with a preset id")
	f.check(id > rows[len(rows)-1].E.ID, "Append reused id %d: the log is append-only", id)
	forged.ID = id
	rows = append(rows, auditRow{E: forged, Prev: prev, Hash: h})

	id, hash, err = r.Head(f.ctx)
	f.must(err, "Head")
	f.check(id == forged.ID && bytes.Equal(hash, h), "Head = (%d, %x), want (%d, %x)", id, hash, forged.ID, h)

	collect := func(from, to int64) []auditRow {
		f.t.Helper()
		var out []auditRow
		f.must(r.Range(f.ctx, from, to, func(e *core.AuditEntry, prevHash, hash []byte) error {
			out = append(out, auditRow{E: *e, Prev: prevHash, Hash: hash})
			return nil
		}), "Range")
		return out
	}
	f.eq("Range of the whole log, in id order", collect(0, 0), rows)
	f.eq("Range with inclusive bounds", collect(rows[1].E.ID, rows[2].E.ID), rows[1:3])
	f.eq("Range to the end", collect(rows[1].E.ID, 0), rows[1:])
	stop := errors.New("stop")
	visited := 0
	err = r.Range(f.ctx, 0, 0, func(*core.AuditEntry, []byte, []byte) error {
		visited++
		if visited == 2 {
			return stop
		}
		return nil
	})
	f.want(err, stop, "Range returns fn's error")
	f.check(visited == 2, "Range kept visiting after fn failed: %d calls", visited)

	query := func(flt core.AuditFilter) []int64 {
		f.t.Helper()
		es, err := r.Query(f.ctx, flt)
		f.must(err, "Query")
		ids := make([]int64, len(es))
		for i, e := range es {
			ids[i] = e.ID
		}
		return ids
	}
	ids := func(idx ...int) []int64 {
		out := make([]int64, len(idx))
		for i, x := range idx {
			out[i] = rows[x].E.ID
		}
		return out
	}
	f.eq("Query newest first", query(core.AuditFilter{}), ids(3, 2, 1, 0))
	f.eq("Query by actor", query(core.AuditFilter{Actor: "user:usr_x"}), ids(2, 0))
	f.eq("Query by action prefix", query(core.AuditFilter{ActionPrefix: "auth."}), ids(2, 0))
	f.eq("Query in [From, To)", query(core.AuditFilter{From: at(2), To: at(3)}), ids(1))
	f.eq("Query paging", query(core.AuditFilter{Page: core.Page{Before: rows[2].E.ID, Limit: 1}}), ids(1))
	es, err := r.Query(f.ctx, core.AuditFilter{Actor: "system"})
	f.must(err, "Query")
	f.eq("Query round trip", es, []*core.AuditEntry{&rows[1].E})

	_, err = r.Append(f.ctx, &core.AuditEntry{At: at(5), Actor: "system", Action: "x", Outcome: "maybe"}, h, h)
	f.want(err, core.ErrInvalid, "outcome CHECK")
	_, err = r.Append(f.ctx, &core.AuditEntry{At: at(5), Actor: "system", Action: "x", Outcome: "ok", Detail: json.RawMessage(`{"a":`)}, h, h)
	f.want(err, core.ErrInvalid, "detail json_valid CHECK")
	id, _, err = r.Head(f.ctx)
	f.must(err, "Head")
	f.check(id == forged.ID, "rejected appends moved Head to %d", id)
}

func egressIDs(rs []*core.EgressRecord) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func testEgress(f *fx) {
	r := f.s.Egress()
	alice := f.user(core.RoleMember)
	bob := f.user(core.RoleMember)
	pa, pb := core.UserPrincipal(alice.ID), core.UserPrincipal(bob.ID)
	sa, sb := f.session(pa), f.session(pb)

	recs := []core.EgressRecord{
		{At: at(1), Dialer: "box", Session: sa.ID, Principal: pa, Run: "run_a", Host: "pypi.org", Port: 443, IP: "151.101.0.223", Decision: "allow", Up: 10, Down: 2000, Duration: 1500 * time.Millisecond},
		{At: at(2), Dialer: "public", Principal: pa, Host: "example.com", Port: 443, Decision: "deny", Reason: "private address"},
		{At: at(3), Dialer: "system", Dest: "anthropic", Host: "api.anthropic.com", Port: 443, Decision: "allow"},
		{At: at(4), Dialer: "box", Session: sb.ID, Principal: pb, Host: "github.com", Port: 443, Decision: "held_allow"},
	}
	f.must(r.Add(f.ctx, recs), "Add")
	all, err := r.Query(f.ctx, core.SystemViewer(), "", core.Page{})
	f.must(err, "Query with SystemViewer")
	f.check(len(all) == 4, "Query with SystemViewer returned %d rows, want 4", len(all))
	if len(all) == 4 {
		for i := range recs {
			recs[i].ID = all[3-i].ID
		}
		f.check(all[3].ID > 0 && all[3].ID < all[2].ID && all[2].ID < all[1].ID && all[1].ID < all[0].ID, "Query is not newest first by id: %v", egressIDs(all))
		f.eq("Query round trip", all, []*core.EgressRecord{&recs[3], &recs[2], &recs[1], &recs[0]})
	}

	query := func(v core.Viewer, s core.SessionID, p core.Page) []int64 {
		f.t.Helper()
		rs, err := r.Query(f.ctx, v, s, p)
		f.must(err, "Query")
		return egressIDs(rs)
	}
	va := viewer(alice)
	vaAdmin := viewer(alice)
	vaAdmin.Admin = true
	f.eq("Query of the viewer's principals", query(va, "", core.Page{}), []int64{recs[1].ID, recs[0].ID})
	f.eq("Query of one session", query(va, sa.ID, core.Page{}), []int64{recs[0].ID})
	f.eq("Query for an admin adds system-dialer rows", query(vaAdmin, "", core.Page{}), []int64{recs[2].ID, recs[1].ID, recs[0].ID})
	f.eq("Query of a foreign session", query(viewer(bob), sa.ID, core.Page{}), []int64{})
	f.eq("Query first page", query(core.SystemViewer(), "", core.Page{Limit: 2}), []int64{recs[3].ID, recs[2].ID})
	f.eq("Query next page", query(core.SystemViewer(), "", core.Page{Before: recs[2].ID}), []int64{recs[1].ID, recs[0].ID})

	ok := core.EgressRecord{At: at(5), Dialer: "box", Principal: pa, Host: "x.example", Port: 443, Decision: "allow"}
	badDialer := ok
	badDialer.Dialer = "proxy"
	f.want(r.Add(f.ctx, []core.EgressRecord{ok, badDialer}), core.ErrInvalid, "dialer CHECK")
	badDecision := ok
	badDecision.Decision = "maybe"
	f.want(r.Add(f.ctx, []core.EgressRecord{badDecision}), core.ErrInvalid, "decision CHECK")
	f.check(len(query(core.SystemViewer(), "", core.Page{})) == 4, "a rejected batch was partly inserted")
	f.must(r.Add(f.ctx, nil), "Add of an empty batch")

	n, err := r.Prune(f.ctx, at(3))
	f.must(err, "Prune")
	f.check(n == 2, "Prune removed %d rows, want 2", n)
	f.eq("Query after Prune", query(core.SystemViewer(), "", core.Page{}), []int64{recs[3].ID, recs[2].ID})
}

func testMCP(f *fx) {
	r := f.s.MCP()
	admin := f.user(core.RoleAdmin)
	def1 := json.RawMessage(`{"name":"send","description":"Send mail","inputSchema":{"type":"object"}}`)
	def2 := json.RawMessage(`{"name":"send","description":"Send mail now","inputSchema":{"type":"object"}}`)

	p1 := &core.MCPPin{Server: "mail", Tool: "send", Hash: "h1", Definition: def1, SideEffect: core.SEExternalSend, FirstSeen: at(1)}
	st, err := r.UpsertPin(f.ctx, p1)
	f.must(err, "UpsertPin")
	f.check(st == core.PinPending, "UpsertPin of an unseen hash = %s, want pending", st)
	st, err = r.UpsertPin(f.ctx, p1)
	f.must(err, "UpsertPin again")
	f.check(st == core.PinPending, "UpsertPin of a pending hash = %s, want pending", st)
	pins, err := r.Pins(f.ctx, "mail")
	f.must(err, "Pins")
	want1 := *p1
	want1.Status = core.PinPending
	f.eq("Pins round trip", pins, []*core.MCPPin{&want1})
	approved, err := r.Approved(f.ctx, "mail")
	f.must(err, "Approved")
	f.check(len(approved) == 0, "Approved before approval returned %d pins", len(approved))

	approval := core.MCPPin{Hash: "h1", SideEffect: core.SEExternalSend, FieldMap: map[string]string{"recipient": "/to"}, Private: true}
	f.must(r.ApprovePins(f.ctx, "mail", map[string]core.MCPPin{"send": approval}, admin.ID, at(5)), "ApprovePins")
	st, err = r.UpsertPin(f.ctx, p1)
	f.must(err, "UpsertPin")
	f.check(st == core.PinApproved, "UpsertPin of the approved hash = %s, want approved", st)
	approved, err = r.Approved(f.ctx, "mail")
	f.must(err, "Approved")
	want1.Status, want1.FieldMap, want1.Private, want1.ApprovedBy, want1.ApprovedAt = core.PinApproved, approval.FieldMap, true, admin.ID, at(5)
	f.eq("Approved round trip", approved, map[string]*core.MCPPin{"send": &want1})

	p2 := &core.MCPPin{Server: "mail", Tool: "send", Hash: "h2", Definition: def2, FirstSeen: at(10)}
	st, err = r.UpsertPin(f.ctx, p2)
	f.must(err, "UpsertPin of a changed definition")
	f.check(st == core.PinPending, "UpsertPin of a changed definition = %s, want pending", st)
	pins, err = r.Pins(f.ctx, "mail")
	f.must(err, "Pins")
	want2 := *p2
	want2.Status, want2.SideEffect = core.PinPending, core.SEExternalSend
	f.eq("Pins shows the latest definition per tool (side_effect defaults to external_send)", pins, []*core.MCPPin{&want2})
	approved, err = r.Approved(f.ctx, "mail")
	f.must(err, "Approved")
	f.check(len(approved) == 1 && approved["send"].Hash == "h1", "a pending change replaced the approved pin: %v", approved)

	f.must(r.ApprovePins(f.ctx, "mail", map[string]core.MCPPin{"send": {Hash: "h2", SideEffect: core.SEIrreversible}}, admin.ID, at(11)), "ApprovePins h2")
	approved, err = r.Approved(f.ctx, "mail")
	f.must(err, "Approved")
	f.check(len(approved) == 1 && approved["send"].Hash == "h2" && approved["send"].SideEffect == core.SEIrreversible,
		"Approved after re-approval = %v", approved)
	st, err = r.UpsertPin(f.ctx, p1)
	f.must(err, "UpsertPin of the superseded hash")
	f.check(st != core.PinApproved, "a superseded definition came back as approved")

	f.want(r.ApprovePins(f.ctx, "mail", map[string]core.MCPPin{"send": {Hash: "h9"}}, admin.ID, at(12)), core.ErrNotFound, "ApprovePins of an unknown hash")
	approved, err = r.Approved(f.ctx, "mail")
	f.must(err, "Approved")
	f.check(approved["send"] != nil && approved["send"].Hash == "h2", "a failed ApprovePins changed the approved pin")

	list := &core.MCPPin{Server: "mail", Tool: "list", Hash: "l1", Definition: json.RawMessage(`{"name":"list"}`), SideEffect: core.SERead, FirstSeen: at(13)}
	_, err = r.UpsertPin(f.ctx, list)
	f.must(err, "UpsertPin second tool")
	pins, err = r.Pins(f.ctx, "mail")
	f.must(err, "Pins")
	f.check(len(pins) == 2, "Pins returned %d tools, want 2", len(pins))
	pins, err = r.Pins(f.ctx, "calendar")
	f.must(err, "Pins of another server")
	f.check(len(pins) == 0, "Pins leaked across servers")

	f.must(r.DisableTool(f.ctx, "mail", "send"), "DisableTool")
	approved, err = r.Approved(f.ctx, "mail")
	f.must(err, "Approved")
	f.check(approved["send"] == nil, "a disabled tool is still approved")
	st, err = r.UpsertPin(f.ctx, p2)
	f.must(err, "UpsertPin")
	f.check(st == core.PinDisabled, "UpsertPin of a disabled tool = %s, want disabled", st)
	f.want(r.DisableTool(f.ctx, "mail", "nope"), core.ErrNotFound, "DisableTool missing")
	_, err = r.UpsertPin(f.ctx, &core.MCPPin{Server: "mail", Tool: "x", Hash: "x1", Definition: json.RawMessage(`{"name":`), FirstSeen: at(14)})
	f.want(err, core.ErrInvalid, "definition json_valid CHECK")
	_, err = r.UpsertPin(f.ctx, &core.MCPPin{Server: "mail", Tool: "x", Hash: "x2", FirstSeen: at(14)})
	f.want(err, core.ErrInvalid, "definition NOT NULL")

	in1 := &core.MCPInstall{Server: "mail", Version: "1.0.0", LockHash: "lh", Lock: []byte(`{"lockfileVersion":3}`), TreeHash: "th", Status: "ready", CreatedAt: at(1)}
	f.must(r.PutInstall(f.ctx, in1), "PutInstall")
	f.check(core.ValidID(core.PrefixInstall, in1.InstallID), "PutInstall assigned id %q, want an in_ id", in1.InstallID)
	install := func(server string) *core.MCPInstall {
		f.t.Helper()
		in, err := r.Install(f.ctx, server)
		f.must(err, "Install "+server)
		return in
	}
	f.eq("Install round trip", install("mail"), in1)
	f.must(r.PutInstall(f.ctx, &core.MCPInstall{Server: "mail", Version: "1.1.0", Status: "installing", CreatedAt: at(2)}), "PutInstall")
	f.eq("Install prefers the latest ready install", install("mail"), in1)
	in3 := &core.MCPInstall{Server: "mail", Version: "1.2.0", Status: "ready", CreatedAt: at(3)}
	f.must(r.PutInstall(f.ctx, in3), "PutInstall")
	f.eq("Install picks the newest ready install", install("mail"), in3)
	in3.Status, in3.Error = "drifted", "tree hash changed"
	f.must(r.PutInstall(f.ctx, in3), "PutInstall updates by install_id")
	f.eq("Install after drift", install("mail"), in1)
	failed := &core.MCPInstall{Server: "fresh", Version: "0.1.0", Status: "failed", Error: "npm ci failed", CreatedAt: at(4)}
	f.must(r.PutInstall(f.ctx, failed), "PutInstall")
	f.eq("Install falls back to the latest install", install("fresh"), failed)
	_, err = r.Install(f.ctx, "none")
	f.want(err, core.ErrNotFound, "Install missing")
	f.want(r.PutInstall(f.ctx, &core.MCPInstall{Server: "mail", Version: "x", Status: "done", CreatedAt: at(5)}), core.ErrInvalid, "status CHECK")
}

func testSkills(f *fx) {
	r := f.s.Skills()
	u := f.user(core.RoleAdmin)
	pdf := &core.SkillMeta{Name: "pdf", Description: "Read PDFs", Path: "skills/pdf", Hash: "h1", Status: core.SkillPending, Tokens: 120,
		InstalledBy: u.ID, InstalledAt: at(1), UpdatedAt: at(1)}
	f.must(r.Upsert(f.ctx, pdf), "Upsert")
	got, err := r.Get(f.ctx, "pdf")
	f.must(err, "Get")
	f.eq("Get round trip", got, pdf)
	pdf.Status, pdf.ApprovedHash, pdf.ApprovedBy, pdf.UpdatedAt = core.SkillEnabled, "h1", u.ID, at(2)
	f.must(r.Upsert(f.ctx, pdf), "Upsert existing")
	got, err = r.Get(f.ctx, "pdf")
	f.must(err, "Get")
	f.eq("Upsert update round trip", got, pdf)
	csv := &core.SkillMeta{Name: "csv", Description: "CSV", Path: "skills/csv", Hash: "h2", Status: core.SkillChanged, InstalledAt: at(3), UpdatedAt: at(3)}
	f.must(r.Upsert(f.ctx, csv), "Upsert")
	list, err := r.List(f.ctx)
	f.must(err, "List")
	f.eq("List", sortBy(list, func(s *core.SkillMeta) string { return s.Name }), []*core.SkillMeta{csv, pdf})
	f.want(r.Upsert(f.ctx, &core.SkillMeta{Name: "x", Status: "on"}), core.ErrInvalid, "status CHECK")

	files, err := r.Approved(f.ctx, "pdf")
	f.must(err, "Approved before a snapshot")
	f.check(len(files) == 0, "Approved before a snapshot = %+v", files)
	snap := []core.SkillFile{
		{Path: "SKILL.md", SHA256: "aa", Size: 12, Text: true, Content: "Read PDFs.\n"},
		{Path: "bin/tool", SHA256: "bb", Size: 9000},
		{Path: "empty.md", SHA256: "cc", Size: 0, Text: true, Content: ""},
	}
	f.must(r.SetApproved(f.ctx, "pdf", snap), "SetApproved")
	files, err = r.Approved(f.ctx, "pdf")
	f.must(err, "Approved")
	f.eq("Approved round trip", files, snap)
	f.must(r.SetApproved(f.ctx, "pdf", snap[:1]), "SetApproved again")
	files, err = r.Approved(f.ctx, "pdf")
	f.must(err, "Approved")
	f.eq("SetApproved replaces the snapshot", files, snap[:1])
	f.want(r.SetApproved(f.ctx, "nope", snap), core.ErrNotFound, "SetApproved of an unknown skill")
	pdf.Status, pdf.UpdatedAt = core.SkillChanged, at(4)
	f.must(r.Upsert(f.ctx, pdf), "Upsert keeps the snapshot")
	files, err = r.Approved(f.ctx, "pdf")
	f.must(err, "Approved after Upsert")
	f.check(len(files) == 1, "Upsert dropped the snapshot: %+v", files)
	f.must(r.Delete(f.ctx, "pdf"), "Delete")
	files, err = r.Approved(f.ctx, "pdf")
	f.must(err, "Approved after Delete")
	f.check(len(files) == 0, "Delete kept the snapshot: %+v", files)
	f.must(r.Upsert(f.ctx, pdf), "Upsert again")
	f.must(r.Delete(f.ctx, "pdf"), "Delete")
	_, err = r.Get(f.ctx, "pdf")
	f.want(err, core.ErrNotFound, "Get after Delete")
	f.want(r.Delete(f.ctx, "pdf"), core.ErrNotFound, "Delete missing")
}

func testMemoryEvents(f *fx) {
	r := f.s.MemoryEvents()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)

	evs := []*core.MemoryEvent{
		{Principal: pu, File: core.FileMemory, Op: "add", After: "likes tea", Run: "run_x", Actor: "agent:run_x", Provenance: core.ProvUser, At: at(1)},
		{Principal: pu, File: core.FileUser, Op: "replace", Before: "tz UTC", After: "tz Europe/London", Actor: "user:" + string(u.ID), Provenance: core.ProvUser, At: at(2)},
		{Principal: pu, File: core.FileMemory, Op: "remove", Before: "likes tea", Actor: "agent:run_y", Run: "run_y", Provenance: core.ProvUntrusted, At: at(3)},
	}
	var prev int64
	for _, e := range evs {
		id, err := r.Add(f.ctx, e)
		f.must(err, "Add")
		f.check(id > prev, "Add returned id %d", id)
		e.ID, prev = id, id
	}
	f.must(func() error {
		_, err := r.Add(f.ctx, &core.MemoryEvent{Principal: core.UserPrincipal(other.ID), File: core.FileMemory, Op: "add", Actor: "x", At: at(4)})
		return err
	}(), "Add for another principal")

	since, err := r.Since(f.ctx, pu, 0)
	f.must(err, "Since")
	f.eq("Since(0)", since, evs)
	since, err = r.Since(f.ctx, pu, evs[0].ID)
	f.must(err, "Since")
	f.eq("Since(first)", since, evs[1:])
	list := func(file core.MemoryFile, p core.Page) []*core.MemoryEvent {
		f.t.Helper()
		out, err := r.List(f.ctx, v, pu, file, p)
		f.must(err, "List")
		return out
	}
	f.eq("List newest first", list("", core.Page{}), []*core.MemoryEvent{evs[2], evs[1], evs[0]})
	f.eq("List one file", list(core.FileMemory, core.Page{}), []*core.MemoryEvent{evs[2], evs[0]})
	f.eq("List paging", list("", core.Page{Before: evs[2].ID, Limit: 1}), []*core.MemoryEvent{evs[1]})
	_, err = r.List(f.ctx, viewer(other), pu, "", core.Page{})
	f.want(err, core.ErrNotFound, "List with a foreign viewer")

	_, err = r.Add(f.ctx, &core.MemoryEvent{Principal: pu, File: "SOUL", Op: "add", Actor: "x"})
	f.want(err, core.ErrInvalid, "file CHECK")
	_, err = r.Add(f.ctx, &core.MemoryEvent{Principal: pu, File: core.FileMemory, Op: "append", Actor: "x"})
	f.want(err, core.ErrInvalid, "op CHECK")
	_, err = r.Add(f.ctx, &core.MemoryEvent{Principal: core.PrincipalID(core.NewID(core.PrefixSpace)), File: core.FileMemory, Op: "add", Actor: "x"})
	f.want(err, core.ErrInvalid, "principal_id foreign key")
}

func testTeamMemoryEvents(f *fx) {
	r := f.s.TeamMemoryEvents()
	evs := []*core.MemoryEvent{
		{File: core.FileTeam, Op: "add", After: "deploy day is Thursday\n", Run: "run_x", Approval: "apr_x", Actor: "agent:run_x",
			Provenance: core.ProvUser, At: at(1)},
		{File: core.FileTeam, Op: "edit", Before: "deploy day is Thursday\n", After: "deploy day is Friday\n", Actor: "user:usr_x",
			Provenance: core.ProvUser, At: at(2)},
		{File: core.FileTeam, Op: "remove", Before: "deploy day is Friday\n", Run: "run_y", Approval: "apr_y", Actor: "agent:run_y",
			Provenance: core.ProvUntrusted, At: at(3)},
	}
	var prev int64
	for _, e := range evs {
		id, err := r.Add(f.ctx, e)
		f.must(err, "Add")
		f.check(id > prev, "Add returned id %d", id)
		e.ID, prev = id, id
	}
	list := func(p core.Page) []*core.MemoryEvent {
		f.t.Helper()
		out, err := r.List(f.ctx, p)
		f.must(err, "List")
		return out
	}
	f.eq("List newest first", list(core.Page{}), []*core.MemoryEvent{evs[2], evs[1], evs[0]})
	f.eq("List paging", list(core.Page{Before: evs[2].ID, Limit: 1}), []*core.MemoryEvent{evs[1]})
	f.eq("List limit", list(core.Page{Limit: 2}), []*core.MemoryEvent{evs[2], evs[1]})

	_, err := r.Add(f.ctx, &core.MemoryEvent{File: core.FileMemory, Op: "add", Actor: "x"})
	f.want(err, core.ErrInvalid, "a personal file")
	_, err = r.Add(f.ctx, &core.MemoryEvent{Principal: core.UserPrincipal("usr_x"), File: core.FileTeam, Op: "add", Actor: "x"})
	f.want(err, core.ErrInvalid, "a principal's row")
	_, err = r.Add(f.ctx, &core.MemoryEvent{File: core.FileTeam, Op: "append", Actor: "x"})
	f.want(err, core.ErrInvalid, "op CHECK")
	_, err = r.Add(f.ctx, &core.MemoryEvent{File: core.FileTeam, Op: "add", After: "x\n", Run: "run_z", Approval: "apr_x", Actor: "agent:run_z",
		Provenance: core.ProvUser, At: at(4)})
	f.want(err, core.ErrConflict, "an approval used twice")
	_, err = r.Add(f.ctx, &core.MemoryEvent{File: core.FileTeam, Op: "edit", After: "x\n", Actor: "user:usr_x", Provenance: core.ProvUser, At: at(5)})
	f.must(err, "a second edit without an approval")
}

func testVault(f *fx) {
	r := f.s.Vault()
	u := f.user(core.RoleOwner)
	_, _, _, _, err := r.ActiveKey(f.ctx)
	f.want(err, core.ErrNotFound, "ActiveKey on first boot")
	k1, err := r.PutKey(f.ctx, []byte("wrapped-1"), []byte("check-1"), "file")
	f.must(err, "PutKey")
	k2, err := r.PutKey(f.ctx, []byte("wrapped-2"), []byte("check-2"), "env")
	f.must(err, "PutKey")
	f.check(k1 != k2, "PutKey reused id %d", k1)
	id, wrapped, check, source, err := r.ActiveKey(f.ctx)
	f.must(err, "ActiveKey")
	f.check(id == k2 && string(wrapped) == "wrapped-2" && string(check) == "check-2" && source == "env",
		"ActiveKey = (%d, %s, %s, %s), want the latest key %d", id, wrapped, check, source, k2)

	us := core.UserScope(u.ID)
	f.must(r.PutSecret(f.ctx, core.GlobalScope, "openai_key", k2, 1, []byte("n1"), []byte("c1"), "abcd", u.ID), "PutSecret")
	f.must(r.PutSecret(f.ctx, us, "openai_key", k2, 1, []byte("n2"), []byte("c2"), "wxyz", u.ID), "PutSecret user scope")
	f.must(r.PutSecret(f.ctx, core.GlobalScope, "openai_key", k2, 2, []byte("n3"), []byte("c3"), "efgh", u.ID), "PutSecret replace")
	secret := func(sc core.Scope, name string) (int64, int, string, string) {
		f.t.Helper()
		keyID, version, nonce, ct, err := r.GetSecret(f.ctx, sc, name)
		f.must(err, "GetSecret")
		return keyID, version, string(nonce), string(ct)
	}
	keyID, version, nonce, ct := secret(core.GlobalScope, "openai_key")
	f.check(keyID == k2 && version == 2 && nonce == "n3" && ct == "c3", "GetSecret = (%d, %d, %s, %s)", keyID, version, nonce, ct)
	keyID, version, nonce, ct = secret(us, "openai_key")
	f.check(keyID == k2 && version == 1 && nonce == "n2" && ct == "c2", "GetSecret of the user scope = (%d, %d, %s, %s)", keyID, version, nonce, ct)
	_, _, _, _, err = r.GetSecret(f.ctx, core.GlobalScope, "missing")
	f.want(err, core.ErrNotFound, "GetSecret missing")

	f.must(r.TouchSecret(f.ctx, core.GlobalScope, "openai_key", at(9)), "TouchSecret")
	f.want(r.TouchSecret(f.ctx, core.GlobalScope, "missing", at(9)), core.ErrNotFound, "TouchSecret missing")
	metas, err := r.ListSecrets(f.ctx, core.GlobalScope)
	f.must(err, "ListSecrets")
	f.check(len(metas) == 1, "ListSecrets returned %d secrets, want 1", len(metas))
	if len(metas) == 1 {
		m := metas[0]
		f.check(m.Scope == core.GlobalScope && m.Name == "openai_key" && m.Source == "vault" && m.Hint == "efgh" &&
			m.LastUsed.Equal(at(9)) && !m.UpdatedAt.IsZero(), "ListSecrets = %+v", m)
	}
	metas, err = r.ListSecrets(f.ctx, core.UserScope("usr_nobody"))
	f.must(err, "ListSecrets")
	f.check(len(metas) == 0, "ListSecrets leaked across scopes")

	for _, name := range []string{"", "UPPER", "has/slash", "has space", strings.Repeat("a", 65)} {
		f.want(r.PutSecret(f.ctx, core.GlobalScope, name, k2, 1, []byte("n"), []byte("c"), "", u.ID), core.ErrInvalid, "name CHECK "+name)
	}
	f.must(r.PutSecret(f.ctx, core.GlobalScope, strings.Repeat("a", 64), k2, 1, []byte("n"), []byte("c"), "", u.ID), "64-character name")
	f.must(r.PutSecret(f.ctx, core.GlobalScope, "a.b-c_9", k2, 1, []byte("n"), []byte("c"), "", u.ID), "name with . - _ and digits")
	f.want(r.PutSecret(f.ctx, core.GlobalScope, "orphan", k2+100, 1, []byte("n"), []byte("c"), "", u.ID), core.ErrInvalid, "key_id foreign key")

	names := []struct {
		sc   core.Scope
		name string
	}{{core.GlobalScope, "openai_key"}, {us, "openai_key"}, {core.GlobalScope, strings.Repeat("a", 64)}, {core.GlobalScope, "a.b-c_9"}}
	snapshot := func() []string {
		f.t.Helper()
		var out []string
		for _, x := range names {
			keyID, version, nonce, ct := secret(x.sc, x.name)
			out = append(out, fmt.Sprint(keyID, version, nonce, ct))
		}
		return out
	}
	before := snapshot()
	stop := errors.New("stop")
	calls := 0
	err = r.Rewrap(f.ctx, func(sc core.Scope, name string, keyID int64, version int, nonce, ct []byte) (int64, []byte, []byte, error) {
		calls++
		if calls == len(names) {
			return 0, nil, nil, stop
		}
		return k1, []byte("rn"), []byte("rc"), nil
	})
	f.want(err, stop, "Rewrap returns fn's error")
	f.eq("a failed Rewrap changes no secret", snapshot(), before)

	seen := map[string]bool{}
	f.must(r.Rewrap(f.ctx, func(sc core.Scope, name string, keyID int64, version int, nonce, ct []byte) (int64, []byte, []byte, error) {
		seen[string(sc)+"/"+name] = true
		return k1, append([]byte("r"), nonce...), append([]byte("r"), ct...), nil
	}), "Rewrap")
	f.check(len(seen) == 4, "Rewrap visited %d secrets, want 4", len(seen))
	keyID, version, nonce, ct = secret(core.GlobalScope, "openai_key")
	f.check(keyID == k1 && version == 2 && nonce == "rn3" && ct == "rc3", "after Rewrap = (%d, %d, %s, %s)", keyID, version, nonce, ct)

	f.must(r.DeleteSecret(f.ctx, core.GlobalScope, "openai_key"), "DeleteSecret")
	_, _, _, _, err = r.GetSecret(f.ctx, core.GlobalScope, "openai_key")
	f.want(err, core.ErrNotFound, "GetSecret after DeleteSecret")
	f.want(r.DeleteSecret(f.ctx, core.GlobalScope, "openai_key"), core.ErrNotFound, "DeleteSecret missing")
	_, _, _, _, err = r.GetSecret(f.ctx, us, "openai_key")
	f.must(err, "DeleteSecret is per scope")
}

// testTimePrecision pins the unix-ms columns: sub-millisecond parts are
// truncated, zero stays zero, and durations stored as *_ms truncate too.
func testTimePrecision(f *fx) {
	u := &core.User{Username: "precise", Role: core.RoleMember, CreatedAt: base.Add(1500*time.Microsecond + 7), UpdatedAt: base}
	f.must(f.s.Users().Create(f.ctx, u), "Create")
	got, err := f.s.Users().Get(f.ctx, u.ID)
	f.must(err, "Get")
	f.check(got.CreatedAt.Equal(base.Add(time.Millisecond)), "created_at = %v, want %v", got.CreatedAt, base.Add(time.Millisecond))
	f.check(got.LockedUntil.IsZero(), "a zero time came back as %v", got.LockedUntil)

	s := f.session(core.UserPrincipal(u.ID))
	run := f.run(s, core.RunRunning)
	tc := &core.ToolCallRecord{Run: run.ID, Session: s.ID, Principal: s.Principal, Tool: "shell", Source: "builtin",
		SideEffect: core.SELocalWrite, Status: core.TCOK, Duration: 1500*time.Microsecond + 7, CreatedAt: at(1)}
	f.must(f.s.Runs().AddToolCall(f.ctx, tc), "AddToolCall")
	tcs, err := f.s.Runs().ToolCalls(f.ctx, viewer(u), run.ID)
	f.must(err, "ToolCalls")
	if len(tcs) == 1 {
		f.check(tcs[0].Duration == time.Millisecond && tcs[0].FinishedAt.IsZero(), "duration = %v, finished_at = %v", tcs[0].Duration, tcs[0].FinishedAt)
	}
}

func testDedup(f *fx) {
	r := f.s.Dedup()
	seen := func(channel, msg string, sec int, want bool) {
		f.t.Helper()
		got, err := r.Seen(f.ctx, channel, msg, at(sec))
		f.must(err, "Seen")
		f.check(got == want, "Seen(%s, %s) = %t, want %t", channel, msg, got, want)
	}
	seen("telegram", "1:1", 1, false)
	seen("telegram", "1:1", 2, true)
	seen("slack", "1:1", 1, false)
	seen("telegram", "1:2", 10, false)
	n, err := r.Prune(f.ctx, at(5))
	f.must(err, "Prune")
	f.check(n == 2, "Prune removed %d rows, want 2", n)
	seen("telegram", "1:1", 20, false)
	seen("telegram", "1:2", 20, true)
}

func testTx(f *fx) {
	var created *core.User
	f.must(f.s.Tx(f.ctx, func(tx core.Store) error {
		created = &core.User{Username: "committed", Role: core.RoleMember, CreatedAt: at(1)}
		if err := tx.Users().Create(f.ctx, created); err != nil {
			return err
		}
		if _, err := tx.Users().Get(f.ctx, created.ID); err != nil {
			return err
		}
		return tx.Meta().Set(f.ctx, "k", "committed")
	}), "Tx commit")
	_, err := f.s.Users().Get(f.ctx, created.ID)
	f.must(err, "a committed Tx is visible")

	stop := errors.New("stop")
	err = f.s.Tx(f.ctx, func(tx core.Store) error {
		if err := tx.Users().Create(f.ctx, &core.User{Username: "rolled", Role: core.RoleMember, CreatedAt: at(2)}); err != nil {
			return err
		}
		if err := tx.Meta().Set(f.ctx, "k", "rolled"); err != nil {
			return err
		}
		return stop
	})
	f.want(err, stop, "Tx returns fn's error")
	_, err = f.s.Users().ByUsername(f.ctx, "rolled")
	f.want(err, core.ErrNotFound, "a failed Tx is rolled back")
	v, err := f.s.Meta().Get(f.ctx, "k")
	f.must(err, "Meta")
	f.check(v == "committed", "a failed Tx left meta k = %q", v)

	err = f.s.Tx(f.ctx, func(tx core.Store) error {
		if err := tx.Meta().Set(f.ctx, "k2", "x"); err != nil {
			return err
		}
		return tx.Users().Create(f.ctx, &core.User{Username: "COMMITTED", Role: core.RoleMember})
	})
	f.want(err, core.ErrConflict, "a constraint error inside Tx")
	_, err = f.s.Meta().Get(f.ctx, "k2")
	f.want(err, core.ErrNotFound, "writes before a constraint error are rolled back")
	n, err := f.s.Users().Count(f.ctx)
	f.must(err, "Count")
	f.check(n == 1, "Count after Tx = %d, want 1", n)
}

// testIDOR checks every by-ID method that takes a Viewer: a viewer without
// the row's principal (admin or not) gets ErrNotFound and changes nothing.
func testIDOR(f *fx) {
	alice := f.user(core.RoleMember)
	bob := f.user(core.RoleAdmin)
	pa := core.UserPrincipal(alice.ID)
	sp := f.space()
	owner := viewer(alice, sp.Principal)

	s := f.session(pa)
	msg := textMsg(s.ID, core.MsgUser, core.ProvUser, "secret plans", 1)
	f.must(f.s.Messages().Append(f.ctx, msg), "Append")
	pin := &core.Pin{Session: s.ID, Text: "pin", Source: "user"}
	f.must(f.s.Pins().Add(f.ctx, pin), "Add pin")
	run := f.run(s, core.RunWaitingApproval)
	f.must(f.s.Runs().AddLLMCall(f.ctx, run.ID, pa, alice.ID, core.CallInfo{Ref: core.ModelRef{Provider: "p", Model: "m"}, Purpose: "agent"}), "AddLLMCall")
	f.must(f.s.Runs().AddToolCall(f.ctx, &core.ToolCallRecord{Run: run.ID, Session: s.ID, Principal: pa, Tool: "fs_read", Source: "builtin", SideEffect: core.SERead, Status: core.TCOK}), "AddToolCall")
	apr := f.approval(run, core.ApprovalPending, at(2))
	grant := &core.Grant{Principal: pa, Scope: core.GrantSession, ScopeID: string(s.ID), Kind: core.GrantDomain, Value: "a.example:443", MaxSE: core.SEExternalSend}
	f.must(f.s.Grants().Add(f.ctx, grant), "Add grant")
	sch := f.schedule(pa, at(100), true)
	_, err := f.s.MemoryEvents().Add(f.ctx, &core.MemoryEvent{Principal: pa, File: core.FileMemory, Op: "add", After: "x", Actor: "user:x", At: at(3)})
	f.must(err, "Add memory event")
	up := &core.Upload{Principal: pa, Session: s.ID, Name: "a.txt", MIME: "text/plain", Ref: "a.txt"}
	f.must(f.s.Uploads().Create(f.ctx, up), "Create upload")
	f.must(f.s.Egress().Add(f.ctx, []core.EgressRecord{{At: at(4), Dialer: "box", Session: s.ID, Principal: pa, Host: "a.example", Port: 443, Decision: "allow"}}), "Add egress")

	ctx := f.ctx
	byID := []struct {
		name string
		call func(core.Viewer) error
	}{
		{"Principals.Get user", func(v core.Viewer) error { _, err := f.s.Principals().Get(ctx, v, pa); return err }},
		{"Principals.Get space", func(v core.Viewer) error { _, err := f.s.Principals().Get(ctx, v, sp.Principal); return err }},
		{"Spaces.Get", func(v core.Viewer) error { _, err := f.s.Spaces().Get(ctx, v, sp.Principal); return err }},
		{"Sessions.Get", func(v core.Viewer) error { _, err := f.s.Sessions().Get(ctx, v, s.ID); return err }},
		{"Messages.Range", func(v core.Viewer) error { _, err := f.s.Messages().Range(ctx, v, s.ID, 1, 0); return err }},
		{"Messages.Page", func(v core.Viewer) error { _, err := f.s.Messages().Page(ctx, v, s.ID, core.Page{}); return err }},
		{"Pins.List", func(v core.Viewer) error { _, err := f.s.Pins().List(ctx, v, s.ID); return err }},
		{"Runs.Get", func(v core.Viewer) error { _, err := f.s.Runs().Get(ctx, v, run.ID); return err }},
		{"Runs.LLMCalls", func(v core.Viewer) error { _, err := f.s.Runs().LLMCalls(ctx, v, run.ID); return err }},
		{"Runs.ToolCalls", func(v core.Viewer) error { _, err := f.s.Runs().ToolCalls(ctx, v, run.ID); return err }},
		{"Approvals.Get", func(v core.Viewer) error { _, err := f.s.Approvals().Get(ctx, v, apr.ID); return err }},
		{"Schedules.Get", func(v core.Viewer) error { _, err := f.s.Schedules().Get(ctx, v, sch.ID); return err }},
		{"MemoryEvents.List", func(v core.Viewer) error { _, err := f.s.MemoryEvents().List(ctx, v, pa, "", core.Page{}); return err }},
		{"Uploads.Get", func(v core.Viewer) error { _, err := f.s.Uploads().Get(ctx, v, up.ID); return err }},
	}
	mutations := []struct {
		name string
		call func(core.Viewer) error
	}{
		{"Sessions.Archive", func(v core.Viewer) error { return f.s.Sessions().Archive(ctx, v, s.ID) }},
		{"Pins.Delete", func(v core.Viewer) error { return f.s.Pins().Delete(ctx, v, s.ID, pin.ID) }},
		{"Grants.Revoke", func(v core.Viewer) error { return f.s.Grants().Revoke(ctx, v, grant.ID, at(9)) }},
		{"Schedules.Delete", func(v core.Viewer) error { return f.s.Schedules().Delete(ctx, v, sch.ID) }},
	}
	bobAdmin := viewer(bob)
	bobAdmin.Admin = true
	foreign := map[string]core.Viewer{
		"foreign":       viewer(bob),
		"foreign admin": bobAdmin,
		"empty":         {},
		"user only":     {User: alice.ID},
	}
	for vname, v := range foreign {
		for _, c := range append(byID, mutations...) {
			f.want(c.call(v), core.ErrNotFound, vname+" viewer: "+c.name)
		}
	}
	for _, c := range byID {
		f.must(c.call(owner), "owner viewer: "+c.name)
	}
	got, err := f.s.Sessions().Get(ctx, owner, s.ID)
	f.must(err, "Get session")
	f.check(!got.Archived, "a foreign Archive archived the session")
	pins, err := f.s.Pins().List(ctx, owner, s.ID)
	f.must(err, "List pins")
	f.check(len(pins) == 1, "a foreign Delete removed the pin")
	grants, err := f.s.Grants().List(ctx, owner, "", "")
	f.must(err, "List grants")
	f.check(len(grants) == 1, "a foreign Revoke revoked the grant")

	lists := []struct {
		name string
		n    func(core.Viewer) (int, error)
	}{
		{"Spaces.List", func(v core.Viewer) (int, error) { x, err := f.s.Spaces().List(ctx, v); return len(x), err }},
		{"Sessions.List", func(v core.Viewer) (int, error) {
			x, err := f.s.Sessions().List(ctx, v, core.SessionFilter{IncludeHidden: true})
			return len(x), err
		}},
		{"Sessions.List by principal", func(v core.Viewer) (int, error) {
			x, err := f.s.Sessions().List(ctx, v, core.SessionFilter{Principal: pa})
			return len(x), err
		}},
		{"Messages.Search", func(v core.Viewer) (int, error) {
			x, err := f.s.Messages().Search(ctx, v, "secret", 10)
			return len(x), err
		}},
		{"Runs.List", func(v core.Viewer) (int, error) {
			x, err := f.s.Runs().List(ctx, v, core.RunFilter{})
			return len(x), err
		}},
		{"Approvals.Pending", func(v core.Viewer) (int, error) { x, err := f.s.Approvals().Pending(ctx, v); return len(x), err }},
		{"Approvals.Recent", func(v core.Viewer) (int, error) {
			x, err := f.s.Approvals().Recent(ctx, v, core.Page{})
			return len(x), err
		}},
		{"Grants.List", func(v core.Viewer) (int, error) { x, err := f.s.Grants().List(ctx, v, "", ""); return len(x), err }},
		{"Schedules.List", func(v core.Viewer) (int, error) { x, err := f.s.Schedules().List(ctx, v); return len(x), err }},
		{"Egress.Query", func(v core.Viewer) (int, error) {
			x, err := f.s.Egress().Query(ctx, v, "", core.Page{})
			return len(x), err
		}},
		{"Egress.Query by session", func(v core.Viewer) (int, error) {
			x, err := f.s.Egress().Query(ctx, v, s.ID, core.Page{})
			return len(x), err
		}},
	}
	for vname, v := range foreign {
		for _, c := range lists {
			n, err := c.n(v)
			f.must(err, vname+" viewer: "+c.name)
			f.check(n == 0, "%s viewer: %s returned %d of another principal's rows", vname, c.name, n)
		}
	}

	missing := []struct {
		name string
		call func() error
	}{
		{"Principals.Get", func() error {
			_, err := f.s.Principals().Get(ctx, owner, core.PrincipalID(core.NewID(core.PrefixSpace)))
			return err
		}},
		{"Spaces.Get", func() error {
			_, err := f.s.Spaces().Get(ctx, owner, core.PrincipalID(core.NewID(core.PrefixSpace)))
			return err
		}},
		{"Runs.Get", func() error { _, err := f.s.Runs().Get(ctx, owner, core.RunID(core.NewID(core.PrefixRun))); return err }},
		{"Approvals.Get", func() error {
			_, err := f.s.Approvals().Get(ctx, owner, core.ApprovalID(core.NewID(core.PrefixApproval)))
			return err
		}},
		{"Schedules.Get", func() error {
			_, err := f.s.Schedules().Get(ctx, owner, core.ScheduleID(core.NewID(core.PrefixSchedule)))
			return err
		}},
		{"Uploads.Get", func() error { _, err := f.s.Uploads().Get(ctx, owner, core.NewID(core.PrefixUpload)); return err }},
		{"Pins.List", func() error {
			_, err := f.s.Pins().List(ctx, owner, core.SessionID(core.NewID(core.PrefixSession)))
			return err
		}},
		{"Pins.Delete", func() error { return f.s.Pins().Delete(ctx, owner, s.ID, pin.ID+1000) }},
		{"Grants.Revoke", func() error {
			return f.s.Grants().Revoke(ctx, owner, core.GrantID(core.NewID(core.PrefixGrant)), at(9))
		}},
		{"Schedules.Delete", func() error {
			return f.s.Schedules().Delete(ctx, owner, core.ScheduleID(core.NewID(core.PrefixSchedule)))
		}},
	}
	for _, c := range missing {
		f.want(c.call(), core.ErrNotFound, "missing id: "+c.name)
	}
}

// testNoAliasing checks that stores copy values in and out: neither the
// caller's struct after a write nor a returned struct shares memory with the
// stored row.
func testNoAliasing(f *fx) {
	u := f.user(core.RoleMember)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u)

	s := &core.Session{Principal: pu, Origin: "telegram", Chat: &core.ChatRef{Channel: "telegram", ChatID: "1", Kind: core.ChatDM},
		ToolsSnapshot: []string{"fs_read", "shell"}, CreatedAt: at(1)}
	f.must(f.s.Sessions().Create(f.ctx, s), "Create session")
	s.ToolsSnapshot[0] = "mutated"
	s.Chat.ChatID = "mutated"
	getSession := func() *core.Session {
		f.t.Helper()
		got, err := f.s.Sessions().Get(f.ctx, v, s.ID)
		f.must(err, "Get")
		f.need(len(got.ToolsSnapshot) == 2 && got.Chat != nil, "session lost tools_snapshot or chat: %+v", got)
		return got
	}
	got := getSession()
	f.check(got.ToolsSnapshot[0] == "fs_read" && got.Chat.ChatID == "1", "Create kept the caller's slices: %v %v", got.ToolsSnapshot, got.Chat)
	got.ToolsSnapshot[1] = "mutated"
	got.Chat.ChatID = "mutated"
	got = getSession()
	f.check(got.ToolsSnapshot[1] == "shell" && got.Chat.ChatID == "1", "Get returned the stored slices: %v %v", got.ToolsSnapshot, got.Chat)

	m := textMsg(s.ID, core.MsgUser, core.ProvUser, "original", 2)
	f.must(f.s.Messages().Append(f.ctx, m), "Append")
	m.Parts[0].Text = "mutated"
	firstPart := func() *core.Part {
		f.t.Helper()
		ms, err := f.s.Messages().Range(f.ctx, v, s.ID, 1, 0)
		f.must(err, "Range")
		f.need(len(ms) == 1 && len(ms[0].Parts) == 1, "Range returned %d messages", len(ms))
		return &ms[0].Parts[0]
	}
	firstPart().Text = "mutated"
	f.check(firstPart().Text == "original", "message parts are shared: %q", firstPart().Text)

	run := f.run(s, core.RunRunning)
	a := f.approval(run, core.ApprovalPending, at(3))
	a.Card.Fields[0].Value = "mutated"
	getApproval := func() *core.Approval {
		f.t.Helper()
		ga, err := f.s.Approvals().Get(f.ctx, v, a.ID)
		f.must(err, "Get approval")
		f.need(ga.Action != nil && len(ga.Card.Fields) > 0, "approval lost its action or card: %+v", ga)
		return ga
	}
	getApproval().Action.Recipient = "mutated"
	ga := getApproval()
	f.check(ga.Card.Fields[0].Value == "Alice" && ga.Action.Recipient == "telegram:42:", "approval JSON is shared: %+v %+v", ga.Card.Fields, ga.Action)

	g := &core.Grant{Principal: pu, Scope: core.GrantPrincipal, Kind: core.GrantTool, Tool: "shell", Pattern: map[string]string{"/argv/0": "ls"}, MaxSE: core.SELocalWrite}
	f.must(f.s.Grants().Add(f.ctx, g), "Add grant")
	g.Pattern["/argv/0"] = "rm"
	pattern := func() map[string]string {
		f.t.Helper()
		gs, err := f.s.Grants().List(f.ctx, v, "", "")
		f.must(err, "List grants")
		f.need(len(gs) == 1 && gs[0].Pattern != nil, "List returned %d grants", len(gs))
		return gs[0].Pattern
	}
	pattern()["/argv/0"] = "rm"
	f.check(pattern()["/argv/0"] == "ls", "grant patterns are shared: %v", pattern())

	tok := &core.TokenRecord{User: u.ID, Name: "t", SecretHash: []byte("0123456789abcdef"), Scopes: []string{"chat"}, OnApproval: "deny"}
	f.must(f.s.Tokens().Create(f.ctx, tok), "Create token")
	tok.SecretHash[0], tok.Scopes[0] = 'X', "admin"
	getToken := func() *core.TokenRecord {
		f.t.Helper()
		gt, err := f.s.Tokens().Get(f.ctx, tok.ID)
		f.must(err, "Get token")
		f.need(len(gt.SecretHash) == 16 && len(gt.Scopes) == 1, "token lost its hash or scopes: %+v", gt)
		return gt
	}
	gt := getToken()
	gt.SecretHash[1], gt.Scopes[0] = 'Y', "admin"
	gt = getToken()
	f.check(string(gt.SecretHash) == "0123456789abcdef" && gt.Scopes[0] == "chat", "token bytes are shared: %q %v", gt.SecretHash, gt.Scopes)

	detail := json.RawMessage(`{"k":"v"}`)
	_, err := f.s.Audit().Append(f.ctx, &core.AuditEntry{At: at(4), Actor: "system", Action: "x", Outcome: "ok", Detail: detail}, []byte("p"), []byte("h"))
	f.must(err, "Append audit")
	detail[2] = 'X'
	latestDetail := func() json.RawMessage {
		f.t.Helper()
		es, err := f.s.Audit().Query(f.ctx, core.AuditFilter{})
		f.must(err, "Query audit")
		f.need(len(es) == 1 && len(es[0].Detail) > 2, "Query returned %d entries", len(es))
		return es[0].Detail
	}
	latestDetail()[2] = 'Y'
	f.check(string(latestDetail()) == `{"k":"v"}`, "audit detail is shared: %s", latestDetail())
}
