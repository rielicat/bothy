package store

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const runCols = `id, session_id, principal_id, user_id, trigger, COALESCE(schedule_id, ''), status, error, steps, tool_calls,
	input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros, models, limits, parked_on, stopped_by,
	created_at, started_at, finished_at`

func scanRun(sc scanner) (*core.Run, error) {
	var r core.Run
	var models, limits string
	var created, started, finished int64
	if err := sc.Scan(&r.ID, &r.Session, &r.Principal, &r.User, &r.Trigger, &r.Schedule, &r.Status, &r.Error, &r.Steps,
		&r.ToolCalls, &r.Usage.Input, &r.Usage.Output, &r.Usage.CacheRead, &r.Usage.CacheWrite, &r.Cost, &models, &limits,
		&r.ParkedOn, &r.StoppedBy, &created, &started, &finished); err != nil {
		return nil, err
	}
	if err := decodeJSON("runs.models", models, &r.Models); err != nil {
		return nil, err
	}
	if err := decodeJSON("runs.limits", limits, &r.Limits); err != nil {
		return nil, err
	}
	r.CreatedAt, r.StartedAt, r.FinishedAt = fromMS(created), fromMS(started), fromMS(finished)
	return &r, nil
}

func runJSON(r *core.Run) (models, limits string, err error) {
	m := r.Models
	if m == nil {
		m = []string{}
	}
	if models, err = jsonText("runs.models", m); err != nil {
		return "", "", err
	}
	limits, err = jsonText("runs.limits", r.Limits)
	return models, limits, err
}

type runRepo struct{ h *handle }

func (r runRepo) Create(ctx context.Context, run *core.Run) error {
	id := idOr(run.ID, core.PrefixRun)
	models, limits, err := runJSON(run)
	if err != nil {
		return err
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO runs(`+runColsInsert+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(id), string(run.Session), string(run.Principal), string(run.User), string(run.Trigger), nullStr(run.Schedule),
		string(run.Status), run.Error, run.Steps, run.ToolCalls, run.Usage.Input, run.Usage.Output, run.Usage.CacheRead,
		run.Usage.CacheWrite, int64(run.Cost), models, limits, string(run.ParkedOn), string(run.StoppedBy),
		ms(run.CreatedAt), ms(run.StartedAt), ms(run.FinishedAt)); err != nil {
		return dbErr("create run", err)
	}
	run.ID = id
	return nil
}

const runColsInsert = `id, session_id, principal_id, user_id, trigger, schedule_id, status, error, steps, tool_calls,
	input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros, models, limits, parked_on, stopped_by,
	created_at, started_at, finished_at`

// Update leaves session_id, principal_id and created_at as created.
func (r runRepo) Update(ctx context.Context, run *core.Run) error {
	models, limits, err := runJSON(run)
	if err != nil {
		return err
	}
	res, err := r.h.wq.ExecContext(ctx, `UPDATE runs SET user_id = ?, trigger = ?, schedule_id = ?, status = ?, error = ?,
		steps = ?, tool_calls = ?, input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
		cost_micros = ?, models = ?, limits = ?, parked_on = ?, stopped_by = ?, started_at = ?, finished_at = ? WHERE id = ?`,
		string(run.User), string(run.Trigger), nullStr(run.Schedule), string(run.Status), run.Error, run.Steps, run.ToolCalls,
		run.Usage.Input, run.Usage.Output, run.Usage.CacheRead, run.Usage.CacheWrite, int64(run.Cost), models, limits,
		string(run.ParkedOn), string(run.StoppedBy), ms(run.StartedAt), ms(run.FinishedAt), string(run.ID))
	return mustAffect("update run", res, err)
}

func (r runRepo) Get(ctx context.Context, v core.Viewer, id core.RunID) (*core.Run, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, notFound("run")
	}
	return queryOne(ctx, r.h.rq, "run", scanRun, `SELECT `+runCols+` FROM runs WHERE id = ? AND `+cond, append([]any{string(id)}, args...)...)
}

func (r runRepo) List(ctx context.Context, v core.Viewer, f core.RunFilter) ([]*core.Run, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, nil
	}
	q := `SELECT ` + runCols + ` FROM runs WHERE ` + cond
	if f.Session != "" {
		q += ` AND session_id = ?`
		args = append(args, string(f.Session))
	}
	if f.Schedule != "" {
		q += ` AND schedule_id = ?`
		args = append(args, string(f.Schedule))
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, string(f.Status))
	}
	if f.Page.Before > 0 {
		q += ` AND created_at < ?`
		args = append(args, f.Page.Before)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, pageLimit(f.Page.Limit))
	return queryAll(ctx, r.h.rq, "list runs", scanRun, q, args...)
}

func (r runRepo) MarkInterrupted(ctx context.Context, now time.Time) ([]*core.Run, error) {
	out, err := queryAll(ctx, r.h.wq, "mark interrupted", scanRun, `UPDATE runs SET status = 'interrupted', finished_at = ?
		WHERE status IN ('queued', 'running', 'waiting_approval') RETURNING `+runCols, ms(now))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b *core.Run) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r runRepo) Parked(ctx context.Context) ([]*core.Run, error) {
	return queryAll(ctx, r.h.rq, "parked runs", scanRun, `SELECT `+runCols+` FROM runs WHERE status = 'parked' ORDER BY created_at, id`)
}

// visibleRun checks the by-ID rule for run-scoped reads.
func (h *handle) visibleRun(ctx context.Context, v core.Viewer, id core.RunID) error {
	var p core.PrincipalID
	if err := h.rq.QueryRowContext(ctx, `SELECT principal_id FROM runs WHERE id = ?`, string(id)).Scan(&p); err != nil {
		return dbErr("run", err)
	}
	if !v.Can(p) {
		return notFound("run")
	}
	return nil
}

// AddLLMCall stamps created_at with the store clock: CallInfo has no time and
// usage reports filter on it. run "" is a passthrough or probe (run_id NULL).
func (r runRepo) AddLLMCall(ctx context.Context, run core.RunID, p core.PrincipalID, u core.UserID, c core.CallInfo) error {
	id := idOr(c.ID, core.PrefixLLMCall)
	attempts := c.Attempts
	if attempts == nil {
		attempts = []core.Attempt{}
	}
	aj, err := jsonText("llm_calls.attempts", attempts)
	if err != nil {
		return err
	}
	bj, err := jsonText("llm_calls.breakdown", c.Breakdown)
	if err != nil {
		return err
	}
	_, err = r.h.wq.ExecContext(ctx, `INSERT INTO llm_calls(id, run_id, principal_id, user_id, step, purpose, provider, model,
		slot, attempts, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, usage_estimated,
		cost_micros, price_known, breakdown, prefix_hash, ttft_ms, latency_ms, stop_reason, error, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, nullStr(run), string(p), string(u), c.Step, c.Purpose, c.Ref.Provider, c.Ref.Model, string(c.Slot), aj,
		c.Usage.Input, c.Usage.Output, c.Usage.CacheRead, c.Usage.CacheWrite, c.Usage.Reasoning, b2i(c.UsageEstimated),
		int64(c.Cost), b2i(c.PriceKnown), bj, c.PrefixHash, durMS(c.TTFT), durMS(c.Latency), string(c.Stop), c.Error,
		ms(r.h.now()))
	return dbErr("add llm call", err)
}

func (r runRepo) LLMCalls(ctx context.Context, v core.Viewer, run core.RunID) ([]core.CallInfo, error) {
	if err := r.h.visibleRun(ctx, v, run); err != nil {
		return nil, err
	}
	return queryAll(ctx, r.h.rq, "llm calls", func(sc scanner) (core.CallInfo, error) {
		var c core.CallInfo
		var attempts, breakdown string
		var ttft, latency int64
		if err := sc.Scan(&c.ID, &c.Ref.Provider, &c.Ref.Model, &c.Slot, &c.Purpose, &c.Step, &attempts, &c.Usage.Input,
			&c.Usage.Output, &c.Usage.CacheRead, &c.Usage.CacheWrite, &c.Usage.Reasoning, &c.UsageEstimated, &c.Cost,
			&c.PriceKnown, &breakdown, &c.PrefixHash, &ttft, &latency, &c.Stop, &c.Error); err != nil {
			return c, err
		}
		if err := decodeJSON("llm_calls.attempts", attempts, &c.Attempts); err != nil {
			return c, err
		}
		if err := decodeJSON("llm_calls.breakdown", breakdown, &c.Breakdown); err != nil {
			return c, err
		}
		c.TTFT, c.Latency = fromDurMS(ttft), fromDurMS(latency)
		return c, nil
	}, `SELECT id, provider, model, slot, purpose, step, attempts, input_tokens, output_tokens, cache_read_tokens,
		cache_write_tokens, reasoning_tokens, usage_estimated, cost_micros, price_known, breakdown, prefix_hash, ttft_ms,
		latency_ms, stop_reason, error FROM llm_calls WHERE run_id = ? ORDER BY step, created_at, rowid`, string(run))
}

const toolCallCols = `id, run_id, session_id, principal_id, step, call_id, tool, source, side_effect, action, verdict, rule,
	approval_id, status, result_preview, result_bytes, full_ref, untrusted, duration_ms, created_at, finished_at`

func scanToolCall(sc scanner) (*core.ToolCallRecord, error) {
	var tc core.ToolCallRecord
	var se string
	var action sql.NullString
	var dur, created, finished int64
	if err := sc.Scan(&tc.ID, &tc.Run, &tc.Session, &tc.Principal, &tc.Step, &tc.CallID, &tc.Tool, &tc.Source, &se, &action,
		&tc.Verdict, &tc.Rule, &tc.Approval, &tc.Status, &tc.ResultPreview, &tc.ResultBytes, &tc.FullRef, &tc.Untrusted,
		&dur, &created, &finished); err != nil {
		return nil, err
	}
	var err error
	if tc.Action, err = decodeNullable[core.Action]("tool_calls.action", action); err != nil {
		return nil, err
	}
	tc.SideEffect = parseSE(se)
	tc.Duration, tc.CreatedAt, tc.FinishedAt = fromDurMS(dur), fromMS(created), fromMS(finished)
	return &tc, nil
}

func (r runRepo) AddToolCall(ctx context.Context, tc *core.ToolCallRecord) error {
	id := idOr(tc.ID, core.PrefixToolCall)
	action, err := jsonNullable("tool_calls.action", tc.Action)
	if err != nil {
		return err
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO tool_calls(`+toolCallCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, string(tc.Run), string(tc.Session), string(tc.Principal), tc.Step, tc.CallID, tc.Tool, tc.Source, seText(tc.SideEffect),
		action, string(tc.Verdict), tc.Rule, string(tc.Approval), string(tc.Status), tc.ResultPreview, tc.ResultBytes, tc.FullRef,
		b2i(tc.Untrusted), durMS(tc.Duration), ms(tc.CreatedAt), ms(tc.FinishedAt)); err != nil {
		return dbErr("add tool call", err)
	}
	tc.ID = id
	return nil
}

// UpdateToolCall leaves run_id and created_at as created.
func (r runRepo) UpdateToolCall(ctx context.Context, tc *core.ToolCallRecord) error {
	action, err := jsonNullable("tool_calls.action", tc.Action)
	if err != nil {
		return err
	}
	res, err := r.h.wq.ExecContext(ctx, `UPDATE tool_calls SET session_id = ?, principal_id = ?, step = ?, call_id = ?, tool = ?,
		source = ?, side_effect = ?, action = ?, verdict = ?, rule = ?, approval_id = ?, status = ?, result_preview = ?,
		result_bytes = ?, full_ref = ?, untrusted = ?, duration_ms = ?, finished_at = ? WHERE id = ?`,
		string(tc.Session), string(tc.Principal), tc.Step, tc.CallID, tc.Tool, tc.Source, seText(tc.SideEffect), action,
		string(tc.Verdict), tc.Rule, string(tc.Approval), string(tc.Status), tc.ResultPreview, tc.ResultBytes, tc.FullRef,
		b2i(tc.Untrusted), durMS(tc.Duration), ms(tc.FinishedAt), tc.ID)
	return mustAffect("update tool call", res, err)
}

func (r runRepo) ToolCalls(ctx context.Context, v core.Viewer, run core.RunID) ([]*core.ToolCallRecord, error) {
	if err := r.h.visibleRun(ctx, v, run); err != nil {
		return nil, err
	}
	return queryAll(ctx, r.h.rq, "tool calls", scanToolCall,
		`SELECT `+toolCallCols+` FROM tool_calls WHERE run_id = ? ORDER BY step, rowid`, string(run))
}

const approvalCols = `id, run_id, session_id, principal_id, tool_call_id, action, decision, card, approver, chat, status, choice,
	on_timeout, decided_by, decided_via, created_at, expires_at, park_until, decided_at`

func scanApproval(sc scanner) (*core.Approval, error) {
	var a core.Approval
	var action, decision, card, approver string
	var chat sql.NullString
	var created, expires, park, decided int64
	if err := sc.Scan(&a.ID, &a.Run, &a.Session, &a.Principal, &a.ToolCallID, &action, &decision, &card, &approver, &chat,
		&a.Status, &a.Choice, &a.OnTimeout, &a.DecidedBy, &a.DecidedVia, &created, &expires, &park, &decided); err != nil {
		return nil, err
	}
	if action != "null" {
		a.Action = new(core.Action)
		if err := decodeJSON("approvals.action", action, a.Action); err != nil {
			return nil, err
		}
	}
	if err := decodeJSON("approvals.decision", decision, &a.Decision); err != nil {
		return nil, err
	}
	if err := decodeJSON("approvals.card", card, &a.Card); err != nil {
		return nil, err
	}
	if err := decodeJSON("approvals.approver", approver, &a.Approver); err != nil {
		return nil, err
	}
	var err error
	if a.Chat, err = decodeNullable[core.ChatRef]("approvals.chat", chat); err != nil {
		return nil, err
	}
	a.CreatedAt, a.ExpiresAt, a.ParkUntil, a.DecidedAt = fromMS(created), fromMS(expires), fromMS(park), fromMS(decided)
	return &a, nil
}

var approvalStatuses = []core.ApprovalStatus{core.ApprovalPending, core.ApprovalAllowed, core.ApprovalDenied,
	core.ApprovalExpired, core.ApprovalParked, core.ApprovalCancelled}

// approvalVisible is the approval form of the Viewer rule: the principal's
// rows, plus admin-only approvals for admins (Viewer.Admin's first exception).
func approvalVisible(v core.Viewer) (cond string, args []any, ok bool) {
	cond, args, ok = viewerCond("principal_id", v)
	if v.System || !v.Admin {
		return cond, args, ok
	}
	admin := `json_extract(approver, '$.admin_only') = 1`
	if !ok {
		return admin, nil, true
	}
	return "(" + cond + " OR " + admin + ")", args, true
}

type approvalRepo struct{ h *handle }

func (r approvalRepo) Create(ctx context.Context, a *core.Approval) error {
	id := idOr(a.ID, core.PrefixApproval)
	action, err := jsonText("approvals.action", a.Action)
	if err != nil {
		return err
	}
	decision, err := jsonText("approvals.decision", a.Decision)
	if err != nil {
		return err
	}
	card, err := jsonText("approvals.card", a.Card)
	if err != nil {
		return err
	}
	approver, err := jsonText("approvals.approver", a.Approver)
	if err != nil {
		return err
	}
	chat, err := jsonNullable("approvals.chat", a.Chat)
	if err != nil {
		return err
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO approvals(`+approvalCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(id), string(a.Run), string(a.Session), string(a.Principal), a.ToolCallID, action, decision, card, approver, chat,
		string(a.Status), string(a.Choice), string(a.OnTimeout), string(a.DecidedBy), a.DecidedVia, ms(a.CreatedAt),
		ms(a.ExpiresAt), ms(a.ParkUntil), ms(a.DecidedAt)); err != nil {
		return dbErr("create approval", err)
	}
	a.ID = id
	return nil
}

func (r approvalRepo) Get(ctx context.Context, v core.Viewer, id core.ApprovalID) (*core.Approval, error) {
	cond, args, ok := approvalVisible(v)
	if !ok {
		return nil, notFound("approval")
	}
	return queryOne(ctx, r.h.rq, "approval", scanApproval,
		`SELECT `+approvalCols+` FROM approvals WHERE id = ? AND `+cond, append([]any{string(id)}, args...)...)
}

// Decide is one conditional UPDATE, so of any number of concurrent deciders
// exactly one sees a changed row.
func (r approvalRepo) Decide(ctx context.Context, id core.ApprovalID, from []core.ApprovalStatus, to core.ApprovalStatus,
	c core.Choice, by core.UserID, via string, at time.Time) (bool, error) {
	if !oneOf(to, approvalStatuses...) {
		return false, invalid("approvals.status")
	}
	if len(from) > 0 {
		args := []any{string(to), string(c), string(by), via, ms(at), string(id)}
		for _, s := range from {
			args = append(args, string(s))
		}
		res, err := r.h.wq.ExecContext(ctx, `UPDATE approvals SET status = ?, choice = ?, decided_by = ?, decided_via = ?, decided_at = ?
			WHERE id = ? AND status IN (`+placeholders(len(from))+`)`, args...)
		if err != nil {
			return false, dbErr("decide approval", err)
		}
		if n, err := res.RowsAffected(); err != nil || n > 0 {
			return n > 0, dbErr("decide approval", err)
		}
	}
	ok, err := exists(ctx, r.h.wq, "decide approval", `SELECT 1 FROM approvals WHERE id = ?`, string(id))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, notFound("approval")
	}
	return false, nil
}

// Park only moves an open approval: a late timeout never reopens a decided one.
func (r approvalRepo) Park(ctx context.Context, id core.ApprovalID, until time.Time) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE approvals SET status = 'parked', park_until = ?
		WHERE id = ? AND status IN ('pending', 'parked')`, ms(until), string(id))
	if err != nil {
		return dbErr("park approval", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	ok, err := exists(ctx, r.h.wq, "park approval", `SELECT 1 FROM approvals WHERE id = ?`, string(id))
	if err != nil {
		return err
	}
	if !ok {
		return notFound("approval")
	}
	return conflict("approval already decided")
}

func (r approvalRepo) Pending(ctx context.Context, v core.Viewer) ([]*core.Approval, error) {
	cond, args, ok := approvalVisible(v)
	if !ok {
		return nil, nil
	}
	return queryAll(ctx, r.h.rq, "pending approvals", scanApproval, `SELECT `+approvalCols+` FROM approvals
		WHERE status IN ('pending', 'parked') AND `+cond+` ORDER BY created_at, id`, args...)
}

func (r approvalRepo) Recent(ctx context.Context, v core.Viewer, p core.Page) ([]*core.Approval, error) {
	cond, args, ok := approvalVisible(v)
	if !ok {
		return nil, nil
	}
	q := `SELECT ` + approvalCols + ` FROM approvals WHERE status NOT IN ('pending', 'parked') AND ` + cond
	if p.Before > 0 {
		q += ` AND created_at < ?`
		args = append(args, p.Before)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, pageLimit(p.Limit))
	return queryAll(ctx, r.h.rq, "recent approvals", scanApproval, q, args...)
}

// dueApprovals runs every 30 s for the life of the process over a table
// that keeps every approval, so each branch must stay on its partial index
// (approvals_due, approvals_parked).
const dueApprovals = `SELECT ` + approvalCols + ` FROM (
	SELECT * FROM approvals WHERE status = 'pending' AND expires_at <= ?1
	UNION ALL SELECT * FROM approvals WHERE status = 'parked' AND park_until <= ?1)
	ORDER BY created_at, id`

func (r approvalRepo) Due(ctx context.Context, now time.Time) ([]*core.Approval, error) {
	return queryAll(ctx, r.h.rq, "due approvals", scanApproval, dueApprovals, ms(now))
}

func (r approvalRepo) ByRun(ctx context.Context, run core.RunID) ([]*core.Approval, error) {
	return queryAll(ctx, r.h.rq, "approvals by run", scanApproval,
		`SELECT `+approvalCols+` FROM approvals WHERE run_id = ? ORDER BY created_at, id`, string(run))
}

// The expressions must match approvals_seen_recipient and approvals_seen_host
// exactly for SQLite to use them.
const (
	seenRecipient = `SELECT 1 FROM approvals WHERE principal_id = ? AND status = 'allowed' AND json_extract(action, '$.recipient') = ?`
	seenHost      = `SELECT 1 FROM approvals WHERE principal_id = ? AND status = 'allowed' AND json_extract(action, '$.host') = ?`
)

func (r approvalRepo) Seen(ctx context.Context, p core.PrincipalID, recipient, host string) (bool, error) {
	for _, c := range []struct{ q, v string }{{seenRecipient, recipient}, {seenHost, host}} {
		if c.v == "" {
			continue
		}
		if ok, err := exists(ctx, r.h.rq, "approval seen", c.q, string(p), c.v); ok || err != nil {
			return ok, err
		}
	}
	return false, nil
}

// AddMessage stores no chat kind: approval_messages has no column for it.
func (r approvalRepo) AddMessage(ctx context.Context, id core.ApprovalID, m core.CardMessage) error {
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO approval_messages(approval_id, channel, chat_id, thread_id, message_id, truncated)
		VALUES (?,?,?,?,?,?)`, string(id), m.Ref.Chat.Channel, m.Ref.Chat.ChatID, m.Ref.Chat.ThreadID, m.Ref.ID, b2i(m.Truncated))
	return dbErr("add card message", err)
}

// Messages lists full cards before truncated ones. approval_messages is a
// WITHOUT ROWID table, so insertion order is not recoverable.
func (r approvalRepo) Messages(ctx context.Context, id core.ApprovalID) ([]core.CardMessage, error) {
	return queryAll(ctx, r.h.rq, "card messages", func(sc scanner) (core.CardMessage, error) {
		var m core.CardMessage
		err := sc.Scan(&m.Ref.Chat.Channel, &m.Ref.Chat.ChatID, &m.Ref.Chat.ThreadID, &m.Ref.ID, &m.Truncated)
		return m, err
	}, `SELECT channel, chat_id, thread_id, message_id, truncated FROM approval_messages WHERE approval_id = ?
		ORDER BY truncated, channel, chat_id, thread_id, message_id`, string(id))
}

const grantCols = `id, principal_id, scope, scope_id, kind, tool, pattern, value, max_se, allow_when_tainted, uses, max_uses,
	created_by, created_at, expires_at`

func scanGrant(sc scanner) (*core.Grant, error) {
	var g core.Grant
	var pattern, se string
	var created, expires int64
	if err := sc.Scan(&g.ID, &g.Principal, &g.Scope, &g.ScopeID, &g.Kind, &g.Tool, &pattern, &g.Value, &se, &g.AllowWhenTainted,
		&g.Uses, &g.MaxUses, &g.CreatedBy, &created, &expires); err != nil {
		return nil, err
	}
	if pattern != "{}" {
		if err := decodeJSON("grants.pattern", pattern, &g.Pattern); err != nil {
			return nil, err
		}
	}
	g.MaxSE = parseSE(se)
	g.CreatedAt, g.ExpiresAt = fromMS(created), fromMS(expires)
	return &g, nil
}

type grantRepo struct{ h *handle }

// Add stores a nil or empty Pattern as '{}', which the whole-tool CHECK
// rejects for tool grants.
func (r grantRepo) Add(ctx context.Context, g *core.Grant) error {
	id := idOr(g.ID, core.PrefixGrant)
	pattern := "{}"
	if len(g.Pattern) > 0 {
		var err error
		if pattern, err = jsonText("grants.pattern", g.Pattern); err != nil {
			return err
		}
	}
	if _, err := r.h.wq.ExecContext(ctx, `INSERT INTO grants(`+grantCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(id), string(g.Principal), string(g.Scope), g.ScopeID, string(g.Kind), g.Tool, pattern, g.Value, seText(g.MaxSE),
		b2i(g.AllowWhenTainted), g.Uses, g.MaxUses, string(g.CreatedBy), ms(g.CreatedAt), ms(g.ExpiresAt)); err != nil {
		return dbErr("add grant", err)
	}
	g.ID = id
	return nil
}

// Active also leaves out used-up grants, so every grant the policy sees is
// one Use will accept.
func (r grantRepo) Active(ctx context.Context, p core.PrincipalID, s core.SessionID, sch core.ScheduleID, now time.Time) ([]*core.Grant, error) {
	return queryAll(ctx, r.h.rq, "active grants", scanGrant, `SELECT `+grantCols+` FROM grants
		WHERE principal_id = ?1 AND revoked_at = 0 AND (max_uses = 0 OR uses < max_uses) AND (expires_at = 0 OR expires_at > ?2)
		AND (scope = 'principal' OR (scope = 'session' AND ?3 <> '' AND scope_id = ?3) OR (scope = 'schedule' AND ?4 <> '' AND scope_id = ?4))
		ORDER BY created_at, id`, string(p), ms(now), string(s), string(sch))
}

func (r grantRepo) Use(ctx context.Context, id core.GrantID) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE grants SET uses = uses + 1
		WHERE id = ? AND revoked_at = 0 AND (max_uses = 0 OR uses < max_uses)`, string(id))
	if err != nil {
		return dbErr("use grant", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	ok, err := exists(ctx, r.h.wq, "use grant", `SELECT 1 FROM grants WHERE id = ?`, string(id))
	if err != nil {
		return err
	}
	if !ok {
		return notFound("grant")
	}
	return conflict("grant revoked or used up")
}

// List omits revoked grants: Grant has no revoked_at field to tell them apart.
func (r grantRepo) List(ctx context.Context, v core.Viewer, scope core.GrantScope, scopeID string) ([]*core.Grant, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, nil
	}
	args = append(args, string(scope), string(scope), scopeID, scopeID)
	return queryAll(ctx, r.h.rq, "list grants", scanGrant, `SELECT `+grantCols+` FROM grants WHERE `+cond+`
		AND revoked_at = 0 AND (? = '' OR scope = ?) AND (? = '' OR scope_id = ?) ORDER BY created_at, id`, args...)
}

func (r grantRepo) Revoke(ctx context.Context, v core.Viewer, id core.GrantID, at time.Time) error {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return notFound("grant")
	}
	if at.IsZero() {
		at = r.h.now()
	}
	res, err := r.h.wq.ExecContext(ctx, `UPDATE grants SET revoked_at = ? WHERE id = ? AND revoked_at = 0 AND `+cond,
		append([]any{ms(at), string(id)}, args...)...)
	return mustAffect("revoke grant", res, err)
}

const scheduleCols = `id, principal_id, name, kind, spec, tz, active_hours, prompt, COALESCE(session_id, ''), deliver, model_slot,
	limits, missed_run, enabled, created_by, failures, next_run_at, last_run_at, last_status, created_at, updated_at`

const scheduleColsInsert = `id, principal_id, name, kind, spec, tz, active_hours, prompt, session_id, deliver, model_slot,
	limits, missed_run, enabled, created_by, failures, next_run_at, last_run_at, last_status, created_at, updated_at`

func scanSchedule(sc scanner) (*core.Schedule, error) {
	var s core.Schedule
	var deliver sql.NullString
	var limits string
	var next, last, created, updated int64
	if err := sc.Scan(&s.ID, &s.Principal, &s.Name, &s.Kind, &s.Spec, &s.TZ, &s.ActiveHours, &s.Prompt, &s.Session, &deliver,
		&s.Slot, &limits, &s.MissedRun, &s.Enabled, &s.CreatedBy, &s.Failures, &next, &last, &s.LastStatus, &created, &updated); err != nil {
		return nil, err
	}
	var err error
	if s.Deliver, err = decodeNullable[core.ChatRef]("schedules.deliver", deliver); err != nil {
		return nil, err
	}
	if err := decodeJSON("schedules.limits", limits, &s.Limits); err != nil {
		return nil, err
	}
	s.NextRunAt, s.LastRunAt, s.CreatedAt, s.UpdatedAt = fromMS(next), fromMS(last), fromMS(created), fromMS(updated)
	return &s, nil
}

type scheduleRepo struct{ h *handle }

// Upsert keeps created_at of an existing row and replaces everything else.
// An existing schedule never changes principal: such an Upsert finds nothing
// to update and reports ErrNotFound.
func (r scheduleRepo) Upsert(ctx context.Context, s *core.Schedule) error {
	id := idOr(s.ID, core.PrefixSchedule)
	deliver, err := jsonNullable("schedules.deliver", s.Deliver)
	if err != nil {
		return err
	}
	limits, err := jsonText("schedules.limits", s.Limits)
	if err != nil {
		return err
	}
	res, err := r.h.wq.ExecContext(ctx, `INSERT INTO schedules(`+scheduleColsInsert+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, kind = excluded.kind, spec = excluded.spec, tz = excluded.tz,
		active_hours = excluded.active_hours, prompt = excluded.prompt, session_id = excluded.session_id,
		deliver = excluded.deliver, model_slot = excluded.model_slot, limits = excluded.limits,
		missed_run = excluded.missed_run, enabled = excluded.enabled, created_by = excluded.created_by,
		failures = excluded.failures, next_run_at = excluded.next_run_at, last_run_at = excluded.last_run_at,
		last_status = excluded.last_status, updated_at = excluded.updated_at
		WHERE schedules.principal_id = excluded.principal_id`,
		string(id), string(s.Principal), s.Name, string(s.Kind), s.Spec, s.TZ, s.ActiveHours, s.Prompt, nullStr(s.Session),
		deliver, string(s.Slot), limits, s.MissedRun, b2i(s.Enabled), s.CreatedBy, s.Failures, ms(s.NextRunAt), ms(s.LastRunAt),
		string(s.LastStatus), ms(s.CreatedAt), ms(s.UpdatedAt))
	if err := mustAffect("upsert schedule", res, err); err != nil {
		return err
	}
	s.ID = id
	return nil
}

func (r scheduleRepo) Get(ctx context.Context, v core.Viewer, id core.ScheduleID) (*core.Schedule, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, notFound("schedule")
	}
	return queryOne(ctx, r.h.rq, "schedule", scanSchedule,
		`SELECT `+scheduleCols+` FROM schedules WHERE id = ? AND `+cond, append([]any{string(id)}, args...)...)
}

func (r scheduleRepo) List(ctx context.Context, v core.Viewer) ([]*core.Schedule, error) {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return nil, nil
	}
	return queryAll(ctx, r.h.rq, "list schedules", scanSchedule,
		`SELECT `+scheduleCols+` FROM schedules WHERE `+cond+` ORDER BY created_at, id`, args...)
}

// Delete relies on runs.schedule_id ON DELETE SET NULL.
func (r scheduleRepo) Delete(ctx context.Context, v core.Viewer, id core.ScheduleID) error {
	cond, args, ok := viewerCond("principal_id", v)
	if !ok {
		return notFound("schedule")
	}
	res, err := r.h.wq.ExecContext(ctx, `DELETE FROM schedules WHERE id = ? AND `+cond, append([]any{string(id)}, args...)...)
	return mustAffect("delete schedule", res, err)
}

func (r scheduleRepo) NextDue(ctx context.Context) (*core.Schedule, error) {
	return queryOne(ctx, r.h.rq, "due schedule", scanSchedule,
		`SELECT `+scheduleCols+` FROM schedules WHERE enabled = 1 ORDER BY next_run_at, id LIMIT 1`)
}

func (r scheduleRepo) SetRun(ctx context.Context, id core.ScheduleID, last, next time.Time, status core.RunStatus, failures int, enabled bool) error {
	res, err := r.h.wq.ExecContext(ctx, `UPDATE schedules SET last_run_at = ?, next_run_at = ?, last_status = ?, failures = ?, enabled = ?
		WHERE id = ?`, ms(last), ms(next), string(status), failures, b2i(enabled), string(id))
	return mustAffect("set schedule run", res, err)
}

type usageRepo struct{ h *handle }

func (r usageRepo) AddSpend(ctx context.Context, u core.UserID, day string, c core.CallInfo) error {
	_, err := r.h.wq.ExecContext(ctx, `INSERT INTO spend_daily(user_id, day, cost_micros, input_tokens, output_tokens, calls)
		VALUES (?, ?, ?, ?, ?, 1) ON CONFLICT(user_id, day) DO UPDATE SET cost_micros = cost_micros + excluded.cost_micros,
		input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens,
		calls = calls + 1`, string(u), day, int64(c.Cost), c.Usage.Input, c.Usage.Output)
	return dbErr("add spend", err)
}

func (r usageRepo) Spend(ctx context.Context, u core.UserID, day string) (core.Micros, error) {
	var total int64
	if err := r.h.rq.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros), 0) FROM spend_daily
		WHERE day = ?1 AND (?2 = '' OR user_id = ?2)`, day, string(u)).Scan(&total); err != nil {
		return 0, dbErr("spend", err)
	}
	return core.Micros(total), nil
}

// usageKeys are the GROUP BY expressions of Report; schedule and session come
// from the call's run, so calls outside runs group under "".
var usageKeys = map[string]string{
	"day":      `strftime('%Y-%m-%d', c.created_at / 1000, 'unixepoch')`,
	"model":    `c.provider || '/' || c.model`,
	"user":     `c.user_id`,
	"schedule": `COALESCE(r.schedule_id, '')`,
	"session":  `COALESCE(r.session_id, '')`,
}

// usageQuery builds Report's statement. llm_calls is never pruned by default,
// so only the bounds that are set appear in WHERE ("? = 0 OR …" forms would
// hide the indexes from the planner), and runs is joined only for the keys
// that come from it.
func usageQuery(q core.UsageQuery) (string, []any, error) {
	key, ok := usageKeys[q.GroupBy]
	if !ok {
		return "", nil, invalid("usage group_by " + q.GroupBy)
	}
	var b strings.Builder
	b.WriteString(`SELECT ` + key + ` AS k, SUM(c.cost_micros), SUM(c.input_tokens), SUM(c.output_tokens), SUM(c.cache_read_tokens),
		SUM(c.cache_write_tokens), COUNT(*), MIN(c.price_known) FROM llm_calls c`)
	if strings.Contains(key, "r.") {
		b.WriteString(` LEFT JOIN runs r ON r.id = c.run_id`)
	}
	b.WriteString(` WHERE 1`)
	var args []any
	if q.User != "" {
		b.WriteString(` AND c.user_id = ?`)
		args = append(args, string(q.User))
	}
	if !q.From.IsZero() {
		b.WriteString(` AND c.created_at >= ?`)
		args = append(args, ms(q.From))
	}
	if !q.To.IsZero() {
		b.WriteString(` AND c.created_at < ?`)
		args = append(args, ms(q.To))
	}
	b.WriteString(` GROUP BY k ORDER BY k`)
	return b.String(), args, nil
}

// Report aggregates llm_calls created in [From, To) (zero bounds are open).
// Day keys are UTC dates.
func (r usageRepo) Report(ctx context.Context, q core.UsageQuery) ([]core.UsageRow, error) {
	query, args, err := usageQuery(q)
	if err != nil {
		return nil, err
	}
	rows, err := queryAll(ctx, r.h.rq, "usage report", func(sc scanner) (core.UsageRow, error) {
		var u core.UsageRow
		err := sc.Scan(&u.Key, &u.Cost, &u.Input, &u.Output, &u.CacheRead, &u.CacheWrite, &u.Calls, &u.PriceKnown)
		return u, err
	}, query, args...)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []core.UsageRow{}
	}
	return rows, nil
}
