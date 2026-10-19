package memstore

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

func normSession(s core.Session) (core.Session, error) {
	chat, err := jsonCol("sessions.chat", s.Chat)
	if err != nil {
		return s, err
	}
	tools, err := jsonCol("sessions.tools_snapshot", s.ToolsSnapshot)
	if err != nil {
		return s, err
	}
	s.Chat, s.ToolsSnapshot = chat, tools
	s.CreatedAt = ms(s.CreatedAt)
	s.UpdatedAt = ms(s.UpdatedAt)
	s.LastMessageAt = ms(s.LastMessageAt)
	return s, nil
}

// checkSession enforces the origin CHECK, the principal, parent and root
// foreign keys and sessions_channel_key, which only covers open sessions
// with a channel key ("" is stored as NULL).
func (st *state) checkSession(s core.Session) error {
	if !oneOf(s.Origin, "web", "telegram", "discord", "slack", "api", "schedule") {
		return invalid("sessions.origin")
	}
	if _, ok := st.principals[s.Principal]; !ok {
		return invalid("sessions.principal_id: no such principal")
	}
	for _, ref := range []core.SessionID{s.ParentID, s.RootID} {
		if _, ok := st.sessions[ref]; ref != "" && !ok {
			return invalid("sessions.parent_id/root_id: no such session")
		}
	}
	if s.ChannelKey == "" || s.Archived {
		return nil
	}
	for id, o := range st.sessions {
		if id != s.ID && !o.Archived && o.Principal == s.Principal && o.ChannelKey == s.ChannelKey {
			return conflict("sessions_channel_key")
		}
	}
	return nil
}

type sessionRepo struct{ s *store }

func (r sessionRepo) Create(_ context.Context, s *core.Session) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *s
	row.ID = idOr(row.ID, core.PrefixSession)
	if _, ok := st.sessions[row.ID]; ok {
		return conflict("sessions.id")
	}
	if err := st.checkSession(row); err != nil {
		return err
	}
	row, err := normSession(row)
	if err != nil {
		return err
	}
	st.sessions[row.ID] = row
	s.ID = row.ID
	return nil
}

func (r sessionRepo) Get(_ context.Context, v core.Viewer, id core.SessionID) (*core.Session, error) {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.sessions[id]
	if !ok || !v.Can(s.Principal) {
		return nil, notFound("session")
	}
	s = must(normSession(s))
	return &s, nil
}

func (r sessionRepo) ByChannelKey(_ context.Context, p core.PrincipalID, key string) (*core.Session, error) {
	st, unlock := r.s.begin()
	defer unlock()
	for _, s := range st.sessions {
		if key != "" && s.Principal == p && s.ChannelKey == key && !s.Archived {
			s = must(normSession(s))
			return &s, nil
		}
	}
	return nil, notFound("session")
}

func (r sessionRepo) List(_ context.Context, v core.Viewer, f core.SessionFilter) ([]*core.Session, error) {
	st, unlock := r.s.begin()
	defer unlock()
	q := asciiLower(f.Query)
	var out []core.Session
	for _, s := range st.sessions {
		switch {
		case !v.Can(s.Principal),
			f.Principal != "" && s.Principal != f.Principal,
			s.Archived != f.Archived,
			s.Hidden && !f.IncludeHidden,
			f.NonEmpty && st.lastSeq[s.ID] == 0,
			!strings.Contains(asciiLower(s.Title), q),
			f.Page.Before > 0 && unixMS(s.LastMessageAt) >= f.Page.Before:
			continue
		}
		out = append(out, s)
	}
	out = newestFirst(out, pageLimit(f.Page.Limit), func(s core.Session) (int64, string) {
		return unixMS(s.LastMessageAt), string(s.ID)
	})
	for i := range out {
		out[i] = must(normSession(out[i]))
	}
	return ptrs(out), nil
}

func (r sessionRepo) Update(_ context.Context, s *core.Session) error {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.sessions[s.ID]
	if !ok {
		return notFound("session")
	}
	row := *s
	row.Principal = cur.Principal
	row.CreatedAt = cur.CreatedAt
	row.ParentID, row.RootID = cur.ParentID, cur.RootID
	if err := st.checkSession(row); err != nil {
		return err
	}
	row, err := normSession(row)
	if err != nil {
		return err
	}
	st.sessions[s.ID] = row
	return nil
}

func (r sessionRepo) SetTaint(_ context.Context, id core.SessionID, t core.Taint) error {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.sessions[id]
	if !ok {
		return notFound("session")
	}
	s.Taint = t
	st.sessions[id] = s
	return nil
}

func (r sessionRepo) SetHidden(_ context.Context, id core.SessionID, hidden bool) error {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.sessions[id]
	if !ok {
		return notFound("session")
	}
	s.Hidden = hidden
	st.sessions[id] = s
	return nil
}

func (r sessionRepo) Related(_ context.Context, v core.Viewer, ids []core.SessionID) ([]*core.Session, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Session
	for _, id := range ids {
		s, ok := st.sessions[id]
		if !ok || !v.Can(s.Principal) || s.Archived || s.Hidden || slices.ContainsFunc(out, func(o core.Session) bool { return o.ID == id }) {
			continue
		}
		out = append(out, must(normSession(s)))
	}
	out = newestFirst(out, len(out), func(s core.Session) (int64, string) { return unixMS(s.LastMessageAt), string(s.ID) })
	return ptrs(out), nil
}

func (r sessionRepo) Archive(_ context.Context, v core.Viewer, id core.SessionID) error {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.sessions[id]
	if !ok || !v.Can(s.Principal) {
		return notFound("session")
	}
	s.Archived = true
	st.sessions[id] = s
	return nil
}

// AddURLs ignores URLs the session already has, keeping their first source.
func (r sessionRepo) AddURLs(_ context.Context, id core.SessionID, urls []string, source string) error {
	st, unlock := r.s.begin()
	defer unlock()
	if !oneOf(source, "search", "user") {
		return invalid("session_urls.source")
	}
	if len(urls) == 0 {
		return nil
	}
	if _, ok := st.sessions[id]; !ok {
		return invalid("session_urls.session_id: no such session")
	}
	for _, u := range urls {
		k := urlKey{id, u}
		if _, ok := st.sessionURLs[k]; !ok {
			st.sessionURLs[k] = source
		}
	}
	return nil
}

func (r sessionRepo) HasURL(_ context.Context, id core.SessionID, url string) (bool, error) {
	st, unlock := r.s.begin()
	defer unlock()
	_, ok := st.sessionURLs[urlKey{id, url}]
	return ok, nil
}

// visibleSession is the by-ID check every session-scoped read starts with.
func (st *state) visibleSession(v core.Viewer, id core.SessionID) (core.Session, bool) {
	s, ok := st.sessions[id]
	return s, ok && v.Can(s.Principal)
}

func normMessage(m core.Message) (core.Message, error) {
	parts, err := jsonCol("messages.parts", m.Parts)
	if err != nil {
		return m, err
	}
	m.Parts = parts
	m.CreatedAt = ms(m.CreatedAt)
	return m, nil
}

type messageRepo struct{ s *store }

func (r messageRepo) Append(_ context.Context, m *core.Message) error {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case !oneOf(m.Role, core.MsgUser, core.MsgAssistant, core.MsgTool):
		return invalid("messages.role")
	case !oneOf(m.Kind, core.KindNormal, core.KindSummary, core.KindSteer, core.KindNotice):
		return invalid("messages.kind")
	case !oneOf(m.Provenance, core.ProvUser, core.ProvUntrusted, core.ProvTool, core.ProvSystem):
		return invalid("messages.provenance")
	}
	if _, ok := st.sessions[m.Session]; !ok {
		return invalid("messages.session_id: no such session")
	}
	row, err := normMessage(*m)
	if err != nil {
		return err
	}
	st.msgID++
	row.ID = st.msgID
	row.Seq = st.lastSeq[m.Session] + 1
	st.lastSeq[m.Session] = row.Seq
	st.messages = append(st.messages, row)
	m.ID, m.Seq = row.ID, row.Seq
	return nil
}

// Range returns every message from fromSeq on when limit <= 0: context
// assembly reads the whole tail after the summary.
func (r messageRepo) Range(_ context.Context, v core.Viewer, s core.SessionID, fromSeq int64, limit int) ([]*core.Message, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.visibleSession(v, s); !ok {
		return nil, notFound("session")
	}
	var out []core.Message
	for _, m := range st.messages {
		if m.Session != s || m.Seq < fromSeq {
			continue
		}
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, must(normMessage(m)))
	}
	return ptrs(out), nil
}

func (r messageRepo) Page(_ context.Context, v core.Viewer, s core.SessionID, p core.Page) ([]*core.Message, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.visibleSession(v, s); !ok {
		return nil, notFound("session")
	}
	limit := pageLimit(p.Limit)
	var out []core.Message
	for i := len(st.messages) - 1; i >= 0 && len(out) < limit; i-- {
		m := st.messages[i]
		if m.Session == s && (p.Before <= 0 || m.Seq < p.Before) {
			out = append(out, must(normMessage(m)))
		}
	}
	return ptrs(out), nil
}

// Search stands in for FTS5: every query token must occur in the message text
// as a case-insensitive substring; more occurrences rank first.
func (r messageRepo) Search(_ context.Context, v core.Viewer, query string, limit int) ([]core.SearchHit, error) {
	st, unlock := r.s.begin()
	defer unlock()
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(terms) == 0 {
		return nil, nil
	}
	type hit struct {
		h  core.SearchHit
		id int64
	}
	var hits []hit
	for _, m := range st.messages {
		s, ok := st.sessions[m.Session]
		if !ok || s.Hidden || !v.Can(s.Principal) {
			continue
		}
		text := m.PlainText()
		lower := strings.ToLower(text)
		score := 0
		for _, t := range terms {
			n := strings.Count(lower, t)
			if n == 0 {
				score = 0
				break
			}
			score += n
		}
		if score == 0 {
			continue
		}
		hits = append(hits, hit{id: m.ID, h: core.SearchHit{
			Session: m.Session,
			Title:   s.Title,
			Seq:     m.Seq,
			Snippet: snippet(text, terms),
			At:      m.CreatedAt,
			Rank:    -float64(score),
			Trusted: m.Provenance == core.ProvUser || m.Provenance == core.ProvSystem,
		}})
	}
	slices.SortFunc(hits, func(a, b hit) int {
		return cmp.Or(cmp.Compare(a.h.Rank, b.h.Rank), cmp.Compare(b.id, a.id))
	})
	if l := pageLimit(limit); len(hits) > l {
		hits = hits[:l]
	}
	out := make([]core.SearchHit, len(hits))
	for i, h := range hits {
		out[i] = h.h
	}
	return out, nil
}

// snippet approximates FTS5 snippet(…, '[', ']', '…', 12): a 12-word window
// starting a few words before the first match, matching words bracketed.
func snippet(text string, terms []string) string {
	words := strings.Fields(text)
	matches := func(w string) bool {
		lw := strings.ToLower(w)
		return slices.ContainsFunc(terms, func(t string) bool { return strings.Contains(lw, t) })
	}
	first := slices.IndexFunc(words, matches)
	start := max(first-4, 0)
	end := min(start+12, len(words))
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	for i, w := range words[start:end] {
		if i > 0 {
			b.WriteByte(' ')
		}
		if matches(w) {
			b.WriteString("[" + w + "]")
		} else {
			b.WriteString(w)
		}
	}
	if end < len(words) {
		b.WriteString("…")
	}
	return b.String()
}

func normPin(p core.Pin) core.Pin {
	p.CreatedAt = ms(p.CreatedAt)
	return p
}

type pinRepo struct{ s *store }

func (r pinRepo) Add(_ context.Context, p *core.Pin) error {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case utf8.RuneCountInString(p.Text) > 2000:
		return invalid("pins.text: longer than 2000 characters")
	case !oneOf(p.Source, "user", "auto"):
		return invalid("pins.source")
	}
	if _, ok := st.sessions[p.Session]; !ok {
		return invalid("pins.session_id: no such session")
	}
	row := normPin(*p)
	st.pinID++
	row.ID = st.pinID
	st.pins = append(st.pins, row)
	p.ID = row.ID
	return nil
}

func (r pinRepo) List(_ context.Context, v core.Viewer, s core.SessionID) ([]*core.Pin, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.visibleSession(v, s); !ok {
		return nil, notFound("session")
	}
	var out []core.Pin
	for _, p := range st.pins {
		if p.Session == s {
			out = append(out, p)
		}
	}
	return ptrs(out), nil
}

func (r pinRepo) Delete(_ context.Context, v core.Viewer, s core.SessionID, id int64) error {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.visibleSession(v, s); !ok {
		return notFound("session")
	}
	i := slices.IndexFunc(st.pins, func(p core.Pin) bool { return p.ID == id && p.Session == s })
	if i < 0 {
		return notFound("pin")
	}
	st.pins = slices.Delete(st.pins, i, i+1)
	return nil
}

func normUpload(u core.Upload) core.Upload {
	u.CreatedAt = ms(u.CreatedAt)
	return u
}

type uploadRepo struct{ s *store }

func (r uploadRepo) Create(_ context.Context, u *core.Upload) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := normUpload(*u)
	row.ID = idOr(row.ID, core.PrefixUpload)
	if _, ok := st.principals[row.Principal]; !ok {
		return invalid("uploads.principal_id: no such principal")
	}
	if _, ok := st.uploads[row.ID]; ok {
		return conflict("uploads.id")
	}
	st.uploads[row.ID] = row
	u.ID = row.ID
	return nil
}

func (r uploadRepo) Get(_ context.Context, v core.Viewer, id string) (*core.Upload, error) {
	st, unlock := r.s.begin()
	defer unlock()
	u, ok := st.uploads[id]
	if !ok || !v.Can(u.Principal) {
		return nil, notFound("upload")
	}
	return &u, nil
}
