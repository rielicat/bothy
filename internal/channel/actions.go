package channel

import (
	"cmp"
	"context"
	"errors"
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

// approvalPress resolves an approval from a chat card's button. Only team
// requests (a teammate's or a channel's) are decided in chat, by a teammate
// an admin marked "Can approve" whose membership is fresh; the person who
// asked may always cancel their own request. Approvals.Resolve checks all
// of it again from the store.
func (h *Hub) approvalPress(ctx context.Context, a *adapter, ev core.ActionEvent) string {
	id, choice, err := h.d.Approvals.ParseButtonData(ev.Data)
	if err != nil {
		return "This button is not valid."
	}
	mc, checks := checkerOf(a)
	if checks {
		// As before a message: when what membership means here changed (the
		// last channel of a server turned off, another workspace's token),
		// every verdict is stale now, not within the hour.
		h.syncScope(ctx, a, mc)
	}
	res, err := h.d.Auth.ResolveIdentity(ctx, a.name, ev.SenderID)
	if err != nil {
		return couldntRecord
	}
	ap, err := h.d.Approvals.Get(ctx, core.SystemViewer(), id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return "This button no longer works."
	case err != nil:
		return couldntRecord
	}
	refs, err := h.d.Approvals.MessageRefs(ctx, id)
	if err != nil {
		return couldntRecord
	}
	rec, ok := findCardMessage(refs, ev.Msg)
	if !ok {
		return couldntRecord
	}
	key := a.name + ":" + ev.SenderID
	msg := ev.Msg
	resolve := func(choice core.Choice) string {
		_, err := h.d.Approvals.Resolve(ctx, core.Resolution{ID: id, Choice: choice, ByIdentity: key, Via: a.name, ViaMsg: &msg})
		switch {
		case err == nil:
			return choiceToast(choice)
		case errors.Is(err, core.ErrConflict):
			return "Already decided."
		case errors.Is(err, core.ErrForbidden):
			if strings.Contains(err.Error(), channelAlways) {
				return channelAlways
			}
			return notApprover
		case errors.Is(err, core.ErrNotFound):
			return couldntRecord
		case errors.Is(err, core.ErrApprovalExpired):
			return "This request expired."
		}
		h.log.Error("channel: resolving approval failed", "approval", id, "err", err)
		return couldntRecord
	}
	if choice == core.Deny && key == ap.Approver.RequesterIdentity && res.Status != core.IdentityBlocked {
		if t := resolve(core.Deny); t != choiceToast(core.Deny) {
			return t
		}
		return "Request cancelled."
	}
	switch {
	case rec.Kind == core.CardMsgNotice:
		return "Only the person who asked can cancel this."
	case ap.Approver.AdminOnly || ap.Approver.Multi() || ap.Approver.User != "":
		return "Open in Bothy to approve."
	case !res.CanApprove || res.Status != core.IdentityTeammate:
		return notApprover
	}
	if checks && !h.fresh(res, core.MemberTTL) {
		ident, err := h.d.Store.Identities().Get(ctx, a.name, ev.SenderID)
		if err != nil {
			return couldntRecord
		}
		r, err := h.recheck(ctx, a, *ident, actionBudget)
		switch {
		case err == nil:
			res = r
		case !res.Admitted() || !h.fresh(res, core.ApproverStale):
			return "Bothy couldn't check that you can approve. Try again, or open it in Bothy."
		}
	}
	if !res.Admitted() || !res.CanApprove {
		return notApprover
	}
	if rec.Truncated && choice != core.Deny {
		return "Open in web to approve."
	}
	return resolve(choice)
}

const (
	notApprover   = "Only an approver can approve this."
	channelAlways = "Only an admin on the web can always allow this for a channel."
)

const couldntRecord = "Couldn't record your decision. Try again in Bothy on the web."

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

// stopPress handles the streamer's Stop button ("s:<run>"): the teammate
// whose chat it is, someone Bothy answers in the channel, or an approver.
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
	if err != nil || res.Status == core.IdentityBlocked {
		return couldntStop
	}
	p, err := h.d.Store.Principals().Get(ctx, core.SystemViewer(), info.Principal)
	if err != nil {
		return couldntStop
	}
	_, checks := checkerOf(a)
	ok := res.CanApprove && h.usable(a, res, core.MemberStale)
	switch p.Kind {
	case core.PrincipalPerson:
		ok = ok || res.Principal == p.ID && res.Admitted()
	case core.PrincipalSpace:
		// In a Telegram group anyone Bothy answers may stop its task.
		ok = ok || !checks || h.usable(a, res, core.MemberStale)
	}
	if !ok {
		return "Only the person this chat belongs to or someone who can approve can stop it."
	}
	name := ev.SenderID
	if id, err := h.d.Store.Identities().Get(ctx, a.name, ev.SenderID); err == nil {
		name = cmp.Or(strings.TrimSpace(id.DisplayName), id.Username, name)
	}
	sctx := chatOrigin(ctx, a, core.Inbound{SenderID: ev.SenderID, SenderName: name})
	stopped, err := h.d.Runner.Stop(sctx, core.SystemViewer(), info.Session, "")
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
