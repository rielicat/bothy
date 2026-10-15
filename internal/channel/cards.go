package channel

import (
	"bytes"
	"context"
	"fmt"
	"slices"
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
	case h.webOnly(ctx, approver):
		// They answer in Bothy's Approvals page; a chat run still hears it waits.
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
		h.reply(ctx, *origin, plainLine("Waiting for "+h.approvers(ctx, ap)+" to approve "+cardIng(ap)+"."))
	}
}

// webOnly reports whether u keeps approval requests on Bothy's Approvals
// page (Settings → Account) instead of a chat app.
func (h *Hub) webOnly(ctx context.Context, u core.UserID) bool {
	usr, err := h.d.Store.Users().Get(ctx, u)
	return err == nil && usr.ApprovalsWebOnly
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

// cardQuestion is the card's plain question; cards stored before v0.2 have
// only a title.
func cardQuestion(ap *core.Approval) string {
	if ap.Card.Question != "" {
		return ap.Card.Question
	}
	return cardTitle(ap) + "?"
}

// cardIng names the action in a sentence: "deleting notes/a.md".
func cardIng(ap *core.Approval) string {
	if ap.Card.Ing != "" {
		return ap.Card.Ing
	}
	return "“" + cardTitle(ap) + "”"
}

// choiceLabel is a button's words: allow_once carries the card's verb.
func choiceLabel(c core.Card, ch core.Choice) string {
	switch ch {
	case core.AllowOnce:
		if c.PrimaryLabel != "" && c.PrimaryLabel != allowForChat {
			return c.PrimaryLabel
		}
		return "Allow"
	case core.AllowSession:
		return allowForChat
	case core.AllowAlways:
		return "Always allow"
	case core.Deny:
		return "Deny"
	}
	return ""
}

const allowForChat = "Allow for this chat"

// plainLine shows a sentence of card copy verbatim. Words a chat client
// would turn into a link (a file name like staging.md passes for a domain)
// go in code spans: one curious tap must not fetch anything.
func plainLine(s string) string {
	s = revealInvisible(s)
	if !core.LooksLikeLink(s) {
		return escapeMD(s)
	}
	words := strings.Split(s, " ")
	for i, w := range words {
		lead := strings.TrimLeft(w, "“(")
		word := strings.TrimRight(lead, "?.,:;!”)")
		head, tail := w[:len(w)-len(lead)], lead[len(word):]
		if word != "" && core.LooksLikeLink(word) {
			words[i] = escapeMD(head) + codeSpan(word) + escapeMD(tail)
		} else {
			words[i] = escapeMD(w)
		}
	}
	return strings.Join(words, " ")
}

// heading is a card's first line: the question in bold. Telegram cannot
// nest code in bold, so its renderer breaks the bold around a code span.
func heading(s string) string {
	return "**" + plainLine(s) + "**"
}

// chatCode is the code block a chat card shows. A command, a script or a
// file's content is the action itself and always shows. Elsewhere Code is
// the action's JSON (hashes, base commits): a built-in tool's fields already
// say what it does, so in a chat message the JSON stays behind the web UI's
// Details. MCP, admin-class and unknown tools keep it, since their fields
// show only some of the arguments, and so do cards from before v0.2.
func chatCode(ap *core.Approval) string {
	c, a := ap.Card, ap.Action
	if c.Code == "" || a == nil || c.Question == "" || c.Risk == core.SEAdmin {
		return c.Code
	}
	switch a.Kind {
	case core.ActExec, core.ActMCP, core.ActGeneric:
		return c.Code
	}
	code, err := core.CanonicalJSON([]byte(c.Code))
	payload, perr := core.CanonicalJSON(a.Payload)
	if err != nil || perr != nil || !bytes.Equal(code, payload) {
		return c.Code
	}
	return ""
}

// renderCard formats the policy-rendered Card for chat in the words of the
// web card (question, consequence, warnings, fields, the verb on the
// button). Every value is shown verbatim (escaped, in code where it is code)
// with invisible characters made visible. A card that does not fit, has a
// diff too long to show, or goes to a channel without buttons is truncated:
// Deny plus a link to the web UI.
func (h *Hub) renderCard(ap *core.Approval, caps core.ChannelCaps) (core.OutMessage, bool) {
	c := ap.Card
	header := heading(cardQuestion(ap))
	lines := []string{header}
	if c.Consequence != "" {
		lines = append(lines, plainLine(c.Consequence))
	}
	if ap.Approver.Multi() && ap.Requester != nil {
		lines = append(lines, plainLine(ap.Requester.Name+" asked."))
	}
	for _, w := range c.Warnings {
		lines = append(lines, "⚠ "+plainLine(w))
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
	if ap.Approver.Multi() {
		lines = append(lines, h.approvalLines(ap)...)
	}
	if code := chatCode(ap); code != "" {
		lines = append(lines, codeBlock("", revealInvisible(code)))
	}
	diff := c.Diff
	if c.Kind == core.CardPullRequest {
		// The pull request's diff opens with its own repository, branch,
		// title and body; the card's fields already say those.
		if i := strings.Index(diff, "diff --git "); i > 0 {
			diff = diff[i:]
		}
	}
	diffCut := len(diff) > maxChatDiff
	if diff != "" && !diffCut {
		lines = append(lines, codeBlock("diff", revealInvisible(diff)))
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
	primary := core.AllowOnce
	if !slices.Contains(c.Choices, core.AllowOnce) {
		primary = core.AllowSession
	}
	for _, ch := range c.Choices {
		label := choiceLabel(c, ch)
		if label == "" {
			continue
		}
		// Deny is never red: the danger fill marks what can't be undone.
		b := core.Button{Label: label, Data: h.d.Approvals.ButtonData(ap.ID, ch)}
		if ch == primary {
			b.Style = core.BtnPrimary
			if c.Tone == core.ToneDestructive {
				b.Style = core.BtnDanger
			}
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
		msg.Buttons = [][]core.Button{{{Label: choiceLabel(c, core.Deny), Data: h.d.Approvals.ButtonData(ap.ID, core.Deny)}}}
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

// contextLine is the card's italic context line; a link-like word in it,
// such as a file name in the chat's title, is a code span.
func contextLine(ctx string) string {
	return "_" + plainLine(ctx) + "_"
}

// expiresText is the card's expiry. A short wait is a countdown in the
// web's words ("in less than a minute", "in 14 min"); a longer one is a
// clock time, because a card is drawn once and a countdown would go stale.
func (h *Hub) expiresText(t time.Time) string {
	d := t.Sub(h.clock.Now())
	if d < time.Minute {
		return "in less than a minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("in %d min", d.Round(time.Minute)/time.Minute)
	}
	loc, err := time.LoadLocation(h.cfg().Timezone)
	if err != nil {
		loc = time.UTC
	}
	return "at " + t.In(loc).Format("15:04 MST on Jan 2")
}

// updateCards edits every delivered card of a resolved approval to its
// outcome and removes the buttons. Each card is its approver's DM, so the
// outcome names its reader "You".
func (h *Hub) updateCards(ctx context.Context, d core.ApprovalResolvedData) {
	refs, err := h.d.Approvals.MessageRefs(ctx, d.Approval)
	if err != nil || len(refs) == 0 {
		return
	}
	ap, err := h.d.Approvals.Get(ctx, core.SystemViewer(), d.Approval)
	if err != nil {
		ap = &core.Approval{ID: d.Approval, Card: core.Card{Title: "Approval"}}
	}
	for _, r := range refs {
		a := h.live(r.Ref.Chat.Channel)
		if a == nil {
			continue
		}
		// Cards only go to DMs, and a stored card ref keeps no chat kind.
		dm := r.Ref.Chat
		dm.Kind = core.ChatDM
		reader := func(u core.UserID) bool { return u != "" && h.isDMOf(ctx, u, dm) }
		text := h.redact(plainLine(h.outcomeText(ctx, d, ap, reader)))
		if err := a.ch.Edit(ctx, r.Ref, core.OutMessage{Chat: r.Ref.Chat, Text: text}); err != nil {
			h.log.Warn("channel: updating approval card failed", "approval", d.Approval, "err", err)
		}
	}
}

// outcomeText is the decided line a card becomes in place of its question
// (docs/design/ui.md §8.4), e.g. "✓ You approved deleting notes/a.md." or
// "✗ Sam Patel denied deleting notes/a.md on the web."; reader tells which
// user reads the card. It is plain text; the caller formats it.
func (h *Hub) outcomeText(ctx context.Context, d core.ApprovalResolvedData, ap *core.Approval, reader func(core.UserID) bool) string {
	ing := cardIng(ap)
	who, where := "Someone", viaText(d.Via)
	switch {
	case reader(d.By):
		who, where = "You", ""
	case d.By != "":
		who = h.userName(ctx, d.By)
	}
	switch d.Status {
	case core.ApprovalAllowed:
		if ap.Approver.Multi() {
			if names := h.allowedBy(ctx, ap, reader); names != "" {
				return "✓ " + names + " approved " + ing + "."
			}
		}
		switch d.Choice {
		case core.AllowSession:
			return "✓ " + who + " approved " + ing + where + ", and similar requests in this chat."
		case core.AllowAlways:
			return "✓ " + who + " approved " + ing + where + ", and similar requests from now on."
		}
		return "✓ " + who + " approved " + ing + where + "."
	case core.ApprovalDenied:
		s := "✗ " + who + " denied " + ing + where + "."
		// The requester's deny is how they withdraw a multi-approver request.
		if ap.Approver.Multi() && d.By != "" && d.By == ap.Approver.Requester {
			s = "✗ Cancelled by " + who + where + "."
			if who == "You" {
				s = "✗ You cancelled the request" + where + "."
			}
		}
		if c := oneLine(d.Comment); c != "" {
			s += " “" + c + "”"
		}
		return s
	case core.ApprovalExpired:
		return "✗ Nobody answered in time, so Bothy didn't go ahead."
	case core.ApprovalCancelled:
		return "Cancelled when the chat was stopped."
	}
	return string(d.Status)
}

func viaText(via string) string {
	switch via {
	case "web":
		return " on the web"
	case "telegram", "discord", "slack":
		return " in " + channelTitle(via)
	}
	return ""
}
