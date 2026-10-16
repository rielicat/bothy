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
	u.LastLoginAt = ms(u.LastLoginAt)
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
	row.CreatedAt, row.LastLoginAt = cur.CreatedAt, cur.LastLoginAt
	st.users[u.ID] = row
	return nil
}

func (r userRepo) SetProfile(_ context.Context, u *core.User) error {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.users[u.ID]
	if !ok {
		return notFound("user")
	}
	cur.DisplayName, cur.Timezone, cur.UpdatedAt = u.DisplayName, u.Timezone, ms(u.UpdatedAt)
	st.users[u.ID] = cur
	return nil
}

func (r userRepo) SignedIn(_ context.Context, id core.UserID, at time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	u, found := st.users[id]
	if !found {
		return notFound("user")
	}
	u.LastLoginAt, u.FailedLogins, u.LockedUntil = ms(at), 0, time.Time{}
	st.users[id] = u
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
// the user's principal and everything it owns, and SET NULL on
// schedules.session_id and runs.schedule_id.
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
	if err := st.deletePrincipals(ps); err != nil {
		return err
	}
	maps.DeleteFunc(st.authSess, func(_ string, a core.AuthSession) bool { return a.User == id })
	maps.DeleteFunc(st.tokens, func(_ string, t core.TokenRecord) bool { return t.User == id })
	maps.DeleteFunc(st.signIns, func(_ signInKey, s core.SignIn) bool { return s.User == id })
	delete(st.users, id)
	return nil
}

// deletePrincipals mirrors the ON DELETE chains from principals: CASCADE
// through everything they own, SET NULL on schedules.session_id,
// runs.schedule_id and channel_identities.principal_id, where the teammate
// CHECK makes SET NULL fail.
func (st *state) deletePrincipals(ps map[core.PrincipalID]bool) error {
	for _, row := range st.identities {
		if row.id.Status == core.IdentityTeammate && ps[row.id.Principal] {
			return invalid("channel_identities CHECK: a teammate needs principal_id")
		}
	}
	for k, row := range st.identities {
		if ps[row.id.Principal] {
			row.id.Principal = ""
			st.identities[k] = row
		}
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
	maps.DeleteFunc(st.votes, func(k voteKey, _ core.ApprovalVote) bool { return approvals[k.approval] })
	maps.DeleteFunc(st.deploys, func(_ string, d deployRow) bool { return ps[d.d.Principal] })
	maps.DeleteFunc(st.grants, func(_ core.GrantID, g grantRow) bool { return ps[g.g.Principal] })
	st.memEvents = slices.DeleteFunc(st.memEvents, func(e core.MemoryEvent) bool { return ps[e.Principal] })
	maps.DeleteFunc(st.uploads, func(_ string, u core.Upload) bool { return ps[u.Principal] })
	maps.DeleteFunc(st.spaces, func(pid core.PrincipalID, _ core.Space) bool { return ps[pid] })
	maps.DeleteFunc(st.principals, func(pid core.PrincipalID, _ core.Principal) bool { return ps[pid] })
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
	var own []core.Principal
	for _, p := range st.principals {
		if u != "" && p.User == u {
			own = append(own, p)
		}
	}
	return ptrs(own), nil
}

func (r principalRepo) Team(context.Context) ([]*core.Principal, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Principal
	for _, p := range st.principals {
		if p.User == "" {
			out = append(out, p)
		}
	}
	byCreated(out, func(p core.Principal) (time.Time, string) { return p.CreatedAt, string(p.ID) })
	return ptrs(out), nil
}

func (r principalRepo) Delete(_ context.Context, id core.PrincipalID) error {
	st, unlock := r.s.begin()
	defer unlock()
	p, ok := st.principals[id]
	switch {
	case !ok:
		return notFound("principal")
	case p.User != "":
		return invalid("delete principal: an account's principal goes only with the account")
	}
	return st.deletePrincipals(map[core.PrincipalID]bool{id: true})
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
	if cur.Kind == core.PrincipalPerson && p.ToolProfile != "standard" {
		return invalid("principals CHECK: a person is standard")
	}
	cur.DisplayName = p.DisplayName
	cur.ToolProfile = p.ToolProfile
	st.principals[p.ID] = cur
	return nil
}

// normSpace drops Chat.ThreadID: spaces store channel, chat_id and chat_kind.
func normSpace(s core.Space) core.Space {
	s.Chat = core.ChatRef{Channel: s.Chat.Channel, ChatID: s.Chat.ChatID, Kind: s.Chat.Kind}
	s.ReviewedAt = ms(s.ReviewedAt)
	s.CreatedAt = ms(s.CreatedAt)
	s.UpdatedAt = ms(s.UpdatedAt)
	return s
}

func (st *state) checkSpace(s core.Space) error {
	if !oneOf(s.Kind, core.SpaceGroup, core.SpacePerson) {
		return invalid("spaces.kind")
	}
	if !oneOf(s.Chat.Kind, core.ChatDM, core.ChatGroup) {
		return invalid("spaces.chat_kind")
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
	if pr.Kind == "" {
		pr.Kind = core.PrincipalSpace
		if s.Kind == core.SpacePerson {
			pr.Kind = core.PrincipalPerson
		}
	}
	pr.User = ""
	pr.Workspace = idOr(pr.Workspace, core.PrefixWorkspace)
	pr.ToolProfile = cmp.Or(pr.ToolProfile, "restricted")
	if pr.Kind == core.PrincipalPerson {
		pr.ToolProfile = "standard"
	}
	sp := *s
	sp.Principal = pr.ID
	switch {
	case !oneOf(pr.Kind, core.PrincipalSpace, core.PrincipalPerson):
		return invalid("principals CHECK: a space's principal is owned by the team")
	case !validToolProfile(pr.ToolProfile):
		return invalid("principals.tool_profile")
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
	// Update never moves a space: kind, channel and chat stay as created.
	upd := *s
	upd.Kind, upd.Chat = cur.Kind, cur.Chat
	if err := st.checkSpace(upd); err != nil {
		return err
	}
	row := normSpace(upd)
	row.CreatedAt = cur.CreatedAt
	st.spaces[s.Principal] = row
	return nil
}

func normIdentity(id core.ChannelIdentity) core.ChannelIdentity {
	id.MemberAt = ms(id.MemberAt)
	id.FirstSeen = ms(id.FirstSeen)
	id.LastSeen = ms(id.LastSeen)
	id.LastNotice = ms(id.LastNotice)
	if id.Status != core.IdentityPending {
		id.FirstText = ""
	}
	return id
}

func (st *state) checkIdentity(id core.ChannelIdentity) error {
	switch {
	case !oneOf(id.Status, core.IdentityPending, core.IdentityTeammate, core.IdentityBlocked):
		return invalid("channel_identities.status")
	case !oneOf(id.Via, "", core.ViaMembership, core.ViaAdmin):
		return invalid("channel_identities.admitted_via")
	case !oneOf(id.Member, core.MemberUnchecked, core.MemberUnknown, core.MemberYes, core.MemberOutsider):
		return invalid("channel_identities.membership")
	case !oneOf(id.Greet, "", core.GreetHello, core.GreetMoved):
		return invalid("channel_identities.greet")
	case id.Status == core.IdentityTeammate && (id.Principal == "" || id.Via == ""):
		return invalid("channel_identities CHECK: a teammate needs principal_id and admitted_via")
	case id.CanApprove && id.Status != core.IdentityTeammate:
		return invalid("channel_identities CHECK: can_approve needs a teammate")
	}
	if _, ok := st.principals[id.Principal]; id.Principal != "" && !ok {
		return invalid("channel_identities.principal_id: no such principal")
	}
	return nil
}

type identityRepo struct{ s *store }

// Touch inserts unknown senders as pending with id.Member as their first
// verdict; for known ones it refreshes only names and last_seen, never
// status or verdicts.
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
	row.Status, row.Principal, row.Via, row.CanApprove, row.Greet = core.IdentityPending, "", "", false, core.GreetHello
	if row.FirstSeen.IsZero() {
		row.FirstSeen = row.LastSeen
	}
	if row.Member != core.MemberUnchecked && row.MemberAt.IsZero() {
		row.MemberAt = row.LastSeen
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
	row.Username, row.DisplayName = id.Username, id.DisplayName
	row.Status = id.Status
	row.Principal = id.Principal
	row.Via, row.CanApprove = id.Via, id.CanApprove
	row.Member, row.MemberAt = id.Member, ms(id.MemberAt)
	row.DMChat = id.DMChat
	row.LastSeen = ms(id.LastSeen)
	row.LastNotice = ms(id.LastNotice)
	row.Greet = id.Greet
	row.FirstText = ""
	if row.Status == core.IdentityPending {
		row.FirstText = id.FirstText
	}
	if err := st.checkIdentity(row); err != nil {
		return err
	}
	cur.id = row
	st.identities[k] = cur
	return nil
}

func (r identityRepo) ByPrincipal(_ context.Context, p core.PrincipalID) (*core.ChannelIdentity, error) {
	list, err := r.filter(func(id core.ChannelIdentity) bool { return p != "" && id.Principal == p })
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, notFound("channel identity")
	}
	best := list[0]
	for _, id := range list[1:] {
		if id.LastSeen.After(best.LastSeen) {
			best = id
		}
	}
	return best, nil
}

func (r identityRepo) ResetMembership(_ context.Context, channel string) error {
	st, unlock := r.s.begin()
	defer unlock()
	for k, row := range st.identities {
		if k.channel == channel {
			row.id.MemberAt = time.Time{}
			st.identities[k] = row
		}
	}
	return nil
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
