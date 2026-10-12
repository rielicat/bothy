package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/url"
	"strings"
	"unicode"

	"github.com/bothyhq/bothy/internal/core"
)

const sessionCols = `id, principal_id, origin, COALESCE(channel_key, ''), chat, title, model_slot, model_override, system_snapshot,
	tools_snapshot, snapshot_hash, summary, summary_upto, epoch, memory_seen, pins_seen, taint, hidden, archived,
	created_at, updated_at, last_message_at`

func scanSession(sc scanner) (*core.Session, error) {
	var s core.Session
	var chat sql.NullString
	var tools string
	var created, updated, last int64
	if err := sc.Scan(&s.ID, &s.Principal, &s.Origin, &s.ChannelKey, &chat, &s.Title, &s.ModelSlot, &s.ModelOverride,
		&s.SystemSnapshot, &tools, &s.SnapshotHash, &s.Summary, &s.SummaryUpto, &s.Epoch, &s.MemorySeen, &s.PinsSeen,
		&s.Taint, &s.Hidden, &s.Archived, &created, &updated, &last); err != nil {
		return nil, err
	}
	var err error
	if s.Chat, err = decodeNullable[core.ChatRef]("sessions.chat", chat); err != nil {
		return nil, err
	}
	if err := decodeJSON("sessions.tools_snapshot", tools, &s.ToolsSnapshot); err != nil {
		return nil, err
	}
	s.CreatedAt, s.UpdatedAt, s.LastMessageAt = fromMS(created), fromMS(updated), fromMS(last)
	return &s, nil
}

// sessionJSON encodes the JSON columns of a session.
func sessionJSON(s *core.Session) (chat any, tools string, err error) {
	if chat, err = jsonNullable("sessions.chat", s.Chat); err != nil {
		return nil, "", err
	}
	t := s.ToolsSnapshot
	if t == nil {
		t = []string{}
	}
	tools, err = jsonText("sessions.tools_snapshot", t)
	return chat, tools, err
}

type sessionRepo struct{ h *handle }

func (r sessionRepo) Create(ctx context.Context, s *core.Session) error {
	id := idOr(s.ID, core.PrefixSession)
	chat, tools, err := sessionJSON(s)
	if err != nil {
		return err
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO sessions(id, principal_id, origin, channel_key, chat, title, model_slot,
		model_override, system_snapshot, tools_snapshot, snapshot_hash, summary, summary_upto, epoch, memory_seen, pins_seen,
		taint, hidden, archived, created_at, updated_at, last_message_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(id), string(s.Principal), s.Origin, nullStr(s.ChannelKey), chat, s.Title, string(s.ModelSlot), s.ModelOverride,
		s.SystemSnapshot, tools, s.SnapshotHash, s.Summary, s.SummaryUpto, s.Epoch, s.MemorySeen, s.PinsSeen, int(s.Taint),
		b2i(s.Hidden), b2i(s.Archived), ms(s.CreatedAt), ms(s.UpdatedAt), ms(s.LastMessageAt)); err != nil {
		return dbErr("create session", err)
	}
	s.ID = id
	return nil
}

func (r sessionRepo) Get(ctx context.Context, v core.Viewer, id core.SessionID) (*core.Session, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, notFound("session")
	}
	return queryOne(ctx, r.h.rq, "session", scanSession,
		`SELECT `+sessionCols+` FROM sessions WHERE id = ? AND `+cond, append([]any{string(id)}, args...)...)
}

func (r sessionRepo) ByChannelKey(ctx context.Context, p core.PrincipalID, key string) (*core.Session, error) {
	if key == "" {
		return nil, notFound("session")
	}
	return queryOne(ctx, r.h.rq, "session", scanSession,
		`SELECT `+sessionCols+` FROM sessions WHERE principal_id = ? AND channel_key = ? AND archived = 0`, string(p), key)
}

func (r sessionRepo) List(ctx context.Context, v core.Viewer, f core.SessionFilter) ([]*core.Session, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, nil
	}
	q := `SELECT ` + sessionCols + ` FROM sessions WHERE ` + cond + ` AND archived = ?`
	args = append(args, b2i(f.Archived))
	if f.Principal != "" {
		q += ` AND principal_id = ?`
		args = append(args, string(f.Principal))
	}
	if !f.IncludeHidden {
		q += ` AND hidden = 0`
	}
	if f.Query != "" {
		q += ` AND title LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(f.Query)+"%")
	}
	if f.Page.Before > 0 {
		q += ` AND last_message_at < ?`
		args = append(args, f.Page.Before)
	}
	q += ` ORDER BY last_message_at DESC, id DESC LIMIT ?`
	args = append(args, pageLimit(f.Page.Limit))
	return queryAll(ctx, r.h.rq, "list sessions", scanSession, q, args...)
}

// Update replaces every mutable column; principal_id and created_at are fixed
// at creation.
func (r sessionRepo) Update(ctx context.Context, s *core.Session) error {
	chat, tools, err := sessionJSON(s)
	if err != nil {
		return err
	}
	res, err := r.h.wq.ExecContext(ctx, `UPDATE sessions SET origin = ?, channel_key = ?, chat = ?, title = ?, model_slot = ?,
		model_override = ?, system_snapshot = ?, tools_snapshot = ?, snapshot_hash = ?, summary = ?, summary_upto = ?, epoch = ?,
		memory_seen = ?, pins_seen = ?, taint = ?, hidden = ?, archived = ?, updated_at = ?, last_message_at = ? WHERE id = ?`,
		s.Origin, nullStr(s.ChannelKey), chat, s.Title, string(s.ModelSlot), s.ModelOverride, s.SystemSnapshot, tools,
		s.SnapshotHash, s.Summary, s.SummaryUpto, s.Epoch, s.MemorySeen, s.PinsSeen, int(s.Taint), b2i(s.Hidden),
		b2i(s.Archived), ms(s.UpdatedAt), ms(s.LastMessageAt), string(s.ID))
	return mustAffect("update session", res, err)
}

func (r sessionRepo) SetTaint(ctx context.Context, id core.SessionID, t core.Taint) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE sessions SET taint = ? WHERE id = ?`, int(t), string(id))
	return mustAffect("set taint", res, err)
}

func (r sessionRepo) Archive(ctx context.Context, v core.Viewer, id core.SessionID) error {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return notFound("session")
	}
	res, err := r.h.wq.ExecContext(ctx, `UPDATE sessions SET archived = 1 WHERE id = ? AND `+cond, append([]any{string(id)}, args...)...)
	return mustAffect("archive session", res, err)
}

// AddURLs keeps the first source of a URL the session already has.
func (r sessionRepo) AddURLs(ctx context.Context, id core.SessionID, urls []string, source string) error {
	if !oneOf(source, "search", "user") {
		return invalid("session_urls.source")
	}
	if len(urls) == 0 {
		return nil
	}
	now := ms(r.h.now())
	return r.h.write(ctx, func(q querier) error {
		for _, u := range urls {
			if _, err := q.ExecContext(ctx, `INSERT INTO session_urls(session_id, url_hash, source, created_at) VALUES (?,?,?,?)
				ON CONFLICT(session_id, url_hash) DO NOTHING`, string(id), urlHash(u), source, now); err != nil {
				return dbErr("add session url", err)
			}
		}
		return nil
	})
}

func (r sessionRepo) HasURL(ctx context.Context, id core.SessionID, u string) (bool, error) {
	return exists(ctx, r.h.rq, "has url", `SELECT 1 FROM session_urls WHERE session_id = ? AND url_hash = ?`, string(id), urlHash(u))
}

// urlHash is sha256 of the URL with the parts that cannot change the request
// target normalised: scheme and host case, default ports, an empty path and
// the fragment. Unparseable input is hashed verbatim.
func urlHash(raw string) []byte {
	s := raw
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		u.Scheme = strings.ToLower(u.Scheme)
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
			port = ""
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port != "" {
			host += ":" + port
		}
		u.Host = host
		u.Fragment, u.RawFragment = "", ""
		if u.Path == "" && u.RawPath == "" {
			u.Path = "/"
		}
		s = u.String()
	}
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// visibleSession checks the by-ID rule every session-scoped read starts with.
func (h *handle) visibleSession(ctx context.Context, q querier, v core.Viewer, id core.SessionID) error {
	var p core.PrincipalID
	if err := q.QueryRowContext(ctx, `SELECT principal_id FROM sessions WHERE id = ?`, string(id)).Scan(&p); err != nil {
		return dbErr("session", err)
	}
	if !v.Can(p) {
		return notFound("session")
	}
	return nil
}

const messageCols = `id, session_id, seq, COALESCE(run_id, ''), role, kind, parts, provenance, sender, tokens, created_at`

func scanMessage(sc scanner) (*core.Message, error) {
	var m core.Message
	var parts string
	var created int64
	if err := sc.Scan(&m.ID, &m.Session, &m.Seq, &m.Run, &m.Role, &m.Kind, &parts, &m.Provenance, &m.Sender, &m.Tokens, &created); err != nil {
		return nil, err
	}
	if err := decodeJSON("messages.parts", parts, &m.Parts); err != nil {
		return nil, err
	}
	m.CreatedAt = fromMS(created)
	return &m, nil
}

type messageRepo struct{ h *handle }

// Append assigns id and seq in the INSERT itself: the single writer runs one
// statement at a time, so MAX(seq)+1 cannot race, and a rejected row consumes
// no seq.
func (r messageRepo) Append(ctx context.Context, m *core.Message) error {
	parts := m.Parts
	if parts == nil {
		parts = []core.Part{}
	}
	pj, err := jsonText("messages.parts", parts)
	if err != nil {
		return err
	}
	var id, seq int64
	err = r.h.wq.QueryRowContext(ctx, `INSERT INTO messages(session_id, seq, run_id, role, kind, provenance, sender, parts, text, tokens, created_at)
		VALUES (?1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_id = ?1), ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)
		RETURNING id, seq`,
		string(m.Session), nullStr(m.Run), string(m.Role), string(m.Kind), string(m.Provenance), m.Sender, pj,
		m.PlainText(), m.Tokens, ms(m.CreatedAt)).Scan(&id, &seq)
	if err != nil {
		return dbErr("append message", err)
	}
	m.ID, m.Seq = id, seq
	return nil
}

func (r messageRepo) Range(ctx context.Context, v core.Viewer, s core.SessionID, fromSeq int64, limit int) ([]*core.Message, error) {
	if err := r.h.visibleSession(ctx, r.h.rq, v, s); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = -1
	}
	return queryAll(ctx, r.h.rq, "range messages", scanMessage,
		`SELECT `+messageCols+` FROM messages WHERE session_id = ? AND seq >= ? ORDER BY seq LIMIT ?`, string(s), fromSeq, limit)
}

func (r messageRepo) Page(ctx context.Context, v core.Viewer, s core.SessionID, p core.Page) ([]*core.Message, error) {
	if err := r.h.visibleSession(ctx, r.h.rq, v, s); err != nil {
		return nil, err
	}
	q := `SELECT ` + messageCols + ` FROM messages WHERE session_id = ?`
	args := []any{string(s)}
	if p.Before > 0 {
		q += ` AND seq < ?`
		args = append(args, p.Before)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, pageLimit(p.Limit))
	return queryAll(ctx, r.h.rq, "page messages", scanMessage, q, args...)
}

// maxSearchTerms bounds the MATCH expression a single query can build.
const maxSearchTerms = 16

// matchQuery turns user input into an FTS5 expression that ANDs quoted terms.
// Terms are runs of letters, digits and combining marks (what unicode61
// tokenizes), so the result never carries FTS syntax: quotes, NEAR, column
// filters, prefixes and operators all become separators or plain words.
func matchQuery(input string) string {
	terms := strings.FieldsFunc(input, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	})
	if len(terms) > maxSearchTerms {
		terms = terms[:maxSearchTerms]
	}
	for i, t := range terms {
		terms[i] = `"` + t + `"`
	}
	return strings.Join(terms, " ")
}

func (r messageRepo) Search(ctx context.Context, v core.Viewer, query string, limit int) ([]core.SearchHit, error) {
	match := matchQuery(query)
	if match == "" {
		return nil, nil
	}
	cond, args, ok := viewerCond("s.principal_id", v)
	if !ok {
		return nil, nil
	}
	args = append([]any{match}, args...)
	args = append(args, pageLimit(limit))
	return queryAll(ctx, r.h.rq, "search messages", func(sc scanner) (core.SearchHit, error) {
		var h core.SearchHit
		var at int64
		err := sc.Scan(&h.Session, &h.Title, &h.Seq, &h.Snippet, &at, &h.Rank, &h.Trusted)
		h.At = fromMS(at)
		return h, err
	}, `SELECT m.session_id, s.title, m.seq, snippet(messages_fts, 0, '[', ']', '…', 12), m.created_at,
		bm25(messages_fts) AS rank, m.provenance IN ('user', 'system')
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		JOIN sessions s ON s.id = m.session_id
		WHERE messages_fts MATCH ? AND `+cond+` AND s.hidden = 0
		ORDER BY rank, m.id DESC LIMIT ?`, args...)
}

const pinCols = `id, session_id, text, source, msg_seq, created_at`

func scanPin(sc scanner) (*core.Pin, error) {
	var p core.Pin
	var created int64
	if err := sc.Scan(&p.ID, &p.Session, &p.Text, &p.Source, &p.MsgSeq, &created); err != nil {
		return nil, err
	}
	p.CreatedAt = fromMS(created)
	return &p, nil
}

type pinRepo struct{ h *handle }

func (r pinRepo) Add(ctx context.Context, p *core.Pin) error {
	var id int64
	if err := r.h.wq.QueryRowContext(ctx, `INSERT INTO pins(session_id, text, source, msg_seq, created_at) VALUES (?,?,?,?,?) RETURNING id`,
		string(p.Session), p.Text, p.Source, p.MsgSeq, ms(p.CreatedAt)).Scan(&id); err != nil {
		return dbErr("add pin", err)
	}
	p.ID = id
	return nil
}

func (r pinRepo) List(ctx context.Context, v core.Viewer, s core.SessionID) ([]*core.Pin, error) {
	if err := r.h.visibleSession(ctx, r.h.rq, v, s); err != nil {
		return nil, err
	}
	return queryAll(ctx, r.h.rq, "list pins", scanPin, `SELECT `+pinCols+` FROM pins WHERE session_id = ? ORDER BY id`, string(s))
}

func (r pinRepo) Delete(ctx context.Context, v core.Viewer, s core.SessionID, id int64) error {
	if err := r.h.visibleSession(ctx, r.h.wq, v, s); err != nil {
		return err
	}
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM pins WHERE id = ? AND session_id = ?`, id, string(s))
	return mustAffect("delete pin", res, err)
}

const uploadCols = `id, principal_id, session_id, name, mime, size, ref, created_at`

func scanUpload(sc scanner) (*core.Upload, error) {
	var u core.Upload
	var created int64
	if err := sc.Scan(&u.ID, &u.Principal, &u.Session, &u.Name, &u.MIME, &u.Size, &u.Ref, &created); err != nil {
		return nil, err
	}
	u.CreatedAt = fromMS(created)
	return &u, nil
}

type uploadRepo struct{ h *handle }

func (r uploadRepo) Create(ctx context.Context, u *core.Upload) error {
	id := idOr(u.ID, core.PrefixUpload)
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO uploads(`+uploadCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		id, string(u.Principal), string(u.Session), u.Name, u.MIME, u.Size, u.Ref, ms(u.CreatedAt)); err != nil {
		return dbErr("create upload", err)
	}
	u.ID = id
	return nil
}

func (r uploadRepo) Get(ctx context.Context, v core.Viewer, id string) (*core.Upload, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, notFound("upload")
	}
	return queryOne(ctx, r.h.rq, "upload", scanUpload, `SELECT `+uploadCols+` FROM uploads WHERE id = ? AND `+cond, append([]any{id}, args...)...)
}
