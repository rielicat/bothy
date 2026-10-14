package httpapi

import (
	"context"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func (n *namer) runView(r *core.Run) *dto.RunView {
	return &dto.RunView{Run: *r, RequestedByName: n.requester(r), ChatTitle: n.chat(r.Session), ScheduleName: n.schedule(r.Schedule)}
}

func (n *namer) runViews(runs []*core.Run) []dto.RunView {
	out := make([]dto.RunView, 0, len(runs))
	for _, r := range runs {
		out = append(out, *n.runView(r))
	}
	return out
}

// nameApprovals fills DecidedByName.
func (n *namer) nameApprovals(aps ...*core.Approval) {
	for _, ap := range aps {
		if ap != nil && ap.DecidedBy != "" {
			ap.DecidedByName = n.user(ap.DecidedBy)
		}
	}
}

// usageRows names usage buckets: people, schedules and chats.
func (n *namer) usageRows(group string, rows []core.UsageRow) []dto.UsageRowView {
	out := make([]dto.UsageRowView, 0, len(rows))
	for _, row := range rows {
		v := dto.UsageRowView{UsageRow: row}
		switch group {
		case "user":
			v.Name = n.user(core.UserID(row.Key))
		case "schedule":
			v.Name = n.schedule(core.ScheduleID(row.Key))
		case "session":
			v.Name = n.chat(core.SessionID(row.Key))
		}
		out = append(out, v)
	}
	return out
}

// egressOutcome merges a row's decision and connection result: a refusal,
// an allowed connection that never opened, or one that reached the site.
func egressOutcome(e *core.EgressRecord) dto.EgressOutcome {
	switch {
	case e.Decision == "deny" || e.Decision == "held_deny":
		return dto.EgressBlocked
	case e.Reason == "connect failed":
		return dto.EgressAllowedUnreachable
	}
	return dto.EgressReached
}

func egressRows(rs []*core.EgressRecord) []dto.EgressRow {
	out := make([]dto.EgressRow, 0, len(rs))
	for _, e := range rs {
		out = append(out, dto.EgressRow{EgressRecord: *e, Outcome: egressOutcome(e)})
	}
	return out
}

// viewerFor is the viewer that names other people's chats and schedules on
// pages admins read for everyone.
func (s *Server) viewerFor(ctx context.Context, a core.Actor) (core.Viewer, error) {
	if !isAdmin(a) {
		return a.Viewer, nil
	}
	return s.adminViewer(ctx, a)
}
