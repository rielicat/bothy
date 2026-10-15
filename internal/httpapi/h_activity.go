package httpapi

import (
	"errors"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

func (n *namer) runView(r *core.Run) *dto.RunView {
	return &dto.RunView{Run: *r, RequestedByName: n.requester(r), ChatTitle: n.chat(r.Session), ScheduleName: n.schedule(r.Schedule),
		Origin: n.runOrigin(r), Reason: n.runReason(r)}
}

// runOrigin is where a task came from: a schedule, the API, or the chat it
// ran in (web when the viewer cannot see that chat's app).
func (n *namer) runOrigin(r *core.Run) string {
	switch {
	case r.Trigger.Scheduled():
		return "schedule"
	case r.Trigger == core.TrigAPI:
		return "api"
	}
	if o := n.origin(r.Session); o != "" {
		return o
	}
	return "web"
}

// runReason says in plain words why a task stopped or failed, completing
// "Stopped …" or "Failed: …". The run's stored error is never shown as is:
// it can hold a provider's message.
func (n *namer) runReason(r *core.Run) string {
	switch r.Status {
	case core.RunStopped:
		switch {
		case r.StoppedBy != "" && r.StoppedBy == n.v.User:
			return "by you"
		case r.StoppedBy != "":
			if name := n.user(r.StoppedBy); name != "" {
				return "by " + name
			}
		case r.ParkedOn != "":
			return "while it waited for approval"
		}
	case core.RunFailed:
		return failureReason(r.Error)
	}
	return ""
}

// failureReason maps a run's error text to a plain clause.
func failureReason(e string) string {
	msg := strings.ToLower(e)
	switch {
	case msg == "":
		return ""
	case strings.HasPrefix(msg, "no model configured"):
		return "no AI model is set up"
	case strings.HasPrefix(msg, "could not load"):
		return "Bothy couldn't load the chat"
	case !strings.HasPrefix(msg, "model error"):
		return ""
	// Checked first: the message names the store, and often "access denied"
	// or a timeout of its own, which are about AWS, not the AI model.
	case strings.Contains(msg, "credential unavailable"):
		return "Bothy couldn't read its key for the AI model"
	case containsAny(msg, "context length", "context_length", "too many tokens", "prompt is too long", "maximum context"):
		return "the conversation was too long for the AI model"
	case containsAny(msg, "credit", "quota", "billing", "insufficient", "payment"):
		return "the AI model account is out of credit"
	case containsAny(msg, "rate limit", "rate_limit", "too many requests", "429", "throttl"):
		return "the AI model service was limiting requests"
	case containsAny(msg, "api key", "api_key", "unauthorized", "authentication", "401", "403", "permission", "forbidden", "access denied"):
		return "the AI model service didn't accept Bothy's key"
	}
	switch core.TransportFailure(errors.New(msg)) {
	case core.FailTimeout:
		return "the AI model didn't answer in time"
	case core.FailUnreachable, core.FailPrivateAddress, core.FailTLS:
		return "Bothy couldn't reach the AI model service"
	}
	if containsAny(msg, "overloaded", "unavailable", "500", "502", "503", "529", "internal server error", "server error") {
		return "the AI model service had a problem on its side"
	}
	return "the AI model returned an error"
}

// deployViews names who asked for and who approved each deploy.
func (n *namer) deployViews(ds []*core.Deploy) []dto.DeployView {
	out := make([]dto.DeployView, 0, len(ds))
	for _, d := range ds {
		v := dto.DeployView{Deploy: *d, RequestedByName: n.user(d.RequestedBy), ApprovedByNames: []string{}}
		for _, u := range d.ApprovedBy {
			if name := n.user(u); name != "" {
				v.ApprovedByNames = append(v.ApprovedByNames, name)
			}
		}
		if len(d.SHA) >= 7 {
			v.ShortSHA = d.SHA[:7]
		}
		out = append(out, v)
	}
	return out
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
