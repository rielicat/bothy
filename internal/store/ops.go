package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const auditCols = `id, ts, actor, action, target, outcome, session_id, run_id, ip, detail, prev_hash, hash`

func scanAudit(sc scanner) (auditRow, error) {
	var a auditRow
	var ts int64
	var detail string
	if err := sc.Scan(&a.e.ID, &ts, &a.e.Actor, &a.e.Action, &a.e.Target, &a.e.Outcome, &a.e.Session, &a.e.Run, &a.e.IP,
		&detail, &a.prev, &a.hash); err != nil {
		return a, err
	}
	a.e.At = fromMS(ts)
	a.e.Detail = json.RawMessage(detail)
	return a, nil
}

type auditRow struct {
	e          core.AuditEntry
	prev, hash []byte
}

type auditRepo struct{ h *handle }

// Append always lets SQLite assign the next id, ignoring e.ID, so no existing
// row can be overwritten. An empty Detail is stored as the column default.
func (r auditRepo) Append(ctx context.Context, e *core.AuditEntry, prevHash, hash []byte) (int64, error) {
	detail := string(e.Detail)
	if detail == "" {
		detail = "{}"
	}
	var id int64
	if err := r.h.wq.QueryRowContext(ctx, `INSERT INTO audit_log(ts, actor, action, target, outcome, session_id, run_id, ip, detail,
		prev_hash, hash) VALUES (?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		ms(e.At), e.Actor, e.Action, e.Target, e.Outcome, string(e.Session), string(e.Run), e.IP, detail,
		blob(prevHash), blob(hash)).Scan(&id); err != nil {
		return 0, dbErr("append audit", err)
	}
	e.ID = id
	return id, nil
}

func (r auditRepo) Head(ctx context.Context) (int64, []byte, error) {
	var id int64
	var hash []byte
	err := r.h.rq.QueryRowContext(ctx, `SELECT id, hash FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&id, &hash)
	if isNoRows(err) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, dbErr("audit head", err)
	}
	return id, hash, nil
}

// auditChunk is how many rows Range buffers per query. fn runs with no rows
// open, so it may call back into the store; rows are immutable, so paging by
// id sees a consistent log.
const auditChunk = 500

func (r auditRepo) Range(ctx context.Context, fromID, toID int64, fn func(e *core.AuditEntry, prevHash, hash []byte) error) error {
	next := fromID
	for {
		rows, err := queryAll(ctx, r.h.rq, "audit range", scanAudit, `SELECT `+auditCols+` FROM audit_log
			WHERE id >= ? AND (? <= 0 OR id <= ?) ORDER BY id LIMIT ?`, next, toID, toID, auditChunk)
		if err != nil {
			return err
		}
		for i := range rows {
			if err := fn(&rows[i].e, rows[i].prev, rows[i].hash); err != nil {
				return err
			}
		}
		if len(rows) < auditChunk {
			return nil
		}
		next = rows[len(rows)-1].e.ID + 1
	}
}

func (r auditRepo) Query(ctx context.Context, f core.AuditFilter) ([]*core.AuditEntry, error) {
	q := `SELECT ` + auditCols + ` FROM audit_log WHERE 1`
	var args []any
	if f.Page.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Page.Before)
	}
	if f.Actor != "" {
		q += ` AND actor = ?`
		args = append(args, f.Actor)
	}
	if f.ActionPrefix != "" {
		q += ` AND action GLOB ?`
		args = append(args, escapeGlob(f.ActionPrefix)+"*")
	}
	if !f.From.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, ms(f.From))
	}
	if !f.To.IsZero() {
		q += ` AND ts < ?`
		args = append(args, ms(f.To))
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, pageLimit(f.Page.Limit))
	rows, err := queryAll(ctx, r.h.rq, "query audit", scanAudit, q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]*core.AuditEntry, len(rows))
	for i := range rows {
		out[i] = &rows[i].e
	}
	return out, nil
}

const egressCols = `id, ts, dialer, dest, session_id, principal_id, run_id, host, port, ip, decision, reason, bytes_up, bytes_down, duration_ms`

func scanEgress(sc scanner) (*core.EgressRecord, error) {
	var e core.EgressRecord
	var ts, dur int64
	if err := sc.Scan(&e.ID, &ts, &e.Dialer, &e.Dest, &e.Session, &e.Principal, &e.Run, &e.Host, &e.Port, &e.IP, &e.Decision,
		&e.Reason, &e.Up, &e.Down, &dur); err != nil {
		return nil, err
	}
	e.At, e.Duration = fromMS(ts), fromDurMS(dur)
	return &e, nil
}

type egressRepo struct{ h *handle }

// Add inserts the whole batch or nothing.
func (r egressRepo) Add(ctx context.Context, rs []core.EgressRecord) error {
	if len(rs) == 0 {
		return nil
	}
	return r.h.write(ctx, func(q querier) error {
		for _, e := range rs {
			if _, err := q.ExecContext(ctx, `INSERT INTO egress_log(ts, dialer, dest, session_id, principal_id, run_id, host, port, ip,
				decision, reason, bytes_up, bytes_down, duration_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				ms(e.At), e.Dialer, e.Dest, string(e.Session), string(e.Principal), string(e.Run), e.Host, e.Port, e.IP,
				e.Decision, e.Reason, e.Up, e.Down, durMS(e.Duration)); err != nil {
				return dbErr("add egress", err)
			}
		}
		return nil
	})
}

// Query applies Viewer.Admin's second exception: admins also see system-dialer
// rows, which have no principal.
func (r egressRepo) Query(ctx context.Context, v core.Viewer, s core.SessionID, p core.Page) ([]*core.EgressRecord, error) {
	cond, args, ok := viewerCond("principal_id", v)
	switch {
	case v.System:
	case v.Admin && ok:
		cond = "(" + cond + " OR principal_id = '')"
	case v.Admin:
		cond, ok = "principal_id = ''", true
	case ok:
		cond = "(" + cond + " AND principal_id <> '')"
	}
	if !ok {
		return nil, nil
	}
	q := `SELECT ` + egressCols + ` FROM egress_log WHERE ` + cond
	if s != "" {
		q += ` AND session_id = ?`
		args = append(args, string(s))
	}
	if p.Before > 0 {
		q += ` AND id < ?`
		args = append(args, p.Before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, pageLimit(p.Limit))
	return queryAll(ctx, r.h.rq, "query egress", scanEgress, q, args...)
}

func (r egressRepo) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM egress_log WHERE ts < ?`, ms(before))
	if err != nil {
		return 0, dbErr("prune egress", err)
	}
	n, err := res.RowsAffected()
	return n, dbErr("prune egress", err)
}

const pinColsMCP = `server, tool, hash, definition, status, side_effect, field_map, private, approved_by, first_seen_at, approved_at`

func scanMCPPin(sc scanner) (*core.MCPPin, error) {
	var p core.MCPPin
	var def, se, fm string
	var first, approved int64
	if err := sc.Scan(&p.Server, &p.Tool, &p.Hash, &def, &p.Status, &se, &fm, &p.Private, &p.ApprovedBy, &first, &approved); err != nil {
		return nil, err
	}
	p.Definition = json.RawMessage(def)
	p.SideEffect = parseSE(se)
	if fm != "{}" {
		if err := decodeJSON("mcp_tool_pins.field_map", fm, &p.FieldMap); err != nil {
			return nil, err
		}
	}
	p.FirstSeen, p.ApprovedAt = fromMS(first), fromMS(approved)
	return &p, nil
}

func fieldMapText(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	return jsonText("mcp_tool_pins.field_map", m)
}

// pinSE falls back to the column default for a missing class.
func pinSE(se core.SideEffect) string {
	if !se.Valid() {
		return core.SEExternalSend.String()
	}
	return se.String()
}

type mcpRepo struct{ h *handle }

// UpsertPin records an unseen (server, tool, hash) as pending and otherwise
// reports the stored status. A superseded hash must be approved again, so
// seeing it again makes it pending.
func (r mcpRepo) UpsertPin(ctx context.Context, p *core.MCPPin) (core.MCPPinStatus, error) {
	var status core.MCPPinStatus
	err := r.h.write(ctx, func(q querier) error {
		err := q.QueryRowContext(ctx, `SELECT status FROM mcp_tool_pins WHERE server = ? AND tool = ? AND hash = ?`,
			p.Server, p.Tool, p.Hash).Scan(&status)
		switch {
		case err == nil && status == core.PinSuperseded:
			status = core.PinPending
			_, err = q.ExecContext(ctx, `UPDATE mcp_tool_pins SET status = 'pending' WHERE server = ? AND tool = ? AND hash = ?`,
				p.Server, p.Tool, p.Hash)
			return dbErr("upsert pin", err)
		case err == nil:
			return nil
		case !isNoRows(err):
			return dbErr("upsert pin", err)
		}
		var def any
		if len(p.Definition) > 0 {
			def = string(p.Definition)
		}
		fm, err := fieldMapText(p.FieldMap)
		if err != nil {
			return err
		}
		status = core.PinPending
		_, err = q.ExecContext(ctx, `INSERT INTO mcp_tool_pins(`+pinColsMCP+`) VALUES (?,?,?,?,'pending',?,?,?,'',?,0)`,
			p.Server, p.Tool, p.Hash, def, pinSE(p.SideEffect), fm, b2i(p.Private), ms(p.FirstSeen))
		return dbErr("insert pin", err)
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

// ApprovePins approves approve[tool].Hash for every tool and supersedes the
// tool's previously approved definition; one unknown hash changes nothing.
func (r mcpRepo) ApprovePins(ctx context.Context, server string, approve map[string]core.MCPPin, by core.UserID, at time.Time) error {
	tools := make([]string, 0, len(approve))
	for t := range approve {
		tools = append(tools, t)
	}
	slices.Sort(tools)
	return r.h.write(ctx, func(q querier) error {
		for _, tool := range tools {
			a := approve[tool]
			fm, err := fieldMapText(a.FieldMap)
			if err != nil {
				return err
			}
			if _, err := q.ExecContext(ctx, `UPDATE mcp_tool_pins SET status = 'superseded'
				WHERE server = ? AND tool = ? AND hash <> ? AND status = 'approved'`, server, tool, a.Hash); err != nil {
				return dbErr("supersede pin", err)
			}
			res, err := q.ExecContext(ctx, `UPDATE mcp_tool_pins SET status = 'approved', side_effect = ?, field_map = ?, private = ?,
				approved_by = ?, approved_at = ? WHERE server = ? AND tool = ? AND hash = ?`,
				pinSE(a.SideEffect), fm, b2i(a.Private), string(by), ms(at), server, tool, a.Hash)
			if err := mustAffect("approve pin "+server+"/"+tool, res, err); err != nil {
				return err
			}
		}
		return nil
	})
}

// Pins returns the latest definition per tool (by first_seen_at).
func (r mcpRepo) Pins(ctx context.Context, server string) ([]*core.MCPPin, error) {
	return queryAll(ctx, r.h.rq, "mcp pins", scanMCPPin, `SELECT `+pinColsMCP+` FROM mcp_tool_pins p WHERE server = ?
		AND NOT EXISTS (SELECT 1 FROM mcp_tool_pins q WHERE q.server = p.server AND q.tool = p.tool
			AND (q.first_seen_at > p.first_seen_at OR (q.first_seen_at = p.first_seen_at AND q.hash > p.hash)))
		ORDER BY tool`, server)
}

func (r mcpRepo) History(ctx context.Context, server string) ([]*core.MCPPin, error) {
	return queryAll(ctx, r.h.rq, "mcp pin history", scanMCPPin, `SELECT `+pinColsMCP+` FROM mcp_tool_pins WHERE server = ?
		ORDER BY tool, first_seen_at, hash`, server)
}

func (r mcpRepo) DeletePins(ctx context.Context, server string) error {
	_, err := r.h.wq.ExecContext(ctx, `DELETE FROM mcp_tool_pins WHERE server = ?`, server)
	return dbErr("delete mcp pins", err)
}

func (r mcpRepo) Approved(ctx context.Context, server string) (map[string]*core.MCPPin, error) {
	pins, err := queryAll(ctx, r.h.rq, "approved pins", scanMCPPin,
		`SELECT `+pinColsMCP+` FROM mcp_tool_pins WHERE server = ? AND status = 'approved'`, server)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*core.MCPPin, len(pins))
	for _, p := range pins {
		out[p.Tool] = p
	}
	return out, nil
}

func (r mcpRepo) DisableTool(ctx context.Context, server, tool string) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE mcp_tool_pins SET status = 'disabled' WHERE server = ? AND tool = ?`, server, tool)
	return mustAffect("disable tool "+server+"/"+tool, res, err)
}

const installCols = `install_id, server, version, lock_hash, lock, tree_hash, status, error, created_at`

func scanInstall(sc scanner) (*core.MCPInstall, error) {
	var in core.MCPInstall
	var created int64
	if err := sc.Scan(&in.InstallID, &in.Server, &in.Version, &in.LockHash, &in.Lock, &in.TreeHash, &in.Status, &in.Error, &created); err != nil {
		return nil, err
	}
	in.CreatedAt = fromMS(created)
	return &in, nil
}

func (r mcpRepo) PutInstall(ctx context.Context, in *core.MCPInstall) error {
	id := idOr(in.InstallID, core.PrefixInstall)
	var lock any
	if in.Lock != nil {
		lock = in.Lock
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO mcp_installs(`+installCols+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(install_id) DO UPDATE SET server = excluded.server, version = excluded.version, lock_hash = excluded.lock_hash,
		lock = excluded.lock, tree_hash = excluded.tree_hash, status = excluded.status, error = excluded.error,
		created_at = excluded.created_at`,
		id, in.Server, in.Version, in.LockHash, lock, in.TreeHash, in.Status, in.Error, ms(in.CreatedAt)); err != nil {
		return dbErr("put install", err)
	}
	in.InstallID = id
	return nil
}

// Install returns the newest ready install, else the newest of any status.
func (r mcpRepo) Install(ctx context.Context, server string) (*core.MCPInstall, error) {
	return queryOne(ctx, r.h.rq, "mcp install "+server, scanInstall, `SELECT `+installCols+` FROM mcp_installs WHERE server = ?
		ORDER BY status = 'ready' DESC, created_at DESC, rowid DESC LIMIT 1`, server)
}

func (r mcpRepo) LastInstall(ctx context.Context, server string) (*core.MCPInstall, error) {
	return queryOne(ctx, r.h.rq, "mcp install "+server, scanInstall, `SELECT `+installCols+` FROM mcp_installs WHERE server = ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, server)
}

const skillCols = `name, description, path, hash, approved_hash, status, tokens, installed_by, approved_by, installed_at, updated_at`

func scanSkill(sc scanner) (*core.SkillMeta, error) {
	var s core.SkillMeta
	var installed, updated int64
	if err := sc.Scan(&s.Name, &s.Description, &s.Path, &s.Hash, &s.ApprovedHash, &s.Status, &s.Tokens, &s.InstalledBy,
		&s.ApprovedBy, &installed, &updated); err != nil {
		return nil, err
	}
	s.InstalledAt, s.UpdatedAt = fromMS(installed), fromMS(updated)
	return &s, nil
}

type skillRepo struct{ h *handle }

func (r skillRepo) Upsert(ctx context.Context, s *core.SkillMeta) error {
	if s.Name == "" {
		return invalid("skills.name")
	}
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO skills(`+skillCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET description = excluded.description, path = excluded.path, hash = excluded.hash,
		approved_hash = excluded.approved_hash, status = excluded.status, tokens = excluded.tokens,
		installed_by = excluded.installed_by, approved_by = excluded.approved_by, installed_at = excluded.installed_at,
		updated_at = excluded.updated_at`,
		s.Name, s.Description, s.Path, s.Hash, s.ApprovedHash, string(s.Status), s.Tokens, string(s.InstalledBy),
		string(s.ApprovedBy), ms(s.InstalledAt), ms(s.UpdatedAt))
	return dbErr("upsert skill", err)
}

func (r skillRepo) Get(ctx context.Context, name string) (*core.SkillMeta, error) {
	return queryOne(ctx, r.h.rq, "skill "+name, scanSkill, `SELECT `+skillCols+` FROM skills WHERE name = ?`, name)
}

func (r skillRepo) List(ctx context.Context) ([]*core.SkillMeta, error) {
	return queryAll(ctx, r.h.rq, "list skills", scanSkill, `SELECT `+skillCols+` FROM skills ORDER BY name`)
}

func (r skillRepo) Delete(ctx context.Context, name string) error {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM skills WHERE name = ?`, name)
	return mustAffect("delete skill "+name, res, err)
}

func (r skillRepo) SetApproved(ctx context.Context, name string, files []core.SkillFile) error {
	return r.h.write(ctx, func(q querier) error {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM skills WHERE name = ?`, name).Scan(&n); err != nil {
			return dbErr("approved skill files", err)
		}
		if n == 0 {
			return notFound("skill " + name)
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM skill_approved_files WHERE skill = ?`, name); err != nil {
			return dbErr("approved skill files", err)
		}
		for _, f := range files {
			var content any
			if f.Text {
				content = f.Content
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO skill_approved_files(skill, path, sha256, size, content) VALUES (?,?,?,?,?)`,
				name, f.Path, f.SHA256, f.Size, content); err != nil {
				return dbErr("approved skill file", err)
			}
		}
		return nil
	})
}

func (r skillRepo) Approved(ctx context.Context, name string) ([]core.SkillFile, error) {
	return queryAll(ctx, r.h.rq, "approved skill files", func(sc scanner) (core.SkillFile, error) {
		var f core.SkillFile
		var content sql.NullString
		err := sc.Scan(&f.Path, &f.SHA256, &f.Size, &content)
		f.Text, f.Content = content.Valid, content.String
		return f, err
	}, `SELECT path, sha256, size, content FROM skill_approved_files WHERE skill = ? ORDER BY path`, name)
}

const memoryEventCols = `id, principal_id, file, op, before, after, run_id, actor, provenance, created_at`

func scanMemoryEvent(sc scanner) (*core.MemoryEvent, error) {
	var e core.MemoryEvent
	var at int64
	if err := sc.Scan(&e.ID, &e.Principal, &e.File, &e.Op, &e.Before, &e.After, &e.Run, &e.Actor, &e.Provenance, &at); err != nil {
		return nil, err
	}
	e.At = fromMS(at)
	return &e, nil
}

type memoryEventRepo struct{ h *handle }

func (r memoryEventRepo) Add(ctx context.Context, e *core.MemoryEvent) (int64, error) {
	var id int64
	if err := r.h.wq.QueryRowContext(ctx, `INSERT INTO memory_events(principal_id, file, op, before, after, run_id, actor, provenance,
		created_at) VALUES (?,?,?,?,?,?,?,?,?) RETURNING id`,
		string(e.Principal), string(e.File), e.Op, e.Before, e.After, string(e.Run), e.Actor, string(e.Provenance), ms(e.At)).Scan(&id); err != nil {
		return 0, dbErr("add memory event", err)
	}
	e.ID = id
	return id, nil
}

func (r memoryEventRepo) Since(ctx context.Context, p core.PrincipalID, afterID int64) ([]*core.MemoryEvent, error) {
	return queryAll(ctx, r.h.rq, "memory events since", scanMemoryEvent,
		`SELECT `+memoryEventCols+` FROM memory_events WHERE principal_id = ? AND id > ? ORDER BY id`, string(p), afterID)
}

func (r memoryEventRepo) List(ctx context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile, page core.Page) ([]*core.MemoryEvent, error) {
	if !v.Can(p) {
		return nil, notFound("principal")
	}
	q := `SELECT ` + memoryEventCols + ` FROM memory_events WHERE principal_id = ?`
	args := []any{string(p)}
	if f != "" {
		q += ` AND file = ?`
		args = append(args, string(f))
	}
	if page.Before > 0 {
		q += ` AND id < ?`
		args = append(args, page.Before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, pageLimit(page.Limit))
	return queryAll(ctx, r.h.rq, "list memory events", scanMemoryEvent, q, args...)
}

const teamMemoryEventCols = `id, op, before, after, run_id, approval_id, actor, provenance, created_at`

func scanTeamMemoryEvent(sc scanner) (*core.MemoryEvent, error) {
	e := core.MemoryEvent{File: core.FileTeam}
	var at int64
	if err := sc.Scan(&e.ID, &e.Op, &e.Before, &e.After, &e.Run, &e.Approval, &e.Actor, &e.Provenance, &at); err != nil {
		return nil, err
	}
	e.At = fromMS(at)
	return &e, nil
}

type teamMemoryEventRepo struct{ h *handle }

func (r teamMemoryEventRepo) Add(ctx context.Context, e *core.MemoryEvent) (int64, error) {
	if e.File != core.FileTeam || e.Principal != "" {
		return 0, invalid("team_memory_events: not a TEAM.md change")
	}
	var id int64
	if err := r.h.wq.QueryRowContext(ctx, `INSERT INTO team_memory_events(op, before, after, run_id, approval_id, actor, provenance,
		created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id`,
		e.Op, e.Before, e.After, string(e.Run), string(e.Approval), e.Actor, string(e.Provenance), ms(e.At)).Scan(&id); err != nil {
		return 0, dbErr("add team memory event", err)
	}
	e.ID = id
	return id, nil
}

func (r teamMemoryEventRepo) List(ctx context.Context, page core.Page) ([]*core.MemoryEvent, error) {
	q := `SELECT ` + teamMemoryEventCols + ` FROM team_memory_events`
	var args []any
	if page.Before > 0 {
		q += ` WHERE id < ?`
		args = append(args, page.Before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, pageLimit(page.Limit))
	return queryAll(ctx, r.h.rq, "list team memory events", scanTeamMemoryEvent, q, args...)
}

type vaultRepo struct{ h *handle }

func (r vaultRepo) ActiveKey(ctx context.Context) (int64, []byte, []byte, string, error) {
	var id int64
	var wrapped, check []byte
	var source string
	if err := r.h.rq.QueryRowContext(ctx, `SELECT id, wrapped_dek, kek_check, kek_source FROM vault_keys WHERE active = 1`).
		Scan(&id, &wrapped, &check, &source); err != nil {
		return 0, nil, nil, "", dbErr("vault key", err)
	}
	return id, wrapped, check, source, nil
}

func (r vaultRepo) PutKey(ctx context.Context, wrapped, check []byte, source string) (int64, error) {
	if wrapped == nil || check == nil {
		return 0, invalid("vault_keys: wrapped_dek and kek_check are required")
	}
	var id int64
	err := r.h.write(ctx, func(q querier) error {
		if _, err := q.ExecContext(ctx, `UPDATE vault_keys SET active = 0 WHERE active = 1`); err != nil {
			return dbErr("deactivate vault key", err)
		}
		return dbErr("put vault key", q.QueryRowContext(ctx, `INSERT INTO vault_keys(wrapped_dek, kek_check, kek_source, active, created_at)
			VALUES (?, ?, ?, 1, ?) RETURNING id`, blob(wrapped), blob(check), source, ms(r.h.now())).Scan(&id))
	})
	return id, err
}

// PutSecret keeps created_by, created_at and last_used_at of an existing
// secret; updated_at is the store clock.
func (r vaultRepo) PutSecret(ctx context.Context, sc core.Scope, name string, keyID int64, version int, nonce, ct []byte, hint string, by core.UserID) error {
	if nonce == nil || ct == nil {
		return invalid("vault_secrets: nonce and ciphertext are required")
	}
	now := ms(r.h.now())
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO vault_secrets(scope, name, key_id, version, nonce, ciphertext, hint, created_by,
		created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(scope, name) DO UPDATE SET key_id = excluded.key_id, version = excluded.version, nonce = excluded.nonce,
		ciphertext = excluded.ciphertext, hint = excluded.hint, updated_at = excluded.updated_at`,
		string(sc), name, keyID, version, blob(nonce), blob(ct), hint, string(by), now, now)
	return dbErr("put secret", err)
}

func (r vaultRepo) GetSecret(ctx context.Context, sc core.Scope, name string) (int64, int, []byte, []byte, error) {
	var keyID int64
	var version int
	var nonce, ct []byte
	if err := r.h.rq.QueryRowContext(ctx, `SELECT key_id, version, nonce, ciphertext FROM vault_secrets WHERE scope = ? AND name = ?`,
		string(sc), name).Scan(&keyID, &version, &nonce, &ct); err != nil {
		return 0, 0, nil, nil, dbErr("secret "+name, err)
	}
	return keyID, version, nonce, ct, nil
}

func (r vaultRepo) DeleteSecret(ctx context.Context, sc core.Scope, name string) error {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM vault_secrets WHERE scope = ? AND name = ?`, string(sc), name)
	return mustAffect("delete secret "+name, res, err)
}

func (r vaultRepo) ListSecrets(ctx context.Context, sc core.Scope) ([]core.SecretMeta, error) {
	return queryAll(ctx, r.h.rq, "list secrets", func(s scanner) (core.SecretMeta, error) {
		m := core.SecretMeta{Source: "vault"}
		var updated, used int64
		err := s.Scan(&m.Scope, &m.Name, &m.Hint, &updated, &used)
		m.UpdatedAt, m.LastUsed = fromMS(updated), fromMS(used)
		return m, err
	}, `SELECT scope, name, hint, updated_at, last_used_at FROM vault_secrets WHERE scope = ? ORDER BY name`, string(sc))
}

func (r vaultRepo) TouchSecret(ctx context.Context, sc core.Scope, name string, at time.Time) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE vault_secrets SET last_used_at = ? WHERE scope = ? AND name = ?`, ms(at), string(sc), name)
	return mustAffect("touch secret "+name, res, err)
}

// Rewrap runs fn for every secret inside one write transaction, so fn must not
// call back into the store. Any error leaves every secret as it was.
func (r vaultRepo) Rewrap(ctx context.Context, fn func(sc core.Scope, name string, keyID int64, version int, nonce, ct []byte) (int64, []byte, []byte, error)) error {
	type secret struct {
		sc        core.Scope
		name      string
		keyID     int64
		version   int
		nonce, ct []byte
	}
	return r.h.write(ctx, func(q querier) error {
		all, err := queryAll(ctx, q, "rewrap", func(s scanner) (secret, error) {
			var x secret
			err := s.Scan(&x.sc, &x.name, &x.keyID, &x.version, &x.nonce, &x.ct)
			return x, err
		}, `SELECT scope, name, key_id, version, nonce, ciphertext FROM vault_secrets ORDER BY scope, name`)
		if err != nil {
			return err
		}
		for _, x := range all {
			keyID, nonce, ct, err := fn(x.sc, x.name, x.keyID, x.version, x.nonce, x.ct)
			if err != nil {
				return err
			}
			if nonce == nil || ct == nil {
				return invalid("vault_secrets: nonce and ciphertext are required")
			}
			if _, err := q.ExecContext(ctx, `UPDATE vault_secrets SET key_id = ?, nonce = ?, ciphertext = ? WHERE scope = ? AND name = ?`,
				keyID, nonce, ct, string(x.sc), x.name); err != nil {
				return dbErr("rewrap secret", err)
			}
		}
		return nil
	})
}

type dedupRepo struct{ h *handle }

func (r dedupRepo) Seen(ctx context.Context, channel, msgID string, at time.Time) (bool, error) {
	res, err := r.h.wq.ExecContext(ctx, `INSERT INTO inbound_dedup(channel, msg_id, seen_at) VALUES (?, ?, ?)
		ON CONFLICT(channel, msg_id) DO NOTHING`, channel, msgID, ms(at))
	if err != nil {
		return false, dbErr("dedup", err)
	}
	n, err := res.RowsAffected()
	return n == 0, dbErr("dedup", err)
}

func (r dedupRepo) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM inbound_dedup WHERE seen_at < ?`, ms(before))
	if err != nil {
		return 0, dbErr("prune dedup", err)
	}
	n, err := res.RowsAffected()
	return n, dbErr("prune dedup", err)
}
