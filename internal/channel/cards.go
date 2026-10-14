package channel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// maxChatDiff is how much of a diff a chat card shows (SPEC §5.9); a longer
// diff makes the card truncated, so it can only be denied from chat.
const maxChatDiff = 3 << 10

func (h *Hub) onBusEvent(e core.Event) {
	switch d := e.Data.(type) {
	case core.ApprovalData:
		ap := d.Approval
		if ap == nil {
			return
		}
		switch e.Type {
		case core.EvApprovalRequested:
			// AdminOnly and multi-approver approvals are published more than
			// once (one copy per audience principal).
			if h.markDelivered(ap.ID) {
				h.enqueueChat(cardQueue(ap.ID), func(ctx context.Context) { h.deliverCard(ctx, ap) })
			}
		case core.EvApprovalVoted:
			if h.markResolved(ap.ID, core.ApprovalStatus(fmt.Sprintf("votes:%d", len(ap.Votes)))) {
				h.enqueueChat(cardQueue(ap.ID), func(ctx context.Context) { h.refreshCards(ctx, ap) })
			}
		}
	case core.ApprovalResolvedData:
		if d.Status == core.ApprovalPending || d.Status == core.ApprovalParked {
			return
		}
		if h.markResolved(d.Approval, d.Status) {
			h.enqueueChat(cardQueue(d.Approval), func(ctx context.Context) { h.updateCards(ctx, d) })
		}
	}
}

// cardQueue serializes delivery and outcome edits of one approval, so a
// quick resolution never overtakes the card it edits.
func cardQueue(id core.ApprovalID) string { return "approval:" + string(id) }

func (h *Hub) markDelivered(id core.ApprovalID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.delivered[id]; ok {
		return false
	}
	h.delivered[id] = time.Now()
	return true
}

func (h *Hub) markResolved(id core.ApprovalID, st core.ApprovalStatus) bool {
	key := string(id) + ":" + string(st)
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.resolved[key]; ok {
		return false
	}
	h.resolved[key] = time.Now()
	return true
}

// resyncCards delivers pending approvals that have no card message yet: ones
// created before the hub ran, or missed when the bus subscription overflowed.
func (h *Hub) resyncCards() {
	pending, err := h.d.Approvals.Pending(h.ctx, core.SystemViewer())
	if err != nil {
		h.log.Error("channel: listing pending approvals failed", "err", err)
		return
	}
	for _, ap := range pending {
		if ap.Status != core.ApprovalPending {
			continue
		}
		refs, err := h.d.Approvals.MessageRefs(h.ctx, ap.ID)
		if err != nil || len(refs) > 0 || !h.markDelivered(ap.ID) {
			continue
		}
		h.enqueueChat(cardQueue(ap.ID), func(ctx context.Context) { h.deliverCard(ctx, ap) })
	}
}

// deliverCard routes a card per SPEC §5.9: the approver's own DM when the
// run started there, else the approver's preferred DM (plus a waiting line in
// the originating group), plus policy.approval.notify channels for runs that
// did not start in a chat. Cards never go to a group.
func (h *Hub) deliverCard(ctx context.Context, ap *core.Approval) {
	if ap.Approver.Multi() {
		h.deliverVoteCards(ctx, ap)
		return
	}
	approver := ap.Approver.User
	if ap.Approver.AdminOnly || approver == "" {
		owner, ok := h.instanceOwner(ctx)
		if !ok {
			return
		}
		approver = owner.ID
	}
	var targets []core.ChatRef
	add := func(c core.ChatRef) {
		for _, t := range targets {
			if t.Key() == c.Key() {
				return
			}
		}
		targets = append(targets, c)
	}
	origin := ap.Chat
	fromChat := origin != nil && h.live(origin.Channel) != nil
	switch {
	case fromChat && h.isDMOf(ctx, approver, *origin):
		add(*origin)
	case fromChat:
		if c, ok := h.PreferredChat(ctx, approver); ok {
			add(c)
		}
	default:
		if c, ok := h.PreferredChat(ctx, approver); ok {
			add(c)
		}
		for _, name := range h.cfg().Policy.Approval.Notify {
			if c, ok := h.dmOn(ctx, approver, name); ok {
				add(c)
			}
		}
	}
	for _, t := range targets {
		h.sendCard(ctx, ap, t)
	}
	if fromChat && (len(targets) == 0 || targets[0].Key() != origin.Key()) {
		h.reply(ctx, *origin, "Waiting for the owner to approve “"+escapeMD(revealInvisible(cardTitle(ap)))+"”.")
	}
}

func (h *Hub) sendCard(ctx context.Context, ap *core.Approval, chat core.ChatRef) {
	a := h.live(chat.Channel)
	if a == nil {
		return
	}
	msg, truncated := h.renderCard(ap, a.ch.Caps())
	msg.Chat = chat
	ref, err := a.ch.Send(ctx, msg)
	if err != nil {
		h.log.Warn("channel: sending approval card failed", "approval", ap.ID, "chat", chat.Key(), "err", err)
		return
	}
	if err := h.d.Approvals.AddMessageRef(ctx, ap.ID, ref, truncated); err != nil {
		h.log.Error("channel: recording approval card failed", "approval", ap.ID, "err", err)
	}
}

func cardTitle(ap *core.Approval) string {
	if ap.Card.Title != "" {
		return ap.Card.Title
	}
	if ap.Action != nil {
		return ap.Action.Tool
	}
	return "Approval"
}

var choiceLabels = map[core.Choice]string{
	core.AllowOnce: "Allow once", core.AllowSession: "Allow for session", core.AllowAlways: "Always allow", core.Deny: "Deny",
}

// renderCard formats the policy-rendered Card for chat. Every value is shown
// verbatim (escaped, in code where it is code) with invisible characters made
// visible. A card that does not fit, has a diff too long to show, or goes to
// a channel without buttons is truncated: Deny plus a link to the web UI.
func (h *Hub) renderCard(ap *core.Approval, caps core.ChannelCaps) (core.OutMessage, bool) {
	c := ap.Card
	header := "🔐 **Approval needed: " + escapeMD(revealInvisible(cardTitle(ap))) + "**"
	lines := []string{header}
	if c.Risk.Valid() {
		lines = append(lines, "Risk: "+escapeMD(c.Risk.String()))
	}
	if ap.Approver.Multi() {
		lines = append(lines, "**"+approvalProgress(ap)+"**")
	}
	for _, f := range c.Fields {
		v := revealInvisible(f.Value)
		label := "**" + escapeMD(revealInvisible(f.Label)) + ":**"
		switch {
		case strings.Contains(v, "\n"):
			lines = append(lines, label+"\n"+codeBlock("", v))
		case f.Mono:
			lines = append(lines, label+" "+codeSpan(v))
		default:
			lines = append(lines, label+" "+inert(v))
		}
	}
	if c.Code != "" {
		lines = append(lines, codeBlock("", revealInvisible(c.Code)))
	}
	diffCut := len(c.Diff) > maxChatDiff
	if c.Diff != "" && !diffCut {
		lines = append(lines, codeBlock("diff", revealInvisible(c.Diff)))
	}
	for _, w := range c.Warnings {
		lines = append(lines, "⚠️ "+inert(revealInvisible(w)))
	}
	if c.Context != "" {
		lines = append(lines, contextLine(c.Context))
	}
	if !c.Expires.IsZero() {
		lines = append(lines, "Expires "+h.expiresText(c.Expires)+".")
	}
	text := h.redact(strings.Join(lines, "\n"))
	web := ""
	if isHTTPURL(c.WebURL) {
		web = c.WebURL
	}
	var choiceRow []core.Button
	for _, ch := range c.Choices {
		label, ok := choiceLabels[ch]
		if !ok {
			continue
		}
		b := core.Button{Label: label, Data: h.d.Approvals.ButtonData(ap.ID, ch)}
		switch ch {
		case core.AllowOnce:
			b.Style = core.BtnPrimary
		case core.Deny:
			b.Style = core.BtnDanger
		}
		choiceRow = append(choiceRow, b)
	}
	if caps.Buttons && !diffCut && len(choiceRow) > 0 && ulen(text) <= caps.MaxLen {
		buttons := [][]core.Button{choiceRow}
		if web != "" {
			buttons = append(buttons, []core.Button{{Label: "Open in web", URL: web}})
		}
		return core.OutMessage{Text: text, Buttons: buttons}, false
	}

	short := []string{header}
	if c.Context != "" {
		short = append(short, contextLine(c.Context))
	}
	switch {
	case !caps.Buttons && web != "":
		short = append(short, "Review it in the web UI: "+web)
	case !caps.Buttons:
		short = append(short, "Review it in Bothy's web UI → Approvals.")
	case web != "":
		short = append(short, "This request is too long to show here. Open it in the web UI to approve.")
	default:
		short = append(short, "This request is too long to show here. Open Bothy's web UI → Approvals to approve.")
	}
	msg := core.OutMessage{Text: h.redact(strings.Join(short, "\n"))}
	if caps.Buttons {
		msg.Buttons = [][]core.Button{{{Label: choiceLabels[core.Deny], Data: h.d.Approvals.ButtonData(ap.ID, core.Deny), Style: core.BtnDanger}}}
		if web != "" {
			msg.Buttons = append(msg.Buttons, []core.Button{{Label: "Open in web to approve", URL: web}})
		}
	}
	return msg, true
}

// inert shows a card value verbatim. A value a chat client would turn into
// a link goes in a code span instead: the URL an action is about to fetch may
// carry the data it would leak, and one curious tap on the card must not
// send it before the approver has decided.
func inert(v string) string {
	if core.LooksLikeLink(v) {
		return codeSpan(v)
	}
	return escapeMD(v)
}

// contextLine is the card's italic context line; italics cannot hold a code
// span on every platform, so a link-like context is shown as code alone.
func contextLine(ctx string) string {
	ctx = revealInvisible(ctx)
	if core.LooksLikeLink(ctx) {
		return codeSpan(ctx)
	}
	return "_" + escapeMD(ctx) + "_"
}

func (h *Hub) expiresText(t time.Time) string {
	d := t.Sub(h.clock.Now())
	switch {
	case d <= 0:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("in %d min", int((d+time.Minute-1)/time.Minute))
	}
	loc, err := time.LoadLocation(h.cfg().Timezone)
	if err != nil {
		loc = time.UTC
	}
	return "at " + t.In(loc).Format("Jan 2 15:04 MST")
}

// updateCards edits every delivered card of a resolved approval to its
// outcome and removes the buttons.
func (h *Hub) updateCards(ctx context.Context, d core.ApprovalResolvedData) {
	refs, err := h.d.Approvals.MessageRefs(ctx, d.Approval)
	if err != nil || len(refs) == 0 {
		return
	}
	title := "Approval"
	ap, err := h.d.Approvals.Get(ctx, core.SystemViewer(), d.Approval)
	if err == nil {
		title = cardTitle(ap)
	}
	text := h.redact("🔐 **" + escapeMD(revealInvisible(title)) + "**\n" + h.outcomeText(ctx, d, ap))
	for _, r := range refs {
		a := h.live(r.Ref.Chat.Channel)
		if a == nil {
			continue
		}
		if err := a.ch.Edit(ctx, r.Ref, core.OutMessage{Chat: r.Ref.Chat, Text: text}); err != nil {
			h.log.Warn("channel: updating approval card failed", "approval", d.Approval, "err", err)
		}
	}
}

func (h *Hub) outcomeText(ctx context.Context, d core.ApprovalResolvedData, ap *core.Approval) string {
	var s string
	switch d.Status {
	case core.ApprovalAllowed:
		if ap != nil && ap.Approver.Multi() {
			if names := h.allowedBy(ctx, ap); names != "" {
				return "✅ Approved by " + names + "."
			}
		}
		s = "✅ " + choiceToast(d.Choice)
		if d.Choice == core.AllowOnce {
			s = "✅ Allowed once"
		}
	case core.ApprovalDenied:
		s = "❌ Denied"
	case core.ApprovalExpired:
		return "⌛ Expired without an answer, so it was not allowed."
	case core.ApprovalCancelled:
		return "Cancelled: the task was stopped."
	default:
		return escapeMD(string(d.Status))
	}
	if d.By != "" {
		s += " by " + escapeMD(h.userName(ctx, d.By))
	}
	if d.Via != "" {
		s += " (" + escapeMD(d.Via) + ")"
	}
	return s + "."
}
