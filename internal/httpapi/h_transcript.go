package httpapi

import (
	"context"
	"slices"

	"github.com/rielicat/bothy/internal/core"
)

// annotateTools sets State and Approval on the tool_call parts of a
// transcript page and returns the approvals they asked for, decided ones
// included, oldest first.
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
		ri := &runInfo{open: !run.Status.Terminal(), recs: map[string][]*core.ToolCallRecord{}, appr: map[core.ApprovalID]*core.Approval{}}
		for _, rec := range recs {
			ri.recs[rec.CallID] = append(ri.recs[rec.CallID], rec)
		}
		for _, ap := range aps {
			if v.Can(ap.Principal) {
				ri.appr[ap.ID] = ap
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
			if rec != nil && rec.Approval != "" {
				ap = ri.appr[rec.Approval]
			}
			p.State = toolState(rec, ap, res, ri.open)
			if ap != nil {
				p.Approval = ap.ID
				if !slices.Contains(approvals, ap) {
					approvals = append(approvals, ap)
				}
			}
			m.Parts[i] = p
		}
	}
	if err := s.withVotes(ctx, approvals); err != nil {
		return nil, err
	}
	s.namer(ctx, v).nameApprovals(approvals...)
	return approvals, nil
}

// toolState is where a call stands. The approval decides while it is open or
// when it said no; otherwise the call record does, and a call is done only
// once its result exists. A call of a finished run with neither record
// nor result never completed.
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
		case core.TCWaiting:
			if runOpen {
				return core.ToolWaitingApproval
			}
			return core.ToolExpired
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
