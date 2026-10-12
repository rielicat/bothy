package conformance

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

func testSessions(f *fx) {
	r := f.s.Sessions()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	sp := f.space(u)
	v := viewer(u, sp.Principal)

	chat := &core.ChatRef{Channel: "telegram", ChatID: "-1001", ThreadID: "7", Kind: core.ChatGroup}
	s := &core.Session{
		Principal: sp.Principal, Origin: "telegram", ChannelKey: chat.Key(), Chat: chat, Title: "Group chat",
		ModelSlot: core.SlotFast, ModelOverride: "anthropic/claude-sonnet", SystemSnapshot: "S2 text",
		ToolsSnapshot: []string{"fs_read", "shell"}, SnapshotHash: "abc123", Summary: "## Goals", SummaryUpto: 4,
		Epoch: 1, MemorySeen: 3, PinsSeen: 2, Taint: core.TaintPrivate | core.TaintUntrusted,
		CreatedAt: at(1), UpdatedAt: at(2), LastMessageAt: at(3),
	}
	f.must(r.Create(f.ctx, s), "Create")
	f.check(core.ValidID(core.PrefixSession, string(s.ID)), "Create assigned id %q, want a ses_ id", s.ID)
	got, err := r.Get(f.ctx, v, s.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, s)
	_, err = r.Get(f.ctx, viewer(other), s.ID)
	f.want(err, core.ErrNotFound, "Get with a foreign viewer")
	_, err = r.Get(f.ctx, core.SystemViewer(), s.ID)
	f.must(err, "Get with SystemViewer")
	_, err = r.Get(f.ctx, v, core.SessionID(core.NewID(core.PrefixSession)))
	f.want(err, core.ErrNotFound, "Get missing")

	got, err = r.ByChannelKey(f.ctx, sp.Principal, chat.Key())
	f.must(err, "ByChannelKey")
	f.eq("ByChannelKey round trip", got, s)
	_, err = r.ByChannelKey(f.ctx, core.UserPrincipal(u.ID), chat.Key())
	f.want(err, core.ErrNotFound, "ByChannelKey of another principal")
	_, err = r.ByChannelKey(f.ctx, sp.Principal, "")
	f.want(err, core.ErrNotFound, "ByChannelKey with an empty key")

	keyed := func(p core.PrincipalID, key string) *core.Session {
		return &core.Session{Principal: p, Origin: "telegram", ChannelKey: key, CreatedAt: at(5), UpdatedAt: at(5)}
	}
	f.want(r.Create(f.ctx, keyed(sp.Principal, chat.Key())), core.ErrConflict, "sessions_channel_key among open sessions")
	f.must(r.Create(f.ctx, keyed(core.UserPrincipal(u.ID), chat.Key())), "the same channel key under another principal")
	f.must(r.Create(f.ctx, keyed(sp.Principal, "")), "a session without channel key")
	f.must(r.Create(f.ctx, keyed(sp.Principal, "")), "a second session without channel key (\"\" is NULL)")
	f.want(r.Create(f.ctx, &core.Session{ID: s.ID, Principal: sp.Principal, Origin: "web"}), core.ErrConflict, "duplicate id")
	f.want(r.Create(f.ctx, &core.Session{Principal: sp.Principal, Origin: "email"}), core.ErrInvalid, "origin CHECK")
	f.want(r.Create(f.ctx, &core.Session{Principal: core.PrincipalID(core.NewID(core.PrefixSpace)), Origin: "web"}), core.ErrInvalid, "principal_id foreign key")

	s.Title = "Renamed"
	s.Chat = nil
	s.ModelOverride = ""
	s.SystemSnapshot = "S2 v2"
	s.ToolsSnapshot = []string{"memory"}
	s.Summary, s.SummaryUpto, s.Epoch = "## Goals\nv2", 9, 2
	s.MemorySeen, s.PinsSeen = 5, 4
	s.Hidden = true
	s.UpdatedAt, s.LastMessageAt = at(8), at(9)
	f.must(r.Update(f.ctx, s), "Update")
	got, err = r.Get(f.ctx, v, s.ID)
	f.must(err, "Get after Update")
	f.eq("Update round trip", got, s)
	f.want(r.Update(f.ctx, &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: sp.Principal, Origin: "web"}), core.ErrNotFound, "Update missing")

	all := core.TaintPrivate | core.TaintUntrusted | core.TaintSend
	f.must(r.SetTaint(f.ctx, s.ID, all), "SetTaint")
	got, err = r.Get(f.ctx, v, s.ID)
	f.must(err, "Get after SetTaint")
	f.check(got.Taint == all, "SetTaint stored %v, want %v", got.Taint, all)
	f.want(r.SetTaint(f.ctx, core.SessionID(core.NewID(core.PrefixSession)), all), core.ErrNotFound, "SetTaint missing")

	f.want(r.Archive(f.ctx, viewer(other), s.ID), core.ErrNotFound, "Archive with a foreign viewer")
	got, err = r.Get(f.ctx, v, s.ID)
	f.must(err, "Get")
	f.check(!got.Archived, "a foreign Archive archived the session")
	f.must(r.Archive(f.ctx, v, s.ID), "Archive")
	got, err = r.Get(f.ctx, v, s.ID)
	f.must(err, "Get after Archive")
	f.check(got.Archived, "Archive did not archive")
	_, err = r.ByChannelKey(f.ctx, sp.Principal, chat.Key())
	f.want(err, core.ErrNotFound, "ByChannelKey only finds open sessions")
	f.want(r.Archive(f.ctx, v, core.SessionID(core.NewID(core.PrefixSession))), core.ErrNotFound, "Archive missing")

	fork := keyed(sp.Principal, chat.Key())
	f.must(r.Create(f.ctx, fork), "an archived session releases its channel key")
	got, err = r.ByChannelKey(f.ctx, sp.Principal, chat.Key())
	f.must(err, "ByChannelKey after fork")
	f.check(got.ID == fork.ID, "ByChannelKey = %s, want the open fork %s", got.ID, fork.ID)
	s.Archived = false
	s.ChannelKey = chat.Key()
	f.want(r.Update(f.ctx, s), core.ErrConflict, "unarchiving onto a taken channel key")
}

func testSessionList(f *fx) {
	r := f.s.Sessions()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	sp := f.space(u)
	pu := core.UserPrincipal(u.ID)
	v := viewer(u, sp.Principal)

	mk := func(p core.PrincipalID, title string, last int, mod func(*core.Session)) *core.Session {
		s := &core.Session{Principal: p, Origin: "web", Title: title, CreatedAt: at(1), UpdatedAt: at(1), LastMessageAt: at(last)}
		if mod != nil {
			mod(s)
		}
		f.must(r.Create(f.ctx, s), "Create "+title)
		return s
	}
	a1 := mk(pu, "Alpha one", 101, nil)
	b := mk(pu, "Beta", 102, nil)
	a2 := mk(pu, "ALPHA two", 103, nil)
	g := mk(sp.Principal, "Group alpha", 104, nil)
	hidden := mk(pu, "Hidden api", 105, func(s *core.Session) { s.Origin, s.Hidden = "api", true })
	archived := mk(pu, "Old alpha", 106, func(s *core.Session) { s.Archived = true })
	foreign := mk(core.UserPrincipal(other.ID), "Foreign alpha", 107, nil)

	list := func(v core.Viewer, flt core.SessionFilter) []*core.Session {
		f.t.Helper()
		out, err := r.List(f.ctx, v, flt)
		f.must(err, "List")
		return out
	}
	f.eq("List orders by last_message_at, newest first", list(v, core.SessionFilter{}), []*core.Session{g, a2, b, a1})
	f.eq("List of one principal", list(v, core.SessionFilter{Principal: sp.Principal}), []*core.Session{g})
	f.eq("List of a foreign principal", list(v, core.SessionFilter{Principal: foreign.Principal}), []*core.Session{})
	f.eq("List archived", list(v, core.SessionFilter{Archived: true}), []*core.Session{archived})
	f.eq("List including hidden", list(v, core.SessionFilter{IncludeHidden: true}), []*core.Session{hidden, g, a2, b, a1})
	f.eq("List by title, case-insensitive", list(v, core.SessionFilter{Query: "alpha"}), []*core.Session{g, a2, a1})
	f.eq("List first page", list(v, core.SessionFilter{Page: core.Page{Limit: 2}}), []*core.Session{g, a2})
	f.eq("List next page", list(v, core.SessionFilter{Page: core.Page{Before: a2.LastMessageAt.UnixMilli(), Limit: 2}}), []*core.Session{b, a1})
	f.eq("List with a foreign viewer", list(viewer(other), core.SessionFilter{}), []*core.Session{foreign})
	f.eq("List with SystemViewer", list(core.SystemViewer(), core.SessionFilter{}), []*core.Session{foreign, g, a2, b, a1})
}

func testSessionURLs(f *fx) {
	r := f.s.Sessions()
	u := f.user(core.RoleMember)
	s := f.session(core.UserPrincipal(u.ID))
	s2 := f.session(core.UserPrincipal(u.ID))

	f.must(r.AddURLs(f.ctx, s.ID, []string{"https://a.example/x", "https://b.example/"}, "search"), "AddURLs")
	f.must(r.AddURLs(f.ctx, s.ID, []string{"https://a.example/x", "https://c.example/"}, "user"), "AddURLs with a URL already recorded")
	for url, want := range map[string]bool{"https://a.example/x": true, "https://c.example/": true, "https://d.example/": false} {
		ok, err := r.HasURL(f.ctx, s.ID, url)
		f.must(err, "HasURL")
		f.check(ok == want, "HasURL(%s) = %t, want %t", url, ok, want)
	}
	ok, err := r.HasURL(f.ctx, s2.ID, "https://a.example/x")
	f.must(err, "HasURL")
	f.check(!ok, "HasURL leaked a URL across sessions")
	f.want(r.AddURLs(f.ctx, s.ID, []string{"https://e.example/"}, "model"), core.ErrInvalid, "source CHECK")
	f.want(r.AddURLs(f.ctx, core.SessionID(core.NewID(core.PrefixSession)), []string{"https://e.example/"}, "user"), core.ErrInvalid, "session_id foreign key")
}

func textMsg(s core.SessionID, role core.MsgRole, prov core.Provenance, text string, sec int) *core.Message {
	return &core.Message{Session: s, Role: role, Kind: core.KindNormal, Parts: []core.Part{{Type: core.PartText, Text: text}},
		Provenance: prov, CreatedAt: at(sec)}
}

func testMessages(f *fx) {
	r := f.s.Messages()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	s := f.session(core.UserPrincipal(u.ID))
	s2 := f.session(core.UserPrincipal(u.ID))
	v := viewer(u)

	m1 := &core.Message{Session: s.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUser, Sender: "web:" + string(u.ID), Tokens: 12, CreatedAt: at(1),
		Parts: []core.Part{{Type: core.PartText, Text: "Book the Lisbon flight"}, {Type: core.PartContext, Text: `<turn-context time="09:00"/>`}}}
	m2 := &core.Message{Session: s.ID, Run: "run_01aaaaaaaaaaaaaaaaaaaaaaaa", Role: core.MsgAssistant, Kind: core.KindNormal, Provenance: core.ProvSystem, Tokens: 40, CreatedAt: at(2),
		Parts: []core.Part{
			{Type: core.PartThinking, Text: "plan", Sig: "c2ln", SigKind: core.KindAnthropic},
			{Type: core.PartText, Text: "Searching."},
			{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "call_1", Name: "web_search", Args: json.RawMessage(`{"q": "lisbon flights"}`)}},
		}}
	m3 := &core.Message{Session: s.ID, Role: core.MsgTool, Kind: core.KindNormal, Provenance: core.ProvUntrusted, CreatedAt: at(3),
		Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{CallID: "call_1", Name: "web_search", Content: "3 results", Truncated: true, FullRef: ".bothy/out/x.txt", Untrusted: true}}}}
	m4 := &core.Message{Session: s.ID, Role: core.MsgUser, Kind: core.KindSteer, Provenance: core.ProvUser, CreatedAt: at(4),
		Parts: []core.Part{{Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Name: "a.png", Ref: ".bothy/in/1/a.png", Size: 3, Data: []byte{1, 2, 3}}}}}
	for i, m := range []*core.Message{m1, m2, m3, m4} {
		f.must(r.Append(f.ctx, m), "Append")
		f.check(m.Seq == int64(i+1), "Append assigned seq %d, want %d", m.Seq, i+1)
		f.check(m.ID > 0, "Append assigned id %d", m.ID)
	}
	f.check(m1.ID < m2.ID && m2.ID < m3.ID && m3.ID < m4.ID, "Append ids not increasing: %d %d %d %d", m1.ID, m2.ID, m3.ID, m4.ID)
	o := textMsg(s2.ID, core.MsgUser, core.ProvUser, "other session", 5)
	f.must(r.Append(f.ctx, o), "Append to another session")
	f.check(o.Seq == 1, "seq is per session: got %d, want 1", o.Seq)

	want4 := *m4
	want4.Parts = []core.Part{{Type: core.PartImage, Blob: &core.Blob{MIME: "image/png", Name: "a.png", Ref: ".bothy/in/1/a.png", Size: 3}}}
	got, err := r.Range(f.ctx, v, s.ID, 1, 0)
	f.must(err, "Range")
	f.eq("Range round trip (Blob.Data is never persisted)", got, []*core.Message{m1, m2, m3, &want4})
	got, err = r.Range(f.ctx, v, s.ID, 2, 2)
	f.must(err, "Range from seq 2, limit 2")
	f.eq("Range window", got, []*core.Message{m2, m3})
	got, err = r.Range(f.ctx, v, s.ID, 10, 0)
	f.must(err, "Range past the end")
	f.check(len(got) == 0, "Range past the end returned %d messages", len(got))
	_, err = r.Range(f.ctx, viewer(other), s.ID, 1, 0)
	f.want(err, core.ErrNotFound, "Range with a foreign viewer")
	_, err = r.Range(f.ctx, v, core.SessionID(core.NewID(core.PrefixSession)), 1, 0)
	f.want(err, core.ErrNotFound, "Range of a missing session")

	bad := func(mod func(*core.Message)) *core.Message {
		m := textMsg(s.ID, core.MsgUser, core.ProvUser, "x", 9)
		mod(m)
		return m
	}
	f.want(r.Append(f.ctx, bad(func(m *core.Message) { m.Role = "system" })), core.ErrInvalid, "role CHECK")
	f.want(r.Append(f.ctx, bad(func(m *core.Message) { m.Kind = "" })), core.ErrInvalid, "kind CHECK")
	f.want(r.Append(f.ctx, bad(func(m *core.Message) { m.Provenance = "" })), core.ErrInvalid, "provenance CHECK")
	f.want(r.Append(f.ctx, bad(func(m *core.Message) { m.Session = core.SessionID(core.NewID(core.PrefixSession)) })), core.ErrInvalid, "session_id foreign key")
	next := textMsg(s.ID, core.MsgUser, core.ProvUser, "after rejects", 10)
	f.must(r.Append(f.ctx, next), "Append")
	f.check(next.Seq == 5, "rejected appends consumed seqs: got %d, want 5", next.Seq)
}

func testMessagePaging(f *fx) {
	r := f.s.Messages()
	u := f.user(core.RoleMember)
	s := f.session(core.UserPrincipal(u.ID))
	v := viewer(u)
	const total = 561
	for i := 1; i <= total; i++ {
		f.must(r.Append(f.ctx, textMsg(s.ID, core.MsgUser, core.ProvUser, fmt.Sprintf("msg %d", i), i)), "Append")
	}
	seqs := func(ms []*core.Message) []int64 {
		out := make([]int64, len(ms))
		for i, m := range ms {
			out[i] = m.Seq
		}
		return out
	}
	desc := func(from, to int64) []int64 {
		var out []int64
		for q := from; q >= to; q-- {
			out = append(out, q)
		}
		return out
	}
	page := func(p core.Page) []int64 {
		f.t.Helper()
		ms, err := r.Page(f.ctx, v, s.ID, p)
		f.must(err, "Page")
		return seqs(ms)
	}
	f.eq("Page default: 50 newest", page(core.Page{}), desc(total, total-49))
	f.eq("Page before seq 11", page(core.Page{Before: 11, Limit: 5}), desc(10, 6))
	f.eq("Page before seq 3", page(core.Page{Before: 3}), []int64{2, 1})
	f.check(len(page(core.Page{Limit: 600})) == 500, "Page limit is capped at 500")
	all, err := r.Range(f.ctx, v, s.ID, 1, 0)
	f.must(err, "Range")
	f.check(len(all) == total, "Range with limit 0 returned %d messages, want all %d", len(all), total)
	_, err = r.Page(f.ctx, viewer(f.user(core.RoleMember)), s.ID, core.Page{})
	f.want(err, core.ErrNotFound, "Page with a foreign viewer")
}

func testMessageSearch(f *fx) {
	r := f.s.Messages()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	sp := f.space(u)
	pu := core.UserPrincipal(u.ID)

	s1 := f.session(pu)
	hidden := &core.Session{Principal: pu, Origin: "api", Hidden: true, CreatedAt: at(1)}
	f.must(f.s.Sessions().Create(f.ctx, hidden), "Create hidden session")
	foreign := f.session(core.UserPrincipal(other.ID))
	group := f.session(sp.Principal)

	add := func(s *core.Session, prov core.Provenance, text string) *core.Message {
		m := textMsg(s.ID, core.MsgUser, prov, text, f.next())
		f.must(r.Append(f.ctx, m), "Append")
		return m
	}
	flight := add(s1, core.ProvUser, "We should book the Lisbon flight tomorrow")
	hotel := add(s1, core.ProvSystem, "Booked: lisbon hotel near the river.")
	add(hidden, core.ProvUser, "lisbon via the hidden api session")
	add(foreign, core.ProvUser, "lisbon in someone else's session")
	groupMsg := add(group, core.ProvUntrusted, "Lisbon meetup in the group")

	search := func(v core.Viewer, q string) []core.SearchHit {
		f.t.Helper()
		hits, err := r.Search(f.ctx, v, q, 10)
		f.must(err, "Search "+q)
		return hits
	}
	keys := func(hits []core.SearchHit) map[string]bool {
		out := map[string]bool{}
		for _, h := range hits {
			out[fmt.Sprintf("%s#%d", h.Session, h.Seq)] = true
		}
		return out
	}
	key := func(m *core.Message) string { return fmt.Sprintf("%s#%d", m.Session, m.Seq) }

	hits := search(viewer(u, sp.Principal), "lisbon")
	f.eq("Search finds the viewer's messages, never hidden or foreign sessions", keys(hits),
		map[string]bool{key(flight): true, key(hotel): true, key(groupMsg): true})
	for _, h := range hits {
		switch fmt.Sprintf("%s#%d", h.Session, h.Seq) {
		case key(flight):
			f.check(h.Title == s1.Title && h.Trusted && h.At.Equal(flight.CreatedAt), "hit for a user message = %+v", h)
		case key(groupMsg):
			f.check(h.Title == group.Title && !h.Trusted, "hit for an untrusted message = %+v", h)
		}
		f.check(strings.Contains(strings.ToLower(h.Snippet), "lisbon"), "snippet %q lacks the match", h.Snippet)
	}
	f.eq("Search ANDs the words", keys(search(viewer(u, sp.Principal), "LISBON flight")), map[string]bool{key(flight): true})
	f.eq("Search without the space principal", keys(search(viewer(u), "lisbon")), map[string]bool{key(flight): true, key(hotel): true})
	f.check(len(search(viewer(other), "lisbon")) == 1, "a foreign viewer sees only its own session")
	f.check(len(search(core.SystemViewer(), "lisbon")) == 4, "SystemViewer sees every non-hidden session")
	f.check(len(search(viewer(u), "madrid")) == 0, "Search without a match returned hits")
	limited, err := r.Search(f.ctx, viewer(u), "lisbon", 1)
	f.must(err, "Search with a limit")
	f.check(len(limited) == 1, "Search limit 1 returned %d hits", len(limited))
	for _, q := range []string{`"`, `NEAR(`, `*`, `lisbon"`, `"lisbon OR`, `AND`, `(`, `)`, `-x`, `^`, `col:val`, `{a b}`, ``} {
		_, err := r.Search(f.ctx, viewer(u), q, 10)
		f.must(err, fmt.Sprintf("hostile query %q", q))
	}
}

func testPins(f *fx) {
	r := f.s.Pins()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	s := f.session(core.UserPrincipal(u.ID))
	s2 := f.session(core.UserPrincipal(u.ID))
	v := viewer(u)

	p1 := &core.Pin{Session: s.ID, Text: "never email my boss", Source: "user", MsgSeq: 3, CreatedAt: at(1)}
	p2 := &core.Pin{Session: s.ID, Text: "don't act until I say", Source: "auto", CreatedAt: at(2)}
	f.must(r.Add(f.ctx, p1), "Add")
	f.must(r.Add(f.ctx, p2), "Add")
	f.check(p1.ID > 0 && p2.ID > p1.ID, "Add assigned ids %d, %d", p1.ID, p2.ID)
	got, err := r.List(f.ctx, v, s.ID)
	f.must(err, "List")
	f.eq("List in id order", got, []*core.Pin{p1, p2})
	_, err = r.List(f.ctx, viewer(other), s.ID)
	f.want(err, core.ErrNotFound, "List with a foreign viewer")

	f.want(r.Delete(f.ctx, viewer(other), s.ID, p1.ID), core.ErrNotFound, "Delete with a foreign viewer")
	f.want(r.Delete(f.ctx, v, s2.ID, p1.ID), core.ErrNotFound, "Delete through another session")
	got, err = r.List(f.ctx, v, s.ID)
	f.must(err, "List")
	f.check(len(got) == 2, "rejected Deletes removed a pin")
	f.must(r.Delete(f.ctx, v, s.ID, p1.ID), "Delete")
	got, err = r.List(f.ctx, v, s.ID)
	f.must(err, "List after Delete")
	f.eq("List after Delete", got, []*core.Pin{p2})
	f.want(r.Delete(f.ctx, v, s.ID, p1.ID), core.ErrNotFound, "Delete twice")

	f.must(r.Add(f.ctx, &core.Pin{Session: s.ID, Text: strings.Repeat("é", 2000), Source: "user"}), "2000 characters (length counts characters)")
	f.want(r.Add(f.ctx, &core.Pin{Session: s.ID, Text: strings.Repeat("a", 2001), Source: "user"}), core.ErrInvalid, "text length CHECK")
	f.want(r.Add(f.ctx, &core.Pin{Session: s.ID, Text: "x", Source: "model"}), core.ErrInvalid, "source CHECK")
	f.want(r.Add(f.ctx, &core.Pin{Session: core.SessionID(core.NewID(core.PrefixSession)), Text: "x", Source: "user"}), core.ErrInvalid, "session_id foreign key")
}

func testUploads(f *fx) {
	r := f.s.Uploads()
	u := f.user(core.RoleMember)
	other := f.user(core.RoleMember)
	s := f.session(core.UserPrincipal(u.ID))

	up := &core.Upload{Principal: core.UserPrincipal(u.ID), Session: s.ID, Name: "report.pdf", MIME: "application/pdf", Size: 1234,
		Ref: ".bothy/in/upl/report.pdf", CreatedAt: at(1)}
	f.must(r.Create(f.ctx, up), "Create")
	f.check(core.ValidID(core.PrefixUpload, up.ID), "Create assigned id %q, want an upl_ id", up.ID)
	got, err := r.Get(f.ctx, viewer(u), up.ID)
	f.must(err, "Get")
	f.eq("Get round trip", got, up)
	_, err = r.Get(f.ctx, viewer(other), up.ID)
	f.want(err, core.ErrNotFound, "Get with a foreign viewer")
	_, err = r.Get(f.ctx, viewer(u), core.NewID(core.PrefixUpload))
	f.want(err, core.ErrNotFound, "Get missing")
	dup := *up
	f.want(r.Create(f.ctx, &dup), core.ErrConflict, "duplicate id")
	f.want(r.Create(f.ctx, &core.Upload{Principal: core.PrincipalID(core.NewID(core.PrefixSpace)), Session: s.ID, Name: "x", MIME: "text/plain", Ref: "x"}), core.ErrInvalid, "principal_id foreign key")
}
