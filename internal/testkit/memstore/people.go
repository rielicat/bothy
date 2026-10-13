package memstore

import (
	"bytes"
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

type metaRepo struct{ s *store }

func (r metaRepo) Get(_ context.Context, k string) (string, error) {
	st, unlock := r.s.begin()
	defer unlock()
	v, ok := st.meta[k]
	if !ok {
		return "", notFound("meta " + k)
	}
	return v, nil
}

func (r metaRepo) Set(_ context.Context, k, v string) error {
	st, unlock := r.s.begin()
	defer unlock()
	st.meta[k] = v
	return nil
}

func (r metaRepo) Delete(_ context.Context, k string) error {
	st, unlock := r.s.begin()
	defer unlock()
	delete(st.meta, k)
	return nil
}

func normUser(u core.User) core.User {
	u.LockedUntil = ms(u.LockedUntil)
	u.CreatedAt = ms(u.CreatedAt)
	u.UpdatedAt = ms(u.UpdatedAt)
	return u
}

func validRole(r core.Role) bool {
	return oneOf(r, core.RoleOwner, core.RoleAdmin, core.RoleMember)
}

// checkUser enforces users.role, username COLLATE NOCASE UNIQUE and
// users_one_owner against every row except u itself.
func (st *state) checkUser(u core.User) error {
	if !validRole(u.Role) {
		return invalid("users.role")
	}
	name := asciiLower(u.Username)
	for id, o := range st.users {
		if id == u.ID {
			continue
		}
		if asciiLower(o.Username) == name {
			return conflict("users.username")
		}
		if u.Role == core.RoleOwner && o.Role == core.RoleOwner {
			return conflict("users_one_owner")
		}
	}
	return nil
}

type userRepo struct{ s *store }

func (r userRepo) Create(_ context.Context, u *core.User) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *u
	row.ID = idOr(row.ID, core.PrefixUser)
	pid := core.UserPrincipal(row.ID)
	if _, ok := st.users[row.ID]; ok {
		return conflict("users.id")
	}
	if _, ok := st.principals[pid]; ok {
		return conflict("principals.id")
	}
	if err := st.checkUser(row); err != nil {
		return err
	}
	st.users[row.ID] = normUser(row)
	st.principals[pid] = normPrincipal(core.Principal{
		ID:          pid,
		Kind:        core.PrincipalUser,
		User:        row.ID,
		DisplayName: row.DisplayName,
		Workspace:   core.WorkspaceID(core.NewID(core.PrefixWorkspace)),
		ToolProfile: "standard",
		CreatedAt:   row.CreatedAt,
	})
	u.ID = row.ID
	return nil
}

func (r userRepo) Get(_ context.Context, id core.UserID) (*core.User, error) {
	st, unlock := r.s.begin()
	defer unlock()
	u, ok := st.users[id]
	if !ok {
		return nil, notFound("user")
	}
	return &u, nil
}

func (r userRepo) ByUsername(_ context.Context, name string) (*core.User, error) {
	st, unlock := r.s.begin()
	defer unlock()
	name = asciiLower(name)
	for _, u := range st.users {
		if asciiLower(u.Username) == name {
			return &u, nil
		}
	}
	return nil, notFound("user")
}

func (r userRepo) List(context.Context) ([]*core.User, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := slices.Collect(maps.Values(st.users))
	byCreated(out, func(u core.User) (time.Time, string) { return u.CreatedAt, string(u.ID) })
	return ptrs(out), nil
}

func (r userRepo) Update(_ context.Context, u *core.User) error {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.users[u.ID]
	if !ok {
		return notFound("user")
	}
	if err := st.checkUser(*u); err != nil {
		return err
	}
	row := normUser(*u)
	row.CreatedAt = cur.CreatedAt
	st.users[u.ID] = row
	return nil
}

func (r userRepo) RecordLogin(_ context.Context, id core.UserID, ok bool, lockUntil time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	u, found := st.users[id]
	if !found {
		return notFound("user")
	}
	if ok {
		u.FailedLogins = 0
	} else {
		u.FailedLogins++
	}
	u.LockedUntil = ms(lockUntil)
	st.users[id] = u
	return nil
}

// Delete mirrors the schema's ON DELETE chains from users: CASCADE through
// principals and everything they own, SET NULL on schedules.session_id and
// runs.schedule_id, and the identity CHECKs that make SET NULL fail for
// linked users and guest principals.
func (r userRepo) Delete(_ context.Context, id core.UserID) error {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.users[id]; !ok {
		return notFound("user")
	}
	ps := map[core.PrincipalID]bool{}
	for pid, p := range st.principals {
		if p.User == id {
			ps[pid] = true
		}
	}
	for pid, sp := range st.spaces {
		if sp.Owner == id {
			ps[pid] = true
		}
	}
	for _, row := range st.identities {
		if (row.id.Status == core.IdentityLinked && row.id.User == id) || (row.id.Status == core.IdentityGuest && ps[row.id.Principal]) {
			return invalid("channel_identities CHECK")
		}
	}
	for k, row := range st.identities {
		if row.id.User == id {
			row.id.User = ""
		}
		if ps[row.id.Principal] {
			row.id.Principal = ""
		}
		st.identities[k] = row
	}
	sessions := map[core.SessionID]bool{}
	for sid, sess := range st.sessions {
		if ps[sess.Principal] {
			sessions[sid] = true
			delete(st.sessions, sid)
			delete(st.lastSeq, sid)
		}
	}
	maps.DeleteFunc(st.sessionURLs, func(k urlKey, _ string) bool { return sessions[k.session] })
	st.messages = slices.DeleteFunc(st.messages, func(m core.Message) bool { return sessions[m.Session] })
	st.pins = slices.DeleteFunc(st.pins, func(p core.Pin) bool { return sessions[p.Session] })
	schedules := map[core.ScheduleID]bool{}
	for sid, sc := range st.schedules {
		switch {
		case ps[sc.Principal]:
			schedules[sid] = true
			delete(st.schedules, sid)
		case sessions[sc.Session]:
			sc.Session = ""
			st.schedules[sid] = sc
		}
	}
	runs := map[core.RunID]bool{}
	for rid, run := range st.runs {
		switch {
		case sessions[run.Session] || ps[run.Principal]:
			runs[rid] = true
			delete(st.runs, rid)
		case schedules[run.Schedule]:
			run.Schedule = ""
			st.runs[rid] = run
		}
	}
	st.llmCalls = slices.DeleteFunc(st.llmCalls, func(c llmRow) bool { return c.run != "" && runs[c.run] })
	maps.DeleteFunc(st.toolCalls, func(_ string, tc toolCallRow) bool { return runs[tc.tc.Run] })
	approvals := map[core.ApprovalID]bool{}
	for aid, a := range st.approvals {
		if runs[a.Run] {
			approvals[aid] = true
			delete(st.approvals, aid)
		}
	}
	st.cardMsgs = slices.DeleteFunc(st.cardMsgs, func(c cardMsgRow) bool { return approvals[c.approval] })
	maps.DeleteFunc(st.grants, func(_ core.GrantID, g grantRow) bool { return ps[g.g.Principal] })
	st.memEvents = slices.DeleteFunc(st.memEvents, func(e core.MemoryEvent) bool { return ps[e.Principal] })
	maps.DeleteFunc(st.uploads, func(_ string, u core.Upload) bool { return ps[u.Principal] })
	maps.DeleteFunc(st.pairings, func(_ string, p pairingRow) bool { return p.p.User == id })
	maps.DeleteFunc(st.authSess, func(_ string, a core.AuthSession) bool { return a.User == id })
	maps.DeleteFunc(st.tokens, func(_ string, t core.TokenRecord) bool { return t.User == id })
	maps.DeleteFunc(st.spaces, func(pid core.PrincipalID, _ core.Space) bool { return ps[pid] })
	maps.DeleteFunc(st.principals, func(pid core.PrincipalID, _ core.Principal) bool { return ps[pid] })
	delete(st.users, id)
	return nil
}

func (r userRepo) Count(context.Context) (int, error) {
	st, unlock := r.s.begin()
	defer unlock()
	return len(st.users), nil
}

func normPrincipal(p core.Principal) core.Principal {
	p.CreatedAt = ms(p.CreatedAt)
	return p
}

func validToolProfile(p string) bool { return oneOf(p, "standard", "restricted") }

type principalRepo struct{ s *store }

func (r principalRepo) Get(_ context.Context, v core.Viewer, id core.PrincipalID) (*core.Principal, error) {
	st, unlock := r.s.begin()
	defer unlock()
	p, ok := st.principals[id]
	if !ok || !v.Can(id) {
		return nil, notFound("principal")
	}
	return &p, nil
}

func (r principalRepo) ForUser(_ context.Context, u core.UserID) ([]*core.Principal, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var own, spaces []core.Principal
	for _, p := range st.principals {
		switch {
		case p.User != u:
		case p.Kind == core.PrincipalUser:
			own = append(own, p)
		default:
			spaces = append(spaces, p)
		}
	}
	byCreated(spaces, func(p core.Principal) (time.Time, string) { return p.CreatedAt, string(p.ID) })
	return ptrs(append(own, spaces...)), nil
}

func (r principalRepo) Update(_ context.Context, p *core.Principal) error {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.principals[p.ID]
	if !ok {
		return notFound("principal")
	}
	if !validToolProfile(p.ToolProfile) {
		return invalid("principals.tool_profile")
	}
	cur.DisplayName = p.DisplayName
	cur.ToolProfile = p.ToolProfile
	st.principals[p.ID] = cur
	return nil
}

// normSpace drops Chat.ThreadID: spaces store channel, chat_id and chat_kind.
func normSpace(s core.Space) core.Space {
	s.Chat = core.ChatRef{Channel: s.Chat.Channel, ChatID: s.Chat.ChatID, Kind: s.Chat.Kind}
	s.CreatedAt = ms(s.CreatedAt)
	s.UpdatedAt = ms(s.UpdatedAt)
	return s
}

func (st *state) checkSpace(s core.Space) error {
	if !oneOf(s.Kind, "group", "guest") {
		return invalid("spaces.kind")
	}
	if !oneOf(s.Chat.Kind, core.ChatDM, core.ChatGroup) {
		return invalid("spaces.chat_kind")
	}
	if _, ok := st.users[s.Owner]; !ok {
		return invalid("spaces.owner_user_id: no such user")
	}
	for id, o := range st.spaces {
		if id != s.Principal && o.Chat.Channel == s.Chat.Channel && o.Chat.ChatID == s.Chat.ChatID {
			return conflict("spaces (channel, chat_id)")
		}
	}
	return nil
}

type spaceRepo struct{ s *store }

func (r spaceRepo) Create(_ context.Context, s *core.Space, p *core.Principal) error {
	st, unlock := r.s.begin()
	defer unlock()
	pr := *p
	pr.ID = idOr(cmp.Or(pr.ID, s.Principal), core.PrefixSpace)
	pr.Kind = cmp.Or(pr.Kind, core.PrincipalSpace)
	pr.User = cmp.Or(pr.User, s.Owner)
	pr.Workspace = idOr(pr.Workspace, core.PrefixWorkspace)
	pr.ToolProfile = cmp.Or(pr.ToolProfile, "restricted")
	sp := *s
	sp.Principal = pr.ID
	switch {
	case !oneOf(pr.Kind, core.PrincipalUser, core.PrincipalSpace):
		return invalid("principals.kind")
	case pr.Kind != core.PrincipalSpace && core.UserID(pr.ID) != pr.User:
		return invalid("principals: user principal id must equal user_id")
	case !validToolProfile(pr.ToolProfile):
		return invalid("principals.tool_profile")
	}
	if _, ok := st.users[pr.User]; !ok {
		return invalid("principals.user_id: no such user")
	}
	if _, ok := st.principals[pr.ID]; ok {
		return conflict("principals.id")
	}
	for _, o := range st.principals {
		if o.Workspace == pr.Workspace {
			return conflict("principals.workspace_id")
		}
	}
	if err := st.checkSpace(sp); err != nil {
		return err
	}
	st.principals[pr.ID] = normPrincipal(pr)
	st.spaces[sp.Principal] = normSpace(sp)
	*p = pr
	s.Principal = pr.ID
	return nil
}

func (r spaceRepo) ByChat(_ context.Context, channel, chatID string) (*core.Space, error) {
	st, unlock := r.s.begin()
	defer unlock()
	for _, s := range st.spaces {
		if s.Chat.Channel == channel && s.Chat.ChatID == chatID {
			return &s, nil
		}
	}
	return nil, notFound("space")
}

func (r spaceRepo) Get(_ context.Context, v core.Viewer, id core.PrincipalID) (*core.Space, error) {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.spaces[id]
	if !ok || !v.Can(id) {
		return nil, notFound("space")
	}
	return &s, nil
}

func (r spaceRepo) List(_ context.Context, v core.Viewer) ([]*core.Space, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Space
	for id, s := range st.spaces {
		if v.Can(id) {
			out = append(out, s)
		}
	}
	byCreated(out, func(s core.Space) (time.Time, string) { return s.CreatedAt, string(s.Principal) })
	return ptrs(out), nil
}

func (r spaceRepo) Update(_ context.Context, s *core.Space) error {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.spaces[s.Principal]
	if !ok {
		return notFound("space")
	}
	if err := st.checkSpace(*s); err != nil {
		return err
	}
	row := normSpace(*s)
	row.CreatedAt = cur.CreatedAt
	st.spaces[s.Principal] = row
	return nil
}

func normIdentity(id core.ChannelIdentity) core.ChannelIdentity {
	id.FirstSeen = ms(id.FirstSeen)
	id.LastSeen = ms(id.LastSeen)
	id.LastNotice = ms(id.LastNotice)
	return id
}

func (st *state) checkIdentity(id core.ChannelIdentity) error {
	switch {
	case !oneOf(id.Status, core.IdentityPending, core.IdentityLinked, core.IdentityGuest, core.IdentityBlocked):
		return invalid("channel_identities.status")
	case id.Status == core.IdentityLinked && id.User == "":
		return invalid("channel_identities: linked requires user_id")
	case id.Status == core.IdentityGuest && id.Principal == "":
		return invalid("channel_identities: guest requires principal_id")
	}
	if _, ok := st.users[id.User]; id.User != "" && !ok {
		return invalid("channel_identities.user_id: no such user")
	}
	if _, ok := st.principals[id.Principal]; id.Principal != "" && !ok {
		return invalid("channel_identities.principal_id: no such principal")
	}
	return nil
}

type identityRepo struct{ s *store }

// Touch inserts unknown senders as pending; for known ones it refreshes only
// names and last_seen, never status or links.
func (r identityRepo) Touch(_ context.Context, id *core.ChannelIdentity) (*core.ChannelIdentity, error) {
	st, unlock := r.s.begin()
	defer unlock()
	k := identKey{id.Channel, id.ExternalID}
	if cur, ok := st.identities[k]; ok {
		cur.id.Username = id.Username
		cur.id.DisplayName = id.DisplayName
		cur.id.LastSeen = ms(id.LastSeen)
		st.identities[k] = cur
		out := cur.id
		return &out, nil
	}
	row := *id
	row.Status = cmp.Or(row.Status, core.IdentityPending)
	if row.FirstSeen.IsZero() {
		row.FirstSeen = row.LastSeen
	}
	if err := st.checkIdentity(row); err != nil {
		return nil, err
	}
	row = normIdentity(row)
	st.identities[k] = identRow{id: row, n: st.next()}
	return &row, nil
}

func (r identityRepo) Get(_ context.Context, channel, externalID string) (*core.ChannelIdentity, error) {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.identities[identKey{channel, externalID}]
	if !ok {
		return nil, notFound("channel identity")
	}
	return &cur.id, nil
}

func (r identityRepo) Update(_ context.Context, id *core.ChannelIdentity) error {
	st, unlock := r.s.begin()
	defer unlock()
	k := identKey{id.Channel, id.ExternalID}
	cur, ok := st.identities[k]
	if !ok {
		return notFound("channel identity")
	}
	row := cur.id
	row.Status = id.Status
	row.User = id.User
	row.Principal = id.Principal
	row.DMChat = id.DMChat
	row.LastNotice = ms(id.LastNotice)
	if err := st.checkIdentity(row); err != nil {
		return err
	}
	cur.id = row
	st.identities[k] = cur
	return nil
}

func (r identityRepo) ForUser(_ context.Context, u core.UserID) ([]*core.ChannelIdentity, error) {
	return r.filter(func(id core.ChannelIdentity) bool { return id.User == u })
}

func (r identityRepo) List(_ context.Context, status core.IdentityStatus) ([]*core.ChannelIdentity, error) {
	return r.filter(func(id core.ChannelIdentity) bool { return status == "" || id.Status == status })
}

func (r identityRepo) filter(keep func(core.ChannelIdentity) bool) ([]*core.ChannelIdentity, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var rows []identRow
	for _, row := range st.identities {
		if keep(row.id) {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b identRow) int { return cmp.Compare(a.n, b.n) })
	out := make([]*core.ChannelIdentity, len(rows))
	for i := range rows {
		out[i] = &rows[i].id
	}
	return out, nil
}

func (r identityRepo) Delete(_ context.Context, channel, externalID string) error {
	st, unlock := r.s.begin()
	defer unlock()
	k := identKey{channel, externalID}
	if _, ok := st.identities[k]; !ok {
		return notFound("channel identity")
	}
	delete(st.identities, k)
	return nil
}

func normPairing(p core.Pairing) core.Pairing {
	p.CodeHash = bytes.Clone(p.CodeHash)
	p.ExpiresAt = ms(p.ExpiresAt)
	p.UsedAt = ms(p.UsedAt)
	return p
}

type pairingRepo struct{ s *store }

func (r pairingRepo) Create(_ context.Context, p *core.Pairing) error {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case len(p.CodeHash) == 0:
		return invalid("pairing_codes.code_hash")
	case !oneOf(p.Purpose, core.PairLinkIdentity, core.PairApproveSender):
		return invalid("pairing_codes.purpose")
	}
	if _, ok := st.users[p.User]; p.User != "" && !ok {
		return invalid("pairing_codes.user_id: no such user")
	}
	k := string(p.CodeHash)
	if _, ok := st.pairings[k]; ok {
		return conflict("pairing_codes.code_hash")
	}
	st.pairings[k] = pairingRow{p: normPairing(*p), n: st.next()}
	return nil
}

func (r pairingRepo) Redeem(_ context.Context, codeHash []byte, purpose core.PairingPurpose, now time.Time) (*core.Pairing, error) {
	st, unlock := r.s.begin()
	defer unlock()
	k := string(codeHash)
	row, ok := st.pairings[k]
	if !ok || row.p.Purpose != purpose || !row.p.UsedAt.IsZero() || !row.p.ExpiresAt.After(now) {
		return nil, notFound("pairing code")
	}
	row.p.UsedAt = ms(now)
	st.pairings[k] = row
	out := normPairing(row.p)
	return &out, nil
}

func (r pairingRepo) ForIdentity(_ context.Context, channel, externalID string, purpose core.PairingPurpose, now time.Time) (*core.Pairing, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var best *pairingRow
	for _, row := range st.pairings {
		p := row.p
		if p.Channel != channel || p.ExternalID != externalID || p.Purpose != purpose ||
			!p.UsedAt.IsZero() || !p.ExpiresAt.After(now) {
			continue
		}
		if best == nil || row.n > best.n {
			best = &row
		}
	}
	if best == nil {
		return nil, notFound("pairing code")
	}
	out := normPairing(best.p)
	return &out, nil
}

func normAuthSession(s core.AuthSession) core.AuthSession {
	s.IDHash = bytes.Clone(s.IDHash)
	s.CreatedAt = ms(s.CreatedAt)
	s.LastSeen = ms(s.LastSeen)
	s.IdleExpires = ms(s.IdleExpires)
	s.Expires = ms(s.Expires)
	return s
}

type authSessionRepo struct{ s *store }

func (r authSessionRepo) Create(_ context.Context, s *core.AuthSession) error {
	st, unlock := r.s.begin()
	defer unlock()
	if len(s.IDHash) == 0 {
		return invalid("auth_sessions.id_hash")
	}
	if _, ok := st.users[s.User]; !ok {
		return invalid("auth_sessions.user_id: no such user")
	}
	k := string(s.IDHash)
	if _, ok := st.authSess[k]; ok {
		return conflict("auth_sessions.id_hash")
	}
	st.authSess[k] = normAuthSession(*s)
	return nil
}

func (r authSessionRepo) Get(_ context.Context, idHash []byte) (*core.AuthSession, error) {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.authSess[string(idHash)]
	if !ok {
		return nil, notFound("auth session")
	}
	s = normAuthSession(s)
	return &s, nil
}

func (r authSessionRepo) Touch(_ context.Context, idHash []byte, lastSeen, idleExpires time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	k := string(idHash)
	s, ok := st.authSess[k]
	if !ok {
		return notFound("auth session")
	}
	s.LastSeen = ms(lastSeen)
	s.IdleExpires = ms(idleExpires)
	st.authSess[k] = s
	return nil
}

func (r authSessionRepo) Delete(_ context.Context, idHash []byte) error {
	st, unlock := r.s.begin()
	defer unlock()
	delete(st.authSess, string(idHash))
	return nil
}

func (r authSessionRepo) DeleteUser(_ context.Context, u core.UserID) error {
	st, unlock := r.s.begin()
	defer unlock()
	for k, s := range st.authSess {
		if s.User == u {
			delete(st.authSess, k)
		}
	}
	return nil
}

func (r authSessionRepo) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	st, unlock := r.s.begin()
	defer unlock()
	n := 0
	for k, s := range st.authSess {
		if !s.Expires.After(now) || !s.IdleExpires.After(now) {
			delete(st.authSess, k)
			n++
		}
	}
	return n, nil
}

func normToken(t core.TokenRecord) (core.TokenRecord, error) {
	scopes, err := jsonCol("api_tokens.scopes", t.Scopes)
	if err != nil {
		return t, err
	}
	t.Scopes = scopes
	t.SecretHash = bytes.Clone(t.SecretHash)
	t.CreatedAt = ms(t.CreatedAt)
	t.LastUsed = ms(t.LastUsed)
	t.Expires = ms(t.Expires)
	t.Revoked = ms(t.Revoked)
	return t, nil
}

type tokenRepo struct{ s *store }

func (r tokenRepo) Create(_ context.Context, t *core.TokenRecord) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *t
	row.ID = idOr(row.ID, core.PrefixToken)
	switch {
	case len(row.SecretHash) == 0:
		return invalid("api_tokens.secret_hash")
	case !oneOf(row.OnApproval, "deny", "wait"):
		return invalid("api_tokens.on_approval")
	}
	if _, ok := st.users[row.User]; !ok {
		return invalid("api_tokens.user_id: no such user")
	}
	if _, ok := st.tokens[row.ID]; ok {
		return conflict("api_tokens.id")
	}
	for _, o := range st.tokens {
		if bytes.Equal(o.SecretHash, row.SecretHash) {
			return conflict("api_tokens.secret_hash")
		}
	}
	row, err := normToken(row)
	if err != nil {
		return err
	}
	st.tokens[row.ID] = row
	t.ID = row.ID
	return nil
}

func (r tokenRepo) Get(_ context.Context, id string) (*core.TokenRecord, error) {
	st, unlock := r.s.begin()
	defer unlock()
	t, ok := st.tokens[id]
	if !ok {
		return nil, notFound("token")
	}
	t = must(normToken(t))
	return &t, nil
}

func (r tokenRepo) List(_ context.Context, u core.UserID) ([]*core.TokenRecord, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.TokenRecord
	for _, t := range st.tokens {
		if t.User == u {
			out = append(out, must(normToken(t)))
		}
	}
	byCreated(out, func(t core.TokenRecord) (time.Time, string) { return t.CreatedAt, t.ID })
	return ptrs(out), nil
}

func (r tokenRepo) Revoke(_ context.Context, u core.UserID, id string, at time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	t, ok := st.tokens[id]
	if !ok || t.User != u {
		return notFound("token")
	}
	t.Revoked = ms(at)
	st.tokens[id] = t
	return nil
}

func (r tokenRepo) Touch(_ context.Context, id string, at time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	t, ok := st.tokens[id]
	if !ok {
		return notFound("token")
	}
	t.LastUsed = ms(at)
	st.tokens[id] = t
	return nil
}
