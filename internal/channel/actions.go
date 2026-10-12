package channel

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

// actionBudget leaves room for the adapter's Ack inside the 3 s platforms
// allow for answering a button press.
const actionBudget = 2500 * time.Millisecond

func (h *Hub) handleAction(ctx context.Context, a *adapter, ev core.ActionEvent) {
	ev.Chat.Channel = a.name
	if ev.Msg.Chat.Channel == "" {
		ev.Msg.Chat = ev.Chat
	}
	pctx, cancel := context.WithTimeout(ctx, actionBudget)
	toast := h.action(pctx, a, ev)
	cancel()
	actx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := a.ch.Ack(actx, ev, toast); err != nil {
		h.log.Warn("channel: acknowledging button press failed", "channel", a.name, "err", err)
	}
}

func (h *Hub) action(ctx context.Context, a *adapter, ev core.ActionEvent) string {
	switch {
	case strings.HasPrefix(ev.Data, "a:"):
		return h.approvalPress(ctx, a, ev)
	case strings.HasPrefix(ev.Data, "s:"):
		return h.stopPress(ctx, a, ev)
	}
	return "This button no longer works."
}

// approvalPress resolves an approval from a card button. Besides the MAC,
// Approvals.Resolve checks authority and the card message; the hub checks
// both too so a non-approver gets a clear toast whatever the backend.
func (h *Hub) approvalPress(ctx context.Context, a *adapter, ev core.ActionEvent) string {
	id, choice, err := h.d.Approvals.ParseButtonData(ev.Data)
	if err != nil {
		return "This button is not valid."
	}
	res, err := h.d.Auth.ResolveIdentity(ctx, a.name, ev.SenderID)
	if err != nil || res.Status != core.IdentityLinked || res.User == "" {
		return "Not allowed."
	}
	ap, err := h.d.Approvals.Get(ctx, core.SystemViewer(), id)
	if err != nil {
		return "Not allowed."
	}
	if !canApprove(ap.Approver, res) {
		return "Not allowed."
	}
	refs, err := h.d.Approvals.MessageRefs(ctx, id)
	if err != nil {
		return "Not allowed."
	}
	rec, ok := findCardMessage(refs, ev.Msg)
	if !ok {
		return "Not allowed."
	}
	if rec.Truncated && choice != core.Deny {
		return "Open in web to approve."
	}
	msg := ev.Msg
	_, err = h.d.Approvals.Resolve(ctx, core.Resolution{ID: id, Choice: choice, By: res.User, ByRole: res.Role, Via: a.name, ViaMsg: &msg})
	switch {
	case err == nil:
		return choiceToast(choice)
	case errors.Is(err, core.ErrConflict):
		return "Already decided."
	case errors.Is(err, core.ErrForbidden), errors.Is(err, core.ErrNotFound):
		return "Not allowed."
	case errors.Is(err, core.ErrApprovalExpired):
		return "This request expired."
	}
	h.log.Error("channel: resolving approval failed", "approval", id, "err", err)
	return "Could not record that; use the web UI."
}

func canApprove(req core.ApproverReq, res core.IdentityResolution) bool {
	if req.AdminOnly {
		return res.Role.IsAdmin()
	}
	return req.User != "" && req.User == res.User
}

func findCardMessage(refs []core.CardMessage, msg core.MsgRef) (core.CardMessage, bool) {
	for _, r := range refs {
		if r.Ref.ID == msg.ID && r.Ref.Chat.Channel == msg.Chat.Channel && r.Ref.Chat.ChatID == msg.Chat.ChatID {
			return r, true
		}
	}
	return core.CardMessage{}, false
}

func choiceToast(c core.Choice) string {
	switch c {
	case core.AllowOnce:
		return "Approved"
	case core.AllowSession:
		return "Allowed for this session"
	case core.AllowAlways:
		return "Always allowed"
	}
	return "Denied"
}

// stopPress handles the streamer's Stop button ("s:<run>"), authorized
// exactly like /stop.
func (h *Hub) stopPress(ctx context.Context, a *adapter, ev core.ActionEvent) string {
	run := core.RunID(strings.TrimPrefix(ev.Data, "s:"))
	if !core.ValidID(core.PrefixRun, string(run)) {
		return "This button is not valid."
	}
	var info *core.RunInfo
	for _, ri := range h.d.Runner.Active() {
		if ri.Run == run {
			info = &ri
			break
		}
	}
	if info == nil {
		return "Already finished."
	}
	res, err := h.d.Auth.ResolveIdentity(ctx, a.name, ev.SenderID)
	if err != nil {
		return "Not allowed."
	}
	p, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), info.Principal)
	if err != nil || !allowed(accessOwner, res, p) {
		return "Not allowed."
	}
	stopped, err := h.d.Runner.Stop(ctx, core.SystemViewer(), info.Session, res.User)
	if err != nil {
		h.log.Error("channel: stop button failed", "run", run, "err", err)
		return "Could not stop; try /stop."
	}
	if !stopped {
		return "Already finished."
	}
	return "Stopped."
}
