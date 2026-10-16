package conformance

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// Store runs the repository contract against a fresh store per subtest:
// round trips for every method, the IDOR table (foreign Viewer => ErrNotFound
// on every by-ID read), Decide CAS under 50 goroutines, runs_one_active =>
// ErrConflict, paging order, MarkInterrupted, Approvals.Seen, Grants.Active
// expiry/revocation, audit Append/Range ordering. memstore and M1 both run it.
//
// Beyond the §4.8 comments it pins the gaps every implementation must agree
// on, so tests written against memstore hold against SQLite:
//   - Constraint errors: UNIQUE/PRIMARY KEY => ErrConflict; CHECK, FOREIGN KEY
//     and NOT NULL => ErrInvalid (§6 CHECKs are exercised one by one).
//   - Create/Add/Upsert with an empty id assign core.NewID with the entity's
//     prefix and write it back; Messages.Append also assigns Seq from 1.
//   - Times and *_ms durations are truncated to milliseconds; zero stays zero.
//   - Update/Delete/Touch/Revoke of a missing row => ErrNotFound, except
//     Meta.Delete and AuthSessions.Delete, which are idempotent.
//   - Messages.Range with limit <= 0 returns the whole tail; Page.Limit 0 is
//     50 and is capped at 500. Search ANDs the query's words and never fails
//     on hostile or empty input.
//   - Identities.Touch inserts an unknown sender as pending (first_seen
//     defaulting to last_seen) and otherwise refreshes only names and
//     last_seen.
//   - Grants: Active omits used-up grants, Use of a revoked or used-up grant
//     => ErrConflict, List omits revoked grants, a second Revoke => ErrNotFound.
//   - Approvals.Park moves only pending or parked approvals (else ErrConflict).
//   - Approvals.AddVote: a second vote by one user => ErrConflict; Votes come
//     oldest first; repositories never fill Approval.Votes. Multi-approver
//     approvals are visible to their voters (ApproverReq.Users, Roles). Tx
//     serializes concurrent vote transactions (status, vote, count, Decide).
//   - core.ReleaseStore is optional (the suite skips it); deploy records
//     follow the Viewer rule with admins seeing every row.
//   - core.SignInStore is optional too; Users.Delete cascades to the links.
//   - Runs.MarkInterrupted sets FinishedAt to now.
//   - Audit: Head of an empty log is (0, nil, nil); Range bounds are inclusive
//     and toID <= 0 means the end; Append ignores AuditEntry.ID and stores an
//     empty Detail as "{}".
//   - Time filters (AuditFilter, UsageQuery) are [From, To); Report day keys
//     are YYYY-MM-DD and an unknown GroupBy => ErrInvalid.
//   - MCP pins without a valid SideEffect get the column default
//     external_send; ApprovePins and Vault.Rewrap are all-or-nothing.
//
// Times the store stamps itself are only checked to lie between 2000 and
// 3000, so open may inject a fixed clock. Subtests run in parallel, each on
// its own store.
func Store(t *testing.T, open func(t *testing.T) core.Store) {
	t.Helper()
	for _, c := range storeSuite {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(&fx{t: t, ctx: t.Context(), s: open(t)})
		})
	}
}

var storeSuite = []struct {
	name string
	run  func(f *fx)
}{
	{"Meta", testMeta},
	{"Users", testUsers},
	{"UserDelete", testUserDelete},
	{"Principals", testPrincipals},
	{"Spaces", testSpaces},
	{"Identities", testIdentities},
	{"Pairings", testPairings},
	{"AuthSessions", testAuthSessions},
	{"Tokens", testTokens},
	{"Sessions", testSessions},
	{"SessionList", testSessionList},
	{"SessionURLs", testSessionURLs},
	{"Messages", testMessages},
	{"MessagePaging", testMessagePaging},
	{"MessageSearch", testMessageSearch},
	{"Pins", testPins},
	{"Uploads", testUploads},
	{"Runs", testRuns},
	{"RunsOneActive", testRunsOneActive},
	{"MarkInterrupted", testMarkInterrupted},
	{"LLMCalls", testLLMCalls},
	{"ToolCalls", testToolCalls},
	{"Approvals", testApprovals},
	{"ApprovalVisibility", testApprovalVisibility},
	{"ApprovalDecideCAS", testApprovalDecideCAS},
	{"ApprovalSeen", testApprovalSeen},
	{"ApprovalVotes", testApprovalVotes},
	{"ApprovalVoteTx", testApprovalVoteTx},
	{"ApprovalMultiVisibility", testApprovalMultiVisibility},
	{"Releases", testReleases},
	{"SignIns", testSignIns},
	{"Grants", testGrants},
	{"GrantsActive", testGrantsActive},
	{"Schedules", testSchedules},
	{"Usage", testUsage},
	{"Audit", testAudit},
	{"Egress", testEgress},
	{"MCP", testMCP},
	{"Skills", testSkills},
	{"MemoryEvents", testMemoryEvents},
	{"Vault", testVault},
	{"Dedup", testDedup},
	{"TimePrecision", testTimePrecision},
	{"Tx", testTx},
	{"IDOR", testIDOR},
	{"NoAliasing", testNoAliasing},
}

// base is millisecond-aligned because stores persist unix ms.
var base = time.UnixMilli(1_780_000_000_000).UTC()

func at(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

type fx struct {
	t   *testing.T
	ctx context.Context
	s   core.Store
	n   int
}

func (f *fx) next() int {
	f.n++
	return f.n
}

func (f *fx) must(err error, what string) {
	f.t.Helper()
	if err != nil {
		f.t.Fatalf("%s: %v", what, err)
	}
}

func (f *fx) want(err, target error, what string) {
	f.t.Helper()
	if !errors.Is(err, target) {
		f.t.Errorf("%s: got error %v, want %v", what, err, target)
	}
}

func (f *fx) eq(what string, got, want any) {
	f.t.Helper()
	if d := diff(got, want); d != "" {
		f.t.Errorf("%s: %s", what, d)
	}
}

func (f *fx) check(ok bool, format string, args ...any) {
	f.t.Helper()
	if !ok {
		f.t.Errorf(format, args...)
	}
}

// need stops the subtest when a result is too short to index, so a broken
// store fails cleanly instead of panicking the whole run.
func (f *fx) need(ok bool, format string, args ...any) {
	f.t.Helper()
	if !ok {
		f.t.Fatalf(format, args...)
	}
}

func (f *fx) user(role core.Role) *core.User {
	f.t.Helper()
	n := f.next()
	u := &core.User{
		Username:    fmt.Sprintf("user%d", n),
		DisplayName: fmt.Sprintf("User %d", n),
		Role:        role,
		PWHash:      "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Timezone:    "Europe/London",
		CreatedAt:   at(n),
		UpdatedAt:   at(n),
	}
	f.must(f.s.Users().Create(f.ctx, u), "create user")
	return u
}

func viewer(u *core.User, extra ...core.PrincipalID) core.Viewer {
	return core.Viewer{User: u.ID, Principals: append([]core.PrincipalID{core.UserPrincipal(u.ID)}, extra...)}
}

func (f *fx) space(owner *core.User) *core.Space {
	f.t.Helper()
	n := f.next()
	s := &core.Space{
		Kind:        "group",
		Chat:        core.ChatRef{Channel: "telegram", ChatID: fmt.Sprintf("-100%d", n), Kind: core.ChatGroup},
		Title:       fmt.Sprintf("Group %d", n),
		Owner:       owner.ID,
		Enabled:     true,
		MentionOnly: true,
		CreatedAt:   at(n),
		UpdatedAt:   at(n),
	}
	f.must(f.s.Spaces().Create(f.ctx, s, &core.Principal{DisplayName: s.Title, CreatedAt: at(n)}), "create space")
	return s
}

func (f *fx) session(p core.PrincipalID) *core.Session {
	f.t.Helper()
	n := f.next()
	s := &core.Session{
		Principal:     p,
		Origin:        "web",
		Title:         fmt.Sprintf("Session %d", n),
		ModelSlot:     core.SlotMain,
		CreatedAt:     at(n),
		UpdatedAt:     at(n),
		LastMessageAt: at(n),
	}
	f.must(f.s.Sessions().Create(f.ctx, s), "create session")
	return s
}

func (f *fx) run(s *core.Session, status core.RunStatus) *core.Run {
	f.t.Helper()
	r := newRun(s, status, at(f.next()))
	f.must(f.s.Runs().Create(f.ctx, r), "create run")
	return r
}

func newRun(s *core.Session, status core.RunStatus, created time.Time) *core.Run {
	return &core.Run{
		Session:   s.ID,
		Principal: s.Principal,
		User:      core.UserID(s.Principal),
		Trigger:   core.TrigMessage,
		Status:    status,
		Limits:    core.RunLimits{MaxSteps: 25, MaxTokens: 400_000, MaxTime: 10 * time.Minute},
		CreatedAt: created,
	}
}

func sendAction(recipient, host string) *core.Action {
	payload := json.RawMessage(`{"text":"hello","to":"` + recipient + `"}`)
	return &core.Action{
		Tool:        "message",
		Kind:        core.ActSend,
		SideEffect:  core.SEExternalSend,
		Host:        host,
		Recipient:   recipient,
		Payload:     payload,
		Fingerprint: core.ActionFingerprint("message", payload),
	}
}

func (f *fx) approval(r *core.Run, status core.ApprovalStatus, created time.Time) *core.Approval {
	f.t.Helper()
	a := newApproval(r, status, created)
	f.must(f.s.Approvals().Create(f.ctx, a), "create approval")
	return a
}

func newApproval(r *core.Run, status core.ApprovalStatus, created time.Time) *core.Approval {
	approver := core.ApproverReq{User: r.User}
	return &core.Approval{
		Run:        r.ID,
		Session:    r.Session,
		Principal:  r.Principal,
		ToolCallID: "tcl_01aaaaaaaaaaaaaaaaaaaaaaaa",
		Action:     sendAction("telegram:42:", ""),
		Decision: core.Decision{
			Verdict:   core.VerdictAsk,
			Rule:      "default:external_send",
			Reason:    "sending a message needs approval",
			Approver:  approver,
			Timeout:   15 * time.Minute,
			OnTimeout: core.TimeoutDeny,
			Choices:   []core.Choice{core.AllowOnce, core.AllowSession, core.Deny},
		},
		Card: core.Card{
			Title:   "Send message",
			Risk:    core.SEExternalSend,
			Fields:  []core.CardField{{Label: "Recipient", Value: "Alice"}, {Label: "Text", Value: "hello", Mono: true}},
			Context: "Telegram · DM",
			Choices: []core.Choice{core.AllowOnce, core.AllowSession, core.Deny},
			Expires: created.Add(15 * time.Minute),
		},
		Status:    status,
		Approver:  approver,
		Chat:      &core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM},
		OnTimeout: core.TimeoutDeny,
		CreatedAt: created,
		ExpiresAt: created.Add(15 * time.Minute),
	}
}

// sortBy orders rows by key so unordered List results compare as sets.
func sortBy[T any, K ~string](rows []*T, key func(*T) K) []*T {
	out := slices.Clone(rows)
	slices.SortFunc(out, func(a, b *T) int { return cmp.Compare(key(a), key(b)) })
	return out
}

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
)

// diff describes the first difference between got and want, or returns "".
// Times compare with Equal, json.RawMessage by canonical JSON, and nil slices
// and maps equal empty ones: stores hand back what their columns hold, not
// the caller's exact representation.
func diff(got, want any) string {
	return diffValue(reflect.ValueOf(got), reflect.ValueOf(want), "")
}

func diffValue(g, w reflect.Value, path string) string {
	if !g.IsValid() || !w.IsValid() {
		if g.IsValid() == w.IsValid() {
			return ""
		}
		return fmt.Sprintf("%s: got %v, want %v", path, g, w)
	}
	if g.Type() != w.Type() {
		return fmt.Sprintf("%s: got type %s, want %s", path, g.Type(), w.Type())
	}
	switch g.Type() {
	case timeType:
		gt, wt := g.Interface().(time.Time), w.Interface().(time.Time)
		if gt.Equal(wt) {
			return ""
		}
		return fmt.Sprintf("%s: got %v, want %v", path, gt, wt)
	case rawType:
		if jsonEqual(g.Bytes(), w.Bytes()) {
			return ""
		}
		return fmt.Sprintf("%s: got %s, want %s", path, g.Bytes(), w.Bytes())
	}
	switch g.Kind() {
	case reflect.Pointer, reflect.Interface:
		if g.IsNil() || w.IsNil() {
			if g.IsNil() == w.IsNil() {
				return ""
			}
			return fmt.Sprintf("%s: got nil=%t, want nil=%t", path, g.IsNil(), w.IsNil())
		}
		return diffValue(g.Elem(), w.Elem(), path)
	case reflect.Struct:
		for i := range g.NumField() {
			if d := diffValue(g.Field(i), w.Field(i), path+"."+g.Type().Field(i).Name); d != "" {
				return d
			}
		}
		return ""
	case reflect.Slice, reflect.Array:
		if g.Len() != w.Len() {
			return fmt.Sprintf("%s: got len %d, want %d", path, g.Len(), w.Len())
		}
		for i := range g.Len() {
			if d := diffValue(g.Index(i), w.Index(i), fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		return ""
	case reflect.Map:
		if g.Len() != w.Len() {
			return fmt.Sprintf("%s: got len %d, want %d", path, g.Len(), w.Len())
		}
		for _, k := range w.MapKeys() {
			gv := g.MapIndex(k)
			if !gv.IsValid() {
				return fmt.Sprintf("%s[%v]: missing", path, k)
			}
			if d := diffValue(gv, w.MapIndex(k), fmt.Sprintf("%s[%v]", path, k)); d != "" {
				return d
			}
		}
		return ""
	}
	if g.Equal(w) {
		return ""
	}
	return fmt.Sprintf("%s: got %v, want %v", path, g, w)
}

func jsonEqual(a, b []byte) bool {
	norm := func(x []byte) []byte {
		if len(x) == 0 {
			return []byte("null")
		}
		c, err := core.CanonicalJSON(json.RawMessage(x))
		if err != nil {
			return x
		}
		return c
	}
	return bytes.Equal(norm(a), norm(b))
}
