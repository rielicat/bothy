package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

type metaRepo struct{ h *handle }

func (r metaRepo) Get(ctx context.Context, k string) (string, error) {
	var v string
	if err := r.h.rq.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, k).Scan(&v); err != nil {
		return "", dbErr("meta "+k, err)
	}
	return v, nil
}

func (r metaRepo) Set(ctx context.Context, k, v string) error {
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return dbErr("meta set", err)
}

func (r metaRepo) Delete(ctx context.Context, k string) error {
	_, err := r.h.wq.ExecContext(ctx, `DELETE FROM meta WHERE k = ?`, k)
	return dbErr("meta delete", err)
}

const userCols = `id, username, display_name, role, pw_hash, timezone, disabled, failed_logins, locked_until, created_at, updated_at, must_change_password, last_login_at`

func scanUser(sc scanner) (*core.User, error) {
	var u core.User
	var locked, created, updated, lastLogin int64
	if err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PWHash, &u.Timezone, &u.Disabled,
		&u.FailedLogins, &locked, &created, &updated, &u.MustChangePassword, &lastLogin); err != nil {
		return nil, err
	}
	u.LockedUntil, u.CreatedAt, u.UpdatedAt = fromMS(locked), fromMS(created), fromMS(updated)
	u.LastLoginAt = fromMS(lastLogin)
	return &u, nil
}

type userRepo struct{ h *handle }

func (r userRepo) Create(ctx context.Context, u *core.User) error {
	id := idOr(u.ID, core.PrefixUser)
	err := r.h.write(ctx, func(q querier) error {
		if _, err := q.ExecContext(ctx, `INSERT INTO users(`+userCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(id), u.Username, u.DisplayName, string(u.Role), u.PWHash, u.Timezone, b2i(u.Disabled),
			u.FailedLogins, ms(u.LockedUntil), ms(u.CreatedAt), ms(u.UpdatedAt), b2i(u.MustChangePassword),
			ms(u.LastLoginAt)); err != nil {
			return dbErr("create user", err)
		}
		_, err := q.ExecContext(ctx, `INSERT INTO principals(id, kind, user_id, display_name, workspace_id, tool_profile, created_at)
			VALUES (?, 'user', ?, ?, ?, 'standard', ?)`,
			string(id), string(id), u.DisplayName, core.NewID(core.PrefixWorkspace), ms(u.CreatedAt))
		return dbErr("create user principal", err)
	})
	if err != nil {
		return err
	}
	u.ID = id
	return nil
}

func (r userRepo) Get(ctx context.Context, id core.UserID) (*core.User, error) {
	return queryOne(ctx, r.h.rq, "user", scanUser, `SELECT `+userCols+` FROM users WHERE id = ?`, string(id))
}

func (r userRepo) ByUsername(ctx context.Context, name string) (*core.User, error) {
	return queryOne(ctx, r.h.rq, "user", scanUser, `SELECT `+userCols+` FROM users WHERE username = ?`, name)
}

func (r userRepo) List(ctx context.Context) ([]*core.User, error) {
	return queryAll(ctx, r.h.rq, "list users", scanUser, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
}

func (r userRepo) Update(ctx context.Context, u *core.User) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE users SET username = ?, display_name = ?, role = ?, pw_hash = ?, timezone = ?,
		disabled = ?, failed_logins = ?, locked_until = ?, updated_at = ?, must_change_password = ? WHERE id = ?`,
		u.Username, u.DisplayName, string(u.Role), u.PWHash, u.Timezone, b2i(u.Disabled), u.FailedLogins,
		ms(u.LockedUntil), ms(u.UpdatedAt), b2i(u.MustChangePassword), string(u.ID))
	return mustAffect("update user", res, err)
}

func (r userRepo) RecordLogin(ctx context.Context, id core.UserID, ok bool, lockUntil time.Time) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE users SET failed_logins = CASE WHEN ? THEN 0 ELSE failed_logins + 1 END,
		locked_until = ? WHERE id = ?`, b2i(ok), ms(lockUntil), string(id))
	return mustAffect("record login", res, err)
}

func (r userRepo) SetProfile(ctx context.Context, u *core.User) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE users SET display_name = ?, timezone = ?, updated_at = ? WHERE id = ?`,
		u.DisplayName, u.Timezone, ms(u.UpdatedAt), string(u.ID))
	return mustAffect("update profile", res, err)
}

func (r userRepo) SignedIn(ctx context.Context, id core.UserID, at time.Time) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE users SET last_login_at = ?, failed_logins = 0, locked_until = 0 WHERE id = ?`,
		ms(at), string(id))
	return mustAffect("record sign-in", res, err)
}

// Delete relies on the schema's ON DELETE CASCADE chains from users through
// the user's own principal.
func (r userRepo) Delete(ctx context.Context, id core.UserID) error {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, string(id))
	return mustAffect("delete user", res, err)
}

func (r userRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.h.rq.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, dbErr("count users", err)
	}
	return n, nil
}

const principalCols = `id, kind, COALESCE(user_id, ''), display_name, workspace_id, tool_profile, created_at`

const principalInsertCols = `id, kind, user_id, display_name, workspace_id, tool_profile, created_at`

func scanPrincipal(sc scanner) (*core.Principal, error) {
	var p core.Principal
	var created int64
	if err := sc.Scan(&p.ID, &p.Kind, &p.User, &p.DisplayName, &p.Workspace, &p.ToolProfile, &created); err != nil {
		return nil, err
	}
	p.CreatedAt = fromMS(created)
	return &p, nil
}

type principalRepo struct{ h *handle }

func (r principalRepo) Get(ctx context.Context, v core.Viewer, id core.PrincipalID) (*core.Principal, error) {
	if !v.Can(id) {
		return nil, notFound("principal")
	}
	return queryOne(ctx, r.h.rq, "principal", scanPrincipal, `SELECT `+principalCols+` FROM principals WHERE id = ?`, string(id))
}

func (r principalRepo) ForUser(ctx context.Context, u core.UserID) ([]*core.Principal, error) {
	return queryAll(ctx, r.h.rq, "principals for user", scanPrincipal,
		`SELECT `+principalCols+` FROM principals WHERE user_id = ? ORDER BY created_at, id`, string(u))
}

func (r principalRepo) Team(ctx context.Context) ([]*core.Principal, error) {
	return queryAll(ctx, r.h.rq, "team principals", scanPrincipal,
		`SELECT `+principalCols+` FROM principals WHERE user_id IS NULL ORDER BY created_at, id`)
}

func (r principalRepo) Update(ctx context.Context, p *core.Principal) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE principals SET display_name = ?, tool_profile = ? WHERE id = ?`,
		p.DisplayName, p.ToolProfile, string(p.ID))
	return mustAffect("update principal", res, err)
}

// Delete cascades through the foreign keys of everything a team-owned
// principal holds; an account's principal goes only with its user.
func (r principalRepo) Delete(ctx context.Context, id core.PrincipalID) error {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM principals WHERE id = ? AND user_id IS NULL`, string(id))
	if err != nil {
		return dbErr("delete principal", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var kind string
	if err := r.h.wq.QueryRowContext(ctx, `SELECT kind FROM principals WHERE id = ?`, string(id)).Scan(&kind); err != nil {
		return dbErr("delete principal", err)
	}
	return invalid("delete principal: an account's principal goes only with the account")
}

const spaceCols = `principal_id, kind, channel, chat_id, chat_kind, team_id, team_name, added_by, title, members, external,
	enabled, mention_only, reviewed_at, created_at, updated_at`

func scanSpace(sc scanner) (*core.Space, error) {
	var s core.Space
	var reviewed, created, updated int64
	if err := sc.Scan(&s.Principal, &s.Kind, &s.Chat.Channel, &s.Chat.ChatID, &s.Chat.Kind, &s.Team, &s.TeamName, &s.AddedBy,
		&s.Title, &s.Members, &s.External, &s.Enabled, &s.MentionOnly, &reviewed, &created, &updated); err != nil {
		return nil, err
	}
	s.ReviewedAt, s.CreatedAt, s.UpdatedAt = fromMS(reviewed), fromMS(created), fromMS(updated)
	return &s, nil
}

type spaceRepo struct{ h *handle }

// Create inserts the space's principal and the space, and writes the
// assigned ids back to both arguments. Defaults: a fresh workspace, kind
// space and tool_profile restricted for a group, kind person for a person
// space, which is always standard. Neither has an owner.
func (r spaceRepo) Create(ctx context.Context, s *core.Space, p *core.Principal) error {
	pr := *p
	if pr.ID == "" {
		pr.ID = idOr(s.Principal, core.PrefixSpace)
	}
	if pr.Kind == "" {
		pr.Kind = core.PrincipalSpace
		if s.Kind == core.SpacePerson {
			pr.Kind = core.PrincipalPerson
		}
	}
	pr.User = ""
	pr.Workspace = idOr(pr.Workspace, core.PrefixWorkspace)
	switch {
	case pr.Kind == core.PrincipalPerson:
		pr.ToolProfile = "standard"
	case pr.ToolProfile == "":
		pr.ToolProfile = "restricted"
	}
	err := r.h.write(ctx, func(q querier) error {
		if _, err := q.ExecContext(ctx, `INSERT INTO principals(`+principalInsertCols+`) VALUES (?,?,NULL,?,?,?,?)`,
			string(pr.ID), string(pr.Kind), pr.DisplayName, string(pr.Workspace), pr.ToolProfile, ms(pr.CreatedAt)); err != nil {
			return dbErr("create space principal", err)
		}
		_, err := q.ExecContext(ctx, `INSERT INTO spaces(`+spaceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(pr.ID), s.Kind, s.Chat.Channel, s.Chat.ChatID, string(s.Chat.Kind), s.Team, s.TeamName, s.AddedBy,
			s.Title, s.Members, b2i(s.External), b2i(s.Enabled), b2i(s.MentionOnly), ms(s.ReviewedAt), ms(s.CreatedAt), ms(s.UpdatedAt))
		return dbErr("create space", err)
	})
	if err != nil {
		return err
	}
	*p = pr
	s.Principal = pr.ID
	return nil
}

func (r spaceRepo) ByChat(ctx context.Context, channel, chatID string) (*core.Space, error) {
	return queryOne(ctx, r.h.rq, "space", scanSpace, `SELECT `+spaceCols+` FROM spaces WHERE channel = ? AND chat_id = ?`, channel, chatID)
}

func (r spaceRepo) Get(ctx context.Context, v core.Viewer, id core.PrincipalID) (*core.Space, error) {
	if !v.Can(id) {
		return nil, notFound("space")
	}
	return queryOne(ctx, r.h.rq, "space", scanSpace, `SELECT `+spaceCols+` FROM spaces WHERE principal_id = ?`, string(id))
}

func (r spaceRepo) List(ctx context.Context, v core.Viewer) ([]*core.Space, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, nil
	}
	return queryAll(ctx, r.h.rq, "list spaces", scanSpace,
		`SELECT `+spaceCols+` FROM spaces WHERE `+cond+` ORDER BY created_at, principal_id`, args...)
}

func (r spaceRepo) Update(ctx context.Context, s *core.Space) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE spaces SET team_id = ?, team_name = ?, added_by = ?, title = ?, members = ?,
		external = ?, enabled = ?, mention_only = ?, reviewed_at = ?, updated_at = ? WHERE principal_id = ?`,
		s.Team, s.TeamName, s.AddedBy, s.Title, s.Members, b2i(s.External), b2i(s.Enabled), b2i(s.MentionOnly),
		ms(s.ReviewedAt), ms(s.UpdatedAt), string(s.Principal))
	return mustAffect("update space", res, err)
}

const identityCols = `channel, external_id, username, display_name, status, COALESCE(principal_id, ''), admitted_via,
	can_approve, membership, membership_at, dm_chat_id, first_seen_at, last_seen_at, last_notice_at, greet, first_text`

func scanIdentity(sc scanner) (*core.ChannelIdentity, error) {
	var id core.ChannelIdentity
	var memberAt, first, last, notice int64
	if err := sc.Scan(&id.Channel, &id.ExternalID, &id.Username, &id.DisplayName, &id.Status, &id.Principal, &id.Via,
		&id.CanApprove, &id.Member, &memberAt, &id.DMChat, &first, &last, &notice, &id.Greet, &id.FirstText); err != nil {
		return nil, err
	}
	id.MemberAt, id.FirstSeen, id.LastSeen, id.LastNotice = fromMS(memberAt), fromMS(first), fromMS(last), fromMS(notice)
	return &id, nil
}

type identityRepo struct{ h *handle }

// Touch inserts an unknown sender (pending, with id.Member as its first
// verdict and first_seen defaulting to last_seen); for a known one it
// refreshes only the names and last_seen, never status or verdicts.
func (r identityRepo) Touch(ctx context.Context, id *core.ChannelIdentity) (*core.ChannelIdentity, error) {
	var out *core.ChannelIdentity
	err := r.h.write(ctx, func(q querier) error {
		res, err := q.ExecContext(ctx, `UPDATE channel_identities SET username = ?, display_name = ?, last_seen_at = ?
			WHERE channel = ? AND external_id = ?`, id.Username, id.DisplayName, ms(id.LastSeen), id.Channel, id.ExternalID)
		if err != nil {
			return dbErr("touch identity", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			row := *id
			row.Status, row.Principal, row.Via, row.CanApprove = core.IdentityPending, "", "", false
			if row.FirstSeen.IsZero() {
				row.FirstSeen = row.LastSeen
			}
			if row.Member != core.MemberUnchecked && row.MemberAt.IsZero() {
				row.MemberAt = row.LastSeen
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO channel_identities(channel, external_id, username, display_name, status,
				membership, membership_at, dm_chat_id, first_seen_at, last_seen_at, last_notice_at, greet, first_text)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,'hello',?)`,
				row.Channel, row.ExternalID, row.Username, row.DisplayName, string(row.Status), string(row.Member),
				ms(row.MemberAt), row.DMChat, ms(row.FirstSeen), ms(row.LastSeen), ms(row.LastNotice), pendingText(row)); err != nil {
				return dbErr("insert identity", err)
			}
		}
		out, err = queryOne(ctx, q, "identity", scanIdentity,
			`SELECT `+identityCols+` FROM channel_identities WHERE channel = ? AND external_id = ?`, id.Channel, id.ExternalID)
		return err
	})
	return out, err
}

func (r identityRepo) Get(ctx context.Context, channel, externalID string) (*core.ChannelIdentity, error) {
	return queryOne(ctx, r.h.rq, "channel identity", scanIdentity,
		`SELECT `+identityCols+` FROM channel_identities WHERE channel = ? AND external_id = ?`, channel, externalID)
}

func (r identityRepo) Update(ctx context.Context, id *core.ChannelIdentity) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE channel_identities SET username = ?, display_name = ?, status = ?,
		principal_id = ?, admitted_via = ?, can_approve = ?, membership = ?, membership_at = ?, dm_chat_id = ?,
		last_seen_at = ?, last_notice_at = ?, greet = ?, first_text = ? WHERE channel = ? AND external_id = ?`,
		id.Username, id.DisplayName, string(id.Status), nullStr(id.Principal), string(id.Via), b2i(id.CanApprove),
		string(id.Member), ms(id.MemberAt), id.DMChat, ms(id.LastSeen), ms(id.LastNotice), id.Greet, pendingText(*id),
		id.Channel, id.ExternalID)
	return mustAffect("update identity", res, err)
}

// pendingText keeps a sender's first words only while an admin may still
// need them to decide; a teammate's or blocked sender's message stays theirs.
func pendingText(id core.ChannelIdentity) string {
	if id.Status != core.IdentityPending {
		return ""
	}
	return id.FirstText
}

func (r identityRepo) ByPrincipal(ctx context.Context, p core.PrincipalID) (*core.ChannelIdentity, error) {
	return queryOne(ctx, r.h.rq, "channel identity", scanIdentity,
		`SELECT `+identityCols+` FROM channel_identities WHERE principal_id = ? ORDER BY last_seen_at DESC LIMIT 1`, string(p))
}

func (r identityRepo) ResetMembership(ctx context.Context, channel string) error {
	_, err := r.h.wq.ExecContext(ctx, `UPDATE channel_identities SET membership_at = 0 WHERE channel = ? AND membership_at <> 0`, channel)
	return dbErr("reset membership", err)
}

func (r identityRepo) List(ctx context.Context, status core.IdentityStatus) ([]*core.ChannelIdentity, error) {
	return queryAll(ctx, r.h.rq, "list identities", scanIdentity,
		`SELECT `+identityCols+` FROM channel_identities WHERE ? = '' OR status = ? ORDER BY first_seen_at, channel, external_id`,
		string(status), string(status))
}

func (r identityRepo) Delete(ctx context.Context, channel, externalID string) error {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM channel_identities WHERE channel = ? AND external_id = ?`, channel, externalID)
	return mustAffect("delete identity", res, err)
}

const authSessionCols = `id_hash, user_id, csrf, ip, ua, created_at, last_seen_at, idle_expires_at, expires_at`

func scanAuthSession(sc scanner) (*core.AuthSession, error) {
	var s core.AuthSession
	var created, last, idle, exp int64
	if err := sc.Scan(&s.IDHash, &s.User, &s.CSRF, &s.IP, &s.UA, &created, &last, &idle, &exp); err != nil {
		return nil, err
	}
	s.CreatedAt, s.LastSeen, s.IdleExpires, s.Expires = fromMS(created), fromMS(last), fromMS(idle), fromMS(exp)
	return &s, nil
}

type authSessionRepo struct{ h *handle }

func (r authSessionRepo) Create(ctx context.Context, s *core.AuthSession) error {
	if len(s.IDHash) == 0 {
		return invalid("auth session id_hash")
	}
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO auth_sessions(`+authSessionCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		s.IDHash, string(s.User), s.CSRF, s.IP, s.UA, ms(s.CreatedAt), ms(s.LastSeen), ms(s.IdleExpires), ms(s.Expires))
	return dbErr("create auth session", err)
}

func (r authSessionRepo) Get(ctx context.Context, idHash []byte) (*core.AuthSession, error) {
	return queryOne(ctx, r.h.rq, "auth session", scanAuthSession, `SELECT `+authSessionCols+` FROM auth_sessions WHERE id_hash = ?`, idHash)
}

func (r authSessionRepo) Touch(ctx context.Context, idHash []byte, lastSeen, idleExpires time.Time) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE auth_sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id_hash = ?`,
		ms(lastSeen), ms(idleExpires), idHash)
	return mustAffect("touch auth session", res, err)
}

func (r authSessionRepo) Delete(ctx context.Context, idHash []byte) error {
	_, err := r.h.wq.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id_hash = ?`, idHash)
	return dbErr("delete auth session", err)
}

func (r authSessionRepo) DeleteUser(ctx context.Context, u core.UserID) error {
	_, err := r.h.wq.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, string(u))
	return dbErr("delete user sessions", err)
}

func (r authSessionRepo) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at <= ? OR idle_expires_at <= ?`, ms(now), ms(now))
	if err != nil {
		return 0, dbErr("delete expired sessions", err)
	}
	n, err := res.RowsAffected()
	return int(n), dbErr("delete expired sessions", err)
}

const tokenCols = `id, user_id, name, secret_hash, scopes, on_approval, rate_per_min, created_at, last_used_at, expires_at, revoked_at`

func scanToken(sc scanner) (*core.TokenRecord, error) {
	var t core.TokenRecord
	var scopes string
	var created, used, exp, revoked int64
	if err := sc.Scan(&t.ID, &t.User, &t.Name, &t.SecretHash, &scopes, &t.OnApproval, &t.RatePerMin, &created, &used, &exp, &revoked); err != nil {
		return nil, err
	}
	if err := decodeJSON("api_tokens.scopes", scopes, &t.Scopes); err != nil {
		return nil, err
	}
	t.CreatedAt, t.LastUsed, t.Expires, t.Revoked = fromMS(created), fromMS(used), fromMS(exp), fromMS(revoked)
	return &t, nil
}

type tokenRepo struct{ h *handle }

func (r tokenRepo) Create(ctx context.Context, t *core.TokenRecord) error {
	if len(t.SecretHash) == 0 {
		return invalid("token secret_hash")
	}
	id := idOr(t.ID, core.PrefixToken)
	scopes := t.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	sj, err := jsonText("api_tokens.scopes", scopes)
	if err != nil {
		return err
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO api_tokens(`+tokenCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, string(t.User), t.Name, t.SecretHash, sj, t.OnApproval, t.RatePerMin, ms(t.CreatedAt), ms(t.LastUsed),
		ms(t.Expires), ms(t.Revoked)); err != nil {
		return dbErr("create token", err)
	}
	t.ID = id
	return nil
}

func (r tokenRepo) Get(ctx context.Context, id string) (*core.TokenRecord, error) {
	return queryOne(ctx, r.h.rq, "token", scanToken, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id)
}

func (r tokenRepo) List(ctx context.Context, u core.UserID) ([]*core.TokenRecord, error) {
	return queryAll(ctx, r.h.rq, "list tokens", scanToken, `SELECT `+tokenCols+` FROM api_tokens WHERE user_id = ? ORDER BY created_at, id`, string(u))
}

func (r tokenRepo) Revoke(ctx context.Context, u core.UserID, id string, at time.Time) error {
	if at.IsZero() {
		at = r.h.now()
	}
	res, err := r.h.wq.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND user_id = ?`, ms(at), id, string(u))
	return mustAffect("revoke token", res, err)
}

func (r tokenRepo) Touch(ctx context.Context, id string, at time.Time) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ms(at), id)
	return mustAffect("touch token", res, err)
}

// isNoRows reports a missing row from a QueryRow scan.
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
