package memstore

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// normRun drops Usage.Reasoning: runs has no reasoning_tokens column.
func normRun(r core.Run) (core.Run, error) {
	models, err := jsonCol("runs.models", r.Models)
	if err != nil {
		return r, err
	}
	limits, err := jsonCol("runs.limits", r.Limits)
	if err != nil {
		return r, err
	}
	r.Models, r.Limits = models, limits
	r.Usage.Reasoning = 0
	r.CreatedAt = ms(r.CreatedAt)
	r.StartedAt = ms(r.StartedAt)
	r.FinishedAt = ms(r.FinishedAt)
	return r, nil
}

// oneActive mirrors the runs_one_active partial index.
func oneActive(s core.RunStatus) bool {
	return s == core.RunRunning || s == core.RunWaitingApproval
}

func (st *state) checkRun(r core.Run) error {
	switch {
	case !oneOf(r.Trigger, core.TrigMessage, core.TrigSchedule, core.TrigHeartbeat, core.TrigAPI, core.TrigResume):
		return invalid("runs.trigger")
	case !oneOf(r.Status, core.RunQueued, core.RunRunning, core.RunWaitingApproval, core.RunParked, core.RunDone,
		core.RunStopped, core.RunFailed, core.RunBudgetExceeded, core.RunLoopDetected, core.RunInterrupted, core.RunSkipped):
		return invalid("runs.status")
	}
	if _, ok := st.sessions[r.Session]; !ok {
		return invalid("runs.session_id: no such session")
	}
	if _, ok := st.principals[r.Principal]; !ok {
		return invalid("runs.principal_id: no such principal")
	}
	if _, ok := st.schedules[r.Schedule]; r.Schedule != "" && !ok {
		return invalid("runs.schedule_id: no such schedule")
	}
	if !oneActive(r.Status) {
		return nil
	}
	for id, o := range st.runs {
		if id != r.ID && o.Session == r.Session && oneActive(o.Status) {
			return conflict("runs_one_active")
		}
	}
	return nil
}

type runRepo struct{ s *store }

func (r runRepo) Create(_ context.Context, run *core.Run) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *run
	row.ID = idOr(row.ID, core.PrefixRun)
	if _, ok := st.runs[row.ID]; ok {
		return conflict("runs.id")
	}
	if err := st.checkRun(row); err != nil {
		return err
	}
	row, err := normRun(row)
	if err != nil {
		return err
	}
	st.runs[row.ID] = row
	run.ID = row.ID
	return nil
}

func (r runRepo) Update(_ context.Context, run *core.Run) error {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.runs[run.ID]
	if !ok {
		return notFound("run")
	}
	row := *run
	row.Session, row.Principal, row.CreatedAt = cur.Session, cur.Principal, cur.CreatedAt
	if err := st.checkRun(row); err != nil {
		return err
	}
	row, err := normRun(row)
	if err != nil {
		return err
	}
	st.runs[run.ID] = row
	return nil
}

func (st *state) visibleRun(v core.Viewer, id core.RunID) (core.Run, bool) {
	r, ok := st.runs[id]
	return r, ok && v.Can(r.Principal)
}

func (r runRepo) Get(_ context.Context, v core.Viewer, id core.RunID) (*core.Run, error) {
	st, unlock := r.s.begin()
	defer unlock()
	run, ok := st.visibleRun(v, id)
	if !ok {
		return nil, notFound("run")
	}
	run = must(normRun(run))
	return &run, nil
}

func (r runRepo) List(_ context.Context, v core.Viewer, f core.RunFilter) ([]*core.Run, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Run
	for _, run := range st.runs {
		switch {
		case !v.Can(run.Principal),
			f.Session != "" && run.Session != f.Session,
			f.Schedule != "" && run.Schedule != f.Schedule,
			f.Status != "" && run.Status != f.Status,
			f.Page.Before > 0 && unixMS(run.CreatedAt) >= f.Page.Before:
			continue
		}
		out = append(out, run)
	}
	out = newestFirst(out, pageLimit(f.Page.Limit), func(r core.Run) (int64, string) {
		return unixMS(r.CreatedAt), string(r.ID)
	})
	for i := range out {
		out[i] = must(normRun(out[i]))
	}
	return ptrs(out), nil
}

func (r runRepo) MarkInterrupted(_ context.Context, now time.Time) ([]*core.Run, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Run
	for id, run := range st.runs {
		if !oneOf(run.Status, core.RunQueued, core.RunRunning, core.RunWaitingApproval) {
			continue
		}
		run.Status = core.RunInterrupted
		run.FinishedAt = ms(now)
		st.runs[id] = run
		out = append(out, must(normRun(run)))
	}
	byCreated(out, func(r core.Run) (time.Time, string) { return r.CreatedAt, string(r.ID) })
	return ptrs(out), nil
}

func (r runRepo) Parked(context.Context) ([]*core.Run, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Run
	for _, run := range st.runs {
		if run.Status == core.RunParked {
			out = append(out, must(normRun(run)))
		}
	}
	byCreated(out, func(r core.Run) (time.Time, string) { return r.CreatedAt, string(r.ID) })
	return ptrs(out), nil
}

func normCall(c core.CallInfo) (core.CallInfo, error) {
	attempts, err := jsonCol("llm_calls.attempts", c.Attempts)
	if err != nil {
		return c, err
	}
	c.Attempts = attempts
	c.TTFT = msDur(c.TTFT)
	c.Latency = msDur(c.Latency)
	return c, nil
}

// AddLLMCall stamps created_at itself: CallInfo carries no time, and usage
// reports filter on it. run "" is a passthrough or probe call (run_id NULL).
func (r runRepo) AddLLMCall(_ context.Context, run core.RunID, p core.PrincipalID, u core.UserID, c core.CallInfo) error {
	st, unlock := r.s.begin()
	defer unlock()
	c.ID = idOr(c.ID, core.PrefixLLMCall)
	if !oneOf(c.Purpose, "agent", "compaction", "title", "heartbeat", "probe", "passthrough", "notes") {
		return invalid("llm_calls.purpose")
	}
	if _, ok := st.runs[run]; run != "" && !ok {
		return invalid("llm_calls.run_id: no such run")
	}
	if slices.ContainsFunc(st.llmCalls, func(l llmRow) bool { return l.c.ID == c.ID }) {
		return conflict("llm_calls.id")
	}
	c, err := normCall(c)
	if err != nil {
		return err
	}
	st.llmCalls = append(st.llmCalls, llmRow{run: run, principal: p, user: u, c: c, at: ms(time.Now())})
	return nil
}

func (r runRepo) LLMCalls(_ context.Context, v core.Viewer, run core.RunID) ([]core.CallInfo, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.visibleRun(v, run); !ok {
		return nil, notFound("run")
	}
	var out []core.CallInfo
	for _, l := range st.llmCalls {
		if l.run == run {
			out = append(out, must(normCall(l.c)))
		}
	}
	slices.SortStableFunc(out, func(a, b core.CallInfo) int { return cmp.Compare(a.Step, b.Step) })
	return out, nil
}

func normToolCall(tc core.ToolCallRecord) (core.ToolCallRecord, error) {
	action, err := jsonCol("tool_calls.action", tc.Action)
	if err != nil {
		return tc, err
	}
	tc.Action = action
	tc.Duration = msDur(tc.Duration)
	tc.CreatedAt = ms(tc.CreatedAt)
	tc.FinishedAt = ms(tc.FinishedAt)
	return tc, nil
}

func (st *state) checkToolCall(tc core.ToolCallRecord) error {
	switch {
	case !oneOf(tc.Verdict, "", core.VerdictAuto, core.VerdictAsk, core.VerdictDeny):
		return invalid("tool_calls.verdict")
	case !oneOf(tc.Status, core.TCPlanned, core.TCWaiting, core.TCApproved, core.TCDenied, core.TCRunning,
		core.TCOK, core.TCError, core.TCTimeout, core.TCStopped, core.TCInvalid):
		return invalid("tool_calls.status")
	case !tc.SideEffect.Valid():
		return invalid("tool_calls.side_effect")
	}
	if _, ok := st.runs[tc.Run]; !ok {
		return invalid("tool_calls.run_id: no such run")
	}
	return nil
}

func (r runRepo) AddToolCall(_ context.Context, tc *core.ToolCallRecord) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *tc
	row.ID = idOr(row.ID, core.PrefixToolCall)
	if _, ok := st.toolCalls[row.ID]; ok {
		return conflict("tool_calls.id")
	}
	if err := st.checkToolCall(row); err != nil {
		return err
	}
	row, err := normToolCall(row)
	if err != nil {
		return err
	}
	st.toolCalls[row.ID] = toolCallRow{tc: row, n: st.next()}
	tc.ID = row.ID
	return nil
}

func (r runRepo) UpdateToolCall(_ context.Context, tc *core.ToolCallRecord) error {
	st, unlock := r.s.begin()
	defer unlock()
	cur, ok := st.toolCalls[tc.ID]
	if !ok {
		return notFound("tool call")
	}
	row := *tc
	row.Run, row.CreatedAt = cur.tc.Run, cur.tc.CreatedAt
	if err := st.checkToolCall(row); err != nil {
		return err
	}
	row, err := normToolCall(row)
	if err != nil {
		return err
	}
	cur.tc = row
	st.toolCalls[tc.ID] = cur
	return nil
}

func (r runRepo) ToolCalls(_ context.Context, v core.Viewer, run core.RunID) ([]*core.ToolCallRecord, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.visibleRun(v, run); !ok {
		return nil, notFound("run")
	}
	var rows []toolCallRow
	for _, row := range st.toolCalls {
		if row.tc.Run == run {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b toolCallRow) int {
		return cmp.Or(cmp.Compare(a.tc.Step, b.tc.Step), cmp.Compare(a.n, b.n))
	})
	out := make([]*core.ToolCallRecord, len(rows))
	for i, row := range rows {
		tc := must(normToolCall(row.tc))
		out[i] = &tc
	}
	return out, nil
}

func normApproval(a core.Approval) (core.Approval, error) {
	var err error
	if a.Action, err = jsonCol("approvals.action", a.Action); err != nil {
		return a, err
	}
	if a.Decision, err = jsonCol("approvals.decision", a.Decision); err != nil {
		return a, err
	}
	if a.Card, err = jsonCol("approvals.card", a.Card); err != nil {
		return a, err
	}
	if a.Approver, err = jsonCol("approvals.approver", a.Approver); err != nil {
		return a, err
	}
	if a.Chat, err = jsonCol("approvals.chat", a.Chat); err != nil {
		return a, err
	}
	a.CreatedAt = ms(a.CreatedAt)
	a.ExpiresAt = ms(a.ExpiresAt)
	a.ParkUntil = ms(a.ParkUntil)
	a.DecidedAt = ms(a.DecidedAt)
	a.Votes = nil // approval_votes is its own table
	a.Approvals, a.Requester = nil, nil
	return a, nil
}

func validApprovalStatus(s core.ApprovalStatus) bool {
	return oneOf(s, core.ApprovalPending, core.ApprovalAllowed, core.ApprovalDenied,
		core.ApprovalExpired, core.ApprovalParked, core.ApprovalCancelled)
}

func openStatus(s core.ApprovalStatus) bool {
	return s == core.ApprovalPending || s == core.ApprovalParked
}

// visibleApproval applies Viewer.Admin's approval exception (every admin sees
// admin-only and team approvals) and the multi-approver one (a voter by id,
// or by a role the viewer may hold: member for anyone, owner/admin for
// admins).
func visibleApproval(v core.Viewer, a core.Approval) bool {
	if v.Can(a.Principal) || v.Admin && (a.Approver.AdminOnly || a.Approver.Team) {
		return true
	}
	if v.System || v.User == "" || a.Approver.AdminOnly {
		return false
	}
	if slices.Contains(a.Approver.Users, v.User) {
		return true
	}
	return slices.ContainsFunc(a.Approver.Roles, func(r core.Role) bool {
		return r == core.RoleMember || v.Admin && (r == core.RoleAdmin || r == core.RoleOwner)
	})
}

type approvalRepo struct{ s *store }

func (r approvalRepo) Create(_ context.Context, a *core.Approval) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *a
	row.ID = idOr(row.ID, core.PrefixApproval)
	switch {
	case !validApprovalStatus(row.Status):
		return invalid("approvals.status")
	case !oneOf(row.OnTimeout, core.TimeoutDeny, core.TimeoutPark):
		return invalid("approvals.on_timeout")
	}
	if _, ok := st.runs[row.Run]; !ok {
		return invalid("approvals.run_id: no such run")
	}
	if _, ok := st.approvals[row.ID]; ok {
		return conflict("approvals.id")
	}
	row, err := normApproval(row)
	if err != nil {
		return err
	}
	st.approvals[row.ID] = row
	a.ID = row.ID
	return nil
}

func (r approvalRepo) Get(_ context.Context, v core.Viewer, id core.ApprovalID) (*core.Approval, error) {
	st, unlock := r.s.begin()
	defer unlock()
	a, ok := st.approvals[id]
	if !ok || !visibleApproval(v, a) {
		return nil, notFound("approval")
	}
	a = must(normApproval(a))
	return &a, nil
}

func (r approvalRepo) Decide(_ context.Context, id core.ApprovalID, from []core.ApprovalStatus, to core.ApprovalStatus,
	c core.Choice, by core.Decider, via string, at time.Time) (bool, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if !validApprovalStatus(to) {
		return false, invalid("approvals.status")
	}
	a, ok := st.approvals[id]
	if !ok {
		return false, notFound("approval")
	}
	if !slices.Contains(from, a.Status) {
		return false, nil
	}
	a.Status, a.Choice, a.DecidedVia, a.DecidedAt = to, c, via, ms(at)
	a.DecidedBy, a.DecidedByIdentity = by.User, by.Identity
	if by.Identity != "" {
		a.DecidedBy = ""
	}
	st.approvals[id] = a
	return true, nil
}

func (r approvalRepo) CountOpen(_ context.Context, p core.PrincipalID) (int, error) {
	st, unlock := r.s.begin()
	defer unlock()
	n := 0
	for _, a := range st.approvals {
		if a.Principal == p && openStatus(a.Status) {
			n++
		}
	}
	return n, nil
}

// Park only moves an open approval: a decided one is never reopened by a
// late timeout.
func (r approvalRepo) Park(_ context.Context, id core.ApprovalID, until time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	a, ok := st.approvals[id]
	if !ok {
		return notFound("approval")
	}
	if !openStatus(a.Status) {
		return conflict("approval already decided")
	}
	a.Status, a.ParkUntil = core.ApprovalParked, ms(until)
	st.approvals[id] = a
	return nil
}

func approvalsWhere(st *state, keep func(core.Approval) bool) []core.Approval {
	var out []core.Approval
	for _, a := range st.approvals {
		if keep(a) {
			out = append(out, must(normApproval(a)))
		}
	}
	return out
}

func (r approvalRepo) Pending(_ context.Context, v core.Viewer) ([]*core.Approval, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := approvalsWhere(st, func(a core.Approval) bool { return openStatus(a.Status) && visibleApproval(v, a) })
	byCreated(out, func(a core.Approval) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return ptrs(out), nil
}

func (r approvalRepo) Recent(_ context.Context, v core.Viewer, p core.Page) ([]*core.Approval, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := approvalsWhere(st, func(a core.Approval) bool {
		return !openStatus(a.Status) && visibleApproval(v, a) && (p.Before <= 0 || unixMS(a.CreatedAt) < p.Before)
	})
	out = newestFirst(out, pageLimit(p.Limit), func(a core.Approval) (int64, string) {
		return unixMS(a.CreatedAt), string(a.ID)
	})
	return ptrs(out), nil
}

func (r approvalRepo) Due(_ context.Context, now time.Time) ([]*core.Approval, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := approvalsWhere(st, func(a core.Approval) bool {
		return (a.Status == core.ApprovalPending && !a.ExpiresAt.After(now)) ||
			(a.Status == core.ApprovalParked && !a.ParkUntil.After(now))
	})
	byCreated(out, func(a core.Approval) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return ptrs(out), nil
}

func (r approvalRepo) ByRun(_ context.Context, run core.RunID) ([]*core.Approval, error) {
	st, unlock := r.s.begin()
	defer unlock()
	out := approvalsWhere(st, func(a core.Approval) bool { return a.Run == run })
	byCreated(out, func(a core.Approval) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return ptrs(out), nil
}

func (r approvalRepo) Seen(_ context.Context, p core.PrincipalID, recipient, host string) (bool, error) {
	st, unlock := r.s.begin()
	defer unlock()
	if recipient == "" && host == "" {
		return false, nil
	}
	for _, a := range st.approvals {
		if a.Principal != p || a.Status != core.ApprovalAllowed || a.Action == nil {
			continue
		}
		if (recipient != "" && a.Action.Recipient == recipient) || (host != "" && a.Action.Host == host) {
			return true, nil
		}
	}
	return false, nil
}

// AddMessage drops Ref.Chat.Kind: approval_messages has no column for it.
func (r approvalRepo) AddMessage(_ context.Context, id core.ApprovalID, m core.CardMessage) error {
	st, unlock := r.s.begin()
	defer unlock()
	if _, ok := st.approvals[id]; !ok {
		return invalid("approval_messages.approval_id: no such approval")
	}
	m.Kind = cmp.Or(m.Kind, core.CardMsgCard)
	if !oneOf(m.Kind, core.CardMsgCard, core.CardMsgLink, core.CardMsgNotice) {
		return invalid("approval_messages.kind")
	}
	m.Ref.Chat.Kind = ""
	for _, o := range st.cardMsgs {
		if o.approval == id && o.m.Ref.Chat.Channel == m.Ref.Chat.Channel &&
			o.m.Ref.Chat.ChatID == m.Ref.Chat.ChatID && o.m.Ref.ID == m.Ref.ID {
			return conflict("approval_messages primary key")
		}
	}
	st.cardMsgs = append(st.cardMsgs, cardMsgRow{approval: id, m: m})
	return nil
}

func (r approvalRepo) Messages(_ context.Context, id core.ApprovalID) ([]core.CardMessage, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.CardMessage
	for _, o := range st.cardMsgs {
		if o.approval == id {
			out = append(out, o.m)
		}
	}
	rank := map[string]int{core.CardMsgCard: 0, core.CardMsgLink: 1, core.CardMsgNotice: 2}
	slices.SortStableFunc(out, func(a, b core.CardMessage) int {
		if c := cmp.Compare(rank[a.Kind], rank[b.Kind]); c != 0 {
			return c
		}
		return cmp.Compare(b2i(a.Truncated), b2i(b.Truncated))
	})
	return out, nil
}

type voteKey struct {
	approval core.ApprovalID
	user     core.UserID
}

func (r approvalRepo) AddVote(_ context.Context, v core.ApprovalVote) error {
	st, unlock := r.s.begin()
	defer unlock()
	switch {
	case v.User == "" || !oneOf(v.Choice, core.AllowOnce, core.Deny):
		return invalid("approval_votes")
	case st.approvals[v.Approval].ID == "":
		return invalid("approval_votes.approval_id: no such approval")
	}
	k := voteKey{v.Approval, v.User}
	if _, ok := st.votes[k]; ok {
		return conflict("approval_votes primary key")
	}
	v.At = ms(v.At)
	st.votes[k] = v
	return nil
}

func (r approvalRepo) Votes(_ context.Context, id core.ApprovalID) ([]core.ApprovalVote, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.ApprovalVote
	for k, v := range st.votes {
		if k.approval == id {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b core.ApprovalVote) int {
		return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.User, b.User))
	})
	return out, nil
}

func (r approvalRepo) SetComment(_ context.Context, id core.ApprovalID, comment string) error {
	st, unlock := r.s.begin()
	defer unlock()
	a, ok := st.approvals[id]
	if !ok {
		return notFound("set approval comment")
	}
	a.Comment = comment
	st.approvals[id] = a
	return nil
}

func normGrant(g core.Grant) (core.Grant, error) {
	pattern, err := jsonCol("grants.pattern", g.Pattern)
	if err != nil {
		return g, err
	}
	g.Pattern = pattern
	g.CreatedAt = ms(g.CreatedAt)
	g.ExpiresAt = ms(g.ExpiresAt)
	return g, nil
}

func (st *state) checkGrant(g core.Grant) error {
	switch {
	case !oneOf(g.Scope, core.GrantSession, core.GrantSchedule, core.GrantPrincipal):
		return invalid("grants.scope")
	case (g.Scope == core.GrantPrincipal) != (g.ScopeID == ""):
		return invalid("grants: scope_id must be empty exactly for principal scope")
	case !oneOf(g.Kind, core.GrantTool, core.GrantDomain, core.GrantRecipient):
		return invalid("grants.kind")
	case g.Kind == core.GrantTool && (g.Tool == "" || len(g.Pattern) == 0):
		return invalid("grants: never a whole-tool grant")
	case g.AllowWhenTainted && g.Kind != core.GrantRecipient:
		return invalid("grants: allow_when_tainted requires kind recipient")
	case !g.MaxSE.Valid():
		return invalid("grants.max_se")
	}
	if _, ok := st.principals[g.Principal]; !ok {
		return invalid("grants.principal_id: no such principal")
	}
	return nil
}

// grantCreator keeps one of CreatedBy and CreatedByIdentity, as created_by
// ("chat:<identity>" for a chat approver) reads back.
func grantCreator(g core.Grant) core.Grant {
	if g.CreatedByIdentity != "" {
		g.CreatedBy = ""
	}
	return g
}

func usable(row grantRow) bool {
	return row.revokedAt.IsZero() && (row.g.MaxUses == 0 || row.g.Uses < row.g.MaxUses)
}

type grantRepo struct{ s *store }

func (r grantRepo) Add(_ context.Context, g *core.Grant) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *g
	row.ID = idOr(row.ID, core.PrefixGrant)
	if _, ok := st.grants[row.ID]; ok {
		return conflict("grants.id")
	}
	if err := st.checkGrant(row); err != nil {
		return err
	}
	row, err := normGrant(grantCreator(row))
	if err != nil {
		return err
	}
	st.grants[row.ID] = grantRow{g: row}
	g.ID = row.ID
	return nil
}

// Active also leaves out grants whose max_uses is spent, so a grant the policy
// sees is one Use will accept.
func (r grantRepo) Active(_ context.Context, p core.PrincipalID, s core.SessionID, sch core.ScheduleID, now time.Time) ([]*core.Grant, error) {
	return r.list(func(row grantRow) bool {
		g := row.g
		if g.Principal != p || !usable(row) || (!g.ExpiresAt.IsZero() && !g.ExpiresAt.After(now)) {
			return false
		}
		switch g.Scope {
		case core.GrantPrincipal:
			return true
		case core.GrantSession:
			return s != "" && g.ScopeID == string(s)
		case core.GrantSchedule:
			return sch != "" && g.ScopeID == string(sch)
		}
		return false
	})
}

func (r grantRepo) Use(_ context.Context, id core.GrantID) error {
	st, unlock := r.s.begin()
	defer unlock()
	row, ok := st.grants[id]
	if !ok {
		return notFound("grant")
	}
	if !usable(row) {
		return conflict("grant revoked or used up")
	}
	row.g.Uses++
	st.grants[id] = row
	return nil
}

// List omits revoked grants: Grant has no revoked_at field to tell them apart.
func (r grantRepo) List(_ context.Context, v core.Viewer, scope core.GrantScope, scopeID string) ([]*core.Grant, error) {
	return r.list(func(row grantRow) bool {
		g := row.g
		return v.Can(g.Principal) && row.revokedAt.IsZero() &&
			(scope == "" || g.Scope == scope) && (scopeID == "" || g.ScopeID == scopeID)
	})
}

func (r grantRepo) list(keep func(grantRow) bool) ([]*core.Grant, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Grant
	for _, row := range st.grants {
		if keep(row) {
			out = append(out, must(normGrant(row.g)))
		}
	}
	byCreated(out, func(g core.Grant) (time.Time, string) { return g.CreatedAt, string(g.ID) })
	return ptrs(out), nil
}

func (r grantRepo) Revoke(_ context.Context, v core.Viewer, id core.GrantID, at time.Time) error {
	st, unlock := r.s.begin()
	defer unlock()
	row, ok := st.grants[id]
	if !ok || !v.Can(row.g.Principal) || !row.revokedAt.IsZero() {
		return notFound("grant")
	}
	row.revokedAt = ms(at)
	st.grants[id] = row
	return nil
}

func normSchedule(s core.Schedule) (core.Schedule, error) {
	deliver, err := jsonCol("schedules.deliver", s.Deliver)
	if err != nil {
		return s, err
	}
	limits, err := jsonCol("schedules.limits", s.Limits)
	if err != nil {
		return s, err
	}
	s.Deliver, s.Limits = deliver, limits
	s.NextRunAt = ms(s.NextRunAt)
	s.LastRunAt = ms(s.LastRunAt)
	s.CreatedAt = ms(s.CreatedAt)
	s.UpdatedAt = ms(s.UpdatedAt)
	return s, nil
}

type scheduleRepo struct{ s *store }

// Upsert keeps created_at of an existing row and replaces everything else.
func (r scheduleRepo) Upsert(_ context.Context, s *core.Schedule) error {
	st, unlock := r.s.begin()
	defer unlock()
	row := *s
	row.ID = idOr(row.ID, core.PrefixSchedule)
	switch {
	case !oneOf(row.Kind, core.SchedCron, core.SchedHeartbeat, core.SchedOnce):
		return invalid("schedules.kind")
	case !oneOf(row.MissedRun, "skip", "once"):
		return invalid("schedules.missed_run")
	}
	if _, ok := st.principals[row.Principal]; !ok {
		return invalid("schedules.principal_id: no such principal")
	}
	if _, ok := st.sessions[row.Session]; row.Session != "" && !ok {
		return invalid("schedules.session_id: no such session")
	}
	if cur, ok := st.schedules[row.ID]; ok {
		row.CreatedAt = cur.CreatedAt
	}
	row, err := normSchedule(row)
	if err != nil {
		return err
	}
	st.schedules[row.ID] = row
	s.ID = row.ID
	return nil
}

func (r scheduleRepo) Get(_ context.Context, v core.Viewer, id core.ScheduleID) (*core.Schedule, error) {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.schedules[id]
	if !ok || !v.Can(s.Principal) {
		return nil, notFound("schedule")
	}
	s = must(normSchedule(s))
	return &s, nil
}

func (r scheduleRepo) List(_ context.Context, v core.Viewer) ([]*core.Schedule, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var out []core.Schedule
	for _, s := range st.schedules {
		if v.Can(s.Principal) {
			out = append(out, must(normSchedule(s)))
		}
	}
	byCreated(out, func(s core.Schedule) (time.Time, string) { return s.CreatedAt, string(s.ID) })
	return ptrs(out), nil
}

// Delete applies runs.schedule_id ON DELETE SET NULL.
func (r scheduleRepo) Delete(_ context.Context, v core.Viewer, id core.ScheduleID) error {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.schedules[id]
	if !ok || !v.Can(s.Principal) {
		return notFound("schedule")
	}
	delete(st.schedules, id)
	for rid, run := range st.runs {
		if run.Schedule == id {
			run.Schedule = ""
			st.runs[rid] = run
		}
	}
	return nil
}

func (r scheduleRepo) NextDue(context.Context) (*core.Schedule, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var best *core.Schedule
	for _, s := range st.schedules {
		if !s.Enabled {
			continue
		}
		if best == nil || cmp.Or(s.NextRunAt.Compare(best.NextRunAt), cmp.Compare(s.ID, best.ID)) < 0 {
			best = &s
		}
	}
	if best == nil {
		return nil, notFound("due schedule")
	}
	out := must(normSchedule(*best))
	return &out, nil
}

func (r scheduleRepo) SetRun(_ context.Context, id core.ScheduleID, last, next time.Time, status core.RunStatus, failures int, enabled bool) error {
	st, unlock := r.s.begin()
	defer unlock()
	s, ok := st.schedules[id]
	if !ok {
		return notFound("schedule")
	}
	s.LastRunAt, s.NextRunAt, s.LastStatus, s.Failures, s.Enabled = ms(last), ms(next), status, failures, enabled
	st.schedules[id] = s
	return nil
}

func (r scheduleRepo) PrincipalsWithEnabled(context.Context) ([]core.PrincipalID, error) {
	st, unlock := r.s.begin()
	defer unlock()
	seen := map[core.PrincipalID]bool{}
	var out []core.PrincipalID
	for _, s := range st.schedules {
		if s.Enabled && !seen[s.Principal] {
			seen[s.Principal] = true
			out = append(out, s.Principal)
		}
	}
	slices.Sort(out)
	return out, nil
}

type usageRepo struct{ s *store }

func (r usageRepo) AddSpend(_ context.Context, p core.PrincipalID, day string, c core.CallInfo) error {
	st, unlock := r.s.begin()
	defer unlock()
	st.spend[spendKey{p, day}] += c.Cost
	return nil
}

func (r usageRepo) Spend(_ context.Context, p core.PrincipalID, day string) (core.Micros, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var total core.Micros
	for k, cost := range st.spend {
		if k.day == day && (p == "" || k.principal == p) {
			total += cost
		}
	}
	return total, nil
}

func (r usageRepo) TeamSpend(_ context.Context, day string) (core.Micros, error) {
	st, unlock := r.s.begin()
	defer unlock()
	var total core.Micros
	for k, cost := range st.spend {
		if k.day == day && strings.HasPrefix(string(k.principal), core.PrefixSpace+"_") {
			total += cost
		}
	}
	return total, nil
}

// Report groups llm_calls rows created in [From, To) (zero bounds are open);
// day keys are UTC dates, schedule and session keys come from the call's run.
func (r usageRepo) Report(_ context.Context, q core.UsageQuery) ([]core.UsageRow, error) {
	st, unlock := r.s.begin()
	defer unlock()
	key := map[string]func(llmRow) string{
		"day":   func(l llmRow) string { return l.at.UTC().Format(time.DateOnly) },
		"model": func(l llmRow) string { return l.c.Ref.String() },
		"user": func(l llmRow) string {
			if l.user != "" {
				return string(l.user)
			}
			return string(l.principal)
		},
		"schedule": func(l llmRow) string {
			return string(st.runs[l.run].Schedule)
		},
		"session": func(l llmRow) string { return string(st.runs[l.run].Session) },
	}[q.GroupBy]
	if key == nil {
		return nil, invalid("usage group_by")
	}
	rows := map[string]*core.UsageRow{}
	for _, l := range st.llmCalls {
		if (q.User != "" && l.user != q.User) ||
			(!q.From.IsZero() && l.at.Before(q.From)) ||
			(!q.To.IsZero() && !l.at.Before(q.To)) {
			continue
		}
		k := key(l)
		row, ok := rows[k]
		if !ok {
			row = &core.UsageRow{Key: k, PriceKnown: true}
			rows[k] = row
		}
		row.Cost += l.c.Cost
		row.Input += l.c.Usage.Input
		row.Output += l.c.Usage.Output
		row.CacheRead += l.c.Usage.CacheRead
		row.CacheWrite += l.c.Usage.CacheWrite
		row.Calls++
		row.PriceKnown = row.PriceKnown && l.c.PriceKnown
	}
	out := make([]core.UsageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b core.UsageRow) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}
