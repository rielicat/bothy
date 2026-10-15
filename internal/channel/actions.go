package channel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// actionBudget leaves room for the adapter's Ack inside the 3 s platforms
// allow for answering a button press.
const actionBudget = 2500 * time.Millisecond

func (h *Hub) handleAction(ctx context.Context, a *adapter, ev core.ActionEvent) {
	ev.Chat.Channel = a.name
	if ev.Msg.Chat.ChatID == "" {
		ev.Msg.Chat = ev.Chat
	}
	// A press can only be on this adapter's own message; never let it match
	// a card recorded on another channel.
	ev.Msg.Chat.Channel = a.name
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
	switch {
	case err != nil:
		return couldntRecord
	case res.Status != core.IdentityLinked || res.User == "":
		return "Link this account first: send /pair with a code from Settings → Account."
	}
	ap, err := h.d.Approvals.Get(ctx, core.SystemViewer(), id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return "This button no longer works."
	case err != nil:
		return couldntRecord
	}
	if ap.Approver.Multi() && res.User == ap.Approver.Requester && choice != core.Deny {
		return "You asked for this, so someone else has to approve it."
	}
	if !canApprove(ap.Approver, res, choice) {
		return "Only " + h.approvers(ctx, ap) + " can approve this."
	}
	if slices.ContainsFunc(ap.Votes, func(v core.ApprovalVote) bool { return v.User == res.User }) {
		return "You already voted."
	}
	refs, err := h.d.Approvals.MessageRefs(ctx, id)
	if err != nil {
		return couldntRecord
	}
	rec, ok := findCardMessage(refs, ev.Msg)
	if !ok {
		return couldntRecord
	}
	if rec.Truncated && choice != core.Deny {
		return "Open in web to approve."
	}
	msg := ev.Msg
	got, err := h.d.Approvals.Resolve(ctx, core.Resolution{ID: id, Choice: choice, By: res.User, ByRole: res.Role, Via: a.name, ViaMsg: &msg})
	switch {
	case err == nil && got != nil && got.Approver.Multi() && got.Status == core.ApprovalPending:
		return votedToast(got)
	case err == nil:
		return choiceToast(choice)
	case errors.Is(err, core.ErrConflict):
		return "Already decided."
	case errors.Is(err, core.ErrForbidden):
		return "Only " + h.approvers(ctx, ap) + " can approve this."
	case errors.Is(err, core.ErrNotFound):
		return couldntRecord
	case errors.Is(err, core.ErrApprovalExpired):
		return "This request expired."
	}
	h.log.Error("channel: resolving approval failed", "approval", id, "err", err)
	return couldntRecord
}

const couldntRecord = "Couldn't record your decision. Try again in Bothy on the web."

// votedToast answers an approve vote that did not settle a multi-approver
// request (docs/design/ui.md §8.5).
func votedToast(ap *core.Approval) string {
	switch left := ap.Approver.Needed() - core.AllowVotes(ap.Votes, ap.Approver); {
	case left == 1:
		return "You approved. Waiting for one more."
	case left > 1:
		return fmt.Sprintf("You approved. Waiting for %d more.", left)
	}
	return "You approved."
}

// approvers names who may approve ap in a sentence: "Maya Chen", "an
// admin", "Maya Chen, Priya Rao or any admin".
func (h *Hub) approvers(ctx context.Context, ap *core.Approval) string {
	req := ap.Approver
	switch {
	case req.Multi():
		var names []string
		if t := ap.Approvals; t != nil {
			for _, p := range t.Eligible {
				names = append(names, p.Name)
			}
			if t.AnyAdmin && len(names) > 0 {
				names = append(names, "any admin")
			}
		}
		if len(names) == 0 {
			return "an admin"
		}
		return orList(names)
	case req.AdminOnly || req.User == "":
		return "an admin"
	}
	return h.userName(ctx, req.User)
}

func canApprove(req core.ApproverReq, res core.IdentityResolution, choice core.Choice) bool {
	switch {
	case req.Multi():
		return req.Eligible(res.User, res.Role) || choice == core.Deny && res.User != "" && res.User == req.Requester
	case req.AdminOnly:
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
		return "Approved."
	case core.AllowSession:
		return "Allowed for this chat."
	case core.AllowAlways:
		return "Always allowed."
	}
	return "Denied."
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
		return couldntStop
	}
	p, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), info.Principal)
	if err != nil {
		return couldntStop
	}
	if !allowed(accessOwner, res, p) {
		return "Only the person this chat belongs to or an admin can stop it."
	}
	stopped, err := h.d.Runner.Stop(ctx, core.SystemViewer(), info.Session, res.User)
	if err != nil {
		h.log.Error("channel: stop button failed", "run", run, "err", err)
		return couldntStop
	}
	if !stopped {
		return "Already finished."
	}
	return "Stopped."
}

const couldntStop = "Couldn't stop it. Try /stop."
