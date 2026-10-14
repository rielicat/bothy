// Package memstore is an in-memory core.Store for module unit tests while M1's
// SQLite store is being built. It passes conformance.Store (Viewer scoping =>
// ErrNotFound, ApprovalRepo.Decide CAS, the runs_one_active ErrConflict,
// keyset paging, FTS replaced by case-insensitive substring search, audit
// append-only). Tx runs fn under one mutex.
//
// Rows are copied in and out and normalised the way the §6 schema stores
// them: times and durations at millisecond precision, JSON columns
// round-tripped through encoding/json, fields without a column dropped.
// Constraint violations map like the SQLite store's: UNIQUE and PRIMARY KEY
// => core.ErrConflict, CHECK, FOREIGN KEY and NOT NULL => core.ErrInvalid.
package memstore

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// New returns an empty store.
func New() core.Store {
	return &store{db: &db{st: newState()}}
}

type db struct {
	mu sync.Mutex
	st state
}

// store is the handle every repository works through. The handle Tx passes
// to fn has tx set: the mutex is already held, so its methods must not lock.
type store struct {
	db *db
	tx bool
}

func (s *store) begin() (*state, func()) {
	if s.tx {
		return &s.db.st, func() {}
	}
	s.db.mu.Lock()
	return &s.db.st, s.db.mu.Unlock
}

func (s *store) Meta() core.MetaRepo                { return metaRepo{s} }
func (s *store) Users() core.UserRepo               { return userRepo{s} }
func (s *store) Principals() core.PrincipalRepo     { return principalRepo{s} }
func (s *store) Spaces() core.SpaceRepo             { return spaceRepo{s} }
func (s *store) Identities() core.IdentityRepo      { return identityRepo{s} }
func (s *store) Pairings() core.PairingRepo         { return pairingRepo{s} }
func (s *store) AuthSessions() core.AuthSessionRepo { return authSessionRepo{s} }
func (s *store) Tokens() core.TokenRepo             { return tokenRepo{s} }
func (s *store) Sessions() core.SessionRepo         { return sessionRepo{s} }
func (s *store) Messages() core.MessageRepo         { return messageRepo{s} }
func (s *store) Pins() core.PinRepo                 { return pinRepo{s} }
func (s *store) Runs() core.RunRepo                 { return runRepo{s} }
func (s *store) Approvals() core.ApprovalRepo       { return approvalRepo{s} }
func (s *store) Grants() core.GrantRepo             { return grantRepo{s} }
func (s *store) Schedules() core.ScheduleRepo       { return scheduleRepo{s} }
func (s *store) Usage() core.UsageRepo              { return usageRepo{s} }
func (s *store) Audit() core.AuditRepo              { return auditRepo{s} }
func (s *store) Egress() core.EgressRepo            { return egressRepo{s} }
func (s *store) MCP() core.MCPRepo                  { return mcpRepo{s} }
func (s *store) Skills() core.SkillRepo             { return skillRepo{s} }
func (s *store) MemoryEvents() core.MemoryEventRepo { return memoryEventRepo{s} }
func (s *store) Vault() core.VaultRepo              { return vaultRepo{s} }
func (s *store) Dedup() core.DedupRepo              { return dedupRepo{s} }
func (s *store) Uploads() core.UploadRepo           { return uploadRepo{s} }
func (s *store) SQLiteVersion() string              { return "" }
func (s *store) Close() error                       { return nil }
func (s *store) Backup(context.Context, string) (string, error) {
	return "", fmt.Errorf("memstore: backup: %w", core.ErrNotImplemented)
}

// Tx restores the pre-transaction state when fn fails or panics, matching a
// rolled-back BEGIN IMMEDIATE. A nested Tx joins the outer one.
func (s *store) Tx(_ context.Context, fn func(core.Store) error) error {
	if s.tx {
		return fn(s)
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	snap := s.db.st.clone()
	committed := false
	defer func() {
		if !committed {
			s.db.st = snap
		}
	}()
	if err := fn(&store{db: s.db, tx: true}); err != nil {
		return err
	}
	committed = true
	return nil
}

type identKey struct{ channel, ext string }

type urlKey struct {
	session core.SessionID
	url     string
}

type spendKey struct {
	user core.UserID
	day  string
}

type pinKey struct{ server, tool, hash string }

type secretKey struct {
	scope core.Scope
	name  string
}

type dedupKey struct{ channel, msg string }

type identRow struct {
	id core.ChannelIdentity
	n  int64
}

type pairingRow struct {
	p core.Pairing
	n int64
}

type grantRow struct {
	g         core.Grant
	revokedAt time.Time
}

type llmRow struct {
	run  core.RunID
	user core.UserID
	c    core.CallInfo
	at   time.Time
}

type toolCallRow struct {
	tc core.ToolCallRecord
	n  int64
}

type cardMsgRow struct {
	approval core.ApprovalID
	m        core.CardMessage
}

type auditRow struct {
	e          core.AuditEntry
	prev, hash []byte
}

type mcpPinRow struct {
	p core.MCPPin
	n int64
}

type installRow struct {
	in core.MCPInstall
	n  int64
}

type vaultKeyRow struct {
	id             int64
	wrapped, check []byte
	source         string
	active         bool
}

type secretRow struct {
	keyID           int64
	version         int
	nonce, ct       []byte
	hint            string
	updatedAt, used time.Time
}

// state holds one map or slice per table, keeping only what some repository
// method can read back (spend_daily: cost; vault_secrets: no created_by).
// Rows are stored by value and never mutated in place (reference fields are
// replaced, not edited), so clone only has to copy the containers.
type state struct {
	serial int64 // insertion order: the tie-breaker SQLite rowids give

	msgID, pinID, auditID, egressID, memID, keyID int64

	meta        map[string]string
	users       map[core.UserID]core.User
	principals  map[core.PrincipalID]core.Principal
	spaces      map[core.PrincipalID]core.Space
	identities  map[identKey]identRow
	pairings    map[string]pairingRow
	authSess    map[string]core.AuthSession
	tokens      map[string]core.TokenRecord
	sessions    map[core.SessionID]core.Session
	sessionURLs map[urlKey]string
	messages    []core.Message
	lastSeq     map[core.SessionID]int64
	pins        []core.Pin
	runs        map[core.RunID]core.Run
	llmCalls    []llmRow
	toolCalls   map[string]toolCallRow
	approvals   map[core.ApprovalID]core.Approval
	cardMsgs    []cardMsgRow
	grants      map[core.GrantID]grantRow
	schedules   map[core.ScheduleID]core.Schedule
	spend       map[spendKey]core.Micros
	audit       []auditRow
	egress      []core.EgressRecord
	mcpPins     map[pinKey]mcpPinRow
	installs    map[string]installRow
	skills      map[string]core.SkillMeta
	skillFiles  map[string][]core.SkillFile
	memEvents   []core.MemoryEvent
	vaultKeys   []vaultKeyRow
	secrets     map[secretKey]secretRow
	dedup       map[dedupKey]time.Time
	uploads     map[string]core.Upload
	votes       map[voteKey]core.ApprovalVote
	deploys     map[string]deployRow
}

func newState() state {
	return state{
		meta:        map[string]string{},
		users:       map[core.UserID]core.User{},
		principals:  map[core.PrincipalID]core.Principal{},
		spaces:      map[core.PrincipalID]core.Space{},
		identities:  map[identKey]identRow{},
		pairings:    map[string]pairingRow{},
		authSess:    map[string]core.AuthSession{},
		tokens:      map[string]core.TokenRecord{},
		sessions:    map[core.SessionID]core.Session{},
		sessionURLs: map[urlKey]string{},
		lastSeq:     map[core.SessionID]int64{},
		runs:        map[core.RunID]core.Run{},
		toolCalls:   map[string]toolCallRow{},
		approvals:   map[core.ApprovalID]core.Approval{},
		grants:      map[core.GrantID]grantRow{},
		schedules:   map[core.ScheduleID]core.Schedule{},
		spend:       map[spendKey]core.Micros{},
		mcpPins:     map[pinKey]mcpPinRow{},
		installs:    map[string]installRow{},
		skills:      map[string]core.SkillMeta{},
		skillFiles:  map[string][]core.SkillFile{},
		secrets:     map[secretKey]secretRow{},
		dedup:       map[dedupKey]time.Time{},
		uploads:     map[string]core.Upload{},
		votes:       map[voteKey]core.ApprovalVote{},
		deploys:     map[string]deployRow{},
	}
}

func (st *state) clone() state {
	c := *st
	c.meta = maps.Clone(st.meta)
	c.users = maps.Clone(st.users)
	c.principals = maps.Clone(st.principals)
	c.spaces = maps.Clone(st.spaces)
	c.identities = maps.Clone(st.identities)
	c.pairings = maps.Clone(st.pairings)
	c.authSess = maps.Clone(st.authSess)
	c.tokens = maps.Clone(st.tokens)
	c.sessions = maps.Clone(st.sessions)
	c.sessionURLs = maps.Clone(st.sessionURLs)
	c.messages = slices.Clone(st.messages)
	c.lastSeq = maps.Clone(st.lastSeq)
	c.pins = slices.Clone(st.pins)
	c.runs = maps.Clone(st.runs)
	c.llmCalls = slices.Clone(st.llmCalls)
	c.toolCalls = maps.Clone(st.toolCalls)
	c.approvals = maps.Clone(st.approvals)
	c.cardMsgs = slices.Clone(st.cardMsgs)
	c.grants = maps.Clone(st.grants)
	c.schedules = maps.Clone(st.schedules)
	c.spend = maps.Clone(st.spend)
	c.audit = slices.Clone(st.audit)
	c.egress = slices.Clone(st.egress)
	c.mcpPins = maps.Clone(st.mcpPins)
	c.installs = maps.Clone(st.installs)
	c.skills = maps.Clone(st.skills)
	c.skillFiles = maps.Clone(st.skillFiles)
	c.memEvents = slices.Clone(st.memEvents)
	c.vaultKeys = slices.Clone(st.vaultKeys)
	c.secrets = maps.Clone(st.secrets)
	c.dedup = maps.Clone(st.dedup)
	c.uploads = maps.Clone(st.uploads)
	c.votes = maps.Clone(st.votes)
	c.deploys = maps.Clone(st.deploys)
	return c
}

func (st *state) next() int64 {
	st.serial++
	return st.serial
}

func notFound(what string) error {
	return fmt.Errorf("memstore: %s: %w", what, core.ErrNotFound)
}

func conflict(what string) error {
	return fmt.Errorf("memstore: %s: %w", what, core.ErrConflict)
}

func invalid(what string) error {
	return fmt.Errorf("memstore: %s: %w", what, core.ErrInvalid)
}

// ms mirrors the unix-ms INTEGER columns, where 0 is the zero time.
func ms(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return time.UnixMilli(t.UnixMilli()).UTC()
}

func unixMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func msDur(d time.Duration) time.Duration { return d.Truncate(time.Millisecond) }

// jsonCol round-trips v the way a JSON TEXT column does, which also yields a
// deep copy that shares nothing with the caller.
func jsonCol[T any](what string, v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	if err != nil {
		return out, fmt.Errorf("memstore: %s: %w: %w", what, core.ErrInvalid, err)
	}
	return out, nil
}

// must is for the read path: stored rows were normalised on write, so
// normalising them again cannot fail.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func oneOf[T comparable](v T, set ...T) bool { return slices.Contains(set, v) }

func idOr[T ~string](id T, prefix string) T {
	if id == "" {
		return T(core.NewID(prefix))
	}
	return id
}

func pageLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 500:
		return 500
	}
	return n
}

func newestFirst[T any](rows []T, limit int, key func(T) (int64, string)) []T {
	slices.SortFunc(rows, func(a, b T) int {
		ak, aid := key(a)
		bk, bid := key(b)
		return cmp.Or(cmp.Compare(bk, ak), cmp.Compare(bid, aid))
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func byCreated[T any](rows []T, key func(T) (time.Time, string)) {
	slices.SortFunc(rows, func(a, b T) int {
		at, aid := key(a)
		bt, bid := key(b)
		return cmp.Or(at.Compare(bt), cmp.Compare(aid, bid))
	})
}

func ptrs[T any](rows []T) []*T {
	out := make([]*T, len(rows))
	for i := range rows {
		out[i] = &rows[i]
	}
	return out
}

// asciiLower folds like SQLite's NOCASE collation and LIKE: ASCII only.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
