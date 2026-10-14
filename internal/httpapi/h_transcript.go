package httpapi

import (
	"context"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// annotateTools sets State and Approval on the tool_call parts of a
// transcript page and returns the approvals they asked for, decided ones
// included, oldest first: a call's own approval and the ones the egress
// proxy held its connections for (tool_call_id "<call>#net").
func (s *Server) annotateTools(ctx context.Context, v core.Viewer, msgs []*core.Message) ([]*core.Approval, error) {
	type key struct {
		run core.RunID
		id  string
	}
	results := map[key][]*core.ToolResult{}
	var runs []core.RunID
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch {
			case p.Type == core.PartToolResult && p.ToolResult != nil:
				k := key{m.Run, p.ToolResult.CallID}
				results[k] = append(results[k], p.ToolResult)
			case p.Type == core.PartToolCall && p.ToolCall != nil && m.Run != "" && !slices.Contains(runs, m.Run):
				runs = append(runs, m.Run)
			}
		}
	}
	type runInfo struct {
		open bool
		recs map[string][]*core.ToolCallRecord
		appr map[core.ApprovalID]*core.Approval
		held map[string][]*core.Approval // by the call record they were held for
	}
	infos := map[core.RunID]*runInfo{}
	var approvals []*core.Approval
	for _, id := range runs {
		run, err := s.d.Store.Runs().Get(ctx, v, id)
		if err != nil {
			continue // pruned by runs retention: parts keep no state
		}
		recs, err := s.d.Store.Runs().ToolCalls(ctx, v, id)
		if err != nil {
			return nil, err
		}
		aps, err := s.d.Store.Approvals().ByRun(ctx, id)
		if err != nil {
			return nil, err
		}
		ri := &runInfo{open: !run.Status.Terminal(), recs: map[string][]*core.ToolCallRecord{}, appr: map[core.ApprovalID]*core.Approval{},
			held: map[string][]*core.Approval{}}
		for _, rec := range recs {
			ri.recs[rec.CallID] = append(ri.recs[rec.CallID], rec)
		}
		for _, ap := range aps {
			if !v.Can(ap.Principal) {
				continue
			}
			ri.appr[ap.ID] = ap
			if call, ok := strings.CutSuffix(ap.ToolCallID, heldSuffix); ok {
				ri.held[call] = append(ri.held[call], ap)
			}
		}
		infos[id] = ri
	}
	seen := map[key]int{}
	for _, m := range msgs {
		ri := infos[m.Run]
		if ri == nil {
			continue
		}
		m.Parts = slices.Clone(m.Parts)
		for i, p := range m.Parts {
			if p.Type != core.PartToolCall || p.ToolCall == nil {
				continue
			}
			k := key{m.Run, p.ToolCall.ID}
			n := seen[k]
			seen[k]++
			var rec *core.ToolCallRecord
			if rs := ri.recs[p.ToolCall.ID]; n < len(rs) {
				rec = rs[n]
			}
			var res *core.ToolResult
			if rs := results[k]; n < len(rs) {
				res = rs[n]
			}
			var ap *core.Approval
			var held []*core.Approval
			if rec != nil {
				ap = ri.appr[rec.Approval]
				held = ri.held[rec.ID]
			}
			p.State = toolState(rec, ap, res, ri.open)
			if p.State == core.ToolRunning && slices.ContainsFunc(held, waiting) {
				p.State = core.ToolWaitingApproval
			}
			shown := ap
			if shown == nil && len(held) > 0 {
				shown = held[len(held)-1]
				if i := slices.IndexFunc(held, waiting); i >= 0 {
					shown = held[i]
				}
			}
			if shown != nil {
				p.Approval = shown.ID
			}
			for _, a := range append([]*core.Approval{ap}, held...) {
				if a != nil && !slices.Contains(approvals, a) {
					approvals = append(approvals, a)
				}
			}
			m.Parts[i] = p
		}
	}
	slices.SortStableFunc(approvals, func(a, b *core.Approval) int { return a.CreatedAt.Compare(b.CreatedAt) })
	if err := s.withVotes(ctx, approvals); err != nil {
		return nil, err
	}
	s.namer(ctx, v).nameApprovals(approvals...)
	return approvals, nil
}

// heldSuffix marks the tool_call_id of an approval the egress proxy asked
// for while a call's connection waited (egress/hold.go).
const heldSuffix = "#net"

func waiting(a *core.Approval) bool {
	return a.Status == core.ApprovalPending || a.Status == core.ApprovalParked
}

// toolState is where a call stands. The approval decides while it is open or
// when it said no; otherwise the call record does, and a call is done only
// once its result exists. A call of a finished run that never started
// (planned, or approved too late) did not happen; one that started and left
// neither record nor result never completed.
func toolState(rec *core.ToolCallRecord, ap *core.Approval, res *core.ToolResult, runOpen bool) core.ToolState {
	if ap != nil {
		switch ap.Status {
		case core.ApprovalPending, core.ApprovalParked:
			return core.ToolWaitingApproval
		case core.ApprovalDenied:
			return core.ToolDenied
		case core.ApprovalExpired, core.ApprovalCancelled:
			return core.ToolExpired
		}
	}
	if rec != nil {
		switch rec.Status {
		case core.TCOK:
			return core.ToolDone
		case core.TCError, core.TCTimeout, core.TCInvalid, core.TCStopped:
			return core.ToolFailed
		case core.TCDenied:
			if ap != nil {
				return core.ToolDenied
			}
			return core.ToolBlocked
		case core.TCWaiting, core.TCPlanned, core.TCApproved:
			switch {
			case !runOpen:
				return core.ToolExpired
			case rec.Status == core.TCWaiting && (ap == nil || ap.Status != core.ApprovalAllowed):
				return core.ToolWaitingApproval
			}
			return core.ToolRunning
		}
	}
	switch {
	case res != nil && res.IsError:
		return core.ToolFailed
	case res != nil:
		return core.ToolDone
	case runOpen:
		return core.ToolRunning
	}
	return core.ToolFailed
}
